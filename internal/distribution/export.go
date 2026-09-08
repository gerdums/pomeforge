package distribution

import (
	"archive/zip"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"howett.net/plist"
)

// Action is passed directly to a caller-supplied subprocess runner. Args is an
// argument array, never a shell command. Signing actions contain private paths
// and must not be logged or persisted by runners.
type Action struct {
	Executable string
	Args       []string
	Directory  string
}

// Runner executes one exact tool action without a shell.
type Runner interface {
	Run(context.Context, Action) error
}

// PublicAction is safe to display or persist; private identity paths and
// staging paths are replaced with descriptive placeholders.
type PublicAction struct {
	Tool string   `json:"tool"`
	Args []string `json:"args"`
}

// ExportPlan is a redacted description, not executable argv.
type ExportPlan struct {
	SourceBundle string         `json:"sourceBundle"`
	OutputIPA    string         `json:"outputIPA"`
	BundleID     string         `json:"bundleID"`
	Version      string         `json:"version"`
	Build        string         `json:"build"`
	Actions      []PublicAction `json:"actions"`
}

// ExportPreflight is the read-only aggregate readiness result used by later
// adapters to decide whether an export can run. It contains no private paths.
type ExportPreflight struct {
	Identity                 IdentityReport `json:"identity"`
	Bundle                   BundleReport   `json:"bundle"`
	AssetCatalogSelected     bool           `json:"assetCatalogSelected"`
	AssetCompilationRequired bool           `json:"assetCompilationRequired"`
}

type exportReadiness struct {
	result            Result[ExportPreflight]
	selectedCatalog   string
	exactEntitlements map[string]any
}

// ExportRequest binds an export to explicit source, destination, identity,
// versions, tools, entitlements, and limits.
type ExportRequest struct {
	SourceBundle            string         `json:"sourceBundle"`
	OutputIPA               string         `json:"outputIPA"`
	IdentityConfigPath      string         `json:"-"`
	ZsignExecutable         string         `json:"zsignExecutable"`
	AssetCompilerExecutable string         `json:"assetCompilerExecutable,omitempty"`
	AssetCatalogPath        string         `json:"assetCatalogPath,omitempty"`
	MinimumIOS              string         `json:"minimumIOS"`
	ExpectedBundleID        string         `json:"expectedBundleID"`
	ExpectedVersion         string         `json:"expectedVersion"`
	ExpectedBuild           string         `json:"expectedBuild"`
	Entitlements            map[string]any `json:"entitlements,omitempty"`
	CurrentTime             time.Time      `json:"currentTime,omitempty"`
	Limits                  Limits         `json:"limits,omitempty"`
}

// ExportReceipt describes locally inspected output. AppleProcessing remains
// not_checked until a separate authorized upload/processing operation exists.
type ExportReceipt struct {
	SHA256          string         `json:"sha256"`
	Size            int64          `json:"size"`
	Identity        IdentityReport `json:"identity"`
	IPA             IPAReport      `json:"ipa"`
	StructureValid  bool           `json:"structureValid"`
	SignatureValid  bool           `json:"profileSignatureValid"`
	ChainTrust      TrustStatus    `json:"profileChainTrust"`
	AppleProcessing string         `json:"appleProcessing"`
}

func requireLinux(operation string) error {
	if runtime.GOOS != "linux" {
		return fmt.Errorf("%s is supported only on Linux; macOS and hosted fallbacks are prohibited", operation)
	}
	return nil
}

func validateExportRequest(req ExportRequest) error {
	for field, value := range map[string]string{
		"sourceBundle": req.SourceBundle, "outputIPA": req.OutputIPA,
		"identityConfigPath": req.IdentityConfigPath, "zsignExecutable": req.ZsignExecutable,
	} {
		if err := requireAbsoluteCleanPath(field, value); err != nil {
			return err
		}
	}
	if req.AssetCompilerExecutable != "" {
		if err := requireAbsoluteCleanPath("assetCompilerExecutable", req.AssetCompilerExecutable); err != nil {
			return err
		}
	}
	if req.AssetCatalogPath != "" {
		if err := requireAbsoluteCleanPath("assetCatalogPath", req.AssetCatalogPath); err != nil {
			return err
		}
	}
	if req.ExpectedBundleID == "" || strings.Contains(req.ExpectedBundleID, "*") {
		return errors.New("expectedBundleID must be an explicit non-wildcard identifier")
	}
	if req.ExpectedVersion == "" || req.ExpectedBuild == "" {
		return errors.New("expectedVersion and expectedBuild are required")
	}
	if req.MinimumIOS == "" {
		return errors.New("minimumIOS is required")
	}
	return nil
}

