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
		{"nested extension", "unsupported_nested_extension", []zipEntry{{"Payload/Orchard.app/PlugIns/Widget.appex/Info.plist", []byte("x"), 0o600}}, false},
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
