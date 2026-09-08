package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"orchard.local/orchard/internal/orchard"
)

type cliTools map[string]orchard.ToolStatus

func (f cliTools) Probe(_ context.Context, id string) orchard.ToolStatus {
	if tool, ok := f[id]; ok {
		return tool
	}
	return orchard.ToolStatus{ID: id, Name: id, Status: "missing", Detail: "missing"}
}

func (f cliTools) ProbeAll(ctx context.Context) []orchard.ToolStatus {
	result := make([]orchard.ToolStatus, 0, len(f))
	for id := range f {
		result = append(result, f.Probe(ctx, id))
	}
	return result
}

func runCLI(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	code := run(args, &stdout, &stderr)
	return code, stdout.String(), stderr.String()
}

func TestJSONFlagBeforeAndAfterCommand(t *testing.T) {
	for _, args := range [][]string{{"--json", "version"}, {"version", "--json"}, {"schema", "--json"}} {
		code, stdout, stderr := runCLI(t, args...)
		if code != 0 || stderr != "" {
			t.Fatalf("%v: code=%d stderr=%q", args, code, stderr)
		}
		var envelope struct {
			OK   bool            `json:"ok"`
			Data json.RawMessage `json:"data"`
		}
		if err := json.Unmarshal([]byte(stdout), &envelope); err != nil || !envelope.OK {
			t.Fatalf("%v: invalid envelope %q: %v", args, stdout, err)
		}
	}
}

func TestSchemaDiscoversCommandsActionsParametersAndEffects(t *testing.T) {
	code, stdout, _ := runCLI(t, "schema", "--json")
	if code != 0 {
		t.Fatalf("schema failed: %s", stdout)
	}
	for _, expected := range []string{`"name":"run"`, `"id":"submit"`, `"requiresConfirmation":true`, `"effect":"account-write"`, `"--execute"`} {
		if !strings.Contains(stdout, expected) {
			t.Errorf("schema missing %s", expected)
		}
	}
}

func TestCLIInitValidationAndNonOverwrite(t *testing.T) {
	destination := filepath.Join(t.TempDir(), "Demo")
	code, stdout, stderr := runCLI(t, "init", "Demo", "--dir", destination, "--bundle-id", "com.example.Demo", "--json")
	if code != 0 || stderr != "" {
		t.Fatalf("init code=%d stdout=%s stderr=%s", code, stdout, stderr)
	}
	if _, _, err := orchard.LoadManifest(destination); err != nil {
		t.Fatal(err)
	}
	code, stdout, _ = runCLI(t, "init", "Demo", "--dir", destination, "--bundle-id", "com.example.Demo", "--json")
	if code == 0 || !strings.Contains(stdout, `"ok":false`) {
		t.Fatalf("overwrite was not a JSON failure: code=%d output=%s", code, stdout)
	}
	bad := filepath.Join(t.TempDir(), "Bad")
	code, stdout, _ = runCLI(t, "init", "../Bad", "--dir", bad, "--bundle-id", "bad", "--json")
	if code == 0 || !strings.Contains(stdout, `"code":"invalid_name"`) {
		t.Fatalf("invalid init: code=%d output=%s", code, stdout)
	}
}

func TestCLIUnknownFlagsAndExitCodes(t *testing.T) {
	code, stdout, stderr := runCLI(t, "doctor", "--does-not-exist", "--json")
	if code != 2 || stderr != "" || !strings.Contains(stdout, `"code":"unknown_flag"`) {
		t.Fatalf("unknown flag: code=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	workspace := t.TempDir()
	project, err := orchard.CreateProject(workspace, "Demo", "Demo", "com.example.Demo")
	if err != nil {
		t.Fatal(err)
	}
	code, stdout, _ = runCLI(t, "run", "build", "--project", project.Path, "--json")
	if code != 2 || !strings.Contains(stdout, `"code":"execute_required"`) {
		t.Fatalf("missing execute: code=%d output=%s", code, stdout)
	}
	t.Setenv("PATH", t.TempDir())
	code, stdout, _ = runCLI(t, "run", "build", "--project", project.Path, "--execute", "--json")
	if code != 3 || !strings.Contains(stdout, `"code":"blocked"`) {
		t.Fatalf("blocked run: code=%d output=%s", code, stdout)
	}
	code, stdout, _ = runCLI(t, "plan", "build", "--project", project.Path, "--json")
	if code != 0 || !strings.Contains(stdout, `"executable":false`) {
		t.Fatalf("blocked plan should remain inspectable: code=%d output=%s", code, stdout)
	}
}

func TestCLIRejectsForeignAppBind(t *testing.T) {
	workspace := t.TempDir()
	code, _, stderr := runCLI(t, "app", "--workspace", workspace, "--listen", "0.0.0.0:0")
	if code == 0 || !strings.Contains(stderr, "127.0.0.1") {
		t.Fatalf("foreign bind: code=%d stderr=%q", code, stderr)
	}
}

func TestCLIReturnsChildExitCode(t *testing.T) {
	workspace := t.TempDir()
	project, err := orchard.CreateProject(workspace, "Demo", "Demo", "com.example.Demo")
	if err != nil {
		t.Fatal(err)
	}
	previous := defaultTools
	defaultTools = cliTools{
		"swift": {ID: "swift", Name: "swift", Status: "available", Path: "/bin/false", Detail: "fixture"},
		"xtool": {ID: "xtool", Name: "xtool", Status: "available", Path: "/bin/false", Detail: "fixture"},
	}
	t.Cleanup(func() { defaultTools = previous })
	code, stdout, stderr := runCLI(t, "run", "build", "--project", project.Path, "--execute", "--json")
	if code != 1 || stderr != "" || !strings.Contains(stdout, `"status":"failed"`) || !strings.Contains(stdout, `"exitCode":1`) {
		t.Fatalf("child failure: code=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
}

func TestCreationContextRejectsSymlinkDestination(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, "linked")); err != nil {
		t.Fatal(err)
	}
	code, _, _ := runCLI(t, "init", "Demo", "--dir", filepath.Join(root, "linked", "Demo"), "--bundle-id", "com.example.Demo")
	if code == 0 {
		t.Fatal("symlink destination was accepted")
	}
}
