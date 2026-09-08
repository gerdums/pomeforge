package pomeforge

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	toolcatalog "pomeforge.local/pomeforge"
	"pomeforge.local/pomeforge/internal/bootstrap"
)

type SetupCommandRunner interface {
	Run(context.Context, string, []string, string, []string) (string, int, error)
}

type SetupManager struct {
	Catalog       bootstrap.Catalog
	Paths         bootstrap.Paths
	Installer     bootstrap.Installer
	Tools         ToolResolver
	Runner        SetupCommandRunner
	HostOS        string
	HostArch      string
	StateDir      string
	Now           func() time.Time
	ProbeTimeout  time.Duration
	XDGConfigHome string
}

func NewSetupManager(tools ToolResolver) (*SetupManager, error) {
	catalog, err := toolcatalog.Load()
	if err != nil {
		return nil, err
	}
	paths, err := bootstrap.DefaultPaths()
	if err != nil {
		return nil, err
	}
	if tools == nil {
		tools = &IntegratedToolResolver{Catalog: catalog, Paths: paths, StateDir: filepath.Dir(paths.ToolsDir)}
	}
	configHome, err := effectiveXDGConfigHome()
	if err != nil {
		return nil, err
	}
	if integrated, ok := tools.(*IntegratedToolResolver); ok {
		if integrated.XDGConfigHome == "" {
			integrated.XDGConfigHome = configHome
		} else {
			configHome = integrated.XDGConfigHome
		}
	}
	if !filepath.IsAbs(configHome) || filepath.Clean(configHome) != configHome || configHome == string(filepath.Separator) {
		return nil, errors.New("effective XDG_CONFIG_HOME must be a clean absolute non-root path")
	}
	return &SetupManager{Catalog: catalog, Paths: paths, Tools: tools, StateDir: filepath.Dir(paths.ToolsDir), XDGConfigHome: configHome}, nil
}

func isWorkspaceAction(action string) bool {
	switch action {
	case "tool-install", "helper-register", "sdk-status", "sdk-import":
		return true
	default:
		return false
	}
}

func (m *SetupManager) Plan(ctx context.Context, input PlanInput) (Plan, error) {
	action, ok := actionByID(input.Action)
	if !ok || action.Scope != "workspace" {
		return Plan{}, Errorf("unknown_action", "unknown workspace action: "+input.Action)
	}
	if err := validateWorkspaceInput(input); err != nil {
		return Plan{}, err
	}
	plan := Plan{Action: input.Action, Title: action.Title, Scope: "workspace", Effect: action.Effect, RequiresConfirmation: action.RequiresConfirmation, Steps: []Step{}, Blockers: []string{}, Warnings: []string{}}
	hostOS := m.HostOS
	if hostOS == "" {
		hostOS = runtime.GOOS
	}
	hostArch := m.HostArch
	if hostArch == "" {
		hostArch = runtime.GOARCH
	}
	if hostOS != "linux" {
		plan.Blockers = append(plan.Blockers, "setup installation and SDK import are supported only on Linux")
	}
	var fingerprint string
	var err error
	switch input.Action {
	case "tool-install":
		fingerprint, err = m.planToolInstall(ctx, input, hostOS, hostArch, &plan)
	case "helper-register":
		fingerprint, err = m.planHelperRegister(ctx, input, &plan)
	case "sdk-status":
		fingerprint, err = m.planSDKStatus(ctx, &plan)
	case "sdk-import":
		fingerprint, err = m.planSDKImport(ctx, input, hostArch, &plan)
	}
	if err != nil {
		return Plan{}, err
	}
	plan.Executable = len(plan.Blockers) == 0 && len(plan.Steps) > 0
	plan.Fingerprint = fingerprint
	plan.ID = setupPlanID(plan)
	return plan, nil
}

