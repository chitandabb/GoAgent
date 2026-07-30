package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestReadObservationsRejectsInvalidJSONL(t *testing.T) {
	_, err := readObservations(strings.NewReader("{invalid}\n"))
	if err == nil {
		t.Fatal("readObservations accepted invalid JSONL")
	}
}

func TestRunRequiresInput(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := run(nil, &stdout, &stderr); code != 2 {
		t.Fatalf("run code = %d", code)
	}
}
