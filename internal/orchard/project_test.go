package orchard

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestCreateProjectTemplateAndNonOverwrite(t *testing.T) {
	workspace := t.TempDir()
	project, err := CreateProject(workspace, "Demo", "Demo", "com.example.Demo")
	if err != nil {
		t.Fatal(err)
	}
	if project.Path != filepath.Join(workspace, "Demo") {
		t.Fatalf("unexpected path %q", project.Path)
	}
	checks := map[string]string{
		"orchard.json":               `"deviceFamilies": [`,
		"Package.swift":              `.library(name: "Demo", targets: ["Demo"])`,
		"xtool.yml":                  "version: 1\nbundleID: com.example.Demo",
		"Info.plist":                 "<key>UIDeviceFamily</key><array><integer>1</integer><integer>2</integer></array>",
		"Sources/Demo/DemoApp.swift": "@main",
		".sourcekit-lsp/config.json": `"arm64-apple-ios"`,
		"RELEASE.md":                 "Development signing is",
	}
	for name, expected := range checks {
		contents, readErr := os.ReadFile(filepath.Join(project.Path, filepath.FromSlash(name)))
		if readErr != nil {
			t.Fatalf("read %s: %v", name, readErr)
		}
		if !strings.Contains(string(contents), expected) {
			t.Errorf("%s does not contain %q", name, expected)
		}
	}
	marker := filepath.Join(project.Path, "untracked.txt")
	if err := os.WriteFile(marker, []byte("preserve"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := CreateProject(workspace, "Demo", "Demo", "com.example.Demo"); err == nil {
		t.Fatal("expected overwrite refusal")
	}
	contents, _ := os.ReadFile(marker)
	if string(contents) != "preserve" {
		t.Fatal("existing content was changed")
	}
}

func TestProjectValidation(t *testing.T) {
	for _, name := range []string{"", "../Demo", "has-dash", "1Demo", strings.Repeat("A", 65)} {
		if err := ValidateName(name); err == nil {
			t.Errorf("name %q should fail", name)
		}
	}
	for _, bundleID := range []string{"", "single", "com..demo", "com.example_bad", ".com.demo"} {
		if err := ValidateBundleID(bundleID); err == nil {
			t.Errorf("bundle ID %q should fail", bundleID)
		}
	}
}

func TestManifestRejectsUnknownAndUnsupportedValues(t *testing.T) {
	project := t.TempDir()
	valid := `{"schemaVersion":1,"name":"Demo","bundleIdentifier":"com.example.Demo","deviceFamilies":["iphone","ipad"],"minimumIOSVersion":"17.0","marketingVersion":"1.0.0","buildNumber":"1"}`
	if err := os.WriteFile(filepath.Join(project, "orchard.json"), []byte(valid), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := LoadManifest(project); err != nil {
		t.Fatalf("valid manifest: %v", err)
	}
	unknown := strings.TrimSuffix(valid, "}") + `,"secret":"no"}`
	if err := os.WriteFile(filepath.Join(project, "orchard.json"), []byte(unknown), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := LoadManifest(project); err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("expected unknown field rejection, got %v", err)
	}
	unsupported := strings.Replace(valid, `"schemaVersion":1`, `"schemaVersion":2`, 1)
	if err := os.WriteFile(filepath.Join(project, "orchard.json"), []byte(unsupported), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := LoadManifest(project); err == nil || !strings.Contains(err.Error(), "unsupported") {
		t.Fatalf("expected schema rejection, got %v", err)
	}
}

func TestManifestAcceptsResourceIDsAndRejectsControlLikeIDs(t *testing.T) {
	manifest := Manifest{
		SchemaVersion: 1, Name: "Demo", BundleIdentifier: "com.example.Demo",
		DeviceFamilies: []string{"iphone", "ipad"}, MinimumIOSVersion: "17.0",
		MarketingVersion: "1.2.3", BuildNumber: "4",
		AppStore: AppStoreIDs{AppID: "123456789", VersionID: "a1b2c3d4-1111-4222-8333-abcdef123456", BuildID: "build_RESOURCE-123"},
	}
	if err := ValidateManifest(manifest); err != nil {
		t.Fatalf("realistic resource IDs rejected: %v", err)
	}
	for _, bad := range []string{"--version", "../escape", "with/slash", "line\nbreak", strings.Repeat("a", 129)} {
		manifest.AppStore.VersionID = bad
		if err := ValidateManifest(manifest); err == nil {
			t.Errorf("unsafe resource ID %q was accepted", bad)
		}
	}
}

func TestLoadManifestRejectsFIFOAndOversizeWithoutBlocking(t *testing.T) {
	project := t.TempDir()
	path := filepath.Join(project, "orchard.json")
	if err := syscall.Mkfifo(path, 0o600); err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	if _, _, err := LoadManifest(project); err == nil {
		t.Fatal("FIFO manifest was accepted")
	}
	if time.Since(started) > time.Second {
		t.Fatal("FIFO manifest read blocked")
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Truncate(maxManifestBytes + 1); err != nil {
		t.Fatal(err)
	}
	_ = file.Close()
	if _, _, err := LoadManifest(project); err == nil || !strings.Contains(err.Error(), "limit") {
		t.Fatalf("oversized manifest was not rejected: %v", err)
	}
}

func TestLoadManifestRejectsSymlinkWithoutChangingTarget(t *testing.T) {
	project := t.TempDir()
	target := filepath.Join(t.TempDir(), "manifest-target")
	if err := os.WriteFile(target, []byte("preserve"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(project, "orchard.json")); err != nil {
		t.Fatal(err)
	}
	if _, _, err := LoadManifest(project); err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("manifest symlink was not rejected: %v", err)
	}
	contents, _ := os.ReadFile(target)
	if string(contents) != "preserve" {
		t.Fatal("manifest symlink target changed")
	}
}

func TestLoadManifestRejectsReplacedProjectAncestorAfterResolution(t *testing.T) {
	workspace := t.TempDir()
	project, err := CreateProject(workspace, filepath.Join("nested", "Demo"), "Demo", "com.example.Demo")
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := ResolveWithin(workspace, project.Path, true)
	if err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	if _, err := CreateProject(outside, "Demo", "Outside", "com.example.Outside"); err != nil {
		t.Fatal(err)
	}
	ancestor := filepath.Join(workspace, "nested")
	if err := os.Rename(ancestor, filepath.Join(workspace, "opened-nested")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, ancestor); err != nil {
		t.Fatal(err)
	}

	manifest, _, err := loadManifestWithin(workspace, resolved)
	if err == nil {
		t.Fatalf("replaced ancestor redirected manifest read to %q", manifest.Name)
	}
}

func TestResolveWithinRejectsTraversalAndSymlink(t *testing.T) {
	workspace := t.TempDir()
	outside := t.TempDir()
	if _, err := ResolveWithin(workspace, "../escape", false); err == nil {
		t.Fatal("expected traversal rejection")
	}
	if err := os.Symlink(outside, filepath.Join(workspace, "link")); err != nil {
		t.Fatal(err)
	}
	if _, err := ResolveWithin(workspace, filepath.Join("link", "child"), false); err == nil {
		t.Fatal("expected symlink rejection")
	}
}
