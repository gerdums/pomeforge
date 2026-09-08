package distribution

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/pem"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInspectIPAValidBinaryPlist(t *testing.T) {
	f := newCryptoFixture(t)
	dir := t.TempDir()
	cms := f.profile(t, profileOptions{})
	ipa := filepath.Join(dir, "valid.ipa")
	writeIPA(t, ipa, validIPAEntries(t, cms, true))
	root := filepath.Join(dir, "root.pem")
	writePrivate(t, root, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: f.root.Raw}))
	result, err := InspectIPA(context.Background(), ipa, InspectionOptions{TrustedRootPaths: []string{root}, CurrentTime: f.now})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Valid() || !result.Value.StructureValid {
		t.Fatalf("valid IPA rejected: %+v", result.Problems)
	}
	if len(result.Value.SHA256) != 64 || result.Value.Bundle.BundleIdentifier != "com.example.Orchard" || result.Value.Bundle.MachO.LoadCommand != "LC_BUILD_VERSION" {
		t.Fatalf("unexpected report: %+v", result.Value)
	}
	if result.Value.Bundle.EmbeddedProfile == nil || result.Value.Bundle.EmbeddedProfile.Trust != TrustVerified {
		t.Fatalf("profile not verified: %+v", result.Value.Bundle.EmbeddedProfile)
	}
}

func TestInspectIPARejectsUnsafeArchives(t *testing.T) {
	f := newCryptoFixture(t)
	cms := f.profile(t, profileOptions{})
	cases := []struct {
		name, code string
		extra      []zipEntry
		replace    bool
	}{
		{"traversal", "unsafe_archive_name", []zipEntry{{"../escape", []byte("x"), 0o600}}, false},
		{"absolute", "unsafe_archive_name", []zipEntry{{"/escape", []byte("x"), 0o600}}, false},
		{"backslash", "unsafe_archive_name", []zipEntry{{`Payload\\bad`, []byte("x"), 0o600}}, false},
		{"duplicate", "duplicate_archive_entry", []zipEntry{{"Payload/Orchard.app/Info.plist", fixtureInfo(t, false), 0o600}}, false},
		{"symlink", "unsafe_archive_entry_type", []zipEntry{{"Payload/Orchard.app/link", []byte("target"), os.ModeSymlink | 0o777}}, false},
		{"symlink with trailing slash", "unsafe_archive_entry_type", []zipEntry{{"Payload/Orchard.app/link/", nil, os.ModeSymlink | 0o777}}, false},
		{"nested extension", "unsupported_nested_extension", []zipEntry{{"Payload/Orchard.app/PlugIns/Widget.appex/Info.plist", []byte("x"), 0o600}}, false},
		{"extraneous top-level", "unsupported_archive_entry", []zipEntry{{"scratch/private-key.pem", []byte("secret"), 0o600}}, false},
		{"framework", "unsupported_nested_framework", []zipEntry{{"Payload/Orchard.app/Frameworks/Kit.framework/Kit", []byte("x"), 0o700}}, false},
		{"multiple apps", "main_bundle_count", []zipEntry{{"Payload/Other.app/Info.plist", fixtureInfo(t, false), 0o600}}, false},
		{"missing app", "main_bundle_count", []zipEntry{{"metadata", []byte("x"), 0o600}}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			entries := validIPAEntries(t, cms, false)
			if tc.replace {
				entries = nil
			}
			entries = append(entries, tc.extra...)
			ipa := filepath.Join(dir, "bad.ipa")
			writeIPA(t, ipa, entries)
			result, err := InspectIPA(context.Background(), ipa, InspectionOptions{CurrentTime: f.now})
			if err != nil {
				t.Fatal(err)
			}
			if !hasProblem(result.Problems, tc.code) || result.Value.StructureValid {
				t.Fatalf("missing %s: %+v", tc.code, result.Problems)
			}
		})
	}
}