func validateExecutable(path, field string) error {
	f, info, err := openRegularNoFollow(path)
	if err != nil {
		return fmt.Errorf("%s must be a concrete executable regular file: %w", field, err)
	}
	_ = f.Close()
	if info.Mode().Perm()&0o111 == 0 {
		return fmt.Errorf("%s must be executable", field)
	}
	return nil
}

func resolveAssetCatalog(ctx context.Context, req ExportRequest, limits Limits) (string, []Problem, error) {
	catalog := req.AssetCatalogPath
	var problems []Problem
	if catalog == "" {
		catalogs, err := discoverCatalogs(ctx, req.SourceBundle, limits)
		if err != nil {
			return "", nil, err
		}
		switch len(catalogs) {
		case 1:
			catalog = catalogs[0]
		case 0:
		default:
			problems = append(problems, problem("multiple_asset_catalogs", "assetCatalogPath", "multiple catalogs found; select one explicitly"))
		}
	}
	if catalog != "" {
		if req.AssetCompilerExecutable == "" {
			problems = append(problems, problem("missing_asset_compiler", "assetCompilerExecutable", "orchard-assets executable is required when a catalog is present"))
		} else if err := validateExecutable(req.AssetCompilerExecutable, "assetCompilerExecutable"); err != nil {
			problems = append(problems, problem("invalid_asset_compiler", "assetCompilerExecutable", err.Error()))
		}
		problems = append(problems, validateIconCatalog(ctx, catalog, limits)...)
	}
	return catalog, problems, nil
}

func performExportPreflight(ctx context.Context, req ExportRequest, configured *SigningConfig) (exportReadiness, error) {
	var readiness exportReadiness
	if err := requireLinux("distribution export preflight"); err != nil {
		return readiness, err
	}
	if err := checkContext(ctx); err != nil {
		return readiness, err
	}
	if err := validateExportRequest(req); err != nil {
		return readiness, err
	}
	limits := req.Limits.withDefaults()
	if req.CurrentTime.IsZero() {
		req.CurrentTime = time.Now()
	}
	parent, err := openDirectoryNoFollow(filepath.Dir(req.OutputIPA))
	if err != nil {
		return readiness, fmt.Errorf("open output parent safely: %w", err)
	}
	_ = parent.Close()
	if _, err := os.Lstat(req.OutputIPA); err == nil {
		return readiness, errors.New("output IPA already exists; export will not overwrite it")
	} else if !os.IsNotExist(err) {
		return readiness, err
	}
	if err := validateExecutable(req.ZsignExecutable, "zsignExecutable"); err != nil {
		readiness.result.Problems = append(readiness.result.Problems, problem("invalid_zsign", "zsignExecutable", err.Error()))
	}
	if configured == nil {
		loaded, err := LoadSigningConfig(ctx, req.IdentityConfigPath, limits)
		if err != nil {
			return readiness, err
		}
		configured = &loaded
	}
	identity, err := InspectIdentity(ctx, *configured, req.CurrentTime, limits)
	if err != nil {
		return readiness, err
	}
	readiness.result.Value.Identity = identity.Value
	readiness.result.Problems = append(readiness.result.Problems, storeIdentityProblems(identity, req)...)
	var entitlementProblems []Problem
	readiness.exactEntitlements, entitlementProblems = exactSigningEntitlements(identity.Value.Profile.Entitlements, req.Entitlements, identity.Value.Profile.ApplicationIdentifierPrefix, identity.Value.Profile.TeamID, req.ExpectedBundleID)
	readiness.result.Problems = append(readiness.result.Problems, entitlementProblems...)
	root, err := openDirectoryNoFollow(req.SourceBundle)
	if err != nil {
		return readiness, err
	}
	_ = root.Close()
	if !strings.HasSuffix(req.SourceBundle, ".app") {
		return readiness, errors.New("sourceBundle must be a real .app directory")
	}
	treeProblems, err := scanBundleTree(ctx, req.SourceBundle, limits, true)
	if err != nil {
		return readiness, err
	}
	readiness.result.Problems = append(readiness.result.Problems, treeProblems...)
	readiness.selectedCatalog, treeProblems, err = resolveAssetCatalog(ctx, req, limits)
	if err != nil {
		return readiness, err
	}
	readiness.result.Problems = append(readiness.result.Problems, treeProblems...)
	readiness.result.Value.AssetCatalogSelected = readiness.selectedCatalog != ""
	readiness.result.Value.AssetCompilationRequired = readiness.selectedCatalog != ""
	bundle, err := inspectBundleDirectory(ctx, req.SourceBundle, limits, readiness.selectedCatalog == "")
	if err != nil {
		return readiness, err
	}
	readiness.result.Value.Bundle = bundle.Value
	readiness.result.Problems = append(readiness.result.Problems, bundle.Problems...)
	readiness.result.Problems = append(readiness.result.Problems, expectedBundleProblems(bundle.Value, req)...)
	return readiness, nil
}