func validateWorkspaceInput(input PlanInput) error {
	if input.Project != "" || input.IPA != "" || input.Device != "" {
		return Errorf("invalid_input", "workspace setup actions do not accept project, IPA, or device fields")
	}
	reject := func(values ...string) bool {
		for _, value := range values {
			if value != "" {
				return true
			}
		}
		return false
	}
	switch input.Action {
	case "tool-install":
		if input.Tool == "" {
			return Errorf("invalid_input", "tool-install requires tool")
		}
		if reject(input.Helper, input.ExecutablePath, input.SourceRevision, input.AssetKitRevision, input.InputPath, input.Arch) {
			return Errorf("invalid_input", "tool-install accepts only the tool field")
		}
	case "helper-register":
		if input.Helper == "" || input.ExecutablePath == "" {
			return Errorf("invalid_input", "helper-register requires helper and executablePath")
		}
		if reject(input.Tool, input.InputPath, input.Arch) {
			return Errorf("invalid_input", "helper-register received fields that do not apply")
		}
		if input.Helper == "unxip" && input.AssetKitRevision != "" {
			return Errorf("invalid_input", "assetKitRevision applies only to pomeforge-assets")
		}
	case "sdk-import":
		if input.InputPath == "" || input.Arch == "" {
			return Errorf("invalid_input", "sdk-import requires inputPath and arch")
		}
		if reject(input.Tool, input.Helper, input.ExecutablePath, input.SourceRevision, input.AssetKitRevision) {
			return Errorf("invalid_input", "sdk-import received fields that do not apply")
		}
	case "sdk-status":
		if reject(input.Tool, input.Helper, input.ExecutablePath, input.SourceRevision, input.AssetKitRevision, input.InputPath, input.Arch) {
			return Errorf("invalid_input", "sdk-status accepts no fields")
		}
	}
	return nil
}

func (m *SetupManager) planToolInstall(ctx context.Context, input PlanInput, hostOS, hostArch string, plan *Plan) (string, error) {
	install, err := bootstrap.PlanInstall(m.Catalog, input.Tool, hostOS, hostArch, m.Paths)
	if err != nil {
		if errors.Is(err, bootstrap.ErrUnsupportedPlatform) {
			plan.Blockers = append(plan.Blockers, err.Error())
			return digestStrings(input.Action, input.Tool, hostOS, hostArch, err.Error()), nil
		}
		return "", Errorf("unsupported_tool", err.Error())
	}
	tool := install.Tool()
	asset := install.Asset()
	plan.ToolInstall = &ToolInstallPlan{Tool: tool.ID, Version: tool.Version, Destination: install.ExecutablePath(), SourceURL: asset.URL, SHA256: strings.ToLower(asset.SHA256), DownloadSize: asset.Size}
	plan.Steps = append(plan.Steps, Step{
		Kind: "internal", Tool: "pomeforge", Operation: "verified-managed-install",
		Parameters:  map[string]string{"tool": tool.ID, "version": tool.Version, "platform": install.Platform(), "destination": install.ExecutablePath(), "sourceUrl": asset.URL, "sha256": strings.ToLower(asset.SHA256), "downloadSize": fmt.Sprint(asset.Size)},
		Description: "Download the immutable catalog artifact, verify its size and SHA-256, stage it privately, and atomically activate it.",
	})
	plan.Warnings = append(plan.Warnings, "This writes only to Pomeforge's private XDG data/cache paths and does not use sudo or modify the host PATH.")
	managedState := "absent"
	if receipt, verifyErr := bootstrap.VerifyInstalled(ctx, install); verifyErr == nil {
		encoded, _ := json.Marshal(receipt)
		managedState = string(encoded)
	} else if !errors.Is(verifyErr, bootstrap.ErrInstallAbsent) {
		managedState = "invalid:" + verifyErr.Error()
	}
	return digestStrings(input.Action, tool.ID, tool.Version, install.Platform(), install.ExecutablePath(), asset.URL, strings.ToLower(asset.SHA256), fmt.Sprint(asset.Size), managedState), nil
}

