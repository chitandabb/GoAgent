package knowledgelayout

import (
	"bytes"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"testing"
)

func TestCropRasterAppliesBoundedPaddingAndProducesPNG(t *testing.T) {
	page := layoutRasterFixture(t, "jpeg", 100, 80)
	result, err := CropRaster(page, BoundingBox{
		Left: 0.2, Top: 0.25, Right: 0.6, Bottom: 0.75,
	}, CropConfig{PaddingRatio: 0.05, MaxPixels: 20_000, MaxBytes: 64 * 1024})
	if err != nil {
		t.Fatal(err)
	}
	if result.Pixels != (PixelBox{Left: 15, Top: 16, Right: 65, Bottom: 64}) ||
		result.Raster.MediaType != "image/png" || result.Raster.Width != 50 ||
		result.Raster.Height != 48 || result.SourceRasterSHA256 == "" ||
		result.RasterSHA256 == "" || result.EncoderVersion != CropEncoderVersion {
		t.Fatalf("result = %+v", result)
	}
	if _, format, err := image.Decode(bytes.NewReader(result.Raster.Content)); err != nil || format != "png" {
		t.Fatalf("decode crop = %s, %v", format, err)
	}
}

func TestCropRasterClampsPaddingAtPageEdges(t *testing.T) {
	page := layoutRasterFixture(t, "png", 10, 10)
	result, err := CropRaster(page, BoundingBox{
		Left: 0.01, Top: 0.01, Right: 0.2, Bottom: 0.2,
	}, CropConfig{PaddingRatio: 0.05, MaxPixels: 100, MaxBytes: 64 * 1024})
	if err != nil {
		t.Fatal(err)
	}
	if result.AppliedBox.Left != 0 || result.AppliedBox.Top != 0 ||
		result.Pixels.Left != 0 || result.Pixels.Top != 0 {
		t.Fatalf("result = %+v", result)
	}
}

func TestCropRasterRejectsMetadataAndSignatureMismatch(t *testing.T) {
	page := layoutRasterFixture(t, "png", 10, 10)
	page.MediaType = "image/jpeg"
	if _, err := CropRaster(page, FullPageBox(), CropConfig{
		MaxPixels: 100, MaxBytes: 64 * 1024,
	}); err != ErrInvalidInput {
		t.Fatalf("CropRaster error = %v", err)
	}
}

func layoutRasterFixture(t *testing.T, format string, width, height int) RasterPage {
	t.Helper()
	source := image.NewRGBA(image.Rect(0, 0, width, height))
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			source.Set(x, y, color.RGBA{R: uint8(x), G: uint8(y), B: 100, A: 255})
		}
	}
	var encoded bytes.Buffer
	var err error
	mediaType := "image/png"
	if format == "jpeg" {
		mediaType = "image/jpeg"
		err = jpeg.Encode(&encoded, source, &jpeg.Options{Quality: 85})
	} else {
		err = png.Encode(&encoded, source)
	}
	if err != nil {
		t.Fatal(err)
	}
	return RasterPage{
		MediaType: mediaType, Width: width, Height: height, Content: encoded.Bytes(),
	}
}