// PreflightExport performs the complete read-only readiness check. It loads
// and validates the identity, concrete tools, source bundle, expected
// metadata, entitlements, and selected catalog without running tools,
// creating output, or modifying the source.
func PreflightExport(ctx context.Context, req ExportRequest) (Result[ExportPreflight], error) {
	readiness, err := performExportPreflight(ctx, req, nil)
	return readiness.result, err
}

// PlanExport delegates to the complete read-only preflight and returns the
// exact public tool contract with private arguments redacted.
func PlanExport(ctx context.Context, req ExportRequest) (Result[ExportPlan], error) {
	preflight, err := PreflightExport(ctx, req)
	var out Result[ExportPlan]
	if err != nil {
		return out, err
	}
	out.Problems = append(out.Problems, preflight.Problems...)
	out.Value = ExportPlan{SourceBundle: req.SourceBundle, OutputIPA: req.OutputIPA, BundleID: req.ExpectedBundleID, Version: req.ExpectedVersion, Build: req.ExpectedBuild}
	if preflight.Value.AssetCompilationRequired {
		out.Value.Actions = append(out.Value.Actions, PublicAction{Tool: filepath.Base(req.AssetCompilerExecutable), Args: []string{"compile", "--catalog", "<validated-catalog>", "--app", "<private-staged-app>", "--minimum-ios", req.MinimumIOS, "--json"}})
	}
	out.Value.Actions = append(out.Value.Actions, PublicAction{Tool: filepath.Base(req.ZsignExecutable), Args: []string{"-k", "<private-key-snapshot>", "-c", "<certificate-snapshot>", "-m", "<provisioning-profile-snapshot>", "-e", "<private-generated-entitlements>", "-o", "<private-staged-output.ipa>", "<private-staged-app>"}})
	return out, nil
}

func replacedMainSigningPath(relative string) bool {
	slash := filepath.ToSlash(relative)
	return slash == "_CodeSignature" || strings.HasPrefix(slash, "_CodeSignature/") || slash == "CodeResources" || slash == "embedded.mobileprovision"
}

func topologyProblem(relative string) error {
	for _, part := range strings.Split(filepath.ToSlash(relative), "/") {
		switch {
		case part == "PlugIns" || part == "Extensions" || strings.HasSuffix(part, ".appex"):
			return fmt.Errorf("unsupported signing topology at %q: app extensions require their own validated identifiers, profiles, entitlements, and nested signatures", relative)
		case part == "Frameworks" || strings.HasSuffix(part, ".framework"):
			return fmt.Errorf("unsupported signing topology at %q: frameworks require separately validated nested code signing", relative)
		case part == "Watch":
			return fmt.Errorf("unsupported signing topology at %q: Watch content requires separately validated signing targets", relative)
		}
	}
	return nil
}

