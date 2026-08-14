package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestReadCasesRejectsUnknownFields(t *testing.T) {
	_, err := readCases(strings.NewReader(`{"datasetVersion":"v1","caseId":"case-1","owner":"owner","repo":"repo","query":"Foo","pathFilter":"src/","expectedPath":"src/Foo.cs","contentMarker":"class Foo","unknown":true}` + "\n"))
	if err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("readCases error = %v", err)
	}
}

func TestRunRequiresCases(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := run(nil, &stdout, &stderr); code != 2 {
		t.Fatalf("run code = %d", code)
	}
}

func TestReadCasesRejectsExpectedPathOutsideFilter(t *testing.T) {
	_, err := readCases(strings.NewReader(`{"datasetVersion":"v1","caseId":"case-1","owner":"owner","repo":"repo","query":"Foo","pathFilter":"tests/","expectedPath":"src/Foo.cs","contentMarker":"class Foo"}` + "\n"))
	if err == nil || !strings.Contains(err.Error(), "expectedPath must be inside pathFilter") {
		t.Fatalf("readCases error = %v", err)
	}
}
