package orchard

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
)

var devicePattern = regexp.MustCompile(`^[A-Za-z0-9-]{1,128}$`)

const (
	maxFingerprintFileBytes  = 64 << 20
	maxFingerprintTotalBytes = 512 << 20
	maxIPAFingerprintBytes   = 8 << 30
	maxToolFingerprintBytes  = 256 << 20
)

var actionCatalog = []ActionInfo{
	{ID: "setup", Title: "Inspect setup", Description: "Inspect the installed Swift SDK setup and explain manual requirements.", Effect: "local-read"},
	{ID: "build", Title: "Build debug app", Description: "Compile a debug device application with xtool.", Effect: "local-build"},
	{ID: "devices", Title: "List devices", Description: "Enumerate connected devices without waiting.", Effect: "device-read"},
	{ID: "install", Title: "Install on device", Description: "Provision/development-sign and install an IPA, or build and run a debug app on a selected device.", Effect: "device-write", RequiresConfirmation: true},
	{ID: "launch", Title: "Launch on device", Description: "Launch the project bundle identifier on a selected device.", Effect: "device-write"},
	{ID: "export", Title: "Export unsigned IPA", Description: "Produce an unsigned release-workflow IPA; this is not App Store distribution signing.", Effect: "local-build"},
	{ID: "store-status", Title: "Inspect store build", Description: "Read App Store Connect build status for the configured build ID.", Effect: "account-read"},
	{ID: "validate", Title: "Validate store readiness", Description: "Run ASC's remote App Store version readiness validation.", Effect: "account-read"},
	{ID: "upload", Title: "Upload IPA", Description: "Upload a pre-exported, correctly distribution-signed IPA to App Store Connect.", Effect: "account-write", RequiresConfirmation: true},
	{ID: "submit", Title: "Submit for review", Description: "Dry-run and then submit a configured build for App Review.", Effect: "account-write", RequiresConfirmation: true},
}

func Actions() []ActionInfo {
	result := make([]ActionInfo, len(actionCatalog))
	copy(result, actionCatalog)
	return result
}

func actionByID(id string) (ActionInfo, bool) {
	for _, action := range actionCatalog {
		if action.ID == id {
			return action, true
		}
	}
	return ActionInfo{}, false
}

type Planner struct {
	Workspace string
	Tools     ToolResolver
	HostOS    string
}

