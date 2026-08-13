package conversationmemory

import "github.com/eino-contrib/jsonschema"

const ResponseSchemaName = "conversation_memory_v2"

type responsePayload struct {
	ConversationGoal   *responseEntry           `json:"conversationGoal"`
	Facts              []responseEntry          `json:"facts"`
	Decisions          []responseEntry          `json:"decisions"`
	Corrections        []responseEntry          `json:"corrections"`
	EvidenceReferences []responseReferenceEntry `json:"evidenceReferences"`
	OpenQuestions      []responseEntry          `json:"openQuestions"`
	Todos              []responseEntry          `json:"todos"`
	TaskReferences     []responseReferenceEntry `json:"taskReferences"`
	ReportReferences   []responseReferenceEntry `json:"reportReferences"`
}

type responseEntry struct {
	EntryID           string      `json:"entryId"`
	Content           string      `json:"content"`
	SourceMessageSeqs []int64     `json:"sourceMessageSeqs"`
	Status            EntryStatus `json:"status" jsonschema:"enum=active,enum=open,enum=completed,enum=cancelled"`
}

type responseReferenceEntry struct {
	responseEntry
	ReferenceType ReferenceType `json:"referenceType" jsonschema:"enum=knowledge_chunk,enum=attachment,enum=web,enum=diagnosis_task,enum=diagnosis_report"`
	ReferenceID   string        `json:"referenceId"`
	ContentSHA256 string        `json:"contentSha256,omitempty"`
}

// PayloadJSONSchema returns the provider-facing structural contract. Trusted
// Source identity and current-state invariants remain enforced by
// ValidatePayload after model generation. Schema v1 keeps deprecated lineage
// fields only so stored payloads remain readable.
func PayloadJSONSchema() *jsonschema.Schema {
	reflector := jsonschema.Reflector{Anonymous: true, DoNotReference: true}
	return reflector.Reflect(responsePayload{})
}
