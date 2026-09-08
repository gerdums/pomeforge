package distribution

import (
	"archive/zip"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"howett.net/plist"
)

// syntheticZsignRunner is intentionally a subprocess-contract fixture. It
// creates an archive but does not create a real Apple code signature.
type syntheticZsignRunner struct {
	t                *testing.T
	failTool         string
	skipOutput       bool
	profileOverride  string
	expectedAppID    string
	expectedKeychain string
	beforeZsign      func(Action)
	extraEntries     []zipEntry
	actions          []Action
}

func (r *syntheticZsignRunner) Run(_ context.Context, action Action) error {
	r.actions = append(r.actions, Action{Executable: action.Executable, Args: append([]string{}, action.Args...), Directory: action.Directory})
	tool := filepath.Base(action.Executable)
	if tool == r.failTool {
		return errors.New("synthetic tool failure")
	}
	switch tool {
	case "orchard-assets":
		wantPrefix := []string{"compile", "--catalog"}
		if len(action.Args) != 8 || !reflect.DeepEqual(action.Args[:2], wantPrefix) || action.Args[2] == "" || action.Args[3] != "--app" || action.Args[5] != "--minimum-ios" || action.Args[7] != "--json" {
			r.t.Fatalf("unexpected AssetKit argv: %#v", action.Args)
		}
		app := action.Args[4]
		for _, rel := range []string{"_CodeSignature", "CodeResources", "embedded.mobileprovision"} {
			if _, err := os.Lstat(filepath.Join(app, rel)); !os.IsNotExist(err) {
				r.t.Fatalf("staging signature artifact still present: %s", rel)
			}
		}
		if err := os.WriteFile(filepath.Join(app, "Assets.car"), []byte("synthetic-assetkit-output"), 0o600); err != nil {
			r.t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(app, "Info.plist"), fixtureInfo(r.t, false), 0o600); err != nil {
			r.t.Fatal(err)
		}
		writeCompiledIcons(r.t, app, "AppIcon")
	case "zsign":
		if r.beforeZsign != nil {
			r.beforeZsign(action)
		}
		if len(action.Args) != 11 || action.Args[0] != "-k" || action.Args[2] != "-c" || action.Args[4] != "-m" || action.Args[6] != "-e" || action.Args[8] != "-o" {
			r.t.Fatalf("unexpected zsign argv: %#v", action.Args)
		}
		entitlementData, err := os.ReadFile(action.Args[7])
		if err != nil {
			r.t.Fatal(err)
		}
		var entitlements map[string]any
		if _, err := plist.Unmarshal(entitlementData, &entitlements); err != nil {
			r.t.Fatal(err)
		}
		expectedAppID := r.expectedAppID
		if expectedAppID == "" {
			expectedAppID = "TEAM123456.com.example.Orchard"
		}
		if entitlements["application-identifier"] != expectedAppID || entitlements["get-task-allow"] != false {
			r.t.Fatalf("unexpected exact entitlements: %#v", entitlements)
		}
		if r.expectedKeychain != "" && !entitlementValueAllowed(entitlements["keychain-access-groups"], []any{r.expectedKeychain}) {
			r.t.Fatalf("keychain group does not preserve App ID prefix: %#v", entitlements)
		}
		if r.skipOutput {
			return nil
		}
		app := action.Args[10]
		if filepath.Base(filepath.Dir(app)) != "Payload" || filepath.Base(filepath.Dir(filepath.Dir(app))) != "archive" {
			r.t.Fatalf("zsign app is not staged under a dedicated archive/Payload root: %s", app)
		}
		archiveRoot := filepath.Dir(filepath.Dir(app))
		for _, privatePath := range action.Args[1:10] {
			if strings.HasPrefix(privatePath, archiveRoot+string(filepath.Separator)) {
				r.t.Fatalf("private signer input/output is inside archive root: %s", privatePath)
			}
		}
		profile := action.Args[5]
		if r.profileOverride != "" {
			profile = r.profileOverride
		}
		zipBundle(r.t, app, action.Args[9], profile, r.extraEntries...)
	}
	return nil
}

type exportFixture struct {
	request                                                                  ExportRequest
	source, output, config, key, cert, profile, root, zsign, assets, catalog string
	crypto                                                                   *cryptoFixture
}

