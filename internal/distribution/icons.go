package distribution

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"image/png"
	"io"
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
	root, err := openDirectoryNoFollow(catalog)
	if err != nil {
		return []Problem{problem("invalid_catalog", "assetCatalogPath", "asset catalog must be a directory")}
	}
	_ = root.Close()
	var appIconSets []string
	var total int64
	err = walkRegularTree(ctx, catalog, limits.MaxArchiveEntries, func(rel string, entryInfo os.FileInfo, _ *os.File) (bool, error) {
		if entryInfo.Mode().IsRegular() {
			total += entryInfo.Size()
			if entryInfo.Size() > limits.MaxFileBytes || total > limits.MaxBundleBytes {
				return false, fmt.Errorf("catalog exceeds size limit")
			}
		}
		if entryInfo.IsDir() && strings.HasSuffix(filepath.Base(rel), ".appiconset") {
			appIconSets = append(appIconSets, filepath.Join(catalog, rel))
		}
		return false, nil
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
	var trailing any
	if err := dec.Decode(&trailing); err == nil {
		return []Problem{problem("invalid_icon_contents", "Contents.json", "trailing JSON value")}
	} else if !errors.Is(err, io.EOF) {
		return []Problem{problem("invalid_icon_contents", "Contents.json", "malformed trailing data: "+err.Error())}
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
			continue
		}
		decoded, err := png.Decode(bytes.NewReader(data))
		if err != nil || decoded.Bounds().Dx() != expected || decoded.Bounds().Dy() != expected {
			problems = append(problems, problem("invalid_icon_png", slot, "icon pixel data cannot be fully decoded"))
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
