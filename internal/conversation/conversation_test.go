package conversation

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestMessageQueryNormalize(t *testing.T) {
	query := MessageQuery{AfterSeq: -1, Limit: 999}
	query.Normalize()
	if query.AfterSeq != 0 || query.Limit != MaxMessageLimit {
		t.Fatalf("normalized query = %+v", query)
	}
}

func TestServiceAppendUserMessageValidatesStructuredReferences(t *testing.T) {
	repository := &conversationRepositoryStub{}
	service, err := NewService(repository)
	if err != nil {
		t.Fatalf("NewService(): %v", err)
	}
	_, err = service.AppendUserMessage(context.Background(), Actor{UserID: uuid.New()}, AppendMessageInput{
		ConversationID: uuid.New(), Content: "诊断这个工单",
		CaseReferences: []CaseReference{{ExternalCaseID: uuid.New(), Kind: ReferenceKindCreated}},
	})
	if !errors.Is(err, ErrInvalidMessage) {
		t.Fatalf("AppendUserMessage() error = %v, want ErrInvalidMessage", err)
	}
	if repository.appendCalls != 0 {
		t.Fatalf("repository append calls = %d, want 0", repository.appendCalls)
	}
}

func TestServiceAppendUserMessageTrimsContentAndUsesUserRole(t *testing.T) {
	repository := &conversationRepositoryStub{message: Message{ID: uuid.New()}}
	service, _ := NewService(repository)
	message, err := service.AppendUserMessage(context.Background(), Actor{UserID: uuid.New()}, AppendMessageInput{
		ConversationID: uuid.New(), Content: "  请查看错误码  ",
		CaseReferences: []CaseReference{{ExternalCaseID: uuid.New(), Kind: ReferenceKindSelected}},
	})
	if err != nil {
		t.Fatalf("AppendUserMessage(): %v", err)
	}
	if repository.gotInput.Role != MessageRoleUser || repository.gotInput.Content != "请查看错误码" {
		t.Fatalf("repository input = %+v", repository.gotInput)
	}
	if message.ID == uuid.Nil {
		t.Fatal("message id is empty")
	}
}

type conversationRepositoryStub struct {
	message     Message
	gotInput    AppendMessageInput
	appendCalls int
}

func (s *conversationRepositoryStub) Create(context.Context, uuid.UUID, CreateInput, time.Time) (Conversation, error) {
	return Conversation{}, nil
}

func (s *conversationRepositoryStub) Get(context.Context, uuid.UUID, uuid.UUID) (Conversation, error) {
	return Conversation{}, nil
}

func (s *conversationRepositoryStub) List(context.Context, uuid.UUID, ListQuery) (ListResult, error) {
	return ListResult{}, nil
}

func (s *conversationRepositoryStub) ListMessages(context.Context, uuid.UUID, uuid.UUID, MessageQuery) (MessagePage, error) {
	return MessagePage{}, nil
}

func (s *conversationRepositoryStub) AppendMessage(_ context.Context, _ uuid.UUID, input AppendMessageInput, _ time.Time) (Message, error) {
	s.appendCalls++
	s.gotInput = input
	return s.message, nil
}
