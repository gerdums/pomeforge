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
	"regexp"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"howett.net/plist"

	"pomeforge.local/pomeforge/internal/distribution"
)

var (
	identityIDPattern = regexp.MustCompile(`^[a-z][a-z0-9-]{0,47}$`)
	uuidPattern       = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[1-8][0-9a-fA-F]{3}-[89aAbB][0-9a-fA-F]{3}-[0-9a-fA-F]{12}$`)
)

type ReleaseManager struct {
	Workspace string
	StateDir  string
	Tools     ToolResolver
	Binder    SDKBindingResolver
	Executor  *Executor
	Now       func() time.Time

	mu      sync.Mutex
	running map[string]bool
}

func NewReleaseManager(workspace, stateDir string, tools ToolResolver, executor *Executor) *ReleaseManager {
	manager := &ReleaseManager{Workspace: workspace, StateDir: stateDir, Tools: tools, Executor: executor}
	manager.Binder = NativeSDKBinder{Tools: tools, StateDir: stateDir}
	return manager
}

func isReleaseAction(action string) bool {
	switch action {
	case "signing-configure", "signing-inspect", "release-build", "icons", "export", "ipa-inspect", "store-status", "validate", "upload", "submit":
		return true
	default:
		return false
	}
}

func isReleaseWorkspaceAction(action string) bool {
	return action == "signing-configure" || action == "signing-inspect"
}

func (m *ReleaseManager) Plan(ctx context.Context, input PlanInput) (Plan, error) {
	if err := validateReleaseInputFields(input); err != nil {
		return Plan{}, err
	}
	if isReleaseWorkspaceAction(input.Action) {
		return m.planSigning(ctx, input)
	}
	return m.planProject(ctx, input)
}

func validateReleaseInputFields(input PlanInput) error {
	allowed := map[string]bool{"action": true, "project": !isReleaseWorkspaceAction(input.Action)}
	for _, field := range map[string][]string{
		"signing-configure": {"identity", "identityLabel", "privateKeyPath", "certificatePath", "profilePath", "trustedRootPaths"},
		"signing-inspect":   {"identity"},
		"release-build":     {"sourceBundle"},
		"icons":             {"iconSource"},
		"export":            {"identity", "sourceBundle", "outputIPA", "assetCatalog"},
		"ipa-inspect":       {"identity", "ipa"},
		"store-status":      {"buildId"},
		"validate":          {"appId", "versionId", "version"},
		"upload":            {"identity", "ipa", "appId"},
		"submit":            {"appId", "versionId", "buildId"},
	}[input.Action] {
		allowed[field] = true
	}
	present := map[string]bool{
		"project": input.Project != "", "ipa": input.IPA != "", "device": input.Device != "",
		"tool": input.Tool != "", "helper": input.Helper != "", "executablePath": input.ExecutablePath != "",
		"sourceRevision": input.SourceRevision != "", "assetKitRevision": input.AssetKitRevision != "",
		"inputPath": input.InputPath != "", "arch": input.Arch != "", "identity": input.Identity != "",
		"identityLabel": input.IdentityLabel != "", "privateKeyPath": input.PrivateKeyPath != "",
		"certificatePath": input.CertificatePath != "", "profilePath": input.ProfilePath != "",
		"trustedRootPaths": len(input.TrustedRootPaths) != 0, "sourceBundle": input.SourceBundle != "",
		"outputIPA": input.OutputIPA != "", "assetCatalog": input.AssetCatalog != "", "iconSource": input.IconSource != "",
		"appId": input.AppID != "", "versionId": input.VersionID != "", "buildId": input.BuildID != "", "version": input.Version != "",
	}
	for field, isPresent := range present {
		if isPresent && !allowed[field] {
			return Errorf("invalid_input", input.Action+" does not accept "+field)
		}
	}
	return nil
}

func (m *ReleaseManager) planSigning(ctx context.Context, input PlanInput) (Plan, error) {
	action, ok := actionByID(input.Action)
	if !ok {
		return Plan{}, Errorf("unknown_action", "unknown release action: "+input.Action)
	}
	if err := validateIdentityID(input.Identity); err != nil {
		return Plan{}, err
	}
	plan := Plan{Action: action.ID, Title: action.Title, Effect: action.Effect, Scope: action.Scope, RequiresConfirmation: action.RequiresConfirmation, Steps: []Step{}, Blockers: []string{}, Warnings: []string{}}
	configPath := m.identityConfigPath(input.Identity)
	switch input.Action {
	case "signing-configure":
		if err := validateIdentityLabel(input.IdentityLabel); err != nil {
			return Plan{}, err
		}
		config := signingConfigFromInput(input)
		if err := m.validatePrivateIdentityPaths(config); err != nil {
			return Plan{}, err
		}
		report, err := distribution.InspectIdentity(ctx, config, m.now(), distribution.DefaultLimits())
		if err != nil {
			return Plan{}, Errorf("invalid_identity", sanitizeIdentityError(err, append(identityPaths(config), configPath)...))
		}
		appendDistributionProblems(&plan, report.Problems)
		plan.IdentityInspection = &report
		if _, err := os.Lstat(configPath); err == nil {
			plan.Blockers = append(plan.Blockers, "identity "+input.Identity+" already exists; choose a new safe identity ID")
		} else if !errors.Is(err, os.ErrNotExist) {
			return Plan{}, Errorf("invalid_identity", "private identity state could not be inspected")
		}
		plan.Steps = append(plan.Steps, Step{Kind: "internal", Tool: "pomeforge", Operation: "create-signing-config", Parameters: map[string]string{"identity": input.Identity, "label": input.IdentityLabel}, Description: "Create a new private named identity configuration and retain only public inspection facts in the result."})
		plan.Warnings = append(plan.Warnings, trustWarning(report.Value.Profile.Trust))
		plan.Fingerprint = identityFingerprint(ctx, input.Identity, input.IdentityLabel, config)
	case "signing-inspect":
		identity, err := m.inspectNamedIdentity(ctx, input.Identity)
		if err != nil {
			plan.Blockers = append(plan.Blockers, "identity "+input.Identity+" is unavailable: "+sanitizeIdentityError(err, configPath)+"; configure it with `pomeforge signing configure`")
		} else {
			plan.IdentityInspection = &identity
			appendDistributionProblems(&plan, identity.Problems)
			plan.Warnings = append(plan.Warnings, trustWarning(identity.Value.Profile.Trust))
			if privateFingerprint, fingerprintErr := m.namedIdentityContentFingerprint(ctx, input.Identity); fingerprintErr == nil {
				plan.Fingerprint = digestStrings(input.Action, input.Identity, privateFingerprint)
			} else {
				return Plan{}, Errorf("invalid_identity", "named identity changed while it was being inspected; inspect it again")
			}
		}
		plan.Steps = append(plan.Steps, Step{Kind: "internal", Tool: "pomeforge", Operation: "inspect-signing-identity", Parameters: map[string]string{"identity": input.Identity}, Description: "Reload the private configuration and return only public certificate, profile, entitlement, and trust facts."})
	}
	plan.Executable = len(plan.Blockers) == 0
	plan.ID = releasePlanID(plan)
	return plan, nil
}

func (m *ReleaseManager) planProject(ctx context.Context, input PlanInput) (Plan, error) {
	action, ok := actionByID(input.Action)
	if !ok || action.Scope != "project" {
		return Plan{}, Errorf("unknown_action", "unknown release action: "+input.Action)
	}
	project, err := ResolveWithin(m.Workspace, input.Project, true)
	if err != nil {
		return Plan{}, err
	}
	manifest, _, err := LoadManifest(project)
	if err != nil {
		return Plan{}, err
	}
	input.Project = project
	plan := Plan{Action: action.ID, Title: action.Title, Effect: action.Effect, Scope: action.Scope, RequiresConfirmation: action.RequiresConfirmation, Steps: []Step{}, Blockers: []string{}, Warnings: []string{}, ProjectLabel: manifest.Name}
	if relative, relErr := filepath.Rel(m.Workspace, project); relErr == nil {
		plan.Project = filepath.ToSlash(relative)
	}
	if runtime.GOOS != "linux" && (input.Action == "release-build" || input.Action == "export") {
		plan.Blockers = append(plan.Blockers, "native release build and export are supported only on Linux")
	}
	var requiredTools []string
	addTool := func(id, description string, args ...string) ToolStatus {
		status := canonicalToolStatus(m.Tools.Probe(ctx, id))
		requiredTools = append(requiredTools, id)
		if status.Status != "available" {
			plan.Blockers = append(plan.Blockers, fmt.Sprintf("%s is %s: %s", status.Name, status.Status, status.Detail))
			return status
		}
		plan.Steps = append(plan.Steps, Step{Kind: "process", Tool: id, Executable: status.Path, Args: args, Directory: project, Description: description})
		return status
	}

	switch input.Action {
	case "release-build":
		bundle, err := m.projectPath(project, input.SourceBundle, filepath.Join("xtool", manifest.Name+".app"), false)
		if err != nil {
			return Plan{}, err
		}
		input.SourceBundle = bundle
		packageData, readErr := os.ReadFile(filepath.Join(project, "Package.swift"))
		if readErr != nil || !hasSDKLinkerHook(string(packageData)) {
			plan.Blockers = append(plan.Blockers, "Package.swift lacks the documented POMEFORGE_IOS_SDK_ROOT linker hook; generated projects include it and existing projects must opt in before release builds")
		}
		binding, bindingBlockers := m.Binder.Resolve(ctx)
		plan.SDKBinding = &binding
		plan.Blockers = append(plan.Blockers, bindingBlockers...)
		addTool("swift", "Verify Swift before the selected release build.", "--version")
		addTool("xtool", "Build the unsigned release application bundle with the explicitly bound active SDK.", "dev", "build", "--configuration", "release")
		plan.Steps = append(plan.Steps, Step{Kind: "internal", Tool: "pomeforge", Operation: "apply-release-metadata", Parameters: map[string]string{"bundle": relativeDisplay(project, bundle)}, Description: "Populate DTPlatform/DTSDK/DTXcode fields from verified named provenance, then inspect the real Mach-O SDK and deployment stamps."})
		plan.Warnings = append(plan.Warnings, "The output is an unsigned .app build artifact. Run the separate signed export action only after inspecting this result.")
		if catalogHasStarterArtwork(filepath.Join(project, "Assets.xcassets")) {
			plan.Warnings = append(plan.Warnings, "The generated catalog contains Pomeforge starter artwork. It is technically complete, but replace it with project artwork before a real release.")
		}
	case "icons":
		source, err := m.projectPath(project, input.IconSource, "", true)
		if err != nil {
			return Plan{}, Errorf("invalid_icon", "--icon-source must name a workspace-confined 1024px PNG")
		}
		input.IconSource = source
		catalog, err := m.projectPath(project, filepath.Join("Assets.xcassets", "AppIcon.appiconset"), "", true)
		if err != nil {
			return Plan{}, Errorf("invalid_icon_catalog", "generated AppIcon catalog is missing or unsafe")
		}
		marker, markerErr := openRegularWithin(project, filepath.Join(catalog, placeholderMarker), os.O_RDONLY, 0)
		if markerErr != nil {
			plan.Blockers = append(plan.Blockers, "the Pomeforge starter-art ownership marker is absent; Pomeforge will not overwrite a customized icon set")
		} else {
			_ = marker.Close()
		}
		if err := validateIconSource(project, source); err != nil {
			plan.Blockers = append(plan.Blockers, err.Error())
		}
		plan.Steps = append(plan.Steps, Step{Kind: "internal", Tool: "pomeforge", Operation: "replace-placeholder-icons", Parameters: map[string]string{"source": relativeDisplay(project, source)}, Description: "Decode the 1024px PNG and deterministically generate every explicit supported iPhone, iPad, and marketing size."})
	case "export":
		bundle, output, catalog, identityConfig, err := m.exportPaths(project, manifest, input)
		if err != nil {
			return Plan{}, err
		}
		input.SourceBundle, input.OutputIPA, input.AssetCatalog = bundle, output, catalog
		stepCount := len(plan.Steps)
		zsign := addTool("zsign", "Sign a private staged copy with zsign through Pomeforge's distribution runner.")
		assets := ToolStatus{}
		if catalog != "" {
			assets = addTool("pomeforge-assets", "Compile the selected catalog into the private staged application before signing.")
		}
		plan.Steps = plan.Steps[:stepCount]
		request := distribution.ExportRequest{SourceBundle: bundle, OutputIPA: output, IdentityConfigPath: identityConfig, ZsignExecutable: zsign.Path, AssetCompilerExecutable: assets.Path, AssetCatalogPath: catalog, MinimumIOS: manifest.MinimumIOSVersion, ExpectedBundleID: manifest.BundleIdentifier, ExpectedVersion: manifest.MarketingVersion, ExpectedBuild: manifest.BuildNumber, CurrentTime: m.now()}
		if catalog != "" && catalogHasStarterArtwork(catalog) {
			plan.Warnings = append(plan.Warnings, "The selected catalog contains Pomeforge starter artwork. Export may proceed because the catalog is complete; use the icons action to customize it.")
		}
		if _, identityErr := m.inspectNamedIdentity(ctx, input.Identity); identityErr != nil {
			return Plan{}, Errorf("invalid_identity", "selected identity is invalid: "+m.sanitizeNamedIdentityError(ctx, input.Identity, identityErr))
		}
		toolsReady := zsign.Status == "available" && (catalog == "" || assets.Status == "available")
		if toolsReady {
			exportPlan, exportErr := distribution.PlanExport(ctx, request)
			if exportErr != nil {
				return Plan{}, Errorf("export_preflight_failed", m.sanitizeNamedIdentityError(ctx, input.Identity, exportErr))
			}
			plan.DistributionExport = &exportPlan.Value
			appendDistributionProblems(&plan, exportPlan.Problems)
			plan.Steps = publicExportSteps(plan.Steps, exportPlan.Value.Actions, project)
		} else {
			plan.Warnings = append(plan.Warnings, "Complete native distribution preflight will run after the named identity and managed signing helpers are available.")
		}
		binding, bindingBlockers := m.Binder.Resolve(ctx)
		plan.SDKBinding = &binding
		plan.Blockers = append(plan.Blockers, bindingBlockers...)
		if sourceInspection, inspectErr := distribution.InspectBundle(ctx, bundle, distribution.InspectionOptions{CurrentTime: m.now()}); inspectErr != nil {
			plan.Blockers = append(plan.Blockers, "source bundle SDK facts could not be inspected")
		} else {
			plan.Blockers = append(plan.Blockers, sdkInspectionMismatch(sourceInspection.Value, binding)...)
		}
		plan.Warnings = append(plan.Warnings, "The source application and catalog are preserved. Local output records AppleProcessing:not_checked; upload and Apple processing are separate confirmed operations.")
	case "ipa-inspect":
		ipa, err := m.projectPath(project, firstNonempty(input.IPA, input.OutputIPA), "", true)
		if err != nil || !strings.EqualFold(filepath.Ext(ipa), ".ipa") {
			return Plan{}, Errorf("invalid_ipa", "--ipa must name an existing workspace-confined IPA")
		}
		input.IPA = ipa
		options, err := m.inspectionOptions(ctx, input.Identity)
		if err != nil {
			return Plan{}, err
		}
		inspection, err := distribution.InspectIPA(ctx, ipa, options)
		if err != nil {
			return Plan{}, Errorf("invalid_ipa", sanitizeIdentityError(err))
		}
		plan.IPAInspection = &inspection.Value
		for _, problem := range inspection.Problems {
			plan.Warnings = append(plan.Warnings, formatProblem(problem))
		}
		plan.Steps = append(plan.Steps, Step{Kind: "internal", Tool: "pomeforge", Operation: "inspect-ipa", Parameters: map[string]string{"ipa": relativeDisplay(project, ipa)}, Description: "Repeat bounded IPA inspection and record the exact SHA-256 and public structural facts."})
	case "upload":
		if err := m.planUpload(ctx, &plan, &input, project, manifest, addTool); err != nil {
			return Plan{}, err
		}
	case "store-status", "validate", "submit":
		if err := m.planStoreReadOrSubmit(&plan, input, manifest, addTool); err != nil {
			return Plan{}, err
		}
	}

	plan.Executable = len(plan.Blockers) == 0 && len(plan.Steps) > 0
	fingerprint, err := (Planner{Workspace: m.Workspace, Tools: m.Tools}).fingerprint(ctx, project, input, requiredTools)
	if err != nil {
		return Plan{}, err
	}
	if input.SourceBundle != "" {
		if digest, digestErr := digestDirectory(ctx, input.SourceBundle); digestErr == nil {
			fingerprint = digestStrings(fingerprint, "source-bundle", digest)
		} else if input.Action == "export" {
			return Plan{}, digestErr
		}
	}
	if input.AssetCatalog != "" {
		if digest, digestErr := digestDirectory(ctx, input.AssetCatalog); digestErr == nil {
			fingerprint = digestStrings(fingerprint, "asset-catalog", digest)
		} else if input.Action == "export" {
			return Plan{}, digestErr
		}
	}
	if input.IconSource != "" {
		if digest, digestErr := digestRegularFile(ctx, input.IconSource, maxFingerprintFileBytes); digestErr == nil {
			fingerprint = digestStrings(fingerprint, "icon-source", digest)
		} else if input.Action == "icons" {
			return Plan{}, digestErr
		}
	}
	if input.Identity != "" {
		if digest, digestErr := m.namedIdentityContentFingerprint(ctx, input.Identity); digestErr == nil {
			fingerprint = digestStrings(fingerprint, "identity", digest)
		} else {
			fingerprint = digestStrings(fingerprint, "identity-unavailable")
		}
	}
	if plan.SDKBinding != nil {
		receiptDigest := "missing"
		if plan.SDKBinding.ReceiptPath != "" {
			if digest, digestErr := digestRegularFile(ctx, plan.SDKBinding.ReceiptPath, maxSDKBindingFileBytes); digestErr == nil {
				receiptDigest = digest
			}
		}
		fingerprint = digestStrings(fingerprint, plan.SDKBinding.SDKRoot, receiptDigest, plan.SDKBinding.XDGConfigHome, plan.SDKBinding.SwiftExecutable, plan.SDKBinding.SwiftSHA256, plan.SDKBinding.ClangExecutable, plan.SDKBinding.ClangSHA256)
	}
	plan.Fingerprint = fingerprint
	plan.ID = releasePlanID(plan)
	return plan, nil
}

func (m *ReleaseManager) planUpload(ctx context.Context, plan *Plan, input *PlanInput, project string, manifest Manifest, addTool func(string, string, ...string) ToolStatus) error {
	appID := firstNonempty(input.AppID, manifest.AppStore.AppID)
	if !identifierPattern.MatchString(appID) {
		plan.Blockers = append(plan.Blockers, "a numeric App Store app ID is required through --app-id or pomeforge.json")
	}
	requestedIPA := firstNonempty(input.IPA, input.OutputIPA)
	if requestedIPA == "" {
		plan.Blockers = append(plan.Blockers, "upload requires an existing distribution-signed --ipa; run the export action first")
		addTool("asc", "Upload the exact inspected IPA bytes and wait for App Store Connect processing.", "builds", "upload", "--app", appID, "--ipa", "<selected-ipa>", "--wait", "--output", "json")
		return nil
	}
	ipa, err := m.projectPath(project, requestedIPA, "", true)
	if err != nil || !strings.EqualFold(filepath.Ext(ipa), ".ipa") {
		return Errorf("invalid_ipa", "upload requires an existing workspace-confined --ipa")
	}
	input.IPA = ipa
	options, err := m.inspectionOptions(ctx, input.Identity)
	if err != nil {
		return err
	}
	inspection, err := distribution.InspectIPA(ctx, ipa, options)
	if err != nil {
		return Errorf("invalid_ipa", sanitizeIdentityError(err))
	}
	plan.IPAInspection = &inspection.Value
	appendDistributionProblems(plan, inspection.Problems)
	bundle := inspection.Value.Bundle
	if bundle.BundleIdentifier != manifest.BundleIdentifier || bundle.MarketingVersion != manifest.MarketingVersion || bundle.BuildNumber != manifest.BuildNumber {
		plan.Blockers = append(plan.Blockers, "selected IPA bundle identifier, marketing version, or build number does not match pomeforge.json")
	}
	if input.Identity == "" {
		plan.Blockers = append(plan.Blockers, "upload requires --identity so the exact embedded App Store profile and explicit trust roots can be bound")
	} else if named, namedErr := m.inspectNamedIdentity(ctx, input.Identity); namedErr != nil {
		plan.Blockers = append(plan.Blockers, "selected identity is unavailable; configure or inspect it before upload")
	} else if bundle.EmbeddedProfile == nil || bundle.EmbeddedProfile.SHA256 != named.Value.Profile.SHA256 {
		plan.Blockers = append(plan.Blockers, "selected IPA does not embed the exact profile from the named identity")
	}
	binding, bindingBlockers := m.Binder.Resolve(ctx)
	plan.SDKBinding = &binding
	plan.Blockers = append(plan.Blockers, bindingBlockers...)
	plan.Blockers = append(plan.Blockers, sdkInspectionMismatch(bundle, binding)...)
	addTool("asc", "Upload the exact inspected IPA bytes and wait for App Store Connect processing.", "builds", "upload", "--app", appID, "--ipa", ipa, "--wait", "--output", "json")
	plan.Warnings = append(plan.Warnings, "This confirmed action uploads only; it never submits for review automatically.")
	return nil
}

func (m *ReleaseManager) planStoreReadOrSubmit(plan *Plan, input PlanInput, manifest Manifest, addTool func(string, string, ...string) ToolStatus) error {
	appID := firstNonempty(input.AppID, manifest.AppStore.AppID)
	versionID := input.VersionID
	if versionID == "" && input.Version == "" {
		versionID = manifest.AppStore.VersionID
	}
	buildID := firstNonempty(input.BuildID, manifest.AppStore.BuildID)
	version := firstNonempty(input.Version, manifest.MarketingVersion)
	if input.AppID != "" && !identifierPattern.MatchString(input.AppID) {
		return Errorf("invalid_app_id", "--app-id must be numeric")
	}
	if input.VersionID != "" && !uuidPattern.MatchString(input.VersionID) {
		return Errorf("invalid_version_id", "--version-id must be a UUID-compatible App Store resource ID")
	}
	if input.BuildID != "" && !uuidPattern.MatchString(input.BuildID) {
		return Errorf("invalid_build_id", "--build-id must be a UUID-compatible App Store resource ID")
	}
	switch input.Action {
	case "store-status":
		if !uuidPattern.MatchString(buildID) {
			plan.Blockers = append(plan.Blockers, "a UUID-compatible App Store build ID is required through --build-id or pomeforge.json")
		}
		addTool("asc", "Read the exact App Store Connect build status.", "builds", "info", "--build-id", buildID, "--output", "json")
	case "validate":
		if !identifierPattern.MatchString(appID) {
			plan.Blockers = append(plan.Blockers, "a numeric App Store app ID is required through --app-id or pomeforge.json")
		}
		if input.VersionID != "" && input.Version != "" {
			return Errorf("invalid_input", "validate accepts exactly one of --version-id or --version")
		}
		args := []string{"validate", "--app", appID}
		if versionID != "" {
			if !uuidPattern.MatchString(versionID) {
				plan.Blockers = append(plan.Blockers, "configured version ID is not UUID-compatible; provide --version or a valid --version-id")
			}
			args = append(args, "--version-id", versionID)
		} else {
			args = append(args, "--version", version)
		}
		addTool("asc", "Run ASC remote readiness validation for exactly one selected version.", append(args, "--platform", "IOS", "--output", "json")...)
	case "submit":
		if !identifierPattern.MatchString(appID) || !uuidPattern.MatchString(versionID) || !uuidPattern.MatchString(buildID) {
			plan.Blockers = append(plan.Blockers, "submit requires a numeric app ID plus UUID-compatible version and build IDs")
		}
		base := []string{"review", "submit", "--app", appID, "--version-id", versionID, "--build-id", buildID, "--platform", "IOS"}
		addTool("asc", "Preview the exact review submission without external mutation.", append(append([]string{}, base...), "--dry-run")...)
		addTool("asc", "Submit only after the separately confirmed preview succeeds.", append(append([]string{}, base...), "--confirm")...)
	}
	plan.Warnings = append(plan.Warnings, "ASC authentication is isolated to this adapter and validated by ASC at execution time.")
	return nil
}

func (m *ReleaseManager) Execute(ctx context.Context, plan Plan, input PlanInput, confirm bool) (OperationResult, error) {
	if !plan.Executable {
		return OperationResult{}, Errorf("blocked", "operation is blocked: "+strings.Join(plan.Blockers, "; "))
	}
	if plan.RequiresConfirmation && !confirm {
		return OperationResult{}, Errorf("confirmation_required", "this external-write operation requires explicit confirmation")
	}
	if input.Action == "store-status" || input.Action == "validate" || input.Action == "upload" || input.Action == "submit" {
		project, err := ResolveWithin(m.Workspace, input.Project, true)
		if err != nil {
			return OperationResult{}, err
		}
		return m.Executor.Execute(ctx, plan, project, confirm)
	}
	key := input.Project + "\x00" + input.Action
	if isReleaseWorkspaceAction(input.Action) {
		key = m.Workspace + "\x00" + input.Action
	}
	m.mu.Lock()
	if m.running == nil {
		m.running = map[string]bool{}
	}
	if m.running[key] {
		m.mu.Unlock()
		return OperationResult{}, Errorf("operation_in_progress", "the same release operation is already running")
	}
	m.running[key] = true
	m.mu.Unlock()
	defer func() {
		m.mu.Lock()
		delete(m.running, key)
		m.mu.Unlock()
	}()

	historyWorkspace, historyProject, err := m.historyRoot(input)
	if err != nil {
		return OperationResult{}, err
	}
	if err := ensureHistory(historyWorkspace, historyProject); err != nil {
		return OperationResult{}, Errorf("history_failed", "operation was not started because private history is unavailable: "+err.Error())
	}
	result := OperationResult{ID: operationID(), Action: input.Action, Status: "succeeded", Scope: plan.Scope, Project: plan.Project, ProjectLabel: plan.ProjectLabel, StartedAt: m.now().UTC()}
	var value any
	var runErr error
	switch input.Action {
	case "signing-configure":
		value, runErr = m.executeSigningConfigure(ctx, input)
	case "signing-inspect":
		value, runErr = m.inspectNamedIdentity(ctx, input.Identity)
	case "release-build":
		input.SourceBundle, runErr = m.projectPath(historyProject, input.SourceBundle, filepath.Join("xtool", plan.ProjectLabel+".app"), false)
		if runErr == nil {
			value, runErr = m.executeReleaseBuild(ctx, plan, input, historyProject)
		}
	case "icons":
		input.IconSource, runErr = m.projectPath(historyProject, input.IconSource, "", true)
		if runErr == nil {
			runErr = replacePlaceholderIcons(historyProject, input.IconSource)
		}
		if runErr == nil {
			value = map[string]string{"status": "custom_icon_catalog_generated", "catalog": "Assets.xcassets"}
		}
	case "export":
		value, runErr = m.executeExport(ctx, input, historyProject)
	case "ipa-inspect":
		input.IPA, runErr = m.projectPath(historyProject, firstNonempty(input.IPA, input.OutputIPA), "", true)
		options, optionsErr := m.inspectionOptions(ctx, input.Identity)
		if optionsErr != nil {
			runErr = optionsErr
		} else if runErr == nil {
			value, runErr = distribution.InspectIPA(ctx, input.IPA, options)
		}
	default:
		runErr = errors.New("unknown release operation")
	}
	if value != nil {
		encoded, _ := json.MarshalIndent(value, "", "  ")
		result.Output = string(encoded) + "\n"
	}
	if runErr != nil {
		result.Status = "failed"
		result.ExitCode = 1
		result.Output += "operation failed: " + sanitizeReleaseError(runErr, input) + "\n"
	}
	result.Output = redactOutput(result.Output)
	result.FinishedAt = m.now().UTC()
	if historyErr := appendHistory(historyWorkspace, historyProject, result); historyErr != nil {
		return result, ErrorWithResult("history_failed", "operation completed, but its private history receipt could not be stored: "+historyErr.Error(), result)
	}
	return result, nil
}

func (m *ReleaseManager) executeSigningConfigure(ctx context.Context, input PlanInput) (SigningIdentity, error) {
	config := signingConfigFromInput(input)
	report, err := distribution.InspectIdentity(ctx, config, m.now(), distribution.DefaultLimits())
	if err != nil {
		return SigningIdentity{}, err
	}
	if !report.Valid() {
		return SigningIdentity{ID: input.Identity, Label: input.IdentityLabel, Status: "blocked", Report: &report.Value, Problems: report.Problems}, errors.New("identity validation has blocking problems")
	}
	directory := filepath.Join(m.StateDir, "signing")
	if err := ensurePrivateDirectory(directory); err != nil {
		return SigningIdentity{}, err
	}
	config.Metadata = map[string]string{"label": input.IdentityLabel}
	if err := distribution.CreateSigningConfig(ctx, m.identityConfigPath(input.Identity), config); err != nil {
		return SigningIdentity{}, err
	}
	return SigningIdentity{ID: input.Identity, Label: input.IdentityLabel, Status: "ready", Report: &report.Value, Problems: report.Problems}, nil
}

func (m *ReleaseManager) executeReleaseBuild(ctx context.Context, plan Plan, input PlanInput, project string) (any, error) {
	if plan.SDKBinding == nil || plan.SDKBinding.SDKRoot == "" {
		return distribution.Result[distribution.BundleReport]{}, errors.New("validated SDK binding is absent")
	}
	for _, step := range plan.Steps {
		if step.Kind == "process" && (plan.SDKBinding.SwiftExecutable != "" || plan.SDKBinding.ClangExecutable != "") {
			if err := verifyReleaseBindingTools(ctx, *plan.SDKBinding); err != nil {
				return distribution.Result[distribution.BundleReport]{}, err
			}
			break
		}
	}
	combined := &cappedBuffer{limit: 64 * 1024}
	for _, step := range plan.Steps {
		if step.Kind != "process" {
			continue
		}
		environment := environmentWithXDGConfigHome(step.Tool, plan.SDKBinding.XDGConfigHome)
		if step.Tool == "xtool" {
			environment = environmentWithSDK(environment, plan.SDKBinding.SDKRoot)
		}
		captured := &cappedBuffer{limit: 64 * 1024}
		if _, _, err := runBoundedProcess(ctx, step.Executable, step.Args, step.Directory, environment, 30*time.Minute, captured); err != nil {
			safe := strings.ReplaceAll(captured.String(), plan.SDKBinding.SDKRoot, "[sdk-root]")
			_, _ = combined.Write([]byte("[" + step.Tool + "]\n" + safe))
			return map[string]any{"toolOutput": combined.String()}, fmt.Errorf("%s failed", step.Tool)
		}
		safe := strings.ReplaceAll(captured.String(), plan.SDKBinding.SDKRoot, "[sdk-root]")
		_, _ = combined.Write([]byte("[" + step.Tool + "]\n" + safe))
	}
	if err := applyReleaseMetadata(ctx, project, input.SourceBundle, *plan.SDKBinding); err != nil {
		return distribution.Result[distribution.BundleReport]{}, err
	}
	inspection, err := distribution.InspectBundle(ctx, input.SourceBundle, distribution.InspectionOptions{CurrentTime: m.now()})
	if err != nil {
		return map[string]any{"inspection": inspection, "toolOutput": combined.String()}, err
	}
	allowDeferredIcons := false
	if catalog, catalogErr := m.projectPath(project, "Assets.xcassets", "", true); catalogErr == nil {
		if info, statErr := os.Stat(catalog); statErr == nil && info.IsDir() {
			allowDeferredIcons = true
		}
	}
	if blockers := releaseBuildBlockingProblems(inspection.Problems, allowDeferredIcons); len(blockers) != 0 {
		return map[string]any{"inspection": inspection, "toolOutput": combined.String(), "releaseBuildReadiness": "blocked"}, errors.New("built release bundle has blocking non-asset problems; inspect the reported problems")
	}
	return map[string]any{"inspection": inspection, "toolOutput": combined.String(), "releaseBuildReadiness": "unsigned_pre_assetkit"}, nil
}

func verifyReleaseBindingTools(ctx context.Context, binding SDKBindingReport) error {
	effectiveConfig, err := effectiveXDGConfigHome()
	if err != nil || effectiveConfig != binding.XDGConfigHome {
		return errors.New("effective XDG_CONFIG_HOME changed after release planning")
	}
	for label, selected := range map[string]struct {
		path   string
		digest string
	}{
		"Swift": {binding.SwiftExecutable, binding.SwiftSHA256},
		"Clang": {binding.ClangExecutable, binding.ClangSHA256},
	} {
		if selected.path == "" || selected.digest == "" {
			return fmt.Errorf("validated %s binding is incomplete", label)
		}
		digest, hashErr := hashExecutableForPlan(ctx, selected.path)
		if hashErr != nil || !strings.EqualFold(digest, selected.digest) {
			return fmt.Errorf("selected %s executable bytes or invocation alias changed after release planning", label)
		}
	}
	return nil
}

func releaseBuildBlockingProblems(problems []distribution.Problem, allowDeferredIcons bool) []distribution.Problem {
	deferred := map[string]bool{"missing_icon_metadata": true, "invalid_primary_icon_name": true, "incomplete_icon_metadata": true, "unsafe_icon_metadata": true, "missing_icon_file": true, "invalid_icon_png": true}
	var blockers []distribution.Problem
	for _, problem := range problems {
		if problem.Severity == distribution.SeverityWarning {
			continue
		}
		if !allowDeferredIcons || !deferred[problem.Code] {
			blockers = append(blockers, problem)
		}
	}
	return blockers
}

func catalogHasStarterArtwork(catalog string) bool {
	marker := filepath.Join(catalog, "AppIcon.appiconset", placeholderMarker)
	info, err := os.Lstat(marker)
	return err == nil && info.Mode().IsRegular() && info.Mode()&os.ModeSymlink == 0
}

func (m *ReleaseManager) executeExport(ctx context.Context, input PlanInput, project string) (any, error) {
	manifest, _, err := LoadManifest(project)
	if err != nil {
		return distribution.Result[distribution.ExportReceipt]{}, err
	}
	bundle, output, catalog, identityConfig, err := m.exportPaths(project, manifest, input)
	if err != nil {
		return distribution.Result[distribution.ExportReceipt]{}, err
	}
	zsign := canonicalToolStatus(m.Tools.Probe(ctx, "zsign"))
	assets := ToolStatus{}
	if catalog != "" {
		assets = canonicalToolStatus(m.Tools.Probe(ctx, "pomeforge-assets"))
	}
	request := distribution.ExportRequest{SourceBundle: bundle, OutputIPA: output, IdentityConfigPath: identityConfig, ZsignExecutable: zsign.Path, AssetCompilerExecutable: assets.Path, AssetCatalogPath: catalog, MinimumIOS: manifest.MinimumIOSVersion, ExpectedBundleID: manifest.BundleIdentifier, ExpectedVersion: manifest.MarketingVersion, ExpectedBuild: manifest.BuildNumber, CurrentTime: m.now()}
	runner := &distributionRunner{}
	result, err := distribution.Export(ctx, request, runner)
	if err != nil {
		return map[string]any{"export": result, "toolOutput": runner.Output()}, errors.New(m.sanitizeNamedIdentityError(ctx, input.Identity, err))
	}
	if !result.Valid() {
		return map[string]any{"export": result, "toolOutput": runner.Output()}, errors.New("native export validation failed; inspect the reported problems")
	}
	return map[string]any{"export": result, "toolOutput": runner.Output()}, nil
}

type distributionRunner struct {
	output cappedBuffer
}

func (r *distributionRunner) Run(ctx context.Context, action distribution.Action) error {
	if r.output.limit == 0 {
		r.output.limit = 64 * 1024
	}
	_, _ = r.output.Write([]byte("[" + filepath.Base(action.Executable) + "]\n"))
	captured := &cappedBuffer{limit: 64 * 1024}
	_, code, err := runBoundedProcess(ctx, action.Executable, action.Args, action.Directory, ChildEnvironmentFor(filepath.Base(action.Executable)), 15*time.Minute, captured)
	safe := captured.String()
	privatePaths := []string{action.Directory}
	for _, privateArgument := range action.Args {
		if filepath.IsAbs(privateArgument) {
			privatePaths = append(privatePaths, privateArgument)
		}
	}
	sort.Slice(privatePaths, func(i, j int) bool { return len(privatePaths[i]) > len(privatePaths[j]) })
	for _, privatePath := range privatePaths {
		if privatePath != "" {
			safe = strings.ReplaceAll(safe, privatePath, "[private-path]")
		}
	}
	_, _ = r.output.Write([]byte(safe))
	if err != nil {
		return fmt.Errorf("tool exited with status %d", code)
	}
	return nil
}

func (r *distributionRunner) Output() string { return r.output.String() }

func runBoundedProcess(ctx context.Context, executable string, args []string, directory string, environment []string, timeout time.Duration, output *cappedBuffer) (string, int, error) {
	if output == nil {
		output = &cappedBuffer{limit: 64 * 1024}
	}
	commandCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
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
	return output.String(), code, errors.New("tool execution failed")
}

func applyReleaseMetadata(ctx context.Context, project, bundle string, binding SDKBindingReport) error {
	inspection, err := distribution.InspectBundle(ctx, bundle, distribution.InspectionOptions{})
	if err != nil {
		return err
	}
	if inspection.Value.MachO.LoadCommand != "LC_BUILD_VERSION" || inspection.Value.MachO.SDKVersion == "" || !versionsEqual(inspection.Value.MachO.SDKVersion, binding.SDKVersion) {
		return errors.New("built Mach-O SDK version does not match the validated active SDK; the linker hook may be missing or stale")
	}
	manifest, _, err := LoadManifest(project)
	if err != nil {
		return err
	}
	if !versionsEqual(inspection.Value.MachO.MinimumOS, manifest.MinimumIOSVersion) {
		return errors.New("built Mach-O minimum iOS version does not match pomeforge.json")
	}
	dtXcode, err := encodeDTXcodeVersion(binding.XcodeVersion)
	if err != nil {
		return err
	}
	values := map[string]string{"DTPlatformName": "iphoneos", "DTPlatformVersion": binding.PlatformVersion, "DTPlatformBuild": binding.PlatformBuildVersion, "DTSDKName": binding.SDKCanonicalName, "DTSDKBuild": binding.SDKBuildVersion, "DTXcode": dtXcode, "DTXcodeBuild": binding.XcodeBuildVersion}
	return rewriteBundleInfoFile(project, bundle, func(data []byte) ([]byte, error) {
		var info map[string]any
		if _, err := plist.Unmarshal(data, &info); err != nil {
			return nil, err
		}
		for key, value := range values {
			if value == "" {
				return nil, fmt.Errorf("verified SDK provenance is missing %s", key)
			}
			info[key] = value
		}
		return plist.Marshal(info, plist.XMLFormat)
	})
}

func (m *ReleaseManager) ListIdentities(ctx context.Context) []SigningIdentity {
	root := filepath.Join(m.StateDir, "signing")
	entries, err := os.ReadDir(root)
	if err != nil {
		return []SigningIdentity{}
	}
	identities := make([]SigningIdentity, 0)
	for _, entry := range entries {
		if entry.IsDir() || entry.Type()&os.ModeSymlink != 0 || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		id := strings.TrimSuffix(entry.Name(), ".json")
		if validateIdentityID(id) != nil {
			continue
		}
		config, loadErr := distribution.LoadSigningConfig(ctx, m.identityConfigPath(id), distribution.DefaultLimits())
		label := id
		if loadErr == nil && config.Metadata["label"] != "" {
			label = config.Metadata["label"]
		}
		if loadErr == nil {
			loadErr = m.validatePrivateIdentityPaths(config)
		}
		if loadErr == nil {
			if labelErr := validateIdentityLabel(label); labelErr != nil {
				loadErr = labelErr
				label = id
			}
		}
		item := SigningIdentity{ID: id, Label: label, Status: "blocked"}
		if loadErr != nil {
			item.Error = sanitizeIdentityError(loadErr, m.identityConfigPath(id))
		} else if report, inspectErr := distribution.InspectIdentity(ctx, config, m.now(), distribution.DefaultLimits()); inspectErr != nil {
			item.Error = sanitizeIdentityError(inspectErr, identityPaths(config)...)
		} else {
			item.Report = &report.Value
			item.Problems = report.Problems
			if report.Valid() {
				item.Status = "ready"
			}
		}
		identities = append(identities, item)
	}
	sort.Slice(identities, func(i, j int) bool { return identities[i].ID < identities[j].ID })
	return identities
}

func (m *ReleaseManager) inspectNamedIdentity(ctx context.Context, id string) (distribution.Result[distribution.IdentityReport], error) {
	if err := validateIdentityID(id); err != nil {
		return distribution.Result[distribution.IdentityReport]{}, err
	}
	path := m.identityConfigPath(id)
	config, err := distribution.LoadSigningConfig(ctx, path, distribution.DefaultLimits())
	if err != nil {
		return distribution.Result[distribution.IdentityReport]{}, errors.New(sanitizeIdentityError(err, path))
	}
	if err := m.validatePrivateIdentityPaths(config); err != nil {
		return distribution.Result[distribution.IdentityReport]{}, err
	}
	result, err := distribution.InspectIdentity(ctx, config, m.now(), distribution.DefaultLimits())
	if err != nil {
		return result, errors.New(sanitizeIdentityError(err, identityPaths(config)...))
	}
	return result, nil
}

func (m *ReleaseManager) sanitizeNamedIdentityError(ctx context.Context, id string, err error) string {
	paths := []string{m.identityConfigPath(id)}
	if config, loadErr := distribution.LoadSigningConfig(ctx, m.identityConfigPath(id), distribution.DefaultLimits()); loadErr == nil {
		paths = append(paths, identityPaths(config)...)
	}
	return sanitizeIdentityError(err, paths...)
}

func (m *ReleaseManager) inspectionOptions(ctx context.Context, identity string) (distribution.InspectionOptions, error) {
	options := distribution.InspectionOptions{CurrentTime: m.now()}
	if identity == "" {
		return options, nil
	}
	if err := validateIdentityID(identity); err != nil {
		return options, err
	}
	config, err := distribution.LoadSigningConfig(ctx, m.identityConfigPath(identity), distribution.DefaultLimits())
	if err != nil {
		return options, Errorf("invalid_identity", "named identity is missing or unreadable; configure it in private user state")
	}
	if err := m.validatePrivateIdentityPaths(config); err != nil {
		return options, err
	}
	options.TrustedRootPaths = append([]string{}, config.TrustedRootPaths...)
	return options, nil
}

func (m *ReleaseManager) namedIdentityContentFingerprint(ctx context.Context, id string) (string, error) {
	if err := validateIdentityID(id); err != nil {
		return "", err
	}
	configPath := m.identityConfigPath(id)
	config, err := distribution.LoadSigningConfig(ctx, configPath, distribution.DefaultLimits())
	if err != nil {
		return "", err
	}
	if err := m.validatePrivateIdentityPaths(config); err != nil {
		return "", err
	}
	configDigest, err := digestRegularFile(ctx, configPath, distribution.DefaultLimits().MaxConfigBytes)
	if err != nil {
		return "", err
	}
	parts := []string{configDigest}
	for _, privatePath := range identityPaths(config) {
		digest, digestErr := digestRegularFile(ctx, privatePath, distribution.DefaultLimits().MaxProfileBytes)
		if digestErr != nil {
			return "", digestErr
		}
		parts = append(parts, digest)
	}
	return digestStrings(parts...), nil
}

func (m *ReleaseManager) validatePrivateIdentityPaths(config distribution.SigningConfig) error {
	if pathWithin(m.Workspace, m.identityConfigPath("fixture")) {
		return Errorf("unsafe_private_state", "Pomeforge private signing state must be outside the selected workspace")
	}
	for _, privatePath := range identityPaths(config) {
		if pathWithin(m.Workspace, privatePath) {
			return Errorf("unsafe_identity_path", "signing identity files and trust roots must be kept outside the selected workspace in private user state")
		}
	}
	return nil
}

func pathWithin(root, candidate string) bool {
	if root == "" || candidate == "" || !filepath.IsAbs(candidate) {
		return false
	}
	relative, err := filepath.Rel(filepath.Clean(root), filepath.Clean(candidate))
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

func (m *ReleaseManager) exportPaths(project string, manifest Manifest, input PlanInput) (bundle, output, catalog, identityConfig string, err error) {
	if validateIdentityID(input.Identity) != nil {
		err = Errorf("invalid_identity", "export requires a safe named --identity")
		return
	}
	bundle, err = m.projectPath(project, input.SourceBundle, filepath.Join("xtool", manifest.Name+".app"), true)
	if err != nil {
		err = Errorf("missing_release_bundle", "source release bundle is absent; first run `pomeforge run release-build --project ... --execute`")
		return
	}
	output, err = m.projectPath(project, input.OutputIPA, manifest.Name+".ipa", false)
	if err != nil {
		return
	}
	if input.AssetCatalog != "" {
		catalog, err = m.projectPath(project, input.AssetCatalog, "", true)
	} else if info, statErr := os.Stat(filepath.Join(project, "Assets.xcassets")); statErr == nil && info.IsDir() {
		catalog = filepath.Join(project, "Assets.xcassets")
	}
	identityConfig = m.identityConfigPath(input.Identity)
	return
}

func (m *ReleaseManager) projectPath(project, requested, fallback string, mustExist bool) (string, error) {
	value := requested
	if value == "" {
		value = fallback
	}
	if value == "" {
		return "", Errorf("invalid_path", "path is required")
	}
	if !filepath.IsAbs(value) {
		value = filepath.Join(project, value)
	}
	resolved, err := ResolveWithin(project, value, mustExist)
	if err != nil {
		return "", err
	}
	return resolved, nil
}

func (m *ReleaseManager) identityConfigPath(id string) string {
	return filepath.Join(m.StateDir, "signing", id+".json")
}

func (m *ReleaseManager) historyRoot(input PlanInput) (string, string, error) {
	if isReleaseWorkspaceAction(input.Action) {
		digest := sha256.Sum256([]byte(filepath.Clean(m.Workspace)))
		root := filepath.Join(m.StateDir, "workspaces", hex.EncodeToString(digest[:16]))
		if err := ensurePrivateDirectory(root); err != nil {
			return "", "", err
		}
		return m.StateDir, root, nil
	}
	project, err := ResolveWithin(m.Workspace, input.Project, true)
	return m.Workspace, project, err
}

func (m *ReleaseManager) now() time.Time {
	if m.Now != nil {
		return m.Now()
	}
	return time.Now()
}

func signingConfigFromInput(input PlanInput) distribution.SigningConfig {
	return distribution.SigningConfig{Version: distribution.SigningConfigVersion, PrivateKeyPath: input.PrivateKeyPath, CertificatePath: input.CertificatePath, ProvisioningProfilePath: input.ProfilePath, TrustedRootPaths: append([]string{}, input.TrustedRootPaths...)}
}

func validateIdentityID(id string) error {
	if !identityIDPattern.MatchString(id) {
		return Errorf("invalid_identity", "identity ID must start with a lowercase letter and contain only lowercase letters, digits, and hyphens (maximum 48)")
	}
	return nil
}

func validateIdentityLabel(label string) error {
	if label == "" || len(label) > 80 || strings.IndexFunc(label, func(r rune) bool { return r < 0x20 || r == 0x7f }) >= 0 {
		return Errorf("invalid_identity_label", "identity label must be 1-80 printable characters")
	}
	return nil
}

func identityPaths(config distribution.SigningConfig) []string {
	return append([]string{config.PrivateKeyPath, config.CertificatePath, config.ProvisioningProfilePath}, config.TrustedRootPaths...)
}

func identityFingerprint(ctx context.Context, id, label string, config distribution.SigningConfig) string {
	parts := []string{id, label}
	for _, path := range identityPaths(config) {
		if digest, err := digestRegularFile(ctx, path, distribution.DefaultLimits().MaxProfileBytes); err == nil {
			parts = append(parts, digest)
		} else {
			parts = append(parts, "invalid")
		}
	}
	return digestStrings(parts...)
}

func sanitizeIdentityError(err error, paths ...string) string {
	if err == nil {
		return ""
	}
	message := redactOutput(err.Error())
	privateValues := map[string]bool{}
	for _, path := range filesystemErrorPaths(err) {
		if path != "" {
			privateValues[filepath.Clean(path)] = true
		}
	}
	for _, path := range paths {
		for current := filepath.Clean(path); current != "." && current != string(filepath.Separator); current = filepath.Dir(current) {
			privateValues[current] = true
			parent := filepath.Dir(current)
			if parent == current {
				break
			}
		}
	}
	paths = paths[:0]
	for path := range privateValues {
		paths = append(paths, path)
	}
	sort.Slice(paths, func(i, j int) bool { return len(paths[i]) > len(paths[j]) })
	for _, path := range paths {
		if path != "" {
			message = strings.ReplaceAll(message, path, "[private-path]")
		}
	}
	return message
}

func filesystemErrorPaths(err error) []string {
	var paths []string
	var visit func(error)
	visit = func(current error) {
		if current == nil {
			return
		}
		switch typed := current.(type) {
		case *os.PathError:
			paths = append(paths, typed.Path)
			visit(typed.Err)
			return
		case *os.LinkError:
			paths = append(paths, typed.Old, typed.New)
			visit(typed.Err)
			return
		case interface{ Unwrap() []error }:
			for _, nested := range typed.Unwrap() {
				visit(nested)
			}
			return
		case interface{ Unwrap() error }:
			visit(typed.Unwrap())
		}
	}
	visit(err)
	return paths
}

func sanitizeReleaseError(err error, input PlanInput) string {
	paths := append([]string{input.PrivateKeyPath, input.CertificatePath, input.ProfilePath}, input.TrustedRootPaths...)
	return sanitizeIdentityError(err, paths...)
}

func appendDistributionProblems(plan *Plan, problems []distribution.Problem) {
	for _, problem := range problems {
		if problem.Severity == distribution.SeverityWarning {
			plan.Warnings = append(plan.Warnings, formatProblem(problem))
		} else {
			plan.Blockers = append(plan.Blockers, formatProblem(problem))
		}
	}
}

func formatProblem(problem distribution.Problem) string {
	if problem.Field != "" {
		return problem.Code + " (" + problem.Field + "): " + problem.Message
	}
	return problem.Code + ": " + problem.Message
}

func trustWarning(status distribution.TrustStatus) string {
	if status == distribution.TrustNoRoots {
		return "No explicit trust root is configured. Local signature parsing is not Apple trust or App Store acceptance."
	}
	return "Trust is reported only against the operator-configured roots; Pomeforge does not label them as Apple roots."
}

func releasePlanID(plan Plan) string {
	copy := plan
	copy.ID = ""
	payload := struct {
		Plan        Plan
		Fingerprint string
	}{copy, plan.Fingerprint}
	encoded, _ := json.Marshal(payload)
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:12])
}

func publicExportSteps(existing []Step, actions []distribution.PublicAction, project string) []Step {
	for _, action := range actions {
		existing = append(existing, Step{Kind: "process", Tool: action.Tool, Executable: action.Tool, Args: append([]string{}, action.Args...), Directory: project, Description: "Distribution-owned private staging action; displayed arguments are intentionally redacted placeholders."})
	}
	return existing
}

func sdkInspectionMismatch(bundle distribution.BundleReport, binding SDKBindingReport) []string {
	var blockers []string
	if binding.Provenance != "verified_operator_import" {
		return blockers
	}
	dtXcode, _ := encodeDTXcodeVersion(binding.XcodeVersion)
	if !versionsEqual(bundle.MachO.SDKVersion, binding.SDKVersion) || bundle.SDKMetadata["DTPlatformName"] != "iphoneos" || bundle.SDKMetadata["DTPlatformVersion"] != binding.PlatformVersion || bundle.SDKMetadata["DTSDKName"] != binding.SDKCanonicalName || bundle.SDKMetadata["DTSDKBuild"] != binding.SDKBuildVersion || bundle.SDKMetadata["DTPlatformBuild"] != binding.PlatformBuildVersion || bundle.SDKMetadata["DTXcode"] != dtXcode || bundle.SDKMetadata["DTXcodeBuild"] != binding.XcodeBuildVersion {
		blockers = append(blockers, "IPA SDK/Mach-O/Xcode facts do not match the validated active SDK import provenance")
	}
	return blockers
}

func versionsEqual(a, b string) bool {
	trim := func(value string) string {
		parts := strings.Split(value, ".")
		for len(parts) > 1 && parts[len(parts)-1] == "0" {
			parts = parts[:len(parts)-1]
		}
		return strings.Join(parts, ".")
	}
	return a != "" && trim(a) == trim(b)
}

func digestDirectory(ctx context.Context, root string) (string, error) {
	hash := sha256.New()
	var total int64
	err := walkRegularFilesWithin(ctx, root, map[string]bool{}, func(relative string, file *os.File) error {
		digest, size, err := digestBounded(ctx, file, maxFingerprintFileBytes, &total, maxFingerprintTotalBytes)
		if err != nil {
			return err
		}
		writeFingerprintRecord(hash, "bundle-file", []byte(filepath.ToSlash(relative)), encodeFingerprintSize(size), digest)
		return nil
	})
	if err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func digestRegularFile(ctx context.Context, path string, limit int64) (string, error) {
	file, err := openRegularAbsolute(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	var total int64
	digest, _, err := digestBounded(ctx, file, limit, &total, limit)
	if err != nil {
		return "", err
	}
	return hex.EncodeToString(digest), nil
}

func relativeDisplay(project, path string) string {
	if relative, err := filepath.Rel(project, path); err == nil {
		return filepath.ToSlash(relative)
	}
	return filepath.Base(path)
}

func firstNonempty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

func hasSDKLinkerHook(contents string) bool {
	return strings.Contains(contents, `ProcessInfo.processInfo.environment["POMEFORGE_IOS_SDK_ROOT"]`) && strings.Contains(contents, `"-Xclang-linker", "-isysroot", "-Xclang-linker"`) && strings.Contains(contents, "linkerSettings: sdkLinkerSettings")
}
