package bootstrap

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// Paths contains every filesystem root managed by the installer. Custom Paths
// are useful for isolated embedding and tests; all fields must be absolute.
type Paths struct {
	ToolsDir     string
	DownloadsDir string
	BinDir       string
}

// DefaultPaths resolves the XDG data and cache homes. Relative XDG values are
// invalid and fall back to $HOME/.local/share and $HOME/.cache respectively.
func DefaultPaths() (Paths, error) {
	home, err := os.UserHomeDir()
	if err != nil || !filepath.IsAbs(home) {
		return Paths{}, errors.New("resolve an absolute user home directory")
	}
	dataHome := xdgHome("XDG_DATA_HOME", filepath.Join(home, ".local", "share"))
	cacheHome := xdgHome("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	paths := Paths{
		ToolsDir:     filepath.Join(dataHome, "orchard", "tools"),
		DownloadsDir: filepath.Join(cacheHome, "orchard", "downloads"),
		BinDir:       filepath.Join(dataHome, "orchard", "bin"),
	}
	if err := validatePaths(paths); err != nil {
		return Paths{}, err
	}
	return paths, nil
}

func xdgHome(name, fallback string) string {
	if value := os.Getenv(name); value != "" && filepath.IsAbs(value) {
		return filepath.Clean(value)
	}
	return fallback
}

func validatePaths(paths Paths) error {
	values := map[string]string{
		"tools directory":     paths.ToolsDir,
		"downloads directory": paths.DownloadsDir,
		"bin directory":       paths.BinDir,
	}
	seen := make(map[string]string, len(values))
	for label, value := range values {
		if value == "" || !filepath.IsAbs(value) || filepath.Clean(value) != value || value == string(filepath.Separator) {
			return fmt.Errorf("%s must be a clean, absolute, non-root path", label)
		}
		if previous, exists := seen[value]; exists {
			return fmt.Errorf("%s and %s must be distinct", label, previous)
		}
		seen[value] = label
	}
	entries := []struct {
		label string
		path  string
	}{
		{"tools directory", paths.ToolsDir},
		{"downloads directory", paths.DownloadsDir},
		{"bin directory", paths.BinDir},
	}
	for i := range entries {
		for j := i + 1; j < len(entries); j++ {
			if isWithin(entries[i].path, entries[j].path) || isWithin(entries[j].path, entries[i].path) {
				return fmt.Errorf("%s and %s must not overlap", entries[i].label, entries[j].label)
			}
		}
	}
	return nil
}
