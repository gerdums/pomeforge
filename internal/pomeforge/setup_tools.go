package pomeforge

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	toolcatalog "pomeforge.local/pomeforge"
	"pomeforge.local/pomeforge/internal/bootstrap"
)

const (
	assetKitRevision = "e763558b55fcbb5a443b1d7b2c6f0972d8bd14f7"
	unxipRevision    = "6c3990517fcc4c1db6952fccf4c562fb14097601"
	maxHelperBytes   = int64(512 << 20)
)

type HelperReceipt struct {
	ID               string    `json:"id"`
	Path             string    `json:"path"`
	SHA256           string    `json:"sha256"`
	SourceRevision   string    `json:"sourceRevision,omitempty"`
	AssetKitRevision string    `json:"assetKitRevision,omitempty"`
	RegisteredAt     time.Time `json:"registeredAt"`
}

type helperRegistry struct {
	SchemaVersion int             `json:"schemaVersion"`
	Helpers       []HelperReceipt `json:"helpers"`
}

type IntegratedToolResolver struct {
	Catalog       bootstrap.Catalog
	Paths         bootstrap.Paths
	System        SystemToolResolver
	StateDir      string
	HostOS        string
	HostArch      string
	XDGConfigHome string
}

func DefaultIntegratedToolResolver() (*IntegratedToolResolver, error) {
	catalog, err := toolcatalog.Load()
	if err != nil {
		return nil, err
	}
	paths, err := bootstrap.DefaultPaths()
	if err != nil {
		return nil, err
	}
	configHome, err := effectiveXDGConfigHome()
	if err != nil {
		return nil, err
	}
	return &IntegratedToolResolver{Catalog: catalog, Paths: paths, StateDir: filepath.Dir(paths.ToolsDir), XDGConfigHome: configHome}, nil
}

func (r *IntegratedToolResolver) ProbeAll(ctx context.Context) []ToolStatus {
	ids := make([]string, 0, len(toolDefinitions)+2)
	for _, definition := range toolDefinitions {
		ids = append(ids, definition.id)
	}
	ids = append(ids, "pomeforge-assets", "unxip")
	result := make([]ToolStatus, len(ids))
	for i, id := range ids {
		result[i] = r.Probe(ctx, id)
	}
	return result
}

func (r *IntegratedToolResolver) Probe(ctx context.Context, id string) ToolStatus {
	if id == "pomeforge-assets" || id == "unxip" {
		return r.probeHelper(ctx, id)
	}
	definition, found := definitionByID(id)
	if !found {
		return ToolStatus{ID: id, Name: id, Status: "missing", Detail: "unknown tool adapter"}
	}
	system := r.System
	if system.XDGConfigHome == "" {
		system.XDGConfigHome = r.XDGConfigHome
	}
	var managedErr error
	if catalogHasTool(r.Catalog, id) {
		hostOS, hostArch := r.HostOS, r.HostArch
		if hostOS == "" {
			hostOS = runtime.GOOS
		}
		if hostArch == "" {
			hostArch = runtime.GOARCH
		}
		plan, err := bootstrap.PlanInstall(r.Catalog, id, hostOS, hostArch, r.Paths)
		if err == nil {
			receipt, verifyErr := bootstrap.VerifyInstalled(ctx, plan)
			if verifyErr == nil {
				definition.executable = receipt.ExecutablePath
				status := system.probeDefinition(ctx, definition)
				if status.Status == "available" {
					status.Detail = "checksum-pinned managed install verified in private user state"
				}
				return status
			}
			managedErr = verifyErr
		} else {
			managedErr = err
		}
	}
	// Reject the path as it appeared on PATH before probeDefinition resolves
	// symlinks. Otherwise a corrupt managed activation link could point outside
	// the managed tree and be mistaken for an independent system executable.
	if candidate, lookupErr := exec.LookPath(definition.executable); lookupErr == nil {
		if absolute, absErr := filepath.Abs(candidate); absErr == nil {
			if lexicalPathInsideAny(absolute, r.Paths.BinDir, r.Paths.ToolsDir) {
				return ToolStatus{ID: id, Name: definition.name, Status: "unverified", Path: absolute, Detail: "managed path was found but its pinned receipt and installed tree did not verify", InstallURL: definition.installURL}
			}
			definition.executable = absolute
		}
	}
	status := system.probeDefinition(ctx, definition)
	if status.Path != "" && pathInsideAny(status.Path, r.Paths.BinDir, r.Paths.ToolsDir) {
		status.Status = "unverified"
		status.Detail = "managed path was found but its pinned receipt and installed tree did not verify"
		return status
	}
	if status.Status == "missing" && managedErr != nil && !errors.Is(managedErr, bootstrap.ErrInstallAbsent) {
		status.Status = "unverified"
		status.Detail = "managed installation is present but invalid: " + managedErr.Error()
	}
	return status
}

func definitionByID(id string) (toolDefinition, bool) {
	for _, definition := range toolDefinitions {
		if definition.id == id {
			return definition, true
		}
	}
	return toolDefinition{}, false
}