func (p Planner) Plan(ctx context.Context, input PlanInput) (Plan, error) {
	action, ok := actionByID(input.Action)
	if !ok {
		return Plan{}, Errorf("unknown_action", "unknown action: "+input.Action)
	}
	project, err := ResolveWithin(p.Workspace, input.Project, true)
	if err != nil {
		return Plan{}, err
	}
	info, err := os.Stat(project)
	if err != nil || !info.IsDir() {
		return Plan{}, Errorf("invalid_project", "project must be a directory")
	}
	manifest, _, err := LoadManifest(project)
	if err != nil {
		return Plan{}, err
	}
	input.Project = project
	if input.Device != "" && !devicePattern.MatchString(input.Device) {
		return Plan{}, Errorf("invalid_device", "device ID may contain only letters, digits, and hyphens")
	}
	if input.IPA != "" {
		input.IPA, err = p.validateIPA(input.IPA)
		if err != nil {
			return Plan{}, err
		}
	}
	plan := Plan{
		Action: input.Action, Title: action.Title, Steps: []Step{}, Blockers: []string{}, Warnings: []string{},
		RequiresConfirmation: action.RequiresConfirmation, Effect: action.Effect,
	}
	hostOS := p.HostOS
	if hostOS == "" {
		hostOS = runtime.GOOS
	}
	if actionRequiresLinux(input.Action) && hostOS != "linux" {
		plan.Blockers = append(plan.Blockers, "this compile, signing, or export workflow is supported only when Orchard is running on Linux")
	}
	requiredTools := []string{}
	addToolStep := func(tool, description string, args ...string) {
		status := canonicalToolStatus(p.Tools.Probe(ctx, tool))
		requiredTools = append(requiredTools, tool)
		if status.Status != "available" {
			plan.Blockers = append(plan.Blockers, fmt.Sprintf("%s is %s: %s (install: %s)", status.Name, status.Status, status.Detail, status.InstallURL))
			return
		}
		plan.Steps = append(plan.Steps, Step{Tool: tool, Executable: status.Path, Args: args, Directory: project, Description: description})
	}
	switch input.Action {
	case "setup":
		addToolStep("swift", "Inspect the installed Swift compiler.", "--version")
		addToolStep("xtool", "Inspect the installed Darwin Swift SDK.", "sdk", "status")
		plan.Warnings = append(plan.Warnings, "Installing the iOS SDK requires a user-supplied Xcode.xip and an explicit `xtool sdk install /path/Xcode.xip`; Orchard does not download Apple SDKs.")
	case "build":
		addToolStep("swift", "Verify Swift before building.", "--version")
		addToolStep("xtool", "Build the debug device application.", "dev", "build", "--configuration", "debug")
		plan.Warnings = append(plan.Warnings, "Build hooks and Swift package plugins can execute project code.")
	case "devices":
		addToolStep("xtool", "Enumerate connected devices without waiting.", "devices", "--no-wait")
	case "install":
		if input.Device == "" {
			plan.Blockers = append(plan.Blockers, "a device ID is required; pass --device after inspecting the devices action")
		}
		if input.IPA != "" {
			addToolStep("xtool", "Install the selected IPA on the selected device.", "install", "--udid", input.Device, input.IPA)
		} else {
			addToolStep("xtool", "Build, install, and run a debug app on the selected device.", "dev", "run", "--configuration", "debug", "--udid", input.Device)
		}
		plan.Warnings = append(plan.Warnings, "xtool install/dev run may provision or development-sign the app and change its signing identity; it does not preserve or produce an App Store distribution signature.")
		plan.Warnings = append(plan.Warnings, "Build hooks and Swift package plugins run with the invoking user's authority.")
	case "launch":
		if input.Device == "" {
			plan.Blockers = append(plan.Blockers, "a device ID is required; pass --device after inspecting the devices action")
		}
		addToolStep("xtool", "Launch the project app on the selected device.", "launch", "--udid", input.Device, manifest.BundleIdentifier)
	case "export":
		addToolStep("xtool", "Produce an unsigned release-workflow IPA.", "dev", "build", "--configuration", "release", "--ipa")
		plan.Warnings = append(plan.Warnings, "This export is unsigned and is not ready for App Store upload. Orchard does not yet implement or validate distribution signing.")
	case "store-status":
		if manifest.AppStore.BuildID == "" {
			plan.Blockers = append(plan.Blockers, "orchard.json appStore.buildId is required")
		}
		addToolStep("asc", "Read the configured App Store Connect build.", "builds", "info", "--build-id", manifest.AppStore.BuildID, "--output", "json")
		plan.Warnings = append(plan.Warnings, "ASC account authentication is resolved from private user configuration and is validated by ASC at execution time.")
	case "validate":
		if manifest.AppStore.AppID == "" {
			plan.Blockers = append(plan.Blockers, "orchard.json appStore.appId is required")
		}
		args := []string{"validate", "--app", manifest.AppStore.AppID}
		if manifest.AppStore.VersionID != "" {
			args = append(args, "--version-id", manifest.AppStore.VersionID)
		} else {
			args = append(args, "--version", manifest.MarketingVersion)
		}
		args = append(args, "--platform", "IOS", "--output", "json")
		addToolStep("asc", "Validate App Store version readiness.", args...)
		plan.Warnings = append(plan.Warnings, "ASC account authentication is resolved from private user configuration and is validated by ASC at execution time.")
	case "upload":
		if manifest.AppStore.AppID == "" {
			plan.Blockers = append(plan.Blockers, "orchard.json appStore.appId is required")
		}
		if input.IPA == "" {
			plan.Blockers = append(plan.Blockers, "a pre-exported distribution-signed IPA is required; pass --ipa PATH")
		}
		addToolStep("asc", "Upload the confirmed IPA and wait for processing.", "builds", "upload", "--app", manifest.AppStore.AppID, "--ipa", input.IPA, "--wait", "--output", "json")
		plan.Warnings = append(plan.Warnings, "Orchard cannot yet validate distribution signing or archive provenance; only upload an independently validated App Store distribution IPA.")
		plan.Warnings = append(plan.Warnings, "ASC account authentication is resolved from private user configuration and is validated by ASC at execution time.")
	case "submit":
		if manifest.AppStore.AppID == "" || manifest.AppStore.VersionID == "" || manifest.AppStore.BuildID == "" {
			plan.Blockers = append(plan.Blockers, "orchard.json appStore.appId, versionId, and buildId are all required")
		}
		base := []string{"review", "submit", "--app", manifest.AppStore.AppID, "--version-id", manifest.AppStore.VersionID, "--build-id", manifest.AppStore.BuildID, "--platform", "IOS"}
		addToolStep("asc", "Inspect the exact App Review submission without writing.", append(append([]string{}, base...), "--dry-run", "--output", "json")...)
		addToolStep("asc", "Submit the configured build for App Review.", append(append([]string{}, base...), "--confirm", "--output", "json")...)
		plan.Warnings = append(plan.Warnings, "ASC account authentication is resolved from private user configuration and is validated by ASC at execution time.")
	}
	plan.Executable = len(plan.Blockers) == 0 && len(plan.Steps) > 0
	fingerprint, err := p.fingerprint(ctx, project, input, requiredTools)
	if err != nil {
		return Plan{}, err
	}
	plan.Fingerprint = fingerprint
	idPayload := struct {
		Action               string
		Title                string
		Steps                []Step
		Blockers             []string
		Warnings             []string
		RequiresConfirmation bool
		Executable           bool
		Effect               string
		Fingerprint          string
	}{plan.Action, plan.Title, plan.Steps, plan.Blockers, plan.Warnings, plan.RequiresConfirmation, plan.Executable, plan.Effect, fingerprint}
	encoded, _ := json.Marshal(idPayload)
	sum := sha256.Sum256(encoded)
	plan.ID = hex.EncodeToString(sum[:12])
	return plan, nil
}

