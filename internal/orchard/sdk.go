package orchard

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const (
	maxSDKInputBytes    = int64(128 << 30)
	maxSDKMetadataBytes = int64(2 << 20)
	maxSDKTreeEntries   = 2_000_000
)

type sdkMetadata struct {
	Values  map[string]string
	Sources map[string]string
	Hashes  map[string]string
	Missing []string
}

const sdkTargetTriple = "arm64-apple-ios"

func (m *SetupManager) planSDKImport(ctx context.Context, input PlanInput, hostArch string, plan *Plan) (string, error) {
	if input.Arch != "arm64" && input.Arch != "x86_64" {
		return "", Errorf("invalid_arch", "--arch must be arm64 or x86_64")
	}
	if hostArch == "amd64" {
		hostArch = "x86_64"
	}
	if hostArch != input.Arch {
		plan.Blockers = append(plan.Blockers, fmt.Sprintf("SDK architecture %s does not match this Linux host architecture %s", input.Arch, hostArch))
	}
	absolute, err := filepath.Abs(input.InputPath)
	if err != nil || filepath.Clean(absolute) != absolute {
		return "", Errorf("invalid_path", "SDK input must resolve to a clean absolute path")
	}
	kind, inputDigest, metadata, err := inspectSDKInput(ctx, absolute)
	if err != nil {
		return "", err
	}
	reservation := reservationToken(input.reservation)
	if reservation == "" {
		return "", errors.New("SDK import plan is missing a private reservation")
	}
	stageRoot := filepath.Join(m.StateDir, "sdk-imports", reservation)
	if _, err := os.Lstat(stageRoot); err == nil {
		plan.Blockers = append(plan.Blockers, "reserved SDK staging path already exists; request a fresh plan")
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	xtool := canonicalToolStatus(m.Tools.Probe(ctx, "xtool"))
	if xtool.Status != "available" {
		plan.Blockers = append(plan.Blockers, "xtool is "+xtool.Status+": "+xtool.Detail)
	}
	var xtoolDigest string
	if xtool.Path != "" {
		xtoolDigest, err = hashExecutableForPlan(ctx, xtool.Path)
		if err != nil {
			return "", err
		}
	}
	swift := canonicalToolStatus(m.Tools.Probe(ctx, "swift"))
	if swift.Status != "available" {
		plan.Blockers = append(plan.Blockers, "Swift 6.3 or later is "+swift.Status+": "+swift.Detail)
	}
	var swiftDigest string
	if swift.Path != "" {
		swiftDigest, err = hashExecutableForPlan(ctx, swift.Path)
		if err != nil {
			return "", err
		}
	}
	configHome, err := m.sdkConfigHome()
	if err != nil {
		return "", Errorf("invalid_environment", err.Error())
	}
	clang := canonicalToolStatus(m.Tools.Probe(ctx, "clang"))
	if clang.Status != "available" {
		plan.Blockers = append(plan.Blockers, "Clang is "+clang.Status+": "+clang.Detail)
	}
	var clangDigest, clangResourceDir, clangHeadersDigest string
	if clang.Path != "" {
		clangDigest, err = hashExecutableForPlan(ctx, clang.Path)
		if err != nil {
			return "", err
		}
	}
	if clang.Status == "available" {
		clangResourceDir, clangHeadersDigest, err = m.inspectClangResources(ctx, clang.Path)
		if err != nil {
			plan.Blockers = append(plan.Blockers, "Clang resource headers could not be verified: "+safeSetupError(err))
		}
	}
	statusOutput := ""
	if xtool.Status == "available" {
		statusCode := 0
		var statusErr error
		statusOutput, statusCode, statusErr = m.runStatusProbe(ctx, xtool.Path, m.workingDirectory())
		installed, recognized := parseSDKStatus(statusOutput)
		if statusErr != nil || statusCode != 0 {
			plan.Blockers = append(plan.Blockers, "xtool sdk status could not be inspected before import")
		} else if !recognized {
			plan.Blockers = append(plan.Blockers, "xtool sdk status output was not recognized; refusing a potentially destructive replacement")
		} else if installed {
			plan.Blockers = append(plan.Blockers, "a Darwin SDK is already installed; Orchard refuses to replace it")
		}
	}
	extractionPath := filepath.Join(stageRoot, "xip-extracted")
	buildParent := filepath.Join(stageRoot, "sdk-build")
	artifactBundle := filepath.Join(stageRoot, "darwin.artifactbundle")
	xcodePath := absolute
	var helperDigest string
	if kind == "xip" {
		helper := canonicalToolStatus(m.Tools.Probe(ctx, "unxip"))
		if helper.Status != "available" {
			plan.Blockers = append(plan.Blockers, "unxip is "+helper.Status+": "+helper.Detail)
		} else {
			helperDigest, err = hashExecutableForPlan(ctx, helper.Path)
			if err != nil {
				return "", err
			}
		}
		plan.Steps = append(plan.Steps, Step{Kind: "process", Tool: "unxip", Executable: helper.Path, Args: []string{"--statistics", absolute, extractionPath}, Directory: stageRoot, Description: "Extract the operator-supplied XIP into a fresh private directory without modifying the original archive."})
		xcodePath = filepath.Join(extractionPath, "<discovered-Xcode.app>")
	}
	plan.Steps = append(plan.Steps,
		Step{Kind: "process", Tool: "xtool", Executable: xtool.Path, Args: []string{"sdk", "build", xcodePath, buildParent, "--arch", input.Arch}, Directory: stageRoot, Description: "Build darwin.xtoolsdk in a new output parent using xtool 1.19's exact sdk build contract."},
		Step{Kind: "process", Tool: "clang", Executable: clang.Path, Args: []string{"-print-resource-dir"}, Directory: stageRoot, Description: "Resolve Clang's resource directory from the same effective PATH used for the SDK build."},
		Step{Kind: "internal", Tool: "orchard", Operation: "stage-sdk-artifactbundle", Parameters: map[string]string{"source": filepath.Join(buildParent, "darwin.xtoolsdk"), "destination": artifactBundle, "clangHeaders": filepath.Join(clangResourceDir, "include"), "clangHeadersSha256": clangHeadersDigest}, Description: "Copy the built SDK into a fresh user-owned artifact bundle and replace its Clang headers from the selected host Clang resource tree."},
		Step{Kind: "internal", Tool: "orchard", Operation: "refuse-sdk-replacement", Parameters: map[string]string{"statusCommand": "xtool sdk status"}, Description: "Recheck actual SDK status immediately before installation and refuse replacement if a Darwin SDK is installed."},
		Step{Kind: "process", Tool: "swift", Executable: swift.Path, Args: []string{"sdk", "install", artifactBundle}, Directory: stageRoot, Description: "Install the fresh user-owned artifact bundle with the selected Swift executable and explicit private XDG_CONFIG_HOME."},
		Step{Kind: "process", Tool: "xtool", Executable: xtool.Path, Args: []string{"sdk", "status"}, Directory: stageRoot, Description: "Verify that xtool reports an actually installed SDK; process exit zero alone is insufficient."},
		Step{Kind: "process", Tool: "swift", Executable: swift.Path, Args: []string{"sdk", "list"}, Directory: stageRoot, Description: "Verify that the selected Swift installation lists the Darwin SDK in the same private XDG_CONFIG_HOME."},
		Step{Kind: "process", Tool: "swift", Executable: swift.Path, Args: []string{"sdk", "configure", "darwin", "arm64-apple-ios", "--show-configuration"}, Directory: stageRoot, Description: "Resolve Swift's active arm64-apple-ios SDK root for the installed Darwin SDK."},
	)
	plan.SDKImport = &SDKImportPlan{InputPath: absolute, InputKind: kind, InputSHA256: inputDigest, Trust: "operator_supplied", Arch: input.Arch, StageRoot: stageRoot, Metadata: metadata.Values, Sources: metadata.Sources, Missing: metadata.Missing, MetadataHashes: metadata.Hashes, XDGConfigHome: configHome, SwiftExecutable: swift.Path, SwiftSHA256: swiftDigest, ClangExecutable: clang.Path, ClangSHA256: clangDigest, ClangResourceDir: clangResourceDir, ClangHeadersSHA256: clangHeadersDigest}
	plan.Warnings = append(plan.Warnings,
		"The Apple input is operator supplied and is never downloaded by Orchard.",
		"A SHA-256 match or successful XIP extraction does not authenticate Apple's XIP signature or establish license permission.",
		"Importing an SDK does not prove an iOS build, physical-device operation, signing validity, or App Store acceptance.",
	)
	metadataJSON, _ := json.Marshal(metadata)
	return digestStrings("sdk-import", absolute, kind, inputDigest, input.Arch, stageRoot, configHome, xtool.Path, xtool.CanonicalPath, xtool.Version, xtoolDigest, swift.Path, swift.CanonicalPath, swift.Version, swiftDigest, clang.Path, clang.CanonicalPath, clang.Version, clangDigest, clangResourceDir, clangHeadersDigest, helperDigest, strings.Join(strings.Fields(statusOutput), " "), string(metadataJSON)), nil
}

func reservationToken(value string) string {
	_, token, found := strings.Cut(value, "\x00")
	if !found || token == "" {
		return ""
	}
	return token
}

func inspectSDKInput(ctx context.Context, path string) (string, string, sdkMetadata, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return "", "", sdkMetadata{}, Errorf("invalid_sdk_input", err.Error())
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return "", "", sdkMetadata{}, Errorf("invalid_sdk_input", "SDK input may not be a symbolic link")
	}
	if info.Mode().IsRegular() && strings.EqualFold(filepath.Ext(path), ".xip") {
		digest, err := hashLargeRegularFile(ctx, path, maxSDKInputBytes)
		metadata := sdkMetadata{Values: map[string]string{}, Sources: map[string]string{}, Hashes: map[string]string{}, Missing: []string{"Xcode and SDK metadata are unavailable until the full XIP is extracted; missing fields remain unset."}}
		return "xip", digest, metadata, err
	}
	if !info.IsDir() || !strings.EqualFold(filepath.Ext(path), ".app") {
		return "", "", sdkMetadata{}, Errorf("invalid_sdk_input", "SDK input must be an extracted Xcode.app directory or a regular .xip file")
	}
	digest, err := hashSDKTree(ctx, path)
	if err != nil {
		return "", "", sdkMetadata{}, Errorf("invalid_sdk_input", err.Error())
	}
	metadata, err := inspectXcodeMetadata(path)
	if err != nil {
		return "", "", sdkMetadata{}, Errorf("invalid_sdk_input", err.Error())
	}
	return "xcode_app", digest, metadata, nil
}

