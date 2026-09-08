package orchard

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"orchard.local/orchard/internal/bootstrap"
)

func setupPaths(root string) bootstrap.Paths {
	return bootstrap.Paths{ToolsDir: filepath.Join(root, "data", "tools"), DownloadsDir: filepath.Join(root, "cache", "downloads"), BinDir: filepath.Join(root, "data", "bin")}
}

func setupCatalog(t *testing.T, serverURL string, artifact []byte) bootstrap.Catalog {
	t.Helper()
	digest := sha256.Sum256(artifact)
	value := fmt.Sprintf(`{"schemaVersion":1,"verifiedAt":"2026-09-08","tools":[{"id":"asc","version":"5.0.0","source":"https://example.test/source","release":"https://example.test/release","assets":[{"os":"linux","arch":"amd64","url":%q,"sha256":%q,"size":%d,"format":"binary"}]}]}`, serverURL, hex.EncodeToString(digest[:]), len(artifact))
	catalog, err := bootstrap.Load(strings.NewReader(value))
	if err != nil {
		t.Fatal(err)
	}
	return catalog
}

func TestWorkspaceToolInstallUsesBootstrapAndVerifiedDiscovery(t *testing.T) {
	artifact := []byte("#!/bin/sh\nprintf 'asc version 5.0.0\\n'\n")
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(artifact) }))
	defer server.Close()
	root := t.TempDir()
	paths := setupPaths(root)
	catalog := setupCatalog(t, server.URL, artifact)
	resolver := &IntegratedToolResolver{Catalog: catalog, Paths: paths, StateDir: filepath.Join(root, "data"), HostOS: "linux", HostArch: "amd64"}
	manager := &SetupManager{Catalog: catalog, Paths: paths, Installer: bootstrap.Installer{Client: server.Client()}, Tools: resolver, HostOS: "linux", HostArch: "amd64", StateDir: filepath.Join(root, "data")}
	workspace := filepath.Join(root, "workspace")
	if err := os.Mkdir(workspace, 0o700); err != nil {
		t.Fatal(err)
	}
	service, err := NewService(workspace, resolver)
	if err != nil {
		t.Fatal(err)
	}
	service.Setup = manager
	input := PlanInput{Action: "tool-install", Tool: "asc"}
	plan, err := service.PlanOperation(context.Background(), input, true)
	if err != nil {
		t.Fatal(err)
	}
	if plan.ToolInstall == nil || plan.ToolInstall.Version != "5.0.0" || plan.ToolInstall.DownloadSize != int64(len(artifact)) || plan.Steps[0].Kind != "internal" || plan.Steps[0].Executable != "" {
		t.Fatalf("unexpected install plan: %#v", plan)
	}
	result, err := service.RunStored(context.Background(), plan.ID, false)
	if err != nil || result.Status != "succeeded" || result.Scope != "workspace" {
		t.Fatalf("result=%#v err=%v", result, err)
	}
	installPlan, _ := bootstrap.PlanInstall(catalog, "asc", "linux", "amd64", paths)
	resolved, err := filepath.EvalSymlinks(installPlan.ExecutablePath())
	if err != nil {
		t.Fatal(err)
	}
	if status := resolver.Probe(context.Background(), "asc"); status.Status != "available" || status.Path != installPlan.ExecutablePath() || status.CanonicalPath != resolved {
		t.Fatalf("installed status = %#v", status)
	}
	if err := os.WriteFile(resolved, []byte("corrupt"), 0o700); err != nil {
		t.Fatal(err)
	}
	if status := resolver.Probe(context.Background(), "asc"); status.Status == "available" {
		t.Fatalf("corrupt managed receipt counted as available: %#v", status)
	}
	historyRoot, err := manager.workspaceHistoryRootFor(workspace)
	if err != nil {
		t.Fatal(err)
	}
	history, err := LoadHistory(historyRoot, 10)
	if err != nil || len(history) != 1 || history[0].Scope != "workspace" {
		t.Fatalf("history=%#v err=%v", history, err)
	}
}

