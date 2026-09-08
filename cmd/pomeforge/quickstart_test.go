package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"pomeforge.local/pomeforge/internal/pomeforge"
)

type wizardBackend struct {
	tools        map[string]bool
	sdkReady     bool
	inputs       map[string]pomeforge.PlanInput
	executed     []pomeforge.PlanInput
	installPlans int
	stale        bool
	failAction   string
}

func (b *wizardBackend) tool(_ context.Context, id string) pomeforge.ToolStatus {
	status := "missing"
	if b.tools[id] {
		status = "available"
	}
	return pomeforge.ToolStatus{ID: id, Status: status, Path: "/fixture/xtool", Detail: "fixture prerequisite"}
}
func (b *wizardBackend) sdk(context.Context) pomeforge.ToolStatus {
	status := "missing"
	if b.sdkReady {
		status = "available"
	}
	return pomeforge.ToolStatus{Status: status, Detail: "fixture SDK"}
}
func (b *wizardBackend) plan(_ context.Context, input pomeforge.PlanInput, store bool) (pomeforge.Plan, error) {
	plan := pomeforge.Plan{ID: input.Action, Title: input.Action, Executable: true}
	if input.Action == "install" {
		b.installPlans++
		if b.stale && b.installPlans > 1 {
			plan.ID += "changed"
		}
		plan.Steps = []pomeforge.Step{{Kind: "process", Tool: "xtool", Executable: "/fixture/xtool", Directory: input.Project, Args: []string{"dev", "run", "--configuration", "debug", "--udid", input.Device}}}
	}
	if store {
		b.inputs[plan.ID] = input
	}
	return plan, nil
}
func (b *wizardBackend) run(_ context.Context, id string) (pomeforge.OperationResult, error) {
	input := b.inputs[id]
	b.executed = append(b.executed, input)
	if b.failAction == input.Action {
		return pomeforge.OperationResult{Status: "failed", ExitCode: 3}, nil
	}
	switch input.Action {
	case "tool-install":
		b.tools[input.Tool] = true
	case "helper-register":
		b.tools[input.Helper] = true
	case "sdk-import":
		b.sdkReady = true
	}
	return pomeforge.OperationResult{Status: "succeeded", ExitCode: 0}, nil
}
func (b *wizardBackend) environment() []string {
	return []string{"HOME=/fixture/private", "XDG_CONFIG_HOME=/fixture/config", "PATH=/fixture/bin"}
}

type wizardInvocation struct {
	executable string
	args, env  []string
	directory  string
}

type wizardProcesses struct {
	runtimeReady bool
	invocations  []wizardInvocation
	devices      string
	failTerminal string
}

func (p *wizardProcesses) terminal(_ context.Context, executable string, args []string, directory string, env []string) error {
	p.invocations = append(p.invocations, wizardInvocation{executable, append([]string(nil), args...), append([]string(nil), env...), directory})
	if len(args) > 0 && args[0] == p.failTerminal {
		return errors.New("fixture child failure")
	}
	if reflect.DeepEqual(args, []string{"install"}) {
		p.runtimeReady = true
	}
	return nil
}
func (p *wizardProcesses) capture(_ context.Context, timeout time.Duration, executable string, args []string, directory string, env []string) (string, error) {
	if reflect.DeepEqual(args, []string{"status"}) {
		if timeout != 3*time.Minute {
			return "", errors.New("runtime status timeout too short")
		}
		if p.runtimeReady {
			return "verified", nil
		}
		return "", errors.New("fixture runtime absent")
	}
	if timeout != 20*time.Second || !reflect.DeepEqual(args, []string{"devices", "--usb", "--no-wait"}) {
		return "", errors.New("unexpected device command")
	}
	return p.devices, nil
}

