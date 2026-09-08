package distribution

import (
	"bytes"
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"time"

	"github.com/smallstep/pkcs7"
	"howett.net/plist"
)

type profilePlist struct {
	UUID                        string         `plist:"UUID"`
	Name                        string         `plist:"Name"`
	CreationDate                time.Time      `plist:"CreationDate"`
	ExpirationDate              time.Time      `plist:"ExpirationDate"`
	TeamIdentifier              []string       `plist:"TeamIdentifier"`
	ApplicationIdentifierPrefix []string       `plist:"ApplicationIdentifierPrefix"`
	DeveloperCertificates       [][]byte       `plist:"DeveloperCertificates"`
	ProvisionedDevices          []string       `plist:"ProvisionedDevices"`
	ProvisionsAllDevices        bool           `plist:"ProvisionsAllDevices"`
	Entitlements                map[string]any `plist:"Entitlements"`
}

func parsePEMCertificate(data []byte) (*x509.Certificate, error) {
	block, rest := pem.Decode(data)
	if block == nil || block.Type != "CERTIFICATE" {
		return nil, errors.New("certificate must be PEM CERTIFICATE data")
	}
	if len(bytes.TrimSpace(rest)) != 0 {
		return nil, errors.New("certificate file must contain exactly one certificate")
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("parse certificate: %w", err)
	}
	return cert, nil
}

func parsePEMPrivateKey(data []byte) (crypto.PrivateKey, error) {
	block, rest := pem.Decode(data)
	if block == nil {
		return nil, errors.New("private key must be PEM data")
	}
	if len(bytes.TrimSpace(rest)) != 0 {
		return nil, errors.New("private key file must contain exactly one key")
	}
	if x509.IsEncryptedPEMBlock(block) || strings.Contains(block.Type, "ENCRYPTED") {
		return nil, errors.New("encrypted private keys are unsupported; provide a protected unencrypted PEM file (no password is accepted on argv)")
	}
	var key any
	var err error
	switch block.Type {
	case "PRIVATE KEY":
		key, err = x509.ParsePKCS8PrivateKey(block.Bytes)
	case "RSA PRIVATE KEY":
		key, err = x509.ParsePKCS1PrivateKey(block.Bytes)
	case "EC PRIVATE KEY":
		key, err = x509.ParseECPrivateKey(block.Bytes)
	default:
		return nil, fmt.Errorf("unsupported private key PEM type %q; use unencrypted PKCS#8, PKCS#1 RSA, or SEC1 EC", block.Type)
	}
	if err != nil {
		return nil, fmt.Errorf("parse private key: %w", err)
	}
	switch key.(type) {
	case *rsa.PrivateKey, *ecdsa.PrivateKey:
		return key, nil
	default:
		return nil, fmt.Errorf("unsupported private key algorithm %T; use RSA or ECDSA", key)
	}
}

func publicPart(key any) any {
	switch k := key.(type) {
	case crypto.Signer:
		return k.Public()
	case *rsa.PublicKey, *ecdsa.PublicKey:
		return k
	default:
		return nil
	}
}