func (m *SetupManager) planHelperRegister(ctx context.Context, input PlanInput, plan *Plan) (string, error) {
	if input.Helper != "pomeforge-assets" && input.Helper != "unxip" {
		return "", Errorf("unsupported_helper", "helper must be pomeforge-assets or unxip")
	}
	for label, revision := range map[string]string{"source revision": input.SourceRevision, "AssetKit revision": input.AssetKitRevision} {
		if revision != "" && (len(revision) != 40 || strings.Trim(revision, "0123456789abcdefABCDEF") != "") {
			return "", Errorf("invalid_input", label+" must be a 40-character hexadecimal Git revision")
		}
	}
	if !filepath.IsAbs(input.ExecutablePath) {
		return "", Errorf("invalid_path", "helper executable path must be absolute")
	}
	path, err := filepath.Abs(input.ExecutablePath)
	if err != nil || filepath.Clean(path) != path {
		return "", Errorf("invalid_path", "helper executable path must resolve to a clean absolute path")
	}
	digest, err := hashRegularFile(ctx, path, maxHelperBytes)
	if err != nil {
		return "", Errorf("invalid_helper", err.Error())
	}
	receipt := HelperReceipt{ID: input.Helper, Path: path, SHA256: digest, SourceRevision: input.SourceRevision, AssetKitRevision: input.AssetKitRevision}
	probe := verifyHelper(ctx, receipt, ToolStatus{ID: input.Helper, Name: input.Helper, InstallURL: helperURL(input.Helper)})
	if probe.Status != "available" {
		plan.Blockers = append(plan.Blockers, probe.Detail)
	}
	if input.Helper == "pomeforge-assets" && input.AssetKitRevision != "" && input.AssetKitRevision != assetKitRevision {
		plan.Blockers = append(plan.Blockers, "AssetKit revision does not match Pomeforge's pinned bridge revision "+assetKitRevision)
	}
	if input.Helper == "unxip" && input.SourceRevision != "" && input.SourceRevision != unxipRevision {
		plan.Blockers = append(plan.Blockers, "unxip source revision does not match the reviewed revision "+unxipRevision)
	}
	plan.Steps = append(plan.Steps, Step{Kind: "internal", Tool: "pomeforge", Operation: "register-helper-integrity", Parameters: map[string]string{"helper": input.Helper, "executablePath": path, "sha256": digest, "sourceRevision": input.SourceRevision, "assetKitRevision": input.AssetKitRevision}, Description: "Recheck the executable and command contract, then store its exact path, SHA-256, and supplied provenance in private user state."})
	if input.SourceRevision == "" {
		plan.Warnings = append(plan.Warnings, "No source revision was supplied; Pomeforge will not infer provenance from the executable path.")
	}
	if input.Helper == "pomeforge-assets" && input.AssetKitRevision == "" {
		plan.Warnings = append(plan.Warnings, "No AssetKit revision was supplied; Pomeforge will not claim which AssetKit source produced this executable.")
	}
	return digestStrings(input.Action, input.Helper, path, digest, input.SourceRevision, input.AssetKitRevision), nil
}

