package bootstrap

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestInstallBinaryActivatesAndReturnsReceipt(t *testing.T) {
	payload := []byte("#!/bin/sh\necho asc\n")
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(payload)
	}))
	defer server.Close()
	plan := planFor(t, "asc", "5.0.0", "binary", payload, server.URL, testPaths(t))
	receipt, err := (Installer{Client: server.Client()}).Install(context.Background(), plan)
	if err != nil {
		t.Fatalf("Install() error = %v", err)
	}
	if receipt.Tool != "asc" || receipt.Version != "5.0.0" || receipt.ExecutablePath != plan.ExecutablePath() {
		t.Fatalf("unexpected receipt: %#v", receipt)
	}
	assertActiveContents(t, plan.ExecutablePath(), payload)
	info, err := os.Stat(plan.ExecutablePath())
	if err != nil || info.Mode().Perm()&0o100 == 0 {
		t.Fatalf("active executable mode/error = %v / %v", info, err)
	}
}

func TestInstallReplacesCorruptCacheOnlyAfterVerification(t *testing.T) {
	payload := []byte("verified payload")
	var requests atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		_, _ = w.Write(payload)
	}))
	defer server.Close()
	plan := planFor(t, "asc", "5.0.0", "binary", payload, server.URL, testPaths(t))
	if err := ensurePrivateDir(plan.paths.DownloadsDir); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(plan.paths.DownloadsDir, cacheName(plan)), bytes.Repeat([]byte{'x'}, len(payload)), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := (Installer{Client: server.Client()}).Install(context.Background(), plan); err != nil {
		t.Fatal(err)
	}
	if requests.Load() != 1 {
		t.Fatalf("requests = %d, want 1", requests.Load())
	}
	assertActiveContents(t, plan.ExecutablePath(), payload)
}

func TestInstallRejectsDownloadFailures(t *testing.T) {
	payload := []byte("expected")
	tests := []struct {
		name    string
		handler http.HandlerFunc
		body    []byte
		want    string
	}{
		{"http status", func(w http.ResponseWriter, _ *http.Request) { http.Error(w, "no", http.StatusBadGateway) }, payload, "HTTP status"},
		{"truncated", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("short")) }, payload, "size mismatch"},
		{"checksum", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("different")) }, []byte("same-size"), "checksum mismatch"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewTLSServer(test.handler)
			defer server.Close()
			plan := planFor(t, "asc", "5.0.0", "binary", test.body, server.URL, testPaths(t))
			_, err := (Installer{Client: server.Client()}).Install(context.Background(), plan)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Install() error = %v, want %q", err, test.want)
			}
			if _, statErr := os.Lstat(plan.ExecutablePath()); !errors.Is(statErr, os.ErrNotExist) {
				t.Fatalf("failed install created activation: %v", statErr)
			}
		})
	}
}

func TestInstallCancellationDuringDownload(t *testing.T) {
	payload := bytes.Repeat([]byte{'a'}, 64)
	started := make(chan struct{})
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "64")
		_, _ = w.Write(payload[:1])
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
		close(started)
		<-r.Context().Done()
	}))
	defer server.Close()
	plan := planFor(t, "asc", "5.0.0", "binary", payload, server.URL, testPaths(t))
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := (Installer{Client: server.Client()}).Install(ctx, plan)
		done <- err
	}()
	<-started
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) && !strings.Contains(err.Error(), "context canceled") {
			t.Fatalf("Install() cancellation error = %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Install did not honor cancellation")
	}
	if _, err := os.Lstat(plan.ExecutablePath()); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("cancelled install created activation: %v", err)
	}
}

func TestInstallTarPreservesTreeAndActivatesUniqueZsign(t *testing.T) {
	archive := makeTarGzip(t, []tarEntry{
		{name: "release/", kind: tar.TypeDir},
		{name: "release/zsign", body: []byte("zsign executable"), mode: 0o755},
		{name: "release/lib/runtime.dat", body: []byte("runtime")},
	})
	server := serveTLS(t, archive)
	plan := planFor(t, "zsign", "v1.1.2", "tar.gz", archive, server.URL, testPaths(t))
	if _, err := (Installer{Client: server.Client()}).Install(context.Background(), plan); err != nil {
		t.Fatal(err)
	}
	assertActiveContents(t, plan.ExecutablePath(), []byte("zsign executable"))
	runtimePath := filepath.Join(plan.versionRoot(), "package", "release", "lib", "runtime.dat")
	if got, err := os.ReadFile(runtimePath); err != nil || string(got) != "runtime" {
		t.Fatalf("runtime file = %q, %v", got, err)
	}
}

