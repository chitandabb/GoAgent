package main

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"

	"github.com/chitandabb/GoAgent/internal/knowledgelayout"
)

func TestParseFlagsAcceptsResourceOverrides(t *testing.T) {
	options, err := parseFlags([]string{
		"-render-dpi", "96", "-max-raster-pixels", "8000000",
		"-intra-op-threads", "4", "-inter-op-threads", "1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if options.renderDPI != 96 || options.maxPixels != 8_000_000 ||
		options.intraOpThreads != 4 || options.interOpThreads != 1 {
		t.Fatalf("options = %+v", options)
	}
	if _, err := parseFlags([]string{"-render-dpi", "71"}); err == nil {
		t.Fatal("expected render DPI validation error")
	}
}

func TestVerifyCorpusFilesChecksSizeAndHash(t *testing.T) {
	root := t.TempDir()
	content := []byte("public corpus fixture")
	fileName := "fixture.pdf"
	if err := os.WriteFile(filepath.Join(root, fileName), content, 0o600); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(content)
	document := knowledgelayout.RoutingCorpusDocument{
		DocumentID: "fixture", FileName: fileName, SizeBytes: int64(len(content)),
		SHA256: hex.EncodeToString(digest[:]),
	}
	corpus := knowledgelayout.RoutingCorpus{Documents: []knowledgelayout.RoutingCorpusDocument{document}}
	documents, err := verifyCorpusFiles(root, corpus)
	if err != nil || documents[document.DocumentID].SHA256 != document.SHA256 {
		t.Fatalf("verify = %+v, %v", documents, err)
	}
	corpus.Documents[0].SizeBytes++
	if _, err := verifyCorpusFiles(root, corpus); err == nil {
		t.Fatal("expected corpus size mismatch")
	}
}
