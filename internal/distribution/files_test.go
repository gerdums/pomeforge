//go:build linux

package distribution

import (
	"context"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestRegularReadsAndTreeWalkRejectFIFOsWithoutBlocking(t *testing.T) {
	dir := t.TempDir()
	fifo := filepath.Join(dir, "identity.key")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := readRegularFile(context.Background(), fifo, 1024)
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "not a regular file") {
			t.Fatalf("FIFO read error=%v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("FIFO read blocked")
	}

	app := filepath.Join(dir, "Unsafe.app")
	if err := syscall.Mkdir(app, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(filepath.Join(app, "resource"), 0o600); err != nil {
		t.Fatal(err)
	}
	done = make(chan error, 1)
	go func() {
		_, err := InspectBundle(context.Background(), app, InspectionOptions{})
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "nonregular") {
			t.Fatalf("FIFO tree error=%v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("bundle FIFO inspection blocked")
	}
}
