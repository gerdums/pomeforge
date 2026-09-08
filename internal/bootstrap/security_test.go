package bootstrap

import (
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestInstallManifestRejectsEveryTreeMutation(t *testing.T) {
	tests := []struct {
		name   string
		id     string
		format string
		body   []byte
		runner ProcessRunner
		mutate func(*testing.T, Plan)
	}{
		{
			name: "binary bytes", id: "asc", format: "binary", body: []byte("original asc"),
			mutate: func(t *testing.T, plan Plan) {
				writeExisting(t, filepath.Join(plan.versionRoot(), "asc"), []byte("replaced asc"), 0o700)
			},
		},
		{
			name: "extracted executable", id: "zsign", format: "tar.gz",
			body: makeTarGzip(t, []tarEntry{{name: "release/zsign", body: []byte("zsign"), mode: 0o755}, {name: "release/lib.dat", body: []byte("runtime")}}),
			mutate: func(t *testing.T, plan Plan) {
				writeExisting(t, filepath.Join(plan.versionRoot(), "package", "release", "zsign"), []byte("changed"), 0o700)
			},
		},
		{
			name: "runtime library", id: "zsign", format: "tar.gz",
			body: makeTarGzip(t, []tarEntry{{name: "release/zsign", body: []byte("zsign"), mode: 0o755}, {name: "release/lib.dat", body: []byte("runtime")}}),
			mutate: func(t *testing.T, plan Plan) {
				writeExisting(t, filepath.Join(plan.versionRoot(), "package", "release", "lib.dat"), []byte("changed"), 0o600)
			},
		},
		{
			name: "appimage symlink target", id: "xtool", format: "appimage", body: []byte("appimage"), runner: &fixtureRunner{},
			mutate: func(t *testing.T, plan Plan) {
				link := filepath.Join(plan.versionRoot(), "squashfs-root", "AppRun")
				if err := os.Remove(link); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink("usr/bin/other", link); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "extra file", id: "zsign", format: "tar.gz",
			body: makeTarGzip(t, []tarEntry{{name: "zsign", body: []byte("zsign"), mode: 0o755}}),
			mutate: func(t *testing.T, plan Plan) {
				writeExisting(t, filepath.Join(plan.versionRoot(), "extra"), []byte("extra"), 0o600)
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := serveTLS(t, test.body)
			paths := testPaths(t)
			plan := planFor(t, test.id, "1.0.0", test.format, test.body, server.URL, paths)
			installer := Installer{Client: server.Client(), Runner: test.runner}
			if _, err := installer.Install(context.Background(), plan); err != nil {
				t.Fatal(err)
			}
			test.mutate(t, plan)
			sentinel := filepath.Join(paths.ToolsDir, "unrelated")
			if err := os.WriteFile(sentinel, []byte("keep"), 0o640); err != nil {
				t.Fatal(err)
			}
			if _, err := installer.Install(context.Background(), plan); err == nil || !strings.Contains(err.Error(), "manifest") {
				t.Fatalf("Install() after mutation error = %v", err)
			}
			contents, err := os.ReadFile(sentinel)
			if err != nil || string(contents) != "keep" {
				t.Fatalf("unrelated file changed: %q, %v", contents, err)
			}
		})
	}
}

func TestVerifyInstalledIsReadOnlyAndChecksExactActivation(t *testing.T) {
	payload := []byte("verified asc")
	server := serveTLS(t, payload)
	paths := testPaths(t)
	plan := planFor(t, "asc", "5.0.0", "binary", payload, server.URL, paths)
	if _, err := VerifyInstalled(context.Background(), plan); !errors.Is(err, ErrInstallAbsent) {
		t.Fatalf("VerifyInstalled() absent error = %v", err)
	}
	if _, err := os.Lstat(paths.ToolsDir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("read-only verification created tools path: %v", err)
	}
	if _, err := (Installer{Client: server.Client()}).Install(context.Background(), plan); err != nil {
		t.Fatal(err)
	}
	receipt, err := VerifyInstalled(context.Background(), plan)
	if err != nil {
		t.Fatal(err)
	}
	if receipt.Tool != "asc" || receipt.Version != "5.0.0" || receipt.ExecutablePath != plan.ExecutablePath() || receipt.InstalledAt.IsZero() {
		t.Fatalf("unexpected receipt: %#v", receipt)
	}
	other := filepath.Join(paths.ToolsDir, "other")
	if err := os.WriteFile(other, []byte("other"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(plan.ExecutablePath()); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(other, plan.ExecutablePath()); err != nil {
		t.Fatal(err)
	}
	if _, err := VerifyInstalled(context.Background(), plan); !errors.Is(err, ErrInstallInvalid) {
		t.Fatalf("VerifyInstalled() wrong activation error = %v", err)
	}
}

func TestInstallRejectsSymlinkAncestorsWithoutTouchingOutside(t *testing.T) {
	for _, selected := range []string{"tools", "downloads", "bin"} {
		t.Run(selected, func(t *testing.T) {
			base := t.TempDir()
			outside := filepath.Join(t.TempDir(), "outside")
			if err := os.Mkdir(outside, 0o751); err != nil {
				t.Fatal(err)
			}
			marker := filepath.Join(outside, "marker")
			if err := os.WriteFile(marker, []byte("outside"), 0o640); err != nil {
				t.Fatal(err)
			}
			link := filepath.Join(base, selected+"-ancestor")
			if err := os.Symlink(outside, link); err != nil {
				t.Fatal(err)
			}
			paths := Paths{
				ToolsDir:     filepath.Join(base, "tools root with spaces & %"),
				DownloadsDir: filepath.Join(base, "downloads root with spaces & %"),
				BinDir:       filepath.Join(base, "bin root with spaces & %"),
			}
			switch selected {
			case "tools":
				paths.ToolsDir = filepath.Join(link, "managed")
			case "downloads":
				paths.DownloadsDir = filepath.Join(link, "managed")
			case "bin":
				paths.BinDir = filepath.Join(link, "managed")
			}
			payload := []byte("asc")
			server := serveTLS(t, payload)
			plan := planFor(t, "asc", "5.0.0", "binary", payload, server.URL, paths)
			if _, err := (Installer{Client: server.Client()}).Install(context.Background(), plan); err == nil || !strings.Contains(err.Error(), "not a real directory") {
				t.Fatalf("Install() ancestor error = %v", err)
			}
			info, err := os.Stat(outside)
			if err != nil || info.Mode().Perm() != 0o751 {
				t.Fatalf("outside mode changed: %v, %v", info, err)
			}
			contents, err := os.ReadFile(marker)
			if err != nil || string(contents) != "outside" {
				t.Fatalf("outside marker changed: %q, %v", contents, err)
			}
			entries, err := os.ReadDir(outside)
			if err != nil || len(entries) != 1 {
				t.Fatalf("outside entries changed: %v, %v", entries, err)
			}
		})
	}
}

func TestInstallRejectsToolAndVersionSymlinksWithoutTouchingTargets(t *testing.T) {
	for _, component := range []string{"tool", "version"} {
		t.Run(component, func(t *testing.T) {
			paths := testPaths(t)
			if err := ensurePrivateDir(paths.ToolsDir); err != nil {
				t.Fatal(err)
			}
			outside := filepath.Join(t.TempDir(), "outside")
			if err := os.Mkdir(outside, 0o751); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(outside, "marker"), []byte("keep"), 0o640); err != nil {
				t.Fatal(err)
			}
			toolRoot := filepath.Join(paths.ToolsDir, "asc")
			if component == "tool" {
				if err := os.Symlink(outside, toolRoot); err != nil {
					t.Fatal(err)
				}
			} else {
				if err := os.Mkdir(toolRoot, 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(outside, filepath.Join(toolRoot, "5.0.0")); err != nil {
					t.Fatal(err)
				}
			}
			payload := []byte("asc")
			server := serveTLS(t, payload)
			plan := planFor(t, "asc", "5.0.0", "binary", payload, server.URL, paths)
			if _, err := (Installer{Client: server.Client()}).Install(context.Background(), plan); err == nil {
				t.Fatal("Install unexpectedly succeeded")
			}
			info, err := os.Stat(outside)
			if err != nil || info.Mode().Perm() != 0o751 {
				t.Fatalf("outside mode changed: %v, %v", info, err)
			}
			entries, _ := os.ReadDir(outside)
			if len(entries) != 1 {
				t.Fatalf("outside entries changed: %v", entries)
			}
		})
	}
}

func TestInstallSupportsSpecialCharactersInManagedPaths(t *testing.T) {
	root := filepath.Join(t.TempDir(), "orchard paths with spaces & %")
	paths := Paths{ToolsDir: filepath.Join(root, "tools"), DownloadsDir: filepath.Join(root, "downloads"), BinDir: filepath.Join(root, "bin")}
	payload := []byte("asc")
	server := serveTLS(t, payload)
	plan := planFor(t, "asc", "5.0.0", "binary", payload, server.URL, paths)
	if _, err := (Installer{Client: server.Client()}).Install(context.Background(), plan); err != nil {
		t.Fatal(err)
	}
	if _, err := VerifyInstalled(context.Background(), plan); err != nil {
		t.Fatal(err)
	}
}

func TestTarExpansionLimitIncludesCompressedTrailingMembers(t *testing.T) {
	archive := makeTarGzip(t, []tarEntry{{name: "zsign", body: []byte("ok"), mode: 0o755}})
	var tail bytes.Buffer
	gz := gzip.NewWriter(&tail)
	if _, err := gz.Write(bytes.Repeat([]byte{0}, 16<<10)); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	archive = append(archive, tail.Bytes()...)
	filename := filepath.Join(t.TempDir(), "trailing.tar.gz")
	if err := os.WriteFile(filename, archive, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := extractTarGzipWithLimit(context.Background(), filename, t.TempDir(), "zsign", 8<<10); err == nil || !strings.Contains(err.Error(), "limit") {
		t.Fatalf("extractTarGzipWithLimit() error = %v", err)
	}
}

func TestTarExtractionHonorsCancellation(t *testing.T) {
	archive := makeTarGzip(t, []tarEntry{{name: "zsign", body: bytes.Repeat([]byte("x"), 1<<20), mode: 0o755}})
	filename := filepath.Join(t.TempDir(), "fixture.tar.gz")
	if err := os.WriteFile(filename, archive, 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := extractTarGzip(ctx, filename, t.TempDir(), "zsign"); !errors.Is(err, context.Canceled) {
		t.Fatalf("extractTarGzip() cancellation error = %v", err)
	}
}

func TestDefaultAppImageRunnerKillsDescendantProcessGroup(t *testing.T) {
	paths := testPaths(t)
	oldPayload := []byte("old appimage")
	oldServer := serveTLS(t, oldPayload)
	oldPlan := planFor(t, "xtool", "1.18.0", "appimage", oldPayload, oldServer.URL, paths)
	if _, err := (Installer{Client: oldServer.Client(), Runner: &fixtureRunner{}}).Install(context.Background(), oldPlan); err != nil {
		t.Fatal(err)
	}
	oldTarget, err := filepath.EvalSymlinks(oldPlan.ExecutablePath())
	if err != nil {
		t.Fatal(err)
	}

	marker := filepath.Join(t.TempDir(), "child-writes")
	started := marker + ".started"
	script := []byte(fmt.Sprintf(`#!/bin/sh
set -eu
(trap '' TERM; : > %q; while :; do printf x >> %q; sleep 0.02; done) &
wait
`, started, marker))
	server := serveTLS(t, script)
	plan := planFor(t, "xtool", "1.19.0", "appimage", script, server.URL, paths)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { _, err := (Installer{Client: server.Client()}).Install(ctx, plan); done <- err }()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := os.Stat(started); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("descendant did not start")
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("cancelled extraction unexpectedly succeeded")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("cancelled extraction did not return promptly")
	}
	before, _ := os.ReadFile(marker)
	time.Sleep(200 * time.Millisecond)
	after, _ := os.ReadFile(marker)
	if len(after) != len(before) {
		t.Fatalf("descendant continued writing after cleanup: %d -> %d", len(before), len(after))
	}
	stagingEntries, err := os.ReadDir(filepath.Join(paths.ToolsDir, ".staging"))
	if err != nil || len(stagingEntries) != 0 {
		t.Fatalf("cancelled extraction stage was not cleaned: %v, %v", stagingEntries, err)
	}
	resolved, err := filepath.EvalSymlinks(oldPlan.ExecutablePath())
	if err != nil || resolved != oldTarget {
		t.Fatalf("previous activation changed: %q, %v", resolved, err)
	}
}

func writeExisting(t *testing.T, filename string, data []byte, mode os.FileMode) {
	t.Helper()
	if err := os.WriteFile(filename, data, mode); err != nil {
		t.Fatal(err)
	}
}
