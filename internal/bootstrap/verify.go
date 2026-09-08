package bootstrap

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// VerifyInstalled performs a read-only validation of the pinned installation
// and its active link. It never creates directories, downloads, activates, or
// repairs files.
func VerifyInstalled(ctx context.Context, plan Plan) (Receipt, error) {
	if err := validatePlan(plan); err != nil {
		return Receipt{}, err
	}
	if err := ctx.Err(); err != nil {
		return Receipt{}, err
	}
	if err := inspectDirectoryPath(plan.paths.ToolsDir, false); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Receipt{}, fmt.Errorf("%w: %s", ErrInstallAbsent, plan.versionRoot())
		}
		return Receipt{}, fmt.Errorf("%w: %v", ErrInstallInvalid, err)
	}
	if err := verifyPrivateTree(plan.paths.ToolsDir, plan.tool.ID, plan.tool.Version); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Receipt{}, fmt.Errorf("%w: %s", ErrInstallAbsent, plan.versionRoot())
		}
		return Receipt{}, fmt.Errorf("%w: %v", ErrInstallInvalid, err)
	}
	executable, err := validateExistingInstall(plan)
	if err != nil {
		return Receipt{}, fmt.Errorf("%w: %v", ErrInstallInvalid, err)
	}
	if executable == "" {
		return Receipt{}, fmt.Errorf("%w: %s", ErrInstallAbsent, plan.versionRoot())
	}
	if err := ctx.Err(); err != nil {
		return Receipt{}, err
	}
	if err := inspectDirectoryPath(plan.paths.BinDir, false); err != nil {
		return Receipt{}, fmt.Errorf("%w: active directory: %v", ErrInstallInvalid, err)
	}
	active := plan.ExecutablePath()
	info, err := os.Lstat(active)
	if err != nil {
		return Receipt{}, fmt.Errorf("%w: inspect active executable: %v", ErrInstallInvalid, err)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		return Receipt{}, fmt.Errorf("%w: active executable is not a symbolic link", ErrInstallInvalid)
	}
	target, err := os.Readlink(active)
	if err != nil || !isManagedActivationTarget(plan, absoluteLinkTarget(active, target)) {
		return Receipt{}, fmt.Errorf("%w: active link is not managed by Orchard", ErrInstallInvalid)
	}
	resolved, err := filepath.EvalSymlinks(active)
	if err != nil || filepath.Clean(resolved) != filepath.Clean(executable) {
		return Receipt{}, fmt.Errorf("%w: active link does not resolve to the verified installation", ErrInstallInvalid)
	}
	artifactInfo, err := os.Lstat(filepath.Join(plan.versionRoot(), ".artifact"))
	if err != nil {
		return Receipt{}, fmt.Errorf("%w: inspect installed artifact: %v", ErrInstallInvalid, err)
	}
	return Receipt{
		Tool:           plan.tool.ID,
		Version:        plan.tool.Version,
		OS:             plan.asset.OS,
		Arch:           plan.asset.Arch,
		SourceURL:      plan.asset.URL,
		SHA256:         strings.ToLower(plan.asset.SHA256),
		ExecutablePath: active,
		InstalledAt:    artifactInfo.ModTime().UTC(),
	}, nil
}