func publicKeyFingerprint(key any) string {
	pub := publicPart(key)
	if pub == nil {
		return ""
	}
	b, err := x509.MarshalPKIXPublicKey(pub)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func certificateFingerprint(cert *x509.Certificate) string {
	sum := sha256.Sum256(cert.Raw)
	return hex.EncodeToString(sum[:])
}

func loadTrustRoots(ctx context.Context, paths []string, max int64) (*x509.CertPool, error) {
	pool := x509.NewCertPool()
	for _, path := range paths {
		if err := rejectSymlinkPath(path, true); err != nil {
			return nil, fmt.Errorf("validate trust root: %w", err)
		}
		data, err := readRegularFile(ctx, path, max)
		if err != nil {
			return nil, fmt.Errorf("read trust root: %w", err)
		}
		found := false
		for len(bytes.TrimSpace(data)) != 0 {
			block, rest := pem.Decode(data)
			if block == nil {
				return nil, errors.New("trust root file contains invalid PEM data")
			}
			if block.Type != "CERTIFICATE" {
				return nil, fmt.Errorf("trust root PEM block has type %q, want CERTIFICATE", block.Type)
			}
			cert, err := x509.ParseCertificate(block.Bytes)
			if err != nil {
				return nil, fmt.Errorf("parse trust root: %w", err)
			}
			pool.AddCert(cert)
			found = true
			data = rest
		}
		if !found {
			return nil, errors.New("trust root file has no certificates")
		}
	}
	return pool, nil
}

func inspectProfileData(ctx context.Context, data []byte, rootPaths []string, at time.Time, limits Limits) (ProfileReport, profilePlist, error) {
	var report ProfileReport
	var raw profilePlist
	if err := checkContext(ctx); err != nil {
		return report, raw, err
	}
	profileSum := sha256.Sum256(data)
	report.SHA256 = hex.EncodeToString(profileSum[:])
	p7, err := pkcs7.Parse(data)
	if err != nil {
		return report, raw, fmt.Errorf("parse provisioning profile CMS: %w", err)
	}
	if len(p7.Content) == 0 {
		return report, raw, errors.New("provisioning profile CMS has no signed content")
	}
	if int64(len(p7.Content)) > limits.MaxProfileBytes {
		return report, raw, fmt.Errorf("provisioning profile content exceeds %d-byte limit", limits.MaxProfileBytes)
	}
	signatureErr := p7.Verify()
	report.SignatureValid = signatureErr == nil
	if len(rootPaths) == 0 {
		report.Trust = TrustNoRoots
	} else {
		roots, rootErr := loadTrustRoots(ctx, rootPaths, limits.MaxProfileBytes)
		if rootErr != nil {
			return report, raw, rootErr
		}
		if err := p7.VerifyWithChainAtTime(roots, at); err != nil {
			report.Trust = TrustUntrusted
			report.TrustError = err.Error()
		} else {
			report.Trust = TrustVerified
		}
	}
	var presence map[string]any
	if _, err := plist.Unmarshal(p7.Content, &raw); err != nil {
		return report, raw, fmt.Errorf("decode provisioning profile plist: %w", err)
	}
	if _, err := plist.Unmarshal(p7.Content, &presence); err != nil {
		return report, raw, fmt.Errorf("decode provisioning profile keys: %w", err)
	}
	report.UUID = raw.UUID
	report.Name = raw.Name
	report.CreationDate = raw.CreationDate
	report.ExpirationDate = raw.ExpirationDate
	report.ProvisionsAllDevices = raw.ProvisionsAllDevices
	_, report.HasProvisionedDevices = presence["ProvisionedDevices"]
	report.DeviceCount = len(raw.ProvisionedDevices)
	report.Entitlements = cloneMap(raw.Entitlements)
	report.GetTaskAllow, _ = boolValue(raw.Entitlements["get-task-allow"])
	if len(raw.TeamIdentifier) > 0 {
		report.TeamID = raw.TeamIdentifier[0]
	}
	if team, ok := stringValue(raw.Entitlements["com.apple.developer.team-identifier"]); ok && report.TeamID == "" {
		report.TeamID = team
	}
	report.ApplicationIdentifier, _ = stringValue(raw.Entitlements["application-identifier"])
	if report.ApplicationIdentifier != "" && report.TeamID != "" && strings.HasPrefix(report.ApplicationIdentifier, report.TeamID+".") {
		report.BundleIdentifier = strings.TrimPrefix(report.ApplicationIdentifier, report.TeamID+".")
	}
	switch {
	case report.ProvisionsAllDevices:
		report.Type = ProfileEnterprise
	case report.HasProvisionedDevices && report.GetTaskAllow:
		report.Type = ProfileDevelopment
	case report.HasProvisionedDevices:
		report.Type = ProfileAdHoc
	default:
		report.Type = ProfileAppStore
	}
	for _, der := range raw.DeveloperCertificates {
		sum := sha256.Sum256(der)
		report.CertificateSHA256 = append(report.CertificateSHA256, hex.EncodeToString(sum[:]))
	}
	sort.Strings(report.CertificateSHA256)
	if signatureErr != nil {
		return report, raw, fmt.Errorf("verify provisioning profile signature: %w", signatureErr)
	}
	return report, raw, nil
}

func profileValidationProblems(report ProfileReport, at time.Time) []Problem {
	var problems []Problem
	if report.CreationDate.IsZero() || at.Before(report.CreationDate) {
		problems = append(problems, problem("profile_not_yet_valid", "CreationDate", "provisioning profile is not current at the requested time"))
	}
	if report.ExpirationDate.IsZero() || at.After(report.ExpirationDate) {
		problems = append(problems, problem("profile_expired", "ExpirationDate", "provisioning profile is expired at the requested time"))
	}
	if report.UUID == "" {
		problems = append(problems, problem("missing_profile_uuid", "UUID", "provisioning profile UUID is missing"))
	}
	if report.TeamID == "" {
		problems = append(problems, problem("missing_team_identifier", "TeamIdentifier", "profile team identifier is missing"))
	}
	if report.ApplicationIdentifier == "" {
		problems = append(problems, problem("missing_application_identifier", "application-identifier", "profile application identifier is missing"))
	}
	return problems
}

// InspectProfile verifies CMS integrity and, separately, optional explicit
// trust roots. It never promotes embedded certificates to roots.
func InspectProfile(ctx context.Context, profilePath string, trustedRootPaths []string, at time.Time, limits Limits) (Result[ProfileReport], error) {
	limits = limits.withDefaults()
	var out Result[ProfileReport]
	if at.IsZero() {
		at = time.Now()
	}
	if err := requireAbsoluteCleanPath("profilePath", profilePath); err != nil {
		return out, err
	}
	data, err := readRegularFile(ctx, profilePath, limits.MaxProfileBytes)
	if err != nil {
		return out, err
	}
	report, _, err := inspectProfileData(ctx, data, trustedRootPaths, at, limits)
	out.Value = report
	if err != nil {
		out.Problems = append(out.Problems, problem("profile_invalid", "provisioningProfile", err.Error()))
	}
	out.Problems = append(out.Problems, profileValidationProblems(report, at)...)
	return out, nil
}

// InspectIdentity loads and validates all referenced material. Returned facts
// omit every private path.
func InspectIdentity(ctx context.Context, c SigningConfig, at time.Time, limits Limits) (Result[IdentityReport], error) {
	limits = limits.withDefaults()
	var out Result[IdentityReport]
	if err := validateConfig(c); err != nil {
		return out, err
	}
	if at.IsZero() {
		at = time.Now()
	}
	for _, p := range append([]string{c.PrivateKeyPath, c.CertificatePath, c.ProvisioningProfilePath}, c.TrustedRootPaths...) {
		if err := rejectSymlinkPath(p, true); err != nil {
			return out, fmt.Errorf("identity path validation: %w", err)
		}
	}
	keyData, err := readRegularFile(ctx, c.PrivateKeyPath, limits.MaxProfileBytes)
	if err != nil {
		return out, fmt.Errorf("read private key: %w", err)
	}
	key, err := parsePEMPrivateKey(keyData)
	if err != nil {
		return out, err
	}
	certData, err := readRegularFile(ctx, c.CertificatePath, limits.MaxProfileBytes)
	if err != nil {
		return out, fmt.Errorf("read certificate: %w", err)
	}
	cert, err := parsePEMCertificate(certData)
	if err != nil {
		return out, err
	}
	cr := CertificateReport{
		SubjectCN: cert.Subject.CommonName, IssuerCN: cert.Issuer.CommonName,
		SerialNumber: cert.SerialNumber.String(), SHA256Fingerprint: certificateFingerprint(cert),
		NotBefore: cert.NotBefore, NotAfter: cert.NotAfter,
		CurrentlyValid:    !at.Before(cert.NotBefore) && !at.After(cert.NotAfter),
		MatchesPrivateKey: samePublicKey(key, cert.PublicKey),
	}
	out.Value.Certificate = cr
	if !cr.CurrentlyValid {
		out.Problems = append(out.Problems, problem("certificate_not_current", "certificate", "signing certificate is not valid at the requested time"))
	}
	if !cr.MatchesPrivateKey {
		out.Problems = append(out.Problems, problem("key_certificate_mismatch", "privateKey", "private key does not match the certificate public key"))
	}
	profileData, err := readRegularFile(ctx, c.ProvisioningProfilePath, limits.MaxProfileBytes)
	if err != nil {
		return out, fmt.Errorf("read provisioning profile: %w", err)
	}
	pr, raw, profileErr := inspectProfileData(ctx, profileData, c.TrustedRootPaths, at, limits)
	out.Value.Profile = pr
	if profileErr != nil {
		out.Problems = append(out.Problems, problem("profile_invalid", "provisioningProfile", profileErr.Error()))
	}
	out.Problems = append(out.Problems, profileValidationProblems(pr, at)...)
	entitlementTeam, _ := stringValue(pr.Entitlements["com.apple.developer.team-identifier"])
	if entitlementTeam == "" || entitlementTeam != pr.TeamID {
		out.Problems = append(out.Problems, problem("profile_team_mismatch", "com.apple.developer.team-identifier", "profile team entitlement does not match TeamIdentifier"))
	}
	if pr.ApplicationIdentifier == "" || !strings.HasPrefix(pr.ApplicationIdentifier, pr.TeamID+".") {
		out.Problems = append(out.Problems, problem("profile_application_identifier_mismatch", "application-identifier", "application identifier does not use the profile TeamIdentifier prefix"))
	}
	for _, team := range raw.TeamIdentifier {
		if team != pr.TeamID {
			out.Problems = append(out.Problems, problem("profile_team_mismatch", "TeamIdentifier", "profile contains inconsistent team identifiers"))
			break
		}
	}
	for _, prefix := range raw.ApplicationIdentifierPrefix {
		if prefix != pr.TeamID {
			out.Problems = append(out.Problems, problem("profile_team_mismatch", "ApplicationIdentifierPrefix", "application identifier prefix does not match TeamIdentifier"))
			break
		}
	}
	for _, der := range raw.DeveloperCertificates {
		if bytes.Equal(der, cert.Raw) {
			out.Value.CertificateInProfile = true
			break
		}
	}
	if !out.Value.CertificateInProfile {
		out.Problems = append(out.Problems, problem("certificate_not_in_profile", "certificate", "certificate DER is not an exact member of DeveloperCertificates"))
	}
	return out, nil
}

// InspectConfiguredIdentity loads a private config then inspects its identity.
func InspectConfiguredIdentity(ctx context.Context, configPath string, at time.Time, limits Limits) (Result[IdentityReport], error) {
	c, err := LoadSigningConfig(ctx, configPath, limits)
	if err != nil {
		return Result[IdentityReport]{}, err
	}
	return InspectIdentity(ctx, c, at, limits)
}

func stringValue(v any) (string, bool) {
	s, ok := v.(string)
	return s, ok
}

func boolValue(v any) (bool, bool) {
	b, ok := v.(bool)
	return b, ok
}

func cloneMap(in map[string]any) map[string]any {
	if in == nil {
		return map[string]any{}
	}
	out := make(map[string]any, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func equalPlistValue(a, b any) bool { return reflect.DeepEqual(a, b) }
