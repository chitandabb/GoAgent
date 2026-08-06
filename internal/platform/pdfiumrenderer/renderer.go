// Package pdfiumrenderer adapts sandboxed PDFium-WASM page rendering to the
// knowledge layout PageRenderer contract.
package pdfiumrenderer

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"image/png"
	"io"
	"math"
	"strings"
	"sync"
	"time"

	pdfium "github.com/klippa-app/go-pdfium"
	"github.com/klippa-app/go-pdfium/requests"
	"github.com/klippa-app/go-pdfium/responses"
	"github.com/klippa-app/go-pdfium/webassembly"
	"github.com/tetratelabs/wazero"

	"github.com/chitandabb/GoAgent/internal/knowledgelayout"
)

var errEncodedRasterTooLarge = errors.New("encoded PDF page exceeds byte limit")

var errExtractedTextTooLarge = errors.New("extracted PDF page text exceeds rune limit")

type Config struct {
	RendererVersion   string
	MaxSourceBytes    int64
	MaxRasterPixels   int64
	MaxRasterBytes    int64
	MaxExtractedRunes int
	MaxConcurrent     int
	AcquireTimeout    time.Duration
	RenderTimeout     time.Duration
}

func (c Config) Validate() error {
	if strings.TrimSpace(c.RendererVersion) == "" ||
		c.RendererVersion != strings.TrimSpace(c.RendererVersion) ||
		len(c.RendererVersion) > 128 ||
		c.MaxSourceBytes < 1 ||
		c.MaxRasterPixels < 1 || c.MaxRasterPixels > 1_000_000_000 ||
		c.MaxRasterBytes < 1 || c.MaxRasterBytes > 256*1024*1024 ||
		c.MaxExtractedRunes < 1 || c.MaxExtractedRunes > 100_000_000 ||
		c.MaxConcurrent < 1 || c.MaxConcurrent > 64 ||
		c.AcquireTimeout < 100*time.Millisecond || c.AcquireTimeout > 2*time.Minute ||
		c.RenderTimeout < 100*time.Millisecond || c.RenderTimeout > 5*time.Minute {
		return knowledgelayout.ErrInvalidInput
	}
	return nil
}

type instance interface {
	OpenDocument(*requests.OpenDocument) (*responses.OpenDocument, error)
	FPDF_CloseDocument(*requests.FPDF_CloseDocument) (*responses.FPDF_CloseDocument, error)
	FPDF_GetPageCount(*requests.FPDF_GetPageCount) (*responses.FPDF_GetPageCount, error)
	GetPageSizeInPixels(*requests.GetPageSizeInPixels) (*responses.GetPageSizeInPixels, error)
	RenderPageInDPI(*requests.RenderPageInDPI) (*responses.RenderPageInDPI, error)
	FPDFText_LoadPage(*requests.FPDFText_LoadPage) (*responses.FPDFText_LoadPage, error)
	FPDFText_ClosePage(*requests.FPDFText_ClosePage) (*responses.FPDFText_ClosePage, error)
	FPDFText_CountChars(*requests.FPDFText_CountChars) (*responses.FPDFText_CountChars, error)
	FPDFText_GetText(*requests.FPDFText_GetText) (*responses.FPDFText_GetText, error)
	Close() error
	Kill() error
}

type instancePool interface {
	GetInstanceWithContext(context.Context) (instance, error)
	Close() error
}

type pdfiumPool struct {
	value pdfium.Pool
}

func (p pdfiumPool) GetInstanceWithContext(ctx context.Context) (instance, error) {
	return p.value.GetInstanceWithContext(ctx)
}

func (p pdfiumPool) Close() error {
	return p.value.Close()
}

type Renderer struct {
	config Config
	pool   instancePool
	mu     sync.RWMutex
	closed bool
}

// OpenWASM compiles the embedded PDFium module without mounting a filesystem.
// ReuseWorkers is disabled so a completed render releases the module's memory.
func OpenWASM(ctx context.Context, config Config) (*Renderer, error) {
	if err := config.Validate(); err != nil {
		return nil, err
	}
	if ctx == nil {
		return nil, knowledgelayout.ErrInvalidInput
	}
	pool, err := webassembly.Init(webassembly.Config{
		Context:       ctx,
		MinIdle:       0,
		MaxIdle:       0,
		MaxTotal:      config.MaxConcurrent,
		FSConfig:      wazero.NewFSConfig(),
		RuntimeConfig: wazero.NewRuntimeConfig().WithCloseOnContextDone(true),
		Stdout:        io.Discard,
		Stderr:        io.Discard,
		ReuseWorkers:  false,
	})
	if err != nil {
		return nil, errors.Join(knowledgelayout.ErrRendererUnavailable, err)
	}
	return newRenderer(pdfiumPool{value: pool}, config)
}

func newRenderer(pool instancePool, config Config) (*Renderer, error) {
	if pool == nil {
		return nil, knowledgelayout.ErrRendererUnavailable
	}
	if err := config.Validate(); err != nil {
		return nil, err
	}
	return &Renderer{config: config, pool: pool}, nil
}

