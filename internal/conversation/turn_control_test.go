package conversation

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
)

func TestTurnEventTerminalSemantics(t *testing.T) {
	for _, eventType := range []TurnEventType{
		TurnEventQueued, TurnEventRunning, TurnEventRetryScheduled,
	} {
		if !eventType.Valid() || eventType.IsTerminal() {
			t.Fatalf("event %q validity=%v terminal=%v", eventType, eventType.Valid(), eventType.IsTerminal())
		}
	}
	for _, eventType := range []TurnEventType{TurnEventCompleted, TurnEventFailed} {
		if !eventType.Valid() || !eventType.IsTerminal() {
			t.Fatalf("event %q validity=%v terminal=%v", eventType, eventType.Valid(), eventType.IsTerminal())
		}
	}
	if TurnEventType("unknown").Valid() || TurnEventType("unknown").IsTerminal() {
		t.Fatal("unknown event must be invalid and non-terminal")
	}
}

func TestConversationServiceOpensAuthorizedTurnEventStream(t *testing.T) {
	userID, conversationID, turnID := uuid.New(), uuid.New(), uuid.New()
	repository := &turnControlRepositoryStub{
		conversationRepositoryStub: &conversationRepositoryStub{},
		turn:                       TurnDetail{ID: turnID, ConversationID: conversationID, Status: TurnStatusRunning},
		page: TurnEventPage{Items: []TurnEvent{{
			TurnID: turnID, ConversationID: conversationID, Seq: 2, EventType: TurnEventRunning,
		}}},
	}
	service, err := NewService(repository)
	if err != nil {
		t.Fatalf("NewService(): %v", err)
	}
	stream, err := service.OpenTurnEventStream(context.Background(), Actor{UserID: userID}, conversationID, turnID)
	if err != nil {
		t.Fatalf("OpenTurnEventStream(): %v", err)
	}
	if stream.InitialStatus() != TurnStatusRunning {
		t.Fatalf("initial status = %q", stream.InitialStatus())
	}
	page, err := stream.Next(context.Background(), 1, MaxTurnEventLimit)
	if err != nil {
		t.Fatalf("stream.Next(): %v", err)
	}
	if len(page.Items) != 1 || repository.gotUserID != userID || repository.gotAfterSeq != 1 || repository.gotLimit != MaxTurnEventLimit {
		t.Fatalf("page=%+v repository=%+v", page, repository)
	}
	if _, err := stream.Next(context.Background(), -1, 1); !errors.Is(err, ErrInvalidMessage) {
		t.Fatalf("negative cursor error = %v, want ErrInvalidMessage", err)
	}
}

type turnControlRepositoryStub struct {
	*conversationRepositoryStub
	turn        TurnDetail
	page        TurnEventPage
	gotUserID   uuid.UUID
	gotAfterSeq int64
	gotLimit    int
}

func (s *turnControlRepositoryStub) GetTurn(_ context.Context, userID, conversationID, turnID uuid.UUID) (TurnDetail, error) {
	s.gotUserID = userID
	if s.turn.ID != turnID || s.turn.ConversationID != conversationID {
		return TurnDetail{}, ErrInvalidMessage
	}
	return s.turn, nil
}

func (s *turnControlRepositoryStub) ListTurnEvents(_ context.Context, userID, conversationID, turnID uuid.UUID, afterSeq int64, limit int) (TurnEventPage, error) {
	s.gotUserID, s.gotAfterSeq, s.gotLimit = userID, afterSeq, limit
	if s.turn.ID != turnID || s.turn.ConversationID != conversationID {
		return TurnEventPage{}, ErrInvalidMessage
	}
	return s.page, nil
}
