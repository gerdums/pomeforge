package pomeforge

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"math/big"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/smallstep/pkcs7"
	"howett.net/plist"

	"pomeforge.local/pomeforge/internal/distribution"
)

type fixedSDKBinding struct {
	report   SDKBindingReport
	blockers []string
}

func (f fixedSDKBinding) Resolve(context.Context) (SDKBindingReport, []string) {
	return f.report, append([]string{}, f.blockers...)
}

func testSDKBinding() SDKBindingReport {
	return SDKBindingReport{TargetTriple: "arm64-apple-ios", SDKVersion: "26.5", SDKProductVersion: "26.5.1", SDKCanonicalName: "iphoneos26.5", SDKBuildVersion: "23F81a", PlatformVersion: "26.5", PlatformBuildVersion: "23F81a", XcodeVersion: "26.6", XcodeBuildVersion: "17F113", XcodeBundleVersion: "24332", Provenance: "verified_operator_import", SDKRoot: "/private/sdk/darwin.artifactbundle/sdk"}
}

type bindingRunner struct {
	root         string
	commands     [][]string
	environments [][]string
}

func (r *bindingRunner) Run(_ context.Context, _ string, args []string, _ string, environment []string) (string, int, error) {
	r.commands = append(r.commands, append([]string{}, args...))
	r.environments = append(r.environments, append([]string{}, environment...))
	switch strings.Join(args, " ") {
	case "sdk list":
		return "darwin\n", 0, nil
	case "sdk configure darwin arm64-apple-ios --show-configuration":
		return "sdkRootPath: " + r.root + "\nswiftResourcesPath: /private/swift\nincludeSearchPaths: []\n", 0, nil
	case "sdk status":
		return "Darwin SDK: Installed\n", 0, nil
	default:
		return "", 2, errors.New("unexpected command")
	}
}

func TestNativeSDKBinderUsesExactDiscoveryAndRejectsChangedProvenance(t *testing.T) {
	runner := &sdkRunner{}
	service, root := sdkService(t, runner)
	xcode := xcodeFixture(t, root)
	plan, err := service.PlanOperation(context.Background(), PlanInput{Action: "sdk-import", InputPath: xcode, Arch: "x86_64"}, true)
	if err != nil || !plan.Executable {
		t.Fatalf("SDK import plan=%#v err=%v", plan, err)
	}
	if result, executeErr := service.RunStored(context.Background(), plan.ID, false); executeErr != nil || result.Status != "succeeded" {
		t.Fatalf("SDK import result=%#v err=%v", result, executeErr)
	}
	info := `{"schemaVersion":"1.0","artifacts":{"darwin":{"type":"swiftSDK","variants":[{"path":".","supportedTriples":["aarch64-unknown-linux-gnu","x86_64-unknown-linux-gnu"]}]}}}`
	writeTestFile(t, filepath.Join(runner.installedBundle, "info.json"), []byte(info), 0o600)
	sourceSDK := filepath.Join(xcode, "Contents", "Developer", "Platforms", "iPhoneOS.platform", "Developer", "SDKs", "iPhoneOS26.5.sdk")
	for _, relative := range []string{"SDKSettings.json", "SDKSettings.plist", filepath.Join("System", "Library", "CoreServices", "SystemVersion.plist")} {
		writeTestFile(t, filepath.Join(runner.activeRoot, relative), readTestFile(t, filepath.Join(sourceSDK, relative)), 0o600)
	}
	t.Setenv("XDG_CONFIG_HOME", service.Setup.XDGConfigHome)
	t.Setenv("POMEFORGE_IOS_SDK_ROOT", "/ambient/must-not-be-used")
	commandStart := len(runner.commands)
	binder := NativeSDKBinder{Tools: service.Tools, StateDir: service.Setup.StateDir, Runner: runner, HostArch: "amd64"}
	report, blockers := binder.Resolve(context.Background())
	if len(blockers) != 0 || report.Provenance != "verified_operator_import" || report.SDKRoot != runner.activeRoot || report.SDKProductVersion != "26.5.1" || report.XcodeBundleVersion != "24332" {
		t.Fatalf("binding report=%#v blockers=%v", report, blockers)
	}
	wantCommands := [][]string{{"sdk", "list"}, {"sdk", "configure", "darwin", "arm64-apple-ios", "--show-configuration"}, {"sdk", "status"}, {"-print-resource-dir"}}
	if !reflect.DeepEqual(runner.commands[commandStart:], wantCommands) {
		t.Fatalf("commands=%#v", runner.commands[commandStart:])
	}
	for _, environment := range runner.environments[commandStart:] {
		if strings.Contains(strings.Join(environment, "\n"), "POMEFORGE_IOS_SDK_ROOT") || environmentValue(environment, "XDG_CONFIG_HOME") != service.Setup.XDGConfigHome {
			t.Fatal("ambient SDK root reached discovery command")
		}
	}
	receipt, err := LoadSDKEnvironmentReceipt(service.Setup.StateDir)
	if err != nil {
		t.Fatal(err)
	}
	receipt.Metadata["platformVersion"] = "99.0"
	encoded, _ := json.MarshalIndent(receipt, "", "  ")
	writeTestFile(t, filepath.Join(service.Setup.StateDir, "sdk-environment.json"), append(encoded, '\n'), 0o600)
	if _, blockers = binder.Resolve(context.Background()); !containsText(blockers, "disagree") {
		t.Fatalf("edited receipt metadata was accepted: %v", blockers)
	}
	receipt.Metadata["platformVersion"] = "26.5"
	encoded, _ = json.MarshalIndent(receipt, "", "  ")
	writeTestFile(t, filepath.Join(service.Setup.StateDir, "sdk-environment.json"), append(encoded, '\n'), 0o600)
	writeTestFile(t, receipt.SwiftSDKMetadataPath, []byte(`{"schemaVersion":"4.0","targetTriples":{"arm64-apple-ios":{"sdkRootPath":"other-sdk"}}}`), 0o600)
	if _, blockers = binder.Resolve(context.Background()); !containsText(blockers, "receipt") {
		t.Fatalf("changed swift-sdk.json was accepted: %v", blockers)
	}
}

