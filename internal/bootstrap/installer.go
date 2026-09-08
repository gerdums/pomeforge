package bootstrap

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"
)

const (
	maxArchiveEntries  = 10_000
	maxExpandedBytes   = int64(512 << 20)
	maxDiagnosticBytes = 64 << 10
)

// ProcessRunner runs the one trusted AppImage extraction operation. Arguments
// are passed directly, never through a shell. Implementations are primarily
// useful for deterministic tests; nil selects the real os/exec runner.
type ProcessRunner interface {
	Run(ctx context.Context, executable string, args []string, directory string, environment []string) (diagnostic string, err error)
}

// Installer performs a validated Plan. A nil Client uses a bounded HTTPS-only
// client, and a nil Runner uses exec.CommandContext.
type Installer struct {
	Client *http.Client
	Runner ProcessRunner
}

// Receipt records the exact activated tool artifact.
type Receipt struct {
	Tool           string    `json:"tool"`
	Version        string    `json:"version"`
	OS             string    `json:"os"`
	Arch           string    `json:"arch"`
	SourceURL      string    `json:"sourceUrl"`
	SHA256         string    `json:"sha256"`
	ExecutablePath string    `json:"executablePath"`
	InstalledAt    time.Time `json:"installedAt"`
}

// Install downloads, verifies, stages, and atomically activates a tool. It is
// safe to cancel: activation happens only after a complete version is ready.
func (i Installer) Install(ctx context.Context, plan Plan) (Receipt, error) {
	if err := validatePlan(plan); err != nil {
		return Receipt{}, err
	}
	if err := ctx.Err(); err != nil {
		return Receipt{}, err
	}
	for _, directory := range []string{plan.paths.ToolsDir, plan.paths.DownloadsDir, plan.paths.BinDir} {
		if err := ensurePrivateDir(directory); err != nil {
			return Receipt{}, err
		}
	}
	unlock, err := acquireLock(plan)
	if err != nil {
		return Receipt{}, err
	}
	defer unlock()
	if err := ensurePrivateTree(plan.paths.ToolsDir, plan.tool.ID, plan.tool.Version); err != nil {
		return Receipt{}, err
	}

	if existing, err := validateExistingInstall(plan); err != nil {
		return Receipt{}, err
	} else if existing != "" {
		if err := activate(plan, existing); err != nil {
			return Receipt{}, err
		}
		return makeReceipt(plan), nil
	}

	stagingParent := filepath.Join(plan.paths.ToolsDir, ".staging")
	if err := ensurePrivateDir(stagingParent); err != nil {
		return Receipt{}, err
	}
	stage, err := os.MkdirTemp(stagingParent, plan.tool.ID+"-")
	if err != nil {
		return Receipt{}, fmt.Errorf("create install stage: %w", err)
	}
	if err := os.Chmod(stage, 0o700); err != nil {
		_ = os.RemoveAll(stage)
		return Receipt{}, fmt.Errorf("protect install stage: %w", err)
	}
	defer os.RemoveAll(stage)

	artifact := filepath.Join(stage, "download")
	if err := i.obtainArtifact(ctx, plan, artifact); err != nil {
		return Receipt{}, err
	}
	complete := filepath.Join(stage, "complete")
	if err := os.Mkdir(complete, 0o700); err != nil {
		return Receipt{}, fmt.Errorf("create complete stage: %w", err)
	}
	storedArtifact := filepath.Join(complete, ".artifact")
	if err := copyFile(artifact, storedArtifact, 0o600); err != nil {
		return Receipt{}, fmt.Errorf("preserve verified artifact: %w", err)
	}
	executable, err := i.prepare(ctx, plan, complete, storedArtifact)
	if err != nil {
		return Receipt{}, err
	}
	if err := verifyExecutableWithin(complete, executable); err != nil {
		return Receipt{}, err
	}
	if plan.asset.Format == "binary" {
		if ok, err := verifyFile(executable, plan.asset.Size, plan.asset.SHA256); err != nil || !ok {
			if err == nil {
				err = errors.New("binary executable differs from the pinned artifact")
			}
			return Receipt{}, fmt.Errorf("verify staged binary: %w", err)
		}
	}
	if err := writeInstallManifest(complete); err != nil {
		return Receipt{}, fmt.Errorf("record installed tree: %w", err)
	}

	versionRoot := plan.versionRoot()
	if err := verifyPrivateTree(plan.paths.ToolsDir, plan.tool.ID, plan.tool.Version); err != nil {
		return Receipt{}, err
	}
	if _, err := os.Lstat(versionRoot); !errors.Is(err, os.ErrNotExist) {
		if err == nil {
			return Receipt{}, fmt.Errorf("managed version path appeared before commit: %s", versionRoot)
		}
		return Receipt{}, fmt.Errorf("recheck managed version path: %w", err)
	}
	if err := os.Rename(complete, versionRoot); err != nil {
		return Receipt{}, fmt.Errorf("commit installed version: %w", err)
	}
	installedExecutable := filepath.Join(versionRoot, strings.TrimPrefix(executable, complete+string(filepath.Separator)))
	if err := activate(plan, installedExecutable); err != nil {
		return Receipt{}, err
	}
	return makeReceipt(plan), nil
}

