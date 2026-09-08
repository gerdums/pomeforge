package pomeforge

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"strings"
	"time"
)

const maxSDKBindingFileBytes = 8 << 20

type SDKBindingResolver interface {
	Resolve(context.Context) (SDKBindingReport, []string)
}

type NativeSDKBinder struct {
	Tools    ToolResolver
	StateDir string
	Runner   SetupCommandRunner
	HostArch string
	Timeout  time.Duration
}

func (b NativeSDKBinder) Resolve(ctx context.Context) (SDKBindingReport, []string) {
	report := SDKBindingReport{TargetTriple: "arm64-apple-ios", Provenance: "unverified"}
	if runtime.GOOS != "linux" {
		return report, []string{"active iOS SDK binding is supported only on Linux"}
	}
	if b.Tools == nil {
		return report, []string{"active iOS SDK binding has no configured tool resolver"}
	}
	receipt, err := LoadSDKEnvironmentReceipt(b.StateDir)
	if err != nil {
		return report, []string{"private SDK environment receipt is missing, stale, or incomplete: " + safeSDKBindingError(err) + "; re-import the operator-supplied SDK with the current setup workflow"}
	}
	report.ReceiptPath = filepath.Join(b.StateDir, "sdk-environment.json")
	report.XDGConfigHome = receipt.XDGConfigHome
	report.SwiftExecutable = receipt.SwiftExecutable
	report.SwiftSHA256 = receipt.SwiftSHA256
	report.ClangExecutable = receipt.ClangExecutable
	report.ClangSHA256 = receipt.ClangSHA256

	effectiveConfig, err := effectiveXDGConfigHome()
	if err != nil || effectiveConfig != receipt.XDGConfigHome {
		return report, []string{"the effective XDG_CONFIG_HOME does not match the SDK import receipt; select the same explicit Swift SDK configuration used during setup"}
	}

	var blockers []string
	swift := canonicalToolStatus(b.Tools.Probe(ctx, "swift"))
	xtool := canonicalToolStatus(b.Tools.Probe(ctx, "xtool"))
	clang := canonicalToolStatus(b.Tools.Probe(ctx, "clang"))
	if swift.Status != "available" {
		blockers = append(blockers, "Swift 6.3 or later is required to inspect the active Darwin SDK")
	}
	if xtool.Status != "available" {
		blockers = append(blockers, "verified xtool 1.19.0 is required to inspect the active Darwin SDK")
	}
	if clang.Status != "available" {
		blockers = append(blockers, "the setup-selected Clang is required to verify its resource headers")
	}
	if len(blockers) != 0 {
		return report, blockers
	}
	if filepath.Clean(swift.Path) != receipt.SwiftExecutable {
		blockers = append(blockers, "the selected Swift invocation alias differs from the SDK import receipt")
	} else if digest, hashErr := hashExecutableForPlan(ctx, swift.Path); hashErr != nil || !strings.EqualFold(digest, receipt.SwiftSHA256) {
		blockers = append(blockers, "the selected Swift executable bytes or alias target changed since SDK import")
	}
	if filepath.Clean(clang.Path) != receipt.ClangExecutable {
		blockers = append(blockers, "the selected Clang invocation alias differs from the SDK import receipt")
	} else if digest, hashErr := hashExecutableForPlan(ctx, clang.Path); hashErr != nil || !strings.EqualFold(digest, receipt.ClangSHA256) {
		blockers = append(blockers, "the selected Clang executable bytes or alias target changed since SDK import")
	}
	if len(blockers) != 0 {
		return report, blockers
	}

	environment := environmentWithXDGConfigHome("xtool", receipt.XDGConfigHome)
	list, code, err := b.run(ctx, swift.Path, []string{"sdk", "list"}, environment)
	if err != nil || code != 0 {
		blockers = append(blockers, "`swift sdk list` did not report the darwin SDK in the selected XDG configuration")
	}
	configuration, code, err := b.run(ctx, swift.Path, []string{"sdk", "configure", "darwin", report.TargetTriple, "--show-configuration"}, environment)
	if err != nil || code != 0 {
		blockers = append(blockers, "Swift could not show the active darwin arm64-apple-ios configuration")
	}
	status, code, err := b.run(ctx, xtool.Path, []string{"sdk", "status"}, environment)
	installed, recognized := parseSDKStatus(status)
	if err != nil || code != 0 || !recognized || !installed {
		blockers = append(blockers, "`xtool sdk status` did not confirm an installed Darwin SDK in the same XDG configuration")
	}
	if len(blockers) != 0 {
		return report, blockers
	}
	root, metadataPath, metadataDigest, selectionErr := verifyReceiptSDKSelection(ctx, receipt, list, configuration, b.hostTriple())
	if selectionErr != nil {
		return report, []string{"the current Swift SDK selection does not match the setup receipt: " + safeSDKBindingError(selectionErr)}
	}
	if metadataPath != receipt.SwiftSDKMetadataPath || !strings.EqualFold(metadataDigest, receipt.SwiftSDKMetadataSHA256) {
		blockers = append(blockers, "the selected swift-sdk.json identity changed since SDK import")
	}

	clangOutput, code, clangErr := b.run(ctx, clang.Path, []string{"-print-resource-dir"}, environment)
	if clangErr != nil || code != 0 {
		blockers = append(blockers, "Clang could not report its current resource directory")
	} else if resource, headersDigest, inspectErr := inspectClangResourceOutput(ctx, clangOutput); inspectErr != nil || resource != receipt.ClangResourceDir || !strings.EqualFold(headersDigest, receipt.ClangHeadersSHA256) {
		blockers = append(blockers, "the selected Clang resource directory or header bytes changed since SDK import")
	}

	retained, metadataErr := inspectXcodeMetadata(receipt.MetadataSnapshotRoot)
	if metadataErr != nil {
		blockers = append(blockers, "retained named Xcode metadata could not be reparsed")
	} else if !reflect.DeepEqual(retained.Values, receipt.Metadata) || !reflect.DeepEqual(retained.Sources, receipt.MetadataSources) || !reflect.DeepEqual(retained.Hashes, receipt.MetadataHashes) || !reflect.DeepEqual(retained.Missing, receipt.MissingMetadata) {
		blockers = append(blockers, "retained named Xcode metadata values, sources, hashes, or missing-field report disagree with the SDK receipt")
	} else {
		blockers = append(blockers, compareSelectedSDKMetadata(ctx, root, retained)...)
	}

	required := []string{"xcodeVersion", "xcodeBuildVersion", "xcodeBundleVersion", "platformVersion", "platformBuildVersion", "sdkVersion", "sdkProductVersion", "sdkBuildVersion", "sdkCanonicalName"}
	for _, field := range required {
		if strings.TrimSpace(receipt.Metadata[field]) == "" {
			blockers = append(blockers, "SDK import receipt is missing named provenance field "+field)
		}
	}
	report.SDKRoot = root
	report.SDKVersion = receipt.Metadata["sdkVersion"]
	report.SDKProductVersion = receipt.Metadata["sdkProductVersion"]
	report.SDKCanonicalName = receipt.Metadata["sdkCanonicalName"]
	report.SDKBuildVersion = receipt.Metadata["sdkBuildVersion"]
	report.PlatformVersion = receipt.Metadata["platformVersion"]
	report.PlatformBuildVersion = receipt.Metadata["platformBuildVersion"]
	report.XcodeVersion = receipt.Metadata["xcodeVersion"]
	report.XcodeBuildVersion = receipt.Metadata["xcodeBuildVersion"]
	report.XcodeBundleVersion = receipt.Metadata["xcodeBundleVersion"]
	if len(blockers) == 0 {
		report.Provenance = "verified_operator_import"
	}
	return report, blockers
}