func TestInspectIPARejectsArchivePathKindConflictsInEitherOrder(t *testing.T) {
	f := newCryptoFixture(t)
	base := validIPAEntries(t, f.profile(t, profileOptions{}), false)
	cases := []struct {
		name  string
		first zipEntry
		last  zipEntry
		codes []string
	}{
		{"file then directory", zipEntry{"Payload/Orchard.app/Resources", []byte("file"), 0o600}, zipEntry{"Payload/Orchard.app/Resources/", nil, os.ModeDir | 0o755}, []string{"archive_path_kind_collision"}},
		{"directory then file", zipEntry{"Payload/Orchard.app/Resources/", nil, os.ModeDir | 0o755}, zipEntry{"Payload/Orchard.app/Resources", []byte("file"), 0o600}, []string{"archive_path_kind_collision"}},
		{"Payload file before descendants", zipEntry{"Payload", []byte("file"), 0o600}, zipEntry{}, []string{"archive_file_ancestor", "archive_required_directory"}},
		{"Payload file after descendants", zipEntry{}, zipEntry{"Payload", []byte("file"), 0o600}, []string{"archive_file_ancestor", "archive_required_directory"}},
		{"app file before descendants", zipEntry{"Payload/Orchard.app", []byte("file"), 0o600}, zipEntry{}, []string{"archive_file_ancestor", "archive_required_directory"}},
		{"app file after descendants", zipEntry{}, zipEntry{"Payload/Orchard.app", []byte("file"), 0o600}, []string{"archive_file_ancestor", "archive_required_directory"}},
		{"nested file before descendant", zipEntry{"Payload/Orchard.app/Nested", []byte("file"), 0o600}, zipEntry{"Payload/Orchard.app/Nested/value", []byte("value"), 0o600}, []string{"archive_file_ancestor"}},
		{"nested file after descendant", zipEntry{"Payload/Orchard.app/Nested/value", []byte("value"), 0o600}, zipEntry{"Payload/Orchard.app/Nested", []byte("file"), 0o600}, []string{"archive_file_ancestor"}},
		{"directory mode without slash", zipEntry{"Payload/Orchard.app/Resources", nil, os.ModeDir | 0o755}, zipEntry{}, []string{"archive_path_kind_mismatch"}},
		{"file mode with slash", zipEntry{"Payload/Orchard.app/Resources/", nil, 0o600}, zipEntry{}, []string{"archive_path_kind_mismatch"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			entries := append([]zipEntry{}, base...)
			if tc.first.name != "" {
				entries = append([]zipEntry{tc.first}, entries...)
			}
			if tc.last.name != "" {
				entries = append(entries, tc.last)
			}
			ipa := filepath.Join(t.TempDir(), "bad.ipa")
			writeIPA(t, ipa, entries)
			result, err := InspectIPA(context.Background(), ipa, InspectionOptions{CurrentTime: f.now})
			if err != nil {
				t.Fatal(err)
			}
			if result.Valid() {
				t.Fatalf("path-kind conflict accepted: %+v", result.Value)
			}
			for _, code := range tc.codes {
				if !hasProblem(result.Problems, code) {
					t.Fatalf("missing %s: %+v", code, result.Problems)
				}
			}
		})
	}
}

func TestInspectIPAAcceptsImplicitAndExplicitDirectories(t *testing.T) {
	f := newCryptoFixture(t)
	base := validIPAEntries(t, f.profile(t, profileOptions{}), false)
	for _, tc := range []struct {
		name  string
		extra []zipEntry
	}{
		{"implicit", nil},
		{"explicit", []zipEntry{
			{"Payload/", nil, os.ModeDir | 0o755},
			{"Payload/Orchard.app/", nil, os.ModeDir | 0o755},
			{"Payload/Orchard.app/Resources/", nil, os.ModeDir | 0o755},
			{"Payload/Orchard.app/Resources/value", []byte("value"), 0o600},
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ipa := filepath.Join(t.TempDir(), "valid.ipa")
			writeIPA(t, ipa, append(append([]zipEntry{}, tc.extra...), base...))
			result, err := InspectIPA(context.Background(), ipa, InspectionOptions{CurrentTime: f.now})
			if err != nil {
				t.Fatal(err)
			}
			if !result.Valid() || !result.Value.StructureValid {
				t.Fatalf("valid directory layout rejected: %+v", result.Problems)
			}
		})
	}
}