func (m *SetupManager) planSDKStatus(ctx context.Context, plan *Plan) (string, error) {
	configHome, err := m.sdkConfigHome()
	if err != nil {
		return "", Errorf("invalid_environment", err.Error())
	}
	status := canonicalToolStatus(m.Tools.Probe(ctx, "xtool"))
	swift := canonicalToolStatus(m.Tools.Probe(ctx, "swift"))
	if status.Status != "available" {
		plan.Blockers = append(plan.Blockers, "xtool is "+status.Status+": "+status.Detail)
	}
	if swift.Status != "available" {
		plan.Blockers = append(plan.Blockers, "Swift 6.3 or later is "+swift.Status+": "+swift.Detail)
	}
	if status.Status != "available" || swift.Status != "available" {
		return digestStrings("sdk-status", configHome, status.Status, status.Path, status.CanonicalPath, status.Version, status.Detail, swift.Status, swift.Path, swift.CanonicalPath, swift.Version, swift.Detail), nil
	}
	plan.Steps = append(plan.Steps,
		Step{Kind: "process", Tool: "xtool", Executable: status.Path, Args: []string{"sdk", "status"}, Directory: m.workingDirectory(), Description: "Read xtool's actual Darwin SDK status output; exit zero alone is not treated as installed."},
		Step{Kind: "process", Tool: "swift", Executable: swift.Path, Args: []string{"sdk", "list"}, Directory: m.workingDirectory(), Description: "Verify that the selected Swift installation lists the Darwin SDK in the effective private XDG_CONFIG_HOME."},
		Step{Kind: "process", Tool: "swift", Executable: swift.Path, Args: []string{"sdk", "configure", "darwin", sdkTargetTriple, "--show-configuration"}, Directory: m.workingDirectory(), Description: "Resolve the selected arm64-apple-ios SDK root and verify it against installed swift-sdk.json."},
	)
	executableHash, err := hashExecutableForPlan(ctx, status.Path)
	if err != nil {
		return "", err
	}
	swiftHash, err := hashExecutableForPlan(ctx, swift.Path)
	if err != nil {
		return "", err
	}
	return digestStrings("sdk-status", configHome, status.Path, status.CanonicalPath, status.Version, executableHash, swift.Path, swift.CanonicalPath, swift.Version, swiftHash), nil
}

func setupPlanID(plan Plan) string {
	payload := struct {
		Action      string
		Title       string
		Steps       []Step
		Blockers    []string
		Warnings    []string
		Effect      string
		Scope       string
		Fingerprint string
		ToolInstall *ToolInstallPlan
		SDKImport   *SDKImportPlan
	}{plan.Action, plan.Title, plan.Steps, plan.Blockers, plan.Warnings, plan.Effect, plan.Scope, plan.Fingerprint, plan.ToolInstall, plan.SDKImport}
	encoded, _ := json.Marshal(payload)
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:12])
}

func digestStrings(values ...string) string {
	hash := sha256.New()
	for _, value := range values {
		_, _ = hash.Write([]byte(value))
		_, _ = hash.Write([]byte{0})
	}
	return hex.EncodeToString(hash.Sum(nil))
}

func hashExecutableForPlan(ctx context.Context, path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", err
	}
	digest, err := hashRegularFile(ctx, resolved, maxToolFingerprintBytes)
	if err != nil {
		return "", err
	}
	current, err := filepath.EvalSymlinks(abs)
	if err != nil || current != resolved {
		return "", errors.New("executable symlink mapping changed while hashing")
	}
	return digest, nil
}