func (i Installer) obtainArtifact(ctx context.Context, plan Plan, destination string) error {
	cache := filepath.Join(plan.paths.DownloadsDir, cacheName(plan))
	if ok, _ := verifyFile(cache, plan.asset.Size, plan.asset.SHA256); ok {
		return copyFile(cache, destination, 0o600)
	}
	if info, err := os.Lstat(cache); err == nil {
		if !info.Mode().IsRegular() {
			return fmt.Errorf("download cache path is not a regular file: %s", cache)
		}
		if err := os.Remove(cache); err != nil {
			return fmt.Errorf("remove corrupt cached artifact: %w", err)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("inspect cached artifact: %w", err)
	}
	if err := i.download(ctx, plan.asset, destination); err != nil {
		return err
	}
	temporaryCache := cache + ".tmp-" + randomToken()
	if err := copyFile(destination, temporaryCache, 0o600); err == nil {
		if renameErr := os.Rename(temporaryCache, cache); renameErr != nil {
			_ = os.Remove(temporaryCache)
		}
	} else {
		_ = os.Remove(temporaryCache)
	}
	return nil
}

func (i Installer) download(ctx context.Context, asset Asset, destination string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, asset.URL, nil)
	if err != nil {
		return fmt.Errorf("create download request: %w", err)
	}
	client := secureClient(i.Client)
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("download artifact: %w", err)
	}
	defer resp.Body.Close()
	if resp.Request == nil || resp.Request.URL == nil || resp.Request.URL.Scheme != "https" {
		return errors.New("download redirected to a non-HTTPS URL")
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		_, _ = io.CopyN(io.Discard, resp.Body, 4096)
		return fmt.Errorf("download artifact: HTTP status %s", resp.Status)
	}
	if resp.ContentLength >= 0 && resp.ContentLength != asset.Size {
		return fmt.Errorf("download size mismatch: expected %d bytes, server declared %d", asset.Size, resp.ContentLength)
	}
	file, err := os.OpenFile(destination, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("create staged download: %w", err)
	}
	hash := sha256.New()
	written, copyErr := io.Copy(io.MultiWriter(file, hash), io.LimitReader(resp.Body, asset.Size+1))
	closeErr := file.Close()
	if copyErr != nil {
		return fmt.Errorf("download artifact: %w", copyErr)
	}
	if closeErr != nil {
		return fmt.Errorf("flush downloaded artifact: %w", closeErr)
	}
	if written != asset.Size {
		return fmt.Errorf("download size mismatch: expected %d bytes, received %d", asset.Size, written)
	}
	actual := hex.EncodeToString(hash.Sum(nil))
	if !strings.EqualFold(actual, asset.SHA256) {
		return fmt.Errorf("download checksum mismatch: expected %s, received %s", strings.ToLower(asset.SHA256), actual)
	}
	return nil
}

func secureClient(provided *http.Client) *http.Client {
	var client http.Client
	if provided == nil {
		client.Timeout = 10 * time.Minute
	} else {
		client = *provided
	}
	previous := client.CheckRedirect
	client.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if req.URL.Scheme != "https" || req.URL.User != nil || req.URL.Fragment != "" {
			return errors.New("refuse non-HTTPS or credential-bearing redirect")
		}
		if previous != nil {
			return previous(req, via)
		}
		if len(via) >= 10 {
			return errors.New("too many redirects")
		}
		return nil
	}
	return &client
}