func TestExecutableMetadataIsValidatedBeforeBundleOrArchiveRead(t *testing.T) {
	malformed := []struct {
		name  string
		value any
	}{
		{"empty", ""},
		{"invalid type", int64(7)},
		{"absolute", "/outside"},
		{"dot", "."},
		{"dotdot", ".."},
		{"traversal", "../outside"},
		{"slash", "sub/Orchard"},
		{"backslash", `sub\Orchard`},
		{"nul", "Orchard\x00outside"},
		{"control", "Orchard\noutside"},
		{"volume syntax", "C:outside"},
	}
	f := newCryptoFixture(t)
	cms := f.profile(t, profileOptions{})
	baseEntries := validIPAEntries(t, cms, false)
	for _, tc := range malformed {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			app := writeBundle(t, dir, false)
			writePrivate(t, filepath.Join(dir, "outside"), fixtureMachO())
			if err := os.WriteFile(filepath.Join(app, "Info.plist"), fixtureInfoWith(t, "CFBundleExecutable", tc.value), 0o600); err != nil {
				t.Fatal(err)
			}
			bundle, err := InspectBundle(context.Background(), app, InspectionOptions{})
			if err != nil {
				t.Fatal(err)
			}
			if !hasProblem(bundle.Problems, "invalid_executable_name") || bundle.Value.MachO.LoadCommand != "" || hasProblem(bundle.Problems, "invalid_main_executable") {
				t.Fatalf("directory inspection did not reject before executable read: value=%q report=%+v", tc.value, bundle)
			}

			entries := append([]zipEntry{}, baseEntries...)
			for i := range entries {
				if entries[i].name == "Payload/Orchard.app/Info.plist" {
					entries[i].data = fixtureInfoWith(t, "CFBundleExecutable", tc.value)
				}
			}
			ipaPath := filepath.Join(dir, "bad.ipa")
			writeIPA(t, ipaPath, entries)
			ipa, err := InspectIPA(context.Background(), ipaPath, InspectionOptions{CurrentTime: f.now})
			if err != nil {
				t.Fatal(err)
			}
			if !hasProblem(ipa.Problems, "invalid_executable_name") || ipa.Value.Bundle.MachO.LoadCommand != "" || hasProblem(ipa.Problems, "invalid_main_executable") {
				t.Fatalf("IPA inspection did not reject before executable lookup: value=%q report=%+v", tc.value, ipa)
			}
		})
	}
}