func wizardFixture(t *testing.T, answers string, ready bool) (*quickstartWizard, *wizardBackend, *wizardProcesses, *bytes.Buffer) {
	t.Helper()
	root := t.TempDir()
	executable := quickstartBundleFixture(t, filepath.Join(root, "package"))
	backend := &wizardBackend{tools: map[string]bool{"swift": true, "clang": true, "xtool": ready, "unxip": ready, "pomeforge-assets": ready}, sdkReady: ready, inputs: map[string]pomeforge.PlanInput{}}
	processes := &wizardProcesses{runtimeReady: ready, devices: "Fixture iPhone [usb]: 00000000-0000000000000000\n"}
	output := &bytes.Buffer{}
	wizard := &quickstartWizard{input: strings.NewReader(answers), output: output, options: quickstartOptions{workspace: filepath.Join(root, "projects"), xcode: filepath.Join(root, "Xcode.xip"), skipDevice: true}, arch: "amd64", bundle: func() (quickstartBundle, error) { return verifyQuickstartBundle(executable) }, newBackend: func(string) (quickstartBackend, error) { return backend, nil }, processes: processes}
	return wizard, backend, processes, output
}

func TestQuickstartJSONAndNonterminalNeverMutate(t *testing.T) {
	for _, jsonMode := range []bool{true, false} {
		root := filepath.Join(t.TempDir(), "must-not-exist")
		arguments := []string{"quickstart", "--workspace", root}
		if jsonMode {
			arguments = append(arguments, "--json")
		}
		var stdout, stderr bytes.Buffer
		if code := run(arguments, &stdout, &stderr); code == 0 {
			t.Fatal("noninteractive quickstart succeeded")
		}
		if _, err := os.Stat(root); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("quickstart mutated workspace: %v", err)
		}
		if jsonMode && !strings.Contains(stdout.String()+stderr.String(), `"code":"interactive_required"`) {
			t.Fatalf("missing structured failure: %s %s", &stdout, &stderr)
		}
	}
	var stdout, stderr bytes.Buffer
	if code := run([]string{"quickstart", "--help", "--json"}, &stdout, &stderr); code != 0 || !strings.Contains(stdout.String(), "without prompting") {
		t.Fatalf("help is not discoverable: %d %s", code, &stdout)
	}
	stdout.Reset()
	if code := run([]string{"schema", "--json"}, &stdout, &stderr); code != 0 || !strings.Contains(stdout.String(), `"name":"quickstart"`) || !strings.Contains(stdout.String(), "interactive_required") {
		t.Fatal("schema lacks interactive contract")
	}
}

