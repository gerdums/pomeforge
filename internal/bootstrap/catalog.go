// Package bootstrap validates Orchard's pinned tool catalog and installs its
// Linux tools into private, per-user directories without invoking a shell.
package bootstrap

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"regexp"
	"strings"
	"time"
)

const (
	SupportedSchemaVersion = 1
	maxAssetSize           = int64(1 << 30) // 1 GiB hard download/catalog bound.
)

var (
	componentPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._+-]*$`)
	hexPattern       = regexp.MustCompile(`^[0-9a-fA-F]{64}$`)
	platformPattern  = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]*$`)

	ErrUnsupportedTool     = errors.New("unsupported tool")
	ErrUnsupportedPlatform = errors.New("unsupported operating system or architecture")
	ErrInstallConflict     = errors.New("another install of this tool is in progress")
	ErrActivationCollision = errors.New("activation path is not managed by Orchard")
)

// Catalog is the complete, pinned Orchard tool catalog.
type Catalog struct {
	SchemaVersion int    `json:"schemaVersion"`
	VerifiedAt    string `json:"verifiedAt"`
	Tools         []Tool `json:"tools"`
}

// Tool describes one pinned upstream tool release.
type Tool struct {
	ID      string  `json:"id"`
	Version string  `json:"version"`
	Source  string  `json:"source"`
	Release string  `json:"release"`
	Assets  []Asset `json:"assets"`
}

// Asset identifies one checksum-pinned platform download.
type Asset struct {
	OS     string `json:"os"`
	Arch   string `json:"arch"`
	URL    string `json:"url"`
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
	Format string `json:"format"`
}

// Load decodes and fully validates a catalog. Unknown JSON fields and trailing
// JSON values are rejected so catalog format changes fail closed.
func Load(r io.Reader) (Catalog, error) {
	var catalog Catalog
	decoder := json.NewDecoder(r)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&catalog); err != nil {
		return Catalog{}, fmt.Errorf("decode tool catalog: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return Catalog{}, errors.New("decode tool catalog: trailing JSON value")
		}
		return Catalog{}, fmt.Errorf("decode tool catalog trailing data: %w", err)
	}
	if err := validateCatalog(catalog); err != nil {
		return Catalog{}, err
	}
	return catalog, nil
}

func validateCatalog(catalog Catalog) error {
	if catalog.SchemaVersion != SupportedSchemaVersion {
		return fmt.Errorf("unsupported catalog schema version %d", catalog.SchemaVersion)
	}
	if _, err := time.Parse("2006-01-02", catalog.VerifiedAt); err != nil {
		return fmt.Errorf("invalid verifiedAt date %q: %w", catalog.VerifiedAt, err)
	}
	if len(catalog.Tools) == 0 {
		return errors.New("tool catalog contains no tools")
	}
	seenTools := make(map[string]struct{}, len(catalog.Tools))
	for i, tool := range catalog.Tools {
		if err := validateComponent("tool id", tool.ID); err != nil {
			return fmt.Errorf("tools[%d]: %w", i, err)
		}
		if err := validateComponent("tool version", tool.Version); err != nil {
			return fmt.Errorf("tool %q: %w", tool.ID, err)
		}
		if _, exists := seenTools[tool.ID]; exists {
			return fmt.Errorf("duplicate tool id %q", tool.ID)
		}
		seenTools[tool.ID] = struct{}{}
		if err := validateHTTPSURL("source", tool.Source); err != nil {
			return fmt.Errorf("tool %q: %w", tool.ID, err)
		}
		if err := validateHTTPSURL("release", tool.Release); err != nil {
			return fmt.Errorf("tool %q: %w", tool.ID, err)
		}
		if len(tool.Assets) == 0 {
			return fmt.Errorf("tool %q contains no assets", tool.ID)
		}
		seenPlatforms := make(map[string]struct{}, len(tool.Assets))
		for j, asset := range tool.Assets {
			if err := validateAsset(asset); err != nil {
				return fmt.Errorf("tool %q assets[%d]: %w", tool.ID, j, err)
			}
			platform := asset.OS + "/" + asset.Arch
			if _, exists := seenPlatforms[platform]; exists {
				return fmt.Errorf("tool %q has duplicate platform asset %q", tool.ID, platform)
			}
			seenPlatforms[platform] = struct{}{}
		}
	}
	return nil
}

func validateAsset(asset Asset) error {
	if !platformPattern.MatchString(asset.OS) {
		return fmt.Errorf("invalid operating system %q", asset.OS)
	}
	if !platformPattern.MatchString(asset.Arch) {
		return fmt.Errorf("invalid architecture %q", asset.Arch)
	}
	switch asset.Format {
	case "binary", "tar.gz", "appimage":
	default:
		return fmt.Errorf("unknown asset format %q", asset.Format)
	}
	if asset.Size <= 0 || asset.Size > maxAssetSize {
		return fmt.Errorf("asset size %d is outside the allowed range 1..%d", asset.Size, maxAssetSize)
	}
	if !hexPattern.MatchString(asset.SHA256) {
		return errors.New("asset sha256 must contain exactly 64 hexadecimal characters")
	}
	if err := validateHTTPSURL("asset", asset.URL); err != nil {
		return err
	}
	return nil
}

func validateComponent(label, value string) error {
	if !componentPattern.MatchString(value) || value == "." || value == ".." || strings.Contains(value, "..") {
		return fmt.Errorf("unsafe %s %q", label, value)
	}
	return nil
}

func validateHTTPSURL(label, raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("invalid %s URL: %w", label, err)
	}
	if u.Scheme != "https" || u.Host == "" || u.User != nil || u.Fragment != "" || u.Opaque != "" || (u.Path != "" && !strings.HasPrefix(u.Path, "/")) {
		return fmt.Errorf("invalid %s URL %q: require an HTTPS hierarchical URL without credentials or fragment", label, raw)
	}
	return nil
}