func catalogHasTool(catalog bootstrap.Catalog, id string) bool {
	for _, tool := range catalog.Tools {
		if tool.ID == id {
			return true
		}
	}
	return false
}

func pathInsideAny(value string, roots ...string) bool {
	resolved, err := filepath.EvalSymlinks(value)
	if err == nil {
		value = resolved
	}
	for _, root := range roots {
		relative, relErr := filepath.Rel(root, value)
		if relErr == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			return true
		}
	}
	return false
}

func lexicalPathInsideAny(value string, roots ...string) bool {
	value = filepath.Clean(value)
	for _, root := range roots {
		relative, err := filepath.Rel(filepath.Clean(root), value)
		if err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			return true
		}
	}
	return false
}

func (r *IntegratedToolResolver) probeHelper(ctx context.Context, id string) ToolStatus {
	name := map[string]string{"pomeforge-assets": "Pomeforge AssetKit bridge", "unxip": "unxip XIP extractor"}[id]
	status := ToolStatus{ID: id, Name: name, Status: "missing", Detail: helperGuidance(id), InstallURL: helperURL(id)}
	receipts, err := loadHelperReceipts(filepath.Join(r.StateDir, "helpers.json"))
	if err == nil {
		for _, receipt := range receipts {
			if receipt.ID == id {
				return verifyHelper(ctx, receipt, status)
			}
		}
	}
	packaged := "/usr/local/bin/" + id
	if info, statErr := os.Lstat(packaged); statErr == nil && info.Mode().IsRegular() {
		if packagedReceipts, receiptErr := loadHelperReceipts("/usr/local/share/pomeforge/helpers.json"); receiptErr == nil {
			for _, receipt := range packagedReceipts {
				if receipt.ID == id && filepath.Clean(receipt.Path) == packaged {
					return verifyHelper(ctx, receipt, status)
				}
			}
		}
		status.Status = "unverified"
		status.Path = packaged
		status.Detail = "packaged executable exists but has no registered SHA-256 receipt; register it explicitly"
	}
	return status
}

func verifyHelper(ctx context.Context, receipt HelperReceipt, fallback ToolStatus) ToolStatus {
	status := fallback
	status.Path = receipt.Path
	if receipt.ID == "unxip" && receipt.SourceRevision != "" && receipt.SourceRevision != unxipRevision {
		status.Status = "unverified"
		status.Detail = "registered unxip source revision does not match Pomeforge's reviewed revision"
		return status
	}
	if receipt.ID == "pomeforge-assets" && receipt.AssetKitRevision != "" && receipt.AssetKitRevision != assetKitRevision {
		status.Status = "unverified"
		status.Detail = "registered AssetKit revision does not match Pomeforge's pinned revision"
		return status
	}
	digest, err := hashRegularFile(ctx, receipt.Path, maxHelperBytes)
	if err != nil || !strings.EqualFold(digest, receipt.SHA256) {
		status.Status = "unverified"
		status.Detail = "registered helper no longer matches its stored SHA-256"
		return status
	}
	definition := toolDefinition{id: receipt.ID, name: status.Name, executable: receipt.Path, installURL: status.InstallURL}
	if receipt.ID == "unxip" {
		definition.versionArgs = []string{"--version"}
	} else {
		definition.versionArgs = []string{"--help"}
	}
	probed := (SystemToolResolver{}).probeDefinition(ctx, definition)
	if probed.Status != "available" {
		probed.Status = "unverified"
		probed.Detail = "registered helper integrity matched, but its executable probe failed"
		return probed
	}
	if receipt.ID == "unxip" && probed.Version != "unxip 3.3" {
		probed.Status = "unverified"
		probed.Detail = "Pomeforge has verified the unxip 3.3 command contract"
		return probed
	}
	if receipt.ID == "pomeforge-assets" {
		help := strings.ToLower(probed.Version)
		if !strings.Contains(help, "pomeforge-assets") || !strings.Contains(help, "compile") {
			probed.Status = "unverified"
			probed.Detail = "registered executable did not report the pomeforge-assets compile help contract"
			return probed
		}
	}
	if receipt.ID == "unxip" {
		helpDefinition := definition
		helpDefinition.versionArgs = []string{"--help"}
		help := (SystemToolResolver{}).probeDefinition(ctx, helpDefinition)
		if help.Status != "available" {
			probed.Status = "unverified"
			probed.Detail = "unxip version matched, but its --help contract probe failed"
			return probed
		}
	}
	probed.Detail = "registered executable matched its stored SHA-256"
	if receipt.SourceRevision != "" {
		probed.Detail += "; source revision " + receipt.SourceRevision
	}
	if receipt.AssetKitRevision != "" {
		probed.Detail += "; AssetKit revision " + receipt.AssetKitRevision
	}
	return probed
}

func helperGuidance(id string) string {
	if id == "unxip" {
		return "build unxip 3.3 from source revision " + unxipRevision + " with Swift 6.3.3, liblzma-dev, and zlib1g-dev, then register the executable"
	}
	return "run `swift build --package-path tools/asset-compiler -c release`, then register the resulting pomeforge-assets executable"
}

