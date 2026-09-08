package bootstrap

import (
	"errors"
	"fmt"
	"path/filepath"
)

// Plan is an immutable, validated installation plan. Its fields are private so
// callers cannot turn an approved catalog selection into an arbitrary download
// or filesystem write. Accessors expose values needed for presentation.
type Plan struct {
	tool  Tool
	asset Asset
	paths Paths
}

// PlanInstall selects a tool and exact OS/architecture asset without writing
// to disk.
func PlanInstall(catalog Catalog, id, goos, goarch string, paths Paths) (Plan, error) {
	if err := validateCatalog(catalog); err != nil {
		return Plan{}, fmt.Errorf("invalid catalog: %w", err)
	}
	if err := validatePaths(paths); err != nil {
		return Plan{}, err
	}
	for _, tool := range catalog.Tools {
		if tool.ID != id {
			continue
		}
		for _, asset := range tool.Assets {
			if asset.OS == goos && asset.Arch == goarch {
				if _, err := expectedExecutable(tool.ID, asset.Format); err != nil {
					return Plan{}, err
				}
				return Plan{tool: cloneTool(tool), asset: asset, paths: paths}, nil
			}
		}
		return Plan{}, fmt.Errorf("%w: tool %q has no asset for %s/%s", ErrUnsupportedPlatform, id, goos, goarch)
	}
	return Plan{}, fmt.Errorf("%w: %q", ErrUnsupportedTool, id)
}

func (p Plan) Tool() Tool       { return cloneTool(p.tool) }
func (p Plan) Asset() Asset     { return p.asset }
func (p Plan) Paths() Paths     { return p.paths }
func (p Plan) Platform() string { return p.asset.OS + "/" + p.asset.Arch }

// ExecutablePath is the active path clients should invoke after installation.
func (p Plan) ExecutablePath() string { return filepath.Join(p.paths.BinDir, p.tool.ID) }

func (p Plan) versionRoot() string {
	return filepath.Join(p.paths.ToolsDir, p.tool.ID, p.tool.Version, p.asset.OS+"-"+p.asset.Arch)
}

func expectedExecutable(id, format string) (string, error) {
	switch {
	case id == "asc" && format == "binary":
		return "asc", nil
	case id == "zsign" && format == "tar.gz":
		return "zsign", nil
	case id == "xtool" && format == "appimage":
		return "xtool-launcher", nil
	default:
		return "", fmt.Errorf("unsupported installer mapping for tool %q with format %q", id, format)
	}
}

func cloneTool(tool Tool) Tool {
	tool.Assets = append([]Asset(nil), tool.Assets...)
	return tool
}

func validatePlan(plan Plan) error {
	if plan.tool.ID == "" || plan.asset.URL == "" {
		return errors.New("install plan was not created by PlanInstall")
	}
	if err := validatePaths(plan.paths); err != nil {
		return err
	}
	catalog := Catalog{SchemaVersion: SupportedSchemaVersion, VerifiedAt: "2000-01-01", Tools: []Tool{plan.tool}}
	if err := validateCatalog(catalog); err != nil {
		return fmt.Errorf("invalid install plan: %w", err)
	}
	found := false
	for _, asset := range plan.tool.Assets {
		if asset == plan.asset {
			found = true
			break
		}
	}
	if !found {
		return errors.New("invalid install plan: selected asset is not part of the tool")
	}
	_, err := expectedExecutable(plan.tool.ID, plan.asset.Format)
	return err
}