func (m *SetupManager) Execute(ctx context.Context, plan Plan, input PlanInput) (OperationResult, error) {
	if !plan.Executable {
		return OperationResult{}, Errorf("blocked", "operation is blocked: "+strings.Join(plan.Blockers, "; "))
	}
	historyRoot, err := m.workspaceHistoryRoot(input)
	if err != nil {
		return OperationResult{}, Errorf("history_failed", "operation was not started because private workspace history is unavailable: "+err.Error())
	}
	if err := ensureHistory(m.StateDir, historyRoot); err != nil {
		return OperationResult{}, Errorf("history_failed", "operation was not started because private workspace history is unavailable: "+err.Error())
	}
	now := time.Now
	if m.Now != nil {
		now = m.Now
	}
	result := OperationResult{ID: operationID(), Action: plan.Action, Status: "succeeded", Scope: "workspace", StartedAt: now().UTC()}
	var output string
	var exitCode int
	switch input.Action {
	case "tool-install":
		output, exitCode, err = m.executeToolInstall(ctx, input)
	case "helper-register":
		output, exitCode, err = m.executeHelperRegister(ctx, input)
	case "sdk-status":
		output, exitCode, err = m.executeSDKStatus(ctx, plan)
	case "sdk-import":
		output, exitCode, result.Metadata, err = m.executeSDKImport(ctx, plan, input)
	default:
		err = errors.New("unknown workspace operation")
		exitCode = 1
	}
	result.ExitCode = exitCode
	result.Output = redactOutput(output)
	if err != nil {
		result.Status = "failed"
		if result.ExitCode == 0 {
			result.ExitCode = 1
		}
		if result.Output != "" && !strings.HasSuffix(result.Output, "\n") {
			result.Output += "\n"
		}
		result.Output += "operation failed: " + safeSetupError(err) + "\n"
	}
	result.FinishedAt = now().UTC()
	if historyErr := appendHistory(m.StateDir, historyRoot, result); historyErr != nil {
		message := "operation completed, but its private workspace history receipt could not be stored: " + historyErr.Error()
		result.Output = redactOutput(result.Output + "\nhistory warning: " + historyErr.Error() + "\n")
		return result, ErrorWithResult("history_failed", message, result)
	}
	return result, nil
}

func (m *SetupManager) workspaceHistoryRoot(input PlanInput) (string, error) {
	workspace := input.reservation
	if before, _, found := strings.Cut(workspace, "\x00"); found {
		workspace = before
	}
	if workspace == "" {
		return "", errors.New("workspace scope was not bound to the plan")
	}
	root, err := m.workspaceHistoryRootFor(workspace)
	if err != nil {
		return "", err
	}
	if err := ensurePrivateDirectory(root); err != nil {
		return "", err
	}
	return root, nil
}

func (m *SetupManager) workspaceHistoryRootFor(workspace string) (string, error) {
	if workspace == "" || !filepath.IsAbs(workspace) {
		return "", errors.New("workspace history requires an absolute workspace")
	}
	digest := sha256.Sum256([]byte(filepath.Clean(workspace)))
	root := filepath.Join(m.StateDir, "workspaces", hex.EncodeToString(digest[:16]))
	return root, nil
}

func (m *SetupManager) executeToolInstall(ctx context.Context, input PlanInput) (string, int, error) {
	hostOS, hostArch := m.HostOS, m.HostArch
	if hostOS == "" {
		hostOS = runtime.GOOS
	}
	if hostArch == "" {
		hostArch = runtime.GOARCH
	}
	install, err := bootstrap.PlanInstall(m.Catalog, input.Tool, hostOS, hostArch, m.Paths)
	if err != nil {
		return "", 1, err
	}
	_, err = m.Installer.Install(ctx, install)
	if err != nil {
		return "", 1, err
	}
	verified, err := bootstrap.VerifyInstalled(ctx, install)
	if err != nil {
		return "", 1, fmt.Errorf("post-install verification failed: %w", err)
	}
	status := m.Tools.Probe(ctx, input.Tool)
	if status.Status != "available" {
		return "", 1, fmt.Errorf("installed artifact passed integrity verification but its executable check is %s: %s", status.Status, status.Detail)
	}
	encoded, _ := json.MarshalIndent(verified, "", "  ")
	return "Pinned tool installed, integrity-verified, and executed successfully.\n" + string(encoded) + "\n", 0, nil
}