func (r *Renderer) RenderPage(
	ctx context.Context,
	request knowledgelayout.RenderRequest,
) (knowledgelayout.RenderResult, error) {
	if r == nil {
		return knowledgelayout.RenderResult{}, knowledgelayout.ErrRendererUnavailable
	}
	r.mu.RLock()
	if r.closed || r.pool == nil {
		r.mu.RUnlock()
		return knowledgelayout.RenderResult{}, knowledgelayout.ErrRendererUnavailable
	}
	pool := r.pool
	config := r.config
	r.mu.RUnlock()
	if err := request.Validate(config.MaxSourceBytes); err != nil {
		return knowledgelayout.RenderResult{}, err
	}
	if err := ctx.Err(); err != nil {
		return knowledgelayout.RenderResult{}, err
	}
	acquireCtx, cancelAcquire := context.WithTimeout(ctx, config.AcquireTimeout)
	defer cancelAcquire()
	worker, err := pool.GetInstanceWithContext(acquireCtx)
	if err != nil {
		if acquireCtx.Err() != nil {
			return knowledgelayout.RenderResult{}, acquireCtx.Err()
		}
		return knowledgelayout.RenderResult{}, errors.Join(knowledgelayout.ErrRendererUnavailable, err)
	}

	renderCtx, cancelRender := context.WithTimeout(ctx, config.RenderTimeout)
	defer cancelRender()
	type outcome struct {
		result knowledgelayout.RenderResult
		err    error
	}
	completed := make(chan outcome, 1)
	go func() {
		var current outcome
		defer func() {
			if recovered := recover(); recovered != nil {
				current = outcome{err: fmt.Errorf("PDFium renderer panic: %v", recovered)}
			}
			completed <- current
		}()
		current.result, current.err = render(worker, request, config)
	}()

	select {
	case current := <-completed:
		_ = worker.Close()
		return current.result, current.err
	case <-renderCtx.Done():
		_ = worker.Kill()
		return knowledgelayout.RenderResult{}, renderCtx.Err()
	}
}

func render(
	worker instance,
	request knowledgelayout.RenderRequest,
	config Config,
) (knowledgelayout.RenderResult, error) {
	content := append([]byte(nil), request.Source.Content...)
	document, err := worker.OpenDocument(&requests.OpenDocument{File: &content})
	if err != nil {
		return knowledgelayout.RenderResult{}, errors.Join(knowledgelayout.ErrInvalidInput, err)
	}
	defer func() {
		_, _ = worker.FPDF_CloseDocument(&requests.FPDF_CloseDocument{Document: document.Document})
	}()
	count, err := worker.FPDF_GetPageCount(&requests.FPDF_GetPageCount{Document: document.Document})
	if err != nil {
		return knowledgelayout.RenderResult{}, fmt.Errorf("read PDF page count: %w", err)
	}
	pageIndex := request.PageNumber - 1
	if pageIndex < 0 || pageIndex >= count.PageCount {
		return knowledgelayout.RenderResult{}, knowledgelayout.ErrInvalidInput
	}
	page := requests.Page{ByIndex: &requests.PageByIndex{
		Document: document.Document, Index: pageIndex,
	}}
	nativeText, nativeTextComplete, err := extractNativeText(worker, page, config.MaxExtractedRunes)
	if err != nil {
		return knowledgelayout.RenderResult{}, err
	}
	effectiveDPI, size, err := boundedDPI(worker, page, pageIndex, request.DPI, config.MaxRasterPixels)
	if err != nil {
		return knowledgelayout.RenderResult{}, fmt.Errorf("read PDF page dimensions: %w", err)
	}
	var encoded *boundedBuffer
	for {
		encoded, err = renderPNG(worker, page, pageIndex, effectiveDPI, size, config.MaxRasterBytes)
		if !errors.Is(err, errEncodedRasterTooLarge) {
			break
		}
		if effectiveDPI <= 72 {
			return knowledgelayout.RenderResult{}, errors.Join(knowledgelayout.ErrInvalidInput, err)
		}
		effectiveDPI = max(72, int(math.Floor(float64(effectiveDPI)*0.8)))
		size, err = pageSize(worker, page, pageIndex, effectiveDPI)
		if err != nil {
			return knowledgelayout.RenderResult{}, fmt.Errorf("read reduced PDF page dimensions: %w", err)
		}
	}
	if err != nil {
		return knowledgelayout.RenderResult{}, err
	}
	sourceDigest := sha256.Sum256(request.Source.Content)
	rasterDigest := sha256.Sum256(encoded.Bytes())
	return knowledgelayout.RenderResult{
		PageNumber: request.PageNumber, RequestedDPI: request.DPI, DPI: effectiveDPI,
		SourceSHA256: hex.EncodeToString(sourceDigest[:]),
		RasterSHA256: hex.EncodeToString(rasterDigest[:]),
		Raster: knowledgelayout.RasterPage{
			MediaType: "image/png", Width: size.Width, Height: size.Height,
			Content: append([]byte(nil), encoded.Bytes()...),
		},
		Renderer: knowledgelayout.RendererTrace{
			Provider: "pdfium-wasm", Name: "pdfium", Version: config.RendererVersion,
		},
		NativeText: nativeText, NativeTextExtractionComplete: nativeTextComplete,
	}, nil
}

