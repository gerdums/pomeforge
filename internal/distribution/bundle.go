package distribution

import (
	"archive/zip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"howett.net/plist"
)

// InspectionOptions controls bounded IPA and embedded-profile inspection.
type InspectionOptions struct {
	Limits           Limits
	TrustedRootPaths []string
	CurrentTime      time.Time
}

// BundleReport contains typed app facts. SDKMetadata includes only the
// provenance fields actually present in Info.plist.
type BundleReport struct {
	AppDirectory     string              `json:"appDirectory"`
	BundleIdentifier string              `json:"bundleIdentifier"`
	MarketingVersion string              `json:"marketingVersion"`
	BuildNumber      string              `json:"buildNumber"`
	Executable       string              `json:"executable"`
	DeviceFamilies   []int               `json:"deviceFamilies"`
	MinimumOSVersion string              `json:"minimumOSVersion"`
	SDKMetadata      map[string]string   `json:"sdkMetadata"`
	IconFiles        []string            `json:"iconFiles"`
	MachO            MachOReport         `json:"machO"`
	CodeSignature    CodeSignatureReport `json:"codeSignature"`
	EmbeddedProfile  *ProfileReport      `json:"embeddedProfile,omitempty"`
}

// CodeSignatureReport deliberately distinguishes structural presence from
// cryptographic verification, which this package does not claim.
type CodeSignatureReport struct {
	CodeResourcesPresent      bool   `json:"codeResourcesPresent"`
	CryptographicallyVerified bool   `json:"cryptographicallyVerified"`
	Verification              string `json:"verification"`
}

// IPAReport contains archive facts; StructureValid is local validation only
// and never means Apple has processed or accepted the archive.
type IPAReport struct {
	SHA256           string       `json:"sha256"`
	Size             int64        `json:"size"`
	EntryCount       int          `json:"entryCount"`
	UncompressedSize uint64       `json:"uncompressedSize"`
	StructureValid   bool         `json:"structureValid"`
	Bundle           BundleReport `json:"bundle"`
}

func decodeInfo(data []byte) (map[string]any, error) {
	var info map[string]any
	if _, err := plist.Unmarshal(data, &info); err != nil {
		return nil, fmt.Errorf("decode Info.plist: %w", err)
	}
	if info == nil {
		return nil, errors.New("Info.plist is not a dictionary")
	}
	return info, nil
}

func plistString(info map[string]any, key string) string {
	v, _ := info[key].(string)
	return v
}

func plistInts(v any) []int {
	var out []int
	switch a := v.(type) {
	case []any:
		for _, value := range a {
			switch n := value.(type) {
			case uint64:
				out = append(out, int(n))
			case int64:
				out = append(out, int(n))
			case int:
				out = append(out, n)
			}
		}
	case []uint64:
		for _, n := range a {
			out = append(out, int(n))
		}
	case []int64:
		for _, n := range a {
			out = append(out, int(n))
		}
	case []int:
		out = append(out, a...)
	}
	return out
}