func safeCopyTree(ctx context.Context, source, destination string, limits Limits, rejectTopology, skipReplacedSigning bool) error {
	if err := os.Mkdir(destination, 0o755); err != nil {
		return err
	}
	var total int64
	var skip func(string) bool
	if skipReplacedSigning {
		skip = replacedMainSigningPath
	}
	return walkRegularTreeSkipping(ctx, source, limits.MaxArchiveEntries, skip, func(rel string, info os.FileInfo, in *os.File) (bool, error) {
		if rejectTopology {
			if err := topologyProblem(rel); err != nil {
				return false, err
			}
		}
		target := filepath.Join(destination, rel)
		if info.IsDir() {
			return false, os.Mkdir(target, 0o755)
		}
		if info.Size() > limits.MaxFileBytes {
			return false, fmt.Errorf("bundle file %q exceeds per-file limit", rel)
		}
		total += info.Size()
		if total > limits.MaxBundleBytes {
			return false, fmt.Errorf("bundle exceeds %d-byte limit", limits.MaxBundleBytes)
		}
		mode := os.FileMode(0o644)
		if info.Mode()&0o111 != 0 {
			mode = 0o755
		}
		out, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
		if err != nil {
			return false, err
		}
		copyErr := copyBounded(ctx, out, in, info.Size(), limits.MaxFileBytes)
		closeErr := out.Close()
		if copyErr != nil {
			return false, copyErr
		}
		if closeErr != nil {
			return false, closeErr
		}
		return false, nil
	})
}

