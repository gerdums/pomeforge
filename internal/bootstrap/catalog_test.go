package bootstrap

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadValidCatalogAndPlan(t *testing.T) {
	catalog, err := Load(strings.NewReader(validCatalogJSON(
		`{"os":"linux","arch":"arm64","url":"https://example.test/asc","sha256":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","size":12,"format":"binary"}`,
	)))
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	paths := testPaths(t)
	plan, err := PlanInstall(catalog, "asc", "linux", "arm64", paths)
	if err != nil {
		t.Fatalf("PlanInstall() error = %v", err)
	}
	if plan.Tool().Version != "5.0.0" || plan.Platform() != "linux/arm64" {
		t.Fatalf("unexpected plan: %#v / %s", plan.Tool(), plan.Platform())
	}
	if got := plan.ExecutablePath(); got != filepath.Join(paths.BinDir, "asc") {
		t.Fatalf("ExecutablePath() = %q", got)
	}
}

func TestRepositoryCatalogLoadsAndPlansEveryAsset(t *testing.T) {
	file, err := os.Open(filepath.Join("..", "..", "toolchains.lock.json"))
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	catalog, err := Load(file)
	if err != nil {
		t.Fatalf("root toolchains.lock.json: %v", err)
	}
	if len(catalog.Tools) != 3 {
		t.Fatalf("tool count = %d, want 3", len(catalog.Tools))
	}
	for _, tool := range catalog.Tools {
		for _, asset := range tool.Assets {
			if _, err := PlanInstall(catalog, tool.ID, asset.OS, asset.Arch, testPaths(t)); err != nil {
				t.Fatalf("plan %s for %s/%s: %v", tool.ID, asset.OS, asset.Arch, err)
			}
		}
	}
}

func TestLoadRejectsMalformedCatalogs(t *testing.T) {
	validAsset := `{"os":"linux","arch":"arm64","url":"https://example.test/asc","sha256":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","size":12,"format":"binary"}`
	tests := []struct {
		name    string
		catalog string
		want    string
	}{
		{"schema", strings.Replace(validCatalogJSON(validAsset), `"schemaVersion":1`, `"schemaVersion":2`, 1), "schema version"},
		{"unsafe id", strings.Replace(validCatalogJSON(validAsset), `"id":"asc"`, `"id":"../asc"`, 1), "unsafe tool id"},
		{"unsafe version", strings.Replace(validCatalogJSON(validAsset), `"version":"5.0.0"`, `"version":".."`, 1), "unsafe tool version"},
		{"short sha", strings.Replace(validCatalogJSON(validAsset), strings.Repeat("a", 64), "abc", 1), "64 hexadecimal"},
		{"zero size", strings.Replace(validCatalogJSON(validAsset), `"size":12`, `"size":0`, 1), "outside the allowed range"},
		{"unknown format", strings.Replace(validCatalogJSON(validAsset), `"format":"binary"`, `"format":"zip"`, 1), "unknown asset format"},
		{"http", strings.Replace(validCatalogJSON(validAsset), "https://example.test/asc", "http://example.test/asc", 1), "require an HTTPS"},
		{"credentials", strings.Replace(validCatalogJSON(validAsset), "https://example.test/asc", "https://user:pass@example.test/asc", 1), "without credentials"},
		{"fragment", strings.Replace(validCatalogJSON(validAsset), "https://example.test/asc", "https://example.test/asc#download", 1), "without credentials or fragment"},
		{"duplicate id", `{"schemaVersion":1,"verifiedAt":"2026-09-08","tools":[{"id":"asc","version":"5.0.0","source":"https://example.test/source","release":"https://example.test/release","assets":[` + validAsset + `]},{"id":"asc","version":"5.0.1","source":"https://example.test/source","release":"https://example.test/release","assets":[` + validAsset + `]}]}`, "duplicate tool id"},
		{"duplicate platform", validCatalogJSON(validAsset + "," + validAsset), "duplicate platform asset"},
		{"unknown field", strings.Replace(validCatalogJSON(validAsset), `"size":12`, `"surprise":true,"size":12`, 1), "unknown field"},
		{"trailing", validCatalogJSON(validAsset) + `{}`, "trailing JSON"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := Load(strings.NewReader(test.catalog))
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Load() error = %v, want containing %q", err, test.want)
			}
		})
	}
}

func TestPlanInstallUnsupportedSelection(t *testing.T) {
	catalog := mustCatalog(t, validCatalogJSON(`{"os":"linux","arch":"arm64","url":"https://example.test/asc","sha256":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","size":12,"format":"binary"}`))
	if _, err := PlanInstall(catalog, "missing", "linux", "arm64", testPaths(t)); !errors.Is(err, ErrUnsupportedTool) {
		t.Fatalf("missing tool error = %v", err)
	}
	if _, err := PlanInstall(catalog, "asc", "darwin", "arm64", testPaths(t)); !errors.Is(err, ErrUnsupportedPlatform) {
		t.Fatalf("missing platform error = %v", err)
	}
}

func TestDefaultPathsUsesXDGAndRelativeFallback(t *testing.T) {
	home := t.TempDir()
	data := filepath.Join(t.TempDir(), "data")
	t.Setenv("HOME", home)
	t.Setenv("XDG_DATA_HOME", data)
	t.Setenv("XDG_CACHE_HOME", "relative/cache")
	paths, err := DefaultPaths()
	if err != nil {
		t.Fatal(err)
	}
	if paths.ToolsDir != filepath.Join(data, "orchard", "tools") {
		t.Fatalf("ToolsDir = %q", paths.ToolsDir)
	}
	if paths.DownloadsDir != filepath.Join(home, ".cache", "orchard", "downloads") {
		t.Fatalf("DownloadsDir = %q", paths.DownloadsDir)
	}
}

func validCatalogJSON(assets string) string {
	return `{"schemaVersion":1,"verifiedAt":"2026-09-08","tools":[{"id":"asc","version":"5.0.0","source":"https://example.test/source","release":"https://example.test/release","assets":[` + assets + `]}]}`
}

func mustCatalog(t *testing.T, value string) Catalog {
	t.Helper()
	catalog, err := Load(strings.NewReader(value))
	if err != nil {
		t.Fatal(err)
	}
	return catalog
}

func testPaths(t *testing.T) Paths {
	t.Helper()
	root := t.TempDir()
	return Paths{
		ToolsDir:     filepath.Join(root, "data", "tools"),
		DownloadsDir: filepath.Join(root, "cache", "downloads"),
		BinDir:       filepath.Join(root, "data", "bin"),
	}
}

func TestPrivateDirectoriesAreOwnerOnly(t *testing.T) {
	paths := testPaths(t)
	for _, path := range []string{paths.ToolsDir, paths.DownloadsDir, paths.BinDir} {
		if err := ensurePrivateDir(path); err != nil {
			t.Fatal(err)
		}
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o700 {
			t.Fatalf("mode for %s = %o", path, info.Mode().Perm())
		}
	}
}
