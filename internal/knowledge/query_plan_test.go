package knowledge

import (
	"strings"
	"testing"
)

func TestBuildQueryPlanPreservesProtectedSignalsAndOriginalQuery(t *testing.T) {
	original := "SQL Server 2022 error 258 最近不能连接 ERP-504"
	plan, err := BuildQueryPlan(original, QueryRewriteResult{
		LexicalQuery:  "SQL Server 2022 ERP-504 error 258 最近不能连接",
		SemanticQuery: "SQL Server 2022 ERP-504 error 258 最近不能建立连接",
		Subqueries:    []string{"ERP-504 error 258", "SQL Server 2022 最近不能连接"},
		PromptVersion: "query-rewrite-v1",
		Usage:         QueryRewriteUsage{PromptTokens: 10, CompletionTokens: 5, TotalTokens: 15},
	}, 2)
	if err != nil {
		t.Fatalf("BuildQueryPlan: %v", err)
	}
	if plan.OriginalQuery != original || !plan.RewriteAttempted || !plan.RewriteApplied || len(plan.Subqueries) != 2 {
		t.Fatalf("plan = %+v", plan)
	}
	if len(plan.FTSQueries()) != 4 || len(plan.VectorQueries()) != 4 {
		t.Fatalf("fts=%v vector=%v", plan.FTSQueries(), plan.VectorQueries())
	}
}

func TestBuildQueryPlanRejectsDroppedOrInventedProtectedSignals(t *testing.T) {
	tests := []struct {
		name     string
		original string
		rewrite  QueryRewriteResult
	}{
		{
			name: "drops error code", original: "ERP-504 timeout",
			rewrite: QueryRewriteResult{LexicalQuery: "ERP timeout", SemanticQuery: "ERP timeout", PromptVersion: "v1"},
		},
		{
			name: "invents version", original: "SQL Server timeout",
			rewrite: QueryRewriteResult{LexicalQuery: "SQL Server 2022 timeout", SemanticQuery: "SQL Server timeout", PromptVersion: "v1"},
		},
		{
			name: "drops negation", original: "当前不能提交工单",
			rewrite: QueryRewriteResult{LexicalQuery: "当前提交工单", SemanticQuery: "当前提交工单", PromptVersion: "v1"},
		},
		{
			name: "subquery invents code", original: "SQL Server timeout",
			rewrite: QueryRewriteResult{LexicalQuery: "SQL Server timeout", SemanticQuery: "SQL Server timeout", Subqueries: []string{"error 258"}, PromptVersion: "v1"},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := BuildQueryPlan(test.original, test.rewrite, 2); err == nil {
				t.Fatal("BuildQueryPlan accepted an unsafe rewrite")
			}
		})
	}
}

func TestQueryPlanDeduplicatesVariantsAndBoundsOutput(t *testing.T) {
	original := "connection pool timeout"
	plan, err := BuildQueryPlan(original, QueryRewriteResult{
		LexicalQuery: original, SemanticQuery: "database connection pool exhaustion",
		Subqueries:    []string{"database connection pool exhaustion", "connection pool timeout causes"},
		PromptVersion: "v1",
	}, 2)
	if err != nil {
		t.Fatal(err)
	}
	if got := plan.VectorQueries(); len(got) != 3 || got[0] != original {
		t.Fatalf("VectorQueries = %v", got)
	}
	invalid := QueryRewriteResult{
		LexicalQuery: strings.Repeat("x", MaxQueryRewriteRunes+1), SemanticQuery: original, PromptVersion: "v1",
	}
	if _, err := BuildQueryPlan(original, invalid, 2); err == nil {
		t.Fatal("BuildQueryPlan accepted an oversized rewrite")
	}
	longPlan, err := OriginalQueryPlan(strings.Repeat("x", MaxQueryRewriteRunes+1))
	if err != nil {
		t.Fatal(err)
	}
	if got := longPlan.FTSQueries(); len(got) != 0 {
		t.Fatalf("FTSQueries = %v, want no oversized candidates", got)
	}
}

func TestBuildQueryPlanRejectsInconsistentTokenUsage(t *testing.T) {
	_, err := BuildQueryPlan("connection timeout", QueryRewriteResult{
		LexicalQuery: "connection timeout", SemanticQuery: "connection timeout", PromptVersion: "v1",
		Usage: QueryRewriteUsage{PromptTokens: 10, CompletionTokens: 5, TotalTokens: 10},
	}, 2)
	if err == nil {
		t.Fatal("BuildQueryPlan accepted total tokens below prompt plus completion tokens")
	}
}

func TestSortedProtectedSignals(t *testing.T) {
	signals := protectedSignals("最近不能处理 ERP-504 error 258")
	for _, expected := range []string{"258", "erp-504", "不能", "最近"} {
		if _, exists := signals[expected]; !exists {
			t.Fatalf("signals = %v", signals)
		}
	}
	if signals := protectedSignals("未来版本"); len(signals) != 0 {
		t.Fatalf("future version signals = %v, want no single-character negation", signals)
	}
	if signals := protectedSignals("任务未完成"); len(signals) != 1 {
		t.Fatalf("unfinished task signals = %v", signals)
	}
	for _, expected := range []string{"未返回", "截至", "今天"} {
		if _, exists := protectedSignals("截至今天接口未返回数据")[expected]; !exists {
			t.Fatalf("missing protected signal %q", expected)
		}
	}
}