func newExportFixture(t *testing.T, withCatalog bool) exportFixture {
	t.Helper()
	f := newCryptoFixture(t)
	privateDir := t.TempDir()
	projectDir := t.TempDir()
	outputDir := t.TempDir()
	profile := f.profile(t, profileOptions{})
	identity := f.writeIdentity(t, privateDir, profile, nil, nil, nil)
	source := writeBundle(t, projectDir, false)
	if err := os.Mkdir(filepath.Join(source, "_CodeSignature"), 0o700); err != nil {
		t.Fatal(err)
	}
	writePrivate(t, filepath.Join(source, "_CodeSignature", "CodeResources"), []byte("adhoc"))
	writePrivate(t, filepath.Join(source, "CodeResources"), []byte("adhoc"))
	writePrivate(t, filepath.Join(source, "embedded.mobileprovision"), []byte("old"))
	zsign := filepath.Join(privateDir, "zsign")
	writePrivate(t, zsign, []byte("fixture"))
	if err := os.Chmod(zsign, 0o700); err != nil {
		t.Fatal(err)
	}
	assets := filepath.Join(privateDir, "orchard-assets")
	writePrivate(t, assets, []byte("fixture"))
	if err := os.Chmod(assets, 0o700); err != nil {
		t.Fatal(err)
	}
	req := ExportRequest{SourceBundle: source, OutputIPA: filepath.Join(outputDir, "Orchard.ipa"), IdentityConfigPath: identity.config, ZsignExecutable: zsign, MinimumIOS: "17.0", ExpectedBundleID: "com.example.Orchard", ExpectedVersion: "1.2.3", ExpectedBuild: "42", CurrentTime: f.now}
	catalog := ""
	if withCatalog {
		catalog = writeIconCatalog(t, projectDir)
		req.AssetCatalogPath = catalog
		req.AssetCompilerExecutable = assets
		var sourceInfo map[string]any
		data, err := os.ReadFile(filepath.Join(source, "Info.plist"))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := plist.Unmarshal(data, &sourceInfo); err != nil {
			t.Fatal(err)
		}
		delete(sourceInfo, "CFBundleIcons")
		delete(sourceInfo, "CFBundleIcons~ipad")
		data, err = plist.Marshal(sourceInfo, plist.XMLFormat)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(source, "Info.plist"), data, 0o600); err != nil {
			t.Fatal(err)
		}
		for _, requirement := range compiledIconRequirements("AppIcon") {
			if err := os.Remove(filepath.Join(source, requirement.filename)); err != nil {
				t.Fatal(err)
			}
		}
	}
	return exportFixture{request: req, source: source, output: req.OutputIPA, config: identity.config, key: identity.key, cert: identity.cert, profile: identity.profile, root: identity.root, zsign: zsign, assets: assets, catalog: catalog, crypto: f}
}

func TestExportStagesAssetsSignsInspectsAndPreservesSource(t *testing.T) {
	fixture := newExportFixture(t, true)
	before := treeDigest(t, fixture.source)
	runner := &syntheticZsignRunner{t: t}
	result, err := Export(context.Background(), fixture.request, runner)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Valid() {
		t.Fatalf("export problems: %+v", result.Problems)
	}
	if result.Value.SHA256 == "" || !result.Value.StructureValid || !result.Value.SignatureValid || result.Value.ChainTrust != TrustVerified || result.Value.AppleProcessing != "not_checked" {
		t.Fatalf("unexpected receipt: %+v", result.Value)
	}
	if _, err := os.Lstat(fixture.output); err != nil {
		t.Fatalf("output absent: %v", err)
	}
	if after := treeDigest(t, fixture.source); after != before {
		t.Fatalf("source bundle changed: before=%s after=%s", before, after)
	}
	if len(runner.actions) != 2 || filepath.Base(runner.actions[0].Executable) != "orchard-assets" || filepath.Base(runner.actions[1].Executable) != "zsign" {
		t.Fatalf("unexpected action order: %+v", runner.actions)
	}
	archive, err := zip.OpenReader(fixture.output)
	if err != nil {
		t.Fatal(err)
	}
	defer archive.Close()
	for _, entry := range archive.File {
		if !strings.HasPrefix(entry.Name, "Payload/") && entry.Name != "Payload/" {
			t.Fatalf("published unsupported entry %q", entry.Name)
		}
		if strings.HasSuffix(entry.Name, "/") && entry.Mode().Perm() != 0o755 {
			t.Fatalf("directory %q mode=%04o", entry.Name, entry.Mode().Perm())
		}
		if entry.Name == "Payload/Orchard.app/Orchard" && entry.Mode().Perm() != 0o755 {
			t.Fatalf("main executable mode=%04o", entry.Mode().Perm())
		}
		if !strings.HasSuffix(entry.Name, "/") && entry.Name != "Payload/Orchard.app/Orchard" && entry.Mode().Perm() != 0o644 {
			t.Fatalf("resource %q mode=%04o", entry.Name, entry.Mode().Perm())
		}
	}
}

