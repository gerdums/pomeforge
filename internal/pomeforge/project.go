package pomeforge

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"syscall"
)

var (
	projectNamePattern = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_]{0,63}$`)
	bundlePartPattern  = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9-]*$`)
	versionPattern     = regexp.MustCompile(`^[0-9]+(?:\.[0-9]+){1,2}$`)
	buildPattern       = regexp.MustCompile(`^[1-9][0-9]*$`)
	identifierPattern  = regexp.MustCompile(`^[0-9]+$`)
)

const maxManifestBytes = 1 << 20

func CanonicalWorkspace(path string) (string, error) {
	if path == "" {
		path = "."
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", Errorf("invalid_path", "resolve workspace: "+err.Error())
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", Errorf("invalid_workspace", "workspace must exist: "+err.Error())
	}
	info, err := os.Stat(resolved)
	if err != nil || !info.IsDir() {
		return "", Errorf("invalid_workspace", "workspace must be a directory")
	}
	return filepath.Clean(resolved), nil
}

func ResolveWithin(workspace, requested string, mustExist bool) (string, error) {
	if requested == "" {
		return "", Errorf("invalid_path", "path is required")
	}
	candidate := requested
	if !filepath.IsAbs(candidate) {
		candidate = filepath.Join(workspace, candidate)
	}
	candidate, err := filepath.Abs(candidate)
	if err != nil {
		return "", Errorf("invalid_path", err.Error())
	}
	candidate = filepath.Clean(candidate)
	rel, err := filepath.Rel(workspace, candidate)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", Errorf("path_outside_workspace", "path must remain inside the workspace")
	}
	current := workspace
	if rel != "." {
		for _, part := range strings.Split(rel, string(filepath.Separator)) {
			current = filepath.Join(current, part)
			info, statErr := os.Lstat(current)
			if statErr != nil {
				if errors.Is(statErr, fs.ErrNotExist) {
					if mustExist {
						return "", Errorf("path_not_found", "path does not exist: "+requested)
					}
					continue
				}
				return "", Errorf("invalid_path", statErr.Error())
			}
			if info.Mode()&os.ModeSymlink != 0 {
				return "", Errorf("symlink_not_allowed", "symlinks are not allowed in workspace paths")
			}
		}
	}
	if mustExist {
		resolved, resolveErr := filepath.EvalSymlinks(candidate)
		if resolveErr != nil {
			return "", Errorf("path_not_found", "path does not exist: "+requested)
		}
		resolvedRel, relErr := filepath.Rel(workspace, resolved)
		if relErr != nil || resolvedRel == ".." || strings.HasPrefix(resolvedRel, ".."+string(filepath.Separator)) {
			return "", Errorf("path_outside_workspace", "resolved path escapes the workspace")
		}
	}
	return candidate, nil
}

func ValidateName(name string) error {
	if !projectNamePattern.MatchString(name) {
		return Errorf("invalid_name", "name must start with a letter and contain only letters, digits, or underscores (maximum 64 characters)")
	}
	return nil
}

func ValidateBundleID(id string) error {
	if len(id) > 255 {
		return Errorf("invalid_bundle_id", "bundle identifier is too long")
	}
	parts := strings.Split(id, ".")
	if len(parts) < 2 {
		return Errorf("invalid_bundle_id", "bundle identifier must contain at least two dot-separated components")
	}
	for _, part := range parts {
		if !bundlePartPattern.MatchString(part) {
			return Errorf("invalid_bundle_id", "bundle identifier components may contain only letters, digits, and hyphens")
		}
	}
	return nil
}

func ValidateManifest(m Manifest) error {
	if m.SchemaVersion != ManifestSchemaVersion {
		return Errorf("unsupported_manifest", fmt.Sprintf("unsupported manifest schemaVersion %d; expected %d", m.SchemaVersion, ManifestSchemaVersion))
	}
	if err := ValidateName(m.Name); err != nil {
		return err
	}
	if err := ValidateBundleID(m.BundleIdentifier); err != nil {
		return err
	}
	if len(m.DeviceFamilies) != 2 || m.DeviceFamilies[0] != "iphone" || m.DeviceFamilies[1] != "ipad" {
		return Errorf("unsupported_manifest", "deviceFamilies must be exactly [\"iphone\", \"ipad\"]")
	}
	if !versionPattern.MatchString(m.MinimumIOSVersion) {
		return Errorf("invalid_manifest", "minimumIOSVersion must be a numeric dotted version")
	}
	major, _ := strconv.Atoi(strings.Split(m.MinimumIOSVersion, ".")[0])
	if major < 17 {
		return Errorf("unsupported_manifest", "minimumIOSVersion must be 17.0 or newer")
	}
	if !versionPattern.MatchString(m.MarketingVersion) {
		return Errorf("invalid_manifest", "marketingVersion must be a numeric dotted version")
	}
	if !buildPattern.MatchString(m.BuildNumber) {
		return Errorf("invalid_manifest", "buildNumber must be a positive integer string")
	}
	if m.AppStore.AppID != "" && !identifierPattern.MatchString(m.AppStore.AppID) {
		return Errorf("invalid_manifest", "appId must be a numeric App Store Connect app ID")
	}
	for label, value := range map[string]string{"versionId": m.AppStore.VersionID, "buildId": m.AppStore.BuildID} {
		if value != "" && !uuidPattern.MatchString(value) {
			return Errorf("invalid_manifest", label+" must be a UUID-compatible App Store Connect resource ID")
		}
	}
	return nil
}