func copyBounded(ctx context.Context, dst io.Writer, src io.Reader, expected, max int64) error {
	buf := make([]byte, 64<<10)
	var written int64
	for {
		if err := checkContext(ctx); err != nil {
			return err
		}
		n, err := src.Read(buf)
		if n > 0 {
			written += int64(n)
			if written > max {
				return errors.New("copy exceeds size limit")
			}
			if _, werr := dst.Write(buf[:n]); werr != nil {
				return werr
			}
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
	}
	if written != expected {
		return fmt.Errorf("source changed during copy: expected %d bytes, read %d", expected, written)
	}
	return nil
}

func discoverCatalogs(ctx context.Context, root string, limits Limits) ([]string, error) {
	var found []string
	err := walkRegularTree(ctx, root, limits.MaxArchiveEntries, func(rel string, info os.FileInfo, _ *os.File) (bool, error) {
		if info.IsDir() && strings.HasSuffix(filepath.Base(rel), ".xcassets") {
			found = append(found, filepath.Join(root, rel))
			return true, nil
		}
		return false, nil
	})
	sort.Strings(found)
	return found, err
}

func storeIdentityProblems(identity Result[IdentityReport], req ExportRequest) []Problem {
	ps := append([]Problem{}, identity.Problems...)
	r := identity.Value
	if !r.Profile.SignatureValid {
		ps = append(ps, problem("profile_signature_invalid", "provisioningProfile", "profile CMS signature integrity is not verified"))
	}
	if r.Profile.Trust != TrustVerified {
		ps = append(ps, problem("profile_chain_unverified", "trustedRootPaths", "export requires a chain verified against explicit operator trust roots"))
	}
	if r.Profile.Type != ProfileAppStore {
		ps = append(ps, problem("profile_not_app_store", "provisioningProfile", "store export requires an App Store profile without devices or enterprise flag"))
	}
	if r.Profile.HasProvisionedDevices {
		ps = append(ps, problem("profile_has_device_list", "ProvisionedDevices", "store profile must omit ProvisionedDevices; an empty array is still a device-list profile"))
	}
	if r.Profile.ProvisionsAllDevices {
		ps = append(ps, problem("enterprise_profile", "ProvisionsAllDevices", "enterprise profiles are not valid for store export"))
	}
	if r.Profile.GetTaskAllow {
		ps = append(ps, problem("get_task_allow_enabled", "get-task-allow", "store export requires get-task-allow=false"))
	}
	if r.Profile.TeamID == "" {
		ps = append(ps, problem("missing_team_identifier", "TeamIdentifier", "profile team identifier is missing"))
	}
	if strings.Contains(r.Profile.BundleIdentifier, "*") || r.Profile.BundleIdentifier != req.ExpectedBundleID {
		ps = append(ps, problem("profile_bundle_mismatch", "application-identifier", "profile must grant the exact expected bundle identifier without a wildcard"))
	}
	return ps
}

func expectedBundleProblems(bundle BundleReport, req ExportRequest) []Problem {
	var ps []Problem
	if bundle.BundleIdentifier != req.ExpectedBundleID {
		ps = append(ps, problem("bundle_identifier_mismatch", "CFBundleIdentifier", fmt.Sprintf("found %q, want %q", bundle.BundleIdentifier, req.ExpectedBundleID)))
	}
	if bundle.MarketingVersion != req.ExpectedVersion {
		ps = append(ps, problem("marketing_version_mismatch", "CFBundleShortVersionString", fmt.Sprintf("found %q, want %q", bundle.MarketingVersion, req.ExpectedVersion)))
	}
	if bundle.BuildNumber != req.ExpectedBuild {
		ps = append(ps, problem("build_number_mismatch", "CFBundleVersion", fmt.Sprintf("found %q, want %q", bundle.BuildNumber, req.ExpectedBuild)))
	}
	if !versionsEquivalent(bundle.MinimumOSVersion, req.MinimumIOS) {
		ps = append(ps, problem("minimum_os_mismatch", "MinimumOSVersion", fmt.Sprintf("found %q, want %q", bundle.MinimumOSVersion, req.MinimumIOS)))
	}
	return ps
}

func writePrivateSnapshot(ctx context.Context, source, destination string, max int64) error {
	data, err := readRegularFile(ctx, source, max)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(destination, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	written, writeErr := f.Write(data)
	closeErr := f.Close()
	if writeErr != nil {
		return writeErr
	}
	if written != len(data) {
		return io.ErrShortWrite
	}
	return closeErr
}

func snapshotSigningConfig(ctx context.Context, source SigningConfig, stagingRoot string, limits Limits) (SigningConfig, error) {
	dir := filepath.Join(stagingRoot, "identity")
	if err := os.Mkdir(dir, 0o700); err != nil {
		return SigningConfig{}, err
	}
	snapshot := SigningConfig{
		Version:                 source.Version,
		PrivateKeyPath:          filepath.Join(dir, "private-key.pem"),
		CertificatePath:         filepath.Join(dir, "certificate.pem"),
		ProvisioningProfilePath: filepath.Join(dir, "profile.mobileprovision"),
		Metadata:                source.Metadata,
	}
	for _, item := range []struct{ source, destination, label string }{
		{source.PrivateKeyPath, snapshot.PrivateKeyPath, "private key"},
		{source.CertificatePath, snapshot.CertificatePath, "certificate"},
		{source.ProvisioningProfilePath, snapshot.ProvisioningProfilePath, "provisioning profile"},
	} {
		if err := writePrivateSnapshot(ctx, item.source, item.destination, limits.MaxProfileBytes); err != nil {
			return SigningConfig{}, fmt.Errorf("snapshot %s: %w", item.label, err)
		}
	}
	for i, root := range source.TrustedRootPaths {
		destination := filepath.Join(dir, fmt.Sprintf("trusted-root-%02d.pem", i))
		if err := writePrivateSnapshot(ctx, root, destination, limits.MaxProfileBytes); err != nil {
			return SigningConfig{}, fmt.Errorf("snapshot trusted root: %w", err)
		}
		snapshot.TrustedRootPaths = append(snapshot.TrustedRootPaths, destination)
	}
	return snapshot, nil
}

func canonicalizeIPA(ctx context.Context, source, destination, appRoot, executable string, limits Limits) error {
	f, info, err := openRegularNoFollow(source)
	if err != nil {
		return err
	}
	defer f.Close()
	zr, err := zip.NewReader(f, info.Size())
	if err != nil {
		return err
	}
	if len(zr.File) > limits.MaxArchiveEntries {
		return fmt.Errorf("signed IPA has %d entries; limit is %d", len(zr.File), limits.MaxArchiveEntries)
	}
	files := map[string]*zip.File{}
	directories := map[string]bool{"Payload/": true, appRoot + "/": true}
	var total uint64
	for _, entry := range zr.File {
		if err := safeArchiveName(entry.Name); err != nil {
			return err
		}
		trimmed := strings.TrimSuffix(entry.Name, "/")
		if trimmed != "Payload" && trimmed != appRoot && !strings.HasPrefix(trimmed, appRoot+"/") {
			return fmt.Errorf("unsupported archive entry %q", entry.Name)
		}
		if strings.HasSuffix(entry.Name, "/") {
			directories[entry.Name] = true
			continue
		}
		if entry.UncompressedSize64 > uint64(limits.MaxFileBytes) {
			return fmt.Errorf("signed IPA entry %q exceeds the per-file limit", entry.Name)
		}
		if ^uint64(0)-total < entry.UncompressedSize64 {
			return errors.New("signed IPA uncompressed size overflow")
		}
		total += entry.UncompressedSize64
		if total > uint64(limits.MaxBundleBytes) {
			return fmt.Errorf("signed IPA content exceeds %d-byte limit", limits.MaxBundleBytes)
		}
		if _, exists := files[entry.Name]; exists {
			return fmt.Errorf("duplicate archive entry %q", entry.Name)
		}
		files[entry.Name] = entry
		for parent := path.Dir(entry.Name); parent != "." && parent != "/"; parent = path.Dir(parent) {
			directories[parent+"/"] = true
		}
	}
	var directoryNames, fileNames []string
	for name := range directories {
		directoryNames = append(directoryNames, name)
	}
	for name := range files {
		fileNames = append(fileNames, name)
	}
	sort.Slice(directoryNames, func(i, j int) bool {
		left, right := strings.Count(directoryNames[i], "/"), strings.Count(directoryNames[j], "/")
		if left != right {
			return left < right
		}
		return directoryNames[i] < directoryNames[j]
	})
	sort.Strings(fileNames)
	out, err := os.OpenFile(destination, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	ok := false
	defer func() {
		_ = out.Close()
		if !ok {
			_ = os.Remove(destination)
		}
	}()
	zw := zip.NewWriter(out)
	for _, name := range directoryNames {
		header := &zip.FileHeader{Name: name, Method: zip.Store}
		header.SetMode(os.ModeDir | 0o755)
		if _, err := zw.CreateHeader(header); err != nil {
			return err
		}
	}
	mainExecutable := appRoot + "/" + executable
	for _, name := range fileNames {
		entry := files[name]
		reader, err := entry.Open()
		if err != nil {
			return err
		}
		header := &zip.FileHeader{Name: name, Method: zip.Deflate}
		if name == mainExecutable {
			header.SetMode(0o755)
		} else {
			header.SetMode(0o644)
		}
		writer, err := zw.CreateHeader(header)
		if err == nil {
			err = copyBounded(ctx, writer, reader, int64(entry.UncompressedSize64), limits.MaxFileBytes)
		}
		closeErr := reader.Close()
		if err != nil {
			return err
		}
		if closeErr != nil {
			return closeErr
		}
	}
	if err := zw.Close(); err != nil {
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	info, err = os.Lstat(destination)
	if err != nil || !info.Mode().IsRegular() || info.Size() > limits.MaxIPABytes {
		return errors.New("canonical IPA is not a bounded regular file")
	}
	ok = true
	return nil
}

// Export stages and validates a bundle, optionally compiles a complete icon
// catalog, invokes zsign 1.1.2's exact contract, canonicalizes only the
// verified signed archive bytes, re-inspects the output, then atomically
// publishes it to a previously absent destination.
func Export(ctx context.Context, req ExportRequest, runner Runner) (Result[ExportReceipt], error) {
	var result Result[ExportReceipt]
	if err := requireLinux("distribution export"); err != nil {
		return result, err
	}
	if err := validateExportRequest(req); err != nil {
		return result, err
	}
	if runner == nil {
		return result, errors.New("runner is required")
	}
	limits := req.Limits.withDefaults()
	if req.CurrentTime.IsZero() {
		req.CurrentTime = time.Now()
	}
	if info, err := os.Lstat(req.OutputIPA); err == nil {
		_ = info
		return result, errors.New("output IPA already exists; export will not overwrite it")
	} else if !os.IsNotExist(err) {
		return result, err
	}
	identityConfig, err := LoadSigningConfig(ctx, req.IdentityConfigPath, limits)
	if err != nil {
		return result, err
	}
	parent, err := openDirectoryNoFollow(filepath.Dir(req.OutputIPA))
	if err != nil {
		return result, fmt.Errorf("open output parent safely: %w", err)
	}
	_ = parent.Close()
	stagingRoot, err := os.MkdirTemp(filepath.Dir(req.OutputIPA), ".orchard-export-")
	if err != nil {
		return result, err
	}
	if err := os.Chmod(stagingRoot, 0o700); err != nil {
		_ = os.RemoveAll(stagingRoot)
		return result, err
	}
	defer os.RemoveAll(stagingRoot)
	snapshotConfig, err := snapshotSigningConfig(ctx, identityConfig, stagingRoot, limits)
	if err != nil {
		return result, err
	}
	readiness, err := performExportPreflight(ctx, req, &snapshotConfig)
	if err != nil {
		return result, err
	}
	result.Problems = append(result.Problems, readiness.result.Problems...)
	if !result.Valid() {
		return result, nil
	}
	archiveRoot := filepath.Join(stagingRoot, "archive")
	payloadRoot := filepath.Join(archiveRoot, "Payload")
	if err := os.Mkdir(archiveRoot, 0o700); err != nil {
		return result, err
	}
	if err := os.Mkdir(payloadRoot, 0o755); err != nil {
		return result, err
	}
	stagedApp := filepath.Join(payloadRoot, filepath.Base(req.SourceBundle))
	if err := safeCopyTree(ctx, req.SourceBundle, stagedApp, limits, true, true); err != nil {
		return result, err
	}
	if readiness.selectedCatalog != "" {
		stagedCatalog := filepath.Join(stagingRoot, "catalog.xcassets")
		if err := safeCopyTree(ctx, readiness.selectedCatalog, stagedCatalog, limits, false, false); err != nil {
			return result, err
		}
		result.Problems = append(result.Problems, validateIconCatalog(ctx, stagedCatalog, limits)...)
		if !result.Valid() {
			return result, nil
		}
		action := Action{Executable: req.AssetCompilerExecutable, Args: []string{"compile", "--catalog", stagedCatalog, "--app", stagedApp, "--minimum-ios", req.MinimumIOS, "--json"}, Directory: stagingRoot}
		if err := runner.Run(ctx, action); err != nil {
			return result, errors.New("asset compilation failed: runner returned an error")
		}
		assetsInfo, err := os.Lstat(filepath.Join(stagedApp, "Assets.car"))
		if err != nil || !assetsInfo.Mode().IsRegular() || assetsInfo.Size() == 0 {
			result.Problems = append(result.Problems, problem("missing_compiled_assets", "Assets.car", "AssetKit did not produce a nonempty regular Assets.car"))
		}
	}
	stagedInspection, err := inspectBundleDirectory(ctx, stagedApp, limits, true)
	if err != nil {
		return result, err
	}
	result.Problems = append(result.Problems, stagedInspection.Problems...)
	result.Problems = append(result.Problems, expectedBundleProblems(stagedInspection.Value, req)...)
	if !result.Valid() {
		return result, nil
	}
	entitlementsPath := filepath.Join(stagingRoot, "entitlements.plist")
	entitlementData, err := plist.Marshal(readiness.exactEntitlements, plist.XMLFormat)
	if err != nil {
		return result, err
	}
	entitlementsFile, err := os.OpenFile(entitlementsPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return result, err
	}
	if _, err := entitlementsFile.Write(entitlementData); err != nil {
		_ = entitlementsFile.Close()
		return result, err
	}
	if err := entitlementsFile.Close(); err != nil {
		return result, err
	}
	zsignIPA := filepath.Join(stagingRoot, "zsign-output.ipa")
	signAction := Action{Executable: req.ZsignExecutable, Args: []string{"-k", snapshotConfig.PrivateKeyPath, "-c", snapshotConfig.CertificatePath, "-m", snapshotConfig.ProvisioningProfilePath, "-e", entitlementsPath, "-o", zsignIPA, stagedApp}, Directory: stagingRoot}
	if err := runner.Run(ctx, signAction); err != nil {
		return result, errors.New("zsign failed: runner returned an error")
	}
	outputInfo, err := os.Lstat(zsignIPA)
	if err != nil {
		if os.IsNotExist(err) {
			return result, errors.New("zsign returned success but did not create output IPA")
		}
		return result, err
	}
	if !outputInfo.Mode().IsRegular() || outputInfo.Size() > limits.MaxIPABytes {
		return result, errors.New("zsign output is not a bounded regular IPA file")
	}
	rawInspection, err := inspectIPA(ctx, zsignIPA, InspectionOptions{Limits: limits, TrustedRootPaths: snapshotConfig.TrustedRootPaths, CurrentTime: req.CurrentTime}, false)
	if err != nil {
		return result, err
	}
	result.Problems = append(result.Problems, rawInspection.Problems...)
	result.Problems = append(result.Problems, expectedBundleProblems(rawInspection.Value.Bundle, req)...)
	identity := readiness.result.Value.Identity
	if rawInspection.Value.Bundle.EmbeddedProfile == nil || rawInspection.Value.Bundle.EmbeddedProfile.UUID != identity.Profile.UUID {
		result.Problems = append(result.Problems, problem("output_profile_mismatch", "embedded.mobileprovision", "output does not embed the selected provisioning profile"))
	} else if rawInspection.Value.Bundle.EmbeddedProfile.SHA256 != identity.Profile.SHA256 {
		result.Problems = append(result.Problems, problem("output_profile_mismatch", "embedded.mobileprovision", "output profile bytes do not match the selected provisioning profile"))
	}
	if !rawInspection.Value.Bundle.CodeSignature.CodeResourcesPresent {
		result.Problems = append(result.Problems, problem("missing_code_resources", "_CodeSignature/CodeResources", "zsign output does not contain the expected code-signature resources"))
	}
	if !result.Valid() {
		return result, nil
	}
	canonicalIPA := filepath.Join(stagingRoot, "canonical.ipa")
	if err := canonicalizeIPA(ctx, zsignIPA, canonicalIPA, rawInspection.Value.Bundle.AppDirectory, rawInspection.Value.Bundle.Executable, limits); err != nil {
		return result, err
	}
	inspection, err := InspectIPA(ctx, canonicalIPA, InspectionOptions{Limits: limits, TrustedRootPaths: snapshotConfig.TrustedRootPaths, CurrentTime: req.CurrentTime})
	if err != nil {
		return result, err
	}
	result.Problems = append(result.Problems, inspection.Problems...)
	result.Problems = append(result.Problems, expectedBundleProblems(inspection.Value.Bundle, req)...)
	if inspection.Value.Bundle.EmbeddedProfile == nil || inspection.Value.Bundle.EmbeddedProfile.SHA256 != identity.Profile.SHA256 {
		result.Problems = append(result.Problems, problem("output_profile_mismatch", "embedded.mobileprovision", "canonical output profile bytes do not match the selected provisioning profile"))
	}
	if !result.Valid() {
		return result, nil
	}
	if _, err := os.Lstat(req.OutputIPA); err == nil {
		return result, errors.New("output IPA appeared during export; refusing to overwrite it")
	} else if !os.IsNotExist(err) {
		return result, err
	}
	if err := os.Link(canonicalIPA, req.OutputIPA); err != nil {
		return result, fmt.Errorf("publish output atomically without replacement: %w", err)
	}
	_ = os.Remove(canonicalIPA)
	result.Value = ExportReceipt{SHA256: inspection.Value.SHA256, Size: inspection.Value.Size, Identity: identity, IPA: inspection.Value, StructureValid: inspection.Value.StructureValid, SignatureValid: identity.Profile.SignatureValid, ChainTrust: identity.Profile.Trust, AppleProcessing: "not_checked"}
	return result, nil
}
