package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unicode"

	"pomeforge.local/pomeforge/internal/pomeforge"
)

const quickstartHelp = `Usage: pomeforge quickstart [--workspace PATH] [--xcode PATH] [--bundle-id ID] [--skip-device]
Guided Linux terminal setup: install the pinned Linux runtime/tools, import your Apple
SDK, create or resume HelloWorld, then optionally authenticate and install on USB.
Defaults: ~/PomeforgeProjects/HelloWorld; a unique bundle ID is generated once.
Every download, SDK import, build and account/device operation requires your choice.
Existing projects and SDKs are preserved. --skip-device stops after the local build.
Requires terminal stdin, stdout and stderr. --json fails with interactive_required
without prompting or changing files. Agents should use schema/tools/sdk/init/plan/run.
Apple credentials are entered directly into xtool and are never captured by Pomeforge.
xtool controls its own private authentication storage. Certificate revocation is never
automatically approved. Installation does not prove that the app opened on a device.`

type quickstartOptions struct {
	workspace, xcode, bundleID string
	skipDevice                 bool
}

type quickstartBackend interface {
	tool(context.Context, string) pomeforge.ToolStatus
	sdk(context.Context) pomeforge.ToolStatus
	plan(context.Context, pomeforge.PlanInput, bool) (pomeforge.Plan, error)
	run(context.Context, string) (pomeforge.OperationResult, error)
	environment() []string
}

type quickstartService struct{ service *pomeforge.Service }

func (s quickstartService) tool(ctx context.Context, id string) pomeforge.ToolStatus {
	return s.service.Tools.Probe(ctx, id)
}
func (s quickstartService) sdk(ctx context.Context) pomeforge.ToolStatus {
	return s.service.Setup.ProbeSDK(ctx)
}
func (s quickstartService) plan(ctx context.Context, input pomeforge.PlanInput, store bool) (pomeforge.Plan, error) {
	return s.service.PlanOperation(ctx, input, store)
}
func (s quickstartService) run(ctx context.Context, id string) (pomeforge.OperationResult, error) {
	return s.service.RunStored(ctx, id, false)
}
func (s quickstartService) environment() []string {
	env := pomeforge.ChildEnvironment()
	for index, entry := range env {
		if strings.HasPrefix(entry, "XDG_CONFIG_HOME=") {
			env[index] = "XDG_CONFIG_HOME=" + s.service.Setup.XDGConfigHome
			return env
		}
	}
	return append(env, "XDG_CONFIG_HOME="+s.service.Setup.XDGConfigHome)
}

type quickstartProcesses interface {
	terminal(context.Context, string, []string, string, []string) error
	capture(context.Context, time.Duration, string, []string, string, []string) (string, error)
}

// Only terminal() is used for authentication and installation. Their output is
// never copied to an OperationResult, receipt, buffer, or retained log.
type quickstartCommands struct {
	input          io.Reader
	output, errors io.Writer
}

func (p quickstartCommands) terminal(ctx context.Context, executable string, args []string, directory string, env []string) error {
	command := exec.CommandContext(ctx, executable, args...)
	command.Dir, command.Env = directory, env
	command.Stdin, command.Stdout, command.Stderr = p.input, p.output, p.errors
	command.WaitDelay = 2 * time.Second
	// Keep the foreground terminal process group so upstream password and 2FA
	// prompts can read the terminal. Ctrl-C also reaches that foreground group.
	return quickstartCommandError(ctx, command.Run())
}

func (p quickstartCommands) capture(ctx context.Context, timeout time.Duration, executable string, args []string, directory string, env []string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	command := exec.CommandContext(ctx, executable, args...)
	command.Dir, command.Env = directory, env
	var output quickstartOutput
	command.Stdout = &output
	// Device enumeration errors can contain identifiers. Keep them out of
	// returned errors and history; the wizard gives concrete troubleshooting.
	command.Stderr = io.Discard
	command.WaitDelay = 2 * time.Second
	if err := quickstartCommandError(ctx, command.Run()); err != nil {
		return "", err
	}
	if output.overflow {
		return "", pomeforge.Errorf("device_output_invalid", "device output exceeded the 64 KiB limit")
	}
	return output.text.String(), nil
}

type quickstartOutput struct {
	text     strings.Builder
	overflow bool
}