func TestReleaseBuildPlanBindsSDKAndGeneratedHook(t *testing.T) {
	workspace, project := testProject(t, AppStoreIDs{})
	manager := NewReleaseManager(workspace, t.TempDir(), availableTools(t), &Executor{})
	binding := testSDKBinding()
	manager.Binder = fixedSDKBinding{report: binding}
	plan, err := manager.Plan(context.Background(), PlanInput{Action: "release-build", Project: project})
	if err != nil {
		t.Fatal(err)
	}
	if !plan.Executable || plan.SDKBinding == nil || plan.SDKBinding.SDKVersion != "26.5" {
		t.Fatalf("release build plan=%#v", plan)
	}
	if got := plan.Steps[1].Args; !reflect.DeepEqual(got, []string{"dev", "build", "--configuration", "release"}) {
		t.Fatalf("xtool args=%#v", got)
	}
	packageData, _ := os.ReadFile(filepath.Join(project, "Package.swift"))
	if !hasSDKLinkerHook(string(packageData)) || strings.Contains(string(packageData), binding.SDKRoot) {
		t.Fatal("generated Package.swift omitted the controlled SDK hook or embedded a private SDK path")
	}
}

func TestReleaseBuildInvokesSwiftAliasAndUsesReceiptXDGAndSDKRoot(t *testing.T) {
	_, project := testProject(t, AppStoreIDs{})
	bundle := filepath.Join(project, "xtool", "Demo.app")
	writeSyntheticBundle(t, bundle, syntheticReleaseInfo(t), syntheticReleaseMachO())
	root := t.TempDir()
	logPath := filepath.Join(root, "environment.log")
	toolScript := fmt.Sprintf("#!/bin/sh\nprintf '%%s\\n' \"$0\" \"${XDG_CONFIG_HOME-unset}\" \"${POMEFORGE_IOS_SDK_ROOT-unset}\" >> %q\n", logPath)
	swiftTarget := filepath.Join(root, "swift-driver")
	writeTestFile(t, swiftTarget, []byte(toolScript), 0o700)
	swiftAlias := filepath.Join(root, "swift")
	if err := os.Symlink(swiftTarget, swiftAlias); err != nil {
		t.Fatal(err)
	}
	clangTarget := filepath.Join(root, "clang-target")
	writeTestFile(t, clangTarget, []byte("#!/bin/sh\nexit 0\n"), 0o700)
	clangAlias := filepath.Join(root, "clang")
	if err := os.Symlink(clangTarget, clangAlias); err != nil {
		t.Fatal(err)
	}
	xtool := filepath.Join(root, "xtool")
	writeTestFile(t, xtool, []byte(toolScript), 0o700)
	configHome := filepath.Join(root, "config")
	t.Setenv("XDG_CONFIG_HOME", configHome)
	binding := testSDKBinding()
	binding.XDGConfigHome = configHome
	binding.SwiftExecutable = swiftAlias
	binding.ClangExecutable = clangAlias
	var err error
	binding.SwiftSHA256, err = hashExecutableForPlan(context.Background(), swiftAlias)
	if err != nil {
		t.Fatal(err)
	}
	binding.ClangSHA256, err = hashExecutableForPlan(context.Background(), clangAlias)
	if err != nil {
		t.Fatal(err)
	}
	plan := Plan{SDKBinding: &binding, Steps: []Step{
		{Kind: "process", Tool: "swift", Executable: swiftAlias, Args: []string{"--version"}, Directory: project},
		{Kind: "process", Tool: "xtool", Executable: xtool, Args: []string{"dev", "build", "--configuration", "release"}, Directory: project},
	}}
	manager := NewReleaseManager(filepath.Dir(project), t.TempDir(), availableTools(t), &Executor{})
	if _, err := manager.executeReleaseBuild(context.Background(), plan, PlanInput{SourceBundle: bundle}, project); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(readTestFile(t, logPath))), "\n")
	want := []string{swiftAlias, configHome, "unset", xtool, configHome, binding.SDKRoot}
	if !reflect.DeepEqual(lines, want) {
		t.Fatalf("release tool invocation/environment=%#v, want %#v", lines, want)
	}
}

func TestReleaseBuildDefersOnlyAssetKitIconProblems(t *testing.T) {
	workspace, project := testProject(t, AppStoreIDs{})
	bundle := filepath.Join(project, "xtool", "Pomeforge.app")
	var info map[string]any
	if _, err := plist.Unmarshal(syntheticReleaseInfo(t), &info); err != nil {
		t.Fatal(err)
	}
	delete(info, "CFBundleIconName")
	delete(info, "CFBundleIconFiles")
	infoData, err := plist.Marshal(info, plist.XMLFormat)
	if err != nil {
		t.Fatal(err)
	}
	writeSyntheticBundle(t, bundle, infoData, syntheticReleaseMachO())
	manager := NewReleaseManager(workspace, t.TempDir(), availableTools(t), &Executor{})
	binding := testSDKBinding()
	value, err := manager.executeReleaseBuild(context.Background(), Plan{SDKBinding: &binding}, PlanInput{SourceBundle: bundle}, project)
	if err != nil {
		t.Fatalf("pre-AssetKit unsigned bundle was blocked: value=%#v err=%v", value, err)
	}
	result, ok := value.(map[string]any)
	if !ok || result["releaseBuildReadiness"] != "unsigned_pre_assetkit" {
		t.Fatalf("unexpected release-build result: %#v", value)
	}
	problems := []distribution.Problem{
		{Code: "missing_icon_file", Severity: distribution.SeverityError},
		{Code: "unsafe_icon_metadata", Severity: distribution.SeverityError},
		{Code: "inspection_note", Severity: distribution.SeverityWarning},
		{Code: "missing_bundle_identifier", Severity: distribution.SeverityError},
	}
	blockers := releaseBuildBlockingProblems(problems, true)
	if len(blockers) != 1 || blockers[0].Code != "missing_bundle_identifier" {
		t.Fatalf("non-icon blocker filtering=%#v", blockers)
	}
	withoutCatalog := releaseBuildBlockingProblems(problems, false)
	if len(withoutCatalog) != 3 || containsProblemCode(withoutCatalog, "inspection_note") {
		t.Fatalf("warning or icon errors were mishandled without a catalog: %#v", withoutCatalog)
	}
}