func collectIconNames(info map[string]any) []string {
	seen := map[string]bool{}
	var walk func(any)
	walk = func(v any) {
		switch x := v.(type) {
		case map[string]any:
			for k, child := range x {
				if k == "CFBundleIconFiles" {
					switch names := child.(type) {
					case []any:
						for _, n := range names {
							if s, ok := n.(string); ok && s != "" {
								seen[s] = true
							}
						}
					case []string:
						for _, s := range names {
							if s != "" {
								seen[s] = true
							}
						}
					}
				}
				walk(child)
			}
		case []any:
			for _, child := range x {
				walk(child)
			}
		}
	}
	walk(info)
	out := make([]string, 0, len(seen))
	for name := range seen {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

func iconNameMatches(metadataName, fileName string) bool {
	base := strings.TrimSuffix(metadataName, ".png")
	return strings.HasSuffix(fileName, ".png") && (strings.TrimSuffix(fileName, ".png") == base || strings.HasPrefix(strings.TrimSuffix(fileName, ".png"), base+"@") || strings.HasPrefix(strings.TrimSuffix(fileName, ".png"), base+"~"))
}

func validateArchivedIconFiles(entries map[string]*zip.File, appRoot string, names []string) []Problem {
	var problems []Problem
	for _, icon := range names {
		found := false
		for entry := range entries {
			rel := strings.TrimPrefix(entry, appRoot+"/")
			if rel != entry && !strings.Contains(rel, "/") && iconNameMatches(icon, rel) {
				found = true
				break
			}
		}
		if !found {
			problems = append(problems, problem("missing_icon_file", "CFBundleIconFiles", fmt.Sprintf("compiled icon metadata %q has no matching top-level PNG", icon)))
		}
	}
	return problems
}

func validateDirectoryIconFiles(root string, names []string) []Problem {
	entries, err := os.ReadDir(root)
	if err != nil {
		return []Problem{problem("read_bundle_icons", "CFBundleIconFiles", err.Error())}
	}
	var problems []Problem
	for _, icon := range names {
		found := false
		for _, entry := range entries {
			if !entry.IsDir() && iconNameMatches(icon, entry.Name()) {
				found = true
				break
			}
		}
		if !found {
			problems = append(problems, problem("missing_icon_file", "CFBundleIconFiles", fmt.Sprintf("compiled icon metadata %q has no matching top-level PNG", icon)))
		}
	}
	return problems
}

var sdkInfoKeys = []string{
	"DTPlatformName", "DTPlatformVersion", "DTPlatformBuild", "DTSDKName",
	"DTSDKBuild", "DTXcode", "DTXcodeBuild",
}

func reportFromInfo(info map[string]any) BundleReport {
	r := BundleReport{
		BundleIdentifier: plistString(info, "CFBundleIdentifier"),
		MarketingVersion: plistString(info, "CFBundleShortVersionString"),
		BuildNumber:      plistString(info, "CFBundleVersion"),
		Executable:       plistString(info, "CFBundleExecutable"),
		MinimumOSVersion: plistString(info, "MinimumOSVersion"),
		DeviceFamilies:   plistInts(info["UIDeviceFamily"]),
		IconFiles:        collectIconNames(info), SDKMetadata: map[string]string{},
	}
	for _, key := range sdkInfoKeys {
		if value := plistString(info, key); value != "" {
			r.SDKMetadata[key] = value
		}
	}
	return r
}

func validateBundleReport(r BundleReport) []Problem {
	var ps []Problem
	required := []struct{ value, field, code string }{
		{r.BundleIdentifier, "CFBundleIdentifier", "missing_bundle_identifier"},
		{r.MarketingVersion, "CFBundleShortVersionString", "missing_marketing_version"},
		{r.BuildNumber, "CFBundleVersion", "missing_build_number"},
		{r.Executable, "CFBundleExecutable", "missing_executable"},
		{r.MinimumOSVersion, "MinimumOSVersion", "missing_minimum_os"},
	}
	for _, req := range required {
		if req.value == "" {
			ps = append(ps, problem(req.code, req.field, req.field+" is required"))
		}
	}
	hasPhone, hasPad := false, false
	for _, family := range r.DeviceFamilies {
		hasPhone = hasPhone || family == 1
		hasPad = hasPad || family == 2
	}
	if !hasPhone || !hasPad {
		ps = append(ps, problem("incomplete_device_families", "UIDeviceFamily", "UIDeviceFamily must explicitly include iPhone (1) and iPad (2)"))
	}
	for _, key := range sdkInfoKeys {
		if r.SDKMetadata[key] == "" {
			ps = append(ps, problem("missing_sdk_provenance", key, key+" is required and must come from the actual toolchain/SDK"))
		}
	}
	if platform := r.SDKMetadata["DTPlatformName"]; platform != "" && platform != "iphoneos" {
		ps = append(ps, problem("sdk_metadata_mismatch", "DTPlatformName", "DTPlatformName must be iphoneos for an iOS app"))
	}
	if r.SDKMetadata["DTPlatformBuild"] != "" && r.SDKMetadata["DTSDKBuild"] != "" && r.SDKMetadata["DTPlatformBuild"] != r.SDKMetadata["DTSDKBuild"] {
		ps = append(ps, problem("sdk_metadata_mismatch", "DTSDKBuild", "DTSDKBuild does not match DTPlatformBuild"))
	}
	if r.MachO.SDKVersion != "" {
		sdkName := strings.TrimPrefix(r.SDKMetadata["DTSDKName"], "iphoneos")
		if sdkName != "" && !versionsEquivalent(sdkName, r.MachO.SDKVersion) {
			ps = append(ps, problem("sdk_metadata_mismatch", "DTSDKName", "DTSDKName does not match the SDK version encoded in the main executable"))
		}
		if version := r.SDKMetadata["DTPlatformVersion"]; version != "" && !versionsEquivalent(version, r.MachO.SDKVersion) {
			ps = append(ps, problem("sdk_metadata_mismatch", "DTPlatformVersion", "DTPlatformVersion does not match the SDK version encoded in the main executable"))
		}
	}
	if r.MachO.MinimumOS != "" && r.MinimumOSVersion != "" && !versionsEquivalent(r.MinimumOSVersion, r.MachO.MinimumOS) {
		ps = append(ps, problem("minimum_os_mismatch", "MinimumOSVersion", "MinimumOSVersion does not match the main executable deployment target"))
	}
	if r.MachO.Platform == "" {
		ps = append(ps, problem("missing_macho_platform", "CFBundleExecutable", "main executable lacks supported arm64 iOS platform metadata"))
	}
	if len(r.IconFiles) == 0 {
		ps = append(ps, problem("missing_icon_metadata", "CFBundleIcons", "Info.plist contains no compiled icon file metadata"))
	}
	return ps
}

func versionsEquivalent(a, b string) bool {
	trim := func(v string) string {
		parts := strings.Split(v, ".")
		for len(parts) > 1 && parts[len(parts)-1] == "0" {
			parts = parts[:len(parts)-1]
		}
		return strings.Join(parts, ".")
	}
	return trim(a) == trim(b)
}

func safeArchiveName(name string) error {
	if name == "" || strings.Contains(name, "\\") || strings.IndexByte(name, 0) >= 0 {
		return errors.New("archive name is empty or contains backslash/NUL")
	}
	if path.IsAbs(name) || strings.HasPrefix(name, "/") {
		return errors.New("archive name is absolute")
	}
	trimmed := strings.TrimSuffix(name, "/")
	if trimmed == "" || path.Clean(trimmed) != trimmed || strings.HasPrefix(trimmed, "../") || trimmed == ".." {
		return errors.New("archive name traverses or is not clean")
	}
	return nil
}

func readZipEntry(ctx context.Context, f *zip.File, max int64) ([]byte, error) {
	if f.UncompressedSize64 > uint64(max) {
		return nil, fmt.Errorf("entry exceeds %d-byte limit", max)
	}
	r, err := f.Open()
	if err != nil {
		return nil, err
	}
	defer r.Close()
	return readBounded(ctx, r, max)
}

func hashRegularFile(ctx context.Context, file *os.File, max int64) (string, int64, error) {
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return "", 0, err
	}
	h := sha256.New()
	lr := &io.LimitedReader{R: file, N: max + 1}
	buf := make([]byte, 64<<10)
	var n int64
	for {
		if err := checkContext(ctx); err != nil {
			return "", 0, err
		}
		read, err := lr.Read(buf)
		if read > 0 {
			_, _ = h.Write(buf[:read])
			n += int64(read)
			if n > max {
				return "", n, fmt.Errorf("IPA exceeds %d-byte limit", max)
			}
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", n, err
		}
	}
	return hex.EncodeToString(h.Sum(nil)), n, nil
}

// InspectIPA performs bounded archive, plist, Mach-O, profile, and metadata
// inspection without extracting entries to disk.
func InspectIPA(ctx context.Context, ipaPath string, options InspectionOptions) (Result[IPAReport], error) {
	limits := options.Limits.withDefaults()
	var out Result[IPAReport]
	if options.CurrentTime.IsZero() {
		options.CurrentTime = time.Now()
	}
	if err := requireAbsoluteCleanPath("ipaPath", ipaPath); err != nil {
		return out, err
	}
	if err := rejectSymlinkPath(ipaPath, true); err != nil {
		return out, err
	}
	info, err := os.Lstat(ipaPath)
	if err != nil {
		return out, err
	}
	if !info.Mode().IsRegular() {
		return out, errors.New("IPA input is not a regular file")
	}
	if info.Size() > limits.MaxIPABytes {
		return out, fmt.Errorf("IPA is %d bytes; limit is %d", info.Size(), limits.MaxIPABytes)
	}
	f, err := os.Open(ipaPath)
	if err != nil {
		return out, err
	}
	defer f.Close()
	openedInfo, err := f.Stat()
	if err != nil {
		return out, err
	}
	if !openedInfo.Mode().IsRegular() || !os.SameFile(info, openedInfo) {
		return out, errors.New("IPA changed during safe open")
	}
	out.Value.SHA256, out.Value.Size, err = hashRegularFile(ctx, f, limits.MaxIPABytes)
	if err != nil {
		return out, err
	}
	zr, err := zip.NewReader(f, info.Size())
	if err != nil {
		return out, fmt.Errorf("open IPA zip: %w", err)
	}
	out.Value.EntryCount = len(zr.File)
	if len(zr.File) > limits.MaxArchiveEntries {
		return out, fmt.Errorf("IPA has %d entries; limit is %d", len(zr.File), limits.MaxArchiveEntries)
	}
	entries := map[string]*zip.File{}
	apps := map[string]bool{}
	for _, zf := range zr.File {
		if err := checkContext(ctx); err != nil {
			return out, err
		}
		if err := safeArchiveName(zf.Name); err != nil {
			out.Problems = append(out.Problems, problem("unsafe_archive_name", zf.Name, err.Error()))
			continue
		}
		if _, exists := entries[zf.Name]; exists {
			out.Problems = append(out.Problems, problem("duplicate_archive_entry", zf.Name, "duplicate archive entry"))
			continue
		}
		entries[zf.Name] = zf
		mode := zf.Mode()
		if !mode.IsDir() && !mode.IsRegular() {
			out.Problems = append(out.Problems, problem("unsafe_archive_entry_type", zf.Name, "archive entries must be regular files or directories"))
		}
		if zf.UncompressedSize64 > uint64(limits.MaxFileBytes) {
			out.Problems = append(out.Problems, problem("archive_entry_too_large", zf.Name, "archive entry exceeds per-file limit"))
		}
		if zf.UncompressedSize64 > 0 && (zf.CompressedSize64 == 0 || zf.UncompressedSize64/zf.CompressedSize64 > limits.MaxCompressionRatio) {
			out.Problems = append(out.Problems, problem("suspicious_compression_ratio", zf.Name, "archive entry has a suspicious compression ratio"))
		}
		if ^uint64(0)-out.Value.UncompressedSize < zf.UncompressedSize64 {
			return out, errors.New("IPA uncompressed size overflow")
		}
		out.Value.UncompressedSize += zf.UncompressedSize64
		if out.Value.UncompressedSize > uint64(limits.MaxBundleBytes) {
			out.Problems = append(out.Problems, problem("archive_expansion_too_large", "ipa", "archive uncompressed content exceeds limit"))
		}
		parts := strings.Split(strings.TrimSuffix(zf.Name, "/"), "/")
		if len(parts) >= 2 && parts[0] == "Payload" && strings.HasSuffix(parts[1], ".app") {
			apps["Payload/"+parts[1]] = true
		}
	}
	if len(apps) != 1 {
		out.Problems = append(out.Problems, problem("main_bundle_count", "Payload", fmt.Sprintf("IPA must contain exactly one main Payload/*.app; found %d", len(apps))))
		return out, nil
	}
	var appRoot string
	for appRoot = range apps {
	}
	out.Value.Bundle.AppDirectory = appRoot
	for name := range entries {
		if strings.HasPrefix(name, appRoot+"/PlugIns/") || strings.HasPrefix(name, appRoot+"/Extensions/") || strings.Contains(name, ".appex/") {
			out.Problems = append(out.Problems, problem("unsupported_nested_extension", name, "app extensions require separately matched profiles and are unsupported"))
			break
		}
	}
	infoEntry := entries[appRoot+"/Info.plist"]
	if infoEntry == nil {
		out.Problems = append(out.Problems, problem("missing_info_plist", appRoot, "main bundle has no Info.plist"))
		return out, nil
	}
	infoData, err := readZipEntry(ctx, infoEntry, limits.MaxFileBytes)
	if err != nil {
		return out, err
	}
	infoPlist, err := decodeInfo(infoData)
	if err != nil {
		out.Problems = append(out.Problems, problem("invalid_info_plist", "Info.plist", err.Error()))
		return out, nil
	}
	out.Value.Bundle = reportFromInfo(infoPlist)
	out.Value.Bundle.AppDirectory = appRoot
	out.Value.Bundle.CodeSignature = CodeSignatureReport{CodeResourcesPresent: entries[appRoot+"/_CodeSignature/CodeResources"] != nil, Verification: "not_performed"}
	if out.Value.Bundle.Executable != "" {
		execEntry := entries[appRoot+"/"+out.Value.Bundle.Executable]
		if execEntry == nil {
			out.Problems = append(out.Problems, problem("missing_main_executable", "CFBundleExecutable", "declared main executable is absent"))
		} else {
			execData, readErr := readZipEntry(ctx, execEntry, limits.MaxFileBytes)
			if readErr != nil {
				return out, readErr
			}
			out.Value.Bundle.MachO, readErr = inspectMachO(execData)
			if readErr != nil {
				out.Problems = append(out.Problems, problem("invalid_main_executable", "CFBundleExecutable", readErr.Error()))
			}
		}
	}
	if profileEntry := entries[appRoot+"/embedded.mobileprovision"]; profileEntry != nil {
		profileData, readErr := readZipEntry(ctx, profileEntry, limits.MaxProfileBytes)
		if readErr != nil {
			return out, readErr
		}
		profileReport, _, profileErr := inspectProfileData(ctx, profileData, options.TrustedRootPaths, options.CurrentTime, limits)
		out.Value.Bundle.EmbeddedProfile = &profileReport
		if profileErr != nil {
			out.Problems = append(out.Problems, problem("invalid_embedded_profile", "embedded.mobileprovision", profileErr.Error()))
		}
	} else {
		out.Problems = append(out.Problems, problem("missing_embedded_profile", appRoot, "signed IPA has no embedded.mobileprovision"))
	}
	out.Problems = append(out.Problems, validateBundleReport(out.Value.Bundle)...)
	out.Problems = append(out.Problems, validateArchivedIconFiles(entries, appRoot, out.Value.Bundle.IconFiles)...)
	out.Value.StructureValid = out.Valid()
	return out, nil
}

func inspectBundleDirectory(ctx context.Context, root string, limits Limits) (Result[BundleReport], error) {
	var out Result[BundleReport]
	infoData, err := readRegularFile(ctx, filepath.Join(root, "Info.plist"), limits.MaxFileBytes)
	if err != nil {
		return out, err
	}
	info, err := decodeInfo(infoData)
	if err != nil {
		return out, err
	}
	out.Value = reportFromInfo(info)
	out.Value.AppDirectory = filepath.Base(root)
	if signatureInfo, err := os.Lstat(filepath.Join(root, "_CodeSignature", "CodeResources")); err == nil && signatureInfo.Mode().IsRegular() {
		out.Value.CodeSignature.CodeResourcesPresent = true
	}
	out.Value.CodeSignature.Verification = "not_performed"
	if out.Value.Executable != "" {
		execData, err := readRegularFile(ctx, filepath.Join(root, out.Value.Executable), limits.MaxFileBytes)
		if err != nil {
			out.Problems = append(out.Problems, problem("missing_main_executable", "CFBundleExecutable", err.Error()))
		} else {
			out.Value.MachO, err = inspectMachO(execData)
			if err != nil {
				out.Problems = append(out.Problems, problem("invalid_main_executable", "CFBundleExecutable", err.Error()))
			}
		}
	}
	out.Problems = append(out.Problems, validateBundleReport(out.Value)...)
	out.Problems = append(out.Problems, validateDirectoryIconFiles(root, out.Value.IconFiles)...)
	return out, nil
}

// InspectBundle performs bounded, non-mutating inspection of a real .app
// directory and rejects symlinks, special files, and unsupported nested
// signing topology.
func InspectBundle(ctx context.Context, bundlePath string, options InspectionOptions) (Result[BundleReport], error) {
	limits := options.Limits.withDefaults()
	var out Result[BundleReport]
	if options.CurrentTime.IsZero() {
		options.CurrentTime = time.Now()
	}
	if err := requireAbsoluteCleanPath("bundlePath", bundlePath); err != nil {
		return out, err
	}
	if err := rejectSymlinkPath(bundlePath, true); err != nil {
		return out, err
	}
	root, err := os.Lstat(bundlePath)
	if err != nil {
		return out, err
	}
	if !root.IsDir() || !strings.HasSuffix(bundlePath, ".app") {
		return out, errors.New("bundle input must be a real .app directory")
	}
	entries := 0
	var total int64
	err = filepath.WalkDir(bundlePath, func(name string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := checkContext(ctx); err != nil {
			return err
		}
		if name == bundlePath {
			return nil
		}
		entries++
		if entries > limits.MaxArchiveEntries {
			return fmt.Errorf("bundle exceeds %d-entry limit", limits.MaxArchiveEntries)
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 || (!info.IsDir() && !info.Mode().IsRegular()) {
			return fmt.Errorf("bundle contains nonregular entry %q", name)
		}
		if info.Mode().IsRegular() {
			if info.Size() > limits.MaxFileBytes {
				return fmt.Errorf("bundle file exceeds per-file limit")
			}
			total += info.Size()
			if total > limits.MaxBundleBytes {
				return fmt.Errorf("bundle exceeds size limit")
			}
		}
		rel, _ := filepath.Rel(bundlePath, name)
		slash := filepath.ToSlash(rel)
		if strings.HasPrefix(slash, "PlugIns/") || strings.HasPrefix(slash, "Extensions/") || strings.HasPrefix(slash, "Frameworks/") || strings.HasPrefix(slash, "Watch/") || strings.Contains(slash, ".appex/") || strings.Contains(slash, ".framework/") {
			out.Problems = append(out.Problems, problem("unsupported_signing_topology", rel, "extensions/frameworks require separate signing identities and profiles"))
		}
		return nil
	})
	if err != nil {
		return out, err
	}
	parsed, err := inspectBundleDirectory(ctx, bundlePath, limits)
	if err != nil {
		return out, err
	}
	out.Value = parsed.Value
	out.Problems = append(out.Problems, parsed.Problems...)
	profilePath := filepath.Join(bundlePath, "embedded.mobileprovision")
	if _, err := os.Lstat(profilePath); err == nil {
		data, err := readRegularFile(ctx, profilePath, limits.MaxProfileBytes)
		if err != nil {
			return out, err
		}
		report, _, profileErr := inspectProfileData(ctx, data, options.TrustedRootPaths, options.CurrentTime, limits)
		out.Value.EmbeddedProfile = &report
		if profileErr != nil {
			out.Problems = append(out.Problems, problem("invalid_embedded_profile", "embedded.mobileprovision", profileErr.Error()))
		}
	} else if !os.IsNotExist(err) {
		return out, err
	}
	return out, nil
}