func verifyReceiptSDKSelection(ctx context.Context, receipt SDKEnvironmentReceipt, listOutput, configurationOutput, hostTriple string) (string, string, string, error) {
	if !parseSwiftSDKList(listOutput) {
		return "", "", "", errors.New("selected Swift installation did not list the darwin SDK")
	}
	installed, err := filepath.EvalSymlinks(receipt.InstalledArtifactBundle)
	if err != nil {
		return "", "", "", errors.New("installed artifact bundle could not be resolved")
	}
	info, err := os.Lstat(installed)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return "", "", "", errors.New("installed artifact bundle is not a real directory")
	}
	activeRoot, err := parseSwiftSDKConfigurationRoot(configurationOutput)
	if err != nil {
		return "", "", "", err
	}
	receiptRoot, err := filepath.EvalSymlinks(receipt.ActiveSDKRoot)
	if err != nil || activeRoot != filepath.Clean(receiptRoot) || !pathInsideAny(activeRoot, installed) || activeRoot == installed {
		return "", "", "", errors.New("configured SDK root disagrees with the installed artifact and receipt")
	}
	if problems := validateArtifactBundleInfo(ctx, installed, hostTriple); len(problems) != 0 {
		return "", "", "", errors.New(problems[0])
	}
	metadataPath := filepath.Clean(receipt.SwiftSDKMetadataPath)
	if !pathInsideAny(metadataPath, installed) {
		return "", "", "", errors.New("receipt swift-sdk.json escapes the installed artifact")
	}
	data, err := readBoundedRegularWithin(ctx, installed, metadataPath, maxSDKMetadataBytes)
	if err != nil {
		return "", "", "", errors.New("receipt-named swift-sdk.json is unreadable")
	}
	var metadata installedSwiftSDKMetadata
	decoder := json.NewDecoder(bytes.NewReader(data))
	if err := decoder.Decode(&metadata); err != nil {
		return "", "", "", errors.New("receipt-named swift-sdk.json is malformed")
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return "", "", "", errors.New("receipt-named swift-sdk.json has trailing data")
	}
	var schema string
	if err := json.Unmarshal(metadata.SchemaVersion, &schema); err != nil || schema != "4.0" {
		return "", "", "", errors.New("installed swift-sdk.json must use schemaVersion 4.0")
	}
	target, ok := metadata.TargetTriples[sdkTargetTriple]
	if !ok || target.SDKRootPath == "" {
		return "", "", "", errors.New("installed swift-sdk.json lacks arm64-apple-ios sdkRootPath")
	}
	declaredRoot := target.SDKRootPath
	if !filepath.IsAbs(declaredRoot) {
		declaredRoot = filepath.Join(filepath.Dir(metadataPath), declaredRoot)
	}
	declaredRoot, err = filepath.EvalSymlinks(filepath.Clean(declaredRoot))
	if err != nil || declaredRoot != activeRoot || !pathInsideAny(declaredRoot, installed) {
		return "", "", "", errors.New("Swift configuration sdkRootPath disagrees with installed swift-sdk.json")
	}
	digest := sha256.Sum256(data)
	return activeRoot, metadataPath, hex.EncodeToString(digest[:]), nil
}