func hashLargeRegularFile(ctx context.Context, path string, limit int64) (string, error) {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return "", errors.New("SDK archive must be a regular file")
	}
	if info.Size() < 0 || info.Size() > limit {
		return "", fmt.Errorf("SDK archive exceeds the %d-byte hashing limit", limit)
	}
	file, err := openRegularAbsolute(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	hash := sha256.New()
	buffer := make([]byte, 1024*1024)
	var total int64
	for {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		n, readErr := file.Read(buffer)
		if n > 0 {
			total += int64(n)
			if total > limit {
				return "", errors.New("SDK archive grew beyond its hashing limit")
			}
			_, _ = hash.Write(buffer[:n])
		}
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			return "", readErr
		}
	}
	if total != info.Size() {
		return "", errors.New("SDK archive changed while hashing")
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func hashSDKTree(ctx context.Context, root string) (string, error) {
	hash := sha256.New()
	entries := 0
	var total int64
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if walkErr != nil {
			return walkErr
		}
		entries++
		if entries > maxSDKTreeEntries {
			return errors.New("Xcode.app contains too many entries")
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		_, _ = fmt.Fprintf(hash, "%s\x00%d\x00%d\x00", filepath.ToSlash(relative), info.Mode(), info.Size())
		switch {
		case info.IsDir():
			return nil
		case info.Mode()&os.ModeSymlink != 0:
			target, err := os.Readlink(path)
			if err != nil {
				return err
			}
			_, _ = fmt.Fprintf(hash, "link\x00%s\x00", target)
			return nil
		case info.Mode().IsRegular():
			if info.Size() < 0 || total > maxSDKInputBytes-info.Size() {
				return errors.New("Xcode.app exceeds the hashing limit")
			}
			total += info.Size()
			file, err := openRegularWithin(root, path, os.O_RDONLY, 0)
			if err != nil {
				return err
			}
			written, copyErr := copyContext(ctx, hash, file, info.Size())
			closeErr := file.Close()
			if copyErr != nil {
				return copyErr
			}
			if closeErr != nil {
				return closeErr
			}
			if written != info.Size() {
				return errors.New("Xcode.app file changed while hashing")
			}
			return nil
		default:
			return fmt.Errorf("Xcode.app contains a nonregular entry: %s", relative)
		}
	})
	if err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func copyContext(ctx context.Context, destination io.Writer, source io.Reader, expected int64) (int64, error) {
	buffer := make([]byte, 1024*1024)
	var total int64
	for total < expected {
		if err := ctx.Err(); err != nil {
			return total, err
		}
		want := int64(len(buffer))
		if expected-total < want {
			want = expected - total
		}
		n, err := source.Read(buffer[:want])
		if n > 0 {
			total += int64(n)
			if _, writeErr := destination.Write(buffer[:n]); writeErr != nil {
				return total, writeErr
			}
		}
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return total, err
		}
	}
	return total, nil
}