func TestReleaseMetadataRejectsMismatchedMachOMinimum(t *testing.T) {
	workspace, project := testProject(t, AppStoreIDs{})
	bundle := filepath.Join(project, "xtool", "Demo.app")
	machO := syntheticReleaseMachO()
	binary.LittleEndian.PutUint32(machO[44:48], 18<<16)
	writeSyntheticBundle(t, bundle, syntheticReleaseInfo(t), machO)
	if err := applyReleaseMetadata(context.Background(), project, bundle, testSDKBinding()); err == nil || !strings.Contains(err.Error(), "minimum iOS") {
		t.Fatalf("mismatched Mach-O deployment minimum was accepted: %v", err)
	}
	if !pathWithin(workspace, bundle) {
		t.Fatal("test bundle escaped its workspace")
	}
}

func TestGeneratedIconsAreCompleteAndPlaceholderReplacementIsSingleUse(t *testing.T) {
	_, project := testProject(t, AppStoreIDs{})
	set := filepath.Join(project, "Assets.xcassets", "AppIcon.appiconset")
	if _, err := os.Stat(filepath.Join(set, placeholderMarker)); err != nil {
		t.Fatal("generated placeholder marker is absent")
	}
	var contents struct {
		Images []generatedCatalogImage `json:"images"`
	}
	data, _ := os.ReadFile(filepath.Join(set, "Contents.json"))
	if err := json.Unmarshal(data, &contents); err != nil || len(contents.Images) != len(generatedIconSlots) {
		t.Fatalf("generated icon catalog is incomplete: images=%d err=%v", len(contents.Images), err)
	}
	sourcePath := filepath.Join(project, "Icon-1024.png")
	source := image.NewNRGBA(image.Rect(0, 0, 1024, 1024))
	for y := 0; y < 1024; y++ {
		for x := 0; x < 1024; x++ {
			source.SetNRGBA(x, y, color.NRGBA{R: 12, G: 44, B: 88, A: 255})
		}
	}
	file, err := os.OpenFile(sourcePath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if err := png.Encode(file, source); err != nil {
		t.Fatal(err)
	}
	_ = file.Close()
	if err := replacePlaceholderIcons(project, sourcePath); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(filepath.Join(set, placeholderMarker)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("placeholder marker survived replacement")
	}
	if err := replacePlaceholderIcons(project, sourcePath); err == nil || !strings.Contains(err.Error(), "refusing") {
		t.Fatalf("custom icon set was overwritten: %v", err)
	}
}

func TestIconSourceSizeIsBounded(t *testing.T) {
	_, project := testProject(t, AppStoreIDs{})
	path := filepath.Join(project, "oversized.png")
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Truncate(maxIconSourceBytes + 1); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	if err := validateIconSource(project, path); err == nil || !strings.Contains(err.Error(), "missing or unreadable") {
		t.Fatalf("oversized icon source was accepted: %v", err)
	}
}

func TestNamedIdentityConfigurationIsValidPrivateAndPathRedacted(t *testing.T) {
	workspace := t.TempDir()
	state := t.TempDir()
	private := t.TempDir()
	now := time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)
	paths := writeSigningFixture(t, private, now)
	manager := NewReleaseManager(workspace, state, availableTools(t), &Executor{})
	manager.Now = func() time.Time { return now }
	input := PlanInput{Action: "signing-configure", Identity: "app-store-main", IdentityLabel: "App Store distribution", PrivateKeyPath: paths.key, CertificatePath: paths.cert, ProfilePath: paths.profile, TrustedRootPaths: []string{paths.root}}
	plan, err := manager.Plan(context.Background(), input)
	if err != nil || !plan.Executable || plan.IdentityInspection == nil || !plan.IdentityInspection.Valid() {
		t.Fatalf("identity plan=%#v err=%v", plan, err)
	}
	publicPlan, _ := json.Marshal(plan)
	for _, path := range []string{paths.key, paths.cert, paths.profile, paths.root} {
		if strings.Contains(string(publicPlan), path) {
			t.Fatalf("public plan leaked %q: %s", path, publicPlan)
		}
	}
	result, err := manager.Execute(context.Background(), plan, input, false)
	if err != nil || result.Status != "succeeded" {
		t.Fatalf("identity result=%#v err=%v", result, err)
	}
	for _, path := range []string{paths.key, paths.cert, paths.profile, paths.root} {
		if strings.Contains(result.Output, path) {
			t.Fatalf("result leaked private path %q", path)
		}
	}
	identities := manager.ListIdentities(context.Background())
	if len(identities) != 1 || identities[0].Status != "ready" || identities[0].Label != "App Store distribution" {
		t.Fatalf("identities=%#v", identities)
	}
	missing := filepath.Join(private, "missing-private-key.pem")
	input.Identity = "broken"
	input.PrivateKeyPath = missing
	_, err = manager.Plan(context.Background(), input)
	if err == nil || strings.Contains(err.Error(), missing) || !strings.Contains(err.Error(), "[private-path]") {
		t.Fatalf("private-path error was not sanitized: %v", err)
	}
}

func TestNamedIdentityMissingParentErrorsNeverExposePrivatePaths(t *testing.T) {
	workspace := t.TempDir()
	project, err := CreateProject(workspace, "Demo", "Demo", "com.example.Demo")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(project.Path, "xtool", "Demo.app"), 0o700); err != nil {
		t.Fatal(err)
	}
	private := t.TempDir()
	if err := os.Mkdir(filepath.Join(private, "signing"), 0o700); err != nil {
		t.Fatal(err)
	}
	privateParent := filepath.Join(private, `PRIVATE IDENTITY "quotes" & 25%`)
	key := filepath.Join(privateParent, "missing key.pem")
	config := distribution.SigningConfig{
		Version:                 distribution.SigningConfigVersion,
		PrivateKeyPath:          key,
		CertificatePath:         filepath.Join(private, "certificate.pem"),
		ProvisioningProfilePath: filepath.Join(private, "profile.mobileprovision"),
	}
	if err := distribution.CreateSigningConfig(context.Background(), filepath.Join(private, "signing", "fixture.json"), config); err != nil {
		t.Fatal(err)
	}
	tools := availableTools(t)
	tools["pomeforge-assets"] = ToolStatus{ID: "pomeforge-assets", Name: "pomeforge-assets", Status: "available", Path: tools["xtool"].Path, Version: "fixture"}
	manager := NewReleaseManager(workspace, private, tools, &Executor{Workspace: workspace})
	_, err = manager.Plan(context.Background(), PlanInput{Action: "export", Project: project.Path, Identity: "fixture"})
	if err == nil || !strings.Contains(err.Error(), "identity path validation") {
		t.Fatalf("missing identity path did not reach the intended validation branch: %v", err)
	}
	for _, forbidden := range []string{private, privateParent, key, "PRIVATE IDENTITY", "missing key.pem"} {
		if strings.Contains(err.Error(), forbidden) {
			t.Fatalf("private path fragment %q reached a public plan error: %v", forbidden, err)
		}
	}
	if !strings.Contains(err.Error(), "[private-path]") {
		t.Fatalf("sanitized error omitted its redaction marker: %v", err)
	}
}

