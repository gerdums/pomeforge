package bootstrap

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// TestRealPinnedInstallers is opt-in because it requires locally retained
// upstream artifacts. Digests, sizes, versions, and formats still come only
// from the repository catalog; a local TLS server replaces only transport.
func TestRealPinnedInstallers(t *testing.T) {
	artifactDirectory := os.Getenv("POMEFORGE_REAL_INSTALL_ARTIFACT_DIR")
	if artifactDirectory == "" {
		t.Skip("set POMEFORGE_REAL_INSTALL_ARTIFACT_DIR to run pinned installer integration")
	}
	file, err := os.Open(filepath.Join("..", "..", "toolchains.lock.json"))
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := Load(file)
	_ = file.Close()
	if err != nil {
		t.Fatal(err)
	}

	artifacts := make(map[string]string)
	selected := make(map[string]Asset)
	for _, tool := range catalog.Tools {
		for _, asset := range tool.Assets {
			if asset.OS == "linux" && asset.Arch == runtime.GOARCH {
				selected[tool.ID] = asset
				artifacts[tool.ID] = filepath.Join(artifactDirectory, fmt.Sprintf(
					"%s-%s-%s-%s-%s", tool.ID, tool.Version, asset.OS, asset.Arch, strings.ToLower(asset.SHA256),
				))
			}
		}
	}
	if len(selected) != 3 {
		t.Fatalf("catalog has %d pinned Linux/%s tools, want 3", len(selected), runtime.GOARCH)
	}
	for id, filename := range artifacts {
		if _, err := os.Stat(filename); err != nil {
			t.Fatalf("artifact for %s at %s: %v", id, filename, err)
		}
	}

	var requests atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		id := filepath.Base(request.URL.Path)
		filename, ok := artifacts[id]
		if !ok {
			http.NotFound(response, request)
			return
		}
		input, err := os.Open(filename)
		if err != nil {
			http.Error(response, err.Error(), http.StatusInternalServerError)
			return
		}
		defer input.Close()
		requests.Add(1)
		_, _ = io.Copy(response, input)
	}))
	defer server.Close()

	for _, originalTool := range catalog.Tools {
		t.Run(originalTool.ID, func(t *testing.T) {
			asset, ok := selected[originalTool.ID]
			if !ok {
				t.Fatalf("no Linux/%s asset", runtime.GOARCH)
			}
			asset.URL = server.URL + "/" + originalTool.ID
			tool := cloneTool(originalTool)
			tool.Assets = []Asset{asset}
			transportCatalog := Catalog{
				SchemaVersion: catalog.SchemaVersion,
				VerifiedAt:    catalog.VerifiedAt,
				Tools:         []Tool{tool},
			}
			root := filepath.Join(t.TempDir(), "XDG path with spaces & 100%")
			paths := Paths{
				ToolsDir:     filepath.Join(root, "tools"),
				DownloadsDir: filepath.Join(root, "downloads"),
				BinDir:       filepath.Join(root, "bin"),
			}
			plan, err := PlanInstall(transportCatalog, tool.ID, "linux", runtime.GOARCH, paths)
			if err != nil {
				t.Fatal(err)
			}
			installer := Installer{Client: server.Client()}
			if _, err := installer.Install(context.Background(), plan); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			arguments := []string{"--version"}
			if tool.ID == "asc" {
				arguments = []string{"version"}
			}
			if output, err := exec.CommandContext(ctx, plan.ExecutablePath(), arguments...).CombinedOutput(); err != nil {
				t.Fatalf("execute %s version: %v: %s", tool.ID, err, output)
			}
			if _, err := VerifyInstalled(context.Background(), plan); err != nil {
				t.Fatal(err)
			}
			beforeReuse := requests.Load()
			if _, err := installer.Install(context.Background(), plan); err != nil {
				t.Fatalf("reuse: %v", err)
			}
			if requests.Load() != beforeReuse {
				t.Fatal("successful reuse unexpectedly downloaded the artifact")
			}
			resolved, err := filepath.EvalSymlinks(plan.ExecutablePath())
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(resolved, []byte("mutated"), 0o700); err != nil {
				t.Fatal(err)
			}
			if _, err := installer.Install(context.Background(), plan); err == nil {
				t.Fatal("mutated installation was accepted for reuse")
			}
		})
	}
}
