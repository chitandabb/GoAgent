package agent

import (
	"context"
	"sync"

	"github.com/chitandabb/GoAgent/internal/conversation"
)

type conversationReportReferenceTrace struct {
	mu      sync.Mutex
	ordered []conversation.ReportReference
	seen    map[string]struct{}
}

type conversationReportReferenceTraceKey struct{}

func withConversationReportReferenceTrace(ctx context.Context, trace *conversationReportReferenceTrace) context.Context {
	return context.WithValue(ctx, conversationReportReferenceTraceKey{}, trace)
}

func conversationReportReferenceTraceFromContext(ctx context.Context) *conversationReportReferenceTrace {
	trace, _ := ctx.Value(conversationReportReferenceTraceKey{}).(*conversationReportReferenceTrace)
	return trace
}

func (t *conversationReportReferenceTrace) append(referenceID string) {
	if t == nil || referenceID == "" {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.seen == nil {
		t.seen = make(map[string]struct{})
	}
	if _, exists := t.seen[referenceID]; exists {
		return
	}
	t.seen[referenceID] = struct{}{}
	t.ordered = append(t.ordered, conversation.ReportReference{ReferenceID: referenceID})
}

func (t *conversationReportReferenceTrace) snapshot() []conversation.ReportReference {
	if t == nil {
		return nil
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	return append([]conversation.ReportReference(nil), t.ordered...)
}
