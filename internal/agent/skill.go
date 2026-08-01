// Package agent 定义 MESGuard 的技能化 Agent 编排模型。
//
// Skill 不是可执行代码插件，而是供单个 Eino ADK Agent 按需读取的调查指南。
// Tool 授权由 TaskScope 和 ToolCatalog 决定，不能由 Skill Prompt 授予。
package agent

import (
	"regexp"
)

type SkillID string

const (
	SkillTicketDiagnosis   SkillID = "ticket-diagnosis"
	SkillCodeInvestigation SkillID = "code-investigation"
	SkillSQLInvestigation  SkillID = "sql-investigation"
	SkillKnowledgeQA       SkillID = "knowledge-qa"
)

var (
	skillIDPattern  = regexp.MustCompile(`^[a-z][a-z0-9-]{1,63}$`)
	toolNamePattern = regexp.MustCompile(`^[a-z][a-z0-9_]{1,127}$`)
)

const (
	ToolReadExternalCase = "read_external_case"
)

var GitHubReadOnlyTools = []string{
	"search_code",
	"get_file_contents",
	"list_commits",
	"get_commit",
}