func (p Planner) validateIPA(requested string) (string, error) {
	path, err := ResolveWithin(p.Workspace, requested, true)
	if err != nil {
		return "", err
	}
	if !strings.EqualFold(filepath.Ext(path), ".ipa") {
		return "", Errorf("invalid_ipa", "IPA path must end in .ipa")
	}
	file, err := openRegularWithin(p.Workspace, path, os.O_RDONLY, 0)
	if err != nil {
		return "", Errorf("invalid_ipa", "IPA path must be a regular file")
	}
	_ = file.Close()
	return path, nil
}

func actionRequiresLinux(action string) bool {
	switch action {
	case "build", "resources", "sign", "export", "install":
		return true
	default:
		return false
	}
}

func (p Planner) fingerprint(ctx context.Context, project string, input PlanInput, tools []string) (string, error) {
	h := sha256.New()
	_, _ = io.WriteString(h, "orchard-plan-v2\x00")
	encodedInput, _ := json.Marshal(input)
	_, _ = h.Write(encodedInput)
	_, _ = io.WriteString(h, "\x00")
	var projectBytes int64
	err := filepath.WalkDir(project, func(path string, entry fs.DirEntry, walkErr error) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if walkErr != nil {
			return walkErr
		}
		rel, _ := filepath.Rel(project, path)
		if entry.Type()&os.ModeSymlink != 0 {
			return Errorf("symlink_not_allowed", "project contains a symlink: "+rel)
		}
		if entry.IsDir() && (rel == ".git" || rel == ".orchard" || rel == ".build" || rel == "xtool") {
			return filepath.SkipDir
		}
		if entry.IsDir() {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return Errorf("invalid_project", "project contains a nonregular file: "+rel)
		}
		_, _ = io.WriteString(h, filepath.ToSlash(rel)+"\x00")
		file, err := openRegularWithin(project, path, os.O_RDONLY, 0)
		if err != nil {
			return err
		}
		copyErr := hashBounded(ctx, h, file, maxFingerprintFileBytes, &projectBytes, maxFingerprintTotalBytes)
		closeErr := file.Close()
		if copyErr != nil {
			return copyErr
		}
		return closeErr
	})
	if err != nil {
		return "", err
	}
	if input.IPA != "" {
		_, _ = io.WriteString(h, "ipa\x00"+input.IPA+"\x00")
		ipa, openErr := openRegularWithin(p.Workspace, input.IPA, os.O_RDONLY, 0)
		if openErr != nil {
			return "", openErr
		}
		var ipaBytes int64
		copyErr := hashBounded(ctx, h, ipa, maxIPAFingerprintBytes, &ipaBytes, maxIPAFingerprintBytes)
		closeErr := ipa.Close()
		if copyErr != nil {
			return "", copyErr
		}
		if closeErr != nil {
			return "", closeErr
		}
	}
	toolSet := map[string]bool{}
	for _, id := range tools {
		toolSet[id] = true
	}
	toolIDs := make([]string, 0, len(toolSet))
	for id := range toolSet {
		toolIDs = append(toolIDs, id)
	}
	sort.Strings(toolIDs)
	for _, id := range toolIDs {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		status := canonicalToolStatus(p.Tools.Probe(ctx, id))
		encoded, _ := json.Marshal(status)
		_, _ = h.Write(encoded)
		if status.Path != "" {
			file, openErr := openRegularAbsolute(status.Path)
			if openErr == nil {
				_, _ = io.WriteString(h, "\x00tool-bytes\x00")
				var toolBytes int64
				copyErr := hashBounded(ctx, h, file, maxToolFingerprintBytes, &toolBytes, maxToolFingerprintBytes)
				closeErr := file.Close()
				if copyErr != nil {
					return "", fmt.Errorf("fingerprint %s executable: %w", id, copyErr)
				}
				if closeErr != nil {
					return "", closeErr
				}
			} else if _, statErr := os.Lstat(status.Path); statErr == nil {
				return "", fmt.Errorf("fingerprint %s executable: %w", id, openErr)
			}
		}
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func canonicalToolStatus(status ToolStatus) ToolStatus {
	if status.Path != "" {
		if resolved, err := filepath.EvalSymlinks(status.Path); err == nil {
			status.Path = resolved
		}
	}
	return status
}

func hashBounded(ctx context.Context, destination io.Writer, source *os.File, perFileLimit int64, total *int64, totalLimit int64) error {
	info, err := source.Stat()
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return errors.New("file is not regular")
	}
	if info.Size() > perFileLimit {
		return fmt.Errorf("file exceeds the %d-byte fingerprint limit", perFileLimit)
	}
	if *total > totalLimit-info.Size() {
		return fmt.Errorf("files exceed the %d-byte total fingerprint limit", totalLimit)
	}
	buffer := make([]byte, 128*1024)
	var read int64
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		n, readErr := source.Read(buffer)
		if n > 0 {
			read += int64(n)
			if read > perFileLimit || *total > totalLimit-int64(n) {
				return errors.New("file changed while reading and exceeded fingerprint limits")
			}
			_, _ = destination.Write(buffer[:n])
			*total += int64(n)
		}
		if errors.Is(readErr, io.EOF) {
			return nil
		}
		if readErr != nil {
			return readErr
		}
	}
}