func (b *quickstartOutput) Write(data []byte) (int, error) {
	remaining := (64 << 10) - b.text.Len()
	if len(data) > remaining {
		b.text.Write(data[:remaining])
		b.overflow = true
	} else {
		b.text.Write(data)
	}
	return len(data), nil
}

func quickstartCommandError(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if err == nil {
		return nil
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		return pomeforge.Errorf("child_failed", fmt.Sprintf("interactive tool exited with status %d; setup can be resumed", exit.ExitCode()))
	}
	return pomeforge.Errorf("child_failed", "could not start the selected tool; check its executable and required Linux libraries")
}

type quickstartWizard struct {
	input      io.Reader
	output     io.Writer
	options    quickstartOptions
	arch       string
	bundle     func() (quickstartBundle, error)
	newBackend func(string) (quickstartBackend, error)
	processes  quickstartProcesses
}

func (c *cli) quickstart(args []string) int {
	flags := newFlagSet("quickstart")
	var options quickstartOptions
	flags.StringVar(&options.workspace, "workspace", "", "Workspace root")
	flags.StringVar(&options.xcode, "xcode", "", "Apple XIP or Xcode.app")
	flags.StringVar(&options.bundleID, "bundle-id", "", "HelloWorld bundle identifier")
	flags.BoolVar(&options.skipDevice, "skip-device", false, "Stop after the local build")
	if err := flags.Parse(args); err != nil || flags.NArg() != 0 {
		return c.fail(pomeforge.Errorf("usage", quickstartHelp), 2)
	}
	if c.json {
		return c.fail(pomeforge.Errorf("interactive_required", "quickstart requires a Linux terminal and does not support JSON execution; use tools, sdk, init, plan and run with --json"), 2)
	}
	if runtime.GOOS != "linux" || (runtime.GOARCH != "amd64" && runtime.GOARCH != "arm64") {
		return c.fail(pomeforge.Errorf("unsupported_host", "quickstart requires Linux amd64 or arm64; all compilation runs on Linux"), 2)
	}
	stdout, outOK := c.stdout.(*os.File)
	stderr, errOK := c.stderr.(*os.File)
	if !outOK || !errOK || !quickstartIsTerminal(os.Stdin) || !quickstartIsTerminal(stdout) || !quickstartIsTerminal(stderr) {
		return c.fail(pomeforge.Errorf("interactive_required", "open a terminal and run pomeforge quickstart; stdin, stdout and stderr must all be terminals so authentication is not logged"), 2)
	}
	if options.bundleID != "" {
		if err := pomeforge.ValidateBundleID(options.bundleID); err != nil {
			return c.fail(err, 2)
		}
	}
	if options.workspace == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return c.fail(pomeforge.Errorf("invalid_environment", "HOME is required for the default workspace"), 2)
		}
		options.workspace = filepath.Join(home, "PomeforgeProjects")
	}
	wizard := quickstartWizard{
		input: os.Stdin, output: c.stdout, options: options, arch: runtime.GOARCH,
		bundle: installedQuickstartBundle,
		newBackend: func(workspace string) (quickstartBackend, error) {
			service, err := pomeforge.NewService(workspace, defaultTools)
			return quickstartService{service}, err
		},
		processes: quickstartCommands{input: os.Stdin, output: c.stdout, errors: c.stderr},
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	if err := wizard.execute(ctx); err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, io.EOF) {
			fmt.Fprintln(c.stdout, "\nSetup stopped. Run pomeforge quickstart again to resume; completed work is preserved.")
			return 130
		}
		return c.fail(err, exitFor(err))
	}
	return 0
}

