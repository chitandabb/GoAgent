package conversation

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
)

func TestTurnEventTerminalSemantics(t *testing.T) {
	for _, eventType := range []TurnEventType{
		TurnEventQueued, TurnEventRunning, TurnEventRetryScheduled, TurnEventMessageDelta,
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

func TestChunkTurnContentByRunes(t *testing.T) {
	tests := []struct {
		name    string
		content string
		size    int
		want    []string
	}{
		{name: "empty", content: "", size: 40, want: []string{}},
		{name: "single chunk", content: "你好", size: 40, want: []string{"你好"}},
		{name: "exact boundary", content: "abcdef", size: 3, want: []string{"abc", "def"}},
		{name: "rune boundary not byte boundary", content: "一二三四五", size: 2, want: []string{"一二", "三四", "五"}},
		{name: "longer than one chunk", content: "hello world hello world", size: 8, want: []string{"hello wo", "rld hell", "o world"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := ChunkTurnContent(test.content, test.size)
			if len(got) != len(test.want) {
				t.Fatalf("ChunkTurnContent(%q, %d) = %v, want %v", test.content, test.size, got, test.want)
			}
			for index := range got {
				if got[index] != test.want[index] {
					t.Fatalf("ChunkTurnContent(%q, %d)[%d] = %q, want %q", test.content, test.size, index, got[index], test.want[index])
				}
			}
			if joined := joinTurnChunks(got); joined != test.content {
				t.Fatalf("joined chunks = %q, want %q", joined, test.content)
			}
		})
	}
	if got := ChunkTurnContent("abc", 0); got != nil {
		t.Fatalf("ChunkTurnContent with size 0 = %v, want nil", got)
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
