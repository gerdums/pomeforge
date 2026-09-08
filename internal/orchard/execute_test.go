package orchard

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
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
	case "sleep":
		time.Sleep(250 * time.Millisecond)
		fmt.Fprint(os.Stdout, "done")
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

func TestChildEnvironmentIsNarrow(t *testing.T) {
	t.Setenv("ORCHARD_TEST_SECRET", "must-not-pass")
	t.Setenv("ASC_CONFIG_PATH", "/private/config")
	environment := strings.Join(ChildEnvironment(), "\n")
	if strings.Contains(environment, "ORCHARD_TEST_SECRET") {
		t.Fatal("unrelated environment leaked")
	}
	if !strings.Contains(environment, "ASC_CONFIG_PATH=/private/config") || !strings.Contains(environment, "ASC_TELEMETRY_DISABLED=1") {
		t.Fatal("required ASC environment not preserved")
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
