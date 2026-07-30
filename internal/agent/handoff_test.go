package agent

import (
	"context"
	"testing"

	"github.com/cloudwego/eino/components/tool"
)

func TestRequestCodeInvestigationToolRecordsStructuredHandoff(t *testing.T) {
	current, err := NewRequestCodeInvestigationTool()
	if err != nil {
		t.Fatalf("NewRequestCodeInvestigationTool: %v", err)
	}
	invokable, ok := current.(tool.InvokableTool)
	if !ok {
		t.Fatal("handoff tool is not invokable")
	}
	trace := &handoffTrace{}
	ctx := withHandoffTrace(context.Background(), trace)
	_, err = invokable.InvokableRun(ctx, `{
		"reason":"需要核对异常对应的实现", "query":"搜索 InventoryService", "clues":["InventoryService"]
	}`)
	if err != nil {
		t.Fatalf("InvokableRun: %v", err)
	}
	request := trace.snapshot()
	if request == nil || request.TargetSkill != SkillCodeInvestigation || request.Query != "搜索 InventoryService" {
		t.Fatalf("handoff request = %+v", request)
	}
}