func LoadManifest(project string) (Manifest, []byte, error) {
	return loadManifestWithin(project, project)
}

func loadManifestWithin(workspace, project string) (Manifest, []byte, error) {
	path := filepath.Join(project, "pomeforge.json")
	file, err := openRegularWithin(workspace, path, os.O_RDONLY, 0)
	if err != nil {
		if errors.Is(err, syscall.ELOOP) {
			return Manifest{}, nil, Errorf("symlink_not_allowed", "pomeforge.json must not be a symlink")
		}
		return Manifest{}, nil, Errorf("manifest_not_found", "read pomeforge.json: "+err.Error())
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return Manifest{}, nil, Errorf("invalid_manifest", "inspect pomeforge.json: "+err.Error())
	}
	if info.Size() > maxManifestBytes {
		return Manifest{}, nil, Errorf("invalid_manifest", fmt.Sprintf("pomeforge.json exceeds the %d-byte limit", maxManifestBytes))
	}
	raw, err := io.ReadAll(io.LimitReader(file, maxManifestBytes+1))
	if err != nil {
		return Manifest{}, nil, Errorf("invalid_manifest", "read pomeforge.json: "+err.Error())
	}
	if len(raw) > maxManifestBytes {
		return Manifest{}, nil, Errorf("invalid_manifest", fmt.Sprintf("pomeforge.json exceeds the %d-byte limit", maxManifestBytes))
	}
	var manifest Manifest
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decodeOne(decoder, &manifest); err != nil {
		return Manifest{}, nil, Errorf("invalid_manifest", "decode pomeforge.json: "+err.Error())
	}
	if err := ValidateManifest(manifest); err != nil {
		return Manifest{}, nil, err
	}
	return manifest, raw, nil
}

func decodeOne(decoder *json.Decoder, value any) error {
	if err := decoder.Decode(value); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); err == nil {
		return errors.New("multiple JSON values")
	} else if !errors.Is(err, io.EOF) {
		return err
	}
	return nil
}

func CreateProject(workspace, directory, name, bundleID string) (ProjectSummary, error) {
	if err := ValidateName(name); err != nil {
		return ProjectSummary{}, err
	}
	if err := ValidateBundleID(bundleID); err != nil {
		return ProjectSummary{}, err
	}
	target, err := ResolveWithin(workspace, directory, false)
	if err != nil {
		return ProjectSummary{}, err
	}
	if _, err := os.Lstat(target); err == nil {
		return ProjectSummary{}, Errorf("already_exists", "refusing to overwrite existing path: "+target)
	} else if !errors.Is(err, fs.ErrNotExist) {
		return ProjectSummary{}, Errorf("create_failed", err.Error())
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return ProjectSummary{}, Errorf("create_failed", err.Error())
	}
	if err := os.Mkdir(target, 0o755); err != nil {
		return ProjectSummary{}, Errorf("create_failed", err.Error())
	}
	manifest := Manifest{
		SchemaVersion: ManifestSchemaVersion, Name: name, BundleIdentifier: bundleID,
		DeviceFamilies: []string{"iphone", "ipad"}, MinimumIOSVersion: "17.0",
		MarketingVersion: "1.0.0", BuildNumber: "1",
	}
	manifestBytes, _ := json.MarshalIndent(manifest, "", "  ")
	manifestBytes = append(manifestBytes, '\n')
	files := map[string][]byte{
		"pomeforge.json": manifestBytes,
		"Package.swift":  []byte(packageTemplate(name)),
		"xtool.yml":      []byte("version: 1\nbundleID: " + bundleID + "\ninfoPath: Info.plist\n"),
		"Info.plist":     []byte(infoPlistTemplate(name, bundleID)),
		filepath.Join("Sources", name, name+"App.swift"): []byte(swiftTemplate(name)),
		filepath.Join(".sourcekit-lsp", "config.json"):   []byte("{\"swiftPM\":{\"swiftSDK\":\"arm64-apple-ios\"}}\n"),
		".gitignore": []byte(".build/\nxtool/\n.pomeforge/\n*.ipa\n"),
		"RELEASE.md": []byte(releaseTemplate),
	}
	iconFiles, err := generatedIconFiles(placeholderIcon(), true)
	if err != nil {
		return ProjectSummary{}, Errorf("create_failed", "generate starter icon catalog: "+err.Error())
	}
	for name, contents := range iconFiles {
		files[filepath.Join("Assets.xcassets", "AppIcon.appiconset", name)] = contents
	}
	for rel, contents := range files {
		path := filepath.Join(target, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return ProjectSummary{}, Errorf("create_failed", err.Error())
		}
		if err := os.WriteFile(path, contents, 0o644); err != nil {
			return ProjectSummary{}, Errorf("create_failed", err.Error())
		}
	}
	return ProjectSummary{Path: target, Name: name, BundleID: bundleID}, nil
}