func inspectXcodeMetadata(xcode string) (sdkMetadata, error) {
	result := sdkMetadata{Values: map[string]string{}, Sources: map[string]string{}, Hashes: map[string]string{}}
	add := func(public, value, source string) {
		if value != "" && result.Values[public] == "" {
			result.Values[public] = value
			result.Sources[public] = source
		}
	}
	versionPath := filepath.Join(xcode, "Contents", "version.plist")
	if values, digest, present, err := readOptionalMetadataMap(xcode, versionPath); err != nil {
		return sdkMetadata{}, fmt.Errorf("read Xcode version metadata: %w", err)
	} else if present {
		result.Hashes[filepath.ToSlash(mustRelative(xcode, versionPath))] = digest
		add("xcodeVersion", values["CFBundleShortVersionString"], relativeMetadataSource(xcode, versionPath, "CFBundleShortVersionString"))
		add("xcodeBuildVersion", values["ProductBuildVersion"], relativeMetadataSource(xcode, versionPath, "ProductBuildVersion"))
		add("xcodeBundleVersion", values["CFBundleVersion"], relativeMetadataSource(xcode, versionPath, "CFBundleVersion"))
	}
	platformRoot := filepath.Join(xcode, "Contents", "Developer", "Platforms", "iPhoneOS.platform")
	platformVersion := filepath.Join(platformRoot, "version.plist")
	if values, digest, present, err := readOptionalMetadataMap(xcode, platformVersion); err != nil {
		return sdkMetadata{}, fmt.Errorf("read platform version metadata: %w", err)
	} else if present {
		result.Hashes[filepath.ToSlash(mustRelative(xcode, platformVersion))] = digest
		add("platformVersion", firstValue(values, "CFBundleShortVersionString", "Version"), relativeMetadataSource(xcode, platformVersion, firstPresentKey(values, "CFBundleShortVersionString", "Version")))
		add("platformBuildVersion", values["ProductBuildVersion"], relativeMetadataSource(xcode, platformVersion, "ProductBuildVersion"))
	}
	platformInfo := filepath.Join(platformRoot, "Info.plist")
	if values, digest, present, err := readOptionalMetadataMap(xcode, platformInfo); err != nil {
		return sdkMetadata{}, fmt.Errorf("read platform Info.plist: %w", err)
	} else if present {
		result.Hashes[filepath.ToSlash(mustRelative(xcode, platformInfo))] = digest
		add("platformVersion", firstValue(values, "CFBundleShortVersionString", "Version"), relativeMetadataSource(xcode, platformInfo, firstPresentKey(values, "CFBundleShortVersionString", "Version")))
		add("platformBuildVersion", values["ProductBuildVersion"], relativeMetadataSource(xcode, platformInfo, "ProductBuildVersion"))
	}
	sdk, err := discoverIPhoneSDK(platformRoot)
	if err != nil {
		return sdkMetadata{}, err
	}
	settingsJSON := filepath.Join(sdk, "SDKSettings.json")
	if values, digest, present, err := readOptionalMetadataMap(xcode, settingsJSON); err != nil {
		return sdkMetadata{}, fmt.Errorf("read SDKSettings.json: %w", err)
	} else if present {
		result.Hashes[filepath.ToSlash(mustRelative(xcode, settingsJSON))] = digest
		add("sdkVersion", values["Version"], relativeMetadataSource(xcode, settingsJSON, "Version"))
		add("sdkCanonicalName", values["CanonicalName"], relativeMetadataSource(xcode, settingsJSON, "CanonicalName"))
		add("sdkBuildVersion", values["ProductBuildVersion"], relativeMetadataSource(xcode, settingsJSON, "ProductBuildVersion"))
	}
	settingsPlist := filepath.Join(sdk, "SDKSettings.plist")
	if values, digest, present, err := readOptionalMetadataMap(xcode, settingsPlist); err != nil {
		return sdkMetadata{}, fmt.Errorf("read SDKSettings.plist: %w", err)
	} else if present {
		result.Hashes[filepath.ToSlash(mustRelative(xcode, settingsPlist))] = digest
		add("sdkVersion", values["Version"], relativeMetadataSource(xcode, settingsPlist, "Version"))
		add("sdkCanonicalName", values["CanonicalName"], relativeMetadataSource(xcode, settingsPlist, "CanonicalName"))
		add("sdkBuildVersion", values["ProductBuildVersion"], relativeMetadataSource(xcode, settingsPlist, "ProductBuildVersion"))
	}
	systemVersion := filepath.Join(sdk, "System", "Library", "CoreServices", "SystemVersion.plist")
	if values, digest, present, err := readOptionalMetadataMap(xcode, systemVersion); err != nil {
		return sdkMetadata{}, fmt.Errorf("read SDK SystemVersion.plist: %w", err)
	} else if present {
		result.Hashes[filepath.ToSlash(mustRelative(xcode, systemVersion))] = digest
		add("sdkProductVersion", values["ProductVersion"], relativeMetadataSource(xcode, systemVersion, "ProductVersion"))
		add("sdkBuildVersion", values["ProductBuildVersion"], relativeMetadataSource(xcode, systemVersion, "ProductBuildVersion"))
	}
	for _, field := range []string{"xcodeVersion", "xcodeBuildVersion", "xcodeBundleVersion", "platformVersion", "platformBuildVersion", "sdkVersion", "sdkProductVersion", "sdkBuildVersion", "sdkCanonicalName"} {
		if result.Values[field] == "" {
			result.Missing = append(result.Missing, field+" was not present in the inspected source metadata and remains unset")
		}
	}
	return result, nil
}

func retainMetadataSnapshot(ctx context.Context, xcode, snapshotRoot string, metadata sdkMetadata) error {
	if !filepath.IsAbs(snapshotRoot) || filepath.Clean(snapshotRoot) != snapshotRoot || snapshotRoot == string(filepath.Separator) {
		return errors.New("metadata snapshot root must be a clean absolute path")
	}
	if err := os.Mkdir(snapshotRoot, 0o700); err != nil {
		return fmt.Errorf("reserve metadata snapshot root: %w", err)
	}
	paths := make([]string, 0, len(metadata.Hashes))
	for relative := range metadata.Hashes {
		paths = append(paths, relative)
	}
	sort.Strings(paths)
	for _, relative := range paths {
		if err := validateMetadataRelativePath(relative); err != nil {
			return err
		}
		source := filepath.Join(xcode, filepath.FromSlash(relative))
		data, err := readBoundedRegularWithin(ctx, xcode, source, maxSDKMetadataBytes)
		if err != nil {
			return fmt.Errorf("retain metadata %s: %w", relative, err)
		}
		digest := sha256.Sum256(data)
		if !strings.EqualFold(hex.EncodeToString(digest[:]), metadata.Hashes[relative]) {
			return fmt.Errorf("metadata %s changed after inspection", relative)
		}
		if err := writeExclusiveFileWithin(snapshotRoot, filepath.FromSlash(relative), data, 0o600); err != nil {
			return fmt.Errorf("write metadata snapshot %s: %w", relative, err)
		}
		retained, err := readBoundedRegularWithin(ctx, snapshotRoot, filepath.Join(snapshotRoot, filepath.FromSlash(relative)), maxSDKMetadataBytes)
		if err != nil {
			return fmt.Errorf("verify metadata snapshot %s: %w", relative, err)
		}
		retainedDigest := sha256.Sum256(retained)
		if !bytes.Equal(retainedDigest[:], digest[:]) {
			return fmt.Errorf("metadata snapshot %s does not match its source hash", relative)
		}
	}
	return nil
}

func validateMetadataRelativePath(relative string) error {
	if relative == "" || strings.Contains(relative, "\\") || filepath.IsAbs(filepath.FromSlash(relative)) || filepath.ToSlash(filepath.Clean(filepath.FromSlash(relative))) != relative || relative == "." || strings.HasPrefix(relative, "../") {
		return fmt.Errorf("invalid metadata snapshot path %q", relative)
	}
	return nil
}

func readBoundedRegularWithin(ctx context.Context, root, path string, limit int64) ([]byte, error) {
	file, err := openRegularWithin(root, path, os.O_RDONLY, 0)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || info.Size() < 0 || info.Size() > limit {
		return nil, errors.New("file is not a bounded regular file")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	data, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) != info.Size() || int64(len(data)) > limit {
		return nil, errors.New("file changed or exceeded its bound while reading")
	}
	return data, nil
}

func readOptionalMetadataMap(root, path string) (map[string]string, string, bool, error) {
	if _, err := os.Lstat(path); errors.Is(err, os.ErrNotExist) {
		return nil, "", false, nil
	} else if err != nil {
		return nil, "", false, err
	}
	values, digest, err := readMetadataMap(root, path)
	return values, digest, true, err
}