func (w *quickstartWizard) execute(ctx context.Context) error {
	fmt.Fprintln(w.output, "Pomeforge Setup — HelloWorld on Linux")
	fmt.Fprintln(w.output, "This creates or resumes HelloWorld, installs Linux tools and imports your Apple SDK. Apple account and device steps are optional.")
	fmt.Fprintln(w.output, "Workspace:", w.options.workspace)
	if err := w.consent(ctx, "Continue setup?"); err != nil {
		return err
	}
	workspace, project, err := quickstartProject(w.options.workspace, w.options.bundleID)
	if err != nil {
		return err
	}
	w.options.workspace = workspace
	fmt.Fprintf(w.output, "HelloWorld: %s\nBundle identifier: %s\n", project.Path, project.BundleID)
	backend, err := w.newBackend(workspace)
	if err != nil {
		return err
	}
	bundle, err := w.bundle()
	if err != nil {
		return err
	}
	_, runtimeErr := w.processes.capture(ctx, 3*time.Minute, bundle.runtime, []string{"status"}, workspace, backend.environment())
	if ctx.Err() != nil {
		return ctx.Err()
	}
	needsXtool := backend.tool(ctx, "xtool").Status != "available"
	if runtimeErr != nil || needsXtool {
		fmt.Fprintln(w.output, "Missing Linux tools will be downloaded and checksum-verified in private user state (up to approximately 1.1 GB, plus extraction space). Existing valid tools are reused.")
		if err := w.consent(ctx, "Set up the Linux tools now?"); err != nil {
			return err
		}
	}
	if runtimeErr != nil {
		if err := w.installRuntime(ctx, bundle.runtime, backend.environment()); err != nil {
			return err
		}
	}
	for _, id := range []string{"swift", "clang"} {
		tool := backend.tool(ctx, id)
		if tool.Status != "available" {
			return pomeforge.Errorf("missing_prerequisite", id+" is "+tool.Status+": "+tool.Detail+"; use the installed Pomeforge launcher and check the runtime's Linux dependencies")
		}
	}
	if needsXtool {
		if err := w.operation(ctx, backend, pomeforge.PlanInput{Action: "tool-install", Tool: "xtool"}, ""); err != nil {
			return err
		}
	}
	fmt.Fprintln(w.output, "Preparing built-in tools…")
	for _, helper := range []string{"unxip", "pomeforge-assets"} {
		if backend.tool(ctx, helper).Status == "available" {
			continue
		}
		path := bundle.helpers[helper]
		input := pomeforge.PlanInput{Action: "helper-register", Helper: helper, ExecutablePath: path}
		if helper == "unxip" {
			input.SourceRevision = "6c3990517fcc4c1db6952fccf4c562fb14097601"
		} else {
			input.AssetKitRevision = "e763558b55fcbb5a443b1d7b2c6f0972d8bd14f7"
		}
		if err := w.operation(ctx, backend, input, ""); err != nil {
			return err
		}
	}
	status := backend.sdk(ctx)
	if status.Status != "available" {
		fmt.Fprintln(w.output, "Apple SDK:", status.Detail)
		fmt.Fprintln(w.output, "Download Xcode 26's .xip in your Linux browser: https://developer.apple.com/download/all/?q=Xcode")
		fmt.Fprintln(w.output, "Use your Apple account and accept Apple's terms yourself. Pomeforge extracts the supplied archive on Linux; it never runs Xcode.")
		path := w.options.xcode
		if path == "" {
			path, err = w.answer(ctx, "XIP or extracted Xcode.app path (blank to stop and resume later): ")
			if err != nil {
				return err
			}
			if path == "" {
				return context.Canceled
			}
		}
		path, err = quickstartInputPath(path)
		if err != nil {
			return err
		}
		arch := "arm64"
		if w.arch == "amd64" {
			arch = "x86_64"
		}
		fmt.Fprintln(w.output, "Inspecting SDK input; a large archive or extracted tree can take a minute.")
		if err := w.operation(ctx, backend, pomeforge.PlanInput{Action: "sdk-import", InputPath: path, Arch: arch}, "Import this SDK into private user state? Allow several minutes and enough free disk space."); err != nil {
			return err
		}
		if backend.sdk(ctx).Status != "available" {
			return pomeforge.Errorf("sdk_unverified", "SDK import returned, but active SDK verification failed; run pomeforge sdk status --execute for diagnostics")
		}
	} else {
		fmt.Fprintln(w.output, "Reusing the verified active Darwin SDK. Existing SDKs are never replaced by this wizard.")
	}
	if err := w.operation(ctx, backend, pomeforge.PlanInput{Action: "build", Project: project.Path}, "Build HelloWorld locally now? Swift package plugins and build hooks run as your user."); err != nil {
		return err
	}
	fmt.Fprintln(w.output, "HelloWorld compiled on Linux. This is a development build, not an App Store submission.")
	if !w.options.skipDevice {
		if err := w.device(ctx, backend, project); err != nil {
			return err
		}
	}
	fmt.Fprintf(w.output, "\nEdit the Swift files in %s with your preferred editor.\n", project.Path)
	fmt.Fprintf(w.output, "Open the graphical workspace: pomeforge app --open --workspace %s\n", quickstartShellArgument(workspace))
	fmt.Fprintln(w.output, "Run pomeforge quickstart again to resume. Agent commands: pomeforge schema --json")
	return nil
}

