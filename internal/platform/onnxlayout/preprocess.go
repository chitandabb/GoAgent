package onnxlayout

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	_ "image/jpeg"
	_ "image/png"

	"github.com/chitandabb/GoAgent/internal/knowledgelayout"
	xdraw "golang.org/x/image/draw"
)

func preprocess(page knowledgelayout.RasterPage, manifest Manifest) ([]float32, []float32, error) {
	decoded, _, err := image.Decode(bytes.NewReader(page.Content))
	if err != nil {
		return nil, nil, fmt.Errorf("decode layout raster: %w", err)
	}
	bounds := decoded.Bounds()
	if bounds.Dx() != page.Width || bounds.Dy() != page.Height {
		return nil, nil, knowledgelayout.ErrInvalidInput
	}

	opaque := image.NewRGBA(image.Rect(0, 0, bounds.Dx(), bounds.Dy()))
	draw.Draw(opaque, opaque.Bounds(), image.NewUniform(color.White), image.Point{}, draw.Src)
	draw.Draw(opaque, opaque.Bounds(), decoded, bounds.Min, draw.Over)
	resized := image.NewRGBA(image.Rect(0, 0, manifest.Preprocess.InputWidth, manifest.Preprocess.InputHeight))
	xdraw.CatmullRom.Scale(resized, resized.Bounds(), opaque, opaque.Bounds(), draw.Src, nil)

	width := manifest.Preprocess.InputWidth
	height := manifest.Preprocess.InputHeight
	planeSize := width * height
	tensor := make([]float32, 3*planeSize)
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			offset := resized.PixOffset(x, y)
			pixelIndex := y*width + x
			for channel := 0; channel < 3; channel++ {
				value := float64(resized.Pix[offset+channel]) * manifest.Preprocess.Scale
				normalized := (value - manifest.Preprocess.Mean[channel]) / manifest.Preprocess.Std[channel]
				tensor[channel*planeSize+pixelIndex] = float32(normalized)
			}
		}
	}

	scaleFactor := []float32{
		float32(height) / float32(page.Height),
		float32(width) / float32(page.Width),
	}
	return tensor, scaleFactor, nil
}
