package distribution

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"image/png"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

type catalogContents struct {
	Images []catalogImage `json:"images"`
	Info   map[string]any `json:"info,omitempty"`
}

type catalogImage struct {
	Idiom    string `json:"idiom"`
	Size     string `json:"size"`
	Scale    string `json:"scale"`
	Filename string `json:"filename"`
}

var requiredIconSlots = []struct{ idiom, size, scale string }{
	{"iphone", "20x20", "2x"}, {"iphone", "20x20", "3x"},
	{"iphone", "29x29", "2x"}, {"iphone", "29x29", "3x"},
	{"iphone", "40x40", "2x"}, {"iphone", "40x40", "3x"},
	{"iphone", "60x60", "2x"}, {"iphone", "60x60", "3x"},
	{"ipad", "20x20", "1x"}, {"ipad", "20x20", "2x"},
	{"ipad", "29x29", "1x"}, {"ipad", "29x29", "2x"},
	{"ipad", "40x40", "1x"}, {"ipad", "40x40", "2x"},
	{"ipad", "76x76", "1x"}, {"ipad", "76x76", "2x"},
	{"ipad", "83.5x83.5", "2x"}, {"ios-marketing", "1024x1024", "1x"},
}

func iconSlot(idiom, size, scale string) string { return idiom + "/" + size + "/" + scale }

func parseDimension(size, scale string) (int, error) {
	parts := strings.Split(size, "x")
	if len(parts) != 2 || parts[0] != parts[1] {
		return 0, fmt.Errorf("icon size %q is not square", size)
	}
	points, err := strconv.ParseFloat(parts[0], 64)
	if err != nil {
		return 0, err
	}
	multiplier, err := strconv.Atoi(strings.TrimSuffix(scale, "x"))
	if err != nil || multiplier < 1 || !strings.HasSuffix(scale, "x") {
		return 0, fmt.Errorf("invalid scale %q", scale)
	}
	pixels := points * float64(multiplier)
	if pixels != float64(int(pixels)) {
		return 0, fmt.Errorf("size %q at %q is not an integral pixel size", size, scale)
	}
	return int(pixels), nil
}

func validateIconCatalog(ctx context.Context, catalog string, limits Limits) []Problem {
	var problems []Problem
	if err := requireAbsoluteCleanPath("assetCatalogPath", catalog); err != nil {
		return []Problem{problem("invalid_catalog_path", "assetCatalogPath", err.Error())}
	}
	if err := rejectSymlinkPath(catalog, true); err != nil {
		return []Problem{problem("unsafe_catalog", "assetCatalogPath", err.Error())}
	}
	info, err := os.Lstat(catalog)
	if err != nil || !info.IsDir() {
		return []Problem{problem("invalid_catalog", "assetCatalogPath", "asset catalog must be a directory")}
	}
	var appIconSets []string
	entries := 0
	var total int64
	err = filepath.WalkDir(catalog, func(name string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := checkContext(ctx); err != nil {
			return err
		}
		entries++
		if entries > limits.MaxArchiveEntries {
			return fmt.Errorf("catalog exceeds %d-entry limit", limits.MaxArchiveEntries)
		}
		entryInfo, err := entry.Info()
		if err != nil {
			return err
		}
		if entryInfo.Mode()&os.ModeSymlink != 0 || (!entryInfo.IsDir() && !entryInfo.Mode().IsRegular()) {
			return fmt.Errorf("catalog contains nonregular entry %q", name)
		}
		if entryInfo.Mode().IsRegular() {
			total += entryInfo.Size()
			if entryInfo.Size() > limits.MaxFileBytes || total > limits.MaxBundleBytes {
				return fmt.Errorf("catalog exceeds size limit")
			}
		}
		if entry.IsDir() && strings.HasSuffix(entry.Name(), ".appiconset") {
			appIconSets = append(appIconSets, name)
		}
		return nil
	})
	if err != nil {
		return []Problem{problem("unsafe_catalog", "assetCatalogPath", err.Error())}
	}
	if len(appIconSets) != 1 {
		return []Problem{problem("app_icon_set_count", "assetCatalogPath", fmt.Sprintf("catalog must contain exactly one .appiconset; found %d", len(appIconSets)))}
	}
	set := appIconSets[0]
	contentsData, err := readRegularFile(ctx, filepath.Join(set, "Contents.json"), 1<<20)
	if err != nil {
		return []Problem{problem("invalid_icon_contents", "Contents.json", err.Error())}
	}
	var contents catalogContents
	dec := json.NewDecoder(bytes.NewReader(contentsData))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&contents); err != nil {
		return []Problem{problem("invalid_icon_contents", "Contents.json", err.Error())}
	}
	found := map[string]bool{}
	for _, image := range contents.Images {
		slot := iconSlot(image.Idiom, image.Size, image.Scale)
		wanted := false
		for _, required := range requiredIconSlots {
			if slot == iconSlot(required.idiom, required.size, required.scale) {
				wanted = true
				break
			}
		}
		if !wanted {
			continue
		}
		if found[slot] {
			problems = append(problems, problem("duplicate_icon_slot", slot, "icon slot is declared more than once"))
			continue
		}
		found[slot] = true
		if image.Filename == "" || filepath.Base(image.Filename) != image.Filename || filepath.Clean(image.Filename) != image.Filename {
			problems = append(problems, problem("unsafe_icon_filename", slot, "icon filename must be a simple contained filename"))
			continue
		}
		file := filepath.Join(set, image.Filename)
		data, err := readRegularFile(ctx, file, limits.MaxFileBytes)
		if err != nil {
			problems = append(problems, problem("missing_icon_file", slot, err.Error()))
			continue
		}
		cfg, err := png.DecodeConfig(bytes.NewReader(data))
		if err != nil {
			problems = append(problems, problem("invalid_icon_png", slot, "icon must be a valid PNG: "+err.Error()))
			continue
		}
		expected, err := parseDimension(image.Size, image.Scale)
		if err != nil || cfg.Width != expected || cfg.Height != expected {
			problems = append(problems, problem("wrong_icon_dimensions", slot, fmt.Sprintf("icon is %dx%d; want %dx%d", cfg.Width, cfg.Height, expected, expected)))
		}
	}
	for _, required := range requiredIconSlots {
		slot := iconSlot(required.idiom, required.size, required.scale)
		if !found[slot] {
			problems = append(problems, problem("missing_icon_slot", slot, "required explicit iPhone/iPad/marketing PNG icon is missing"))
		}
	}
	return problems
}
