package pomeforge

import (
	"time"

	"pomeforge.local/pomeforge/internal/distribution"
)

const (
	ManifestSchemaVersion = 1
	Version               = "0.1.0"
)

type AppStoreIDs struct {
	AppID     string `json:"appId,omitempty"`
	VersionID string `json:"versionId,omitempty"`
	BuildID   string `json:"buildId,omitempty"`
}

type Manifest struct {
	SchemaVersion     int         `json:"schemaVersion"`
	Name              string      `json:"name"`
	BundleIdentifier  string      `json:"bundleIdentifier"`
	DeviceFamilies    []string    `json:"deviceFamilies"`
	MinimumIOSVersion string      `json:"minimumIOSVersion"`
	MarketingVersion  string      `json:"marketingVersion"`
	BuildNumber       string      `json:"buildNumber"`
	AppStore          AppStoreIDs `json:"appStore,omitempty"`
}

type ProjectSummary struct {
	Path     string `json:"path"`
	Name     string `json:"name"`
	BundleID string `json:"bundleId"`
}

type ToolStatus struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Status  string `json:"status"`
	Version string `json:"version,omitempty"`
	// Path preserves the selected invocation basename for multicall tools.
	Path string `json:"path,omitempty"`
	// CanonicalPath identifies the regular file whose bytes are fingerprinted.
	CanonicalPath string `json:"-"`
	Detail        string `json:"detail"`
	InstallURL    string `json:"installUrl"`
}

type Capability struct {
	ID     string `json:"id"`
	Title  string `json:"title"`
	Status string `json:"status"`
	Detail string `json:"detail"`
}

type ActionInfo struct {
	ID                   string            `json:"id"`
	Title                string            `json:"title"`
	Description          string            `json:"description"`
	RequiresConfirmation bool              `json:"requiresConfirmation"`
	Effect               string            `json:"effect"`
	Scope                string            `json:"scope"`
	Parameters           []ActionParameter `json:"parameters,omitempty"`
}

type ActionParameter struct {
	Name        string   `json:"name"`
	Type        string   `json:"type"`
	Required    bool     `json:"required"`
	Description string   `json:"description"`
	Values      []string `json:"values,omitempty"`
}

type PlanInput struct {
	Action           string   `json:"action"`
	Project          string   `json:"project,omitempty"`
	IPA              string   `json:"ipa,omitempty"`
	Device           string   `json:"device,omitempty"`
	Tool             string   `json:"tool,omitempty"`
	Helper           string   `json:"helper,omitempty"`
	ExecutablePath   string   `json:"executablePath,omitempty"`
	SourceRevision   string   `json:"sourceRevision,omitempty"`
	AssetKitRevision string   `json:"assetKitRevision,omitempty"`
	InputPath        string   `json:"inputPath,omitempty"`
	Arch             string   `json:"arch,omitempty"`
	Identity         string   `json:"identity,omitempty"`
	IdentityLabel    string   `json:"identityLabel,omitempty"`
	PrivateKeyPath   string   `json:"privateKeyPath,omitempty"`
	CertificatePath  string   `json:"certificatePath,omitempty"`
	ProfilePath      string   `json:"profilePath,omitempty"`
	TrustedRootPaths []string `json:"trustedRootPaths,omitempty"`
	SourceBundle     string   `json:"sourceBundle,omitempty"`
	OutputIPA        string   `json:"outputIPA,omitempty"`
	AssetCatalog     string   `json:"assetCatalog,omitempty"`
	IconSource       string   `json:"iconSource,omitempty"`
	AppID            string   `json:"appId,omitempty"`
	VersionID        string   `json:"versionId,omitempty"`
	BuildID          string   `json:"buildId,omitempty"`
	Version          string   `json:"version,omitempty"`
	reservation      string
}

type Step struct {
	Kind        string            `json:"kind"`
	Tool        string            `json:"tool"`
	Executable  string            `json:"executable,omitempty"`
	Args        []string          `json:"args,omitempty"`
	Directory   string            `json:"directory,omitempty"`
	Operation   string            `json:"operation,omitempty"`
	Parameters  map[string]string `json:"parameters,omitempty"`
	Description string            `json:"description"`
}

type ToolInstallPlan struct {
	Tool         string `json:"tool"`
	Version      string `json:"version"`
	Destination  string `json:"destination"`
	SourceURL    string `json:"sourceUrl"`
	SHA256       string `json:"sha256"`
	DownloadSize int64  `json:"downloadSize"`
}

type SDKImportPlan struct {
	InputPath          string            `json:"inputPath"`
	InputKind          string            `json:"inputKind"`
	InputSHA256        string            `json:"inputSha256"`
	Trust              string            `json:"trust"`
	Arch               string            `json:"arch"`
	StageRoot          string            `json:"stageRoot"`
	Metadata           map[string]string `json:"metadata,omitempty"`
	Sources            map[string]string `json:"metadataSources,omitempty"`
	Missing            []string          `json:"missingMetadata,omitempty"`
	MetadataHashes     map[string]string `json:"metadataHashes,omitempty"`
	XDGConfigHome      string            `json:"xdgConfigHome"`
	SwiftExecutable    string            `json:"swiftExecutable"`
	SwiftSHA256        string            `json:"swiftSha256"`
	ClangExecutable    string            `json:"clangExecutable"`
	ClangSHA256        string            `json:"clangSha256"`
	ClangResourceDir   string            `json:"clangResourceDir"`
	ClangHeadersSHA256 string            `json:"clangHeadersSha256"`
}