func (m *SetupManager) executeHelperRegister(ctx context.Context, input PlanInput) (string, int, error) {
	path, err := filepath.Abs(input.ExecutablePath)
	if err != nil {
		return "", 1, err
	}
	digest, err := hashRegularFile(ctx, path, maxHelperBytes)
	if err != nil {
		return "", 1, err
	}
	receipt := HelperReceipt{ID: input.Helper, Path: path, SHA256: digest, SourceRevision: input.SourceRevision, AssetKitRevision: input.AssetKitRevision, RegisteredAt: time.Now().UTC()}
	status := verifyHelper(ctx, receipt, ToolStatus{ID: input.Helper, Name: input.Helper, InstallURL: helperURL(input.Helper)})
	if status.Status != "available" {
		return "", 1, errors.New(status.Detail)
	}
	if err := storeHelperReceipt(m.StateDir, receipt); err != nil {
		return "", 1, err
	}
	encoded, _ := json.MarshalIndent(receipt, "", "  ")
	return "Helper registered with integrity verification.\n" + string(encoded) + "\n", 0, nil
}

func (m *SetupManager) executeSDKStatus(ctx context.Context, plan Plan) (string, int, error) {
	if len(plan.Steps) != 3 || len(plan.Steps[0].Args) != 2 || strings.Join(plan.Steps[0].Args, "\x00") != "sdk\x00status" || strings.Join(plan.Steps[1].Args, "\x00") != "sdk\x00list" || strings.Join(plan.Steps[2].Args, "\x00") != "sdk\x00configure\x00darwin\x00"+sdkTargetTriple+"\x00--show-configuration" {
		return "", 1, errors.New("SDK status plan has an invalid step set")
	}
	output, code, err := m.runStatusProbe(ctx, plan.Steps[0].Executable, plan.Steps[0].Directory)
	if err != nil {
		return output, code, err
	}
	installed, recognized := parseSDKStatus(output)
	if !recognized {
		return output, 1, errors.New("xtool SDK status output was not recognized")
	}
	if !installed {
		return output, 2, errors.New("Darwin SDK is not installed")
	}
	listOutput, listCode, listErr := m.runWithTimeout(ctx, plan.Steps[1].Executable, plan.Steps[1].Args, plan.Steps[1].Directory, 10*time.Second)
	output += listOutput
	if listErr != nil || listCode != 0 {
		return output, listCode, errors.New("Swift could not list the installed Darwin SDK")
	}
	configurationOutput, configurationCode, configurationErr := m.runWithTimeout(ctx, plan.Steps[2].Executable, plan.Steps[2].Args, plan.Steps[2].Directory, 10*time.Second)
	output += configurationOutput
	if configurationErr != nil || configurationCode != 0 {
		return output, configurationCode, errors.New("Swift could not configure the arm64-apple-ios Darwin SDK")
	}
	configHome, err := m.sdkConfigHome()
	if err != nil {
		return output, 1, err
	}
	installedBundle := filepath.Join(configHome, "swiftpm", "swift-sdks", "darwin.artifactbundle")
	if _, _, _, err := verifyInstalledSDKSelection(ctx, installedBundle, listOutput, configurationOutput); err != nil {
		return output, 1, err
	}
	return output, 0, nil
}

func (m *SetupManager) run(ctx context.Context, executable string, args []string, directory string) (string, int, error) {
	return m.runWithTimeout(ctx, executable, args, directory, 30*time.Minute)
}

func (m *SetupManager) runStatusProbe(ctx context.Context, executable, directory string) (string, int, error) {
	timeout := m.ProbeTimeout
	if timeout <= 0 {
		timeout = 4 * time.Second
	}
	return m.runWithTimeout(ctx, executable, []string{"sdk", "status"}, directory, timeout)
}

func (m *SetupManager) runWithTimeout(ctx context.Context, executable string, args []string, directory string, timeout time.Duration) (string, int, error) {
	adapter := EnvironmentProbe
	if len(args) > 0 && args[0] == "sdk" {
		adapter = "xtool"
	}
	environment := environmentWithXDGConfigHome(adapter, m.XDGConfigHome)
	commandCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	if m.Runner != nil {
		return m.Runner.Run(commandCtx, executable, args, directory, environment)
	}
	output := &cappedBuffer{limit: 64 * 1024}
	cmd := exec.CommandContext(commandCtx, executable, args...)
	cmd.Dir = directory
	cmd.Env = environment
	cmd.Stdout = output
	cmd.Stderr = output
	configureProcessGroup(cmd)
	err := cmd.Run()
	killProcessGroup(cmd)
	if errors.Is(commandCtx.Err(), context.DeadlineExceeded) {
		return output.String(), 124, errors.New("operation timed out")
	}
	if err == nil {
		return output.String(), 0, nil
	}
	code := 127
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		code = exitErr.ExitCode()
	}
	return output.String(), code, err
}

