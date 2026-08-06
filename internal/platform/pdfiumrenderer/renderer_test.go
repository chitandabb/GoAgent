package pdfiumrenderer

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"image"
	"image/color"
	"sync/atomic"
	"testing"
	"time"

	"github.com/klippa-app/go-pdfium/references"
	"github.com/klippa-app/go-pdfium/requests"
	"github.com/klippa-app/go-pdfium/responses"

	"github.com/chitandabb/GoAgent/internal/knowledgelayout"
)

func TestRendererProducesTraceableBoundedPNG(t *testing.T) {
	var documentClosed, instanceClosed, cleaned atomic.Bool
	worker := &fakeInstance{
		openDocument: func(*requests.OpenDocument) (*responses.OpenDocument, error) {
			return &responses.OpenDocument{Document: references.FPDF_DOCUMENT("document")}, nil
		},
		closeDocument: func(*requests.FPDF_CloseDocument) (*responses.FPDF_CloseDocument, error) {
			documentClosed.Store(true)
			return &responses.FPDF_CloseDocument{}, nil
		},
		pageCount: func(*requests.FPDF_GetPageCount) (*responses.FPDF_GetPageCount, error) {
			return &responses.FPDF_GetPageCount{PageCount: 1}, nil
		},
		pageSize: func(*requests.GetPageSizeInPixels) (*responses.GetPageSizeInPixels, error) {
			return &responses.GetPageSizeInPixels{Page: 0, Width: 20, Height: 10}, nil
		},
		countText: func(*requests.FPDFText_CountChars) (*responses.FPDFText_CountChars, error) {
			return &responses.FPDFText_CountChars{Count: 16}, nil
		},
		getText: func(*requests.FPDFText_GetText) (*responses.FPDFText_GetText, error) {
			return &responses.FPDFText_GetText{Text: "  native\r\ntext\x00  "}, nil
		},
		renderPage: func(*requests.RenderPageInDPI) (*responses.RenderPageInDPI, error) {
			raster := image.NewRGBA(image.Rect(0, 0, 20, 10))
			for y := 0; y < 10; y++ {
				for x := 0; x < 20; x++ {
					raster.Set(x, y, color.RGBA{R: uint8(x), G: uint8(y), B: 80, A: 255})
				}
			}
			return &responses.RenderPageInDPI{
				Result:      responses.RenderPage{Page: 0, Width: 20, Height: 10, Image: raster},
				CleanupFunc: func() { cleaned.Store(true) },
			}, nil
		},
		close: func() error {
			instanceClosed.Store(true)
			return nil
		},
	}
	renderer := testRenderer(t, &fakePool{worker: worker})
	request := testRenderRequest()
	result, err := renderer.RenderPage(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if err := result.Validate(request, 10_000, 64*1024); err != nil {
		t.Fatalf("result validation: %v", err)
	}
	if result.Renderer.Provider != "pdfium-wasm" ||
		result.Renderer.Version != "go-pdfium-v1.19.6" ||
		result.NativeText != "native\ntext" || !result.NativeTextExtractionComplete ||
		!documentClosed.Load() || !instanceClosed.Load() || !cleaned.Load() {
		t.Fatalf("result = %+v, documentClosed=%v instanceClosed=%v cleaned=%v",
			result, documentClosed.Load(), instanceClosed.Load(), cleaned.Load())
	}
}

func TestRendererDownscalesOversizedPage(t *testing.T) {
	worker := validFakeInstance()
	worker.pageSize = func(value *requests.GetPageSizeInPixels) (*responses.GetPageSizeInPixels, error) {
		return &responses.GetPageSizeInPixels{Page: 0, Width: value.DPI, Height: value.DPI}, nil
	}
	worker.renderPage = func(value *requests.RenderPageInDPI) (*responses.RenderPageInDPI, error) {
		return &responses.RenderPageInDPI{Result: responses.RenderPage{
			Page: 0, Width: value.DPI, Height: value.DPI,
			Image: image.NewRGBA(image.Rect(0, 0, value.DPI, value.DPI)),
		}}, nil
	}
	renderer := testRendererWithConfig(t, &fakePool{worker: worker}, Config{
		RendererVersion: "go-pdfium-v1.19.6", MaxSourceBytes: 1024,
		MaxRasterPixels: 10_000, MaxRasterBytes: 64 * 1024, MaxConcurrent: 1,
		MaxExtractedRunes: 1000,
		AcquireTimeout:    time.Second, RenderTimeout: time.Second,
	})
	request := testRenderRequest()
	result, err := renderer.RenderPage(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if result.RequestedDPI != request.DPI || result.DPI >= request.DPI ||
		int64(result.Raster.Width)*int64(result.Raster.Height) > 10_000 {
		t.Fatalf("result = %+v", result)
	}
	if err := result.Validate(request, 10_000, 64*1024); err != nil {
		t.Fatal(err)
	}
}

func TestRendererRejectsOversizedPageBeforeRendering(t *testing.T) {
	renderCalled := false
	worker := validFakeInstance()
	worker.pageSize = func(*requests.GetPageSizeInPixels) (*responses.GetPageSizeInPixels, error) {
		return &responses.GetPageSizeInPixels{Page: 0, Width: 101, Height: 100}, nil
	}
	worker.renderPage = func(*requests.RenderPageInDPI) (*responses.RenderPageInDPI, error) {
		renderCalled = true
		return nil, errors.New("must not render")
	}
	renderer := testRendererWithConfig(t, &fakePool{worker: worker}, Config{
		RendererVersion: "go-pdfium-v1.19.6", MaxSourceBytes: 1024,
		MaxRasterPixels: 10_000, MaxRasterBytes: 64 * 1024, MaxConcurrent: 1,
		MaxExtractedRunes: 1000,
		AcquireTimeout:    time.Second, RenderTimeout: time.Second,
	})
	_, err := renderer.RenderPage(context.Background(), testRenderRequest())
	if !errors.Is(err, knowledgelayout.ErrInvalidInput) || renderCalled {
		t.Fatalf("RenderPage error = %v, renderCalled = %v", err, renderCalled)
	}
}

func TestRendererRejectsNativeTextCountBeforeAllocation(t *testing.T) {
	getTextCalled := false
	worker := validFakeInstance()
	worker.countText = func(*requests.FPDFText_CountChars) (*responses.FPDFText_CountChars, error) {
		return &responses.FPDFText_CountChars{Count: 1001}, nil
	}
	worker.getText = func(*requests.FPDFText_GetText) (*responses.FPDFText_GetText, error) {
		getTextCalled = true
		return nil, errors.New("must not allocate text")
	}
	renderer := testRenderer(t, &fakePool{worker: worker})
	_, err := renderer.RenderPage(context.Background(), testRenderRequest())
	if !errors.Is(err, errExtractedTextTooLarge) || getTextCalled {
		t.Fatalf("RenderPage error = %v, getTextCalled = %v", err, getTextCalled)
	}
}

func TestRendererMapsPoolFailureToUnavailable(t *testing.T) {
	poolErr := errors.New("pool offline")
	renderer := testRenderer(t, &fakePool{err: poolErr})
	_, err := renderer.RenderPage(context.Background(), testRenderRequest())
	if !errors.Is(err, knowledgelayout.ErrRendererUnavailable) || !errors.Is(err, poolErr) {
		t.Fatalf("RenderPage error = %v", err)
	}
}

func TestRendererKillsHungWorkerAtDeadline(t *testing.T) {
	release := make(chan struct{})
	var killed atomic.Bool
	worker := validFakeInstance()
	worker.renderPage = func(*requests.RenderPageInDPI) (*responses.RenderPageInDPI, error) {
		<-release
		return nil, errors.New("worker killed")
	}
	worker.kill = func() error {
		if killed.CompareAndSwap(false, true) {
			close(release)
		}
		return nil
	}
	renderer := testRendererWithConfig(t, &fakePool{worker: worker}, Config{
		RendererVersion: "go-pdfium-v1.19.6", MaxSourceBytes: 1024,
		MaxRasterPixels: 10_000, MaxRasterBytes: 64 * 1024, MaxConcurrent: 1,
		MaxExtractedRunes: 1000,
		AcquireTimeout:    time.Second, RenderTimeout: 100 * time.Millisecond,
	})
	_, err := renderer.RenderPage(context.Background(), testRenderRequest())
	if !errors.Is(err, context.DeadlineExceeded) || !killed.Load() {
		t.Fatalf("RenderPage error = %v, killed = %v", err, killed.Load())
	}
}

func TestRendererCloseIsIdempotent(t *testing.T) {
	pool := &fakePool{worker: validFakeInstance()}
	renderer := testRenderer(t, pool)
	if err := renderer.Close(); err != nil {
		t.Fatal(err)
	}
	if err := renderer.Close(); err != nil {
		t.Fatal(err)
	}
	if pool.closeCalls.Load() != 1 {
		t.Fatalf("pool close calls = %d", pool.closeCalls.Load())
	}
	if _, err := renderer.RenderPage(context.Background(), testRenderRequest()); !errors.Is(
		err, knowledgelayout.ErrRendererUnavailable,
	) {
		t.Fatalf("RenderPage after Close error = %v", err)
	}
}

func testRenderer(t *testing.T, pool instancePool) *Renderer {
	t.Helper()
	return testRendererWithConfig(t, pool, Config{
		RendererVersion: "go-pdfium-v1.19.6", MaxSourceBytes: 1024,
		MaxRasterPixels: 10_000, MaxRasterBytes: 64 * 1024, MaxConcurrent: 1,
		MaxExtractedRunes: 1000,
		AcquireTimeout:    time.Second, RenderTimeout: time.Second,
	})
}

func testRendererWithConfig(t *testing.T, pool instancePool, config Config) *Renderer {
	t.Helper()
	renderer, err := newRenderer(pool, config)
	if err != nil {
		t.Fatal(err)
	}
	return renderer
}

func testRenderRequest() knowledgelayout.RenderRequest {
	content := []byte("%PDF-test")
	digest := sha256.Sum256(content)
	return knowledgelayout.RenderRequest{
		Source: knowledgelayout.DocumentSource{
			MediaType: "application/pdf", Content: content, SHA256: hex.EncodeToString(digest[:]),
		},
		PageNumber: 1, DPI: 144,
	}
}

func validFakeInstance() *fakeInstance {
	return &fakeInstance{
		openDocument: func(*requests.OpenDocument) (*responses.OpenDocument, error) {
			return &responses.OpenDocument{Document: references.FPDF_DOCUMENT("document")}, nil
		},
		closeDocument: func(*requests.FPDF_CloseDocument) (*responses.FPDF_CloseDocument, error) {
			return &responses.FPDF_CloseDocument{}, nil
		},
		pageCount: func(*requests.FPDF_GetPageCount) (*responses.FPDF_GetPageCount, error) {
			return &responses.FPDF_GetPageCount{PageCount: 1}, nil
		},
		pageSize: func(*requests.GetPageSizeInPixels) (*responses.GetPageSizeInPixels, error) {
			return &responses.GetPageSizeInPixels{Page: 0, Width: 20, Height: 10}, nil
		},
		renderPage: func(*requests.RenderPageInDPI) (*responses.RenderPageInDPI, error) {
			return &responses.RenderPageInDPI{
				Result: responses.RenderPage{
					Page: 0, Width: 20, Height: 10,
					Image: image.NewRGBA(image.Rect(0, 0, 20, 10)),
				},
			}, nil
		},
	}
}

type fakePool struct {
	worker     instance
	err        error
	closeCalls atomic.Int32
}

func (p *fakePool) GetInstanceWithContext(context.Context) (instance, error) {
	return p.worker, p.err
}

func (p *fakePool) Close() error {
	p.closeCalls.Add(1)
	return nil
}

type fakeInstance struct {
	openDocument  func(*requests.OpenDocument) (*responses.OpenDocument, error)
	closeDocument func(*requests.FPDF_CloseDocument) (*responses.FPDF_CloseDocument, error)
	pageCount     func(*requests.FPDF_GetPageCount) (*responses.FPDF_GetPageCount, error)
	pageSize      func(*requests.GetPageSizeInPixels) (*responses.GetPageSizeInPixels, error)
	renderPage    func(*requests.RenderPageInDPI) (*responses.RenderPageInDPI, error)
	loadTextPage  func(*requests.FPDFText_LoadPage) (*responses.FPDFText_LoadPage, error)
	closeTextPage func(*requests.FPDFText_ClosePage) (*responses.FPDFText_ClosePage, error)
	countText     func(*requests.FPDFText_CountChars) (*responses.FPDFText_CountChars, error)
	getText       func(*requests.FPDFText_GetText) (*responses.FPDFText_GetText, error)
	close         func() error
	kill          func() error
}

func (f *fakeInstance) OpenDocument(value *requests.OpenDocument) (*responses.OpenDocument, error) {
	return f.openDocument(value)
}

func (f *fakeInstance) FPDF_CloseDocument(value *requests.FPDF_CloseDocument) (*responses.FPDF_CloseDocument, error) {
	return f.closeDocument(value)
}

func (f *fakeInstance) FPDF_GetPageCount(value *requests.FPDF_GetPageCount) (*responses.FPDF_GetPageCount, error) {
	return f.pageCount(value)
}

func (f *fakeInstance) GetPageSizeInPixels(value *requests.GetPageSizeInPixels) (*responses.GetPageSizeInPixels, error) {
	return f.pageSize(value)
}

func (f *fakeInstance) RenderPageInDPI(value *requests.RenderPageInDPI) (*responses.RenderPageInDPI, error) {
	return f.renderPage(value)
}

func (f *fakeInstance) FPDFText_LoadPage(value *requests.FPDFText_LoadPage) (*responses.FPDFText_LoadPage, error) {
	if f.loadTextPage == nil {
		return &responses.FPDFText_LoadPage{TextPage: references.FPDF_TEXTPAGE("text-page")}, nil
	}
	return f.loadTextPage(value)
}

func (f *fakeInstance) FPDFText_ClosePage(value *requests.FPDFText_ClosePage) (*responses.FPDFText_ClosePage, error) {
	if f.closeTextPage == nil {
		return &responses.FPDFText_ClosePage{}, nil
	}
	return f.closeTextPage(value)
}

func (f *fakeInstance) FPDFText_CountChars(value *requests.FPDFText_CountChars) (*responses.FPDFText_CountChars, error) {
	if f.countText == nil {
		return &responses.FPDFText_CountChars{}, nil
	}
	return f.countText(value)
}

func (f *fakeInstance) FPDFText_GetText(value *requests.FPDFText_GetText) (*responses.FPDFText_GetText, error) {
	if f.getText == nil {
		return &responses.FPDFText_GetText{}, nil
	}
	return f.getText(value)
}

func (f *fakeInstance) Close() error {
	if f.close != nil {
		return f.close()
	}
	return nil
}

func (f *fakeInstance) Kill() error {
	if f.kill != nil {
		return f.kill()
	}
	return nil
}
