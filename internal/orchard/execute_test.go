package orchard

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestProcessHelper(t *testing.T) {
	separator := -1
	for index, argument := range os.Args {
		if argument == "--" {
			separator = index
			break
		}
	}
	if separator < 0 || separator+1 >= len(os.Args) {
		return
	}
	switch os.Args[separator+1] {
	case "fail":
		fmt.Fprint(os.Stdout, "fixture failure")
		os.Exit(7)
	case "large":
		fmt.Fprint(os.Stdout, strings.Repeat("x", 4096))
	case "secret":
		fmt.Fprint(os.Stdout, "Authorization: Bearer abc.def.secret token=supersecrettoken")
	case "long-secret":
		fmt.Fprint(os.Stdout, strings.Repeat("safe", 40)+"-----BEGIN PRIVATE KEY-----\n"+strings.Repeat("synthetic-private-material", 200)+"\n-----END PRIVATE KEY-----")
	case "break-history":
		history := filepath.Join(os.Args[separator+2], ".orchard", "history.jsonl")
		if err := os.Remove(history); err != nil {
			os.Exit(10)
		}
		if err := os.Mkdir(history, 0o700); err != nil {
			os.Exit(11)
		}
		fmt.Fprint(os.Stdout, "operation output survived")
	case "redirect-project-ancestor":
		if err := os.Rename(os.Args[separator+2], os.Args[separator+3]); err != nil {
			os.Exit(12)
		}
		if err := os.Symlink(os.Args[separator+4], os.Args[separator+2]); err != nil {
			os.Exit(13)
		}
		fmt.Fprint(os.Stdout, "operation output survived ancestor replacement")
	case "sleep":
		time.Sleep(250 * time.Millisecond)
		fmt.Fprint(os.Stdout, "done")
	case "descendant-parent":
		child := exec.Command(os.Args[0], "-test.run=TestProcessHelper", "--", "descendant-child", os.Args[separator+2])
		child.Stdout = os.Stdout
		child.Stderr = os.Stderr
		if err := child.Start(); err != nil {
			os.Exit(9)
		}
		time.Sleep(10 * time.Second)
	case "descendant-child":
		time.Sleep(800 * time.Millisecond)
		_ = os.WriteFile(os.Args[separator+2], []byte(strconv.Itoa(os.Getpid())), 0o600)
	}
}

func helperPlan(project, mode string) Plan {
	return Plan{
		ID: "fixture", Action: "build", Title: "fixture", Executable: true,
		Steps:    []Step{{Tool: "fixture", Executable: os.Args[0], Args: []string{"-test.run=TestProcessHelper", "--", mode}, Directory: project, Description: "run fixture"}},
		Blockers: []string{}, Warnings: []string{},
	}
}