func (i Installer) prepare(ctx context.Context, plan Plan, root, artifact string) (string, error) {
	switch plan.asset.Format {
	case "binary":
		destination := filepath.Join(root, "asc")
		if err := copyFile(artifact, destination, 0o700); err != nil {
			return "", fmt.Errorf("stage asc executable: %w", err)
		}
		return destination, nil
	case "tar.gz":
		extractRoot := filepath.Join(root, "package")
		if err := os.Mkdir(extractRoot, 0o700); err != nil {
			return "", err
		}
		found, err := extractTarGzip(ctx, artifact, extractRoot, "zsign")
		if err != nil {
			return "", err
		}
		return found, nil
	case "appimage":
		if err := os.Chmod(artifact, 0o700); err != nil {
			return "", fmt.Errorf("make AppImage executable: %w", err)
		}
		runner := i.Runner
		if runner == nil {
			runner = execRunner{}
		}
		environment := []string{
			"PATH=/usr/bin:/bin",
			"HOME=" + root,
			"XDG_CACHE_HOME=" + filepath.Join(root, ".cache"),
			"LANG=C.UTF-8",
			"LC_ALL=C.UTF-8",
		}
		diagnostic, err := runner.Run(ctx, artifact, []string{"--appimage-extract"}, root, environment)
		if err != nil {
			return "", fmt.Errorf("extract xtool AppImage: %w: %s", err, truncateDiagnostic(diagnostic))
		}
		appRun := filepath.Join(root, "squashfs-root", "AppRun")
		if err := verifyAppRunWithin(root, appRun); err != nil {
			return "", fmt.Errorf("validate extracted AppRun: %w", err)
		}
		launcher := filepath.Join(root, "xtool-launcher")
		if err := os.WriteFile(launcher, []byte(appImageLauncher), 0o700); err != nil {
			return "", fmt.Errorf("write AppImage launcher: %w", err)
		}
		if err := os.Chmod(launcher, 0o700); err != nil {
			return "", err
		}
		return launcher, nil
	default:
		return "", fmt.Errorf("unsupported asset format %q", plan.asset.Format)
	}
}

// The launcher resolves its own active symlink before invoking AppRun. This is
// static text (no path interpolation) and preserves AppRun's expected location.
const appImageLauncher = `#!/bin/sh
set -eu
self=$(readlink -f -- "$0")
root=${self%/*}
exec "$root/squashfs-root/AppRun" "$@"
`

type limitedBuffer struct {
	data      []byte
	remaining int
	truncated bool
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	original := len(p)
	if len(p) > b.remaining {
		p = p[:b.remaining]
		b.truncated = true
	}
	b.data = append(b.data, p...)
	b.remaining -= len(p)
	return original, nil
}

func (b *limitedBuffer) String() string {
	text := string(b.data)
	if b.truncated {
		text += "\n[diagnostic output truncated]"
	}
	return text
}

func truncateDiagnostic(value string) string {
	if len(value) <= maxDiagnosticBytes {
		return value
	}
	return value[:maxDiagnosticBytes] + "\n[diagnostic output truncated]"
}

func extractTarGzip(ctx context.Context, archive, destination, expected string) (string, error) {
	return extractTarGzipWithLimit(ctx, archive, destination, expected, maxExpandedBytes)
}

