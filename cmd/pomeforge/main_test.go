package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"pomeforge.local/pomeforge/internal/pomeforge"
)

type cliTools map[string]pomeforge.ToolStatus

func (f cliTools) Probe(_ context.Context, id string) pomeforge.ToolStatus {
	if tool, ok := f[id]; ok {
		return tool
	}
	return pomeforge.ToolStatus{ID: id, Name: id, Status: "missing", Detail: "missing"}
}

func (f cliTools) ProbeAll(ctx context.Context) []pomeforge.ToolStatus {
	result := make([]pomeforge.ToolStatus, 0, len(f))
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
	for _, expected := range []string{`"name":"run"`, `"name":"signing"`, `"id":"release-build"`, `"id":"ipa-inspect"`, `"id":"submit"`, `"requiresConfirmation":true`, `"effect":"account-write"`, `"--identity"`, `"--output-ipa"`, `"--execute"`} {
		if !strings.Contains(stdout, expected) {
			t.Errorf("schema missing %s", expected)
		}
	}
}

func TestCLIOperationResourceIDSchemaAndConfirmationHelp(t *testing.T) {
	code, stdout, stderr := runCLI(t, "schema", "--json")
	if code != 0 || stderr != "" {
		t.Fatalf("schema: code=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	var envelope struct {
		Data struct {
			Commands []schemaCommand `json:"commands"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(stdout), &envelope); err != nil {
		t.Fatalf("decode schema: %v", err)
	}
	for _, commandName := range []string{"plan", "run"} {
		var parameters []schemaParameter
		for _, command := range envelope.Data.Commands {
			if command.Name == commandName {
				parameters = command.Parameters
				break
			}
		}
		for _, parameterName := range []string{"--version-id", "--build-id"} {
			var found *schemaParameter
			for index := range parameters {
				if parameters[index].Name == parameterName {
					found = &parameters[index]
					break
				}
			}
			if found == nil || found.Type != "string" || !strings.Contains(found.Description, "resource ID") {
				t.Errorf("%s schema parameter %s = %#v; want string resource ID", commandName, parameterName, found)
			}
		}
	}

	_, generalHelp, _ := runCLI(t, "--help")
	if strings.Contains(generalHelp, "signing/account effects") || !strings.Contains(generalHelp, "account writes and development installation need --confirm") {
		t.Fatalf("general run help has incorrect confirmation contract: %q", generalHelp)
	}
	_, runHelp, _ := runCLI(t, "run", "--help")
	if !strings.Contains(runHelp, "Local export requires only --execute") || !strings.Contains(runHelp, "account writes and development installation also require --confirm") {
		t.Fatalf("run help has incorrect confirmation contract: %q", runHelp)
	}
}

func TestCLISigningErrorDoesNotExposePrivatePaths(t *testing.T) {
	private := filepath.Join(t.TempDir(), "sensitive-private-key.pem")
	certificate := filepath.Join(t.TempDir(), "sensitive-certificate.pem")
	profile := filepath.Join(t.TempDir(), "sensitive-profile.mobileprovision")
	code, stdout, stderr := runCLI(t, "signing", "configure", "release-main", "--label", "Release main", "--private-key", private, "--certificate", certificate, "--profile", profile, "--json")
	if code == 0 || stderr != "" || !strings.Contains(stdout, `"code":"invalid_identity"`) {
		t.Fatalf("code=%d stdout=%s stderr=%s", code, stdout, stderr)
	}
	for _, path := range []string{private, certificate, profile} {
		if strings.Contains(stdout, path) {
			t.Fatalf("CLI error exposed private path %q: %s", path, stdout)
		}
	}
	if !strings.Contains(stdout, "[private-path]") {
		t.Fatalf("CLI error omitted redaction marker: %s", stdout)
	}
}

func TestCLIInitValidationAndNonOverwrite(t *testing.T) {
	destination := filepath.Join(t.TempDir(), "Demo")
	code, stdout, stderr := runCLI(t, "init", "Demo", "--dir", destination, "--bundle-id", "com.example.Demo", "--json")
	if code != 0 || stderr != "" {
		t.Fatalf("init code=%d stdout=%s stderr=%s", code, stdout, stderr)
	}
	if _, _, err := pomeforge.LoadManifest(destination); err != nil {
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
	project, err := pomeforge.CreateProject(workspace, "Demo", "Demo", "com.example.Demo")
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

func TestHumanHelpAndCommandHelp(t *testing.T) {
	for _, args := range [][]string{{"--help"}, {"init", "--help"}, {"plan", "--help"}, {"run", "build", "--help"}, {"app", "-h"}} {
		code, stdout, stderr := runCLI(t, args...)
		if code != 0 || stderr != "" || !strings.Contains(stdout, "Usage:") {
			t.Fatalf("%v: code=%d stdout=%q stderr=%q", args, code, stdout, stderr)
		}
	}
	_, planHelp, _ := runCLI(t, "plan", "--help")
	if !strings.Contains(planHelp, "upload, submit") {
		t.Fatalf("plan help omitted action list: %q", planHelp)
	}
	code, stdout, _ := runCLI(t, "doctor", "--unknown", "--help", "--json")
	if code != 2 || !strings.Contains(stdout, `"code":"unknown_flag"`) {
		t.Fatalf("unknown flag was hidden by help: code=%d output=%s", code, stdout)
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
	project, err := pomeforge.CreateProject(workspace, "Demo", "Demo", "com.example.Demo")
	if err != nil {
		t.Fatal(err)
	}
	previous := defaultTools
	executable := filepath.Join(workspace, "failure-fixture")
	if err := os.WriteFile(executable, []byte("#!/bin/sh\nprintf 'token=syntheticsecrettoken'\nexit 7\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	defaultTools = cliTools{
		"swift": {ID: "swift", Name: "swift", Status: "available", Path: executable, Detail: "fixture"},
		"xtool": {ID: "xtool", Name: "xtool", Status: "available", Path: executable, Detail: "fixture"},
	}
	t.Cleanup(func() { defaultTools = previous })
	code, stdout, stderr := runCLI(t, "run", "build", "--project", project.Path, "--execute", "--json")
	if code != 7 || stderr != "" || !strings.Contains(stdout, `"status":"failed"`) || !strings.Contains(stdout, `"exitCode":7`) || strings.Contains(stdout, "syntheticsecrettoken") || !strings.Contains(stdout, "[redacted]") {
		t.Fatalf("child failure: code=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
}

func TestCLIHistoryFailureCarriesResultInJSONAndHumanOutput(t *testing.T) {
	previous := defaultTools
	t.Cleanup(func() { defaultTools = previous })
	for _, jsonMode := range []bool{true, false} {
		workspace := t.TempDir()
		project, err := pomeforge.CreateProject(workspace, "Demo", "Demo", "com.example.Demo")
		if err != nil {
			t.Fatal(err)
		}
		executable := filepath.Join(workspace, "history-fixture")
		script := "#!/bin/sh\nif [ -f .pomeforge/history.jsonl ]; then rm .pomeforge/history.jsonl && mkdir .pomeforge/history.jsonl; fi\nprintf 'completed output token=synthetichistorysecret'\n"
		if err := os.WriteFile(executable, []byte(script), 0o700); err != nil {
			t.Fatal(err)
		}
		defaultTools = cliTools{
			"swift": {ID: "swift", Name: "swift", Status: "available", Path: executable, Detail: "fixture"},
			"xtool": {ID: "xtool", Name: "xtool", Status: "available", Path: executable, Detail: "fixture"},
		}
		args := []string{"run", "build", "--project", project.Path, "--execute"}
		if jsonMode {
			args = append(args, "--json")
		}
		code, stdout, stderr := runCLI(t, args...)
		if code != 1 || !strings.Contains(stdout, "completed output") || strings.Contains(stdout, "synthetichistorysecret") || !strings.Contains(stdout, "[redacted]") {
			t.Fatalf("json=%t code=%d stdout=%q stderr=%q", jsonMode, code, stdout, stderr)
		}
		if jsonMode {
			if stderr != "" || !strings.Contains(stdout, `"code":"history_failed"`) || !strings.Contains(stdout, `"result":{"id"`) {
				t.Fatalf("JSON history failure lost structured result: stdout=%q stderr=%q", stdout, stderr)
			}
		} else if !strings.Contains(stdout, "build: succeeded") || !strings.Contains(stderr, "history receipt") {
			t.Fatalf("human history failure lost result or error: stdout=%q stderr=%q", stdout, stderr)
		}
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

func TestCLIToolInstallPreviewAndSetupSchema(t *testing.T) {
	code, stdout, stderr := runCLI(t, "tools", "install", "asc", "--json")
	if code != 0 || stderr != "" {
		t.Fatalf("code=%d stdout=%s stderr=%s", code, stdout, stderr)
	}
	for _, expected := range []string{`"scope":"workspace"`, `"version":"5.0.0"`, `"downloadSize":`, `"sourceUrl":"https://github.com/`, `"operation":"verified-managed-install"`} {
		if !strings.Contains(stdout, expected) {
			t.Errorf("install preview missing %s: %s", expected, stdout)
		}
	}
	code, stdout, _ = runCLI(t, "schema", "--json")
	if code != 0 {
		t.Fatal(stdout)
	}
	for _, expected := range []string{`"name":"sdk"`, `"inputPath"`, `"assetKitRevision"`, `"scope":"workspace"`} {
		if !strings.Contains(stdout, expected) {
			t.Errorf("schema missing %s", expected)
		}
	}
}

func TestCLIHelperRegistrationPreviewRetainsOnlySuppliedProvenance(t *testing.T) {
	previous := defaultTools
	t.Cleanup(func() { defaultTools = previous })
	helper := filepath.Join(t.TempDir(), "pomeforge-assets")
	if err := os.WriteFile(helper, []byte("#!/bin/sh\nif [ \"$1\" = --help ]; then echo 'Usage: pomeforge-assets compile'; exit 0; fi\nexit 2\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	code, stdout, stderr := runCLI(t, "tools", "register", "pomeforge-assets", "--path", helper, "--json")
	if code != 0 || stderr != "" {
		t.Fatalf("code=%d stdout=%s stderr=%s", code, stdout, stderr)
	}
	if !strings.Contains(stdout, `"sha256"`) || strings.Contains(stdout, `"assetKitRevision":"e763`) || !strings.Contains(stdout, "will not infer provenance") {
		t.Fatalf("unexpected registration preview: %s", stdout)
	}
}

func TestCLIErrorEnvelopeRetainsCompletedOperationResult(t *testing.T) {
	var stdout, stderr bytes.Buffer
	command := &cli{json: true, stdout: &stdout, stderr: &stderr}
	result := pomeforge.OperationResult{ID: "failed-result", Action: "sdk-import", Scope: "workspace", Status: "failed", ExitCode: 9, Output: "safe output"}
	code := command.fail(pomeforge.ErrorWithResult("operation_failed", "operation failed", result), 3)
	if code != 3 || stderr.Len() != 0 || !strings.Contains(stdout.String(), `"result":{"id":"failed-result"`) || !strings.Contains(stdout.String(), `"scope":"workspace"`) {
		t.Fatalf("code=%d stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
}
