package main

import (
	"bytes"
	"testing"
	"time"
)

func TestLatencyFixtureIsFiveQuestionFixedReplay(t *testing.T) {
	fixture, err := readFixture("../../../testdata/semantic-cache-latency-v1.json")
	if err != nil {
		t.Fatal(err)
	}
	if len(fixture.Pairs) != 5 || fixture.Repetitions != 4 {
		t.Fatalf("pairs=%d repetitions=%d", len(fixture.Pairs), fixture.Repetitions)
	}
	if calls := 1 + fixture.Repetitions*len(fixture.Pairs); calls != 21 {
		t.Fatalf("provider calls=%d", calls)
	}
	questions := make(map[string]struct{}, len(fixture.Pairs))
	for _, pair := range fixture.Pairs {
		questions[pair.CandidateQuestion] = struct{}{}
	}
	if len(questions) != len(fixture.Pairs) {
		t.Fatal("fixed replay contains duplicated candidate questions")
	}
}

func TestRunRejectsProviderBudgetBeforeLoadingRuntime(t *testing.T) {
	var stdout, stderr bytes.Buffer
	exitCode := run([]string{
		"-fixture", "../../../testdata/semantic-cache-latency-v1.json",
		"-output", t.TempDir() + "/report.json",
		"-max-provider-calls", "20",
	}, &stdout, &stderr)
	if exitCode != 1 || stdout.Len() != 0 || !bytes.Contains(stderr.Bytes(), []byte("requires 21 Provider calls")) {
		t.Fatalf("exit=%d stdout=%q stderr=%q", exitCode, stdout.String(), stderr.String())
	}
}

func TestDurationPercentilesUseNearestRank(t *testing.T) {
	values := make([]time.Duration, 100)
	for index := range values {
		values[index] = time.Duration(index+1) * time.Millisecond
	}
	p50, p95 := durationPercentiles(values)
	if p50 != 50*time.Millisecond || p95 != 95*time.Millisecond {
		t.Fatalf("p50=%s p95=%s", p50, p95)
	}
}
