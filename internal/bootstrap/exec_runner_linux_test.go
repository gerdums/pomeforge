//go:build linux

package bootstrap

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestExecRunnerKillsDescendantHoldingOutputPipes(t *testing.T) {
	directory := t.TempDir()
	marker := filepath.Join(directory, "survived")
	environment := []string{"PATH=/usr/bin:/bin", "OUT=" + marker}

	diagnostic, err := (execRunner{}).Run(
		context.Background(),
		"/bin/sh",
		[]string{"-c", `printf 'parent diagnostic\n'; (sleep 3; printf survived > "$OUT") & exit 0`},
		directory,
		environment,
	)
	if !errors.Is(err, exec.ErrWaitDelay) {
		t.Fatalf("Run() error = %v, want exec.ErrWaitDelay", err)
	}
	if diagnostic != "parent diagnostic\n" {
		t.Fatalf("Run() diagnostic = %q", diagnostic)
	}

	assertMarkerNeverAppears(t, marker, 1500*time.Millisecond)
}

func TestExecRunnerKillsDescendantThatClosesOutputPipes(t *testing.T) {
	directory := t.TempDir()
	marker := filepath.Join(directory, "survived")
	environment := []string{"PATH=/usr/bin:/bin", "OUT=" + marker}

	_, err := (execRunner{}).Run(
		context.Background(),
		"/bin/sh",
		[]string{"-c", `(exec >/dev/null 2>&1; sleep 1; printf survived > "$OUT") & exit 0`},
		directory,
		environment,
	)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	assertMarkerNeverAppears(t, marker, 1500*time.Millisecond)
}

func assertMarkerNeverAppears(t *testing.T, marker string, duration time.Duration) {
	t.Helper()
	deadline := time.Now().Add(duration)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(marker); err == nil {
			t.Fatalf("descendant wrote marker after Run returned: %s", marker)
		} else if !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("stat marker: %v", err)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
