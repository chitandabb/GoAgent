package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/chitandabb/GoAgent/internal/agentruntime"
	"github.com/chitandabb/GoAgent/internal/conversation"
	"github.com/chitandabb/GoAgent/internal/diagnosis"

	"github.com/google/uuid"
)

func TestCreateDiagnosisTaskToolUsesNarrowArguments(t *testing.T) {
	caseID, taskID := uuid.New(), uuid.New()
	creator := &diagnosisToolCreatorStub{result: conversation.CreateDiagnosisResult{
		Task: diagnosis.DiagnosisTask{ID: taskID, Status: diagnosis.TaskPending},
	}}
	current, err := NewCreateDiagnosisTaskTool(creator)
	if err != nil {
		t.Fatalf("NewCreateDiagnosisTaskTool(): %v", err)
	}
	info, err := current.Info(context.Background())
	if err != nil {
		t.Fatalf("Info(): %v", err)
	}
	encoded, err := info.ParamsOneOf.ToJSONSchema()
	if err != nil || encoded == nil || encoded.Properties == nil {
		t.Fatalf("tool schema is empty: schema=%+v err=%v", info, err)
	}
	schemaBytes, err := json.Marshal(encoded)
	if err != nil {
		t.Fatalf("marshal tool schema: %v", err)
	}
	schemaText := string(schemaBytes)
	for _, forbidden := range []string{"expectedSourceFingerprint", "requestScope", "idempotencyKey", "budget", "toolNames"} {
		if strings.Contains(schemaText, forbidden) {
			t.Fatalf("tool schema exposes forbidden field %q: %s", forbidden, schemaText)
		}
	}
	ctx := agentruntime.WithRunAccess(context.Background(), mustConversationAccess(t,
		[]agentruntime.Permission{agentruntime.PermissionDiagnosisCreate},
		agentruntime.ResourceGrantsConfig{ExternalCaseIDs: []uuid.UUID{caseID}}))
	result, err := current.InvokableRun(ctx, `{"externalCaseId":"`+caseID.String()+`","diagnosisGoal":"诊断这个工单"}`)
	if err != nil {
		t.Fatalf("InvokableRun(): %v", err)
	}
	if !strings.Contains(result, taskID.String()) || creator.input.ExternalCaseID != caseID || creator.input.DiagnosisGoal != "诊断这个工单" {
		t.Fatalf("result=%s input=%+v", result, creator.input)
	}
}

func TestCreateDiagnosisTaskToolRejectsUntrimmedGoal(t *testing.T) {
	creator := &diagnosisToolCreatorStub{}
	current, _ := NewCreateDiagnosisTaskTool(creator)
	if _, err := current.InvokableRun(context.Background(), `{"externalCaseId":"`+uuid.NewString()+`","diagnosisGoal":" 诊断 "}`); err == nil {
		t.Fatal("InvokableRun accepted an untrimmed diagnosis goal")
	}
	if creator.calls != 0 {
		t.Fatalf("creator calls = %d, want 0", creator.calls)
	}
}

type diagnosisToolCreatorStub struct {
	result conversation.CreateDiagnosisResult
	input  conversation.CreateDiagnosisInput
	calls  int
}

func (s *diagnosisToolCreatorStub) CreateDiagnosisTask(_ context.Context, input conversation.CreateDiagnosisInput) (conversation.CreateDiagnosisResult, error) {
	s.calls++
	s.input = input
	return s.result, nil
}