func TestSafeArchiveRejectsMaliciousAndInvalidInputs(t *testing.T) {
	tests := []struct {
		name    string
		entries []tarEntry
		mutate  func([]byte) []byte
		want    string
	}{
		{"traversal", []tarEntry{{name: "../zsign", body: []byte("x")}}, nil, "unsafe archive path"},
		{"absolute", []tarEntry{{name: "/zsign", body: []byte("x")}}, nil, "unsafe archive path"},
		{"symlink", []tarEntry{{name: "zsign", kind: tar.TypeSymlink, link: "/bin/sh"}}, nil, "unsupported type"},
		{"hardlink", []tarEntry{{name: "zsign", kind: tar.TypeLink, link: "elsewhere"}}, nil, "unsupported type"},
		{"duplicate", []tarEntry{{name: "zsign", body: []byte("a")}, {name: "zsign", body: []byte("b")}}, nil, "duplicate entry"},
		{"two executables", []tarEntry{{name: "a/zsign", body: []byte("a")}, {name: "b/zsign", body: []byte("b")}}, nil, "exactly one"},
		{"truncated", []tarEntry{{name: "zsign", body: bytes.Repeat([]byte("x"), 200)}}, func(value []byte) []byte { return value[:len(value)/2] }, "tar.gz"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			archive := makeTarGzip(t, test.entries)
			if test.mutate != nil {
				archive = test.mutate(archive)
			}
			archivePath := filepath.Join(t.TempDir(), "fixture.tar.gz")
			if err := os.WriteFile(archivePath, archive, 0o600); err != nil {
				t.Fatal(err)
			}
			_, err := extractTarGzip(context.Background(), archivePath, t.TempDir(), "zsign")
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("extractTarGzip() error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestInstallAppImageUsesExtractionAndStableLauncher(t *testing.T) {
	payload := []byte("fake appimage bytes")
	server := serveTLS(t, payload)
	runner := &fixtureRunner{}
	plan := planFor(t, "xtool", "1.19.0", "appimage", payload, server.URL, testPaths(t))
	if _, err := (Installer{Client: server.Client(), Runner: runner}).Install(context.Background(), plan); err != nil {
		t.Fatal(err)
	}
	if !runner.called || runner.args != "--appimage-extract" || runner.path != filepath.Join(plan.versionRoot(), ".artifact") && !strings.HasSuffix(runner.path, "/complete/.artifact") {
		t.Fatalf("unexpected runner call: %#v", runner)
	}
	activeTarget, err := filepath.EvalSymlinks(plan.ExecutablePath())
	if err != nil {
		t.Fatal(err)
	}
	launcher, err := os.ReadFile(activeTarget)
	if err != nil || !bytes.Equal(launcher, []byte(appImageLauncher)) {
		t.Fatalf("launcher = %q, %v", launcher, err)
	}
	if _, err := os.Stat(filepath.Join(plan.versionRoot(), "squashfs-root", "AppRun")); err != nil {
		t.Fatalf("extracted AppRun missing: %v", err)
	}
}

func TestInstallAppImageDefaultRunnerExecutesVerifiedArtifact(t *testing.T) {
	payload := []byte(`#!/bin/sh
set -eu
test "$1" = "--appimage-extract"
mkdir -p squashfs-root/usr/bin
printf '#!/bin/sh\nprintf "xtool fixture\\n"\n' > squashfs-root/usr/bin/xtool
chmod 700 squashfs-root/usr/bin/xtool
ln -s usr/bin/xtool squashfs-root/AppRun
`)
	server := serveTLS(t, payload)
	plan := planFor(t, "xtool", "1.19.0", "appimage", payload, server.URL, testPaths(t))
	if _, err := (Installer{Client: server.Client()}).Install(context.Background(), plan); err != nil {
		t.Fatal(err)
	}
	output, err := exec.Command(plan.ExecutablePath()).CombinedOutput()
	if err != nil || string(output) != "xtool fixture\n" {
		t.Fatalf("active launcher output/error = %q / %v", output, err)
	}
}

func TestFailedAppImageExtractionPreservesPreviousActivation(t *testing.T) {
	paths := testPaths(t)
	oldPayload := []byte("old asc")
	oldServer := serveTLS(t, oldPayload)
	oldPlan := planFor(t, "asc", "4.9.0", "binary", oldPayload, oldServer.URL, paths)
	if _, err := (Installer{Client: oldServer.Client()}).Install(context.Background(), oldPlan); err != nil {
		t.Fatal(err)
	}
	oldResolved, err := filepath.EvalSymlinks(oldPlan.ExecutablePath())
	if err != nil {
		t.Fatal(err)
	}

	// Use the same active tool name with an appimage plan constructed inside
	// this package to prove activation ordering independent of format mapping.
	payload := []byte("new appimage")
	server := serveTLS(t, payload)
	newPlan := Plan{
		tool:  Tool{ID: "xtool", Version: "1.19.0", Source: "https://example.test/source", Release: "https://example.test/release"},
		asset: assetFor("appimage", payload, server.URL),
		paths: paths,
	}
	newPlan.tool.Assets = []Asset{newPlan.asset}
	// Seed a managed xtool activation to represent the previous usable version.
	oldXtoolRoot := filepath.Join(paths.ToolsDir, "xtool", "1.18.0", "linux-arm64")
	if err := os.MkdirAll(oldXtoolRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	oldXtool := filepath.Join(oldXtoolRoot, "xtool-launcher")
	if err := os.WriteFile(oldXtool, []byte("old xtool"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(oldXtool, filepath.Join(paths.BinDir, "xtool")); err != nil {
		t.Fatal(err)
	}
	failing := &fixtureRunner{err: errors.New("extract failed")}
	if _, err := (Installer{Client: server.Client(), Runner: failing}).Install(context.Background(), newPlan); err == nil {
		t.Fatal("Install unexpectedly succeeded")
	}
	resolved, err := filepath.EvalSymlinks(filepath.Join(paths.BinDir, "xtool"))
	if err != nil || resolved != oldXtool {
		t.Fatalf("previous xtool activation changed: %q, %v", resolved, err)
	}
	// The unrelated asc activation is untouched too.
	resolvedAsc, _ := filepath.EvalSymlinks(oldPlan.ExecutablePath())
	if resolvedAsc != oldResolved {
		t.Fatalf("asc activation changed: %q", resolvedAsc)
	}
}

func TestInstallRefusesUnmanagedActivationAndConcurrentLock(t *testing.T) {
	payload := []byte("binary")
	server := serveTLS(t, payload)
	paths := testPaths(t)
	plan := planFor(t, "asc", "5.0.0", "binary", payload, server.URL, paths)
	if err := os.MkdirAll(paths.BinDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(plan.ExecutablePath(), []byte("user file"), 0o700); err != nil {
		t.Fatal(err)
	}
	_, err := (Installer{Client: server.Client()}).Install(context.Background(), plan)
	if !errors.Is(err, ErrActivationCollision) {
		t.Fatalf("collision error = %v", err)
	}
	assertActiveContents(t, plan.ExecutablePath(), []byte("user file"))

	pathsLink := testPaths(t)
	planLink := planFor(t, "asc", "5.0.0", "binary", payload, server.URL, pathsLink)
	if err := os.MkdirAll(pathsLink.BinDir, 0o700); err != nil {
		t.Fatal(err)
	}
	unmanaged := filepath.Join(pathsLink.ToolsDir, "personal", "executable")
	if err := os.MkdirAll(filepath.Dir(unmanaged), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(unmanaged, []byte("personal"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(unmanaged, planLink.ExecutablePath()); err != nil {
		t.Fatal(err)
	}
	if _, err := (Installer{Client: server.Client()}).Install(context.Background(), planLink); !errors.Is(err, ErrActivationCollision) {
		t.Fatalf("unmanaged symlink collision error = %v", err)
	}
	resolvedLink, err := filepath.EvalSymlinks(planLink.ExecutablePath())
	if err != nil || resolvedLink != unmanaged {
		t.Fatalf("unmanaged symlink changed: %q / %v", resolvedLink, err)
	}

	paths2 := testPaths(t)
	plan2 := planFor(t, "asc", "5.0.0", "binary", payload, server.URL, paths2)
	if err := ensurePrivateDir(filepath.Join(paths2.ToolsDir, ".locks")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(paths2.ToolsDir, ".locks", "asc.lock"), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := (Installer{Client: server.Client()}).Install(context.Background(), plan2); !errors.Is(err, ErrInstallConflict) {
		t.Fatalf("lock error = %v", err)
	}
}

func TestInstallRejectsSymlinkInsideManagedToolTree(t *testing.T) {
	payload := []byte("binary")
	server := serveTLS(t, payload)
	paths := testPaths(t)
	if err := os.MkdirAll(paths.ToolsDir, 0o700); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(paths.ToolsDir, "asc")); err != nil {
		t.Fatal(err)
	}
	plan := planFor(t, "asc", "5.0.0", "binary", payload, server.URL, paths)
	if _, err := (Installer{Client: server.Client()}).Install(context.Background(), plan); err == nil || !strings.Contains(err.Error(), "not a real directory") {
		t.Fatalf("Install() symlink-tree error = %v", err)
	}
	entries, err := os.ReadDir(outside)
	if err != nil || len(entries) != 0 {
		t.Fatalf("installer wrote through managed symlink: entries=%v error=%v", entries, err)
	}
}

type fixtureRunner struct {
	called bool
	path   string
	args   string
	err    error
}

func (r *fixtureRunner) Run(_ context.Context, executable string, args []string, directory string, environment []string) (string, error) {
	r.called = true
	r.path = executable
	r.args = strings.Join(args, " ")
	if r.err != nil {
		return "fixture extraction failed", r.err
	}
	if len(environment) == 0 || filepath.Dir(executable) != directory {
		return "bad controlled invocation", errors.New("bad invocation")
	}
	root := filepath.Join(directory, "squashfs-root")
	bin := filepath.Join(root, "usr", "bin")
	if err := os.MkdirAll(bin, 0o700); err != nil {
		return "", err
	}
	if err := os.WriteFile(filepath.Join(bin, "xtool"), []byte("#!/bin/sh\n"), 0o700); err != nil {
		return "", err
	}
	return "", os.Symlink(filepath.Join("usr", "bin", "xtool"), filepath.Join(root, "AppRun"))
}

type tarEntry struct {
	name string
	body []byte
	kind byte
	mode int64
	link string
}

func makeTarGzip(t *testing.T, entries []tarEntry) []byte {
	t.Helper()
	var output bytes.Buffer
	gz := gzip.NewWriter(&output)
	tw := tar.NewWriter(gz)
	for _, entry := range entries {
		kind := entry.kind
		if kind == 0 {
			kind = tar.TypeReg
		}
		mode := entry.mode
		if mode == 0 {
			mode = 0o644
		}
		header := &tar.Header{Name: entry.name, Typeflag: kind, Mode: mode, Linkname: entry.link}
		if kind == tar.TypeReg {
			header.Size = int64(len(entry.body))
		}
		if err := tw.WriteHeader(header); err != nil {
			t.Fatal(err)
		}
		if len(entry.body) != 0 {
			if _, err := tw.Write(entry.body); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return output.Bytes()
}

func serveTLS(t *testing.T, payload []byte) *httptest.Server {
	t.Helper()
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(payload)
	}))
	t.Cleanup(server.Close)
	return server
}

func planFor(t *testing.T, id, version, format string, payload []byte, url string, paths Paths) Plan {
	t.Helper()
	asset := assetFor(format, payload, url)
	tool := Tool{ID: id, Version: version, Source: "https://example.test/source", Release: "https://example.test/release", Assets: []Asset{asset}}
	catalog := Catalog{SchemaVersion: 1, VerifiedAt: "2026-09-08", Tools: []Tool{tool}}
	plan, err := PlanInstall(catalog, id, "linux", "arm64", paths)
	if err != nil {
		t.Fatal(err)
	}
	return plan
}

func assetFor(format string, payload []byte, url string) Asset {
	digest := sha256.Sum256(payload)
	return Asset{OS: "linux", Arch: "arm64", URL: url, SHA256: hex.EncodeToString(digest[:]), Size: int64(len(payload)), Format: format}
}

func assertActiveContents(t *testing.T, path string, expected []byte) {
	t.Helper()
	actual, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(actual, expected) {
		t.Fatalf("contents = %q, want %q", actual, expected)
	}
}
