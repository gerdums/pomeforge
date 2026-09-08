package distribution

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func hasProblem(problems []Problem, code string) bool {
	for _, p := range problems {
		if p.Code == code {
			return true
		}
	}
	return false
}

func TestTrustedProfileAndIdentity(t *testing.T) {
	f := newCryptoFixture(t)
	dir := t.TempDir()
	cms := f.profile(t, profileOptions{binary: true})
	paths := f.writeIdentity(t, dir, cms, nil, nil, nil)
	result, err := InspectConfiguredIdentity(context.Background(), paths.config, f.now, Limits{})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Valid() {
		t.Fatalf("unexpected problems: %+v", result.Problems)
	}
	if !result.Value.Certificate.MatchesPrivateKey || !result.Value.CertificateInProfile {
		t.Fatalf("identity mismatch: %+v", result.Value)
	}
	p := result.Value.Profile
	if !p.SignatureValid || p.Trust != TrustVerified || p.Type != ProfileAppStore || p.BundleIdentifier != "com.example.Pomeforge" || p.TeamID != "TEAM123456" {
		t.Fatalf("unexpected profile: %+v", p)
	}
	if p.HasProvisionedDevices {
		t.Fatal("device-list key unexpectedly present")
	}
	if strings.Contains(mustJSON(t, result), paths.key) || strings.Contains(mustJSON(t, result), paths.profile) {
		t.Fatal("public identity report leaked private path")
	}
}

func TestIdentityKeepsApplicationPrefixSeparateFromTeam(t *testing.T) {
	f := newCryptoFixture(t)
	dir := t.TempDir()
	paths := f.writeIdentity(t, dir, f.profile(t, profileOptions{prefix: "LEGACY1234"}), nil, nil, nil)
	result, err := InspectConfiguredIdentity(context.Background(), paths.config, f.now, Limits{})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Valid() {
		t.Fatalf("legacy App ID prefix rejected: %+v", result.Problems)
	}
	profile := result.Value.Profile
	if profile.ApplicationIdentifierPrefix != "LEGACY1234" || profile.TeamID != "TEAM123456" || profile.BundleIdentifier != "com.example.Pomeforge" {
		t.Fatalf("prefix/team parsing is incorrect: %+v", profile)
	}
}

func TestProfileTrustIsSeparateFromSignature(t *testing.T) {
	f := newCryptoFixture(t)
	dir := t.TempDir()
	cms := f.profile(t, profileOptions{})
	profilePath := filepath.Join(dir, "profile")
	writePrivate(t, profilePath, cms)
	withoutRoots, err := InspectProfile(context.Background(), profilePath, nil, f.now, Limits{})
	if err != nil {
		t.Fatal(err)
	}
	if !withoutRoots.Value.SignatureValid || withoutRoots.Value.Trust != TrustNoRoots {
		t.Fatalf("unexpected no-root result: %+v", withoutRoots.Value)
	}
	other := newCryptoFixture(t)
	wrongRoot := filepath.Join(dir, "wrong-root.pem")
	writePrivate(t, wrongRoot, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: other.root.Raw}))
	untrusted, err := InspectProfile(context.Background(), profilePath, []string{wrongRoot}, f.now, Limits{})
	if err != nil {
		t.Fatal(err)
	}
	if !untrusted.Value.SignatureValid || untrusted.Value.Trust != TrustUntrusted || untrusted.Value.TrustError == "" {
		t.Fatalf("signature and trust were not separated: %+v", untrusted.Value)
	}
}

func TestTamperedCMSIsRejected(t *testing.T) {
	f := newCryptoFixture(t)
	dir := t.TempDir()
	cms := f.profile(t, profileOptions{})
	cms[len(cms)-20] ^= 0xff
	profilePath := filepath.Join(dir, "profile")
	writePrivate(t, profilePath, cms)
	result, err := InspectProfile(context.Background(), profilePath, nil, f.now, Limits{})
	if err != nil {
		t.Fatal(err)
	}
	if result.Valid() || !hasProblem(result.Problems, "profile_invalid") {
		t.Fatalf("tampered CMS accepted: %+v", result)
	}
}

