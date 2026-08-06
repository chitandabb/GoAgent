package knowledgelayout

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"image"
	"image/draw"
	"image/png"
	"math"
)

const CropEncoderVersion = "go-png-region-crop-v1"

type CropConfig struct {
	PaddingRatio float64
	MaxPixels    int64
	MaxBytes     int64
}

func (c CropConfig) Validate() error {
	if math.IsNaN(c.PaddingRatio) || math.IsInf(c.PaddingRatio, 0) ||
		c.PaddingRatio < 0 || c.PaddingRatio > 0.2 ||
		c.MaxPixels < 1 || c.MaxPixels > 1_000_000_000 ||
		c.MaxBytes < 1 || c.MaxBytes > 256*1024*1024 {
		return ErrInvalidInput
	}
	return nil
}

type PixelBox struct {
	Left   int
	Top    int
	Right  int
	Bottom int
}

func (b PixelBox) Validate(width, height int) error {
	if width < 1 || height < 1 || b.Left < 0 || b.Top < 0 ||
		b.Right > width || b.Bottom > height || b.Left >= b.Right || b.Top >= b.Bottom {
		return ErrInvalidInput
	}
	return nil
}

type CropResult struct {
	RequestedBox       BoundingBox
	AppliedBox         BoundingBox
	Pixels             PixelBox
	SourceRasterSHA256 string
	RasterSHA256       string
	EncoderVersion     string
	Raster             RasterPage
}

func CropRaster(page RasterPage, box BoundingBox, config CropConfig) (CropResult, error) {
	if err := config.Validate(); err != nil {
		return CropResult{}, err
	}
	if err := page.Validate(config.MaxPixels, config.MaxBytes); err != nil {
		return CropResult{}, err
	}
	if err := box.Validate(); err != nil {
		return CropResult{}, err
	}
	decoded, format, err := image.Decode(bytes.NewReader(page.Content))
	if err != nil || !matchingImageFormat(page.MediaType, format) {
		return CropResult{}, ErrInvalidInput
	}
	bounds := decoded.Bounds()
	if bounds.Dx() != page.Width || bounds.Dy() != page.Height {
		return CropResult{}, ErrInvalidInput
	}
	applied := paddedBox(box, config.PaddingRatio)
	pixels := PixelBox{
		Left:   int(math.Floor(applied.Left * float64(page.Width))),
		Top:    int(math.Floor(applied.Top * float64(page.Height))),
		Right:  int(math.Ceil(applied.Right * float64(page.Width))),
		Bottom: int(math.Ceil(applied.Bottom * float64(page.Height))),
	}
	if err := pixels.Validate(page.Width, page.Height); err != nil {
		return CropResult{}, err
	}
	width, height := pixels.Right-pixels.Left, pixels.Bottom-pixels.Top
	if int64(width) > config.MaxPixels/int64(height) {
		return CropResult{}, ErrInvalidInput
	}
	target := image.NewRGBA(image.Rect(0, 0, width, height))
	draw.Draw(target, target.Bounds(), decoded, image.Point{X: bounds.Min.X + pixels.Left, Y: bounds.Min.Y + pixels.Top}, draw.Src)
	var encoded bytes.Buffer
	if err := png.Encode(&encoded, target); err != nil {
		return CropResult{}, err
	}
	if encoded.Len() < 1 || int64(encoded.Len()) > config.MaxBytes {
		return CropResult{}, ErrInvalidInput
	}
	sourceDigest := sha256.Sum256(page.Content)
	rasterDigest := sha256.Sum256(encoded.Bytes())
	return CropResult{
		RequestedBox: box, AppliedBox: applied, Pixels: pixels,
		SourceRasterSHA256: hex.EncodeToString(sourceDigest[:]),
		RasterSHA256:       hex.EncodeToString(rasterDigest[:]),
		EncoderVersion:     CropEncoderVersion,
		Raster: RasterPage{
			MediaType: "image/png", Width: width, Height: height,
			Content: append([]byte(nil), encoded.Bytes()...),
		},
	}, nil
}

func paddedBox(box BoundingBox, padding float64) BoundingBox {
	return BoundingBox{
		Left: math.Max(0, box.Left-padding), Top: math.Max(0, box.Top-padding),
		Right: math.Min(1, box.Right+padding), Bottom: math.Min(1, box.Bottom+padding),
	}
}

func matchingImageFormat(mediaType, format string) bool {
	return (mediaType == "image/png" && format == "png") ||
		(mediaType == "image/jpeg" && format == "jpeg")
}