// SDKEnvironmentReceipt is the read-only handoff for the installed SDK's
// source provenance and the exact Linux Swift environment that owns it.
type SDKEnvironmentReceipt struct {
	SchemaVersion           int               `json:"schemaVersion"`
	InputPath               string            `json:"inputPath"`
	InputKind               string            `json:"inputKind"`
	InputSHA256             string            `json:"inputSha256"`
	Trust                   string            `json:"trust"`
	Arch                    string            `json:"arch"`
	StageRoot               string            `json:"stageRoot"`
	XDGConfigHome           string            `json:"xdgConfigHome"`
	SwiftExecutable         string            `json:"swiftExecutable"`
	SwiftSHA256             string            `json:"swiftSha256"`
	ClangExecutable         string            `json:"clangExecutable"`
	ClangSHA256             string            `json:"clangSha256"`
	ClangResourceDir        string            `json:"clangResourceDir"`
	ClangHeadersSHA256      string            `json:"clangHeadersSha256"`
	StagedArtifactBundle    string            `json:"stagedArtifactBundle"`
	InstalledArtifactBundle string            `json:"installedArtifactBundle"`
	ActiveSDKRoot           string            `json:"activeSdkRoot"`
	TargetTriple            string            `json:"targetTriple"`
	SwiftSDKMetadataPath    string            `json:"swiftSdkMetadataPath"`
	SwiftSDKMetadataSHA256  string            `json:"swiftSdkMetadataSha256"`
	SDKStatus               string            `json:"sdkStatus"`
	Metadata                map[string]string `json:"metadata,omitempty"`
	MetadataSources         map[string]string `json:"metadataSources,omitempty"`
	MetadataHashes          map[string]string `json:"metadataHashes,omitempty"`
	MetadataSnapshotRoot    string            `json:"metadataSnapshotRoot"`
	MissingMetadata         []string          `json:"missingMetadata,omitempty"`
	CreatedAt               time.Time         `json:"createdAt"`
}

type Plan struct {
	ID                   string                                            `json:"id"`
	Action               string                                            `json:"action"`
	Title                string                                            `json:"title"`
	Steps                []Step                                            `json:"steps"`
	Blockers             []string                                          `json:"blockers"`
	Warnings             []string                                          `json:"warnings"`
	RequiresConfirmation bool                                              `json:"requiresConfirmation"`
	Executable           bool                                              `json:"executable"`
	Effect               string                                            `json:"effect"`
	Scope                string                                            `json:"scope"`
	Project              string                                            `json:"project,omitempty"`
	ProjectLabel         string                                            `json:"projectLabel,omitempty"`
	ToolInstall          *ToolInstallPlan                                  `json:"toolInstall,omitempty"`
	SDKImport            *SDKImportPlan                                    `json:"sdkImport,omitempty"`
	SDKBinding           *SDKBindingReport                                 `json:"sdkBinding,omitempty"`
	DistributionExport   *distribution.ExportPlan                          `json:"distributionExport,omitempty"`
	IPAInspection        *distribution.IPAReport                           `json:"ipaInspection,omitempty"`
	IdentityInspection   *distribution.Result[distribution.IdentityReport] `json:"identityInspection,omitempty"`
	Fingerprint          string                                            `json:"-"`
}

type SDKBindingReport struct {
	TargetTriple         string `json:"targetTriple"`
	SDKVersion           string `json:"sdkVersion"`
	SDKProductVersion    string `json:"sdkProductVersion"`
	SDKCanonicalName     string `json:"sdkCanonicalName"`
	SDKBuildVersion      string `json:"sdkBuildVersion"`
	PlatformVersion      string `json:"platformVersion"`
	PlatformBuildVersion string `json:"platformBuildVersion"`
	XcodeVersion         string `json:"xcodeVersion"`
	XcodeBuildVersion    string `json:"xcodeBuildVersion"`
	XcodeBundleVersion   string `json:"xcodeBundleVersion"`
	Provenance           string `json:"provenance"`
	SDKRoot              string `json:"-"`
	ReceiptPath          string `json:"-"`
	XDGConfigHome        string `json:"-"`
	SwiftExecutable      string `json:"-"`
	SwiftSHA256          string `json:"-"`
	ClangExecutable      string `json:"-"`
	ClangSHA256          string `json:"-"`
}

type SigningIdentity struct {
	ID       string                       `json:"id"`
	Label    string                       `json:"label"`
	Status   string                       `json:"status"`
	Report   *distribution.IdentityReport `json:"report,omitempty"`
	Problems []distribution.Problem       `json:"problems,omitempty"`
	Error    string                       `json:"error,omitempty"`
}

type OperationResult struct {
	ID           string            `json:"id"`
	Action       string            `json:"action"`
	Status       string            `json:"status"`
	ExitCode     int               `json:"exitCode"`
	Output       string            `json:"output"`
	StartedAt    time.Time         `json:"startedAt"`
	FinishedAt   time.Time         `json:"finishedAt"`
	Scope        string            `json:"scope,omitempty"`
	Project      string            `json:"project,omitempty"`
	ProjectLabel string            `json:"projectLabel,omitempty"`
	Metadata     map[string]string `json:"metadata,omitempty"`
}

type State struct {
	Version      string            `json:"version"`
	Workspace    string            `json:"workspace"`
	Projects     []ProjectSummary  `json:"projects"`
	Tools        []ToolStatus      `json:"tools"`
	Capabilities []Capability      `json:"capabilities"`
	Actions      []ActionInfo      `json:"actions"`
	History      []OperationResult `json:"history"`
	Identities   []SigningIdentity `json:"identities"`
}

type CodedError struct {
	Code    string
	Message string
	Result  *OperationResult
}

func (e *CodedError) Error() string { return e.Message }

func Errorf(code, message string) error { return &CodedError{Code: code, Message: message} }

func ErrorWithResult(code, message string, result OperationResult) error {
	return &CodedError{Code: code, Message: message, Result: &result}
}
