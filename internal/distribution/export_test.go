package distribution

import (
	"context"
	"encoding/json"
	"errors"
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
	t               *testing.T
	failTool        string
	skipOutput      bool
	profileOverride string
	actions         []Action
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
		if err := os.WriteFile(filepath.Join(app, "AppIcon60x60.png"), []byte("synthetic-icon"), 0o600); err != nil {
			r.t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(app, "AppIcon76x76.png"), []byte("synthetic-icon"), 0o600); err != nil {
			r.t.Fatal(err)
		}
	case "zsign":
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
		if entitlements["application-identifier"] != "TEAM123456.com.example.Orchard" || entitlements["get-task-allow"] != false {
			r.t.Fatalf("unexpected exact entitlements: %#v", entitlements)
		}
		if r.skipOutput {
			return nil
		}
		profile := action.Args[5]
		if r.profileOverride != "" {
			profile = r.profileOverride
		}
		zipBundle(r.t, action.Args[10], action.Args[9], profile)
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
		if err := os.Remove(filepath.Join(source, "AppIcon60x60.png")); err != nil {
			t.Fatal(err)
		}
		if err := os.Remove(filepath.Join(source, "AppIcon76x76.png")); err != nil {
			t.Fatal(err)
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
}

func TestExportPublicPlanRedactsPrivatePaths(t *testing.T) {
	fixture := newExportFixture(t, true)
	plan, err := PlanExport(context.Background(), fixture.request)
	if err != nil {
		t.Fatal(err)
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
	_, err := Export(context.Background(), fixture.request, runner)
	if err == nil || !strings.Contains(err.Error(), "unsupported signing topology") {
		t.Fatalf("topology error=%v", err)
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
}
