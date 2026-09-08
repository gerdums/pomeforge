package orchard

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
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