func TestPreflightAndExportRejectUnsafeExecutableMetadata(t *testing.T) {
	fixture := newExportFixture(t, false)
	if err := os.WriteFile(filepath.Join(fixture.source, "Info.plist"), fixtureInfoWith(t, "CFBundleExecutable", "../../outside"), 0o600); err != nil {
		t.Fatal(err)
	}
	preflight, err := PreflightExport(context.Background(), fixture.request)
	if err != nil {
		t.Fatal(err)
	}
	if preflight.Valid() || !hasProblem(preflight.Problems, "invalid_executable_name") {
		t.Fatalf("unsafe executable metadata passed preflight: %+v", preflight.Problems)
	}
	runner := &syntheticZsignRunner{t: t}
	result, err := Export(context.Background(), fixture.request, runner)
	if err != nil {
		t.Fatal(err)
	}
	if result.Valid() || !hasProblem(result.Problems, "invalid_executable_name") || len(runner.actions) != 0 {
		t.Fatalf("unsafe executable metadata reached export actions: result=%+v actions=%+v", result, runner.actions)
	}
	if _, err := os.Lstat(fixture.output); !os.IsNotExist(err) {
		t.Fatalf("unsafe export published output: %v", err)
	}
}

func TestExportRejectsSignerArchivePathKindConflict(t *testing.T) {
	fixture := newExportFixture(t, false)
	runner := &syntheticZsignRunner{t: t, extraEntries: []zipEntry{{name: "Payload", data: []byte("file"), mode: 0o600}}}
	result, err := Export(context.Background(), fixture.request, runner)
	if err != nil {
		t.Fatal(err)
	}
	if result.Valid() || !hasProblem(result.Problems, "archive_file_ancestor") {
		t.Fatalf("signer path-kind conflict accepted: %+v", result.Problems)
	}
	if _, err := os.Lstat(fixture.output); !os.IsNotExist(err) {
		t.Fatalf("malformed signer output was published: %v", err)
	}
}

func TestCanonicalizeIPARejectsArchivePathKindConflict(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "source.ipa")
	destination := filepath.Join(dir, "canonical.ipa")
	writeIPA(t, source, []zipEntry{
		{name: "Payload", data: []byte("file"), mode: 0o600},
		{name: "Payload/Orchard.app/Info.plist", data: fixtureInfo(t, false), mode: 0o600},
	})
	err := canonicalizeIPA(context.Background(), source, destination, "Payload/Orchard.app", "Orchard", DefaultLimits())
	if err == nil || !strings.Contains(err.Error(), "archive_file_ancestor") {
		t.Fatalf("canonicalization error=%v", err)
	}
	if _, statErr := os.Lstat(destination); !os.IsNotExist(statErr) {
		t.Fatalf("canonicalization created output: %v", statErr)
	}
}

func TestExportPublicPlanRedactsPrivatePaths(t *testing.T) {
	fixture := newExportFixture(t, true)
	plan, err := PlanExport(context.Background(), fixture.request)
	if err != nil {
		t.Fatal(err)
	}
	if !plan.Valid() {
		t.Fatalf("ready export plan is invalid: %+v", plan.Problems)
	}
	encoded := mustJSON(t, plan)
	for _, secretPath := range []string{fixture.config, fixture.key, fixture.cert, fixture.profile, fixture.root} {
		if strings.Contains(encoded, secretPath) {
			t.Fatalf("plan leaks private path %q: %s", secretPath, encoded)
		}
	}
	if !strings.Contains(encoded, "private-key") || !strings.Contains(encoded, "--minimum-ios") {
		t.Fatalf("plan omitted contract: %s", encoded)
	}
}