func TestWorkspaceHistoryFailureReturnsPopulatedResult(t *testing.T) {
	root := t.TempDir()
	workspace := filepath.Join(root, "workspace")
	if err := os.Mkdir(workspace, 0o700); err != nil {
		t.Fatal(err)
	}
	xtool := filepath.Join(root, "xtool")
	swift := filepath.Join(root, "swift")
	for _, path := range []string{xtool, swift} {
		if err := os.WriteFile(path, []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	tools := fakeTools{"xtool": {ID: "xtool", Name: "xtool", Status: "available", Path: xtool}, "swift": {ID: "swift", Name: "Swift", Status: "available", Path: swift}}
	service, err := NewService(workspace, tools)
	if err != nil {
		t.Fatal(err)
	}
	service.Setup.StateDir = filepath.Join(root, "state")
	historyRoot, err := service.Setup.workspaceHistoryRootFor(workspace)
	if err != nil {
		t.Fatal(err)
	}
	service.Setup.Runner = setupRunnerFunc(func(_ context.Context, _ string, _ []string, _ string, _ []string) (string, int, error) {
		historyPath := filepath.Join(historyRoot, ".orchard", "history.jsonl")
		if err := os.Remove(historyPath); err != nil {
			return "", 1, err
		}
		if err := os.Mkdir(historyPath, 0o700); err != nil {
			return "", 1, err
		}
		return "Darwin SDK: Installed\n", 0, nil
	})
	input := PlanInput{Action: "sdk-status", reservation: workspace + "\x00fixture"}
	plan := Plan{Action: "sdk-status", Scope: "workspace", Executable: true, Steps: []Step{
		{Kind: "process", Tool: "xtool", Executable: xtool, Args: []string{"sdk", "status"}, Directory: root},
		{Kind: "process", Tool: "swift", Executable: swift, Args: []string{"sdk", "list"}, Directory: root},
		{Kind: "process", Tool: "swift", Executable: swift, Args: []string{"sdk", "configure", "darwin", sdkTargetTriple, "--show-configuration"}, Directory: root},
	}}
	result, err := service.Setup.Execute(context.Background(), plan, input)
	var coded *CodedError
	if !errors.As(err, &coded) || coded.Code != "history_failed" || coded.Result == nil {
		t.Fatalf("result=%#v err=%#v", result, err)
	}
	if coded.Result.ID != result.ID || result.Scope != "workspace" || result.Output == "" {
		t.Fatalf("result-bearing error lost operation: result=%#v error=%#v", result, coded.Result)
	}
}

func TestInvalidManagedActivationCannotBecomeSystemFallback(t *testing.T) {
	root := t.TempDir()
	paths := setupPaths(root)
	artifact := []byte("#!/bin/sh\nprintf 'asc version 5.0.0\\n'\n")
	catalog := setupCatalog(t, "https://example.test/asc", artifact)
	if err := os.MkdirAll(paths.BinDir, 0o700); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(root, "outside-asc")
	marker := filepath.Join(root, "executed")
	if err := os.WriteFile(outside, []byte("#!/bin/sh\nprintf ran >\""+marker+"\"\nprintf 'asc version 5.0.0\\n'\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(paths.BinDir, "asc")); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", paths.BinDir)
	resolver := &IntegratedToolResolver{Catalog: catalog, Paths: paths, StateDir: filepath.Join(root, "data"), HostOS: "linux", HostArch: "amd64"}
	status := resolver.Probe(context.Background(), "asc")
	if status.Status != "unverified" {
		t.Fatalf("status = %#v", status)
	}
	if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("invalid managed activation was executed: %v", err)
	}
}

type sdkRunner struct {
	mu                           sync.Mutex
	installed                    bool
	statusCalls                  int
	installCalls                 int
	failBuild                    bool
	becomeInstalledOnThirdStatus bool
	clangResource                string
	commands                     [][]string
	executables                  []string
	environments                 [][]string
	installedBundle              string
	activeRoot                   string
}

type setupRunnerFunc func(context.Context, string, []string, string, []string) (string, int, error)

func (f setupRunnerFunc) Run(ctx context.Context, executable string, args []string, directory string, environment []string) (string, int, error) {
	return f(ctx, executable, args, directory, environment)
}

func environmentValue(environment []string, key string) string {
	prefix := key + "="
	for _, item := range environment {
		if strings.HasPrefix(item, prefix) {
			return strings.TrimPrefix(item, prefix)
		}
	}
	return ""
}

func (r *sdkRunner) Run(_ context.Context, executable string, args []string, _ string, environment []string) (string, int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.commands = append(r.commands, append([]string(nil), args...))
	r.executables = append(r.executables, executable)
	r.environments = append(r.environments, append([]string(nil), environment...))
	if len(args) >= 2 && args[0] == "sdk" && args[1] == "status" {
		r.statusCalls++
		if r.becomeInstalledOnThirdStatus && r.statusCalls >= 3 {
			r.installed = true
		}
		if r.installed {
			return "Darwin SDK: Installed\n", 0, nil
		}
		return "Darwin SDK: Not installed\n", 0, nil
	}
	if len(args) >= 2 && args[0] == "sdk" && args[1] == "build" {
		output := args[3]
		if err := writeBuiltSDKFixture(output); err != nil {
			return "", 1, err
		}
		if err := os.WriteFile(filepath.Join(output, "build-marker"), []byte("preserve"), 0o600); err != nil {
			return "", 1, err
		}
		if r.failBuild {
			return "synthetic build failure\n", 9, errors.New("build failed")
		}
		return "built\n", 0, nil
	}
	if len(args) >= 2 && args[0] == "sdk" && args[1] == "install" {
		r.installCalls++
		configHome := environmentValue(environment, "XDG_CONFIG_HOME")
		if configHome == "" {
			return "", 1, errors.New("missing XDG_CONFIG_HOME")
		}
		parent := filepath.Join(configHome, "swiftpm", "swift-sdks")
		if err := os.MkdirAll(parent, 0o700); err != nil {
			return "", 1, err
		}
		r.installedBundle = filepath.Join(parent, "darwin.artifactbundle")
		if err := copyOwnedTree(context.Background(), args[2], r.installedBundle); err != nil {
			return "", 1, err
		}
		r.activeRoot = filepath.Join(r.installedBundle, "Platforms", "iPhoneOS.platform", "Developer", "SDKs", "iPhoneOS26.5.sdk")
		r.installed = true
		return "installed\n", 0, nil
	}
	if len(args) == 2 && args[0] == "sdk" && args[1] == "list" {
		if r.installed {
			return "darwin\n", 0, nil
		}
		return "", 0, nil
	}
	if len(args) == 5 && args[0] == "sdk" && args[1] == "configure" {
		if r.installed && r.activeRoot != "" {
			return "sdkRootPath: " + r.activeRoot + "\ntoolsetPaths: []\n", 0, nil
		}
		return "", 1, errors.New("not installed")
	}
	if len(args) == 1 && args[0] == "-print-resource-dir" {
		return r.clangResource + "\n", 0, nil
	}
	return "", 1, errors.New("unexpected command")
}

func writeBuiltSDKFixture(output string) error {
	built := filepath.Join(output, "darwin.xtoolsdk")
	sdkRelative := filepath.Join("Platforms", "iPhoneOS.platform", "Developer", "SDKs", "iPhoneOS26.5.sdk")
	if err := os.MkdirAll(filepath.Join(built, sdkRelative), 0o700); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(built, "sdk-marker"), []byte("preserve"), 0o644); err != nil {
		return err
	}
	if err := os.Symlink("sdk-marker", filepath.Join(built, "sdk-link")); err != nil {
		return err
	}
	clangLibrary := filepath.Join(built, "Developer", "Toolchains", "XcodeDefault.xctoolchain", "usr", "lib")
	if err := os.MkdirAll(filepath.Join(clangLibrary, "clang", "21", "include"), 0o700); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(clangLibrary, "clang", "21", "include", "staged.h"), []byte("staged header\n"), 0o600); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Join(clangLibrary, "swift"), 0o700); err != nil {
		return err
	}
	if err := os.Symlink(filepath.Join("..", "clang", "21"), filepath.Join(clangLibrary, "swift", "clang")); err != nil {
		return err
	}
	metadata := fmt.Sprintf(`{"schemaVersion":"4.0","targetTriples":{"%s":{"sdkRootPath":%q}}}`, sdkTargetTriple, filepath.ToSlash(sdkRelative))
	return os.WriteFile(filepath.Join(built, "swift-sdk.json"), []byte(metadata), 0o600)
}

func writePlist(t *testing.T, path string, values map[string]string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	var value strings.Builder
	value.WriteString(`<?xml version="1.0" encoding="UTF-8"?><plist version="1.0"><dict>`)
	for key, item := range values {
		fmt.Fprintf(&value, "<key>%s</key><string>%s</string>", key, item)
	}
	value.WriteString(`</dict></plist>`)
	if err := os.WriteFile(path, []byte(value.String()), 0o600); err != nil {
		t.Fatal(err)
	}
}

