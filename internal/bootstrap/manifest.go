package bootstrap

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
)

const (
	installManifestName   = ".install-manifest.json"
	manifestSchemaVersion = 1
	maxManifestBytes      = int64(16 << 20)
	maxManifestEntries    = 100_000
	maxManifestFileBytes  = int64(2 << 30)
	maxManifestHashBytes  = int64(4 << 30)
)

type installManifest struct {
	SchemaVersion int             `json:"schemaVersion"`
	Entries       []manifestEntry `json:"entries"`
}

type manifestEntry struct {
	Path       string `json:"path"`
	Type       string `json:"type"`
	Mode       uint32 `json:"mode"`
	Size       int64  `json:"size,omitempty"`
	SHA256     string `json:"sha256,omitempty"`
	LinkTarget string `json:"linkTarget,omitempty"`
}

func writeInstallManifest(root string) error {
	manifest, err := snapshotInstallTree(root)
	if err != nil {
		return err
	}
	data, err := json.Marshal(manifest)
	if err != nil {
		return err
	}
	if int64(len(data)) > maxManifestBytes {
		return fmt.Errorf("install manifest exceeds %d bytes", maxManifestBytes)
	}
	data = append(data, '\n')
	filename := filepath.Join(root, installManifestName)
	file, err := os.OpenFile(filename, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err := file.Write(data); err != nil {
		_ = file.Close()
		return err
	}
	return file.Close()
}

func validateInstallManifest(root string) error {
	expected, err := readInstallManifest(filepath.Join(root, installManifestName))
	if err != nil {
		return fmt.Errorf("read install manifest: %w", err)
	}
	actual, err := snapshotInstallTree(root)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(expected, actual) {
		return errors.New("installed tree differs from its integrity manifest")
	}
	return nil
}

func readInstallManifest(filename string) (installManifest, error) {
	info, err := os.Lstat(filename)
	if err != nil {
		return installManifest{}, err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Size() <= 0 || info.Size() > maxManifestBytes {
		return installManifest{}, errors.New("manifest is not a bounded regular file")
	}
	file, err := os.Open(filename)
	if err != nil {
		return installManifest{}, err
	}
	defer file.Close()
	decoder := json.NewDecoder(io.LimitReader(file, maxManifestBytes+1))
	decoder.DisallowUnknownFields()
	var manifest installManifest
	if err := decoder.Decode(&manifest); err != nil {
		return installManifest{}, err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return installManifest{}, errors.New("manifest contains trailing JSON")
		}
		return installManifest{}, err
	}
	if manifest.SchemaVersion != manifestSchemaVersion {
		return installManifest{}, fmt.Errorf("unsupported manifest schema version %d", manifest.SchemaVersion)
	}
	if len(manifest.Entries) == 0 || len(manifest.Entries) > maxManifestEntries {
		return installManifest{}, errors.New("manifest entry count is outside the allowed range")
	}
	previous := ""
	for index, entry := range manifest.Entries {
		if err := validateManifestEntry(entry); err != nil {
			return installManifest{}, fmt.Errorf("manifest entry %d: %w", index, err)
		}
		if index > 0 && entry.Path <= previous {
			return installManifest{}, errors.New("manifest entries are not uniquely sorted")
		}
		previous = entry.Path
	}
	return manifest, nil
}

func validateManifestEntry(entry manifestEntry) error {
	if entry.Path == "" || entry.Path == installManifestName || strings.ContainsRune(entry.Path, '\x00') || strings.Contains(entry.Path, "\\") {
		return fmt.Errorf("unsafe path %q", entry.Path)
	}
	if entry.Path != "." {
		clean := filepath.ToSlash(filepath.Clean(filepath.FromSlash(entry.Path)))
		if clean != entry.Path || clean == ".." || strings.HasPrefix(clean, "../") || filepath.IsAbs(filepath.FromSlash(entry.Path)) {
			return fmt.Errorf("unsafe path %q", entry.Path)
		}
	}
	switch entry.Type {
	case "directory":
		if entry.Size != 0 || entry.SHA256 != "" || entry.LinkTarget != "" {
			return errors.New("directory has file or link metadata")
		}
	case "file":
		if entry.Size < 0 || entry.Size > maxManifestFileBytes || !hexPattern.MatchString(entry.SHA256) || entry.LinkTarget != "" {
			return errors.New("file metadata is invalid")
		}
	case "symlink":
		if entry.LinkTarget == "" || strings.ContainsRune(entry.LinkTarget, '\x00') || entry.Size != 0 || entry.SHA256 != "" {
			return errors.New("symbolic-link metadata is invalid")
		}
	default:
		return fmt.Errorf("unsupported type %q", entry.Type)
	}
	return nil
}

func snapshotInstallTree(root string) (installManifest, error) {
	manifest := installManifest{SchemaVersion: manifestSchemaVersion}
	var hashed int64
	err := filepath.Walk(root, func(current string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relative, err := filepath.Rel(root, current)
		if err != nil {
			return err
		}
		relative = filepath.ToSlash(relative)
		if relative == installManifestName {
			return nil
		}
		if len(manifest.Entries) >= maxManifestEntries {
			return fmt.Errorf("installed tree contains more than %d entries", maxManifestEntries)
		}
		entry := manifestEntry{Path: relative, Mode: uint32(info.Mode())}
		switch {
		case info.Mode().IsDir():
			entry.Type = "directory"
		case info.Mode().IsRegular():
			if info.Size() < 0 || info.Size() > maxManifestFileBytes || hashed > maxManifestHashBytes-info.Size() {
				return fmt.Errorf("installed tree exceeds hashing bounds at %s", relative)
			}
			hashed += info.Size()
			entry.Type = "file"
			entry.Size = info.Size()
			digest, err := digestBoundedFile(current, info.Size())
			if err != nil {
				return fmt.Errorf("hash installed file %s: %w", relative, err)
			}
			entry.SHA256 = digest
		case info.Mode()&os.ModeSymlink != 0:
			entry.Type = "symlink"
			target, err := os.Readlink(current)
			if err != nil {
				return err
			}
			entry.LinkTarget = target
		default:
			return fmt.Errorf("installed tree contains unsupported file type at %s", relative)
		}
		manifest.Entries = append(manifest.Entries, entry)
		return nil
	})
	if err != nil {
		return installManifest{}, err
	}
	sort.Slice(manifest.Entries, func(i, j int) bool { return manifest.Entries[i].Path < manifest.Entries[j].Path })
	return manifest, nil
}

func digestBoundedFile(filename string, expectedSize int64) (string, error) {
	file, err := os.Open(filename)
	if err != nil {
		return "", err
	}
	defer file.Close()
	hash := sha256.New()
	written, err := io.Copy(hash, io.LimitReader(file, expectedSize+1))
	if err != nil {
		return "", err
	}
	if written != expectedSize {
		return "", errors.New("file size changed while hashing")
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}
