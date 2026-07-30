package agent

import (
	"encoding/json"
	"testing"
)

func TestBuildUserPrompt(t *testing.T) {
	tests := []struct {
		name        string
		request     RunRequest
		want        string
		wantPayload *userPromptPayload
		wantErr     bool
	}{
		{name: "empty query", request: RunRequest{UserQuery: "  "}, wantErr: true},
		{name: "query without case", request: RunRequest{UserQuery: "  分析故障  "}, want: "分析故障"},
		{
			name:        "structured case query",
			request:     RunRequest{UserQuery: "  分析故障  ", ExternalCaseID: "  case-1  "},
			wantPayload: &userPromptPayload{ExternalCaseID: "case-1", Question: "分析故障"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := BuildUserPrompt(tt.request)
			if (err != nil) != tt.wantErr {
				t.Fatalf("BuildUserPrompt error = %v", err)
			}
			if tt.wantErr {
				return
			}
			if tt.wantPayload == nil {
				if got != tt.want {
					t.Fatalf("BuildUserPrompt = %q, want %q", got, tt.want)
				}
				return
			}
			var payload userPromptPayload
			if err := json.Unmarshal([]byte(got), &payload); err != nil {
				t.Fatalf("decode prompt: %v", err)
			}
			if payload != *tt.wantPayload {
				t.Fatalf("payload = %+v, want %+v", payload, *tt.wantPayload)
			}
		})
	}
}