func TestSigningConfigurationRejectsWorkspaceMaterialAndPrivateState(t *testing.T) {
	workspace := t.TempDir()
	now := time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)
	paths := writeSigningFixture(t, filepath.Join(workspace, "private"), now)
	input := PlanInput{Action: "signing-configure", Identity: "workspace-key", IdentityLabel: "Unsafe", PrivateKeyPath: paths.key, CertificatePath: paths.cert, ProfilePath: paths.profile}
	manager := NewReleaseManager(workspace, t.TempDir(), availableTools(t), &Executor{})
	manager.Now = func() time.Time { return now }
	if _, err := manager.Plan(context.Background(), input); err == nil || !strings.Contains(err.Error(), "outside") || strings.Contains(err.Error(), paths.key) {
		t.Fatalf("workspace identity material was not safely rejected: %v", err)
	}
	outside := writeSigningFixture(t, t.TempDir(), now)
	input.PrivateKeyPath, input.CertificatePath, input.ProfilePath = outside.key, outside.cert, outside.profile
	manager = NewReleaseManager(workspace, filepath.Join(workspace, ".private-state"), availableTools(t), &Executor{})
	if _, err := manager.Plan(context.Background(), input); err == nil || !strings.Contains(err.Error(), "private signing state") {
		t.Fatalf("workspace-contained private state was accepted: %v", err)
	}
}

func TestIconPlanningRejectsSymlinkedCatalogAndSource(t *testing.T) {
	workspace, project := testProject(t, AppStoreIDs{})
	source := filepath.Join(project, "Icon-1024.png")
	file, err := os.OpenFile(source, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if err := png.Encode(file, image.NewNRGBA(image.Rect(0, 0, 1024, 1024))); err != nil {
		t.Fatal(err)
	}
	_ = file.Close()
	manager := NewReleaseManager(workspace, t.TempDir(), availableTools(t), &Executor{})
	outsideSource := filepath.Join(t.TempDir(), "outside.png")
	writeTestFile(t, outsideSource, []byte("not relevant"), 0o600)
	if err := os.Symlink(outsideSource, filepath.Join(project, "Linked.png")); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Plan(context.Background(), PlanInput{Action: "icons", Project: project, IconSource: "Linked.png"}); err == nil {
		t.Fatal("symlinked icon source was accepted")
	}
	if err := os.Remove(filepath.Join(project, "Linked.png")); err != nil {
		t.Fatal(err)
	}
	tools := availableTools(t)
	service := &Service{Workspace: workspace, Tools: tools, Planner: Planner{Workspace: workspace, Tools: tools}, Release: manager, Executor: manager.Executor, plans: map[string]PlanInput{}}
	stored, err := service.PlanOperation(context.Background(), PlanInput{Action: "icons", Project: project, IconSource: "Icon-1024.png"}, true)
	if err != nil || !stored.Executable {
		t.Fatalf("valid icon plan=%#v err=%v", stored, err)
	}
	assets := filepath.Join(project, "Assets.xcassets")
	if err := os.Rename(assets, assets+".original"); err != nil {
		t.Fatal(err)
	}
	outsideCatalog := t.TempDir()
	if err := os.Symlink(outsideCatalog, assets); err != nil {
		t.Fatal(err)
	}
	if _, err := service.RunStored(context.Background(), stored.ID, false); err == nil {
		t.Fatal("execution-time catalog symlink replacement was accepted")
	}
	if entries, err := os.ReadDir(outsideCatalog); err != nil || len(entries) != 0 {
		t.Fatalf("icon replacement wrote outside the workspace: entries=%v err=%v", entries, err)
	}
	if _, err := manager.Plan(context.Background(), PlanInput{Action: "icons", Project: project, IconSource: "Icon-1024.png"}); err == nil || !strings.Contains(err.Error(), "unsafe") {
		t.Fatalf("symlinked catalog was accepted: %v", err)
	}
	if err := os.Remove(assets); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(assets+".original", assets); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(assets, "AppIcon.appiconset", placeholderMarker)
	if err := os.Remove(marker); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outsideSource, marker); err != nil {
		t.Fatal(err)
	}
	blocked, err := manager.Plan(context.Background(), PlanInput{Action: "icons", Project: project, IconSource: "Icon-1024.png"})
	if err == nil && (blocked.Executable || !containsText(blocked.Blockers, "ownership marker")) {
		t.Fatalf("symlinked starter-art marker was not blocked: plan=%#v err=%v", blocked, err)
	}
}

func TestIPAInspectionRejectsUnsafeIdentityIDBeforeConfigLoad(t *testing.T) {
	workspace, project := testProject(t, AppStoreIDs{})
	writeTestFile(t, filepath.Join(project, "fixture.ipa"), []byte("fixture"), 0o600)
	manager := NewReleaseManager(workspace, t.TempDir(), availableTools(t), &Executor{})
	_, err := manager.Plan(context.Background(), PlanInput{Action: "ipa-inspect", Project: project, IPA: "fixture.ipa", Identity: "../../outside"})
	if err == nil || !strings.Contains(err.Error(), "identity ID") {
		t.Fatalf("unsafe identity ID reached config loading: %v", err)
	}
}

