package rabbitmq

import (
	"testing"
	"time"
)

func TestDiagnosisRetryQueueSelection(t *testing.T) {
	base := "mesguard.diagnosis.execute"
	for _, test := range []struct {
		delay time.Duration
		want  string
	}{
		{30 * time.Second, base + ".retry.30s"},
		{2 * time.Minute, base + ".retry.2m"},
		{10 * time.Minute, base + ".retry.10m"},
	} {
		got, ok := diagnosisRetryQueue(base, test.delay)
		if !ok || got != test.want {
			t.Fatalf("diagnosisRetryQueue(%s) = %q, %v; want %q, true", test.delay, got, ok, test.want)
		}
	}
	if _, ok := diagnosisRetryQueue(base, time.Minute); ok {
		t.Fatal("diagnosisRetryQueue() accepted unsupported delay")
	}
}