func DiscoverProjects(workspace string) ([]ProjectSummary, error) {
	projects := make([]ProjectSummary, 0)
	err := filepath.WalkDir(workspace, func(projectPath string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if !entry.IsDir() {
			return nil
		}
		relative, _ := filepath.Rel(workspace, projectPath)
		if relative != "." && (entry.Type()&os.ModeSymlink != 0 || strings.HasPrefix(entry.Name(), ".") || entry.Name() == "node_modules") {
			return filepath.SkipDir
		}
		manifestInfo, statErr := os.Lstat(filepath.Join(projectPath, "pomeforge.json"))
		if statErr != nil || !manifestInfo.Mode().IsRegular() {
			return nil
		}
		path, resolveErr := ResolveWithin(workspace, relative, true)
		if resolveErr != nil {
			return filepath.SkipDir
		}
		manifest, _, loadErr := loadManifestWithin(workspace, path)
		if loadErr != nil {
			return filepath.SkipDir
		}
		projects = append(projects, ProjectSummary{Path: filepath.ToSlash(relative), Name: manifest.Name, BundleID: manifest.BundleIdentifier})
		return filepath.SkipDir
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(projects, func(i, j int) bool { return projects[i].Path < projects[j].Path })
	return projects, nil
}

func packageTemplate(name string) string {
	return "// swift-tools-version: 6.0\nimport Foundation\nimport PackageDescription\n\nlet sdkRoot = ProcessInfo.processInfo.environment[\"POMEFORGE_IOS_SDK_ROOT\"]\nlet sdkLinkerSettings: [LinkerSetting] = sdkRoot.map { [.unsafeFlags([\"-Xclang-linker\", \"-isysroot\", \"-Xclang-linker\", $0], .when(platforms: [.iOS]))] } ?? []\n\nlet package = Package(\n    name: \"" + name + "\",\n    platforms: [.iOS(.v17), .macOS(.v14)],\n    products: [.library(name: \"" + name + "\", targets: [\"" + name + "\"])],\n    targets: [.target(name: \"" + name + "\", linkerSettings: sdkLinkerSettings)]\n)\n"
}

func swiftTemplate(name string) string {
	return "import SwiftUI\n\n@main\nstruct " + name + "App: App {\n    var body: some Scene {\n        WindowGroup {\n            ContentView()\n        }\n    }\n}\n\nstruct ContentView: View {\n    var body: some View {\n        VStack(spacing: 12) {\n            Image(systemName: \"leaf.fill\")\n                .font(.largeTitle)\n            Text(\"Hello from " + name + "\")\n        }\n        .padding()\n    }\n}\n"
}

func infoPlistTemplate(name, bundleID string) string {
	return `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
  <key>CFBundleDisplayName</key><string>` + name + `</string>
  <key>CFBundleName</key><string>` + name + `</string>
  <key>CFBundleExecutable</key><string>` + name + `</string>
  <key>CFBundleIdentifier</key><string>` + bundleID + `</string>
  <key>CFBundleShortVersionString</key><string>1.0.0</string>
  <key>CFBundleVersion</key><string>1</string>
  <key>MinimumOSVersion</key><string>17.0</string>
  <key>UIDeviceFamily</key><array><integer>1</integer><integer>2</integer></array>
  <key>UISupportedInterfaceOrientations</key><array><string>UIInterfaceOrientationPortrait</string></array>
  <key>UISupportedInterfaceOrientations~ipad</key><array><string>UIInterfaceOrientationPortrait</string><string>UIInterfaceOrientationPortraitUpsideDown</string><string>UIInterfaceOrientationLandscapeLeft</string><string>UIInterfaceOrientationLandscapeRight</string></array>
</dict></plist>
`
}

const releaseTemplate = `# Linux release path

The generated package is a SwiftPM library target for a SwiftUI iPhone/iPad app.
Its linker settings consume POMEFORGE_IOS_SDK_ROOT only when Pomeforge supplies the
validated active SDK for a selected release build. Do not hardcode an SDK path.

Assets.xcassets contains a technically complete, clearly marked starter catalog.
Customize it without AI using ` + "`pomeforge run icons --project . --icon-source Icon-1024.png --execute`" + `.
Then build the unsigned release bundle, configure a named private distribution
identity, and export the signed IPA as separate explicit operations.
Development signing is not App Store signing. Upload and review submission remain separate,
operator-confirmed actions, and local inspection never means Apple acceptance.
`