func (w *quickstartWizard) installRuntime(ctx context.Context, helper string, env []string) error {
	if err := w.processes.terminal(ctx, helper, []string{"install"}, w.options.workspace, env); err != nil {
		return err
	}
	if _, err := w.processes.capture(ctx, 3*time.Minute, helper, []string{"status"}, w.options.workspace, env); err != nil {
		return pomeforge.Errorf("runtime_unverified", "runtime installation did not pass integrity verification; rerun pomeforge-runtime install for diagnostics")
	}
	return nil
}

func (w *quickstartWizard) operation(ctx context.Context, backend quickstartBackend, input pomeforge.PlanInput, question string) error {
	plan, err := backend.plan(ctx, input, true)
	if err != nil {
		return err
	}
	if !plan.Executable {
		return pomeforge.Errorf("blocked", strings.Join(plan.Blockers, "; "))
	}
	if question != "" {
		fmt.Fprintln(w.output, "\n"+plan.Title)
		for _, warning := range plan.Warnings {
			fmt.Fprintln(w.output, warning)
		}
		if err := w.consent(ctx, question); err != nil {
			return err
		}
	}
	fmt.Fprintln(w.output, "Working…")
	result, err := backend.run(ctx, plan.ID)
	if result.Output != "" {
		fmt.Fprintln(w.output, result.Output)
	}
	if err != nil {
		return err
	}
	if result.Status != "succeeded" || result.ExitCode != 0 {
		return pomeforge.Errorf("operation_failed", plan.Title+" did not succeed; completed setup is preserved")
	}
	return nil
}

func quickstartProject(requested, bundleID string) (string, pomeforge.ProjectSummary, error) {
	if bundleID != "" {
		if err := pomeforge.ValidateBundleID(bundleID); err != nil {
			return "", pomeforge.ProjectSummary{}, err
		}
	}
	if err := os.MkdirAll(requested, 0700); err != nil {
		return "", pomeforge.ProjectSummary{}, err
	}
	workspace, err := pomeforge.CanonicalWorkspace(requested)
	if err != nil {
		return "", pomeforge.ProjectSummary{}, err
	}
	path, err := pomeforge.ResolveWithin(workspace, "HelloWorld", false)
	if err != nil {
		return "", pomeforge.ProjectSummary{}, err
	}
	if _, err := os.Lstat(path); err == nil {
		manifest, _, err := pomeforge.LoadManifest(path)
		if err != nil {
			return "", pomeforge.ProjectSummary{}, pomeforge.Errorf("existing_project", "HelloWorld already exists but is not a valid Pomeforge project; choose another --workspace without changing the existing path")
		}
		if manifest.Name != "HelloWorld" || (bundleID != "" && manifest.BundleIdentifier != bundleID) {
			return "", pomeforge.ProjectSummary{}, pomeforge.Errorf("existing_project", "existing HelloWorld name or bundle ID differs; choose another --workspace or omit the conflicting --bundle-id")
		}
		return workspace, pomeforge.ProjectSummary{Path: path, Name: manifest.Name, BundleID: manifest.BundleIdentifier}, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", pomeforge.ProjectSummary{}, err
	}
	if bundleID == "" {
		var random [16]byte
		if _, err := rand.Read(random[:]); err != nil {
			return "", pomeforge.ProjectSummary{}, err
		}
		bundleID = "com.example.helloworld.a" + hex.EncodeToString(random[:])
	}
	project, err := pomeforge.CreateProject(workspace, "HelloWorld", "HelloWorld", bundleID)
	project.Path = path
	return workspace, project, err
}

type quickstartDevice struct{ name, id string }

var quickstartDeviceLine = regexp.MustCompile(`^(.*) \[([^\[\]\r\n]+)\]: ([A-Za-z0-9-]{1,128})$`)