func discoverIPhoneSDK(platformRoot string) (string, error) {
	sdks := filepath.Join(platformRoot, "Developer", "SDKs")
	alias := filepath.Join(sdks, "iPhoneOS.sdk")
	if _, err := os.Lstat(alias); err == nil {
		resolved, err := filepath.EvalSymlinks(alias)
		if err != nil {
			return "", fmt.Errorf("resolve iPhoneOS.sdk alias: %w", err)
		}
		if !pathInsideAny(resolved, sdks) {
			return "", errors.New("iPhoneOS.sdk alias escapes its SDK directory")
		}
		info, err := os.Stat(resolved)
		if err != nil || !info.IsDir() {
			return "", errors.New("iPhoneOS.sdk alias does not resolve to a directory")
		}
		return resolved, nil
	}
	entries, err := os.ReadDir(sdks)
	if err != nil {
		return "", fmt.Errorf("locate iPhoneOS SDK: %w", err)
	}
	candidates := make([]string, 0)
	for _, entry := range entries {
		if entry.IsDir() && strings.HasPrefix(entry.Name(), "iPhoneOS") && strings.HasSuffix(entry.Name(), ".sdk") {
			candidates = append(candidates, filepath.Join(sdks, entry.Name()))
		}
	}
	sort.Strings(candidates)
	if len(candidates) != 1 {
		return "", fmt.Errorf("expected exactly one iPhoneOS SDK without an alias, found %d", len(candidates))
	}
	return candidates[0], nil
}

func readMetadataMap(root, path string) (map[string]string, string, error) {
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return nil, "", err
	}
	if !pathInsideAny(resolved, root) {
		return nil, "", errors.New("metadata path escapes Xcode.app")
	}
	info, err := os.Stat(resolved)
	if err != nil || !info.Mode().IsRegular() || info.Size() <= 0 || info.Size() > maxSDKMetadataBytes {
		return nil, "", errors.New("metadata is not a bounded regular file")
	}
	file, err := openRegularWithin(root, resolved, os.O_RDONLY, 0)
	if err != nil {
		return nil, "", err
	}
	data, err := io.ReadAll(io.LimitReader(file, maxSDKMetadataBytes+1))
	closeErr := file.Close()
	if err != nil {
		return nil, "", err
	}
	if closeErr != nil {
		return nil, "", closeErr
	}
	if int64(len(data)) > maxSDKMetadataBytes || int64(len(data)) != info.Size() {
		return nil, "", errors.New("metadata changed or exceeded its bound while reading")
	}
	sum := sha256.Sum256(data)
	digest := hex.EncodeToString(sum[:])
	if strings.EqualFold(filepath.Ext(path), ".json") {
		values, err := parseJSONMetadataScalars(data)
		return values, digest, err
	}
	values, err := parseXMLPlistStrings(data)
	return values, digest, err
}

func parseJSONMetadataScalars(data []byte) (map[string]string, error) {
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.UseNumber()
	token, err := decoder.Token()
	if err != nil || token != json.Delim('{') {
		return nil, errors.New("metadata JSON top value must be an object")
	}
	values := map[string]string{}
	seen := map[string]bool{}
	for decoder.More() {
		keyToken, err := decoder.Token()
		if err != nil {
			return nil, err
		}
		key, ok := keyToken.(string)
		if !ok || key == "" {
			return nil, errors.New("metadata JSON contains an invalid key")
		}
		if seen[key] {
			return nil, errors.New("metadata JSON contains a duplicate top-level key: " + key)
		}
		seen[key] = true
		var raw any
		if err := decoder.Decode(&raw); err != nil {
			return nil, err
		}
		switch value := raw.(type) {
		case string:
			values[key] = value
		case json.Number:
			values[key] = value.String()
		}
	}
	if token, err := decoder.Token(); err != nil || token != json.Delim('}') {
		return nil, errors.New("metadata JSON object is incomplete")
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return nil, errors.New("metadata JSON has trailing content")
	}
	if len(values) == 0 {
		return nil, errors.New("metadata JSON contained no supported top-level scalar metadata")
	}
	return values, nil
}

func parseXMLPlistStrings(data []byte) (map[string]string, error) {
	if len(data) >= 8 && string(data[:8]) == "bplist00" {
		return parseBinaryPlistStrings(data)
	}
	decoder := xml.NewDecoder(strings.NewReader(string(data)))
	values := map[string]string{}
	seen := map[string]bool{}
	containers := make([]string, 0, 4)
	key := ""
	foundTopDictionary := false
	for {
		token, err := decoder.Token()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, err
		}
		switch element := token.(type) {
		case xml.StartElement:
			switch element.Name.Local {
			case "dict", "array":
				if len(containers) == 0 {
					if element.Name.Local != "dict" || foundTopDictionary {
						return nil, errors.New("plist top value must be one dictionary")
					}
					foundTopDictionary = true
				}
				if key != "" {
					key = ""
				}
				containers = append(containers, element.Name.Local)
			case "key":
				var decoded string
				if err := decoder.DecodeElement(&decoded, &element); err != nil {
					return nil, err
				}
				if len(containers) == 1 && containers[0] == "dict" {
					decoded = strings.TrimSpace(decoded)
					if decoded == "" {
						return nil, errors.New("plist contains an empty top-level key")
					}
					if seen[decoded] {
						return nil, errors.New("plist contains a duplicate top-level key: " + decoded)
					}
					seen[decoded] = true
					key = decoded
				} else {
					key = ""
				}
			case "string", "integer", "real":
				var value string
				if err := decoder.DecodeElement(&value, &element); err != nil {
					return nil, err
				}
				if len(containers) == 1 && containers[0] == "dict" && key != "" {
					values[key] = strings.TrimSpace(value)
				}
				key = ""
			default:
				if len(containers) == 1 {
					key = ""
				}
			}
		case xml.EndElement:
			if element.Name.Local == "dict" || element.Name.Local == "array" {
				if len(containers) == 0 || containers[len(containers)-1] != element.Name.Local {
					return nil, errors.New("plist container nesting is invalid")
				}
				containers = containers[:len(containers)-1]
				key = ""
			}
		}
	}
	if !foundTopDictionary || len(containers) != 0 || len(values) == 0 {
		return nil, errors.New("plist contained no supported top-level string metadata")
	}
	return values, nil
}

func mustRelative(root, path string) string {
	relative, err := filepath.Rel(root, path)
	if err != nil {
		return path
	}
	return relative
}

func relativeMetadataSource(root, path, key string) string {
	if key == "" {
		return ""
	}
	relative, err := filepath.Rel(root, path)
	if err != nil {
		relative = path
	}
	return filepath.ToSlash(relative) + "#" + key
}

func firstValue(values map[string]string, keys ...string) string {
	for _, key := range keys {
		if values[key] != "" {
			return values[key]
		}
	}
	return ""
}

func firstPresentKey(values map[string]string, keys ...string) string {
	for _, key := range keys {
		if values[key] != "" {
			return key
		}
	}
	return ""
}

func parseSwiftSDKList(output string) bool {
	for _, line := range strings.Split(output, "\n") {
		if strings.TrimSpace(line) == "darwin" {
			return true
		}
	}
	return false
}

func parseSwiftSDKConfigurationRoot(output string) (string, error) {
	root := ""
	for _, line := range strings.Split(output, "\n") {
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(trimmed, "sdkRootPath:") {
			continue
		}
		value := strings.TrimSpace(strings.TrimPrefix(trimmed, "sdkRootPath:"))
		if value == "" || root != "" {
			return "", errors.New("Swift SDK configuration contains an invalid or duplicate sdkRootPath")
		}
		root = value
	}
	if root == "" || !filepath.IsAbs(root) || filepath.Clean(root) != root {
		return "", errors.New("Swift SDK configuration did not report a clean absolute sdkRootPath")
	}
	resolved, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", fmt.Errorf("resolve configured Swift SDK root: %w", err)
	}
	info, err := os.Stat(resolved)
	if err != nil || !info.IsDir() {
		return "", errors.New("configured Swift SDK root is not a directory")
	}
	return filepath.Clean(resolved), nil
}

type installedSwiftSDKMetadata struct {
	SchemaVersion json.RawMessage `json:"schemaVersion"`
	TargetTriples map[string]struct {
		SDKRootPath string `json:"sdkRootPath"`
	} `json:"targetTriples"`
}

