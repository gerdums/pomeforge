package orchard

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

var devicePattern = regexp.MustCompile(`^[A-Za-z0-9-]{1,128}$`)

var actionCatalog = []ActionInfo{
	{ID: "setup", Title: "Inspect setup", Description: "Inspect the installed Swift SDK setup and explain manual requirements.", Effect: "local-read"},
	{ID: "build", Title: "Build debug app", Description: "Compile a debug device application with xtool.", Effect: "local-build"},
	{ID: "devices", Title: "List devices", Description: "Enumerate connected devices without waiting.", Effect: "device-read"},
	{ID: "install", Title: "Install on device", Description: "Install an IPA or build and run a debug app on a selected device.", Effect: "device-write"},
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
	requiredTools := []string{}
	addToolStep := func(tool, description string, args ...string) {
		status := p.Tools.Probe(ctx, tool)
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
	fingerprint, err := p.fingerprint(project, input, requiredTools)
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
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() {
		return "", Errorf("invalid_ipa", "IPA path must be a regular file")
	}
	return path, nil
}

func (p Planner) fingerprint(project string, input PlanInput, tools []string) (string, error) {
	h := sha256.New()
	_, _ = io.WriteString(h, "orchard-plan-v1\x00")
	encodedInput, _ := json.Marshal(input)
	_, _ = h.Write(encodedInput)
	_, _ = io.WriteString(h, "\x00")
	err := filepath.WalkDir(project, func(path string, entry fs.DirEntry, walkErr error) error {
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
		_, _ = io.WriteString(h, filepath.ToSlash(rel)+"\x00")
		file, err := os.Open(path)
		if err != nil {
			return err
		}
		_, copyErr := io.Copy(h, file)
		closeErr := file.Close()
		if copyErr != nil {
			return copyErr
		}
		return closeErr
	})
	if err != nil {
		return "", err
	}
	if input.IPA != "" && !strings.HasPrefix(input.IPA, project+string(filepath.Separator)) {
		_, _ = io.WriteString(h, "ipa\x00"+input.IPA+"\x00")
		ipa, openErr := os.Open(input.IPA)
		if openErr != nil {
			return "", openErr
		}
		_, copyErr := io.Copy(h, ipa)
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
		status := p.Tools.Probe(context.Background(), id)
		encoded, _ := json.Marshal(status)
		_, _ = h.Write(encoded)
		if status.Path != "" {
			if info, statErr := os.Stat(status.Path); statErr == nil {
				_, _ = io.WriteString(h, fmt.Sprintf("\x00%d\x00%d", info.Size(), info.ModTime().UnixNano()))
			}
		}
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