func xcodeFixture(t *testing.T, root string) string {
	t.Helper()
	xcode := filepath.Join(root, "Xcode.app")
	writePlist(t, filepath.Join(xcode, "Contents", "version.plist"), map[string]string{"CFBundleShortVersionString": "26.6", "ProductBuildVersion": "17G86", "CFBundleVersion": "24332"})
	platform := filepath.Join(xcode, "Contents", "Developer", "Platforms", "iPhoneOS.platform")
	writePlist(t, filepath.Join(platform, "version.plist"), map[string]string{"CFBundleShortVersionString": "26.5", "ProductBuildVersion": "23F90", "CFBundleVersion": "17.0", "BuildVersion": "99"})
	writePlist(t, filepath.Join(platform, "Info.plist"), map[string]string{"Version": "26.5"})
	sdk := filepath.Join(platform, "Developer", "SDKs", "iPhoneOS26.5.sdk")
	if err := os.MkdirAll(sdk, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sdk, "SDKSettings.json"), []byte(`{"Version":"26.5","CanonicalName":"iphoneos26.5","ProductBuildVersion":"23F90"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sdk, "SDKSettings.plist"), binaryMetadataFixture(t), 0o600); err != nil {
		t.Fatal(err)
	}
	writePlist(t, filepath.Join(sdk, "System", "Library", "CoreServices", "SystemVersion.plist"), map[string]string{"ProductVersion": "26.5.1", "ProductBuildVersion": "23F90"})
	if err := os.Symlink("iPhoneOS26.5.sdk", filepath.Join(filepath.Dir(sdk), "iPhoneOS.sdk")); err != nil {
		t.Fatal(err)
	}
	return xcode
}

func binaryMetadataFixture(t *testing.T) []byte {
	t.Helper()
	fixture, err := hex.DecodeString("62706c6973743030d30102030405065f101a434642756e646c6553686f727456657273696f6e537472696e675f100f434642756e646c6556657273696f6e5f101350726f647563744275696c6456657273696f6e5432362e36553234333332553137473836080f2c3e54595f0000000000000101000000000000000700000000000000000000000000000065")
	if err != nil {
		t.Fatal(err)
	}
	return fixture
}

func sdkService(t *testing.T, runner *sdkRunner) (*Service, string) {
	t.Helper()
	root := t.TempDir()
	workspace := filepath.Join(root, "workspace")
	if err := os.Mkdir(workspace, 0o700); err != nil {
		t.Fatal(err)
	}
	xtool := filepath.Join(root, "xtool")
	if err := os.WriteFile(xtool, []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	swift := filepath.Join(root, "swift")
	if err := os.WriteFile(swift, []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	clang := filepath.Join(root, "clang")
	if err := os.WriteFile(clang, []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	clangResource := filepath.Join(root, "clang-resource")
	if err := os.MkdirAll(filepath.Join(clangResource, "include"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(clangResource, "include", "stddef.h"), []byte("fixture header\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runner.clangResource = clangResource
	tools := fakeTools{
		"xtool": {ID: "xtool", Name: "xtool", Status: "available", Version: "xtool 1.19.0", Path: xtool, Detail: "fixture"},
		"swift": {ID: "swift", Name: "Swift", Status: "available", Version: "Swift version 6.3.3", Path: swift, Detail: "fixture"},
		"clang": {ID: "clang", Name: "Clang", Status: "available", Version: "clang version 18.0.0", Path: clang, Detail: "fixture"},
	}
	service, err := NewService(workspace, tools)
	if err != nil {
		t.Fatal(err)
	}
	service.Setup.Tools = tools
	service.Setup.Runner = runner
	service.Setup.HostOS = "linux"
	service.Setup.HostArch = "amd64"
	service.Setup.StateDir = filepath.Join(root, "state")
	service.Setup.XDGConfigHome = filepath.Join(root, "config")
	service.Executor.XDGConfigHome = service.Setup.XDGConfigHome
	service.Planner.XDGConfigHome = service.Setup.XDGConfigHome
	return service, root
}

func TestSDKImportSwiftFingerprintAndMinimumAreRequired(t *testing.T) {
	runner := &sdkRunner{}
	service, root := sdkService(t, runner)
	xcode := xcodeFixture(t, root)
	tools := service.Tools.(fakeTools)
	swift := tools["swift"]
	swift.Status = "incompatible"
	swift.Detail = "Swift 6.2 is too old"
	tools["swift"] = swift
	blocked, err := service.PlanOperation(context.Background(), PlanInput{Action: "sdk-import", InputPath: xcode, Arch: "x86_64"}, false)
	if err != nil || blocked.Executable || !strings.Contains(strings.Join(blocked.Blockers, " "), "Swift 6.3") {
		t.Fatalf("blocked=%#v err=%v", blocked, err)
	}
	swift.Status = "available"
	swift.Detail = "fixture"
	tools["swift"] = swift
	plan, err := service.PlanOperation(context.Background(), PlanInput{Action: "sdk-import", InputPath: xcode, Arch: "x86_64"}, true)
	if err != nil || !plan.Executable {
		t.Fatalf("plan=%#v err=%v", plan, err)
	}
	if err := os.WriteFile(swift.Path, []byte("#!/bin/sh\necho changed\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := service.RunStored(context.Background(), plan.ID, false); err == nil || !strings.Contains(err.Error(), "changed") {
		t.Fatalf("changed Swift did not stale the plan: %v", err)
	}
}

func TestSDKImportPreservesSwiftAliasAndBindsCanonicalMapping(t *testing.T) {
	service, root := sdkService(t, &sdkRunner{})
	tools := service.Tools.(fakeTools)
	swift := tools["swift"]
	firstTarget := filepath.Join(root, "swift-driver-first")
	secondTarget := filepath.Join(root, "swift-driver-second")
	if err := os.Rename(swift.Path, firstTarget); err != nil {
		t.Fatal(err)
	}
	contents, err := os.ReadFile(firstTarget)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(secondTarget, contents, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(firstTarget, swift.Path); err != nil {
		t.Fatal(err)
	}
	tools["swift"] = swift
	input := PlanInput{Action: "sdk-import", InputPath: xcodeFixture(t, root), Arch: "x86_64"}
	plan, err := service.PlanOperation(context.Background(), input, true)
	if err != nil || !plan.Executable {
		t.Fatalf("plan=%#v err=%v", plan, err)
	}
	if plan.SDKImport.SwiftExecutable != swift.Path {
		t.Fatalf("Swift invocation path = %q, want %q", plan.SDKImport.SwiftExecutable, swift.Path)
	}
	wantDigest, err := hashRegularFile(context.Background(), firstTarget, maxToolFingerprintBytes)
	if err != nil || plan.SDKImport.SwiftSHA256 != wantDigest {
		t.Fatalf("Swift digest = %q, want canonical target digest %q (err=%v)", plan.SDKImport.SwiftSHA256, wantDigest, err)
	}
	if err := os.Remove(swift.Path); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(secondTarget, swift.Path); err != nil {
		t.Fatal(err)
	}
	if _, err := service.RunStored(context.Background(), plan.ID, false); err == nil || !strings.Contains(err.Error(), "changed") {
		t.Fatalf("same-byte Swift alias retarget did not stale SDK import: %v", err)
	}
}

func TestSDKImportsAreSerializedAcrossPlans(t *testing.T) {
	service, root := sdkService(t, &sdkRunner{})
	xcode := xcodeFixture(t, root)
	buildStarted := make(chan struct{})
	releaseBuild := make(chan struct{})
	var mu sync.Mutex
	installed, builds, installs := false, 0, 0
	activeRoot := ""
	service.Setup.Runner = setupRunnerFunc(func(ctx context.Context, _ string, args []string, _ string, environment []string) (string, int, error) {
		if len(args) >= 2 && args[0] == "sdk" && args[1] == "status" {
			mu.Lock()
			value := installed
			mu.Unlock()
			if value {
				return "Darwin SDK: Installed\n", 0, nil
			}
			return "Darwin SDK: Not installed\n", 0, nil
		}
		if len(args) >= 2 && args[0] == "sdk" && args[1] == "build" {
			mu.Lock()
			builds++
			first := builds == 1
			mu.Unlock()
			if !first {
				return "", 1, errors.New("concurrent build reached")
			}
			if err := writeBuiltSDKFixture(args[3]); err != nil {
				return "", 1, err
			}
			close(buildStarted)
			select {
			case <-releaseBuild:
			case <-ctx.Done():
				return "", 1, ctx.Err()
			}
			return "built\n", 0, nil
		}
		if len(args) >= 2 && args[0] == "sdk" && args[1] == "install" {
			configHome := environmentValue(environment, "XDG_CONFIG_HOME")
			parent := filepath.Join(configHome, "swiftpm", "swift-sdks")
			if err := os.MkdirAll(parent, 0o700); err != nil {
				return "", 1, err
			}
			installedBundle := filepath.Join(parent, "darwin.artifactbundle")
			if err := copyOwnedTree(context.Background(), args[2], installedBundle); err != nil {
				return "", 1, err
			}
			mu.Lock()
			installs++
			installed = true
			activeRoot = filepath.Join(installedBundle, "Platforms", "iPhoneOS.platform", "Developer", "SDKs", "iPhoneOS26.5.sdk")
			mu.Unlock()
			return "installed\n", 0, nil
		}
		if len(args) == 2 && args[0] == "sdk" && args[1] == "list" {
			mu.Lock()
			value := installed
			mu.Unlock()
			if value {
				return "darwin\n", 0, nil
			}
			return "", 0, nil
		}
		if len(args) == 5 && args[0] == "sdk" && args[1] == "configure" {
			mu.Lock()
			value := activeRoot
			mu.Unlock()
			if value != "" {
				return "sdkRootPath: " + value + "\n", 0, nil
			}
			return "", 1, errors.New("not installed")
		}
		if len(args) == 1 && args[0] == "-print-resource-dir" {
			return filepath.Join(root, "clang-resource") + "\n", 0, nil
		}
		return "", 1, errors.New("unexpected command")
	})
	input := PlanInput{Action: "sdk-import", InputPath: xcode, Arch: "x86_64"}
	firstPlan, err := service.PlanOperation(context.Background(), input, true)
	if err != nil {
		t.Fatal(err)
	}
	secondPlan, err := service.PlanOperation(context.Background(), input, true)
	if err != nil {
		t.Fatal(err)
	}
	firstDone := make(chan OperationResult, 1)
	go func() { result, _ := service.RunStored(context.Background(), firstPlan.ID, false); firstDone <- result }()
	select {
	case <-buildStarted:
	case <-time.After(time.Second):
		t.Fatal("first SDK build did not start")
	}
	second, err := service.RunStored(context.Background(), secondPlan.ID, false)
	if err != nil {
		t.Fatal(err)
	}
	if second.Status != "failed" || !strings.Contains(second.Output, "another SDK import is in progress") {
		t.Fatalf("second result = %#v", second)
	}
	close(releaseBuild)
	select {
	case first := <-firstDone:
		if first.Status != "succeeded" {
			t.Fatalf("first result = %#v", first)
		}
	case <-time.After(time.Second):
		t.Fatal("first SDK import did not finish")
	}
	mu.Lock()
	defer mu.Unlock()
	if builds != 1 || installs != 1 {
		t.Fatalf("builds=%d installs=%d", builds, installs)
	}
}

func TestSDKDiagnosticProbeUsesShortTimeout(t *testing.T) {
	service, _ := sdkService(t, &sdkRunner{})
	service.Setup.ProbeTimeout = 10 * time.Millisecond
	service.Setup.Runner = setupRunnerFunc(func(ctx context.Context, _ string, _ []string, _ string, _ []string) (string, int, error) {
		<-ctx.Done()
		return "", 124, ctx.Err()
	})
	started := time.Now()
	status := service.Setup.ProbeSDK(context.Background())
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("probe took %s", elapsed)
	}
	if status.Status != "unverified" {
		t.Fatalf("status = %#v", status)
	}
}

func TestSDKStatusParsingAndImportProvenance(t *testing.T) {
	binaryFixture := binaryMetadataFixture(t)
	parsed, parseErr := parseXMLPlistStrings(binaryFixture)
	if parseErr != nil || parsed["CFBundleShortVersionString"] != "26.6" || parsed["ProductBuildVersion"] != "17G86" || parsed["CFBundleVersion"] != "24332" {
		t.Fatalf("binary plist metadata=%#v err=%v", parsed, parseErr)
	}
	if installed, recognized := parseSDKStatus("Darwin SDK: Not installed\n"); installed || !recognized {
		t.Fatal("not-installed status was not recognized")
	}
	if installed, recognized := parseSDKStatus("Darwin SDK: Installed\n"); !installed || !recognized {
		t.Fatal("installed status was not recognized")
	}
	runner := &sdkRunner{}
	service, root := sdkService(t, runner)
	xcode := xcodeFixture(t, root)
	plan, err := service.PlanOperation(context.Background(), PlanInput{Action: "sdk-import", InputPath: xcode, Arch: "x86_64"}, true)
	if err != nil {
		t.Fatal(err)
	}
	if !plan.Executable || plan.SDKImport.Metadata["xcodeVersion"] != "26.6" || plan.SDKImport.Metadata["sdkVersion"] != "26.5" || plan.SDKImport.Metadata["sdkProductVersion"] != "26.5.1" || plan.SDKImport.Metadata["xcodeBuildVersion"] != "17G86" || plan.SDKImport.Metadata["platformBuildVersion"] != "23F90" {
		t.Fatalf("unexpected SDK plan: %#v", plan)
	}
	settingsPlistPath := "Contents/Developer/Platforms/iPhoneOS.platform/Developer/SDKs/iPhoneOS26.5.sdk/SDKSettings.plist"
	if plan.SDKImport.MetadataHashes[settingsPlistPath] == "" || !strings.HasSuffix(plan.SDKImport.Sources["sdkVersion"], "SDKSettings.json#Version") {
		t.Fatalf("mixed JSON/binary SDK settings provenance was not retained: %#v", plan.SDKImport)
	}
	if len(plan.Steps) != 8 || plan.Steps[1].Tool != "clang" || plan.Steps[2].Operation != "stage-sdk-artifactbundle" || plan.Steps[4].Tool != "swift" || plan.Steps[4].Args[0] != "sdk" || plan.Steps[4].Args[1] != "install" || !strings.HasSuffix(plan.Steps[4].Args[2], "darwin.artifactbundle") || strings.Join(plan.Steps[7].Args, " ") != "sdk configure darwin arm64-apple-ios --show-configuration" {
		t.Fatalf("SDK plan does not represent non-root install sequence: %#v", plan.Steps)
	}
	if plan.SDKImport.XDGConfigHome != service.Setup.XDGConfigHome || plan.SDKImport.ClangHeadersSHA256 == "" || len(plan.SDKImport.MetadataHashes) < 4 {
		t.Fatalf("SDK environment fingerprint = %#v", plan.SDKImport)
	}
	result, err := service.RunStored(context.Background(), plan.ID, false)
	if err != nil || result.Status != "succeeded" {
		t.Fatalf("result=%#v err=%v", result, err)
	}
	if result.Metadata["trust"] != "operator_supplied" || result.Metadata["sdkVersion"] != "26.5" || result.Metadata["sdkProductVersion"] != "26.5.1" || result.Metadata["xcodeVersion"] != "26.6" {
		t.Fatalf("provenance = %#v", result.Metadata)
	}
	if runner.installCalls != 1 {
		t.Fatalf("install calls = %d", runner.installCalls)
	}
	if status := service.Setup.ProbeSDK(context.Background()); status.Status != "available" || !strings.Contains(status.Detail, "arm64-apple-ios") {
		t.Fatalf("installed SDK diagnostic was not verified: %#v", status)
	}
	for index, environment := range runner.environments {
		if got := environmentValue(environment, "XDG_CONFIG_HOME"); got != service.Setup.XDGConfigHome {
			t.Fatalf("command %d XDG_CONFIG_HOME=%q", index, got)
		}
	}
	artifact := filepath.Join(plan.SDKImport.StageRoot, "darwin.artifactbundle")
	if contents, err := os.ReadFile(filepath.Join(artifact, "sdk-marker")); err != nil || string(contents) != "preserve" {
		t.Fatalf("staged SDK content=%q err=%v", contents, err)
	}
	if target, err := os.Readlink(filepath.Join(artifact, "sdk-link")); err != nil || target != "sdk-marker" {
		t.Fatalf("contained symlink target=%q err=%v", target, err)
	}
	if _, err := os.Stat(filepath.Join(plan.SDKImport.StageRoot, "sdk-build", "darwin.xtoolsdk", "sdk-marker")); err != nil {
		t.Fatalf("source SDK was not preserved: %v", err)
	}
	receipt, err := LoadSDKEnvironmentReceipt(service.Setup.StateDir)
	if err != nil {
		t.Fatal(err)
	}
	platformMetadataPath := "Contents/Developer/Platforms/iPhoneOS.platform/version.plist"
	if receipt.XDGConfigHome != service.Setup.XDGConfigHome || receipt.SDKStatus == "" || receipt.Metadata["platformBuildVersion"] != "23F90" || receipt.MetadataSources["platformBuildVersion"] != platformMetadataPath+"#ProductBuildVersion" || receipt.MetadataHashes[platformMetadataPath] == "" || len(receipt.MetadataHashes) < 4 || receipt.ActiveSDKRoot == receipt.InstalledArtifactBundle || receipt.TargetTriple != sdkTargetTriple || receipt.MetadataSnapshotRoot == "" {
		t.Fatalf("receipt=%#v", receipt)
	}
	if contents, err := os.ReadFile(filepath.Join(receipt.MetadataSnapshotRoot, filepath.FromSlash(platformMetadataPath))); err != nil || len(contents) == 0 {
		t.Fatalf("retained platform metadata is unavailable: bytes=%d err=%v", len(contents), err)
	}
	if err := os.RemoveAll(xcode); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadSDKEnvironmentReceipt(service.Setup.StateDir); err != nil {
		t.Fatalf("receipt did not survive removal of operator input: %v", err)
	}
	retainedPath := filepath.Join(receipt.MetadataSnapshotRoot, filepath.FromSlash(platformMetadataPath))
	if err := os.WriteFile(retainedPath, []byte("tampered"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadSDKEnvironmentReceipt(service.Setup.StateDir); err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("tampered retained metadata was accepted: %v", err)
	}
	if info, err := os.Stat(artifact); err != nil {
		t.Fatal(err)
	} else if stat, ok := info.Sys().(*syscall.Stat_t); !ok || stat.Uid != uint32(os.Getuid()) {
		t.Fatalf("artifact owner=%#v", info.Sys())
	}
}

func TestSDKMetadataDoesNotFlattenNestedOrConflateVersions(t *testing.T) {
	nested := []byte(`<?xml version="1.0"?><plist version="1.0"><dict><key>Version</key><string>26.5</string><key>Nested</key><dict><key>ProductBuildVersion</key><string>wrong</string></dict><key>CanonicalName</key><string>iphoneos26.5</string></dict></plist>`)
	values, err := parseXMLPlistStrings(nested)
	if err != nil || values["Version"] != "26.5" || values["CanonicalName"] != "iphoneos26.5" || values["ProductBuildVersion"] != "" {
		t.Fatalf("nested plist values=%#v err=%v", values, err)
	}
	duplicate := []byte(`<?xml version="1.0"?><plist version="1.0"><dict><key>Version</key><string>26.5</string><key>Version</key><string>26.6</string></dict></plist>`)
	if _, err := parseXMLPlistStrings(duplicate); err == nil || !strings.Contains(err.Error(), "duplicate") {
		t.Fatalf("duplicate key accepted: %v", err)
	}
	if _, err := parseJSONMetadataScalars([]byte(`{"Version":"26.5","Nested":{"ProductBuildVersion":"wrong"},"Version":"26.6"}`)); err == nil || !strings.Contains(err.Error(), "duplicate") {
		t.Fatalf("duplicate JSON key accepted: %v", err)
	}
	if _, err := parseBinaryPlistStrings(binaryPlistWithDuplicateNonScalarKey()); err == nil || !strings.Contains(err.Error(), "duplicate") {
		t.Fatalf("duplicate binary plist key accepted: %v", err)
	}
	root := t.TempDir()
	xcode := xcodeFixture(t, root)
	sdkSettings := filepath.Join(xcode, "Contents", "Developer", "Platforms", "iPhoneOS.platform", "Developer", "SDKs", "iPhoneOS26.5.sdk", "SDKSettings.json")
	if err := os.Remove(sdkSettings); err != nil {
		t.Fatal(err)
	}
	metadata, err := inspectXcodeMetadata(xcode)
	if err != nil {
		t.Fatal(err)
	}
	if metadata.Values["sdkVersion"] != "" || metadata.Values["sdkProductVersion"] != "26.5.1" {
		t.Fatalf("metadata conflated versions: %#v", metadata.Values)
	}
}

func TestInstalledSDKSelectionRejectsConfigurationMetadataMismatch(t *testing.T) {
	root := t.TempDir()
	installed := filepath.Join(root, "darwin.artifactbundle")
	if err := writeBuiltSDKFixture(root); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(filepath.Join(root, "darwin.xtoolsdk"), installed); err != nil {
		t.Fatal(err)
	}
	other := filepath.Join(installed, "Platforms", "iPhoneOS.platform", "Developer", "SDKs", "Other.sdk")
	if err := os.Mkdir(other, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := verifyInstalledSDKSelection(context.Background(), installed, "darwin\n", "sdkRootPath: "+other+"\n"); err == nil || !strings.Contains(err.Error(), "disagrees") {
		t.Fatalf("mismatched configured SDK root was accepted: %v", err)
	}
}

func binaryPlistWithDuplicateNonScalarKey() []byte {
	data := append([]byte("bplist00"), 0xd2, 0x01, 0x01, 0x02, 0x03)
	offsets := []byte{8, byte(len(data))}
	data = append(data, 0x57)
	data = append(data, []byte("Version")...)
	offsets = append(offsets, byte(len(data)))
	data = append(data, 0xd0)
	offsets = append(offsets, byte(len(data)))
	data = append(data, 0x53)
	data = append(data, []byte("two")...)
	offsetTable := len(data)
	data = append(data, offsets...)
	trailer := make([]byte, 32)
	trailer[6], trailer[7] = 1, 1
	binary.BigEndian.PutUint64(trailer[8:16], 4)
	binary.BigEndian.PutUint64(trailer[16:24], 0)
	binary.BigEndian.PutUint64(trailer[24:32], uint64(offsetTable))
	return append(data, trailer...)
}

func TestSDKArtifactStagingRejectsEscapingSymlink(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "darwin.xtoolsdk")
	headers := filepath.Join(root, "headers")
	if err := os.MkdirAll(source, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(headers, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(headers, "stddef.h"), []byte("header"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("../../outside", filepath.Join(source, "escape")); err != nil {
		t.Fatal(err)
	}
	digest, err := hashSafeDirectory(context.Background(), headers, 1<<20, 100)
	if err != nil {
		t.Fatal(err)
	}
	if err := stageSDKArtifactBundle(context.Background(), source, filepath.Join(root, "darwin.artifactbundle"), headers, digest); err == nil || !strings.Contains(err.Error(), "escapes") {
		t.Fatalf("escaping symlink was accepted: %v", err)
	}
}

func TestSDKArtifactStagingReplacesHeadersThroughContainedClangAlias(t *testing.T) {
	root := t.TempDir()
	buildRoot := filepath.Join(root, "sdk-build")
	if err := writeBuiltSDKFixture(buildRoot); err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(buildRoot, "darwin.xtoolsdk")
	clangLibrary := filepath.Join("Developer", "Toolchains", "XcodeDefault.xctoolchain", "usr", "lib")
	aliasRelative := filepath.Join(clangLibrary, "swift", "clang")
	sourceAlias := filepath.Join(source, aliasRelative)
	if target, err := os.Readlink(sourceAlias); err != nil || target != filepath.Join("..", "clang", "21") {
		t.Fatalf("source Clang alias=%q err=%v", target, err)
	}

	headers := filepath.Join(root, "selected-headers")
	if err := os.Mkdir(headers, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(headers, "stddef.h"), []byte("selected header\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	digest, err := hashSafeDirectory(context.Background(), headers, 1<<20, 100)
	if err != nil {
		t.Fatal(err)
	}
	artifact := filepath.Join(root, "darwin.artifactbundle")
	if err := stageSDKArtifactBundle(context.Background(), source, artifact, headers, digest); err != nil {
		t.Fatal(err)
	}

	artifactAlias := filepath.Join(artifact, aliasRelative)
	if target, err := os.Readlink(artifactAlias); err != nil || target != filepath.Join("..", "clang", "21") {
		t.Fatalf("staged Clang alias=%q err=%v", target, err)
	}
	if contents, err := os.ReadFile(filepath.Join(artifactAlias, "include", "stddef.h")); err != nil || string(contents) != "selected header\n" {
		t.Fatalf("selected staged header=%q err=%v", contents, err)
	}
	if _, err := os.Stat(filepath.Join(artifactAlias, "include", "staged.h")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("old staged header remains: %v", err)
	}
	if contents, err := os.ReadFile(filepath.Join(sourceAlias, "include", "staged.h")); err != nil || string(contents) != "staged header\n" {
		t.Fatalf("source header changed: %q err=%v", contents, err)
	}
	if _, err := os.Stat(filepath.Join(sourceAlias, "include", "stddef.h")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("selected header was written into source: %v", err)
	}
}

func TestClangAliasEscapeCannotModifyOutsideHeaders(t *testing.T) {
	root := t.TempDir()
	artifact := filepath.Join(root, "darwin.artifactbundle")
	clangLibrary := filepath.Join(artifact, "Developer", "Toolchains", "XcodeDefault.xctoolchain", "usr", "lib")
	if err := os.MkdirAll(filepath.Join(clangLibrary, "clang", "21"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(clangLibrary, "swift"), 0o700); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(root, "outside", "21")
	if err := os.MkdirAll(filepath.Join(outside, "include"), 0o700); err != nil {
		t.Fatal(err)
	}
	canary := filepath.Join(outside, "include", "canary.h")
	if err := os.WriteFile(canary, []byte("outside canary\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(clangLibrary, "swift", "clang")); err != nil {
		t.Fatal(err)
	}
	headers := filepath.Join(root, "selected-headers")
	if err := os.Mkdir(headers, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(headers, "stddef.h"), []byte("selected\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	libRelative := filepath.Join("Developer", "Toolchains", "XcodeDefault.xctoolchain", "usr", "lib")
	err := replaceDirectoryThroughContainedAlias(context.Background(), artifact, filepath.Join(libRelative, "swift", "clang"), filepath.Join(libRelative, "clang"), "include", headers)
	if err == nil || !strings.Contains(err.Error(), "escapes") {
		t.Fatalf("escaping Clang alias was accepted: %v", err)
	}
	if contents, err := os.ReadFile(canary); err != nil || string(contents) != "outside canary\n" {
		t.Fatalf("outside canary changed: %q err=%v", contents, err)
	}
	if _, err := os.Stat(filepath.Join(outside, "include", "stddef.h")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("selected header reached outside directory: %v", err)
	}
}

func TestClangAliasRejectsMissingBrokenAndCyclicTargets(t *testing.T) {
	for _, test := range []struct {
		name   string
		target string
	}{
		{name: "missing"},
		{name: "broken", target: filepath.Join("..", "clang", "99")},
		{name: "cyclic", target: "clang"},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			artifact := filepath.Join(root, "darwin.artifactbundle")
			libRelative := filepath.Join("Developer", "Toolchains", "XcodeDefault.xctoolchain", "usr", "lib")
			clangLibrary := filepath.Join(artifact, libRelative)
			if err := os.MkdirAll(filepath.Join(clangLibrary, "clang", "21", "include"), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.MkdirAll(filepath.Join(clangLibrary, "swift"), 0o700); err != nil {
				t.Fatal(err)
			}
			if test.target != "" {
				if err := os.Symlink(test.target, filepath.Join(clangLibrary, "swift", "clang")); err != nil {
					t.Fatal(err)
				}
			}
			canary := filepath.Join(clangLibrary, "clang", "21", "include", "canary.h")
			if err := os.WriteFile(canary, []byte("staged canary\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			headers := filepath.Join(root, "selected-headers")
			if err := os.Mkdir(headers, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(headers, "stddef.h"), []byte("selected\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			err := replaceDirectoryThroughContainedAlias(context.Background(), artifact, filepath.Join(libRelative, "swift", "clang"), filepath.Join(libRelative, "clang"), "include", headers)
			if err == nil {
				t.Fatalf("%s Clang alias was accepted", test.name)
			}
			if contents, err := os.ReadFile(canary); err != nil || string(contents) != "staged canary\n" {
				t.Fatalf("staged canary changed: %q err=%v", contents, err)
			}
		})
	}
}

func TestOpenedClangAliasTargetCannotBeRedirectedByAliasSubstitution(t *testing.T) {
	root := t.TempDir()
	artifact := filepath.Join(root, "darwin.artifactbundle")
	libRelative := filepath.Join("Developer", "Toolchains", "XcodeDefault.xctoolchain", "usr", "lib")
	clangLibrary := filepath.Join(artifact, libRelative)
	inside := filepath.Join(clangLibrary, "clang", "21")
	if err := os.MkdirAll(filepath.Join(inside, "include"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(clangLibrary, "swift"), 0o700); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(clangLibrary, "swift", "clang")
	if err := os.Symlink(filepath.Join("..", "clang", "21"), alias); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(root, "outside", "21")
	if err := os.MkdirAll(filepath.Join(outside, "include"), 0o700); err != nil {
		t.Fatal(err)
	}
	canary := filepath.Join(outside, "include", "canary.h")
	if err := os.WriteFile(canary, []byte("outside canary\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	rootDirectory, err := openDirectoryAbsolute(artifact)
	if err != nil {
		t.Fatal(err)
	}
	defer rootDirectory.Close()
	aliasParts, err := relativePathParts(artifact, alias, false)
	if err != nil {
		t.Fatal(err)
	}
	allowedParts, err := relativePathParts(artifact, filepath.Join(clangLibrary, "clang"), false)
	if err != nil {
		t.Fatal(err)
	}
	openedTarget, err := openContainedDirectoryAlias(rootDirectory, aliasParts, allowedParts)
	if err != nil {
		t.Fatal(err)
	}
	defer openedTarget.Close()
	if err := os.Remove(alias); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, alias); err != nil {
		t.Fatal(err)
	}

	headers := filepath.Join(root, "selected-headers")
	if err := os.Mkdir(headers, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(headers, "stddef.h"), []byte("selected\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := replaceDirectoryWithOwnedTreeAt(context.Background(), openedTarget, "include", inside, headers); err != nil {
		t.Fatal(err)
	}
	if contents, err := os.ReadFile(filepath.Join(inside, "include", "stddef.h")); err != nil || string(contents) != "selected\n" {
		t.Fatalf("held contained target was not updated: %q err=%v", contents, err)
	}
	if contents, err := os.ReadFile(canary); err != nil || string(contents) != "outside canary\n" {
		t.Fatalf("outside canary changed after alias substitution: %q err=%v", contents, err)
	}
	if _, err := os.Stat(filepath.Join(outside, "include", "stddef.h")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("replacement followed substituted alias: %v", err)
	}
}

func TestSDKArtifactStagingDoesNotFollowRebasedAncestorIntoSource(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "darwin.xtoolsdk")
	realToolchains := filepath.Join(source, "real-toolchains")
	originalHeaders := filepath.Join(realToolchains, "XcodeDefault.xctoolchain", "usr", "lib", "swift", "clang", "include")
	if err := os.MkdirAll(originalHeaders, 0o700); err != nil {
		t.Fatal(err)
	}
	canary := filepath.Join(originalHeaders, "canary.h")
	if err := os.WriteFile(canary, []byte("preserve original headers"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(source, "Developer"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(realToolchains, filepath.Join(source, "Developer", "Toolchains")); err != nil {
		t.Fatal(err)
	}
	headers := filepath.Join(root, "selected-headers")
	if err := os.Mkdir(headers, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(headers, "stddef.h"), []byte("selected"), 0o600); err != nil {
		t.Fatal(err)
	}
	digest, err := hashSafeDirectory(context.Background(), headers, 1<<20, 100)
	if err != nil {
		t.Fatal(err)
	}
	artifact := filepath.Join(root, "darwin.artifactbundle")
	err = stageSDKArtifactBundle(context.Background(), source, artifact, headers, digest)
	if err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("symlinked destination ancestor was accepted: %v", err)
	}
	contents, readErr := os.ReadFile(canary)
	if readErr != nil || string(contents) != "preserve original headers" {
		t.Fatalf("source header canary changed: %q %v", contents, readErr)
	}
	link, readLinkErr := os.Readlink(filepath.Join(artifact, "Developer", "Toolchains"))
	if readLinkErr != nil || filepath.IsAbs(link) {
		t.Fatalf("contained absolute source link was not safely rebased: %q %v", link, readLinkErr)
	}
	resolved, resolveErr := filepath.EvalSymlinks(filepath.Join(artifact, "Developer", "Toolchains"))
	if resolveErr != nil || !pathInsideAny(resolved, artifact) {
		t.Fatalf("rebased link escapes artifact: %q %v", resolved, resolveErr)
	}
}

func TestSDKTreeCopyKeepsOpenedDirectoriesAfterAncestorReplacement(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "source")
	if err := os.Mkdir(source, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "inside"), []byte("copy this"), 0o600); err != nil {
		t.Fatal(err)
	}
	sourceDirectory, err := openDirectoryAbsolute(source)
	if err != nil {
		t.Fatal(err)
	}
	defer sourceDirectory.Close()
	parkedSource := filepath.Join(root, "parked-source")
	outsideSource := filepath.Join(root, "outside-source")
	if err := os.Mkdir(outsideSource, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outsideSource, "outside"), []byte("do not copy"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(source, parkedSource); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outsideSource, source); err != nil {
		t.Fatal(err)
	}

	destinationParentPath := filepath.Join(root, "destination-parent")
	if err := os.Mkdir(destinationParentPath, 0o700); err != nil {
		t.Fatal(err)
	}
	destinationParent, err := openDirectoryAbsolute(destinationParentPath)
	if err != nil {
		t.Fatal(err)
	}
	defer destinationParent.Close()
	parkedDestination := filepath.Join(root, "parked-destination")
	outsideDestination := filepath.Join(root, "outside-destination")
	if err := os.Mkdir(outsideDestination, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(destinationParentPath, parkedDestination); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outsideDestination, destinationParentPath); err != nil {
		t.Fatal(err)
	}

	if err := copyOwnedTreeToParent(context.Background(), sourceDirectory, source, destinationParent, "artifact", filepath.Join(destinationParentPath, "artifact")); err != nil {
		t.Fatal(err)
	}
	contents, err := os.ReadFile(filepath.Join(parkedDestination, "artifact", "inside"))
	if err != nil || string(contents) != "copy this" {
		t.Fatalf("opened source was not copied to opened destination: %q %v", contents, err)
	}
	if entries, err := os.ReadDir(outsideDestination); err != nil || len(entries) != 0 {
		t.Fatalf("substituted destination was modified: entries=%v err=%v", entries, err)
	}
}

func TestSDKPlanRejectsChangedXDGConfigHome(t *testing.T) {
	service, root := sdkService(t, &sdkRunner{})
	plan, err := service.PlanOperation(context.Background(), PlanInput{Action: "sdk-import", InputPath: xcodeFixture(t, root), Arch: "x86_64"}, true)
	if err != nil {
		t.Fatal(err)
	}
	service.Setup.XDGConfigHome = filepath.Join(root, "different-config")
	_, err = service.RunStored(context.Background(), plan.ID, false)
	var coded *CodedError
	if !errors.As(err, &coded) || coded.Code != "stale_plan" {
		t.Fatalf("changed XDG config root accepted: %v", err)
	}
}

func TestSDKImportRefusesReplacementImmediatelyBeforeInstall(t *testing.T) {
	runner := &sdkRunner{becomeInstalledOnThirdStatus: true}
	service, root := sdkService(t, runner)
	xcode := xcodeFixture(t, root)
	plan, err := service.PlanOperation(context.Background(), PlanInput{Action: "sdk-import", InputPath: xcode, Arch: "x86_64"}, true)
	if err != nil || !plan.Executable {
		t.Fatalf("plan=%#v err=%v", plan, err)
	}
	result, err := service.RunStored(context.Background(), plan.ID, false)
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "failed" || !strings.Contains(result.Output, "refusing destructive replacement") {
		t.Fatalf("result = %#v", result)
	}
	if runner.installCalls != 0 {
		t.Fatalf("destructive install was called %d times", runner.installCalls)
	}
}

func TestSDKImportRejectsSymlinkedSwiftSDKDestination(t *testing.T) {
	runner := &sdkRunner{}
	service, root := sdkService(t, runner)
	plan, err := service.PlanOperation(context.Background(), PlanInput{Action: "sdk-import", InputPath: xcodeFixture(t, root), Arch: "x86_64"}, true)
	if err != nil || !plan.Executable {
		t.Fatalf("plan=%#v err=%v", plan, err)
	}
	redirect := filepath.Join(root, "redirected-swift-state")
	if err := os.MkdirAll(redirect, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(service.Setup.XDGConfigHome, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(redirect, filepath.Join(service.Setup.XDGConfigHome, "swiftpm")); err != nil {
		t.Fatal(err)
	}
	result, err := service.RunStored(context.Background(), plan.ID, false)
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "failed" || !strings.Contains(result.Output, "symlink") {
		t.Fatalf("result=%#v", result)
	}
	if runner.installCalls != 0 {
		t.Fatalf("Swift install ran through a redirected destination %d times", runner.installCalls)
	}
	if entries, err := os.ReadDir(redirect); err != nil || len(entries) != 0 {
		t.Fatalf("redirect destination changed: entries=%v err=%v", entries, err)
	}
}

func TestSDKBuildFailurePreservesFreshReservedOutput(t *testing.T) {
	runner := &sdkRunner{failBuild: true}
	service, root := sdkService(t, runner)
	plan, err := service.PlanOperation(context.Background(), PlanInput{Action: "sdk-import", InputPath: xcodeFixture(t, root), Arch: "x86_64"}, true)
	if err != nil {
		t.Fatal(err)
	}
	result, err := service.RunStored(context.Background(), plan.ID, false)
	if err != nil || result.Status != "failed" {
		t.Fatalf("result=%#v err=%v", result, err)
	}
	marker := filepath.Join(plan.SDKImport.StageRoot, "sdk-build", "build-marker")
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("failed build output was not preserved: %v", err)
	}
	fresh, err := service.PlanOperation(context.Background(), PlanInput{Action: "sdk-import", InputPath: xcodeFixture(t, filepath.Join(root, "second")), Arch: "x86_64"}, false)
	if err != nil {
		t.Fatal(err)
	}
	if fresh.SDKImport.StageRoot == plan.SDKImport.StageRoot {
		t.Fatal("fresh plan reused a prior output reservation")
	}
}

func TestSDKAliasEscapeRejected(t *testing.T) {
	root := t.TempDir()
	xcode := xcodeFixture(t, root)
	alias := filepath.Join(xcode, "Contents", "Developer", "Platforms", "iPhoneOS.platform", "Developer", "SDKs", "iPhoneOS.sdk")
	if err := os.Remove(alias); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(t.TempDir(), alias); err != nil {
		t.Fatal(err)
	}
	if _, err := inspectXcodeMetadata(xcode); err == nil || !strings.Contains(err.Error(), "escapes") {
		t.Fatalf("escaping SDK alias accepted: %v", err)
	}
}

func TestXIPImportUsesUnxipFreshOutputAndRetainsMissingMetadataTruthfully(t *testing.T) {
	inner := &sdkRunner{}
	service, root := sdkService(t, inner)
	unxip := filepath.Join(root, "unxip")
	if err := os.WriteFile(unxip, []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	service.Tools.(fakeTools)["unxip"] = ToolStatus{ID: "unxip", Name: "unxip", Status: "available", Version: "unxip 3.3", Path: unxip, Detail: "fixture"}
	service.Setup.Tools = service.Tools
	service.Setup.Runner = setupRunnerFunc(func(ctx context.Context, executable string, args []string, directory string, environment []string) (string, int, error) {
		if executable == unxip {
			if len(args) != 3 || args[0] != "--statistics" {
				return "", 1, errors.New("wrong unxip arguments")
			}
			if directory != filepath.Dir(args[2]) || args[2] != filepath.Join(directory, "xip-extracted") || !pathInsideAny(args[2], directory) {
				return "", 1, errors.New("unxip output was not confined to the reserved stage root")
			}
			info, err := os.Lstat(args[2])
			if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() != 0o700 {
				return "", 1, errors.New("unxip output was not an existing private mode 0700 directory")
			}
			entries, err := os.ReadDir(args[2])
			if err != nil || len(entries) != 0 {
				return "", 1, errors.New("unxip output was not empty")
			}
			xcodeFixture(t, args[2])
			return "extracted\n", 0, nil
		}
		return inner.Run(ctx, executable, args, directory, environment)
	})
	xip := filepath.Join(root, "Xcode.xip")
	if err := os.WriteFile(xip, []byte("operator supplied XIP fixture"), 0o600); err != nil {
		t.Fatal(err)
	}
	plan, err := service.PlanOperation(context.Background(), PlanInput{Action: "sdk-import", InputPath: xip, Arch: "x86_64"}, true)
	if err != nil || !plan.Executable {
		t.Fatalf("plan=%#v err=%v", plan, err)
	}
	if plan.SDKImport.InputKind != "xip" || len(plan.SDKImport.Metadata) != 0 || len(plan.SDKImport.Missing) == 0 {
		t.Fatalf("XIP metadata was invented: %#v", plan.SDKImport)
	}
	if got := plan.Steps[0].Args; len(got) != 3 || got[0] != "--statistics" || got[1] != xip || got[2] != filepath.Join(plan.SDKImport.StageRoot, "xip-extracted") {
		t.Fatalf("unxip args = %#v", got)
	}
	result, err := service.RunStored(context.Background(), plan.ID, false)
	if err != nil || result.Status != "succeeded" {
		t.Fatalf("result=%#v err=%v", result, err)
	}
	if result.Metadata["xcodeVersion"] != "26.6" || result.Metadata["sdkVersion"] != "26.5" {
		t.Fatalf("extracted metadata = %#v", result.Metadata)
	}
	if contents, err := os.ReadFile(xip); err != nil || string(contents) != "operator supplied XIP fixture" {
		t.Fatalf("original XIP changed: %q %v", contents, err)
	}
}

func TestLegacyHistoryRemainsUnscoped(t *testing.T) {
	workspace, project := testProject(t, AppStoreIDs{})
	legacy := OperationResult{ID: "legacy", Action: "build", Status: "failed", ExitCode: 1}
	if err := ensureHistory(workspace, project); err != nil {
		t.Fatal(err)
	}
	if err := appendHistory(workspace, project, legacy); err != nil {
		t.Fatal(err)
	}
	service, err := NewService(workspace, fakeTools{})
	if err != nil {
		t.Fatal(err)
	}
	state, err := service.State(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, result := range state.History {
		if result.ID == "legacy" {
			found = true
			if result.Scope != "" || result.Project != "" || result.ProjectLabel != "" {
				t.Fatalf("legacy result was guessed into scope: %#v", result)
			}
		}
	}
	if !found {
		t.Fatal("legacy history record was not loaded")
	}
}

func TestBlockedCapabilitiesDescribeActualMissingPrerequisites(t *testing.T) {
	workspace := t.TempDir()
	tools := fakeTools{
		"swift":          {ID: "swift", Name: "Swift", Status: "available", Detail: "verified"},
		"xtool":          {ID: "xtool", Name: "xtool", Status: "missing", Detail: "executable not found"},
		"orchard-assets": {ID: "orchard-assets", Name: "Orchard AssetKit bridge", Status: "unverified", Detail: "receipt mismatch"},
		"unxip":          {ID: "unxip", Name: "unxip", Status: "missing", Detail: "not registered"},
		"usbmuxd":        {ID: "usbmuxd", Name: "usbmuxd", Status: "missing", Detail: "executable not found"},
		"asc":            {ID: "asc", Name: "ASC CLI", Status: "missing", Detail: "executable not found"},
	}
	service, err := NewService(workspace, tools)
	if err != nil {
		t.Fatal(err)
	}
	service.Setup.ProbeTimeout = time.Millisecond
	state, err := service.State(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, capability := range state.Capabilities {
		if capability.Status != "blocked" {
			continue
		}
		if strings.Contains(capability.Detail, "are available") || strings.Contains(capability.Detail, "is available") {
			t.Fatalf("blocked capability claims availability: %#v", capability)
		}
		if capability.ID != "distribution-signing" && !strings.Contains(capability.Detail, "Required prerequisites are unavailable") {
			t.Fatalf("blocked capability lacks prerequisite detail: %#v", capability)
		}
	}
}

func TestHelperRegistrationStoresIntegrityAndKnownAssetKitPin(t *testing.T) {
	resolved, err := os.ReadFile(filepath.Join("..", "..", "tools", "asset-compiler", "Package.resolved"))
	if err != nil || !strings.Contains(string(resolved), assetKitRevision) {
		t.Fatalf("composed AssetKit pin does not match %s: %v", assetKitRevision, err)
	}
	root := t.TempDir()
	workspace := filepath.Join(root, "workspace")
	if err := os.Mkdir(workspace, 0o700); err != nil {
		t.Fatal(err)
	}
	helper := filepath.Join(root, "orchard-assets")
	if err := os.WriteFile(helper, []byte("#!/bin/sh\nif [ \"$1\" = --help ]; then echo 'Usage: orchard-assets compile'; exit 0; fi\nexit 2\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	resolver := &IntegratedToolResolver{StateDir: filepath.Join(root, "state")}
	service, err := NewService(workspace, resolver)
	if err != nil {
		t.Fatal(err)
	}
	service.Setup.StateDir = resolver.StateDir
	service.Setup.Tools = resolver
	input := PlanInput{Action: "helper-register", Helper: "orchard-assets", ExecutablePath: helper, AssetKitRevision: assetKitRevision}
	plan, err := service.PlanOperation(context.Background(), input, true)
	if err != nil || !plan.Executable {
		t.Fatalf("plan=%#v err=%v", plan, err)
	}
	result, err := service.RunStored(context.Background(), plan.ID, false)
	if err != nil || result.Status != "succeeded" {
		t.Fatalf("result=%#v err=%v", result, err)
	}
	receipts, err := loadHelperReceipts(filepath.Join(resolver.StateDir, "helpers.json"))
	if err != nil || len(receipts) != 1 || receipts[0].AssetKitRevision != assetKitRevision || receipts[0].SHA256 == "" {
		t.Fatalf("receipts=%#v err=%v", receipts, err)
	}
	if status := resolver.Probe(context.Background(), "orchard-assets"); status.Status != "available" {
		t.Fatalf("registered status=%#v", status)
	}
	if err := os.WriteFile(helper, []byte("changed"), 0o700); err != nil {
		t.Fatal(err)
	}
	if status := resolver.Probe(context.Background(), "orchard-assets"); status.Status == "available" {
		t.Fatalf("changed helper remained available: %#v", status)
	}
}