func parseSDKStatus(output string) (installed, recognized bool) {
	normalized := strings.ToLower(strings.Join(strings.Fields(output), " "))
	if strings.Contains(normalized, "not installed") || strings.Contains(normalized, "no darwin sdk") {
		return false, true
	}
	if strings.Contains(normalized, "installed") && (strings.Contains(normalized, "sdk") || strings.Contains(normalized, "darwin")) {
		return true, true
	}
	return false, false
}

func (m *SetupManager) ProbeSDK(ctx context.Context) ToolStatus {
	status := ToolStatus{ID: "darwin-sdk", Name: "Darwin Swift SDK", Status: "missing", Detail: "xtool reports that no Darwin SDK is installed", InstallURL: "https://developer.apple.com/download/all/?q=Xcode"}
	xtool := m.Tools.Probe(ctx, "xtool")
	if xtool.Status != "available" {
		status.Detail = "SDK status requires a verified xtool 1.19.0 executable"
		return status
	}
	swift := m.Tools.Probe(ctx, "swift")
	if swift.Status != "available" {
		status.Detail = "SDK status requires Swift 6.3 or later"
		return status
	}
	output, code, err := m.runStatusProbe(ctx, xtool.Path, m.workingDirectory())
	if err != nil || code != 0 {
		status.Status = "unverified"
		status.Detail = "xtool sdk status failed"
		return status
	}
	installed, recognized := parseSDKStatus(output)
	status.Version = strings.TrimSpace(strings.Join(strings.Fields(redactOutput(output)), " "))
	if !recognized {
		status.Status = "unverified"
		status.Detail = "xtool sdk status output was not recognized"
	} else if installed {
		configHome, configErr := m.sdkConfigHome()
		if configErr != nil {
			status.Status = "unverified"
			status.Detail = "the effective private XDG_CONFIG_HOME is invalid"
			return status
		}
		listOutput, listCode, listErr := m.runWithTimeout(ctx, swift.Path, []string{"sdk", "list"}, m.workingDirectory(), 10*time.Second)
		configurationOutput, configurationCode, configurationErr := m.runWithTimeout(ctx, swift.Path, []string{"sdk", "configure", "darwin", sdkTargetTriple, "--show-configuration"}, m.workingDirectory(), 10*time.Second)
		installedBundle := filepath.Join(configHome, "swiftpm", "swift-sdks", "darwin.artifactbundle")
		activeRoot, metadataPath, _, verifyErr := verifyInstalledSDKSelection(ctx, installedBundle, listOutput, configurationOutput)
		if listErr != nil || listCode != 0 || configurationErr != nil || configurationCode != 0 || verifyErr != nil {
			status.Status = "unverified"
			status.Detail = "xtool reports an SDK, but Swift's active arm64-apple-ios root and installed swift-sdk.json could not be verified"
			return status
		}
		status.Status = "available"
		status.Detail = "Swift configured " + sdkTargetTriple + " at " + activeRoot + " from " + metadataPath + " in the verified installed Darwin artifact"
	}
	return status
}

func (m *SetupManager) workingDirectory() string {
	if info, err := os.Stat(m.StateDir); err == nil && info.IsDir() {
		return m.StateDir
	}
	return os.TempDir()
}

func safeSetupError(err error) string {
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return fmt.Sprintf("process exited with status %d", exitErr.ExitCode())
	}
	return redactOutput(err.Error())
}