func TestPreflightExportAggregatesReadinessWithoutMutation(t *testing.T) {
	fixture := newExportFixture(t, true)
	staleSignature := filepath.Join(fixture.source, "_CodeSignature")
	if err := os.RemoveAll(staleSignature); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(fixture.source, "Info.plist"), staleSignature); err != nil {
		t.Fatal(err)
	}
	before := treeDigest(t, fixture.source)
	preflight, err := PreflightExport(context.Background(), fixture.request)
	if err != nil {
		t.Fatal(err)
	}
	if !preflight.Valid() || !preflight.Value.AssetCompilationRequired {
		t.Fatalf("catalog-backed unsigned bundle was not ready: %+v", preflight)
	}
	if after := treeDigest(t, fixture.source); after != before {
		t.Fatal("preflight mutated the source bundle")
	}
	if _, err := os.Lstat(fixture.output); !os.IsNotExist(err) {
		t.Fatalf("preflight created output: %v", err)
	}

	missingSigner := fixture.request
	missingSigner.ZsignExecutable = filepath.Join(filepath.Dir(fixture.zsign), "missing-zsign")
	plan, err := PlanExport(context.Background(), missingSigner)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Valid() || !hasProblem(plan.Problems, "invalid_zsign") {
		t.Fatalf("missing signer produced runnable plan: %+v", plan)
	}

	missingIdentity := fixture.request
	missingIdentity.IdentityConfigPath = filepath.Join(filepath.Dir(fixture.config), "missing-config.json")
	if _, err := PlanExport(context.Background(), missingIdentity); err == nil {
		t.Fatal("missing identity produced a plan")
	}
}

func TestExportUsesImmutableIdentitySnapshots(t *testing.T) {
	fixture := newExportFixture(t, false)
	originalProfile, err := os.ReadFile(fixture.profile)
	if err != nil {
		t.Fatal(err)
	}
	runner := &syntheticZsignRunner{t: t, beforeZsign: func(action Action) {
		for _, original := range []string{fixture.key, fixture.cert, fixture.profile} {
			for _, arg := range action.Args {
				if arg == original {
					t.Fatalf("zsign received mutable original identity path %q", original)
				}
			}
		}
		writePrivate(t, fixture.key, []byte("changed-key"))
		writePrivate(t, fixture.cert, []byte("changed-cert"))
		writePrivate(t, fixture.profile, []byte("changed-profile"))
	}}
	result, err := Export(context.Background(), fixture.request, runner)
	if err != nil || !result.Valid() {
		t.Fatalf("snapshot-bound export failed: result=%+v err=%v", result, err)
	}
	if result.Value.Identity.Profile.SHA256 != fmt.Sprintf("%x", sha256.Sum256(originalProfile)) {
		t.Fatal("receipt did not bind the snapshotted profile")
	}
}

func TestExportRejectsConflictingRequiredEntitlements(t *testing.T) {
	for _, entitlement := range []map[string]any{
		{"application-identifier": "WRONG.com.example.Orchard"},
		{"com.apple.developer.team-identifier": "WRONGTEAM"},
		{"get-task-allow": true},
	} {
		fixture := newExportFixture(t, false)
		fixture.request.Entitlements = entitlement
		plan, err := PlanExport(context.Background(), fixture.request)
		if err != nil {
			t.Fatal(err)
		}
		if plan.Valid() || !hasProblem(plan.Problems, "required_entitlement_conflict") {
			t.Fatalf("conflicting entitlement accepted: %+v", plan.Problems)
		}
	}
}

func TestExportPreservesDistinctApplicationIdentifierPrefix(t *testing.T) {
	fixture := newExportFixture(t, false)
	prefix := "LEGACY1234"
	writePrivate(t, fixture.profile, fixture.crypto.profile(t, profileOptions{prefix: prefix}))
	runner := &syntheticZsignRunner{t: t, expectedAppID: prefix + ".com.example.Orchard", expectedKeychain: prefix + ".com.example.Orchard"}
	result, err := Export(context.Background(), fixture.request, runner)
	if err != nil || !result.Valid() {
		t.Fatalf("distinct-prefix export failed: result=%+v err=%v", result, err)
	}
	if result.Value.Identity.Profile.ApplicationIdentifierPrefix != prefix || result.Value.Identity.Profile.TeamID != "TEAM123456" {
		t.Fatalf("prefix/team were conflated: %+v", result.Value.Identity.Profile)
	}
}

