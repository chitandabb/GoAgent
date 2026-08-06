package knowledgelayout

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strings"
)

type DocumentSource struct {
	MediaType string
	Content   []byte
	SHA256    string
}

func (s DocumentSource) Validate(maxBytes int64) error {
	if s.MediaType != "application/pdf" || len(s.Content) == 0 ||
		int64(len(s.Content)) > maxBytes || !validSHA256(s.SHA256) {
		return ErrInvalidInput
	}
	digest := sha256.Sum256(s.Content)
	if hex.EncodeToString(digest[:]) != s.SHA256 {
		return ErrInvalidInput
	}
	return nil
}

type RenderRequest struct {
	Source     DocumentSource
	PageNumber int
	DPI        int
}

func (r RenderRequest) Validate(maxSourceBytes int64) error {
	if r.PageNumber < 1 || r.DPI < 72 || r.DPI > 600 {
		return ErrInvalidInput
	}
	return r.Source.Validate(maxSourceBytes)
}

type RendererTrace struct {
	Provider string
	Name     string
	Version  string
}

func (t RendererTrace) Validate() error {
	for _, value := range []string{t.Provider, t.Name, t.Version} {
		if strings.TrimSpace(value) == "" || value != strings.TrimSpace(value) || len(value) > 128 {
			return ErrInvalidInput
		}
	}
	return nil
}

type RenderResult struct {
	PageNumber                   int
	RequestedDPI                 int
	DPI                          int
	SourceSHA256                 string
	RasterSHA256                 string
	Raster                       RasterPage
	Renderer                     RendererTrace
	NativeText                   string
	NativeTextExtractionComplete bool
}

func (r RenderResult) Validate(request RenderRequest, maxPixels, maxBytes int64) error {
	if r.PageNumber != request.PageNumber || r.RequestedDPI != request.DPI ||
		r.DPI < 72 || r.DPI > request.DPI ||
		r.SourceSHA256 != request.Source.SHA256 || !validSHA256(r.RasterSHA256) {
		return ErrInvalidInput
	}
	if r.NativeText != strings.TrimSpace(r.NativeText) || strings.ContainsRune(r.NativeText, '\x00') ||
		(!r.NativeTextExtractionComplete && r.NativeText != "") {
		return ErrInvalidInput
	}
	if err := r.Raster.Validate(maxPixels, maxBytes); err != nil {
		return err
	}
	digest := sha256.Sum256(r.Raster.Content)
	if hex.EncodeToString(digest[:]) != r.RasterSHA256 {
		return ErrInvalidInput
	}
	return r.Renderer.Validate()
}

type PageRenderer interface {
	RenderPage(context.Context, RenderRequest) (RenderResult, error)
}

func validSHA256(value string) bool {
	if len(value) != sha256.Size*2 || strings.ToLower(value) != value {
		return false
	}
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == sha256.Size
}