func validateArtifactBundleInfo(ctx context.Context, installed, hostTriple string) []string {
	data, err := readBoundedRegularWithin(ctx, installed, filepath.Join(installed, "info.json"), maxSDKMetadataBytes)
	if err != nil {
		return []string{"installed darwin.artifactbundle has no readable info.json"}
	}
	var info struct {
		Artifacts map[string]struct {
			Type     string `json:"type"`
			Variants []struct {
				Path             string   `json:"path"`
				SupportedTriples []string `json:"supportedTriples"`
			} `json:"variants"`
		} `json:"artifacts"`
	}
	if err := json.Unmarshal(data, &info); err != nil {
		return []string{"installed darwin.artifactbundle info.json is malformed"}
	}
	for _, artifact := range info.Artifacts {
		if artifact.Type != "swiftSDK" {
			continue
		}
		for _, variant := range artifact.Variants {
			if variant.Path == "." && stringSliceContains(variant.SupportedTriples, hostTriple) {
				return nil
			}
		}
	}
	return []string{"darwin.artifactbundle has no '.' variant for this Linux host triple"}
}

func compareSelectedSDKMetadata(ctx context.Context, activeRoot string, retained sdkMetadata) []string {
	checked := map[string]bool{}
	var blockers []string
	for _, source := range retained.Sources {
		separator := strings.LastIndexByte(source, '#')
		if separator <= 0 || separator == len(source)-1 {
			continue
		}
		relative := source[:separator]
		actual := ""
		slash := filepath.ToSlash(relative)
		switch {
		case strings.HasSuffix(slash, "/SDKSettings.json"):
			actual = filepath.Join(activeRoot, "SDKSettings.json")
		case strings.HasSuffix(slash, "/SDKSettings.plist"):
			actual = filepath.Join(activeRoot, "SDKSettings.plist")
		case strings.HasSuffix(slash, "/System/Library/CoreServices/SystemVersion.plist"):
			actual = filepath.Join(activeRoot, "System", "Library", "CoreServices", "SystemVersion.plist")
		default:
			continue
		}
		if checked[actual] {
			continue
		}
		checked[actual] = true
		values, digest, err := readMetadataMap(activeRoot, actual)
		if err != nil || !strings.EqualFold(digest, retained.Hashes[relative]) {
			blockers = append(blockers, "selected SDKSettings/SystemVersion bytes do not match retained named provenance")
			continue
		}
		for candidateField, candidateSource := range retained.Sources {
			if strings.HasPrefix(candidateSource, relative+"#") {
				candidateKey := strings.TrimPrefix(candidateSource, relative+"#")
				if values[candidateKey] != retained.Values[candidateField] {
					blockers = append(blockers, "selected SDKSettings/SystemVersion values disagree with retained named provenance")
					break
				}
			}
		}
	}
	return blockers
}