func TestIdentityFailures(t *testing.T) {
	t.Run("wrong key", func(t *testing.T) {
		f := newCryptoFixture(t)
		other := newCryptoFixture(t)
		dir := t.TempDir()
		paths := f.writeIdentity(t, dir, f.profile(t, profileOptions{}), other.signingKey, nil, nil)
		result, err := InspectConfiguredIdentity(context.Background(), paths.config, f.now, Limits{})
		if err != nil {
			t.Fatal(err)
		}
		if !hasProblem(result.Problems, "key_certificate_mismatch") {
			t.Fatalf("missing mismatch: %+v", result.Problems)
		}
	})
	t.Run("certificate absent from profile", func(t *testing.T) {
		f := newCryptoFixture(t)
		other := newCryptoFixture(t)
		dir := t.TempDir()
		paths := f.writeIdentity(t, dir, f.profile(t, profileOptions{developerCert: other.signingCert}), nil, nil, nil)
		result, err := InspectConfiguredIdentity(context.Background(), paths.config, f.now, Limits{})
		if err != nil {
			t.Fatal(err)
		}
		if !hasProblem(result.Problems, "certificate_not_in_profile") {
			t.Fatalf("missing membership failure: %+v", result.Problems)
		}
	})
	t.Run("expired certificate and profile", func(t *testing.T) {
		f := newCryptoFixture(t)
		dir := t.TempDir()
		paths := f.writeIdentity(t, dir, f.profile(t, profileOptions{expiry: f.now.Add(-time.Minute)}), nil, nil, nil)
		result, err := InspectConfiguredIdentity(context.Background(), paths.config, f.now, Limits{})
		if err != nil {
			t.Fatal(err)
		}
		if !hasProblem(result.Problems, "profile_expired") {
			t.Fatalf("missing expiry: %+v", result.Problems)
		}
	})
	t.Run("expired certificate", func(t *testing.T) {
		f := newCryptoFixture(t)
		dir := t.TempDir()
		paths := f.writeIdentity(t, dir, f.profile(t, profileOptions{expiry: f.now.Add(60 * 24 * time.Hour)}), nil, nil, nil)
		result, err := InspectConfiguredIdentity(context.Background(), paths.config, f.now.Add(31*24*time.Hour), Limits{})
		if err != nil {
			t.Fatal(err)
		}
		if !hasProblem(result.Problems, "certificate_not_current") {
			t.Fatalf("missing certificate expiry: %+v", result.Problems)
		}
	})
}

func TestProfileTypesDetectEmptyDeviceList(t *testing.T) {
	f := newCryptoFixture(t)
	limits := DefaultLimits()
	cases := []struct {
		name    string
		options profileOptions
		want    ProfileType
		devices bool
	}{
		{"store", profileOptions{}, ProfileAppStore, false},
		{"development", profileOptions{devicesPresent: true, devices: []string{"UDID"}, getTaskAllow: true}, ProfileDevelopment, true},
		{"ad-hoc-empty", profileOptions{devicesPresent: true, devices: []string{}}, ProfileAdHoc, true},
		{"enterprise", profileOptions{enterprise: true}, ProfileEnterprise, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			report, _, err := inspectProfileData(context.Background(), f.profile(t, tc.options), nil, f.now, limits)
			if err != nil {
				t.Fatal(err)
			}
			if report.Type != tc.want || report.HasProvisionedDevices != tc.devices {
				t.Fatalf("got type=%s devices=%v; want %s/%v", report.Type, report.HasProvisionedDevices, tc.want, tc.devices)
			}
		})
	}
}

func TestEncryptedPrivateKeyRejected(t *testing.T) {
	f := newCryptoFixture(t)
	dir := t.TempDir()
	paths := f.writeIdentity(t, dir, f.profile(t, profileOptions{}), nil, nil, nil)
	block, err := x509.EncryptPEMBlock(rand.Reader, "RSA PRIVATE KEY", x509.MarshalPKCS1PrivateKey(f.signingKey), []byte("secret"), x509.PEMCipherAES256)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(paths.key, pem.EncodeToMemory(block), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err = InspectConfiguredIdentity(context.Background(), paths.config, f.now, Limits{})
	if err == nil || !strings.Contains(err.Error(), "encrypted private keys are unsupported") || strings.Contains(err.Error(), "secret") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestPrivateKeyPEMFormats(t *testing.T) {
	rsaKey := mustRSA(t)
	pkcs8RSA, err := x509.MarshalPKCS8PrivateKey(rsaKey)
	if err != nil {
		t.Fatal(err)
	}
	ecKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	ecDER, err := x509.MarshalECPrivateKey(ecKey)
	if err != nil {
		t.Fatal(err)
	}
	pkcs8EC, err := x509.MarshalPKCS8PrivateKey(ecKey)
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name, block string
		der         []byte
	}{
		{"pkcs1-rsa", "RSA PRIVATE KEY", x509.MarshalPKCS1PrivateKey(rsaKey)},
		{"pkcs8-rsa", "PRIVATE KEY", pkcs8RSA},
		{"sec1-ec", "EC PRIVATE KEY", ecDER},
		{"pkcs8-ec", "PRIVATE KEY", pkcs8EC},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			key, err := parsePEMPrivateKey(pem.EncodeToMemory(&pem.Block{Type: tc.block, Bytes: tc.der}))
			if err != nil {
				t.Fatal(err)
			}
			if key == nil {
				t.Fatal("nil key")
			}
		})
	}
}