func TestExecutorFailureOutputBoundsRedactionAndHistory(t *testing.T) {
	project := t.TempDir()
	executor := &Executor{OutputCap: 256, Timeout: time.Second}
	failed, err := executor.Execute(context.Background(), helperPlan(project, "fail"), project, false)
	if err != nil {
		t.Fatal(err)
	}
	if failed.Status != "failed" || failed.ExitCode != 7 || !strings.Contains(failed.Output, "fixture failure") {
		t.Fatalf("unexpected failed result: %#v", failed)
	}
	large, err := executor.Execute(context.Background(), helperPlan(project, "large"), project, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(large.Output) > 320 || !strings.Contains(large.Output, "truncated") {
		t.Fatalf("output was not bounded: length=%d output=%q", len(large.Output), large.Output)
	}
	secret, err := executor.Execute(context.Background(), helperPlan(project, "secret"), project, false)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(secret.Output, "abc.def.secret") || strings.Contains(secret.Output, "supersecrettoken") {
		t.Fatalf("secret retained: %q", secret.Output)
	}
	history, err := LoadHistory(project, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(history) != 3 {
		t.Fatalf("history length = %d", len(history))
	}
	info, err := os.Stat(filepath.Join(project, ".orchard", "history.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("history permissions = %o", info.Mode().Perm())
	}
}

func TestExecutorTimeoutConfirmationAndDuplicate(t *testing.T) {
	project := t.TempDir()
	confirmedPlan := helperPlan(project, "sleep")
	confirmedPlan.RequiresConfirmation = true
	executor := &Executor{Timeout: 20 * time.Millisecond}
	if _, err := executor.Execute(context.Background(), confirmedPlan, project, false); err == nil || !strings.Contains(err.Error(), "confirm") {
		t.Fatalf("expected confirmation error, got %v", err)
	}
	timed, err := executor.Execute(context.Background(), confirmedPlan, project, true)
	if err != nil {
		t.Fatal(err)
	}
	if timed.ExitCode != 124 || timed.Status != "failed" {
		t.Fatalf("unexpected timeout: %#v", timed)
	}

	executor = &Executor{Timeout: time.Second}
	started := make(chan struct{})
	done := make(chan struct{})
	go func() {
		close(started)
		_, _ = executor.Execute(context.Background(), helperPlan(project, "sleep"), project, false)
		close(done)
	}()
	<-started
	deadline := time.Now().Add(200 * time.Millisecond)
	for {
		executor.mu.Lock()
		running := len(executor.running) > 0
		executor.mu.Unlock()
		if running {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("first operation did not start")
		}
		time.Sleep(time.Millisecond)
	}
	if _, err := executor.Execute(context.Background(), helperPlan(project, "sleep"), project, false); err == nil || !strings.Contains(err.Error(), "already running") {
		t.Fatalf("expected duplicate rejection, got %v", err)
	}
	<-done
}

func TestExecutorEnforcesLinuxForBuildActions(t *testing.T) {
	project := t.TempDir()
	if _, err := (&Executor{HostOS: "darwin"}).Execute(context.Background(), helperPlan(project, "large"), project, false); err == nil || !strings.Contains(err.Error(), "only when Orchard is running on Linux") {
		t.Fatalf("non-Linux executor accepted build plan: %v", err)
	}
}

func TestChildEnvironmentIsNarrow(t *testing.T) {
	t.Setenv("ORCHARD_TEST_SECRET", "must-not-pass")
	t.Setenv("ASC_CONFIG_PATH", "/private/config")
	t.Setenv("SWIFT_DRIVER_SWIFTSCAN_LIB", "/swift/lib")
	buildEnvironment := strings.Join(ChildEnvironmentFor("xtool"), "\n")
	if strings.Contains(buildEnvironment, "ORCHARD_TEST_SECRET") || strings.Contains(buildEnvironment, "ASC_CONFIG_PATH") {
		t.Fatal("unrelated environment leaked")
	}
	if !strings.Contains(buildEnvironment, "SWIFT_DRIVER_SWIFTSCAN_LIB=/swift/lib") {
		t.Fatal("Swift environment was not preserved for build tools")
	}
	ascEnvironment := strings.Join(ChildEnvironmentFor(EnvironmentASC), "\n")
	if !strings.Contains(ascEnvironment, "ASC_CONFIG_PATH=/private/config") || !strings.Contains(ascEnvironment, "ASC_TELEMETRY_DISABLED=1") {
		t.Fatal("required ASC environment not preserved")
	}
	probeEnvironment := strings.Join(ChildEnvironmentFor(EnvironmentProbe), "\n")
	if strings.Contains(probeEnvironment, "ASC_CONFIG_PATH") {
		t.Fatal("credential-free metadata probe inherited ASC configuration")
	}
}

func TestExecutorKillsProcessGroupAtDeadline(t *testing.T) {
	project := t.TempDir()
	marker := filepath.Join(project, "descendant-finished")
	plan := helperPlan(project, "descendant-parent")
	plan.Steps[0].Args = append(plan.Steps[0].Args, marker)
	started := time.Now()
	result, err := (&Executor{Timeout: 80 * time.Millisecond}).Execute(context.Background(), plan, project, false)
	if err != nil {
		t.Fatal(err)
	}
	if result.ExitCode != 124 || result.Status != "failed" {
		t.Fatalf("timeout result = %#v", result)
	}
	if time.Since(started) > time.Second {
		t.Fatalf("process group timeout was not bounded: %s", time.Since(started))
	}
	time.Sleep(850 * time.Millisecond)
	if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("descendant survived process-group kill: %v", err)
	}
}

func TestOutputRedactsPEMJWTEnvironmentValueAndPresignedURL(t *testing.T) {
	credential := "synthetic-environment-canary"
	t.Setenv("ASC_PRIVATE_KEY", credential)
	input := "-----BEGIN PRIVATE KEY-----\nsynthetic-body\n-----END PRIVATE KEY-----\n" +
		"eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiJzeW50aGV0aWMifQ.c3ludGhldGljLXNpZ25hdHVyZQ " + credential +
		" https://upload.example.invalid/object?X-Amz-Signature=fakecapability&X-Amz-Expires=60"
	redacted := redactOutput(input)
	for _, forbidden := range []string{"synthetic-body", "eyJhbGci", credential, "fakecapability"} {
		if strings.Contains(redacted, forbidden) {
			t.Fatalf("redaction retained %q in %q", forbidden, redacted)
		}
	}
}

func TestExecutorRedactsBeforeOutputTruncation(t *testing.T) {
	project := t.TempDir()
	result, err := (&Executor{OutputCap: 256, Timeout: time.Second}).Execute(context.Background(), helperPlan(project, "long-secret"), project, false)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"BEGIN PRIVATE KEY", "synthetic-private-material"} {
		if strings.Contains(result.Output, forbidden) {
			t.Fatalf("truncated operation output retained %q: %q", forbidden, result.Output)
		}
	}
	if !strings.Contains(result.Output, "[redacted]") {
		t.Fatalf("missing redaction marker: %q", result.Output)
	}

	credential := strings.Repeat("credential", 40)
	t.Setenv("ASC_LONG_CANARY", credential)
	var output cappedBuffer
	output.limit = 64
	_, _ = output.Write([]byte(strings.Repeat("x", 60) + credential + " trailing"))
	retained := output.String()
	if strings.Contains(retained, credential[:8]) || !strings.Contains(retained, "[redacted]") {
		t.Fatalf("credential crossing output cap was retained: %q", retained)
	}
	for name, value := range map[string]string{
		"jwt":           strings.Repeat("y", 20) + "eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiJzeW50aGV0aWMifQ.c3ludGhldGljLXNpZ25hdHVyZQ",
		"presigned URL": strings.Repeat("z", 20) + "https://upload.example.invalid/object?X-Amz-Signature=fakecapability",
	} {
		var bounded cappedBuffer
		bounded.limit = 64
		_, _ = bounded.Write([]byte(value))
		retained = bounded.String()
		if strings.Contains(retained, "eyJhbGci") || strings.Contains(retained, "fakecapability") || !strings.Contains(retained, "[redacted]") {
			t.Fatalf("%s crossing output cap was retained: %q", name, retained)
		}
	}
}

func TestExecutorReturnsResultWithPostOperationHistoryFailure(t *testing.T) {
	project := t.TempDir()
	plan := helperPlan(project, "break-history")
	plan.Steps[0].Args = append(plan.Steps[0].Args, project)
	result, err := (&Executor{Timeout: 5 * time.Second}).Execute(context.Background(), plan, project, false)
	if err == nil {
		t.Fatal("expected history persistence failure")
	}
	var coded *CodedError
	if !errors.As(err, &coded) || coded.Code != "history_failed" || coded.Result == nil {
		t.Fatalf("history error did not carry result: %#v", err)
	}
	if result.ID == "" || coded.Result.ID != result.ID || coded.Result.Status != "succeeded" || !strings.Contains(coded.Result.Output, "operation output survived") {
		t.Fatalf("completed result was not preserved: result=%#v error=%#v", result, coded)
	}
}

func TestHistoryAppendRejectsProjectAncestorReplacedByCompletedOperation(t *testing.T) {
	workspace := t.TempDir()
	project, err := CreateProject(workspace, filepath.Join("nested", "Demo"), "Demo", "com.example.Demo")
	if err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	outsideProject := filepath.Join(outside, "Demo")
	outsideHistoryDirectory := filepath.Join(outsideProject, ".orchard")
	if err := os.MkdirAll(outsideHistoryDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	outsideHistory := filepath.Join(outsideHistoryDirectory, "history.jsonl")
	if err := os.WriteFile(outsideHistory, []byte("preserve\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	ancestor := filepath.Join(workspace, "nested")
	parked := filepath.Join(workspace, "opened-nested")
	plan := helperPlan(project.Path, "redirect-project-ancestor")
	plan.Steps[0].Args = append(plan.Steps[0].Args, ancestor, parked, outside)

	result, err := (&Executor{Workspace: workspace, Timeout: 5 * time.Second}).Execute(context.Background(), plan, project.Path, false)
	if err == nil {
		t.Fatal("expected history persistence failure after ancestor replacement")
	}
	var coded *CodedError
	if !errors.As(err, &coded) || coded.Code != "history_failed" || coded.Result == nil {
		t.Fatalf("history error did not preserve the operation result: %#v", err)
	}
	if result.Status != "succeeded" || !strings.Contains(result.Output, "operation output survived") {
		t.Fatalf("completed operation result was discarded: %#v", result)
	}
	contents, readErr := os.ReadFile(outsideHistory)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(contents) != "preserve\n" {
		t.Fatalf("outside history was modified: %q", contents)
	}
}

func TestHistoryRejectsSymlink(t *testing.T) {
	project := t.TempDir()
	if err := os.Mkdir(filepath.Join(project, ".orchard"), 0o700); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "outside")
	if err := os.WriteFile(outside, []byte("preserve"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(project, ".orchard", "history.jsonl")); err != nil {
		t.Fatal(err)
	}
	if _, err := (&Executor{Timeout: time.Second}).Execute(context.Background(), helperPlan(project, "secret"), project, false); err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("expected history symlink rejection, got %v", err)
	}
	contents, _ := os.ReadFile(outside)
	if string(contents) != "preserve" {
		t.Fatal("symlink target was modified")
	}
}

func TestHistoryRejectsSpecialAndOversizedFiles(t *testing.T) {
	project := t.TempDir()
	directory := filepath.Join(project, ".orchard")
	if err := os.Mkdir(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(directory, "history.jsonl")
	if err := syscall.Mkfifo(path, 0o600); err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	if _, err := LoadHistory(project, 10); err == nil || !strings.Contains(err.Error(), "regular") {
		t.Fatalf("FIFO history was not rejected: %v", err)
	}
	if time.Since(started) > time.Second {
		t.Fatal("FIFO history read blocked")
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Truncate(maxHistoryBytes + 1); err != nil {
		t.Fatal(err)
	}
	_ = file.Close()
	if _, err := LoadHistory(project, 10); err == nil || !strings.Contains(err.Error(), "read limit") {
		t.Fatalf("oversized history was not rejected: %v", err)
	}
}

func TestHistoryStreamingLimitRejectsGrowthBeyondInitialSize(t *testing.T) {
	line := strings.Repeat("x", 64*1024) + "\n"
	reader := strings.NewReader(strings.Repeat(line, maxHistoryBytes/len(line)+2))
	if _, err := scanHistory(reader, 10); err == nil || !strings.Contains(err.Error(), "read limit") {
		t.Fatalf("streaming history limit was not enforced: %v", err)
	}
}

func TestLoadHistoryRedactsLegacyOutput(t *testing.T) {
	project := t.TempDir()
	directory := filepath.Join(project, ".orchard")
	if err := os.Mkdir(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	credential := "synthetic-legacy-environment-canary"
	t.Setenv("ASC_PRIVATE_KEY", credential)
	legacy := OperationResult{
		ID: "legacy", Action: "upload", Status: "failed", ExitCode: 7,
		Output: "-----BEGIN PRIVATE KEY-----\nsynthetic-legacy-pem\n-----END PRIVATE KEY-----\n" +
			"eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiJsZWdhY3kifQ.c3ludGhldGljLXNpZ25hdHVyZQ " + credential +
			" https://upload.example.invalid/object?X-Amz-Signature=synthetic-legacy-capability&X-Amz-Expires=60",
	}
	encoded, err := json.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "history.jsonl"), append(encoded, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}

	history, err := LoadHistory(project, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(history) != 1 {
		t.Fatalf("history length = %d", len(history))
	}
	for _, forbidden := range []string{"synthetic-legacy-pem", "eyJhbGci", credential, "synthetic-legacy-capability"} {
		if strings.Contains(history[0].Output, forbidden) {
			t.Fatalf("legacy output retained %q: %q", forbidden, history[0].Output)
		}
	}
	if !strings.Contains(history[0].Output, "[redacted]") {
		t.Fatalf("legacy output omitted redaction marker: %q", history[0].Output)
	}
}