func TestQuickstartFreshSetupAndResume(t *testing.T) {
	w, b, p, out := wizardFixture(t, "y\ny\ny\ny\n", false)
	if err := w.execute(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(b.executed) != 5 {
		t.Fatalf("expected tool, two helpers, SDK, build; got %#v", b.executed)
	}
	if b.executed[0].Tool != "xtool" || b.executed[3].Action != "sdk-import" || b.executed[3].Arch != "x86_64" || b.executed[4].Action != "build" {
		t.Fatalf("wrong setup ordering: %#v", b.executed)
	}
	if len(p.invocations) != 1 || !reflect.DeepEqual(p.invocations[0].args, []string{"install"}) {
		t.Fatalf("unexpected terminal effects: %#v", p.invocations)
	}
	project := filepath.Join(w.options.workspace, "HelloWorld")
	manifest, original, err := pomeforge.LoadManifest(project)
	if err != nil || !strings.HasPrefix(manifest.BundleIdentifier, "com.example.helloworld.a") {
		t.Fatalf("unique manifest: %#v %v", manifest, err)
	}
	for _, input := range b.executed {
		if input.Helper == "unxip" && input.SourceRevision != "6c3990517fcc4c1db6952fccf4c562fb14097601" {
			t.Fatal("unxip provenance drift")
		}
		if input.Helper == "pomeforge-assets" && input.AssetKitRevision != "e763558b55fcbb5a443b1d7b2c6f0972d8bd14f7" {
			t.Fatal("AssetKit provenance drift")
		}
	}
	w.input = strings.NewReader("y\ny\n")
	if err := w.execute(context.Background()); err != nil {
		t.Fatal(err)
	}
	_, resumed, err := pomeforge.LoadManifest(project)
	if err != nil || !bytes.Equal(original, resumed) {
		t.Fatal("resume changed the manifest or bundle ID")
	}
	if len(b.executed) != 6 || len(p.invocations) != 1 {
		t.Fatal("resume repeated setup effects")
	}
	if !strings.Contains(out.String(), "Reusing the verified active") || strings.Contains(out.String(), "You confirmed HelloWorld opened") {
		t.Fatal("incorrect resume/proof wording")
	}
}

func TestQuickstartPreservesExistingProjects(t *testing.T) {
	root := t.TempDir()
	_, first, err := quickstartProject(root, "")
	if err != nil {
		t.Fatal(err)
	}
	_, second, err := quickstartProject(root, "")
	if err != nil || first.BundleID != second.BundleID {
		t.Fatal("bundle identifier not stable")
	}
	if _, _, err := quickstartProject(root, "com.example.other"); err == nil {
		t.Fatal("conflicting bundle ID accepted")
	}
	other := t.TempDir()
	if err := os.Mkdir(filepath.Join(other, "HelloWorld"), 0700); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(other, "HelloWorld", "keep.txt")
	if err := os.WriteFile(file, []byte("human work"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := quickstartProject(other, ""); err == nil {
		t.Fatal("invalid existing project accepted")
	}
	if data, err := os.ReadFile(file); err != nil || string(data) != "human work" {
		t.Fatal("existing work changed")
	}
	symlink := t.TempDir()
	if err := os.Symlink(first.Path, filepath.Join(symlink, "HelloWorld")); err != nil {
		t.Fatal(err)
	}
	if _, _, err := quickstartProject(symlink, ""); err == nil {
		t.Fatal("symlinked project accepted")
	}
}

func TestQuickstartConsentAndFailureStopEffects(t *testing.T) {
	for _, tc := range []struct {
		name, answers, fail string
		wantRuntime         int
	}{
		{"initial-decline", "n\n", "", 0},
		{"runtime-decline", "y\nn\n", "", 0},
		{"runtime-failure", "y\ny\n", "install", 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w, b, p, _ := wizardFixture(t, tc.answers, false)
			p.failTerminal = tc.fail
			if err := w.execute(context.Background()); err == nil {
				t.Fatal("declined/failed flow succeeded")
			}
			if len(p.invocations) != tc.wantRuntime || len(b.executed) != 0 {
				t.Fatalf("unexpected downstream effects: %#v %#v", p.invocations, b.executed)
			}
		})
	}
	w, b, p, out := wizardFixture(t, "y\ny\n", true)
	b.failAction = "build"
	if err := w.execute(context.Background()); err == nil {
		t.Fatal("failed build succeeded")
	}
	if len(p.invocations) != 0 || strings.Contains(out.String(), "compiled on Linux") {
		t.Fatal("failed build reached success/device path")
	}
}

func TestQuickstartInteractiveDeviceConsentAndExactCommands(t *testing.T) {
	w, b, p, out := wizardFixture(t, "y\ny\n\n1\n3\ny\ny\nn\n", true)
	w.options.skipDevice = false
	if err := w.execute(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(p.invocations) != 2 {
		t.Fatalf("expected auth/install only: %#v", p.invocations)
	}
	if !reflect.DeepEqual(p.invocations[0].args, []string{"auth", "login", "--mode", "password"}) || !reflect.DeepEqual(p.invocations[1].args, []string{"dev", "run", "--configuration", "debug", "--udid", "00000000-0000000000000000"}) {
		t.Fatalf("wrong upstream argv: %#v", p.invocations)
	}
	for _, call := range p.invocations {
		if !reflect.DeepEqual(call.env, b.environment()) || call.directory != filepath.Join(w.options.workspace, "HelloWorld") {
			t.Fatal("lost selected XDG/project environment")
		}
	}
	if strings.Contains(out.String(), "00000000-0000000000000000") || !strings.Contains(out.String(), "Device launch remains unverified") {
		t.Fatalf("identifier leaked or false device proof: %s", out)
	}
	for _, input := range b.executed {
		if input.Action == "install" {
			t.Fatal("interactive install entered captured operation history")
		}
	}
}

func TestQuickstartExistingAuthSkipAndStalePlan(t *testing.T) {
	for _, tc := range []struct {
		name, answers string
		stale         bool
		wantCalls     int
		wantError     bool
	}{
		{"existing-auth", "y\ny\n\n1\n\ny\ny\n", false, 1, false},
		{"skip-device", "y\ny\ns\n", false, 0, false},
		{"skip-auth", "y\ny\n\n1\n4\n", false, 0, false},
		{"decline-install", "y\ny\n\n1\n\nn\n", false, 0, true},
		{"stale-plan", "y\ny\n\n1\n\ny\n", true, 0, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w, b, p, _ := wizardFixture(t, tc.answers, true)
			w.options.skipDevice = false
			b.stale = tc.stale
			err := w.execute(context.Background())
			if (err != nil) != tc.wantError || len(p.invocations) != tc.wantCalls {
				t.Fatalf("err=%v calls=%#v", err, p.invocations)
			}
			for _, call := range p.invocations {
				if call.args[0] == "auth" {
					t.Fatal("existing/skip authentication prompted login")
				}
			}
		})
	}
}

func TestQuickstartDeviceCommandFailureIsNotSuccess(t *testing.T) {
	w, _, p, out := wizardFixture(t, "y\ny\n\n1\n\ny\n", true)
	w.options.skipDevice = false
	p.failTerminal = "dev"
	if err := w.execute(context.Background()); err == nil {
		t.Fatal("install failure accepted")
	}
	if strings.Contains(out.String(), "installation completed") || strings.Contains(out.String(), "Did you see") {
		t.Fatal("failed install reported success")
	}
}

func TestQuickstartDeviceParsingAndOutputBounds(t *testing.T) {
	devices, err := parseQuickstartDevices("Phone: work [usb]: 00000000-0000000000000000\nPhone: work [usb]: 00000000-0000000000000000\nTablet [usb]: aabbccdd\n")
	if err != nil || len(devices) != 2 || devices[0].name != "Phone: work" {
		t.Fatalf("parse failed: %#v %v", devices, err)
	}
	for _, invalid := range []string{"unrecognized output", "Phone [usb]: ../../outside", "Phone [usb]: id --password secret"} {
		if _, err := parseQuickstartDevices(invalid); err == nil {
			t.Fatalf("accepted %q", invalid)
		}
	}
	var output quickstartOutput
	if n, err := output.Write(bytes.Repeat([]byte("x"), 70<<10)); err != nil || n != 70<<10 || !output.overflow || output.text.Len() != 64<<10 {
		t.Fatal("capture is not bounded")
	}
	if _, err := output.Write([]byte("more")); err != nil || output.text.Len() != 64<<10 {
		t.Fatal("overflow grew buffer")
	}
}

func TestQuickstartPromptCancellationAndNoReadAhead(t *testing.T) {
	input := strings.NewReader("yes\nprivate-input-for-upstream\n")
	w := &quickstartWizard{input: input, output: io.Discard}
	if answer, err := w.answer(context.Background(), ""); err != nil || answer != "yes" {
		t.Fatal(answer, err)
	}
	rest, _ := io.ReadAll(input)
	if string(rest) != "private-input-for-upstream\n" {
		t.Fatal("prompt consumed upstream input")
	}
	reader, writer := io.Pipe()
	defer reader.Close()
	defer writer.Close()
	w.input = reader
	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(10 * time.Millisecond); cancel() }()
	if _, err := w.answer(ctx, ""); !errors.Is(err, context.Canceled) {
		t.Fatalf("prompt did not cancel: %v", err)
	}
}

func TestQuickstartCommandExitAndCancellation(t *testing.T) {
	commands := quickstartCommands{input: strings.NewReader(""), output: io.Discard, errors: io.Discard}
	if err := commands.terminal(context.Background(), "/bin/sh", []string{"-c", "exit 7"}, t.TempDir(), []string{"PATH=/usr/bin:/bin"}); err == nil || !strings.Contains(err.Error(), "status 7") {
		t.Fatalf("child exit was not checked: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if err := commands.terminal(ctx, "/bin/sleep", []string{"5"}, t.TempDir(), []string{"PATH=/usr/bin:/bin"}); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("child did not cancel: %v", err)
	}
}

func quickstartBundleFixture(t *testing.T, root string) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(root, "libexec"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "share"), 0700); err != nil {
		t.Fatal(err)
	}
	entries := map[string]quickstartBundleEntry{}
	for _, name := range []string{"pomeforge", "pomeforge-runtime", "unxip", "pomeforge-assets", "runtime-lock.json"} {
		data := []byte("fixture binary " + name)
		path := filepath.Join(root, "libexec", name)
		mode := os.FileMode(0700)
		if name == "runtime-lock.json" {
			mode = 0644
		}
		if err := os.WriteFile(path, data, mode); err != nil {
			t.Fatal(err)
		}
		digest := sha256.Sum256(data)
		entries["libexec/"+name] = quickstartBundleEntry{SHA256: hex.EncodeToString(digest[:]), Mode: uint32(mode), Size: int64(len(data))}
	}
	data, err := json.Marshal(entries)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "share", "package-manifest.json"), data, 0600); err != nil {
		t.Fatal(err)
	}
	return filepath.Join(root, "libexec", "pomeforge")
}