func extractNativeText(worker instance, page requests.Page, maxRunes int) (string, bool, error) {
	loaded, err := worker.FPDFText_LoadPage(&requests.FPDFText_LoadPage{Page: page})
	if err != nil || loaded == nil || loaded.TextPage == "" {
		return "", false, nil
	}
	defer func() {
		_, _ = worker.FPDFText_ClosePage(&requests.FPDFText_ClosePage{TextPage: loaded.TextPage})
	}()
	count, err := worker.FPDFText_CountChars(&requests.FPDFText_CountChars{TextPage: loaded.TextPage})
	if err != nil || count == nil || count.Count < 0 {
		return "", false, nil
	}
	if count.Count > maxRunes {
		return "", false, errExtractedTextTooLarge
	}
	if count.Count == 0 {
		return "", true, nil
	}
	result, err := worker.FPDFText_GetText(&requests.FPDFText_GetText{
		TextPage: loaded.TextPage, StartIndex: 0, Count: count.Count,
	})
	if err != nil || result == nil {
		return "", false, nil
	}
	text := strings.TrimSpace(strings.NewReplacer("\r\n", "\n", "\r", "\n", "\x00", "").Replace(result.Text))
	return text, true, nil
}

func boundedDPI(
	worker instance,
	page requests.Page,
	pageIndex int,
	requestedDPI int,
	maxPixels int64,
) (int, *responses.GetPageSizeInPixels, error) {
	dpi := requestedDPI
	for {
		size, err := pageSize(worker, page, pageIndex, dpi)
		if err != nil {
			return 0, nil, err
		}
		if int64(size.Width) <= maxPixels/int64(size.Height) {
			return dpi, size, nil
		}
		if dpi <= 72 {
			return 0, nil, knowledgelayout.ErrInvalidInput
		}
		pixels := float64(size.Width) * float64(size.Height)
		scale := math.Sqrt(float64(maxPixels)/pixels) * 0.99
		next := int(math.Floor(float64(dpi) * scale))
		if next >= dpi {
			next = dpi - 1
		}
		dpi = max(72, next)
	}
}

func pageSize(
	worker instance,
	page requests.Page,
	pageIndex int,
	dpi int,
) (*responses.GetPageSizeInPixels, error) {
	size, err := worker.GetPageSizeInPixels(&requests.GetPageSizeInPixels{Page: page, DPI: dpi})
	if err != nil {
		return nil, err
	}
	if size.Page != pageIndex || size.Width < 1 || size.Height < 1 {
		return nil, knowledgelayout.ErrInvalidInput
	}
	return size, nil
}

func renderPNG(
	worker instance,
	page requests.Page,
	pageIndex int,
	dpi int,
	size *responses.GetPageSizeInPixels,
	maxBytes int64,
) (*boundedBuffer, error) {
	rendered, err := worker.RenderPageInDPI(&requests.RenderPageInDPI{Page: page, DPI: dpi})
	if err != nil {
		return nil, fmt.Errorf("render PDF page: %w", err)
	}
	if rendered == nil {
		return nil, knowledgelayout.ErrInvalidInput
	}
	defer rendered.Cleanup()
	if rendered.Result.Image == nil {
		return nil, knowledgelayout.ErrInvalidInput
	}
	bounds := rendered.Result.Image.Bounds()
	if rendered.Result.Page != pageIndex ||
		rendered.Result.Width != size.Width || rendered.Result.Height != size.Height ||
		bounds.Dx() != size.Width || bounds.Dy() != size.Height {
		return nil, knowledgelayout.ErrInvalidInput
	}
	encoded := &boundedBuffer{maxBytes: maxBytes}
	if err := png.Encode(encoded, rendered.Result.Image); err != nil {
		if errors.Is(err, errEncodedRasterTooLarge) {
			return nil, errEncodedRasterTooLarge
		}
		return nil, fmt.Errorf("encode PDF page PNG: %w", err)
	}
	if encoded.Len() < 1 {
		return nil, knowledgelayout.ErrInvalidInput
	}
	return encoded, nil
}

func (r *Renderer) Close() error {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return nil
	}
	r.closed = true
	pool := r.pool
	r.pool = nil
	r.mu.Unlock()
	if pool == nil {
		return nil
	}
	return pool.Close()
}

type boundedBuffer struct {
	value    bytes.Buffer
	maxBytes int64
}

func (b *boundedBuffer) Write(value []byte) (int, error) {
	if int64(b.value.Len())+int64(len(value)) > b.maxBytes {
		return 0, errEncodedRasterTooLarge
	}
	return b.value.Write(value)
}

func (b *boundedBuffer) Len() int {
	return b.value.Len()
}

func (b *boundedBuffer) Bytes() []byte {
	return b.value.Bytes()
}