func parseQuickstartDevices(output string) ([]quickstartDevice, error) {
	devices := []quickstartDevice{}
	seen := map[string]bool{}
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		parts := quickstartDeviceLine.FindStringSubmatch(line)
		if parts == nil || len(devices) >= 128 {
			return nil, pomeforge.Errorf("device_output_invalid", "xtool device output was not recognized; run xtool devices --usb --no-wait in your own terminal for diagnostics")
		}
		if seen[parts[3]] {
			continue
		}
		seen[parts[3]] = true
		name := strings.Map(func(r rune) rune {
			if unicode.IsControl(r) || r == '\u202e' || r == '\u202d' {
				return -1
			}
			return r
		}, parts[1])
		devices = append(devices, quickstartDevice{name: name, id: parts[3]})
	}
	return devices, nil
}

func (w *quickstartWizard) device(ctx context.Context, backend quickstartBackend, project pomeforge.ProjectSummary) error {
	fmt.Fprintln(w.output, "\nDevice setup is optional. Connect an unlocked iPhone or iPad with a data-capable USB cable and accept Trust This Computer.")
	fmt.Fprintln(w.output, "Your Linux host needs usbmuxd running and permission to access its socket/device. Install/enable usbmuxd using your distribution's package manager if needed; this wizard never runs sudo.")
	fmt.Fprintln(w.output, "On the device enable Settings > Privacy & Security > Developer Mode, restart when requested, and confirm. If the option is not visible, attempt pairing/development installation first, then return to Settings. Keep the device unlocked.")
	var selected quickstartDevice
	var tool pomeforge.ToolStatus
	for {
		answer, err := w.answer(ctx, "Press Enter to scan USB devices, or s to skip: ")
		if err != nil {
			return err
		}
		if strings.EqualFold(answer, "s") {
			fmt.Fprintln(w.output, "Device setup skipped; the local project and build are ready.")
			return nil
		}
		if answer != "" {
			continue
		}
		tool = backend.tool(ctx, "xtool")
		if tool.Status != "available" {
			return pomeforge.Errorf("missing_prerequisite", "verified xtool is no longer available")
		}
		output, err := w.processes.capture(ctx, 20*time.Second, tool.Path, []string{"devices", "--usb", "--no-wait"}, project.Path, backend.environment())
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			fmt.Fprintln(w.output, "Device scan failed. Check usbmuxd, USB permissions, the cable, unlock, and trust; then retry or skip.")
			continue
		}
		devices, err := parseQuickstartDevices(output)
		if err != nil {
			return err
		}
		if len(devices) == 0 {
			fmt.Fprintln(w.output, "No USB devices found. Check the cable, unlock, trust and usbmuxd; then retry or skip.")
			continue
		}
		for index, device := range devices {
			fmt.Fprintf(w.output, "%d. %s\n", index+1, device.name)
		}
		answer, err = w.answer(ctx, "Choose a device number, or s to skip: ")
		if err != nil {
			return err
		}
		if strings.EqualFold(answer, "s") {
			return nil
		}
		index, err := strconv.Atoi(answer)
		if err != nil || index < 1 || index > len(devices) {
			fmt.Fprintln(w.output, "Choose one of the listed device numbers.")
			continue
		}
		selected = devices[index-1]
		break
	}
	input := pomeforge.PlanInput{Action: "install", Project: project.Path, Device: selected.id}
	plan, err := backend.plan(ctx, input, false)
	if err != nil {
		return err
	}
	if !plan.Executable {
		return pomeforge.Errorf("blocked", strings.Join(plan.Blockers, "; "))
	}
	fmt.Fprintln(w.output, "\nApple authentication: 1. Use existing xtool authentication (default)  2. API key (paid program)  3. Apple ID/password + 2FA (free or paid)  4. Skip")
	for {
		choice, err := w.answer(ctx, "Choose [1]: ")
		if err != nil {
			return err
		}
		if choice == "4" {
			return nil
		}
		if choice == "" || choice == "1" {
			break
		}
		mode := ""
		if choice == "2" {
			mode = "key"
		} else if choice == "3" {
			mode = "password"
		} else {
			continue
		}
		fmt.Fprintln(w.output, "xtool will replace any saved login and prompt directly in this terminal. Pomeforge does not capture its output or credentials. Never paste secrets into an AI chat.")
		if mode == "key" {
			fmt.Fprintln(w.output, "Have your App Store Connect Key ID, Issuer ID, and downloaded .p8 key path ready. xtool will request them interactively.")
		}
		if err := w.consent(ctx, "Start interactive Apple login?"); err != nil {
			return err
		}
		tool = backend.tool(ctx, "xtool")
		if tool.Status != "available" {
			return pomeforge.Errorf("missing_prerequisite", "verified xtool is no longer available")
		}
		if err := w.processes.terminal(ctx, tool.Path, []string{"auth", "login", "--mode", mode}, project.Path, backend.environment()); err != nil {
			return err
		}
		break
	}
	fmt.Fprintln(w.output, "Installation may create or update Apple development provisioning/signing resources. Read every upstream prompt; if xtool asks to revoke certificates, decide yourself. Pomeforge never answers that prompt.")
	if err := w.consent(ctx, "Build, development-sign and install HelloWorld on the selected device?"); err != nil {
		return err
	}
	fresh, err := backend.plan(ctx, input, false)
	if err != nil {
		return err
	}
	if fresh.ID != plan.ID || !fresh.Executable {
		return pomeforge.Errorf("stale_plan", "project or tools changed while you were choosing; rerun quickstart to inspect a new installation plan")
	}
	if len(fresh.Steps) != 1 || fresh.Steps[0].Kind != "process" || fresh.Steps[0].Tool != "xtool" || strings.Join(fresh.Steps[0].Args, "\x00") != strings.Join([]string{"dev", "run", "--configuration", "debug", "--udid", selected.id}, "\x00") {
		return pomeforge.Errorf("invalid_plan", "development installation plan does not match the reviewed interactive contract")
	}
	step := fresh.Steps[0]
	if err := w.processes.terminal(ctx, step.Executable, step.Args, step.Directory, backend.environment()); err != nil {
		return err
	}
	fmt.Fprintln(w.output, "xtool reported installation completed. It does not launch the app. Tap HelloWorld on the device; if asked, trust the developer under Settings > General > VPN & Device Management.")
	answer, err := w.answer(ctx, "Did you see HelloWorld open on the device? [y/N]: ")
	if err != nil {
		return err
	}
	if strings.EqualFold(answer, "y") || strings.EqualFold(answer, "yes") {
		fmt.Fprintln(w.output, "You confirmed HelloWorld opened. No automated device proof was captured.")
	} else {
		fmt.Fprintln(w.output, "Device launch remains unverified. Check Developer Mode, developer trust and the device screen, then retry when ready.")
	}
	return nil
}

