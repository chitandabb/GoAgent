package main

import "testing"

func TestParseFlagsRequiresExplicitInputs(t *testing.T) {
	if _, err := parseFlags(nil); err == nil {
		t.Fatal("parseFlags accepted an empty corpus")
	}
	options, err := parseFlags([]string{"-input", "one.pptx", "-input", "two.pptx"})
	if err != nil || len(options.inputs) != 2 {
		t.Fatalf("parseFlags = %+v, %v", options, err)
	}
}

func TestFinishSummaryComputesThroughput(t *testing.T) {
	result := summary{
		SucceededFiles: 2, TotalBytes: 2 * 1024 * 1024, TotalSlides: 30,
		TotalDurationMillis: 2000,
		Files:               []fileObservation{{DurationMillis: 500}, {DurationMillis: 1500}},
	}
	finishSummary(&result)
	if result.ThroughputMiBPerSecond != 1 || result.SlidesPerSecond != 15 ||
		result.P50FileMillis != 500 || result.P95FileMillis != 1500 {
		t.Fatalf("summary = %+v", result)
	}
}
