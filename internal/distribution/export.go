package distribution

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
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

// PlanExport returns the exact public tool contract with private arguments
// redacted. It performs no filesystem mutation.
func PlanExport(ctx context.Context, req ExportRequest) (Result[ExportPlan], error) {
	var out Result[ExportPlan]
	if err := requireLinux("distribution export planning"); err != nil {
		return out, err
	}
	if err := checkContext(ctx); err != nil {
		return out, err
	}
	if err := validateExportRequest(req); err != nil {
		return out, err
	}
	out.Value = ExportPlan{SourceBundle: req.SourceBundle, OutputIPA: req.OutputIPA, BundleID: req.ExpectedBundleID, Version: req.ExpectedVersion, Build: req.ExpectedBuild}
	catalogPath := req.AssetCatalogPath
	if catalogPath == "" {
		catalogs, err := discoverCatalogs(req.SourceBundle)
		if err != nil {
			return out, err
		}
		if len(catalogs) == 1 {
			catalogPath = catalogs[0]
		}
		if len(catalogs) > 1 {
			out.Problems = append(out.Problems, problem("multiple_asset_catalogs", "assetCatalogPath", "multiple catalogs found; select one explicitly"))
		}
	}
	if catalogPath != "" {
		if req.AssetCompilerExecutable == "" {
			out.Problems = append(out.Problems, problem("missing_asset_compiler", "assetCompilerExecutable", "orchard-assets executable is required when a catalog is present"))
		}
		out.Value.Actions = append(out.Value.Actions, PublicAction{Tool: filepath.Base(req.AssetCompilerExecutable), Args: []string{"compile", "--catalog", "<validated-catalog>", "--app", "<private-staged-app>", "--minimum-ios", req.MinimumIOS, "--json"}})
	}
	out.Value.Actions = append(out.Value.Actions, PublicAction{Tool: filepath.Base(req.ZsignExecutable), Args: []string{"-k", "<private-key>", "-c", "<certificate>", "-m", "<provisioning-profile>", "-e", "<private-generated-entitlements>", "-o", "<private-staged-output.ipa>", "<private-staged-app>"}})
	return out, nil
}