func extractTarGzipWithLimit(ctx context.Context, archive, destination, expected string, expansionLimit int64) (string, error) {
	file, err := os.Open(archive)
	if err != nil {
		return "", err
	}
	defer file.Close()
	gz, err := gzip.NewReader(file)
	if err != nil {
		return "", fmt.Errorf("open tar.gz: %w", err)
	}
	defer gz.Close()
	decompressed := &boundedContextReader{ctx: ctx, reader: gz, remaining: expansionLimit}
	reader := tar.NewReader(decompressed)
	seen := make(map[string]struct{})
	var matches []string
	var expanded int64
	entries := 0
	for {
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return "", fmt.Errorf("read tar.gz: %w", err)
		}
		entries++
		if entries > maxArchiveEntries {
			return "", fmt.Errorf("archive contains more than %d entries", maxArchiveEntries)
		}
		name, err := safeArchiveName(header.Name)
		if err != nil {
			return "", err
		}
		if _, duplicate := seen[name]; duplicate {
			return "", fmt.Errorf("archive contains duplicate entry %q", name)
		}
		seen[name] = struct{}{}
		target := filepath.Join(destination, filepath.FromSlash(name))
		switch header.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o700); err != nil {
				return "", err
			}
		case tar.TypeReg, tar.TypeRegA:
			if header.Size < 0 || expanded > expansionLimit-header.Size {
				return "", fmt.Errorf("archive expands beyond %d bytes", expansionLimit)
			}
			expanded += header.Size
			if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
				return "", err
			}
			mode := os.FileMode(0o600)
			if header.FileInfo().Mode()&0o111 != 0 || path.Base(name) == expected {
				mode = 0o700
			}
			output, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)
			if err != nil {
				return "", err
			}
			_, copyErr := io.CopyN(output, reader, header.Size)
			closeErr := output.Close()
			if copyErr != nil {
				return "", fmt.Errorf("extract %q: %w", name, copyErr)
			}
			if closeErr != nil {
				return "", closeErr
			}
			if path.Base(name) == expected {
				matches = append(matches, target)
			}
		default:
			return "", fmt.Errorf("archive entry %q has unsupported type %d", name, header.Typeflag)
		}
	}
	var trailing [32 << 10]byte
	for {
		count, readErr := decompressed.Read(trailing[:])
		if count > 0 && !allZero(trailing[:count]) {
			return "", errors.New("archive contains non-padding data after the tar end marker")
		}
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			return "", fmt.Errorf("verify gzip trailer: %w", readErr)
		}
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if err := gz.Close(); err != nil {
		return "", fmt.Errorf("verify gzip trailer: %w", err)
	}
	if len(matches) != 1 {
		return "", fmt.Errorf("archive must contain exactly one %q executable; found %d", expected, len(matches))
	}
	if err := os.Chmod(matches[0], 0o700); err != nil {
		return "", err
	}
	return matches[0], nil
}

type boundedContextReader struct {
	ctx       context.Context
	reader    io.Reader
	remaining int64
}

func (r *boundedContextReader) Read(buffer []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	if r.remaining <= 0 {
		var probe [1]byte
		count, err := r.reader.Read(probe[:])
		if count > 0 {
			return 0, fmt.Errorf("archive expands beyond configured limit")
		}
		return 0, err
	}
	if int64(len(buffer)) > r.remaining {
		buffer = buffer[:r.remaining]
	}
	count, err := r.reader.Read(buffer)
	r.remaining -= int64(count)
	return count, err
}

func allZero(value []byte) bool {
	for _, item := range value {
		if item != 0 {
			return false
		}
	}
	return true
}

func safeArchiveName(name string) (string, error) {
	if name == "" || strings.ContainsRune(name, '\x00') || strings.Contains(name, "\\") || strings.HasPrefix(name, "/") {
		return "", fmt.Errorf("unsafe archive path %q", name)
	}
	clean := path.Clean(name)
	if clean == "." || clean == ".." || strings.HasPrefix(clean, "../") {
		return "", fmt.Errorf("unsafe archive path %q", name)
	}
	return clean, nil
}

func validateExistingInstall(plan Plan) (string, error) {
	root := plan.versionRoot()
	info, err := os.Lstat(root)
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return "", fmt.Errorf("managed version path is not a directory: %s", root)
	}
	if ok, err := verifyFile(filepath.Join(root, ".artifact"), plan.asset.Size, plan.asset.SHA256); err != nil || !ok {
		if err == nil {
			err = errors.New("artifact checksum or size differs from the catalog")
		}
		return "", fmt.Errorf("existing version is invalid: %w", err)
	}
	if err := validateInstallManifest(root); err != nil {
		return "", fmt.Errorf("existing version is invalid: %w", err)
	}
	name, _ := expectedExecutable(plan.tool.ID, plan.asset.Format)
	var executable string
	if plan.asset.Format == "tar.gz" {
		matches, walkErr := findRegularBasename(filepath.Join(root, "package"), name)
		if walkErr != nil || len(matches) != 1 {
			return "", errors.New("existing zsign installation does not contain exactly one executable")
		}
		executable = matches[0]
	} else {
		executable = filepath.Join(root, name)
	}
	if err := verifyExecutableWithin(root, executable); err != nil {
		return "", fmt.Errorf("existing version is invalid: %w", err)
	}
	if plan.asset.Format == "binary" {
		if ok, err := verifyFile(executable, plan.asset.Size, plan.asset.SHA256); err != nil || !ok {
			if err == nil {
				err = errors.New("binary executable differs from the pinned artifact")
			}
			return "", fmt.Errorf("existing version is invalid: %w", err)
		}
	}
	if plan.asset.Format == "appimage" {
		if err := verifyAppRunWithin(root, filepath.Join(root, "squashfs-root", "AppRun")); err != nil {
			return "", fmt.Errorf("existing version is invalid: %w", err)
		}
	}
	return executable, nil
}

