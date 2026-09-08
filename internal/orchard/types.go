package orchard

import "time"

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
	ID         string `json:"id"`
	Name       string `json:"name"`
	Status     string `json:"status"`
	Version    string `json:"version,omitempty"`
	Path       string `json:"path,omitempty"`
	Detail     string `json:"detail"`
	InstallURL string `json:"installUrl"`
}

type Capability struct {
	ID     string `json:"id"`
	Title  string `json:"title"`
	Status string `json:"status"`
	Detail string `json:"detail"`
}

type ActionInfo struct {
	ID                   string `json:"id"`
	Title                string `json:"title"`
	Description          string `json:"description"`
	RequiresConfirmation bool   `json:"requiresConfirmation"`
	Effect               string `json:"effect"`
}

type PlanInput struct {
	Action  string `json:"action"`
	Project string `json:"project"`
	IPA     string `json:"ipa,omitempty"`
	Device  string `json:"device,omitempty"`
}

type Step struct {
	Tool        string   `json:"tool"`
	Executable  string   `json:"executable"`
	Args        []string `json:"args"`
	Directory   string   `json:"directory"`
	Description string   `json:"description"`
}

type Plan struct {
	ID                   string   `json:"id"`
	Action               string   `json:"action"`
	Title                string   `json:"title"`
	Steps                []Step   `json:"steps"`
	Blockers             []string `json:"blockers"`
	Warnings             []string `json:"warnings"`
	RequiresConfirmation bool     `json:"requiresConfirmation"`
	Executable           bool     `json:"executable"`
	Effect               string   `json:"effect"`
	Fingerprint          string   `json:"-"`
}

type OperationResult struct {
	ID         string    `json:"id"`
	Action     string    `json:"action"`
	Status     string    `json:"status"`
	ExitCode   int       `json:"exitCode"`
	Output     string    `json:"output"`
	StartedAt  time.Time `json:"startedAt"`
	FinishedAt time.Time `json:"finishedAt"`
}

type State struct {
	Version      string            `json:"version"`
	Workspace    string            `json:"workspace"`
	Projects     []ProjectSummary  `json:"projects"`
	Tools        []ToolStatus      `json:"tools"`
	Capabilities []Capability      `json:"capabilities"`
	Actions      []ActionInfo      `json:"actions"`
	History      []OperationResult `json:"history"`
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
