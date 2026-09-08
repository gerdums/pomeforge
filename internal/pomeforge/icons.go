package pomeforge

import (
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"io"
	"os"
	"strconv"
	"strings"
)

const (
	placeholderMarker  = "POMEFORGE_PLACEHOLDER.txt"
	maxIconSourceBytes = 64 << 20
)

type iconSlotSpec struct {
	Idiom string `json:"idiom"`
	Size  string `json:"size"`
	Scale string `json:"scale"`
}

var generatedIconSlots = []iconSlotSpec{
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

type generatedCatalogImage struct {
	iconSlotSpec
	Filename string `json:"filename"`
}

func generatedIconFiles(source image.Image, placeholder bool) (map[string][]byte, error) {
	if source.Bounds().Dx() != 1024 || source.Bounds().Dy() != 1024 {
		return nil, fmt.Errorf("icon source is %dx%d; a square 1024x1024 PNG is required", source.Bounds().Dx(), source.Bounds().Dy())
	}
	files := map[string][]byte{}
	dimensions := map[int]string{}
	contents := struct {
		Images []generatedCatalogImage `json:"images"`
		Info   map[string]any          `json:"info"`
	}{Info: map[string]any{"author": "pomeforge", "version": 1}}
	for _, slot := range generatedIconSlots {
		dimension, err := iconDimension(slot.Size, slot.Scale)
		if err != nil {
			return nil, err
		}
		filename := dimensions[dimension]
		if filename == "" {
			filename = fmt.Sprintf("AppIcon-%d.png", dimension)
			dimensions[dimension] = filename
			resized := resizeOpaque(source, dimension)
			var encoded strings.Builder
			writer := &stringWriter{builder: &encoded}
			if err := png.Encode(writer, resized); err != nil {
				return nil, err
			}
			files[filename] = []byte(encoded.String())
		}
		contents.Images = append(contents.Images, generatedCatalogImage{iconSlotSpec: slot, Filename: filename})
	}
	encoded, err := json.MarshalIndent(contents, "", "  ")
	if err != nil {
		return nil, err
	}
	files["Contents.json"] = append(encoded, '\n')
	if placeholder {
		files[placeholderMarker] = []byte("Generated complete Pomeforge starter artwork. Customize with: pomeforge run icons --project PATH --icon-source ICON_1024.png --execute\n")
	}
	return files, nil
}

type stringWriter struct{ builder *strings.Builder }

func (w *stringWriter) Write(value []byte) (int, error) { return w.builder.Write(value) }

func iconDimension(size, scale string) (int, error) {
	parts := strings.Split(size, "x")
	if len(parts) != 2 || parts[0] != parts[1] || !strings.HasSuffix(scale, "x") {
		return 0, errors.New("invalid icon slot")
	}
	points, err := strconv.ParseFloat(parts[0], 64)
	if err != nil {
		return 0, err
	}
	multiplier, err := strconv.Atoi(strings.TrimSuffix(scale, "x"))
	if err != nil || multiplier < 1 {
		return 0, errors.New("invalid icon scale")
	}
	pixels := points * float64(multiplier)
	if pixels != float64(int(pixels)) {
		return 0, errors.New("icon slot is not an integral pixel dimension")
	}
	return int(pixels), nil
}

func resizeOpaque(source image.Image, dimension int) *image.NRGBA {
	destination := image.NewNRGBA(image.Rect(0, 0, dimension, dimension))
	draw.Draw(destination, destination.Bounds(), &image.Uniform{C: color.NRGBA{R: 245, G: 247, B: 242, A: 255}}, image.Point{}, draw.Src)
	bounds := source.Bounds()
	for y := 0; y < dimension; y++ {
		for x := 0; x < dimension; x++ {
			sourceX := bounds.Min.X + x*bounds.Dx()/dimension
			sourceY := bounds.Min.Y + y*bounds.Dy()/dimension
			pixel := color.NRGBAModel.Convert(source.At(sourceX, sourceY)).(color.NRGBA)
			alpha := uint32(pixel.A)
			background := uint32(255 - pixel.A)
			destination.SetNRGBA(x, y, color.NRGBA{
				R: uint8((uint32(pixel.R)*alpha + 245*background) / 255),
				G: uint8((uint32(pixel.G)*alpha + 247*background) / 255),
				B: uint8((uint32(pixel.B)*alpha + 242*background) / 255),
				A: 255,
			})
		}
	}
	return destination
}

func placeholderIcon() image.Image {
	image := image.NewNRGBA(image.Rect(0, 0, 1024, 1024))
	for y := 0; y < 1024; y++ {
		for x := 0; x < 1024; x++ {
			leaf := (x-520)*(x-520)+(y-490)*(y-490) < 250*250
			if leaf {
				image.SetNRGBA(x, y, color.NRGBA{R: 82, G: 139, B: 91, A: 255})
			} else {
				image.SetNRGBA(x, y, color.NRGBA{R: 27, G: 39, B: 31, A: 255})
			}
		}
	}
	return image
}

func validateIconSource(project, path string) error {
	file, err := openBoundedIconSource(project, path)
	if err != nil {
		return errors.New("icon source is missing or unreadable")
	}
	defer file.Close()
	config, err := png.DecodeConfig(io.LimitReader(file, maxIconSourceBytes+1))
	if err != nil {
		return errors.New("icon source must be a valid PNG")
	}
	if config.Width != 1024 || config.Height != 1024 {
		return fmt.Errorf("icon source is %dx%d; a square 1024x1024 PNG is required", config.Width, config.Height)
	}
	return nil
}

func replacePlaceholderIcons(project, sourcePath string) error {
	file, err := openBoundedIconSource(project, sourcePath)
	if err != nil {
		return err
	}
	source, err := png.Decode(io.LimitReader(file, maxIconSourceBytes+1))
	closeErr := file.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	files, err := generatedIconFiles(source, false)
	if err != nil {
		return err
	}
	return installGeneratedIconFiles(project, files)
}

func openBoundedIconSource(project, path string) (*os.File, error) {
	file, err := openRegularWithin(project, path, os.O_RDONLY, 0)
	if err != nil {
		return nil, err
	}
	info, err := file.Stat()
	if err != nil || info.Size() <= 0 || info.Size() > maxIconSourceBytes {
		_ = file.Close()
		return nil, errors.New("icon source is not a bounded regular file")
	}
	return file, nil
}