func (b NativeSDKBinder) run(ctx context.Context, executable string, args, environment []string) (string, int, error) {
	timeout := b.Timeout
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	commandCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	if b.Runner != nil {
		return b.Runner.Run(commandCtx, executable, args, b.StateDir, environment)
	}
	output := &cappedBuffer{limit: 64 * 1024}
	cmd := exec.CommandContext(commandCtx, executable, args...)
	cmd.Dir = b.StateDir
	cmd.Env = environment
	cmd.Stdout = output
	cmd.Stderr = output
	configureProcessGroup(cmd)
	err := cmd.Run()
	killProcessGroup(cmd)
	if errors.Is(commandCtx.Err(), context.DeadlineExceeded) {
		return output.String(), 124, errors.New("SDK probe timed out")
	}
	if err == nil {
		return output.String(), 0, nil
	}
	code := 127
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		code = exitErr.ExitCode()
	}
	return output.String(), code, err
}

func (b NativeSDKBinder) hostTriple() string {
	arch := b.HostArch
	if arch == "" {
		arch = runtime.GOARCH
	}
	if arch == "arm64" || arch == "aarch64" {
		return "aarch64-unknown-linux-gnu"
	}
	return "x86_64-unknown-linux-gnu"
}

func environmentWithSDK(base []string, sdkRoot string) []string {
	out := make([]string, 0, len(base)+1)
	for _, entry := range base {
		if !strings.HasPrefix(entry, "POMEFORGE_IOS_SDK_ROOT=") {
			out = append(out, entry)
		}
	}
	out = append(out, "POMEFORGE_IOS_SDK_ROOT="+sdkRoot)
	sort.Strings(out)
	return out
}

func stringSliceContains(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}

func safeSDKBindingError(err error) string {
	if err == nil {
		return "unknown error"
	}
	message := err.Error()
	if strings.Contains(message, string(filepath.Separator)) {
		return "private provenance could not be validated"
	}
	return message
}

func encodeDTXcodeVersion(value string) (string, error) {
	parts := strings.Split(value, ".")
	if len(parts) < 2 || len(parts) > 3 {
		return "", fmt.Errorf("Xcode version must have two or three numeric components")
	}
	for _, part := range parts {
		if part == "" || strings.Trim(part, "0123456789") != "" || len(part) > 2 {
			return "", fmt.Errorf("Xcode version contains an unsupported component")
		}
	}
	patch := "0"
	if len(parts) == 3 {
		patch = parts[2]
	}
	return parts[0] + parts[1] + patch, nil
}