func TestStorePlansUseExactASC5CommandsAndRefuseUnconfirmedSubmit(t *testing.T) {
	versionID := "a1b2c3d4-1111-4222-8333-abcdef123456"
	buildID := "b2c3d4e5-2222-4333-8444-bcdefa234567"
	workspace, project := testProject(t, AppStoreIDs{AppID: "1001", VersionID: versionID, BuildID: buildID})
	service, err := NewService(workspace, availableTools(t))
	if err != nil {
		t.Fatal(err)
	}
	service.Release.Binder = fixedSDKBinding{report: testSDKBinding()}
	plan, err := service.PlanOperation(context.Background(), PlanInput{Action: "submit", Project: project}, true)
	if err != nil || !plan.Executable || len(plan.Steps) != 2 {
		t.Fatalf("submit plan=%#v err=%v", plan, err)
	}
	wantPreview := []string{"review", "submit", "--app", "1001", "--version-id", versionID, "--build-id", buildID, "--platform", "IOS", "--dry-run"}
	wantSubmit := []string{"review", "submit", "--app", "1001", "--version-id", versionID, "--build-id", buildID, "--platform", "IOS", "--confirm"}
	if !reflect.DeepEqual(plan.Steps[0].Args, wantPreview) || !reflect.DeepEqual(plan.Steps[1].Args, wantSubmit) {
		t.Fatalf("submit argv=%#v", plan.Steps)
	}
	if _, err := service.RunStored(context.Background(), plan.ID, false); err == nil || !strings.Contains(err.Error(), "confirmation") {
		t.Fatalf("unconfirmed submission was not refused: %v", err)
	}
	if _, err := service.PlanOperation(context.Background(), PlanInput{Action: "submit", Project: project, BuildID: "not-a-uuid"}, false); err == nil {
		t.Fatal("unsafe build resource ID was accepted")
	}
}

func TestStoreReadCommandsAndExplicitValidationSelector(t *testing.T) {
	versionID := "a1b2c3d4-1111-4222-8333-abcdef123456"
	buildID := "b2c3d4e5-2222-4333-8444-bcdefa234567"
	workspace, project := testProject(t, AppStoreIDs{AppID: "1001", VersionID: versionID, BuildID: buildID})
	manager := NewReleaseManager(workspace, t.TempDir(), availableTools(t), &Executor{})
	status, err := manager.Plan(context.Background(), PlanInput{Action: "store-status", Project: project})
	if err != nil || !reflect.DeepEqual(status.Steps[0].Args, []string{"builds", "info", "--build-id", buildID, "--output", "json"}) {
		t.Fatalf("store-status plan=%#v err=%v", status, err)
	}
	validation, err := manager.Plan(context.Background(), PlanInput{Action: "validate", Project: project, Version: "2.3.4"})
	want := []string{"validate", "--app", "1001", "--version", "2.3.4", "--platform", "IOS", "--output", "json"}
	if err != nil || !reflect.DeepEqual(validation.Steps[0].Args, want) {
		t.Fatalf("explicit version was ignored: plan=%#v err=%v", validation, err)
	}
}

func TestSDKInspectionComparesEveryGeneratedMetadataField(t *testing.T) {
	binding := testSDKBinding()
	dtXcode, _ := encodeDTXcodeVersion(binding.XcodeVersion)
	report := distribution.BundleReport{MachO: distribution.MachOReport{SDKVersion: binding.SDKVersion}, SDKMetadata: map[string]string{
		"DTPlatformName": "iphoneos", "DTPlatformVersion": binding.PlatformVersion,
		"DTPlatformBuild": binding.PlatformBuildVersion, "DTSDKName": binding.SDKCanonicalName,
		"DTSDKBuild": binding.SDKBuildVersion, "DTXcode": dtXcode, "DTXcodeBuild": binding.XcodeBuildVersion,
	}}
	if blockers := sdkInspectionMismatch(report, binding); len(blockers) != 0 {
		t.Fatalf("matching metadata blocked: %v", blockers)
	}
	for _, key := range []string{"DTPlatformName", "DTPlatformVersion", "DTPlatformBuild", "DTSDKName", "DTSDKBuild", "DTXcode", "DTXcodeBuild"} {
		original := report.SDKMetadata[key]
		report.SDKMetadata[key] = "wrong"
		if blockers := sdkInspectionMismatch(report, binding); len(blockers) == 0 {
			t.Fatalf("mismatched %s was accepted", key)
		}
		report.SDKMetadata[key] = original
	}
}

func TestNativeExportAdapterPreservesSourceAndReturnsFailedResult(t *testing.T) {
	fixture := newReleaseWorkflowFixture(t)
	before, err := digestDirectory(context.Background(), fixture.bundle)
	if err != nil {
		t.Fatal(err)
	}
	input := PlanInput{Action: "export", Project: fixture.project, SourceBundle: fixture.bundle, OutputIPA: "Pomeforge.ipa", Identity: fixture.identity}
	plan, err := fixture.manager.Plan(context.Background(), input)
	if err != nil || !plan.Executable || plan.DistributionExport == nil {
		t.Fatalf("export plan=%#v err=%v", plan, err)
	}
	result, err := fixture.manager.Execute(context.Background(), plan, input, false)
	if err != nil || result.Status != "succeeded" {
		t.Fatalf("export result=%#v err=%v", result, err)
	}
	after, err := digestDirectory(context.Background(), fixture.bundle)
	if err != nil || before != after {
		t.Fatalf("source bundle changed: before=%s after=%s err=%v", before, after, err)
	}
	arguments, err := os.ReadFile(fixture.argumentLog)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(arguments)), "\n")
	if len(lines) != 11 || lines[0] != "-k" || lines[2] != "-c" || lines[4] != "-m" || lines[6] != "-e" || lines[8] != "-o" || !strings.HasSuffix(lines[10], ".app") {
		t.Fatalf("unexpected native zsign argv boundaries: %#v", lines)
	}
	for _, privatePath := range []string{fixture.paths.key, fixture.paths.cert, fixture.paths.profile} {
		if strings.Contains(result.Output, privatePath) {
			t.Fatalf("export result leaked private path %q", privatePath)
		}
	}

	failing := filepath.Join(t.TempDir(), "zsign")
	writeTestFile(t, failing, []byte("#!/bin/sh\nexit 9\n"), 0o700)
	fixture.tools["zsign"] = ToolStatus{ID: "zsign", Name: "zsign", Status: "available", Version: "fixture", Path: failing}
	failureInput := input
	failureInput.OutputIPA = "Failed.ipa"
	failurePlan, err := fixture.manager.Plan(context.Background(), failureInput)
	if err != nil || !failurePlan.Executable {
		t.Fatalf("failure export plan=%#v err=%v", failurePlan, err)
	}
	failed, err := fixture.manager.Execute(context.Background(), failurePlan, failureInput, false)
	if err != nil || failed.Status != "failed" || failed.ExitCode != 1 || !strings.Contains(failed.Output, "zsign failed") {
		t.Fatalf("failed export result=%#v err=%v", failed, err)
	}
}