func verifyInstalledSDKSelection(ctx context.Context, installedBundle, listOutput, configurationOutput string) (string, string, string, error) {
	if !parseSwiftSDKList(listOutput) {
		return "", "", "", errors.New("selected Swift installation did not list the darwin SDK")
	}
	installedResolved, err := filepath.EvalSymlinks(installedBundle)
	if err != nil {
		return "", "", "", fmt.Errorf("resolve installed artifact bundle: %w", err)
	}
	installedInfo, err := os.Lstat(installedResolved)
	if err != nil || !installedInfo.IsDir() || installedInfo.Mode()&os.ModeSymlink != 0 {
		return "", "", "", errors.New("installed artifact bundle must be a real directory")
	}
	activeRoot, err := parseSwiftSDKConfigurationRoot(configurationOutput)
	if err != nil {
		return "", "", "", err
	}
	if !pathInsideAny(activeRoot, installedResolved) || activeRoot == installedResolved {
		return "", "", "", errors.New("configured Swift SDK root is not inside the installed artifact bundle")
	}
	metadataPath, data, err := findInstalledSwiftSDKMetadata(ctx, installedResolved)
	if err != nil {
		return "", "", "", err
	}
	var metadata installedSwiftSDKMetadata
	decoder := json.NewDecoder(bytes.NewReader(data))
	if err := decoder.Decode(&metadata); err != nil {
		return "", "", "", fmt.Errorf("decode installed swift-sdk.json: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return "", "", "", errors.New("installed swift-sdk.json has trailing data")
	}
	var schema string
	if err := json.Unmarshal(metadata.SchemaVersion, &schema); err != nil || schema != "4.0" {
		return "", "", "", errors.New("installed swift-sdk.json must use schemaVersion 4.0")
	}
	target, ok := metadata.TargetTriples[sdkTargetTriple]
	if !ok || target.SDKRootPath == "" {
		return "", "", "", errors.New("installed swift-sdk.json lacks arm64-apple-ios sdkRootPath")
	}
	metadataRoot := target.SDKRootPath
	if !filepath.IsAbs(metadataRoot) {
		metadataRoot = filepath.Join(filepath.Dir(metadataPath), metadataRoot)
	}
	metadataRoot, err = filepath.EvalSymlinks(filepath.Clean(metadataRoot))
	if err != nil {
		return "", "", "", fmt.Errorf("resolve swift-sdk.json sdkRootPath: %w", err)
	}
	if !pathInsideAny(metadataRoot, installedResolved) || metadataRoot != activeRoot {
		return "", "", "", errors.New("Swift configuration sdkRootPath disagrees with installed swift-sdk.json")
	}
	digest := sha256.Sum256(data)
	return activeRoot, metadataPath, hex.EncodeToString(digest[:]), nil
}

func findInstalledSwiftSDKMetadata(ctx context.Context, installedBundle string) (string, []byte, error) {
	candidates, err := findNamedRegularFilesWithin(ctx, installedBundle, "swift-sdk.json", maxSDKTreeEntries)
	if err != nil {
		return "", nil, err
	}
	if len(candidates) != 1 {
		return "", nil, fmt.Errorf("expected exactly one installed swift-sdk.json, found %d", len(candidates))
	}
	path := filepath.Join(installedBundle, candidates[0])
	data, err := readBoundedRegularWithin(ctx, installedBundle, path, maxSDKMetadataBytes)
	if err != nil {
		return "", nil, err
	}
	return path, data, nil
}

func (m *SetupManager) sdkConfigHome() (string, error) {
	value := m.XDGConfigHome
	if value == "" {
		var err error
		value, err = effectiveXDGConfigHome()
		if err != nil {
			return "", err
		}
	}
	if !filepath.IsAbs(value) || filepath.Clean(value) != value || value == string(filepath.Separator) {
		return "", errors.New("effective XDG_CONFIG_HOME must be a clean absolute non-root path")
	}
	return value, nil
}

func (m *SetupManager) inspectClangResources(ctx context.Context, clang string) (string, string, error) {
	if clang == "" || !filepath.IsAbs(clang) {
		return "", "", errors.New("selected Clang executable is not absolute")
	}
	output, code, err := m.runWithTimeout(ctx, clang, []string{"-print-resource-dir"}, m.workingDirectory(), 10*time.Second)
	if err != nil || code != 0 {
		return "", "", fmt.Errorf("clang -print-resource-dir failed with exit %d: %w", code, err)
	}
	return inspectClangResourceOutput(ctx, output)
}

func inspectClangResourceOutput(ctx context.Context, output string) (string, string, error) {
	lines := strings.Split(strings.TrimSpace(output), "\n")
	if len(lines) != 1 || !filepath.IsAbs(strings.TrimSpace(lines[0])) {
		return "", "", errors.New("clang returned an invalid resource directory")
	}
	resource, err := filepath.EvalSymlinks(filepath.Clean(strings.TrimSpace(lines[0])))
	if err != nil {
		return "", "", fmt.Errorf("resolve Clang resource directory: %w", err)
	}
	headers := filepath.Join(resource, "include")
	info, err := os.Lstat(headers)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return "", "", errors.New("Clang resource directory has no real include directory")
	}
	digest, err := hashSafeDirectory(ctx, headers, 2<<30, 200_000)
	if err != nil {
		return "", "", fmt.Errorf("fingerprint Clang headers: %w", err)
	}
	return resource, digest, nil
}

func hashSafeDirectory(ctx context.Context, root string, byteLimit int64, entryLimit int) (string, error) {
	rootInfo, err := os.Lstat(root)
	if err != nil || !rootInfo.IsDir() || rootInfo.Mode()&os.ModeSymlink != 0 {
		return "", errors.New("tree root must be a real directory")
	}
	hash := sha256.New()
	entries := 0
	var total int64
	err = filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		if walkErr != nil {
			return walkErr
		}
		entries++
		if entries > entryLimit {
			return errors.New("tree contains too many entries")
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		_, _ = fmt.Fprintf(hash, "%s\x00%d\x00%d\x00", filepath.ToSlash(relative), info.Mode(), info.Size())
		switch {
		case info.IsDir():
			return nil
		case info.Mode()&os.ModeSymlink != 0:
			target, err := os.Readlink(path)
			if err != nil {
				return err
			}
			if filepath.IsAbs(target) {
				return fmt.Errorf("tree contains an absolute symbolic link: %s", relative)
			}
			lexical := filepath.Clean(filepath.Join(filepath.Dir(path), target))
			if !lexicalPathInsideAny(lexical, root) {
				return fmt.Errorf("tree symbolic link escapes its root: %s", relative)
			}
			resolved, err := filepath.EvalSymlinks(path)
			if err != nil || !pathInsideAny(resolved, root) {
				return fmt.Errorf("tree symbolic link is broken or escapes its root: %s", relative)
			}
			_, _ = fmt.Fprintf(hash, "link\x00%s\x00", target)
			return nil
		case info.Mode().IsRegular():
			if info.Size() < 0 || total > byteLimit-info.Size() {
				return errors.New("tree exceeds its byte limit")
			}
			total += info.Size()
			file, err := openRegularWithin(root, path, os.O_RDONLY, 0)
			if err != nil {
				return err
			}
			written, copyErr := copyContext(ctx, hash, file, info.Size())
			closeErr := file.Close()
			if copyErr != nil {
				return copyErr
			}
			if closeErr != nil {
				return closeErr
			}
			if written != info.Size() {
				return errors.New("tree file changed while hashing")
			}
			return nil
		default:
			return fmt.Errorf("tree contains a special file: %s", relative)
		}
	})
	if err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func stageSDKArtifactBundle(ctx context.Context, builtSDK, artifactBundle, clangHeaders, expectedHeadersDigest string) error {
	if err := copyOwnedTree(ctx, builtSDK, artifactBundle); err != nil {
		return fmt.Errorf("stage user-owned SDK artifact bundle: %w", err)
	}
	digest, err := hashSafeDirectory(ctx, clangHeaders, 2<<30, 200_000)
	if err != nil || !strings.EqualFold(digest, expectedHeadersDigest) {
		return errors.New("selected Clang header tree changed after planning")
	}
	toolchainLibrary := filepath.Join("Developer", "Toolchains", "XcodeDefault.xctoolchain", "usr", "lib")
	clangAlias := filepath.Join(toolchainLibrary, "swift", "clang")
	clangVersions := filepath.Join(toolchainLibrary, "clang")
	if err := replaceDirectoryThroughContainedAlias(ctx, artifactBundle, clangAlias, clangVersions, "include", clangHeaders); err != nil {
		return fmt.Errorf("copy selected Clang headers: %w", err)
	}
	return nil
}

