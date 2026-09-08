package orchard

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
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

func availableTools() fakeTools {
	result := fakeTools{}
	for _, id := range []string{"xtool", "swift", "asc", "zsign", "idevice_id", "usbmuxd"} {
		result[id] = ToolStatus{ID: id, Name: id, Status: "available", Version: "test 1.0", Path: "/test/bin/" + id, Detail: "test", InstallURL: "https://example.invalid"}
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
		if err := os.WriteFile(filepath.Join(project.Path, "orchard.json"), encoded, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return workspace, project.Path
}

func jsonMarshalIndent(value any) ([]byte, error) {
	data, err := json.MarshalIndent(value, "", "  ")
	return append(data, '\n'), err
}

func TestVerifiedPlanCommands(t *testing.T) {
	workspace, project := testProject(t, AppStoreIDs{AppID: "1001", VersionID: "2002", BuildID: "3003"})
	ipa := filepath.Join(workspace, "Demo.ipa")
	if err := os.WriteFile(ipa, []byte("fixture"), 0o600); err != nil {
		t.Fatal(err)
	}
	planner := Planner{Workspace: workspace, Tools: availableTools()}
	tests := []struct {
		action  string
		input   PlanInput
		want    [][]string
		confirm bool
	}{
		{"build", PlanInput{Action: "build", Project: project}, [][]string{{"--version"}, {"dev", "build", "--configuration", "debug"}}, false},
		{"devices", PlanInput{Action: "devices", Project: project}, [][]string{{"devices", "--no-wait"}}, false},
		{"install", PlanInput{Action: "install", Project: project, IPA: ipa, Device: "abc-123"}, [][]string{{"install", "--udid", "abc-123", ipa}}, false},
		{"launch", PlanInput{Action: "launch", Project: project, Device: "abc-123"}, [][]string{{"launch", "--udid", "abc-123", "com.example.Demo"}}, false},
		{"export", PlanInput{Action: "export", Project: project}, [][]string{{"dev", "build", "--configuration", "release", "--ipa"}}, false},
		{"store-status", PlanInput{Action: "store-status", Project: project}, [][]string{{"builds", "info", "--build-id", "3003", "--output", "json"}}, false},
		{"validate", PlanInput{Action: "validate", Project: project}, [][]string{{"validate", "--app", "1001", "--version-id", "2002", "--platform", "IOS", "--output", "json"}}, false},
		{"upload", PlanInput{Action: "upload", Project: project, IPA: ipa}, [][]string{{"builds", "upload", "--app", "1001", "--ipa", ipa, "--wait", "--output", "json"}}, true},
		{"submit", PlanInput{Action: "submit", Project: project}, [][]string{{"review", "submit", "--app", "1001", "--version-id", "2002", "--build-id", "3003", "--platform", "IOS", "--dry-run", "--output", "json"}, {"review", "submit", "--app", "1001", "--version-id", "2002", "--build-id", "3003", "--platform", "IOS", "--confirm", "--output", "json"}}, true},
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
			if plan.RequiresConfirmation != test.confirm {
				t.Errorf("confirmation = %t", plan.RequiresConfirmation)
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
	service, err = NewService(workspace, availableTools())
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
	planner := Planner{Workspace: workspace, Tools: availableTools()}
	first, err := planner.Plan(context.Background(), PlanInput{Action: "upload", Project: project, IPA: ipa})
	if err != nil {
		t.Fatal(err)
	}
	repeated, err := planner.Plan(context.Background(), PlanInput{Action: "upload", Project: project, IPA: ipa})
	if err != nil {
		t.Fatal(err)
	}
	if first.ID != repeated.ID {
		t.Fatalf("unchanged plan was not deterministic: %s != %s", first.ID, repeated.ID)
	}
	if err := os.WriteFile(ipa, []byte("two"), 0o600); err != nil {
		t.Fatal(err)
	}
	second, err := planner.Plan(context.Background(), PlanInput{Action: "upload", Project: project, IPA: ipa})
	if err != nil {
		t.Fatal(err)
	}
	if first.ID == second.ID {
		t.Fatal("IPA mutation did not change plan ID")
	}
}

func TestPlanRejectsProjectSymlink(t *testing.T) {
	workspace, project := testProject(t, AppStoreIDs{})
	if err := os.Symlink(filepath.Join(project, "Package.swift"), filepath.Join(project, "linked.swift")); err != nil {
		t.Fatal(err)
	}
	planner := Planner{Workspace: workspace, Tools: availableTools()}
	if _, err := planner.Plan(context.Background(), PlanInput{Action: "build", Project: project}); err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("expected project symlink rejection, got %v", err)
	}
}
