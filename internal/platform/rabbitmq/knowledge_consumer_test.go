package rabbitmq

import (
	"testing"
	"time"
)

func TestKnowledgeRetryQueueSelection(t *testing.T) {
	base := "mesguard.knowledge.ingest"
	for _, test := range []struct {
		delay time.Duration
		want  string
	}{
		{30 * time.Second, base + ".retry.30s"},
		{2 * time.Minute, base + ".retry.2m"},
		{10 * time.Minute, base + ".retry.10m"},
	} {
		got, ok := knowledgeRetryQueue(base, test.delay)
		if !ok || got != test.want {
			t.Fatalf("knowledgeRetryQueue(%s) = %q, %v; want %q, true", test.delay, got, ok, test.want)
		}
	}
	if _, ok := knowledgeRetryQueue(base, time.Minute); ok {
		t.Fatal("knowledgeRetryQueue accepted unsupported delay")
	}
}