func TestExportAllowsCompleteStarterArtworkWithWarning(t *testing.T) {
	fixture := newReleaseWorkflowFixture(t)
	assetCompiler := filepath.Join(t.TempDir(), "pomeforge-assets")
	writeTestFile(t, assetCompiler, []byte("#!/bin/sh\nexit 0\n"), 0o700)
	fixture.tools["pomeforge-assets"] = ToolStatus{ID: "pomeforge-assets", Name: "pomeforge-assets", Status: "available", Version: "fixture", Path: assetCompiler}
	input := PlanInput{Action: "export", Project: fixture.project, SourceBundle: fixture.bundle, OutputIPA: "Placeholder.ipa", Identity: fixture.identity, AssetCatalog: "SourceAssets.xcassets"}
	plan, err := fixture.manager.Plan(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	if !plan.Executable || !containsText(plan.Warnings, "starter artwork") {
		t.Fatalf("complete starter catalog was not export-ready with a warning: %#v", plan)
	}
}

func containsProblemCode(problems []distribution.Problem, code string) bool {
	for _, problem := range problems {
		if problem.Code == code {
			return true
		}
	}
	return false
}

func TestUploadBindsExactIPAAndStoredPlanRejectsChangedBytes(t *testing.T) {
	fixture := newReleaseWorkflowFixture(t)
	input := PlanInput{Action: "upload", Project: fixture.project, IPA: "Candidate.ipa", Identity: fixture.identity, AppID: "1001"}
	plan, err := fixture.service.PlanOperation(context.Background(), input, true)
	want := []string{"builds", "upload", "--app", "1001", "--ipa", fixture.ipa, "--wait", "--output", "json"}
	if err != nil || !plan.Executable || plan.IPAInspection == nil || plan.IPAInspection.SHA256 == "" || !reflect.DeepEqual(plan.Steps[0].Args, want) {
		t.Fatalf("upload plan=%#v err=%v", plan, err)
	}
	writeSyntheticIPA(t, fixture.ipa, fixture.info, fixture.machO, readTestFile(t, fixture.paths.profile), "changed archive comment")
	if _, err := fixture.service.RunStored(context.Background(), plan.ID, true); err == nil || !strings.Contains(err.Error(), "changed") {
		t.Fatalf("changed IPA bytes did not stale the stored upload plan: %v", err)
	}
}

func TestStoredExportPlanBindsEditedIdentityConfigAndMaterial(t *testing.T) {
	fixture := newReleaseWorkflowFixture(t)
	input := PlanInput{Action: "export", Project: fixture.project, SourceBundle: fixture.bundle, OutputIPA: "Identity-Stale.ipa", Identity: fixture.identity}
	plan, err := fixture.service.PlanOperation(context.Background(), input, true)
	if err != nil || !plan.Executable {
		t.Fatalf("export plan=%#v err=%v", plan, err)
	}
	configPath := fixture.manager.identityConfigPath(fixture.identity)
	config, err := distribution.LoadSigningConfig(context.Background(), configPath, distribution.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	relocated := t.TempDir()
	config.PrivateKeyPath = copyTestFile(t, fixture.paths.key, filepath.Join(relocated, "key.pem"))
	config.CertificatePath = copyTestFile(t, fixture.paths.cert, filepath.Join(relocated, "certificate.pem"))
	config.ProvisioningProfilePath = copyTestFile(t, fixture.paths.profile, filepath.Join(relocated, "profile.mobileprovision"))
	config.TrustedRootPaths = []string{copyTestFile(t, fixture.paths.root, filepath.Join(relocated, "root.pem"))}
	encoded, _ := json.MarshalIndent(config, "", "  ")
	if err := os.WriteFile(configPath, append(encoded, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.service.RunStored(context.Background(), plan.ID, false); err == nil || !strings.Contains(err.Error(), "changed") {
		t.Fatalf("edited identity did not stale stored export plan: %v", err)
	}
}

func TestEditedIdentityConfigCannotReferenceWorkspaceMaterial(t *testing.T) {
	fixture := newReleaseWorkflowFixture(t)
	configPath := fixture.manager.identityConfigPath(fixture.identity)
	config, err := distribution.LoadSigningConfig(context.Background(), configPath, distribution.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	config.PrivateKeyPath = copyTestFile(t, fixture.paths.key, filepath.Join(fixture.project, "tracked-private-key.pem"))
	encoded, _ := json.MarshalIndent(config, "", "  ")
	if err := os.WriteFile(configPath, append(encoded, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.manager.inspectNamedIdentity(context.Background(), fixture.identity); err == nil || !strings.Contains(err.Error(), "outside") {
		t.Fatalf("edited workspace identity config was accepted: %v", err)
	}
	identities := fixture.manager.ListIdentities(context.Background())
	if len(identities) != 1 || identities[0].Status != "blocked" || identities[0].Error == "" {
		t.Fatalf("unsafe edited identity was not reported safely: %#v", identities)
	}
	if _, err := fixture.manager.inspectionOptions(context.Background(), fixture.identity); err == nil || !strings.Contains(err.Error(), "outside") {
		t.Fatalf("unsafe trust configuration reached IPA inspection: %v", err)
	}
}

func TestDistributionRunnerRedactsEveryPrivateActionPath(t *testing.T) {
	private := filepath.Join(t.TempDir(), "secret.key")
	privateStage := t.TempDir()
	script := filepath.Join(t.TempDir(), "tool")
	writeTestFile(t, script, []byte("#!/bin/sh\nprintf '%s\\n' \"$1\" \"$PWD\" \"$PWD/derived/file\"\nexit 9\n"), 0o700)
	runner := &distributionRunner{}
	action := distributionAction(script, private)
	action.Directory = privateStage
	err := runner.Run(context.Background(), action)
	if err == nil || strings.Contains(runner.Output(), private) || strings.Contains(runner.Output(), privateStage) || !strings.Contains(runner.Output(), "[private-path]") {
		t.Fatalf("runner output=%q err=%v", runner.Output(), err)
	}
}

func distributionAction(executable, private string) distribution.Action {
	return distribution.Action{Executable: executable, Args: []string{private}, Directory: filepath.Dir(executable)}
}

type signingFixturePaths struct{ key, cert, profile, root string }

func writeSigningFixture(t *testing.T, directory string, now time.Time) signingFixturePaths {
	t.Helper()
	rootKey := mustTestRSA(t)
	rootTemplate := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "Synthetic Root"}, NotBefore: now.Add(-time.Hour), NotAfter: now.Add(365 * 24 * time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature}
	rootDER, err := x509.CreateCertificate(rand.Reader, rootTemplate, rootTemplate, &rootKey.PublicKey, rootKey)
	if err != nil {
		t.Fatal(err)
	}
	root, _ := x509.ParseCertificate(rootDER)
	leaf := func(serial int64, name string) (*x509.Certificate, *rsa.PrivateKey) {
		key := mustTestRSA(t)
		template := &x509.Certificate{SerialNumber: big.NewInt(serial), Subject: pkix.Name{CommonName: name}, NotBefore: now.Add(-time.Hour), NotAfter: now.Add(30 * 24 * time.Hour), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageCodeSigning}}
		der, createErr := x509.CreateCertificate(rand.Reader, template, root, &key.PublicKey, rootKey)
		if createErr != nil {
			t.Fatal(createErr)
		}
		certificate, _ := x509.ParseCertificate(der)
		return certificate, key
	}
	profileSigner, profileKey := leaf(2, "Synthetic Profile Signer")
	signingCertificate, signingKey := leaf(3, "Synthetic Distribution")
	entitlements := map[string]any{"application-identifier": "TEAM123456.com.example.Pomeforge", "com.apple.developer.team-identifier": "TEAM123456", "get-task-allow": false, "keychain-access-groups": []string{"TEAM123456.*"}}
	profile := map[string]any{"UUID": "AAAAAAAA-BBBB-CCCC-DDDD-EEEEEEEEEEEE", "Name": "Synthetic App Store", "CreationDate": now.Add(-time.Hour), "ExpirationDate": now.Add(24 * time.Hour), "TeamIdentifier": []string{"TEAM123456"}, "ApplicationIdentifierPrefix": []string{"TEAM123456"}, "DeveloperCertificates": [][]byte{signingCertificate.Raw}, "Entitlements": entitlements}
	content, err := plist.Marshal(profile, plist.XMLFormat)
	if err != nil {
		t.Fatal(err)
	}
	signed, err := pkcs7.NewSignedData(content)
	if err != nil {
		t.Fatal(err)
	}
	if err := signed.AddSignerChain(profileSigner, profileKey, []*x509.Certificate{root}, pkcs7.SignerInfoConfig{}); err != nil {
		t.Fatal(err)
	}
	cms, err := signed.Finish()
	if err != nil {
		t.Fatal(err)
	}
	paths := signingFixturePaths{key: filepath.Join(directory, "private.key"), cert: filepath.Join(directory, "certificate.pem"), profile: filepath.Join(directory, "profile.mobileprovision"), root: filepath.Join(directory, "root.pem")}
	writeTestFile(t, paths.key, pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(signingKey)}), 0o600)
	writeTestFile(t, paths.cert, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: signingCertificate.Raw}), 0o600)
	writeTestFile(t, paths.profile, cms, 0o600)
	writeTestFile(t, paths.root, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: root.Raw}), 0o600)
	return paths
}

