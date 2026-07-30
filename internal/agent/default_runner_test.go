package agent

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/chitandabb/GoAgent/internal/externalcase"

	"github.com/google/uuid"
	"go.uber.org/zap"
)

func TestDefaultRunnerDegradesWhenGitHubMCPIsUnavailable(t *testing.T) {
	ctx := context.Background()
	caseID := uuid.MustParse("11111111-1111-1111-1111-111111111111")
	now := time.Now().UTC()
	state := &fakeModelState{arguments: `{"externalCaseId":"11111111-1111-1111-1111-111111111111"}`}
	runner, err := NewDefaultRunner(ctx, DefaultRunnerDependencies{
		ChatModel:        &fakeToolCallingModel{state: state},
		SkillDefinitions: testSkills(),
		ExternalCases: stubExternalCaseGetter{item: &externalcase.ExternalCase{
			ID: caseID, ExternalCaseKey: "TKT-1", Title: "timeout",
			ReportedAt: now, SourceUpdatedAt: now,
		}},
		Logger: zap.NewNop(),
	})
	if err != nil {
		t.Fatalf("NewDefaultRunner: %v", err)
	}
	result, err := runner.Invoke(ctx, RunRequest{
		UserQuery: "请结合代码分析这个工单", ExternalCaseID: caseID.String(),
	})
	if err != nil {
		t.Fatalf("Invoke: %v", err)
	}
	if result.SkillID != SkillTicketDiagnosis || result.RouteReason != "external_case_present" {
		t.Fatalf("unexpected degraded result: %+v", result)
	}
	_, err = runner.Invoke(ctx, RunRequest{UserQuery: "搜索相关代码提交"})
	if !errors.Is(err, ErrSkillUnavailable) {
		t.Fatalf("degraded code Invoke error = %v", err)
	}
}