func (m *SetupManager) executeSDKImport(ctx context.Context, plan Plan, input PlanInput) (string, int, map[string]string, error) {
	if plan.SDKImport == nil {
		return "", 1, nil, errors.New("SDK import plan lacks source details")
	}
	configHome, err := m.sdkConfigHome()
	if err != nil || configHome != plan.SDKImport.XDGConfigHome {
		return "", 1, nil, errors.New("effective XDG_CONFIG_HOME changed after planning")
	}
	if err := ensurePrivateDirectory(configHome); err != nil {
		return "", 1, nil, fmt.Errorf("prepare private XDG_CONFIG_HOME: %w", err)
	}
	releaseSDKLock, err := m.acquireSDKImportLock()
	if err != nil {
		return "", 1, nil, err
	}
	defer releaseSDKLock()
	stageRoot := plan.SDKImport.StageRoot
	if err := ensurePrivateDirectory(filepath.Dir(stageRoot)); err != nil {
		return "", 1, nil, err
	}
	if err := os.Mkdir(stageRoot, 0o700); err != nil {
		return "", 1, nil, fmt.Errorf("reserve fresh SDK staging root: %w", err)
	}
	output := &cappedBuffer{limit: 64 * 1024}
	runStep := func(label, executable string, args []string) (string, error) {
		_, _ = fmt.Fprintf(output, "[%s] %s\n", label, strings.Join(args, " "))
		captured, code, err := m.run(ctx, executable, args, stageRoot)
		_, _ = output.Write([]byte(captured))
		if captured != "" && !strings.HasSuffix(captured, "\n") {
			_, _ = output.Write([]byte{'\n'})
		}
		if err != nil {
			return captured, fmt.Errorf("%s failed with exit %d: %w", label, code, err)
		}
		return captured, nil
	}
	xcode := plan.SDKImport.InputPath
	step := func(kind, tool, operation string, argsPrefix ...string) (Step, error) {
		for _, candidate := range plan.Steps {
			matchesArgs := len(candidate.Args) >= len(argsPrefix)
			for index := range argsPrefix {
				if !matchesArgs || candidate.Args[index] != argsPrefix[index] {
					matchesArgs = false
					break
				}
			}
			if candidate.Kind == kind && candidate.Tool == tool && (operation == "" || candidate.Operation == operation) && matchesArgs {
				return candidate, nil
			}
		}
		return Step{}, fmt.Errorf("SDK import plan lacks %s %s step", tool, operation)
	}
	if plan.SDKImport.InputKind == "xip" {
		unxipStep, stepErr := step("process", "unxip", "", "--statistics")
		if stepErr != nil {
			return output.String(), 1, nil, stepErr
		}
		extractionPath := filepath.Join(stageRoot, "xip-extracted")
		if len(unxipStep.Args) != 3 || unxipStep.Args[2] != extractionPath {
			return output.String(), 1, nil, errors.New("unxip output directory is not the reserved confined extraction path")
		}
		extractionDirectory, err := createFreshPrivateDirectoryWithin(stageRoot, extractionPath)
		if err != nil {
			return output.String(), 1, nil, fmt.Errorf("prepare fresh private unxip output directory: %w", err)
		}
		if err := verifyFreshPrivateDirectoryWithin(stageRoot, extractionPath, extractionDirectory); err != nil {
			extractionDirectory.Close()
			return output.String(), 1, nil, fmt.Errorf("verify fresh private unxip output directory: %w", err)
		}
		_, runErr := runStep("unxip", unxipStep.Executable, unxipStep.Args)
		closeErr := extractionDirectory.Close()
		if runErr != nil {
			return output.String(), 1, nil, runErr
		}
		if closeErr != nil {
			return output.String(), 1, nil, closeErr
		}
		xcode, err = discoverExtractedXcode(filepath.Join(stageRoot, "xip-extracted"))
		if err != nil {
			return output.String(), 1, nil, err
		}
	}
	metadata, err := inspectXcodeMetadata(xcode)
	if err != nil {
		return output.String(), 1, nil, err
	}
	metadataSnapshotRoot := filepath.Join(stageRoot, "metadata-snapshot")
	if err := retainMetadataSnapshot(ctx, xcode, metadataSnapshotRoot, metadata); err != nil {
		return output.String(), 1, nil, err
	}
	_, _ = output.Write([]byte("[orchard] retained and hash-verified named SDK metadata snapshot\n"))
	build, err := step("process", "xtool", "", "sdk", "build")
	if err != nil {
		return output.String(), 1, nil, err
	}
	buildArgs := []string{"sdk", "build", xcode, filepath.Join(stageRoot, "sdk-build"), "--arch", input.Arch}
	if _, err := os.Lstat(filepath.Join(stageRoot, "sdk-build")); !errors.Is(err, os.ErrNotExist) {
		return output.String(), 1, nil, errors.New("fresh xtool SDK output parent unexpectedly exists")
	}
	if _, err := runStep("xtool sdk build", build.Executable, buildArgs); err != nil {
		return output.String(), 1, nil, err
	}
	builtSDK := filepath.Join(stageRoot, "sdk-build", "darwin.xtoolsdk")
	builtInfo, err := os.Lstat(builtSDK)
	if err != nil || !builtInfo.IsDir() || builtInfo.Mode()&os.ModeSymlink != 0 {
		return output.String(), 1, nil, errors.New("xtool did not create a regular sdk-build/darwin.xtoolsdk directory")
	}
	clangStep, err := step("process", "clang", "", "-print-resource-dir")
	if err != nil {
		return output.String(), 1, nil, err
	}
	clangOutput, err := runStep("clang resource directory", clangStep.Executable, clangStep.Args)
	if err != nil {
		return output.String(), 1, nil, err
	}
	clangResource, clangHeadersDigest, err := inspectClangResourceOutput(ctx, clangOutput)
	if err != nil {
		return output.String(), 1, nil, err
	}
	if clangResource != plan.SDKImport.ClangResourceDir || !strings.EqualFold(clangHeadersDigest, plan.SDKImport.ClangHeadersSHA256) {
		return output.String(), 1, nil, errors.New("selected Clang resources changed after planning")
	}
	artifactBundle := filepath.Join(stageRoot, "darwin.artifactbundle")
	if err := stageSDKArtifactBundle(ctx, builtSDK, artifactBundle, filepath.Join(clangResource, "include"), clangHeadersDigest); err != nil {
		return output.String(), 1, nil, err
	}
	_, _ = output.Write([]byte("[orchard] staged fresh user-owned darwin.artifactbundle with selected Clang headers\n"))
	statusOutput, statusCode, statusErr := m.runStatusProbe(ctx, build.Executable, stageRoot)
	_, _ = output.Write([]byte("[xtool sdk status before install]\n" + statusOutput))
	installed, recognized := parseSDKStatus(statusOutput)
	if statusErr != nil || statusCode != 0 || !recognized {
		return output.String(), 1, nil, errors.New("could not prove the existing Darwin SDK is absent immediately before install")
	}
	if installed {
		return output.String(), 1, nil, errors.New("a Darwin SDK became installed; refusing destructive replacement")
	}
	sdkInstallParent := filepath.Join(configHome, "swiftpm", "swift-sdks")
	if err := ensurePrivateDirectory(sdkInstallParent); err != nil {
		return output.String(), 1, nil, fmt.Errorf("prepare symlink-free Swift SDK destination: %w", err)
	}
	installedBundle := filepath.Join(sdkInstallParent, "darwin.artifactbundle")
	if _, err := os.Lstat(installedBundle); err == nil {
		return output.String(), 1, nil, errors.New("a Darwin SDK destination already exists; refusing destructive replacement")
	} else if !errors.Is(err, os.ErrNotExist) {
		return output.String(), 1, nil, fmt.Errorf("inspect Swift SDK destination: %w", err)
	}
	swiftStep, err := step("process", "swift", "", "sdk", "install")
	if err != nil {
		return output.String(), 1, nil, err
	}
	if _, err := runStep("swift sdk install", swiftStep.Executable, []string{"sdk", "install", artifactBundle}); err != nil {
		return output.String(), 1, nil, err
	}
	finalStatus, finalCode, finalErr := m.runStatusProbe(ctx, build.Executable, stageRoot)
	_, _ = output.Write([]byte("[xtool sdk status after install]\n" + finalStatus))
	installed, recognized = parseSDKStatus(finalStatus)
	if finalErr != nil || finalCode != 0 || !recognized || !installed {
		return output.String(), 1, nil, errors.New("xtool did not report an installed Darwin SDK after installation")
	}
	if err := ensurePrivateDirectory(sdkInstallParent); err != nil {
		return output.String(), 1, nil, fmt.Errorf("verify symlink-free Swift SDK destination: %w", err)
	}
	installedInfo, err := os.Lstat(installedBundle)
	if err != nil || !installedInfo.IsDir() || installedInfo.Mode()&os.ModeSymlink != 0 {
		return output.String(), 1, nil, errors.New("Swift reported success but the expected installed darwin.artifactbundle is absent")
	}
	listStep, err := step("process", "swift", "", "sdk", "list")
	if err != nil {
		return output.String(), 1, nil, err
	}
	listOutput, err := runStep("swift sdk list", listStep.Executable, listStep.Args)
	if err != nil {
		return output.String(), 1, nil, err
	}
	configureStep, err := step("process", "swift", "", "sdk", "configure")
	if err != nil {
		return output.String(), 1, nil, err
	}
	configurationOutput, err := runStep("swift sdk configure", configureStep.Executable, configureStep.Args)
	if err != nil {
		return output.String(), 1, nil, err
	}
	activeSDKRoot, swiftSDKMetadataPath, swiftSDKMetadataDigest, err := verifyInstalledSDKSelection(ctx, installedBundle, listOutput, configurationOutput)
	if err != nil {
		return output.String(), 1, nil, err
	}
	receipt := SDKEnvironmentReceipt{SchemaVersion: 1, InputPath: plan.SDKImport.InputPath, InputKind: plan.SDKImport.InputKind, InputSHA256: plan.SDKImport.InputSHA256, Trust: "operator_supplied", Arch: input.Arch, StageRoot: stageRoot, XDGConfigHome: configHome, SwiftExecutable: plan.SDKImport.SwiftExecutable, SwiftSHA256: plan.SDKImport.SwiftSHA256, ClangExecutable: plan.SDKImport.ClangExecutable, ClangSHA256: plan.SDKImport.ClangSHA256, ClangResourceDir: clangResource, ClangHeadersSHA256: clangHeadersDigest, StagedArtifactBundle: artifactBundle, InstalledArtifactBundle: installedBundle, ActiveSDKRoot: activeSDKRoot, TargetTriple: sdkTargetTriple, SwiftSDKMetadataPath: swiftSDKMetadataPath, SwiftSDKMetadataSHA256: swiftSDKMetadataDigest, SDKStatus: strings.TrimSpace(strings.Join(strings.Fields(redactOutput(finalStatus)), " ")), Metadata: metadata.Values, MetadataSources: metadata.Sources, MetadataHashes: metadata.Hashes, MetadataSnapshotRoot: metadataSnapshotRoot, MissingMetadata: metadata.Missing, CreatedAt: time.Now().UTC()}
	resultMetadata := map[string]string{"inputPath": receipt.InputPath, "inputKind": receipt.InputKind, "inputSha256": receipt.InputSHA256, "trust": receipt.Trust, "arch": receipt.Arch, "stageRoot": stageRoot, "xdgConfigHome": configHome, "swiftExecutable": receipt.SwiftExecutable, "swiftSha256": receipt.SwiftSHA256, "clangExecutable": receipt.ClangExecutable, "clangSha256": receipt.ClangSHA256, "clangResourceDir": receipt.ClangResourceDir, "clangHeadersSha256": receipt.ClangHeadersSHA256, "installedArtifactBundle": installedBundle, "activeSdkRoot": activeSDKRoot, "targetTriple": receipt.TargetTriple, "swiftSdkMetadataPath": swiftSDKMetadataPath, "swiftSdkMetadataSha256": swiftSDKMetadataDigest, "metadataSnapshotRoot": metadataSnapshotRoot}
	for key, value := range metadata.Values {
		resultMetadata[key] = value
	}
	for key, source := range metadata.Sources {
		resultMetadata[key+"Source"] = source
	}
	for path, digest := range metadata.Hashes {
		resultMetadata["metadataSha256:"+path] = digest
	}
	if len(metadata.Missing) > 0 {
		resultMetadata["missingMetadata"] = strings.Join(metadata.Missing, "; ")
	}
	receiptPath := filepath.Join(stageRoot, "import-receipt.json")
	if err := writeSDKReceipt(receiptPath, receipt, true); err != nil {
		return output.String(), 1, resultMetadata, err
	}
	stableReceipt := filepath.Join(m.StateDir, "sdk-environment.json")
	if err := writeSDKReceipt(stableReceipt, receipt, false); err != nil {
		return output.String(), 1, resultMetadata, err
	}
	resultMetadata["receiptPath"] = stableReceipt
	_, _ = output.Write([]byte("SDK import completed and actual installed status was verified.\n"))
	return output.String(), 0, resultMetadata, nil
}