func mustTestRSA(t *testing.T) *rsa.PrivateKey {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	return key
}

func writeTestFile(t *testing.T, path string, data []byte, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, mode); err != nil {
		t.Fatal(err)
	}
}

func digestBytes(data []byte) string {
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:])
}

func digestFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return digestBytes(data)
}

func containsText(values []string, text string) bool {
	for _, value := range values {
		if strings.Contains(value, text) {
			return true
		}
	}
	return false
}

func TestDTXcodeVersionTransformation(t *testing.T) {
	for input, expected := range map[string]string{"26.6": "2660", "15.4.1": "1541"} {
		actual, err := encodeDTXcodeVersion(input)
		if err != nil || actual != expected {
			t.Fatalf("%s => %s, %v; want %s", input, actual, err, expected)
		}
	}
	if _, err := encodeDTXcodeVersion("26-beta"); err == nil {
		t.Fatal("fabricated nonnumeric Xcode version accepted")
	}
}

func Example_releaseCommands() {
	fmt.Println("release-build -> export -> upload -> validate -> submit")
	// Output: release-build -> export -> upload -> validate -> submit
}

type releaseWorkflowFixture struct {
	workspace, project, bundle, ipa, identity, argumentLog string
	info, machO                                            []byte
	paths                                                  signingFixturePaths
	tools                                                  fakeTools
	manager                                                *ReleaseManager
	service                                                *Service
}

