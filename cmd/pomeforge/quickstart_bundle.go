package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"pomeforge.local/pomeforge/internal/pomeforge"
)

type quickstartBundle struct {
	runtime string
	helpers map[string]string
}

type quickstartBundleEntry struct {
	SHA256 string `json:"sha256"`
	Mode   uint32 `json:"mode"`
	Size   int64  `json:"size"`
}

func installedQuickstartBundle() (quickstartBundle, error) {
	executable, err := os.Executable()
	if err != nil {
		return quickstartBundle{}, err
	}
	return verifyQuickstartBundle(executable)
}

// Automatic provenance assertions apply only to the running package's native
// helpers. A similarly named binary found elsewhere on PATH is not a package.
func verifyQuickstartBundle(executable string) (quickstartBundle, error) {
	executable, err := filepath.EvalSymlinks(executable)
	if err != nil {
		return quickstartBundle{}, err
	}
	directory := filepath.Dir(executable)
	if filepath.Base(directory) != "libexec" || filepath.Base(executable) != "pomeforge" {
		return quickstartBundle{}, invalidQuickstartBundle("use the installed package launcher")
	}
	root := filepath.Dir(directory)
	manifestPath := filepath.Join(root, "share", "package-manifest.json")
	manifest, err := openQuickstartBundleFile(manifestPath)
	if err != nil {
		return quickstartBundle{}, invalidQuickstartBundle("package manifest is unavailable")
	}
	defer manifest.Close()
	data, err := io.ReadAll(io.LimitReader(manifest, (4<<20)+1))
	if err != nil || len(data) > 4<<20 {
		return quickstartBundle{}, invalidQuickstartBundle("package manifest is unreadable or too large")
	}
	entries := map[string]quickstartBundleEntry{}
	if err := json.Unmarshal(data, &entries); err != nil {
		return quickstartBundle{}, invalidQuickstartBundle("package manifest is invalid")
	}
	bundle := quickstartBundle{runtime: filepath.Join(directory, "pomeforge-runtime"), helpers: map[string]string{}}
	for _, name := range []string{"pomeforge", "pomeforge-runtime", "unxip", "pomeforge-assets", "runtime-lock.json"} {
		entry, ok := entries["libexec/"+name]
		executable := name != "runtime-lock.json"
		if !ok || entry.Size <= 0 || entry.Size > 512<<20 || (entry.Mode&0111 != 0) != executable || entry.Mode > 0777 || len(entry.SHA256) != 64 {
			return quickstartBundle{}, invalidQuickstartBundle("missing or invalid manifest entry for " + name)
		}
		path := filepath.Join(directory, name)
		if err := checkQuickstartBundleFile(path, entry); err != nil {
			return quickstartBundle{}, invalidQuickstartBundle(name + " does not match the package manifest")
		}
		if name == "unxip" || name == "pomeforge-assets" {
			bundle.helpers[name] = path
		}
	}
	return bundle, nil
}

func openQuickstartBundleFile(path string) (*os.File, error) {
	canonical, err := filepath.EvalSymlinks(path)
	if err != nil || canonical != path {
		return nil, fmt.Errorf("package file must not use symlinks")
	}
	before, err := os.Lstat(path)
	if err != nil || !before.Mode().IsRegular() {
		return nil, fmt.Errorf("package file is not regular")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	after, err := file.Stat()
	if err != nil || !after.Mode().IsRegular() || !os.SameFile(before, after) {
		file.Close()
		return nil, fmt.Errorf("package file changed while opening")
	}
	return file, nil
}

func checkQuickstartBundleFile(path string, entry quickstartBundleEntry) error {
	file, err := openQuickstartBundleFile(path)
	if err != nil {
		return err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || info.Size() != entry.Size || uint32(info.Mode().Perm()) != entry.Mode {
		return fmt.Errorf("package size or mode differs")
	}
	digest := sha256.New()
	count, err := io.Copy(digest, io.LimitReader(file, entry.Size+1))
	if err != nil || count != entry.Size || hex.EncodeToString(digest.Sum(nil)) != entry.SHA256 {
		return fmt.Errorf("package hash differs")
	}
	return nil
}

func invalidQuickstartBundle(detail string) error {
	return pomeforge.Errorf("invalid_package", detail+"; reinstall the official Pomeforge package or use the documented manual tools/sdk commands")
}