func findRegularBasename(root, basename string) ([]string, error) {
	var matches []string
	err := filepath.Walk(root, func(current string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.Mode().IsRegular() && info.Name() == basename {
			matches = append(matches, current)
		}
		return nil
	})
	return matches, err
}

func activate(plan Plan, executable string) error {
	if err := inspectDirectoryPath(plan.paths.BinDir, false); err != nil {
		return err
	}
	if err := verifyPrivateTree(plan.paths.ToolsDir, plan.tool.ID, plan.tool.Version, plan.asset.OS+"-"+plan.asset.Arch); err != nil {
		return err
	}
	if err := verifyExecutableWithin(plan.versionRoot(), executable); err != nil {
		return err
	}
	active := plan.ExecutablePath()
	if info, err := os.Lstat(active); err == nil {
		if info.Mode()&os.ModeSymlink == 0 {
			return fmt.Errorf("%w: %s", ErrActivationCollision, active)
		}
		target, err := os.Readlink(active)
		if err != nil || !isManagedActivationTarget(plan, absoluteLinkTarget(active, target)) {
			return fmt.Errorf("%w: existing symlink is not an Orchard tool link", ErrActivationCollision)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	temporary := filepath.Join(plan.paths.BinDir, "."+plan.tool.ID+".tmp-"+randomToken())
	if err := os.Symlink(executable, temporary); err != nil {
		return fmt.Errorf("create activation link: %w", err)
	}
	defer os.Remove(temporary)
	if err := os.Rename(temporary, active); err != nil {
		return fmt.Errorf("activate tool: %w", err)
	}
	resolved, err := filepath.EvalSymlinks(active)
	if err != nil || filepath.Clean(resolved) != filepath.Clean(executable) {
		return errors.New("activated executable failed verification")
	}
	return nil
}

func isManagedActivationTarget(plan Plan, target string) bool {
	relative, err := filepath.Rel(plan.paths.ToolsDir, filepath.Clean(target))
	if err != nil || filepath.IsAbs(relative) {
		return false
	}
	parts := strings.Split(relative, string(filepath.Separator))
	if len(parts) < 4 || parts[0] != plan.tool.ID {
		return false
	}
	versionRoot := filepath.Join(plan.paths.ToolsDir, parts[0], parts[1], parts[2])
	if !isWithin(versionRoot, target) || verifyPrivateTree(plan.paths.ToolsDir, parts[0], parts[1], parts[2]) != nil {
		return false
	}
	info, err := os.Lstat(filepath.Join(versionRoot, ".artifact"))
	return err == nil && info.Mode().IsRegular() && info.Mode()&os.ModeSymlink == 0
}

func acquireLock(plan Plan) (func(), error) {
	locks := filepath.Join(plan.paths.ToolsDir, ".locks")
	if err := ensurePrivateDir(locks); err != nil {
		return nil, err
	}
	lock := filepath.Join(locks, plan.tool.ID+".lock")
	if err := os.Mkdir(lock, 0o700); err != nil {
		if errors.Is(err, os.ErrExist) {
			return nil, fmt.Errorf("%w: %s", ErrInstallConflict, plan.tool.ID)
		}
		return nil, fmt.Errorf("acquire install lock: %w", err)
	}
	return func() { _ = os.Remove(lock) }, nil
}

func verifyExecutableWithin(root, executable string) error {
	return verifyRunnableWithin(root, executable, false)
}

func verifyAppRunWithin(root, executable string) error {
	return verifyRunnableWithin(root, executable, true)
}

func verifyRunnableWithin(root, executable string, allowSymlink bool) error {
	if !isWithin(root, executable) {
		return errors.New("executable escapes installed version directory")
	}
	info, err := os.Lstat(executable)
	if err != nil {
		return fmt.Errorf("inspect executable: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 && !allowSymlink {
		return errors.New("activated executable must not be a symbolic link")
	}
	if info.Mode()&os.ModeSymlink == 0 && (!info.Mode().IsRegular() || info.Mode().Perm()&0o100 == 0) {
		return errors.New("activated path is not an owner-executable regular file")
	}
	resolved, err := filepath.EvalSymlinks(executable)
	if err != nil || !isWithin(root, resolved) {
		return errors.New("resolved executable escapes installed version directory")
	}
	resolvedInfo, err := os.Stat(resolved)
	if err != nil || !resolvedInfo.Mode().IsRegular() || resolvedInfo.Mode().Perm()&0o100 == 0 {
		return errors.New("resolved executable is not an owner-executable regular file")
	}
	return nil
}

func isWithin(root, candidate string) bool {
	relative, err := filepath.Rel(filepath.Clean(root), filepath.Clean(candidate))
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) && !filepath.IsAbs(relative)
}

func absoluteLinkTarget(link, target string) string {
	if filepath.IsAbs(target) {
		return filepath.Clean(target)
	}
	return filepath.Clean(filepath.Join(filepath.Dir(link), target))
}

func ensurePrivateDir(directory string) error {
	return inspectDirectoryPath(directory, true)
}

func ensurePrivateTree(root string, components ...string) error {
	if err := ensurePrivateDir(root); err != nil {
		return err
	}
	current := root
	for _, component := range components {
		if err := validateComponent("managed directory component", component); err != nil {
			return err
		}
		current = filepath.Join(current, component)
		if err := os.Mkdir(current, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
			return fmt.Errorf("create private directory %s: %w", current, err)
		}
		info, err := os.Lstat(current)
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("managed path component is not a real directory: %s", current)
		}
		if err := os.Chmod(current, 0o700); err != nil {
			return fmt.Errorf("protect private directory %s: %w", current, err)
		}
	}
	return nil
}

func verifyPrivateTree(root string, components ...string) error {
	if err := inspectDirectoryPath(root, false); err != nil {
		return err
	}
	current := root
	for _, component := range components {
		current = filepath.Join(current, component)
		info, err := os.Lstat(current)
		if err != nil {
			return fmt.Errorf("inspect managed path component %s: %w", current, err)
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("managed path component is not a real directory: %s", current)
		}
	}
	return nil
}

func verifyFile(filename string, expectedSize int64, expectedHash string) (bool, error) {
	info, err := os.Lstat(filename)
	if err != nil {
		return false, err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Size() != expectedSize {
		return false, nil
	}
	file, err := os.Open(filename)
	if err != nil {
		return false, err
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, io.LimitReader(file, expectedSize+1)); err != nil {
		return false, err
	}
	return strings.EqualFold(hex.EncodeToString(hash.Sum(nil)), expectedHash), nil
}

func copyFile(source, destination string, mode os.FileMode) error {
	input, err := os.Open(source)
	if err != nil {
		return err
	}
	defer input.Close()
	output, err := os.OpenFile(destination, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(output, input)
	closeErr := output.Close()
	if copyErr != nil {
		return copyErr
	}
	if closeErr != nil {
		return closeErr
	}
	return os.Chmod(destination, mode)
}

func cacheName(plan Plan) string {
	hash := strings.ToLower(plan.asset.SHA256)
	return fmt.Sprintf("%s-%s-%s-%s-%s", plan.tool.ID, plan.tool.Version, plan.asset.OS, plan.asset.Arch, hash)
}

func randomToken() string {
	var value [8]byte
	if _, err := rand.Read(value[:]); err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(value[:])
}

func makeReceipt(plan Plan) Receipt {
	return Receipt{
		Tool:           plan.tool.ID,
		Version:        plan.tool.Version,
		OS:             plan.asset.OS,
		Arch:           plan.asset.Arch,
		SourceURL:      plan.asset.URL,
		SHA256:         strings.ToLower(plan.asset.SHA256),
		ExecutablePath: plan.ExecutablePath(),
		InstalledAt:    time.Now().UTC(),
	}
}
