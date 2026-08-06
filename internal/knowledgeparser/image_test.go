package knowledgeparser

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"testing"
)

func TestImageParserExtractsBoundedPNGAndJPEGAssets(t *testing.T) {
	parser, _ := NewImageParser(testParserLimits())
	for _, fixture := range []struct {
		name      string
		mediaType string
		content   []byte
	}{
		{name: "png", mediaType: "image/png", content: rasterFixture(t, "png", 80, 60)},
		{name: "jpeg", mediaType: "image/jpeg", content: rasterFixture(t, "jpeg", 120, 90)},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			result, err := parser.Parse(context.Background(), Input{
				MediaType: fixture.mediaType, OriginalName: "fault." + fixture.name, Content: fixture.content,
			})
			if err != nil {
				t.Fatalf("Parse: %v", err)
			}
			if len(result.Elements) != 0 || len(result.VisualAssets) != 1 ||
				result.VisualAssets[0].Width < 1 || result.VisualAssets[0].SHA256 == "" {
				t.Fatalf("result = %+v", result)
			}
			if len(result.Pages) != 1 || !result.Pages[0].VisualCandidatesKnown ||
				result.Pages[0].VisualCandidateCount != 1 {
				t.Fatalf("page observations = %+v", result.Pages)
			}
		})
	}
}

func TestImageParserRejectsSignatureMismatchAndByteLimit(t *testing.T) {
	limits := testParserLimits()
	parser, _ := NewImageParser(limits)
	jpegContent := rasterFixture(t, "jpeg", 20, 20)
	if _, err := parser.Parse(context.Background(), Input{
		MediaType: "image/png", OriginalName: "fault.png", Content: jpegContent,
	}); !errors.Is(err, ErrInvalidContent) {
		t.Fatalf("signature mismatch error = %v", err)
	}

	limits.MaxVisualAssetBytes = 1024
	limits.MaxTotalVisualBytes = 1024
	parser, _ = NewImageParser(limits)
	if _, err := parser.Parse(context.Background(), Input{
		MediaType: "image/png", OriginalName: "fault.png", Content: bytes.Repeat([]byte("x"), 1025),
	}); !errors.Is(err, ErrResourceLimit) {
		t.Fatalf("byte limit error = %v", err)
	}
}

func rasterFixture(t *testing.T, format string, width, height int) []byte {
	t.Helper()
	value := image.NewRGBA(image.Rect(0, 0, width, height))
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			value.Set(x, y, color.RGBA{R: uint8(x), G: uint8(y), B: 100, A: 255})
		}
	}
	var output bytes.Buffer
	var err error
	if format == "jpeg" {
		err = jpeg.Encode(&output, value, &jpeg.Options{Quality: 80})
	} else {
		err = png.Encode(&output, value)
	}
	if err != nil {
		t.Fatal(err)
	}
	return output.Bytes()
}
