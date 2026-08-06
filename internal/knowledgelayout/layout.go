// Package knowledgelayout defines page-layout detection and deterministic
// routing contracts for knowledge ingestion.
package knowledgelayout

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"math"
	"strings"
	"unicode"
)

var (
	ErrInvalidInput        = errors.New("knowledge layout input is invalid")
	ErrRouterUnavailable   = errors.New("knowledge layout router is unavailable")
	ErrRendererUnavailable = errors.New("knowledge page renderer is unavailable")
)

type PageClass string

const (
	PageNativeDigital PageClass = "native_digital"
	PageScanned       PageClass = "scanned"
	PageMixed         PageClass = "mixed"
)

func (c PageClass) Valid() bool {
	return c == PageNativeDigital || c == PageScanned || c == PageMixed
}

type RegionType string

const (
	RegionText       RegionType = "text"
	RegionTable      RegionType = "table"
	RegionPicture    RegionType = "picture"
	RegionCaption    RegionType = "caption"
	RegionFormula    RegionType = "formula"
	RegionDecorative RegionType = "decorative"
)

func (t RegionType) Valid() bool {
	switch t {
	case RegionText, RegionTable, RegionPicture, RegionCaption, RegionFormula, RegionDecorative:
		return true
	default:
		return false
	}
}

// BoundingBox uses normalized page coordinates in [0,1]. Right and Bottom are
// exclusive boundaries. Adapters convert model pixels into this representation.
type BoundingBox struct {
	Left   float64 `json:"left"`
	Top    float64 `json:"top"`
	Right  float64 `json:"right"`
	Bottom float64 `json:"bottom"`
}

func (b BoundingBox) Validate() error {
	values := []float64{b.Left, b.Top, b.Right, b.Bottom}
	for _, value := range values {
		if math.IsNaN(value) || math.IsInf(value, 0) || value < 0 || value > 1 {
			return ErrInvalidInput
		}
	}
	if b.Left >= b.Right || b.Top >= b.Bottom {
		return ErrInvalidInput
	}
	return nil
}

func FullPageBox() BoundingBox {
	return BoundingBox{Left: 0, Top: 0, Right: 1, Bottom: 1}
}

type RasterPage struct {
	MediaType string
	Width     int
	Height    int
	Content   []byte
}

func (p RasterPage) Validate(maxPixels int64, maxBytes int64) error {
	if p.MediaType != "image/png" && p.MediaType != "image/jpeg" {
		return ErrInvalidInput
	}
	if p.Width < 1 || p.Height < 1 || int64(len(p.Content)) < 1 || int64(len(p.Content)) > maxBytes {
		return ErrInvalidInput
	}
	if int64(p.Width) > maxPixels/int64(p.Height) {
		return ErrInvalidInput
	}
	return nil
}

type NativeTextSignals struct {
	RuneCount          int
	NonWhitespaceRunes int
	PrintableRatio     float64
	ExtractionComplete bool
}

func ObserveNativeText(text string, extractionComplete bool) NativeTextSignals {
	runes := []rune(text)
	nonWhitespace := 0
	printable := 0
	for _, value := range runes {
		if !unicode.IsSpace(value) {
			nonWhitespace++
		}
		if unicode.IsPrint(value) || unicode.IsSpace(value) {
			printable++
		}
	}
	ratio := 0.0
	if len(runes) > 0 {
		ratio = float64(printable) / float64(len(runes))
	}
	return NativeTextSignals{
		RuneCount: len(runes), NonWhitespaceRunes: nonWhitespace,
		PrintableRatio: ratio, ExtractionComplete: extractionComplete,
	}
}

func (s NativeTextSignals) Validate() error {
	if s.RuneCount < 0 || s.NonWhitespaceRunes < 0 || s.NonWhitespaceRunes > s.RuneCount ||
		math.IsNaN(s.PrintableRatio) || math.IsInf(s.PrintableRatio, 0) ||
		s.PrintableRatio < 0 || s.PrintableRatio > 1 {
		return ErrInvalidInput
	}
	return nil
}

type PageInput struct {
	PageNumber            int
	NativeText            NativeTextSignals
	VisualCandidateCount  int
	VisualCandidatesKnown bool
	Raster                *RasterPage
}

func (i PageInput) Validate() error {
	if i.PageNumber < 1 || i.VisualCandidateCount < 0 ||
		(i.VisualCandidateCount > 0 && !i.VisualCandidatesKnown) {
		return ErrInvalidInput
	}
	return i.NativeText.Validate()
}

type DetectedRegion struct {
	Type       RegionType
	Box        BoundingBox
	Confidence float64
}

func (r DetectedRegion) Validate() error {
	if !r.Type.Valid() || math.IsNaN(r.Confidence) || math.IsInf(r.Confidence, 0) ||
		r.Confidence < 0 || r.Confidence > 1 {
		return ErrInvalidInput
	}
	return r.Box.Validate()
}

type ModelTrace struct {
	Provider           string
	Name               string
	Version            string
	SHA256             string
	PreprocessVersion  string
	PostprocessVersion string
}

func (m ModelTrace) Validate() error {
	values := []string{m.Provider, m.Name, m.Version, m.PreprocessVersion, m.PostprocessVersion}
	for _, value := range values {
		trimmed := strings.TrimSpace(value)
		if trimmed == "" || trimmed != value || len(value) > 128 {
			return ErrInvalidInput
		}
	}
	if len(m.SHA256) != sha256.Size*2 || strings.ToLower(m.SHA256) != m.SHA256 {
		return ErrInvalidInput
	}
	decoded, err := hex.DecodeString(m.SHA256)
	if err != nil || len(decoded) != sha256.Size {
		return ErrInvalidInput
	}
	return nil
}

type DetectionResult struct {
	Regions []DetectedRegion
	Model   ModelTrace
}

func (r DetectionResult) Validate(maxRegions int) error {
	if maxRegions < 1 || len(r.Regions) > maxRegions {
		return ErrInvalidInput
	}
	if err := r.Model.Validate(); err != nil {
		return err
	}
	for _, region := range r.Regions {
		if err := region.Validate(); err != nil {
			return err
		}
	}
	return nil
}

// LayoutRouter is the local model boundary. Implementations may use ONNX
// Runtime, but must return normalized, model-independent domain detections.
type LayoutRouter interface {
	Detect(context.Context, RasterPage) (DetectionResult, error)
}