func TestExportRejectsMalformedOrIncompletePrecompiledIcons(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(exportFixture)
	}{
		{"missing", func(f exportFixture) { _ = os.Remove(filepath.Join(f.source, "AppIcon1024x1024.png")) }},
		{"malformed", func(f exportFixture) {
			writePrivate(t, filepath.Join(f.source, "AppIcon60x60@3x.png"), []byte("not-png"))
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fixture := newExportFixture(t, false)
			tc.mutate(fixture)
			plan, err := PlanExport(context.Background(), fixture.request)
			if err != nil {
				t.Fatal(err)
			}
			if plan.Valid() || (!hasProblem(plan.Problems, "missing_icon_file") && !hasProblem(plan.Problems, "invalid_icon_png")) {
				t.Fatalf("bad precompiled icons accepted: %+v", plan.Problems)
			}
		})
	}
}

func TestExportRejectsExtraneousSignerOutput(t *testing.T) {
	fixture := newExportFixture(t, false)
	runner := &syntheticZsignRunner{t: t, extraEntries: []zipEntry{{name: "identity/private-key.pem", data: []byte("secret"), mode: 0o600}}}
	result, err := Export(context.Background(), fixture.request, runner)
	if err != nil {
		t.Fatal(err)
	}
	if result.Valid() || !hasProblem(result.Problems, "unsupported_archive_entry") {
		t.Fatalf("extraneous signer output accepted: %+v", result.Problems)
	}
	if _, err := os.Lstat(fixture.output); !os.IsNotExist(err) {
		t.Fatalf("invalid signer output published: %v", err)
	}
}

func TestExportRacePreservesAppearingOutput(t *testing.T) {
	fixture := newExportFixture(t, false)
	kept := []byte("created concurrently")
	runner := &syntheticZsignRunner{t: t, beforeZsign: func(Action) { writePrivate(t, fixture.output, kept) }}
	if _, err := Export(context.Background(), fixture.request, runner); err == nil {
		t.Fatal("concurrently appearing output was overwritten")
	}
	after, err := os.ReadFile(fixture.output)
	if err != nil || !reflect.DeepEqual(after, kept) {
		t.Fatalf("appearing output changed: %q err=%v", after, err)
	}
}

func TestExportSignerFailuresLeaveNoOutput(t *testing.T) {
	for _, tc := range []struct {
		name, fail string
		missing    bool
	}{{"failed", "zsign", false}, {"missing output", "", true}} {
		t.Run(tc.name, func(t *testing.T) {
			fixture := newExportFixture(t, false)
			runner := &syntheticZsignRunner{t: t, failTool: tc.fail, skipOutput: tc.missing}
			_, err := Export(context.Background(), fixture.request, runner)
			if err == nil {
				t.Fatal("signer failure accepted")
			}
			if _, statErr := os.Lstat(fixture.output); !os.IsNotExist(statErr) {
				t.Fatalf("output exists after failure: %v", statErr)
			}
		})
	}
}

func TestExportRejectsSignerOutputWithDifferentProfile(t *testing.T) {
	fixture := newExportFixture(t, false)
	alternate := filepath.Join(t.TempDir(), "alternate.mobileprovision")
	writePrivate(t, alternate, fixture.crypto.profile(t, profileOptions{uuid: "11111111-2222-3333-4444-555555555555"}))
	runner := &syntheticZsignRunner{t: t, profileOverride: alternate}
	result, err := Export(context.Background(), fixture.request, runner)
	if err != nil {
		t.Fatal(err)
	}
	if !hasProblem(result.Problems, "output_profile_mismatch") {
		t.Fatalf("different output profile accepted: %+v", result.Problems)
	}
	if _, err := os.Lstat(fixture.output); !os.IsNotExist(err) {
		t.Fatalf("invalid output published: %v", err)
	}
}

func TestExportPreservesExistingOutput(t *testing.T) {
	fixture := newExportFixture(t, false)
	original := []byte("keep me")
	writePrivate(t, fixture.output, original)
	runner := &syntheticZsignRunner{t: t}
	if _, err := Export(context.Background(), fixture.request, runner); err == nil {
		t.Fatal("existing output accepted")
	}
	after, err := os.ReadFile(fixture.output)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(after, original) {
		t.Fatal("existing output changed")
	}
	if len(runner.actions) != 0 {
		t.Fatal("runner called despite existing output")
	}
}