func TestQuickstartAutomaticHelpersAreBoundToInstalledPackage(t *testing.T) {
	for _, change := range []string{"unchanged", "helper-bytes", "helper-mode", "helper-symlink", "runtime-bytes", "go-bytes", "missing-manifest", "lock-bytes", "lock-mode", "missing-lock"} {
		t.Run(change, func(t *testing.T) {
			executable := quickstartBundleFixture(t, t.TempDir())
			directory := filepath.Dir(executable)
			helper := filepath.Join(directory, "unxip")
			switch change {
			case "helper-bytes":
				if err := os.WriteFile(helper, []byte("changed binary"), 0700); err != nil {
					t.Fatal(err)
				}
			case "helper-mode":
				if err := os.Chmod(helper, 0600); err != nil {
					t.Fatal(err)
				}
			case "helper-symlink":
				outside := filepath.Join(t.TempDir(), "unxip")
				if err := os.Rename(helper, outside); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(outside, helper); err != nil {
					t.Fatal(err)
				}
			case "runtime-bytes":
				if err := os.WriteFile(filepath.Join(directory, "pomeforge-runtime"), []byte("untrusted runtime"), 0700); err != nil {
					t.Fatal(err)
				}
			case "go-bytes":
				if err := os.WriteFile(executable, []byte("other CLI"), 0700); err != nil {
					t.Fatal(err)
				}
			case "missing-manifest":
				if err := os.Remove(filepath.Join(filepath.Dir(directory), "share", "package-manifest.json")); err != nil {
					t.Fatal(err)
				}
			case "lock-bytes":
				if err := os.WriteFile(filepath.Join(directory, "runtime-lock.json"), []byte("changed download URL and hash"), 0644); err != nil {
					t.Fatal(err)
				}
			case "lock-mode":
				if err := os.Chmod(filepath.Join(directory, "runtime-lock.json"), 0755); err != nil {
					t.Fatal(err)
				}
			case "missing-lock":
				if err := os.Remove(filepath.Join(directory, "runtime-lock.json")); err != nil {
					t.Fatal(err)
				}
			}
			bundle, err := verifyQuickstartBundle(executable)
			if change == "unchanged" {
				if err != nil || bundle.helpers["unxip"] != helper || bundle.runtime != filepath.Join(directory, "pomeforge-runtime") {
					t.Fatalf("wrong bundle binding: %#v %v", bundle, err)
				}
			} else if err == nil {
				t.Fatal("modified/nonpackaged helper accepted")
			}
		})
	}
}
