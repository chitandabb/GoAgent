package conversationmemoryworker

import (
	"context"
	"errors"

	"github.com/chitandabb/GoAgent/internal/conversationmemory"
)

type ServiceExecutor struct {
	service        *conversationmemory.Service
	activationGate conversationmemory.ActivationGate
}

func NewServiceExecutor(
	service *conversationmemory.Service,
	activationGate conversationmemory.ActivationGate,
) (*ServiceExecutor, error) {
	if service == nil || activationGate == nil {
		return nil, errors.New("conversation memory executor dependencies are nil")
	}
	return &ServiceExecutor{service: service, activationGate: activationGate}, nil
}

func (e *ServiceExecutor) Execute(ctx context.Context, task Task) (ExecutionResult, error) {
	if e == nil || e.service == nil || e.activationGate == nil || task.Validate() != nil {
		return ExecutionResult{}, errors.New("conversation memory executor input is invalid")
	}
	current, err := e.service.PrepareActiveOnce(ctx, conversationmemory.PrepareActiveRequest{
		ConversationID: task.ConversationID, CompletedMessages: task.CompletedMessages,
		ActivationGate: e.activationGate,
	}, task.AttemptCount)
	if err != nil {
		return ExecutionResult{}, err
	}
	return ExecutionResult{CurrentSnapshotID: current.ID, ThroughSeq: current.ThroughSeq}, nil
}

var _ Executor = (*ServiceExecutor)(nil)