func newReleaseWorkflowFixture(t *testing.T) releaseWorkflowFixture {
	t.Helper()
	workspace := t.TempDir()
	projectSummary, err := CreateProject(workspace, "Pomeforge", "Pomeforge", "com.example.Pomeforge")
	if err != nil {
		t.Fatal(err)
	}
	project := projectSummary.Path
	manifest, _, err := LoadManifest(project)
	if err != nil {
		t.Fatal(err)
	}
	manifest.MarketingVersion, manifest.BuildNumber = "1.2.3", "42"
	manifestBytes, _ := json.MarshalIndent(manifest, "", "  ")
	writeTestFile(t, filepath.Join(project, "pomeforge.json"), append(manifestBytes, '\n'), 0o644)
	if err := os.Rename(filepath.Join(project, "Assets.xcassets"), filepath.Join(project, "SourceAssets.xcassets")); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)
	paths := writeSigningFixture(t, t.TempDir(), now)
	state := t.TempDir()
	tools := availableTools(t)
	argumentLog := filepath.Join(t.TempDir(), "zsign-args")
	prebuilt := filepath.Join(t.TempDir(), "signed.ipa")
	info := syntheticReleaseInfo(t)
	machO := syntheticReleaseMachO()
	writeSyntheticIPA(t, prebuilt, info, machO, readTestFile(t, paths.profile), "")
	zsign := filepath.Join(t.TempDir(), "zsign")
	script := fmt.Sprintf("#!/bin/sh\nprintf '%%s\\n' \"$@\" > %s\nout=\nwhile [ \"$#\" -gt 0 ]; do\n  if [ \"$1\" = -o ]; then out=$2; shift 2; else shift; fi\ndone\ncp %s \"$out\"\n", argumentLog, prebuilt)
	writeTestFile(t, zsign, []byte(script), 0o700)
	tools["zsign"] = ToolStatus{ID: "zsign", Name: "zsign", Status: "available", Version: "fixture", Path: zsign}
	manager := NewReleaseManager(workspace, state, tools, &Executor{})
	manager.Now = func() time.Time { return now }
	manager.Binder = fixedSDKBinding{report: testSDKBinding()}
	identity := "release-main"
	configure := PlanInput{Action: "signing-configure", Identity: identity, IdentityLabel: "Release main", PrivateKeyPath: paths.key, CertificatePath: paths.cert, ProfilePath: paths.profile, TrustedRootPaths: []string{paths.root}}
	identityPlan, err := manager.Plan(context.Background(), configure)
	if err != nil {
		t.Fatal(err)
	}
	if result, executeErr := manager.Execute(context.Background(), identityPlan, configure, false); executeErr != nil || result.Status != "succeeded" {
		t.Fatalf("configure result=%#v err=%v", result, executeErr)
	}
	bundle := filepath.Join(project, "xtool", "Pomeforge.app")
	writeSyntheticBundle(t, bundle, info, machO)
	ipa := filepath.Join(project, "Candidate.ipa")
	writeSyntheticIPA(t, ipa, info, machO, readTestFile(t, paths.profile), "")
	service := &Service{Workspace: workspace, Tools: tools, Planner: Planner{Workspace: workspace, Tools: tools}, Release: manager, Executor: manager.Executor, plans: map[string]PlanInput{}}
	return releaseWorkflowFixture{workspace: workspace, project: project, bundle: bundle, ipa: ipa, identity: identity, argumentLog: argumentLog, info: info, machO: machO, paths: paths, tools: tools, manager: manager, service: service}
}

func syntheticReleaseInfo(t *testing.T) []byte {
	t.Helper()
	iconNames := map[string]bool{}
	for _, slot := range generatedIconSlots {
		if slot.Idiom != "ios-marketing" {
			iconNames["AppIcon"+slot.Size] = true
		}
	}
	names := make([]string, 0, len(iconNames))
	for name := range iconNames {
		names = append(names, name)
	}
	sort.Strings(names)
	info := map[string]any{
		"CFBundleIdentifier": "com.example.Pomeforge", "CFBundleShortVersionString": "1.2.3", "CFBundleVersion": "42",
		"CFBundleExecutable": "Pomeforge", "MinimumOSVersion": "17.0", "UIDeviceFamily": []int{1, 2},
		"CFBundleIconName": "AppIcon", "CFBundleIconFiles": names,
		"DTPlatformName": "iphoneos", "DTPlatformVersion": "26.5", "DTPlatformBuild": "23F81a",
		"DTSDKName": "iphoneos26.5", "DTSDKBuild": "23F81a", "DTXcode": "2660", "DTXcodeBuild": "17F113",
	}
	data, err := plist.Marshal(info, plist.XMLFormat)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func syntheticReleaseMachO() []byte {
	buffer := new(bytes.Buffer)
	for _, value := range []uint32{0xfeedfacf, 0x0100000c, 0, 2, 1, 24, 0, 0, 0x32, 24, 2, 17 << 16, (26 << 16) | (5 << 8), 0} {
		_ = binary.Write(buffer, binary.LittleEndian, value)
	}
	return buffer.Bytes()
}

func writeSyntheticBundle(t *testing.T, bundle string, info, machO []byte) {
	t.Helper()
	writeTestFile(t, filepath.Join(bundle, "Info.plist"), info, 0o644)
	writeTestFile(t, filepath.Join(bundle, "Pomeforge"), machO, 0o755)
	for name, data := range compiledIconFixtureFiles(t) {
		writeTestFile(t, filepath.Join(bundle, name), data, 0o644)
	}
}

func writeSyntheticIPA(t *testing.T, destination string, info, machO, profile []byte, comment string) {
	t.Helper()
	temporary := destination + ".new"
	_ = os.Remove(temporary)
	file, err := os.OpenFile(temporary, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	writer := zip.NewWriter(file)
	if comment != "" {
		if err := writer.SetComment(comment); err != nil {
			t.Fatal(err)
		}
	}
	entries := map[string]struct {
		data []byte
		mode os.FileMode
	}{
		"Payload/Pomeforge.app/Info.plist":                   {info, 0o644},
		"Payload/Pomeforge.app/Pomeforge":                    {machO, 0o755},
		"Payload/Pomeforge.app/embedded.mobileprovision":     {profile, 0o644},
		"Payload/Pomeforge.app/_CodeSignature/CodeResources": {[]byte("synthetic structural fixture"), 0o644},
	}
	for name, data := range compiledIconFixtureFiles(t) {
		entries["Payload/Pomeforge.app/"+name] = struct {
			data []byte
			mode os.FileMode
		}{data, 0o644}
	}
	names := make([]string, 0, len(entries))
	for name := range entries {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		header := &zip.FileHeader{Name: name, Method: zip.Deflate}
		header.SetMode(entries[name].mode)
		entry, createErr := writer.CreateHeader(header)
		if createErr != nil {
			t.Fatal(createErr)
		}
		if _, writeErr := entry.Write(entries[name].data); writeErr != nil {
			t.Fatal(writeErr)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(temporary, destination); err != nil {
		t.Fatal(err)
	}
}

func compiledIconFixtureFiles(t *testing.T) map[string][]byte {
	t.Helper()
	generated, err := generatedIconFiles(placeholderIcon(), false)
	if err != nil {
		t.Fatal(err)
	}
	result := map[string][]byte{}
	for _, slot := range generatedIconSlots {
		dimension, err := iconDimension(slot.Size, slot.Scale)
		if err != nil {
			t.Fatal(err)
		}
		name := "AppIcon" + slot.Size
		if slot.Scale != "1x" {
			name += "@" + slot.Scale
		}
		result[name+".png"] = generated[fmt.Sprintf("AppIcon-%d.png", dimension)]
	}
	return result
}

func readTestFile(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func copyTestFile(t *testing.T, source, destination string) string {
	t.Helper()
	writeTestFile(t, destination, readTestFile(t, source), 0o600)
	return destination
}
