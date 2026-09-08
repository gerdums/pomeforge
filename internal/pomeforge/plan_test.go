package pomeforge

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"
	"time"
)

type fakeTools map[string]ToolStatus

func (f fakeTools) Probe(_ context.Context, id string) ToolStatus {
	if status, ok := f[id]; ok {
		return status
	}
	return ToolStatus{ID: id, Name: id, Status: "missing", Detail: "test missing", InstallURL: "https://example.invalid"}
}

func (f fakeTools) ProbeAll(ctx context.Context) []ToolStatus {
	ids := []string{"xtool", "swift", "asc", "zsign", "idevice_id", "usbmuxd"}
	result := make([]ToolStatus, 0, len(ids))
	for _, id := range ids {
		result = append(result, f.Probe(ctx, id))
	}
	return result
}

func availableTools(t *testing.T) fakeTools {
	t.Helper()
	root := t.TempDir()
	result := fakeTools{}
	for _, id := range []string{"xtool", "swift", "asc", "zsign", "idevice_id", "usbmuxd"} {
		path := filepath.Join(root, id)
		if err := os.WriteFile(path, []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil {
			t.Fatal(err)
		}
		result[id] = ToolStatus{ID: id, Name: id, Status: "available", Version: "test 1.0", Path: path, Detail: "test", InstallURL: "https://example.invalid"}
	}
	return result
}

func testProject(t *testing.T, appStore AppStoreIDs) (string, string) {
	t.Helper()
	workspace := t.TempDir()
	project, err := CreateProject(workspace, "Demo", "Demo", "com.example.Demo")
	if err != nil {
		t.Fatal(err)
	}
	if appStore != (AppStoreIDs{}) {
		manifest, _, err := LoadManifest(project.Path)
		if err != nil {
			t.Fatal(err)
		}
		manifest.AppStore = appStore
		encoded, _ := jsonMarshalIndent(manifest)
		if err := os.WriteFile(filepath.Join(project.Path, "pomeforge.json"), encoded, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return workspace, project.Path
}

func jsonMarshalIndent(value any) ([]byte, error) {
	data, err := json.MarshalIndent(value, "", "  ")
	return append(data, '\n'), err
}

func TestActionCatalogResourceIDParametersAreStrings(t *testing.T) {
	want := map[string]map[string]bool{
		"store-status": {"buildId": true},
		"validate":     {"versionId": true},
		"submit":       {"versionId": true, "buildId": true},
	}
	for _, action := range Actions() {
		parameters, ok := want[action.ID]
		if !ok {
			continue
		}
		for _, parameter := range action.Parameters {
			if !parameters[parameter.Name] {
				continue
			}
			if parameter.Type != "string" || !strings.Contains(parameter.Description, "resource ID") {
				t.Errorf("%s.%s = %#v; want string resource ID", action.ID, parameter.Name, parameter)
			}
			delete(parameters, parameter.Name)
		}
	}
	for action, parameters := range want {
		for parameter := range parameters {
			t.Errorf("missing public action metadata for %s.%s", action, parameter)
		}
	}
}

func TestVerifiedPlanCommands(t *testing.T) {
	workspace, project := testProject(t, AppStoreIDs{})
	ipa := filepath.Join(workspace, "Demo.ipa")
	if err := os.WriteFile(ipa, []byte("fixture"), 0o600); err != nil {
		t.Fatal(err)
	}
	planner := Planner{Workspace: workspace, Tools: availableTools(t)}
	tests := []struct {
		action  string
		input   PlanInput
		want    [][]string
		confirm bool
	}{
		{"build", PlanInput{Action: "build", Project: project}, [][]string{{"--version"}, {"dev", "build", "--configuration", "debug"}}, false},
		{"devices", PlanInput{Action: "devices", Project: project}, [][]string{{"devices", "--no-wait"}}, false},
		{"install", PlanInput{Action: "install", Project: project, IPA: ipa, Device: "abc-123"}, [][]string{{"install", "--udid", "abc-123", ipa}}, true},
		{"launch", PlanInput{Action: "launch", Project: project, Device: "abc-123"}, [][]string{{"launch", "--udid", "abc-123", "com.example.Demo"}}, false},
	}
	for _, test := range tests {
		t.Run(test.action, func(t *testing.T) {
			plan, err := planner.Plan(context.Background(), test.input)
			if err != nil {
				t.Fatal(err)
			}
			if !plan.Executable {
				t.Fatalf("unexpected blockers: %v", plan.Blockers)
			}
			if plan.Scope != "project" || plan.Project != "Demo" || plan.ProjectLabel != "Demo" {
				t.Fatalf("project scope was not bound truthfully: %#v", plan)
			}
			if plan.RequiresConfirmation != test.confirm {
				t.Errorf("confirmation = %t", plan.RequiresConfirmation)
			}
			if test.action == "install" && !strings.Contains(strings.Join(plan.Warnings, " "), "does not preserve") {
				t.Fatal("install plan omitted development-signing warning")
			}
			args := make([][]string, len(plan.Steps))
			for index := range plan.Steps {
				args[index] = plan.Steps[index].Args
				if strings.Contains(strings.Join(args[index], " "), "submit create") {
					t.Fatal("removed asc submit create command used")
				}
			}
			if !reflect.DeepEqual(args, test.want) {
				t.Fatalf("args = %#v, want %#v", args, test.want)
			}
		})
	}
}

func TestPlanIdentityIncludesSkippedDirectoryIPAAndStoredPlanGoesStale(t *testing.T) {
	workspace, project := testProject(t, AppStoreIDs{AppID: "1001"})
	if err := os.Mkdir(filepath.Join(project, "xtool"), 0o755); err != nil {
		t.Fatal(err)
	}
	ipa := filepath.Join(project, "xtool", "Demo.ipa")
	if err := os.WriteFile(ipa, []byte("same-one"), 0o600); err != nil {
		t.Fatal(err)
	}
	service, err := NewService(workspace, availableTools(t))
	if err != nil {
		t.Fatal(err)
	}
	plan, err := service.PlanOperation(context.Background(), PlanInput{Action: "install", Project: project, IPA: ipa, Device: "abc-123"}, true)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(ipa, []byte("same-two"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := service.RunStored(context.Background(), plan.ID, true); err == nil || !strings.Contains(err.Error(), "changed") {
		t.Fatalf("same-path, same-size skipped IPA mutation was not stale: %v", err)
	}
}

func TestPlanRejectsSpecialAndOversizedProjectFilesWithoutBlocking(t *testing.T) {
	workspace, project := testProject(t, AppStoreIDs{})
	fifo := filepath.Join(project, "fixture.fifo")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Fatal(err)
	}
	planner := Planner{Workspace: workspace, Tools: availableTools(t)}
	started := time.Now()
	if _, err := planner.Plan(context.Background(), PlanInput{Action: "build", Project: project}); err == nil || !strings.Contains(err.Error(), "nonregular") {
		t.Fatalf("expected FIFO rejection, got %v", err)
	}
	if time.Since(started) > time.Second {
		t.Fatal("FIFO validation blocked")
	}
	if err := os.Remove(fifo); err != nil {
		t.Fatal(err)
	}
	large := filepath.Join(project, "oversized.bin")
	file, err := os.Create(large)
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Truncate(maxFingerprintFileBytes + 1); err != nil {
		t.Fatal(err)
	}
	_ = file.Close()
	if _, err := planner.Plan(context.Background(), PlanInput{Action: "build", Project: project}); err == nil || !strings.Contains(err.Error(), "fingerprint limit") {
		t.Fatalf("expected oversized source rejection, got %v", err)
	}
}

func TestPlanBindsExecutableBytesAndEnforcesLinuxPolicy(t *testing.T) {
	workspace, project := testProject(t, AppStoreIDs{})
	executable := filepath.Join(workspace, "xtool-fixture")
	if err := os.WriteFile(executable, []byte("first"), 0o700); err != nil {
		t.Fatal(err)
	}
	info, _ := os.Stat(executable)
	tools := availableTools(t)
	tools["xtool"] = ToolStatus{ID: "xtool", Name: "xtool", Status: "available", Version: "xtool 1.19.0", Path: executable, Detail: "fixture"}
	planner := Planner{Workspace: workspace, Tools: tools}
	first, err := planner.Plan(context.Background(), PlanInput{Action: "devices", Project: project})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(executable, []byte("other"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(executable, info.ModTime(), info.ModTime()); err != nil {
		t.Fatal(err)
	}
	second, err := planner.Plan(context.Background(), PlanInput{Action: "devices", Project: project})
	if err != nil {
		t.Fatal(err)
	}
	if first.ID == second.ID {
		t.Fatal("same-size executable replacement did not change plan")
	}
	blocked, err := (Planner{Workspace: workspace, Tools: tools, HostOS: "darwin"}).Plan(context.Background(), PlanInput{Action: "build", Project: project})
	if err != nil {
		t.Fatal(err)
	}
	if blocked.Executable || !strings.Contains(strings.Join(blocked.Blockers, " "), "only when Pomeforge is running on Linux") {
		t.Fatalf("non-Linux build was not blocked: %#v", blocked)
	}
}

func TestPlanRejectsAvailableToolWithoutCanonicalBytes(t *testing.T) {
	workspace, project := testProject(t, AppStoreIDs{})
	tools := availableTools(t)
	status := tools["xtool"]
	status.Path = filepath.Join(t.TempDir(), "vanished-xtool")
	tools["xtool"] = status
	if _, err := (Planner{Workspace: workspace, Tools: tools}).Plan(context.Background(), PlanInput{Action: "devices", Project: project}); err == nil || !strings.Contains(err.Error(), "canonical regular file") {
		t.Fatalf("available tool without canonical bytes was accepted: %v", err)
	}
}

func TestStoredPlanPreservesMulticallAliasAndRejectsRetarget(t *testing.T) {
	root := t.TempDir()
	bin := filepath.Join(root, "bin")
	if err := os.Mkdir(bin, 0o700); err != nil {
		t.Fatal(err)
	}
	driverContents := []byte("#!/bin/sh\nif [ \"${0##*/}\" != swift ]; then printf 'invalid driver name: %s\\n' \"${0##*/}\" >&2; exit 1; fi\nprintf 'Swift version 6.3.3 (swift-6.3.3-RELEASE)\\n'\n")
	firstTarget := filepath.Join(root, "swift-driver-first")
	secondTarget := filepath.Join(root, "swift-driver-second")
	for _, target := range []string{firstTarget, secondTarget} {
		if err := os.WriteFile(target, driverContents, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	swiftAlias := filepath.Join(bin, "swift")
	if err := os.Symlink(firstTarget, swiftAlias); err != nil {
		t.Fatal(err)
	}
	xtool := filepath.Join(bin, "xtool")
	if err := os.WriteFile(xtool, []byte("#!/bin/sh\nif [ \"$1\" = --version ]; then printf 'xtool 1.19.0\\n'; fi\nexit 0\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	workspace, project := testProject(t, AppStoreIDs{})
	paths := setupPaths(filepath.Join(root, "managed"))
	resolver := &IntegratedToolResolver{Paths: paths, StateDir: filepath.Join(root, "state"), HostOS: "linux", HostArch: "amd64", XDGConfigHome: filepath.Join(root, "config")}
	service, err := NewService(workspace, resolver)
	if err != nil {
		t.Fatal(err)
	}
	input := PlanInput{Action: "build", Project: project}
	plan, err := service.PlanOperation(context.Background(), input, true)
	if err != nil || !plan.Executable {
		t.Fatalf("plan=%#v err=%v", plan, err)
	}
	if len(plan.Steps) < 1 || plan.Steps[0].Executable != swiftAlias {
		t.Fatalf("Swift invocation path was not preserved: %#v", plan.Steps)
	}
	if result, err := service.RunStored(context.Background(), plan.ID, false); err != nil || result.Status != "succeeded" {
		t.Fatalf("result=%#v err=%v", result, err)
	}

	plan, err = service.PlanOperation(context.Background(), input, true)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(swiftAlias); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(secondTarget, swiftAlias); err != nil {
		t.Fatal(err)
	}
	if _, err := service.RunStored(context.Background(), plan.ID, false); err == nil || !strings.Contains(err.Error(), "changed") {
		t.Fatalf("same-byte alias retarget did not stale the plan: %v", err)
	}
}

func TestStoredPlanIsConsumedAfterAttemptAndDuringConcurrentRun(t *testing.T) {
	workspace, project := testProject(t, AppStoreIDs{})
	executable := filepath.Join(workspace, "xtool-fixture")
	if err := os.WriteFile(executable, []byte("#!/bin/sh\nsleep 0.15\nexit 7\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	tools := availableTools(t)
	tools["xtool"] = ToolStatus{ID: "xtool", Name: "xtool", Status: "available", Path: executable, Detail: "fixture"}
	service, err := NewService(workspace, tools)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := service.PlanOperation(context.Background(), PlanInput{Action: "devices", Project: project}, true)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan OperationResult, 1)
	go func() {
		result, _ := service.RunStored(context.Background(), plan.ID, false)
		done <- result
	}()
	deadline := time.Now().Add(time.Second)
	for {
		service.mu.Lock()
		_, stillStored := service.plans[plan.ID]
		service.mu.Unlock()
		if !stillStored {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("first run did not reserve the stored plan")
		}
		time.Sleep(time.Millisecond)
	}
	if _, err := service.RunStored(context.Background(), plan.ID, false); err == nil || !strings.Contains(err.Error(), "expired") {
		t.Fatalf("concurrent replay was accepted: %v", err)
	}
	result := <-done
	if result.ExitCode != 7 || result.Status != "failed" {
		t.Fatalf("failed attempt result = %#v", result)
	}
	if _, err := service.RunStored(context.Background(), plan.ID, false); err == nil {
		t.Fatal("sequential replay was accepted")
	}
	replanned, err := service.PlanOperation(context.Background(), PlanInput{Action: "devices", Project: project}, true)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.RunStored(context.Background(), replanned.ID, false); err != nil {
		t.Fatalf("explicit re-planning did not create a new attempt: %v", err)
	}
}

func TestBlockedPlanAndStalePlan(t *testing.T) {
	workspace, project := testProject(t, AppStoreIDs{})
	service, err := NewService(workspace, fakeTools{})
	if err != nil {
		t.Fatal(err)
	}
	blocked, err := service.PlanOperation(context.Background(), PlanInput{Action: "upload", Project: project}, false)
	if err != nil {
		t.Fatal(err)
	}
	if blocked.Executable || len(blocked.Blockers) < 2 {
		t.Fatalf("expected honest blockers, got %#v", blocked)
	}
	service, err = NewService(workspace, availableTools(t))
	if err != nil {
		t.Fatal(err)
	}
	plan, err := service.PlanOperation(context.Background(), PlanInput{Action: "build", Project: project}, true)
	if err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(project, "Sources", "Demo", "DemoApp.swift")
	file, err := os.OpenFile(source, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = file.WriteString("\n// mutation\n")
	_ = file.Close()
	if _, err := service.RunStored(context.Background(), plan.ID, false); err == nil || !strings.Contains(err.Error(), "changed") {
		t.Fatalf("expected stale plan, got %v", err)
	}
}

func TestPlanIdentityIncludesIPAContent(t *testing.T) {
	workspace, project := testProject(t, AppStoreIDs{AppID: "1001"})
	ipa := filepath.Join(workspace, "Demo.ipa")
	if err := os.WriteFile(ipa, []byte("one"), 0o600); err != nil {
		t.Fatal(err)
	}
	planner := Planner{Workspace: workspace, Tools: availableTools(t)}
	first, err := planner.Plan(context.Background(), PlanInput{Action: "install", Project: project, IPA: ipa, Device: "fixture-device"})
	if err != nil {
		t.Fatal(err)
	}
	repeated, err := planner.Plan(context.Background(), PlanInput{Action: "install", Project: project, IPA: ipa, Device: "fixture-device"})
	if err != nil {
		t.Fatal(err)
	}
	if first.ID != repeated.ID {
		t.Fatalf("unchanged plan was not deterministic: %s != %s", first.ID, repeated.ID)
	}
	if err := os.WriteFile(ipa, []byte("two"), 0o600); err != nil {
		t.Fatal(err)
	}
	second, err := planner.Plan(context.Background(), PlanInput{Action: "install", Project: project, IPA: ipa, Device: "fixture-device"})
	if err != nil {
		t.Fatal(err)
	}
	if first.ID == second.ID {
		t.Fatal("IPA mutation did not change plan ID")
	}
}

func TestPlanFingerprintFramesProjectFileRecords(t *testing.T) {
	workspace, project := testProject(t, AppStoreIDs{})
	firstPath := filepath.Join(project, "zz-a")
	secondPath := filepath.Join(project, "zz-b")
	if err := os.WriteFile(firstPath, []byte("A"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(secondPath, []byte("B"), 0o600); err != nil {
		t.Fatal(err)
	}
	planner := Planner{Workspace: workspace, Tools: availableTools(t)}
	first, err := planner.Plan(context.Background(), PlanInput{Action: "build", Project: project})
	if err != nil {
		t.Fatal(err)
	}

	// Without record framing these two project shapes both contribute the raw
	// stream "zz-a\\0Azz-b\\0B" to the project fingerprint.
	if err := os.Remove(secondPath); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(firstPath, []byte("Azz-b\x00B"), 0o600); err != nil {
		t.Fatal(err)
	}
	second, err := planner.Plan(context.Background(), PlanInput{Action: "build", Project: project})
	if err != nil {
		t.Fatal(err)
	}
	if first.ID == second.ID {
		t.Fatal("reshaping project files across a path/content boundary did not change plan ID")
	}
}

func TestProjectLocalSelectedIPALargerThanSourceLimitUsesIPALimit(t *testing.T) {
	workspace, project := testProject(t, AppStoreIDs{AppID: "1001"})
	if err := os.Mkdir(filepath.Join(project, "dist"), 0o755); err != nil {
		t.Fatal(err)
	}
	ipa := filepath.Join(project, "dist", "Demo.ipa")
	file, err := os.Create(ipa)
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Truncate(maxFingerprintFileBytes + 1); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	plan, err := (Planner{Workspace: workspace, Tools: availableTools(t)}).Plan(context.Background(), PlanInput{Action: "install", Project: project, IPA: ipa, Device: "fixture-device"})
	if err != nil {
		t.Fatalf("selected project-local IPA was constrained as source: %v", err)
	}
	if !plan.Executable {
		t.Fatalf("selected IPA plan was unexpectedly blocked: %#v", plan.Blockers)
	}
}

func TestNoFollowTraversalKeepsOpenedDirectoryAfterPathReplacement(t *testing.T) {
	root := t.TempDir()
	inside := filepath.Join(root, "inside")
	if err := os.Mkdir(inside, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"a-trigger", "b-source"} {
		if err := os.WriteFile(filepath.Join(inside, name), []byte(name), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "outside-secret"), []byte("must-not-visit"), 0o600); err != nil {
		t.Fatal(err)
	}
	visited := []string{}
	err := walkRegularFilesWithin(context.Background(), root, map[string]bool{}, func(relative string, _ *os.File) error {
		visited = append(visited, filepath.ToSlash(relative))
		if relative == filepath.Join("inside", "a-trigger") {
			if err := os.Rename(inside, filepath.Join(root, "opened-directory")); err != nil {
				return err
			}
			return os.Symlink(outside, inside)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(visited, " ")
	if !strings.Contains(joined, "inside/b-source") || strings.Contains(joined, "outside-secret") {
		t.Fatalf("descriptor traversal escaped replaced directory: %v", visited)
	}
}

func TestFingerprintTraversalRejectsReplacedProjectAncestorAfterResolution(t *testing.T) {
	workspace := t.TempDir()
	project, err := CreateProject(workspace, filepath.Join("nested", "Demo"), "Demo", "com.example.Demo")
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := ResolveWithin(workspace, project.Path, true)
	if err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	outsideProject := filepath.Join(outside, "Demo")
	if err := os.Mkdir(outsideProject, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outsideProject, "outside-secret"), []byte("must-not-visit"), 0o600); err != nil {
		t.Fatal(err)
	}
	ancestor := filepath.Join(workspace, "nested")
	if err := os.Rename(ancestor, filepath.Join(workspace, "opened-nested")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, ancestor); err != nil {
		t.Fatal(err)
	}

	visited := []string{}
	err = walkRegularFilesInProject(context.Background(), workspace, resolved, map[string]bool{}, func(relative string, _ *os.File) error {
		visited = append(visited, relative)
		return nil
	})
	if err == nil {
		t.Fatalf("replaced ancestor was accepted; visited %v", visited)
	}
	if strings.Contains(strings.Join(visited, " "), "outside-secret") {
		t.Fatalf("fingerprint traversal escaped through replaced ancestor: %v", visited)
	}
}

func TestPlanRejectsProjectSymlink(t *testing.T) {
	workspace, project := testProject(t, AppStoreIDs{})
	if err := os.Symlink(filepath.Join(project, "Package.swift"), filepath.Join(project, "linked.swift")); err != nil {
		t.Fatal(err)
	}
	planner := Planner{Workspace: workspace, Tools: availableTools(t)}
	if _, err := planner.Plan(context.Background(), PlanInput{Action: "build", Project: project}); err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("expected project symlink rejection, got %v", err)
	}
}

func TestPlanFingerprintHonorsCanceledContext(t *testing.T) {
	workspace, project := testProject(t, AppStoreIDs{})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := (Planner{Workspace: workspace, Tools: availableTools(t)}).Plan(ctx, PlanInput{Action: "build", Project: project}); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled fingerprint returned %v", err)
	}
}