func helperURL(id string) string {
	if id == "unxip" {
		return "https://github.com/saagarjha/unxip"
	}
	return "https://github.com/xtool-org/AssetKit"
}

func hashRegularFile(ctx context.Context, path string, limit int64) (string, error) {
	if path == "" || !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return "", errors.New("executable path must be clean and absolute")
	}
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return "", errors.New("path must identify a regular file, not a symbolic link")
	}
	if info.Mode().Perm()&0o111 == 0 {
		return "", errors.New("file is not executable")
	}
	if info.Size() < 0 || info.Size() > limit {
		return "", fmt.Errorf("file exceeds the %d-byte integrity limit", limit)
	}
	file, err := openRegularAbsolute(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	openedInfo, err := file.Stat()
	if err != nil || !openedInfo.Mode().IsRegular() || openedInfo.Mode().Perm()&0o111 == 0 || openedInfo.Size() != info.Size() {
		return "", errors.New("executable changed while opening it for integrity verification")
	}
	hash := sha256.New()
	buffer := make([]byte, 128*1024)
	var read int64
	for {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		n, readErr := file.Read(buffer)
		if n > 0 {
			read += int64(n)
			if read > limit {
				return "", errors.New("file grew beyond the integrity limit while hashing")
			}
			_, _ = hash.Write(buffer[:n])
		}
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			return "", readErr
		}
	}
	if read != info.Size() {
		return "", errors.New("file changed while hashing")
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func loadHelperReceipts(path string) ([]HelperReceipt, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Size() <= 0 || info.Size() > 1<<20 {
		return nil, errors.New("helper registry must be a bounded regular file")
	}
	file, err := openRegularAbsolute(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	decoder := json.NewDecoder(io.LimitReader(file, 1<<20))
	decoder.DisallowUnknownFields()
	var registry helperRegistry
	if err := decoder.Decode(&registry); err != nil {
		return nil, err
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return nil, errors.New("helper registry contains trailing data")
	}
	if registry.SchemaVersion != 1 {
		return nil, errors.New("unsupported helper registry schema")
	}
	seen := map[string]bool{}
	for _, receipt := range registry.Helpers {
		if receipt.ID != "pomeforge-assets" && receipt.ID != "unxip" {
			return nil, errors.New("helper registry contains an unsupported helper")
		}
		if seen[receipt.ID] {
			return nil, errors.New("helper registry contains a duplicate helper")
		}
		seen[receipt.ID] = true
		if !filepath.IsAbs(receipt.Path) || filepath.Clean(receipt.Path) != receipt.Path {
			return nil, errors.New("helper registry contains an invalid executable path")
		}
		if len(receipt.SHA256) != 64 || strings.Trim(receipt.SHA256, "0123456789abcdefABCDEF") != "" {
			return nil, errors.New("helper registry contains an invalid SHA-256")
		}
		for _, revision := range []string{receipt.SourceRevision, receipt.AssetKitRevision} {
			if revision != "" && (len(revision) != 40 || strings.Trim(revision, "0123456789abcdefABCDEF") != "") {
				return nil, errors.New("helper registry contains an invalid source revision")
			}
		}
	}
	return registry.Helpers, nil
}

func storeHelperReceipt(stateDir string, receipt HelperReceipt) error {
	if err := ensurePrivateDirectory(stateDir); err != nil {
		return err
	}
	path := filepath.Join(stateDir, "helpers.json")
	receipts, err := loadHelperReceipts(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	kept := make([]HelperReceipt, 0, len(receipts)+1)
	for _, existing := range receipts {
		if existing.ID != receipt.ID {
			kept = append(kept, existing)
		}
	}
	kept = append(kept, receipt)
	sort.Slice(kept, func(i, j int) bool { return kept[i].ID < kept[j].ID })
	data, err := json.MarshalIndent(helperRegistry{SchemaVersion: 1, Helpers: kept}, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	temporary := filepath.Join(stateDir, ".helpers-"+operationID()+".tmp")
	file, err := os.OpenFile(temporary, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err = file.Write(data); err == nil {
		err = file.Sync()
	}
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		_ = os.Remove(temporary)
		return err
	}
	if info, statErr := os.Lstat(path); statErr == nil && (!info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0) {
		_ = os.Remove(temporary)
		return errors.New("helper registry is not a regular file")
	}
	return os.Rename(temporary, path)
}

func ensurePrivateDirectory(path string) error {
	if path == "" || !filepath.IsAbs(path) || filepath.Clean(path) != path || path == string(filepath.Separator) {
		return errors.New("private state directory must be a clean absolute non-root path")
	}
	volume := filepath.VolumeName(path)
	current := volume + string(filepath.Separator)
	parts := strings.Split(strings.TrimPrefix(path, current), string(filepath.Separator))
	for index, part := range parts {
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if errors.Is(err, os.ErrNotExist) {
			if err := os.Mkdir(current, 0o700); err != nil {
				return err
			}
			info, err = os.Lstat(current)
		}
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("private state path contains a non-directory or symbolic link: %s", current)
		}
		if index == len(parts)-1 {
			if err := os.Chmod(current, 0o700); err != nil {
				return err
			}
		}
	}
	return nil
}
