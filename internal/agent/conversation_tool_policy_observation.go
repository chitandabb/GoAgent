package agent

import "context"

// ConversationToolPolicyErrorRunLimit identifies a recoverable per-Tool run limit.
const ConversationToolPolicyErrorRunLimit = "tool_run_limit_exhausted"

// ConversationToolPolicyObservation is a bounded projection of a Tool request
// rejected by a recoverable Conversation run policy. It deliberately excludes
// Tool arguments, SQL, results, user content, and raw errors.
type ConversationToolPolicyObservation struct {
	ToolName  string
	ErrorType string
}

// ConversationToolPolicyObserver receives bounded recoverable policy events.
type ConversationToolPolicyObserver interface {
	ObserveConversationToolPolicy(ConversationToolPolicyObservation)
}

type conversationToolPolicyObserverContextKey struct{}

// WithConversationToolPolicyObserver installs a run-scoped bounded observer.
func WithConversationToolPolicyObserver(
	ctx context.Context,
	observer ConversationToolPolicyObserver,
) context.Context {
	if observer == nil {
		return ctx
	}
	return context.WithValue(ctx, conversationToolPolicyObserverContextKey{}, observer)
}

func observeConversationToolPolicy(ctx context.Context, observation ConversationToolPolicyObservation) {
	observer, _ := ctx.Value(conversationToolPolicyObserverContextKey{}).(ConversationToolPolicyObserver)
	if observer != nil {
		observer.ObserveConversationToolPolicy(observation)
	}
}