func writeSDKReceipt(path string, receipt SDKEnvironmentReceipt, exclusive bool) error {
	data, err := json.MarshalIndent(receipt, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	if err := ensurePrivateDirectory(filepath.Dir(path)); err != nil {
		return err
	}
	writePath := path
	if !exclusive {
		writePath = filepath.Join(filepath.Dir(path), ".sdk-environment-"+operationID()+".tmp")
	}
	file, err := os.OpenFile(writePath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	if _, err = file.Write(data); err == nil {
		err = file.Sync()
	}
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		_ = os.Remove(writePath)
		return err
	}
	if !exclusive {
		if info, statErr := os.Lstat(path); statErr == nil && (!info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0) {
			_ = os.Remove(writePath)
			return errors.New("existing SDK environment receipt is not a regular file")
		} else if statErr != nil && !errors.Is(statErr, os.ErrNotExist) {
			_ = os.Remove(writePath)
			return statErr
		}
		if err := os.Rename(writePath, path); err != nil {
			_ = os.Remove(writePath)
			return err
		}
	}
	return nil
}

// LoadSDKEnvironmentReceipt returns the verified private receipt describing
// the installed Swift SDK environment without probing tools or changing state.
func LoadSDKEnvironmentReceipt(stateDir string) (SDKEnvironmentReceipt, error) {
	if stateDir == "" || !filepath.IsAbs(stateDir) || filepath.Clean(stateDir) != stateDir {
		return SDKEnvironmentReceipt{}, errors.New("SDK receipt state directory must be clean and absolute")
	}
	path := filepath.Join(stateDir, "sdk-environment.json")
	info, err := os.Lstat(path)
	if err != nil {
		return SDKEnvironmentReceipt{}, err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Size() <= 0 || info.Size() > 4<<20 {
		return SDKEnvironmentReceipt{}, errors.New("SDK environment receipt must be a bounded regular file")
	}
	file, err := openRegularAbsolute(path)
	if err != nil {
		return SDKEnvironmentReceipt{}, err
	}
	defer file.Close()
	decoder := json.NewDecoder(io.LimitReader(file, 4<<20))
	decoder.DisallowUnknownFields()
	var receipt SDKEnvironmentReceipt
	if err := decoder.Decode(&receipt); err != nil {
		return SDKEnvironmentReceipt{}, err
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return SDKEnvironmentReceipt{}, errors.New("SDK environment receipt has trailing data")
	}
	if receipt.SchemaVersion != 1 || receipt.Trust != "operator_supplied" {
		return SDKEnvironmentReceipt{}, errors.New("unsupported SDK environment receipt")
	}
	for label, value := range map[string]string{"input path": receipt.InputPath, "stage root": receipt.StageRoot, "XDG config home": receipt.XDGConfigHome, "Swift executable": receipt.SwiftExecutable, "Clang executable": receipt.ClangExecutable, "staged artifact": receipt.StagedArtifactBundle, "installed artifact": receipt.InstalledArtifactBundle, "active SDK root": receipt.ActiveSDKRoot, "Swift SDK metadata": receipt.SwiftSDKMetadataPath, "metadata snapshot root": receipt.MetadataSnapshotRoot} {
		if !filepath.IsAbs(value) || filepath.Clean(value) != value {
			return SDKEnvironmentReceipt{}, fmt.Errorf("SDK receipt %s is invalid", label)
		}
	}
	for _, digest := range []string{receipt.InputSHA256, receipt.SwiftSHA256, receipt.ClangSHA256, receipt.ClangHeadersSHA256, receipt.SwiftSDKMetadataSHA256} {
		if len(digest) != 64 || strings.Trim(digest, "0123456789abcdefABCDEF") != "" {
			return SDKEnvironmentReceipt{}, errors.New("SDK receipt contains an invalid SHA-256")
		}
	}
	if receipt.TargetTriple != sdkTargetTriple {
		return SDKEnvironmentReceipt{}, errors.New("SDK receipt target triple is unsupported")
	}
	installed, err := filepath.EvalSymlinks(receipt.InstalledArtifactBundle)
	if err != nil || !pathInsideAny(receipt.ActiveSDKRoot, installed) || receipt.ActiveSDKRoot == installed || !pathInsideAny(receipt.SwiftSDKMetadataPath, installed) {
		return SDKEnvironmentReceipt{}, errors.New("SDK receipt active root or metadata escapes its installed artifact")
	}
	metadataBytes, err := readBoundedRegularWithin(context.Background(), installed, receipt.SwiftSDKMetadataPath, maxSDKMetadataBytes)
	if err != nil {
		return SDKEnvironmentReceipt{}, fmt.Errorf("verify installed Swift SDK metadata: %w", err)
	}
	metadataDigest := sha256.Sum256(metadataBytes)
	if !strings.EqualFold(hex.EncodeToString(metadataDigest[:]), receipt.SwiftSDKMetadataSHA256) {
		return SDKEnvironmentReceipt{}, errors.New("installed swift-sdk.json does not match its receipt hash")
	}
	if !lexicalPathInsideAny(receipt.MetadataSnapshotRoot, receipt.StageRoot) || receipt.MetadataSnapshotRoot == receipt.StageRoot {
		return SDKEnvironmentReceipt{}, errors.New("SDK metadata snapshot root escapes its private import stage")
	}
	if directory, err := openDirectoryWithin(receipt.StageRoot, receipt.MetadataSnapshotRoot); err != nil {
		return SDKEnvironmentReceipt{}, fmt.Errorf("verify SDK metadata snapshot root: %w", err)
	} else {
		_ = directory.Close()
	}
	for relative, digest := range receipt.MetadataHashes {
		if err := validateMetadataRelativePath(relative); err != nil {
			return SDKEnvironmentReceipt{}, err
		}
		if len(digest) != 64 || strings.Trim(digest, "0123456789abcdefABCDEF") != "" {
			return SDKEnvironmentReceipt{}, errors.New("SDK receipt contains an invalid metadata SHA-256")
		}
		data, err := readBoundedRegularWithin(context.Background(), receipt.MetadataSnapshotRoot, filepath.Join(receipt.MetadataSnapshotRoot, filepath.FromSlash(relative)), maxSDKMetadataBytes)
		if err != nil {
			return SDKEnvironmentReceipt{}, fmt.Errorf("verify retained metadata %s: %w", relative, err)
		}
		sum := sha256.Sum256(data)
		if !strings.EqualFold(hex.EncodeToString(sum[:]), digest) {
			return SDKEnvironmentReceipt{}, fmt.Errorf("retained metadata %s does not match its receipt hash", relative)
		}
	}
	for field, source := range receipt.MetadataSources {
		separator := strings.LastIndexByte(source, '#')
		if separator <= 0 || separator == len(source)-1 {
			return SDKEnvironmentReceipt{}, fmt.Errorf("SDK receipt metadata source for %s is invalid", field)
		}
		relative := source[:separator]
		if _, ok := receipt.MetadataHashes[relative]; !ok {
			return SDKEnvironmentReceipt{}, fmt.Errorf("SDK receipt metadata source for %s has no retained named file", field)
		}
	}
	return receipt, nil
}

func (m *SetupManager) acquireSDKImportLock() (func(), error) {
	locks := filepath.Join(m.StateDir, "locks")
	if err := ensurePrivateDirectory(locks); err != nil {
		return nil, err
	}
	lock := filepath.Join(locks, "sdk-import.lock")
	if err := os.Mkdir(lock, 0o700); err != nil {
		if errors.Is(err, os.ErrExist) {
			return nil, errors.New("another SDK import is in progress; refusing concurrent replacement risk")
		}
		return nil, fmt.Errorf("acquire SDK import lock: %w", err)
	}
	return func() { _ = os.Remove(lock) }, nil
}

func discoverExtractedXcode(root string) (string, error) {
	candidates := make([]string, 0, 1)
	entries := 0
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		entries++
		if entries > 100_000 {
			return errors.New("XIP extraction contains too many entries")
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return nil
		}
		if entry.IsDir() && strings.EqualFold(filepath.Ext(entry.Name()), ".app") {
			if _, err := os.Stat(filepath.Join(path, "Contents", "Developer")); err == nil {
				candidates = append(candidates, path)
				return filepath.SkipDir
			}
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	if len(candidates) != 1 {
		return "", fmt.Errorf("expected exactly one extracted Xcode.app, found %d", len(candidates))
	}
	if !pathInsideAny(candidates[0], root) {
		return "", errors.New("extracted Xcode.app escapes its private extraction root")
	}
	return candidates[0], nil
}