func TestExportRejectsUnsupportedTopology(t *testing.T) {
	fixture := newExportFixture(t, false)
	framework := filepath.Join(fixture.source, "Frameworks", "Kit.framework")
	if err := os.MkdirAll(framework, 0o700); err != nil {
		t.Fatal(err)
	}
	writePrivate(t, filepath.Join(framework, "Kit"), []byte("binary"))
	runner := &syntheticZsignRunner{t: t}
	result, err := Export(context.Background(), fixture.request, runner)
	if err != nil || !hasProblem(result.Problems, "unsupported_signing_topology") {
		t.Fatalf("topology result=%+v error=%v", result, err)
	}
	if len(runner.actions) != 0 {
		t.Fatal("runner called for unsupported topology")
	}
}

func TestExportRejectsSourceSymlink(t *testing.T) {
	fixture := newExportFixture(t, false)
	if err := os.Symlink(filepath.Join(fixture.source, "Info.plist"), filepath.Join(fixture.source, "linked-info")); err != nil {
		t.Fatal(err)
	}
	runner := &syntheticZsignRunner{t: t}
	_, err := Export(context.Background(), fixture.request, runner)
	if err == nil || !strings.Contains(err.Error(), "nonregular") {
		t.Fatalf("symlink error=%v", err)
	}
	if len(runner.actions) != 0 {
		t.Fatal("runner called for symlink source")
	}
}

func TestExportRejectsStoreIdentityAndBundleFailures(t *testing.T) {
	cases := []struct {
		name    string
		profile profileOptions
		mutate  func(*ExportRequest)
	}{
		{"wrong profile bundle", profileOptions{bundle: "com.example.Other"}, nil},
		{"development", profileOptions{devicesPresent: true, devices: []string{"UDID"}, getTaskAllow: true}, nil},
		{"ad-hoc", profileOptions{devicesPresent: true}, nil},
		{"enterprise", profileOptions{enterprise: true}, nil},
		{"wrong team", profileOptions{extraEntitlements: map[string]any{"com.apple.developer.team-identifier": "WRONGTEAM"}}, nil},
		{"wrong expected version", profileOptions{}, func(r *ExportRequest) { r.ExpectedVersion = "9.9" }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fixture := newExportFixture(t, false)
			cms := fixture.crypto.profile(t, tc.profile)
			writePrivate(t, fixture.profile, cms)
			if tc.mutate != nil {
				tc.mutate(&fixture.request)
			}
			runner := &syntheticZsignRunner{t: t}
			result, err := Export(context.Background(), fixture.request, runner)
			if err != nil {
				t.Fatal(err)
			}
			if result.Valid() {
				t.Fatalf("invalid export accepted: %+v", result.Value)
			}
			if len(runner.actions) != 0 {
				t.Fatal("runner called for invalid export")
			}
		})
	}
}

func TestEntitlementSubsetAndCatalogCoverage(t *testing.T) {
	profile := map[string]any{"application-identifier": "TEAM.*", "aps-environment": "production", "keychain-access-groups": []any{"TEAM.*"}}
	valid := map[string]any{"application-identifier": "TEAM.com.example.App", "keychain-access-groups": []any{"TEAM.com.example.App"}}
	if problems := ValidateEntitlements(profile, valid); len(problems) != 0 {
		t.Fatalf("valid wildcard subset rejected: %+v", problems)
	}
	invalid := map[string]any{"mystery.capability": true, "aps-environment": "development"}
	problems := ValidateEntitlements(profile, invalid)
	if !hasProblem(problems, "unsupported_entitlement") || !hasProblem(problems, "entitlement_value_not_granted") {
		t.Fatalf("invalid entitlements accepted: %+v", problems)
	}
	catalog := writeIconCatalog(t, t.TempDir())
	contents := filepath.Join(catalog, "AppIcon.appiconset", "Contents.json")
	data, err := os.ReadFile(contents)
	if err != nil {
		t.Fatal(err)
	}
	var c catalogContents
	if err := json.Unmarshal(data, &c); err != nil {
		t.Fatal(err)
	}
	c.Images = c.Images[1:]
	data, _ = json.Marshal(c)
	if err := os.WriteFile(contents, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if problems := validateIconCatalog(context.Background(), catalog, DefaultLimits()); !hasProblem(problems, "missing_icon_slot") {
		t.Fatalf("incomplete catalog accepted: %+v", problems)
	}
	if got := len(compiledIconRequirements("AppIcon")); got != 15 {
		t.Fatalf("AssetKit's 18 supported slots must map to 15 distinct loose PNG filenames, got %d", got)
	}
}
