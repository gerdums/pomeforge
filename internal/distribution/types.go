// Package distribution implements bounded inspection and native Linux export
// of iOS application bundles. It has no dependency on Orchard's CLI or server.
package distribution

import "time"

// Severity classifies a machine-readable validation finding.
type Severity string

const (
	SeverityError   Severity = "error"
	SeverityWarning Severity = "warning"
)

// Problem is a stable, machine-readable validation finding.
type Problem struct {
	Code     string   `json:"code"`
	Severity Severity `json:"severity"`
	Field    string   `json:"field,omitempty"`
	Message  string   `json:"message"`
}

// Result keeps parsed facts separate from validation failures.
type Result[T any] struct {
	Value    T         `json:"value"`
	Problems []Problem `json:"problems"`
}

// Valid reports whether a result has no error-severity problems.
func (r Result[T]) Valid() bool {
	for _, p := range r.Problems {
		if p.Severity == SeverityError {
			return false
		}
	}
	return true
}

// Limits bounds all attacker-controlled files and archive expansion.
type Limits struct {
	MaxConfigBytes      int64  `json:"maxConfigBytes"`
	MaxProfileBytes     int64  `json:"maxProfileBytes"`
	MaxIPABytes         int64  `json:"maxIPABytes"`
	MaxBundleBytes      int64  `json:"maxBundleBytes"`
	MaxFileBytes        int64  `json:"maxFileBytes"`
	MaxArchiveEntries   int    `json:"maxArchiveEntries"`
	MaxCompressionRatio uint64 `json:"maxCompressionRatio"`
}

// DefaultLimits returns conservative limits suitable for an ordinary app.
func DefaultLimits() Limits {
	return Limits{
		MaxConfigBytes:      64 << 10,
		MaxProfileBytes:     8 << 20,
		MaxIPABytes:         2 << 30,
		MaxBundleBytes:      4 << 30,
		MaxFileBytes:        512 << 20,
		MaxArchiveEntries:   20000,
		MaxCompressionRatio: 200,
	}
}

func (l Limits) withDefaults() Limits {
	d := DefaultLimits()
	if l.MaxConfigBytes <= 0 {
		l.MaxConfigBytes = d.MaxConfigBytes
	}
	if l.MaxProfileBytes <= 0 {
		l.MaxProfileBytes = d.MaxProfileBytes
	}
	if l.MaxIPABytes <= 0 {
		l.MaxIPABytes = d.MaxIPABytes
	}
	if l.MaxBundleBytes <= 0 {
		l.MaxBundleBytes = d.MaxBundleBytes
	}
	if l.MaxFileBytes <= 0 {
		l.MaxFileBytes = d.MaxFileBytes
	}
	if l.MaxArchiveEntries <= 0 {
		l.MaxArchiveEntries = d.MaxArchiveEntries
	}
	if l.MaxCompressionRatio == 0 {
		l.MaxCompressionRatio = d.MaxCompressionRatio
	}
	return l
}

func problem(code, field, message string) Problem {
	return Problem{Code: code, Severity: SeverityError, Field: field, Message: message}
}

// TrustStatus intentionally does not imply that a caller-provided root is an
// Apple root. It describes only validation against the configured trust pool.
type TrustStatus string

const (
	TrustNoRoots   TrustStatus = "no_explicit_roots"
	TrustVerified  TrustStatus = "verified_against_explicit_roots"
	TrustUntrusted TrustStatus = "untrusted"
)

// CertificateReport contains public certificate facts and no source path.
type CertificateReport struct {
	SubjectCN         string    `json:"subjectCN,omitempty"`
	IssuerCN          string    `json:"issuerCN,omitempty"`
	SerialNumber      string    `json:"serialNumber"`
	SHA256Fingerprint string    `json:"sha256Fingerprint"`
	NotBefore         time.Time `json:"notBefore"`
	NotAfter          time.Time `json:"notAfter"`
	CurrentlyValid    bool      `json:"currentlyValid"`
	MatchesPrivateKey bool      `json:"matchesPrivateKey"`
}

// ProfileType is inferred from profile fields and is not an Apple server
// attestation.
type ProfileType string

const (
	ProfileAppStore    ProfileType = "app-store"
	ProfileDevelopment ProfileType = "development"
	ProfileAdHoc       ProfileType = "ad-hoc"
	ProfileEnterprise  ProfileType = "enterprise"
)

// ProfileReport contains public provisioning profile facts.
type ProfileReport struct {
	SHA256                      string         `json:"sha256"`
	UUID                        string         `json:"uuid"`
	Name                        string         `json:"name"`
	CreationDate                time.Time      `json:"creationDate"`
	ExpirationDate              time.Time      `json:"expirationDate"`
	TeamID                      string         `json:"teamID"`
	ApplicationIdentifierPrefix string         `json:"applicationIdentifierPrefix"`
	ApplicationIdentifier       string         `json:"applicationIdentifier"`
	BundleIdentifier            string         `json:"bundleIdentifier"`
	Type                        ProfileType    `json:"type"`
	SignatureValid              bool           `json:"signatureValid"`
	Trust                       TrustStatus    `json:"trust"`
	TrustError                  string         `json:"trustError,omitempty"`
	HasProvisionedDevices       bool           `json:"hasProvisionedDevices"`
	DeviceCount                 int            `json:"deviceCount"`
	ProvisionsAllDevices        bool           `json:"provisionsAllDevices"`
	GetTaskAllow                bool           `json:"getTaskAllow"`
	CertificateSHA256           []string       `json:"certificateSHA256"`
	Entitlements                map[string]any `json:"entitlements"`
}

// IdentityReport combines public identity and profile inspection facts.
type IdentityReport struct {
	Certificate          CertificateReport `json:"certificate"`
	Profile              ProfileReport     `json:"profile"`
	CertificateInProfile bool              `json:"certificateInProfile"`
}