func (w *quickstartWizard) consent(ctx context.Context, question string) error {
	answer, err := w.answer(ctx, question+" [y/N]: ")
	if err != nil {
		return err
	}
	if !strings.EqualFold(answer, "y") && !strings.EqualFold(answer, "yes") {
		return context.Canceled
	}
	return nil
}

func (w *quickstartWizard) answer(ctx context.Context, prompt string) (string, error) {
	fmt.Fprint(w.output, prompt)
	type response struct {
		text string
		err  error
	}
	ready := make(chan response, 1)
	go func() {
		// Read one byte at a time: a buffered reader could consume bytes intended
		// for the subsequent upstream password/2FA prompt on the same terminal.
		var line strings.Builder
		var one [1]byte
		for {
			count, err := w.input.Read(one[:])
			if count > 0 {
				if one[0] == '\n' {
					ready <- response{strings.TrimSpace(line.String()), nil}
					return
				}
				if line.Len() >= 4096 {
					ready <- response{"", pomeforge.Errorf("invalid_input", "answer exceeds 4096 bytes")}
					return
				}
				line.WriteByte(one[0])
			}
			if err != nil {
				ready <- response{"", err}
				return
			}
		}
	}()
	select {
	case <-ctx.Done():
		return "", ctx.Err()
	case result := <-ready:
		return result.text, result.err
	}
}

func quickstartInputPath(input string) (string, error) {
	if input == "~" || strings.HasPrefix(input, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		if input == "~" {
			input = home
		} else {
			input = filepath.Join(home, strings.TrimPrefix(input, "~/"))
		}
	}
	return filepath.Abs(input)
}

func quickstartShellArgument(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
}
