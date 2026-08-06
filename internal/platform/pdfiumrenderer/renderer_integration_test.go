package pdfiumrenderer

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/chitandabb/GoAgent/internal/knowledgelayout"
)

func TestWASMRendererRendersMinimalPDF(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	renderer, err := OpenWASM(ctx, Config{
		RendererVersion:   "go-pdfium-v1.19.6",
		MaxSourceBytes:    1024 * 1024,
		MaxRasterPixels:   2_000_000,
		MaxRasterBytes:    4 * 1024 * 1024,
		MaxExtractedRunes: 10_000,
		MaxConcurrent:     1,
		AcquireTimeout:    5 * time.Second,
		RenderTimeout:     10 * time.Second,
	})
	if err != nil {
		t.Fatalf("OpenWASM: %v", err)
	}
	defer renderer.Close()
	content := minimalPDF("MESGuard PDFium smoke")
	digest := sha256.Sum256(content)
	request := knowledgelayout.RenderRequest{
		Source: knowledgelayout.DocumentSource{
			MediaType: "application/pdf", Content: content,
			SHA256: hex.EncodeToString(digest[:]),
		},
		PageNumber: 1, DPI: 72,
	}
	result, err := renderer.RenderPage(ctx, request)
	if err != nil {
		t.Fatalf("RenderPage: %v", err)
	}
	if err := result.Validate(request, 2_000_000, 4*1024*1024); err != nil {
		t.Fatalf("Validate result: %v", err)
	}
	if result.Raster.Width != 612 || result.Raster.Height != 792 ||
		result.Renderer.Provider != "pdfium-wasm" {
		t.Fatalf("result = %+v", result)
	}
}

func minimalPDF(text string) []byte {
	var output bytes.Buffer
	offsets := []int{0}
	writeObject := func(id int, body string) {
		for len(offsets) <= id {
			offsets = append(offsets, 0)
		}
		offsets[id] = output.Len()
		fmt.Fprintf(&output, "%d 0 obj\n%s\nendobj\n", id, body)
	}
	output.WriteString("%PDF-1.4\n")
	writeObject(1, "<< /Type /Catalog /Pages 2 0 R >>")
	writeObject(2, "<< /Type /Pages /Kids [3 0 R] /Count 1 >>")
	writeObject(3, "<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Resources << /Font << /F1 4 0 R >> >> /Contents 5 0 R >>")
	writeObject(4, "<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>")
	text = strings.NewReplacer("\\", "\\\\", "(", "\\(", ")", "\\)").Replace(text)
	stream := fmt.Sprintf("BT /F1 12 Tf 72 720 Td (%s) Tj ET", text)
	writeObject(5, fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", len(stream), stream))
	xrefOffset := output.Len()
	fmt.Fprintf(&output, "xref\n0 %d\n", len(offsets))
	output.WriteString("0000000000 65535 f \n")
	for id := 1; id < len(offsets); id++ {
		fmt.Fprintf(&output, "%010d 00000 n \n", offsets[id])
	}
	fmt.Fprintf(&output, "trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", len(offsets), xrefOffset)
	return output.Bytes()
}