func safeCopyTree(ctx context.Context, source, destination string, limits Limits, rejectTopology bool) error {
	rootInfo, err := os.Lstat(source)
	if err != nil {
		return err
	}
	if !rootInfo.IsDir() || rootInfo.Mode()&os.ModeSymlink != 0 {
		return errors.New("source must be a real directory, not a symlink")
	}
	if err := os.Mkdir(destination, 0o700); err != nil {
		return err
	}
	entries := 0
	var total int64
	return filepath.WalkDir(source, func(name string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := checkContext(ctx); err != nil {
			return err
		}
		if name == source {
			return nil
		}
		entries++
		if entries > limits.MaxArchiveEntries {
			return fmt.Errorf("bundle exceeds %d-entry limit", limits.MaxArchiveEntries)
		}
		rel, err := filepath.Rel(source, name)
		if err != nil || rel == "." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return errors.New("bundle path escaped source")
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 || (!info.IsDir() && !info.Mode().IsRegular()) {
			return fmt.Errorf("bundle contains unsupported nonregular entry %q", rel)
		}
		if rejectTopology {
			parts := strings.Split(filepath.ToSlash(rel), "/")
			for _, part := range parts {
				if part == "PlugIns" || part == "Extensions" || part == "Frameworks" || part == "Watch" || strings.HasSuffix(part, ".appex") || strings.HasSuffix(part, ".framework") {
					return fmt.Errorf("unsupported signing topology at %q: extensions/frameworks require separate signing identities and profiles", rel)
				}
			}
		}
		target := filepath.Join(destination, rel)
		if info.IsDir() {
			return os.Mkdir(target, 0o700)
		}
		if info.Size() > limits.MaxFileBytes {
			return fmt.Errorf("bundle file %q exceeds per-file limit", rel)
		}
		total += info.Size()
		if total > limits.MaxBundleBytes {
			return fmt.Errorf("bundle exceeds %d-byte limit", limits.MaxBundleBytes)
		}
		in, err := os.Open(name)
		if err != nil {
			return err
		}
		openedInfo, err := in.Stat()
		if err != nil || !openedInfo.Mode().IsRegular() || !os.SameFile(info, openedInfo) {
			_ = in.Close()
			return fmt.Errorf("bundle file %q changed during safe open", rel)
		}
		mode := os.FileMode(0o600)
		if info.Mode()&0o111 != 0 {
			mode = 0o700
		}
		out, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
		if err != nil {
			_ = in.Close()
			return err
		}
		copyErr := copyBounded(ctx, out, in, info.Size(), limits.MaxFileBytes)
		closeErr := out.Close()
		inCloseErr := in.Close()
		if copyErr != nil {
			return copyErr
		}
		if closeErr != nil {
			return closeErr
		}
		return inCloseErr
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

func discoverCatalogs(root string) ([]string, error) {
	var found []string
	err := filepath.WalkDir(root, func(name string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() && strings.HasSuffix(entry.Name(), ".xcassets") {
			found = append(found, name)
			return filepath.SkipDir
		}
		return nil
	})
	sort.Strings(found)
	return found, err
}

func removeStagedSigning(staged string) error {
	for _, rel := range []string{"_CodeSignature", "CodeResources", "embedded.mobileprovision"} {
		target := filepath.Join(staged, rel)
		if err := os.RemoveAll(target); err != nil {
			return err
		}
	}
	return nil
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

// Export stages and validates a bundle, optionally compiles a complete icon
// catalog, invokes zsign 1.1.2's exact contract, re-inspects the output, then
// atomically renames it to a previously absent destination.
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
	for _, p := range []string{req.SourceBundle, req.IdentityConfigPath, req.ZsignExecutable, filepath.Dir(req.OutputIPA)} {
		if err := rejectSymlinkPath(p, true); err != nil {
			return result, err
		}
	}
	if info, err := os.Lstat(req.OutputIPA); err == nil {
		_ = info
		return result, errors.New("output IPA already exists; export will not overwrite it")
	} else if !os.IsNotExist(err) {
		return result, err
	}
	for field, executable := range map[string]string{"zsignExecutable": req.ZsignExecutable, "assetCompilerExecutable": req.AssetCompilerExecutable} {
		if executable == "" {
			continue
		}
		info, err := os.Lstat(executable)
		if err != nil || !info.Mode().IsRegular() || info.Mode()&0o111 == 0 {
			return result, fmt.Errorf("%s must be an executable regular file", field)
		}
	}
	identityConfig, err := LoadSigningConfig(ctx, req.IdentityConfigPath, limits)
	if err != nil {
		return result, err
	}
	identity, err := InspectIdentity(ctx, identityConfig, req.CurrentTime, limits)
	if err != nil {
		return result, err
	}
	result.Problems = append(result.Problems, storeIdentityProblems(identity, req)...)
	exactEntitlements, entitlementProblems := exactSigningEntitlements(identity.Value.Profile.Entitlements, req.Entitlements, identity.Value.Profile.TeamID, req.ExpectedBundleID)
	result.Problems = append(result.Problems, entitlementProblems...)
	sourceInfo, err := os.Lstat(req.SourceBundle)
	if err != nil {
		return result, err
	}
	if !sourceInfo.IsDir() || !strings.HasSuffix(req.SourceBundle, ".app") {
		return result, errors.New("sourceBundle must be a real .app directory")
	}
	catalogPath := req.AssetCatalogPath
	if catalogPath == "" {
		catalogs, err := discoverCatalogs(req.SourceBundle)
		if err != nil {
			return result, err
		}
		if len(catalogs) == 1 {
			catalogPath = catalogs[0]
		}
		if len(catalogs) > 1 {
			result.Problems = append(result.Problems, problem("multiple_asset_catalogs", "assetCatalogPath", "multiple catalogs found; select one explicitly"))
		}
	}
	if catalogPath != "" {
		if req.AssetCompilerExecutable == "" {
			result.Problems = append(result.Problems, problem("missing_asset_compiler", "assetCompilerExecutable", "orchard-assets executable is required when a catalog is present"))
		}
		result.Problems = append(result.Problems, validateIconCatalog(ctx, catalogPath, limits)...)
	}
	if !result.Valid() {
		return result, nil
	}
	stagingRoot, err := os.MkdirTemp(filepath.Dir(req.OutputIPA), ".orchard-export-")
	if err != nil {
		return result, err
	}
	if err := os.Chmod(stagingRoot, 0o700); err != nil {
		_ = os.RemoveAll(stagingRoot)
		return result, err
	}
	defer os.RemoveAll(stagingRoot)
	stagedApp := filepath.Join(stagingRoot, filepath.Base(req.SourceBundle))
	if err := safeCopyTree(ctx, req.SourceBundle, stagedApp, limits, true); err != nil {
		return result, err
	}
	if err := removeStagedSigning(stagedApp); err != nil {
		return result, err
	}
	if catalogPath != "" {
		stagedCatalog := filepath.Join(stagingRoot, "catalog.xcassets")
		if err := safeCopyTree(ctx, catalogPath, stagedCatalog, limits, false); err != nil {
			return result, err
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
	stagedInspection, err := inspectBundleDirectory(ctx, stagedApp, limits)
	if err != nil {
		return result, err
	}
	result.Problems = append(result.Problems, stagedInspection.Problems...)
	result.Problems = append(result.Problems, expectedBundleProblems(stagedInspection.Value, req)...)
	if !result.Valid() {
		return result, nil
	}
	entitlementsPath := filepath.Join(stagingRoot, "entitlements.plist")
	entitlementData, err := plist.Marshal(exactEntitlements, plist.XMLFormat)
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
	stagedIPA := filepath.Join(stagingRoot, "signed.ipa")
	signAction := Action{Executable: req.ZsignExecutable, Args: []string{"-k", identityConfig.PrivateKeyPath, "-c", identityConfig.CertificatePath, "-m", identityConfig.ProvisioningProfilePath, "-e", entitlementsPath, "-o", stagedIPA, stagedApp}, Directory: stagingRoot}
	if err := runner.Run(ctx, signAction); err != nil {
		return result, errors.New("zsign failed: runner returned an error")
	}
	outputInfo, err := os.Lstat(stagedIPA)
	if err != nil {
		if os.IsNotExist(err) {
			return result, errors.New("zsign returned success but did not create output IPA")
		}
		return result, err
	}
	if !outputInfo.Mode().IsRegular() || outputInfo.Size() > limits.MaxIPABytes {
		return result, errors.New("zsign output is not a bounded regular IPA file")
	}
	inspection, err := InspectIPA(ctx, stagedIPA, InspectionOptions{Limits: limits, TrustedRootPaths: identityConfig.TrustedRootPaths, CurrentTime: req.CurrentTime})
	if err != nil {
		return result, err
	}
	result.Problems = append(result.Problems, inspection.Problems...)
	result.Problems = append(result.Problems, expectedBundleProblems(inspection.Value.Bundle, req)...)
	if inspection.Value.Bundle.EmbeddedProfile == nil || inspection.Value.Bundle.EmbeddedProfile.UUID != identity.Value.Profile.UUID {
		result.Problems = append(result.Problems, problem("output_profile_mismatch", "embedded.mobileprovision", "output does not embed the selected provisioning profile"))
	} else if inspection.Value.Bundle.EmbeddedProfile.SHA256 != identity.Value.Profile.SHA256 {
		result.Problems = append(result.Problems, problem("output_profile_mismatch", "embedded.mobileprovision", "output profile bytes do not match the selected provisioning profile"))
	}
	if !inspection.Value.Bundle.CodeSignature.CodeResourcesPresent {
		result.Problems = append(result.Problems, problem("missing_code_resources", "_CodeSignature/CodeResources", "zsign output does not contain the expected code-signature resources"))
	}
	if !result.Valid() {
		return result, nil
	}
	if _, err := os.Lstat(req.OutputIPA); err == nil {
		return result, errors.New("output IPA appeared during export; refusing to overwrite it")
	} else if !os.IsNotExist(err) {
		return result, err
	}
	if err := os.Link(stagedIPA, req.OutputIPA); err != nil {
		return result, fmt.Errorf("publish output atomically without replacement: %w", err)
	}
	_ = os.Remove(stagedIPA)
	result.Value = ExportReceipt{SHA256: inspection.Value.SHA256, Size: inspection.Value.Size, Identity: identity.Value, IPA: inspection.Value, StructureValid: inspection.Value.StructureValid, SignatureValid: identity.Value.Profile.SignatureValid, ChainTrust: identity.Value.Profile.Trust, AppleProcessing: "not_checked"}
	return result, nil
}
