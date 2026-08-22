package agent

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/chitandabb/GoAgent/internal/conversation"
	"github.com/cloudwego/eino/compose"
)

func TestConversationToolActivityProjectsSafeKnowledgeAndWebSummaries(t *testing.T) {
	if got := conversationToolInputSummary(ToolSearchKnowledge, `{"query":"设备离线怎么办","maxResults":8}`); got != "检索“设备离线怎么办”" {
		t.Fatalf("knowledge input summary = %q", got)
	}
	knowledgeOutput := `{"results":[{"title":"设备离线处理手册","contentText":"不应进入摘要"},{"title":"值班操作规范"}]}`
	if got := conversationToolOutputSummary(ToolSearchKnowledge, knowledgeOutput, nil); got != "找到 2 个知识片段：设备离线处理手册、值班操作规范" {
		t.Fatalf("knowledge output summary = %q", got)
	}
	webOutput := `{"results":[{"title":"Microsoft Agent Framework","domain":"learn.microsoft.com","description":"不应进入摘要"}]}`
	if got := conversationToolOutputSummary(ToolWebSearch, webOutput, nil); got != "找到 1 个公开网页：Microsoft Agent Framework" {
		t.Fatalf("web output summary = %q", got)
	}
}

func TestConversationToolActivityDoesNotExposeReadonlyRowsOrToolErrors(t *testing.T) {
	inputSummary := conversationToolInputSummary(
		ToolExecuteReadonlyQuery,
		`{"query":"SELECT Status FROM dbo.v_Tickets WHERE TicketID='TKT-1001' AND Priority=3"}`,
	)
	if strings.Contains(inputSummary, "TKT-1001") || strings.Contains(inputSummary, "Priority=3") ||
		!strings.Contains(inputSummary, "TicketID='?'") || !strings.Contains(inputSummary, "Priority=?") {
		t.Fatalf("readonly input was not safely projected: %q", inputSummary)
	}
	output := `{"ok":true,"returnedRows":2,"columns":["TicketID","Status"],"rows":[["TKT-1001","secret-row"],["TKT-1002","closed"]],"truncated":false}`
	summary := conversationToolOutputSummary(ToolExecuteReadonlyQuery, output, nil)
	if summary != "返回 2 行，字段：TicketID、Status" {
		t.Fatalf("readonly output summary = %q", summary)
	}
	for _, forbidden := range []string{"secret-row", "TKT-1001", "closed"} {
		if strings.Contains(summary, forbidden) {
			t.Fatalf("readonly summary leaked %q: %q", forbidden, summary)
		}
	}
	if got := conversationToolOutputSummary(ToolWebSearch, `{}`, errors.New("provider key=secret")); got != "调用失败，未取得可用结果" {
		t.Fatalf("error summary = %q", got)
	}
}

func TestConversationToolActivityBoundsUserVisibleText(t *testing.T) {
	query := strings.Repeat("很长的问题", 200)
	summary := conversationToolInputSummary(ToolWebSearch, `{"query":"`+query+`"}`)
	if len([]rune(summary)) != 700 || !strings.HasSuffix(summary, "…") {
		t.Fatalf("bounded summary runes=%d suffix=%q", len([]rune(summary)), summary[len(summary)-3:])
	}
}

func TestConversationToolTraceMiddlewareEmitsStartedAndCompletedActivity(t *testing.T) {
	sink := &activitySinkCapture{}
	ctx := conversation.WithTurnActivitySink(context.Background(), sink)
	endpoint := newConversationToolTraceMiddleware(1024).Invokable(
		func(context.Context, *compose.ToolInput) (*compose.ToolOutput, error) {
			return &compose.ToolOutput{Result: `{"status":"ok","secret":"must-not-leak"}`}, nil
		},
	)
	if _, err := endpoint(ctx, &compose.ToolInput{Name: "fixture_tool", Arguments: `{"secret":"must-not-leak"}`}); err != nil {
		t.Fatal(err)
	}
	if len(sink.events) != 2 || sink.events[0].eventType != conversation.TurnEventToolStarted ||
		sink.events[1].eventType != conversation.TurnEventToolCompleted {
		t.Fatalf("activity events = %+v", sink.events)
	}
	if sink.events[0].activity.ActivityID != sink.events[1].activity.ActivityID ||
		sink.events[1].activity.Status != conversation.TurnToolActivitySucceeded {
		t.Fatalf("activity lifecycle = %+v", sink.events)
	}
	for _, event := range sink.events {
		if strings.Contains(event.activity.InputSummary, "must-not-leak") ||
			strings.Contains(event.activity.OutputSummary, "must-not-leak") {
			t.Fatalf("activity leaked raw payload: %+v", event.activity)
		}
	}
}

type capturedActivityEvent struct {
	eventType conversation.TurnEventType
	activity  conversation.TurnToolActivity
}

type activitySinkCapture struct {
	events []capturedActivityEvent
}

func (s *activitySinkCapture) RecordTurnToolActivity(
	_ context.Context,
	eventType conversation.TurnEventType,
	activity conversation.TurnToolActivity,
) error {
	s.events = append(s.events, capturedActivityEvent{eventType: eventType, activity: activity})
	return nil
}