func TestInvalidPrimaryIconNameDoesNotReadDerivedOutsidePath(t *testing.T) {
	dir := t.TempDir()
	app := writeBundle(t, dir, false)
	outside := filepath.Join(dir, "outside20x20@2x.png")
	writePrivate(t, outside, []byte("sentinel-not-a-png"))
	if err := os.WriteFile(filepath.Join(app, "Info.plist"), fixtureInfoWith(t, "CFBundleIconName", "../outside"), 0o600); err != nil {
		t.Fatal(err)
	}
	result, err := InspectBundle(context.Background(), app, InspectionOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if !hasProblem(result.Problems, "invalid_primary_icon_name") {
		t.Fatalf("missing invalid icon-name problem: %+v", result.Problems)
	}
	if hasProblem(result.Problems, "invalid_icon_png") || hasProblem(result.Problems, "missing_icon_file") {
		t.Fatalf("invalid icon stem was used for derived reads: %+v", result.Problems)
	}
}

func TestInspectIPARejectsUnsafePermissionsAndIconBytes(t *testing.T) {
	f := newCryptoFixture(t)
	for _, tc := range []struct {
		name, code string
		mutate     func([]zipEntry) []zipEntry
	}{
		{"non-executable main", "unsafe_archive_executable_mode", func(entries []zipEntry) []zipEntry {
			for i := range entries {
				if entries[i].name == "Payload/Orchard.app/Orchard" {
					entries[i].mode = 0o666
				}
			}
			return entries
		}},
		{"non-traversable directory", "unsafe_archive_directory_mode", func(entries []zipEntry) []zipEntry {
			return append(entries, zipEntry{"Payload/Orchard.app/Resources/", nil, os.ModeDir | 0o600})
		}},
		{"malformed icon", "invalid_icon_png", func(entries []zipEntry) []zipEntry {
			for i := range entries {
				if entries[i].name == "Payload/Orchard.app/AppIcon60x60@3x.png" {
					entries[i].data = []byte("not-png")
				}
			}
			return entries
		}},
		{"missing marketing icon", "missing_icon_file", func(entries []zipEntry) []zipEntry {
			var kept []zipEntry
			for _, entry := range entries {
				if entry.name != "Payload/Orchard.app/AppIcon1024x1024.png" {
					kept = append(kept, entry)
				}
			}
			return kept
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ipa := filepath.Join(t.TempDir(), "bad.ipa")
			writeIPA(t, ipa, tc.mutate(validIPAEntries(t, f.profile(t, profileOptions{}), false)))
			result, err := InspectIPA(context.Background(), ipa, InspectionOptions{CurrentTime: f.now})
			if err != nil {
				t.Fatal(err)
			}
			if result.Valid() || !hasProblem(result.Problems, tc.code) {
				t.Fatalf("missing %s: %+v", tc.code, result.Problems)
			}
		})
	}
}

func TestInspectIPARejectsSuspiciousCompressionAndLimits(t *testing.T) {
	f := newCryptoFixture(t)
	cms := f.profile(t, profileOptions{})
	dir := t.TempDir()
	entries := validIPAEntries(t, cms, false)
	entries = append(entries, zipEntry{"Payload/Orchard.app/zeros", make([]byte, 1<<20), 0o600})
	ipa := filepath.Join(dir, "ratio.ipa")
	writeIPA(t, ipa, entries)
	result, err := InspectIPA(context.Background(), ipa, InspectionOptions{CurrentTime: f.now, Limits: Limits{MaxCompressionRatio: 10}})
	if err != nil {
		t.Fatal(err)
	}
	if !hasProblem(result.Problems, "suspicious_compression_ratio") {
		t.Fatalf("ratio not rejected: %+v", result.Problems)
	}
	if _, err := InspectIPA(context.Background(), ipa, InspectionOptions{Limits: Limits{MaxIPABytes: 10}}); err == nil || !strings.Contains(err.Error(), "limit") {
		t.Fatalf("oversize error=%v", err)
	}
	if _, err := InspectIPA(context.Background(), ipa, InspectionOptions{Limits: Limits{MaxArchiveEntries: 2}}); err == nil || !strings.Contains(err.Error(), "entries") {
		t.Fatalf("entry limit error=%v", err)
	}
}

func TestInspectIPAMalformedAndCanceled(t *testing.T) {
	dir := t.TempDir()
	malformed := filepath.Join(dir, "bad.ipa")
	writePrivate(t, malformed, []byte("not a zip"))
	if _, err := InspectIPA(context.Background(), malformed, InspectionOptions{}); err == nil {
		t.Fatal("malformed IPA accepted")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := InspectIPA(ctx, malformed, InspectionOptions{}); err == nil {
		t.Fatal("canceled inspection accepted")
	}
}

func TestInspectBundleAndRejectSymlink(t *testing.T) {
	dir := t.TempDir()
	app := writeBundle(t, dir, true)
	result, err := InspectBundle(context.Background(), app, InspectionOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Valid() || result.Value.MachO.Platform != "iOS" {
		t.Fatalf("bundle rejected: %+v", result)
	}
	if err := os.Symlink(filepath.Join(app, "Info.plist"), filepath.Join(app, "linked")); err != nil {
		t.Fatal(err)
	}
	if _, err := InspectBundle(context.Background(), app, InspectionOptions{}); err == nil || !strings.Contains(err.Error(), "nonregular") {
		t.Fatalf("symlink error=%v", err)
	}
}

func TestMachOSupportedLegacyAndRejectsWrongArchitecture(t *testing.T) {
	legacy := new(bytes.Buffer)
	for _, v := range []uint32{0xfeedfacf, 0x0100000c, 0, 2, 1, 16, 0, 0, lcVersionMinIPhoneOS, 16, 17 << 16, 26 << 16} {
		if err := binary.Write(legacy, binary.LittleEndian, v); err != nil {
			t.Fatal(err)
		}
	}
	report, err := inspectMachO(legacy.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	if report.LoadCommand != "LC_VERSION_MIN_IPHONEOS" || report.MinimumOS != "17.0.0" {
		t.Fatalf("legacy report=%+v", report)
	}
	wrong := append([]byte{}, fixtureMachO()...)
	binary.LittleEndian.PutUint32(wrong[4:8], 0x01000007)
	if _, err := inspectMachO(wrong); err == nil || !strings.Contains(err.Error(), "want arm64") {
		t.Fatalf("wrong-architecture error=%v", err)
	}
}
