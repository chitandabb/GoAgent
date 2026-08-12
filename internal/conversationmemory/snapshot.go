// Package conversationmemory owns MESGuard's structured, conversation-scoped
// memory contract. Snapshots are derived facts: they retain stable source
// references but never replace the original conversation messages.
package conversationmemory

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"slices"
	"strings"

	"github.com/chitandabb/GoAgent/internal/conversation"
)

const (
	CurrentSchemaVersion = 1
	MaxEntries           = 256
	MaxEntryContentRunes = 4096
)

var (
	ErrInvalidPayloadSchema     = errors.New("conversation memory payload schema is invalid")
	ErrInvalidEntry             = errors.New("conversation memory entry is invalid")
	ErrInvalidEntryStatus       = errors.New("conversation memory entry status is invalid")
	ErrSourceOutOfRange         = errors.New("conversation memory source message is outside the covered range")
	ErrUserSourceRequired       = errors.New("conversation memory entry requires a user message source")
	ErrUnknownEntryReference    = errors.New("conversation memory entry reference is unknown")
	ErrUnknownStableReference   = errors.New("conversation memory stable reference is unknown")
	ErrSupersedeCycle           = errors.New("conversation memory supersede relation contains a cycle")
	ErrMultipleActiveEntries    = errors.New("conversation memory lineage contains multiple active entries")
	ErrCorrectionTargetRequired = errors.New("conversation memory correction requires an explicit target")
	ErrPayloadTooLarge          = errors.New("conversation memory payload exceeds its size limit")
)

// EntryValidationError reports a stable contract location without exposing
// model-produced IDs, content, or source values.
type EntryValidationError struct {
	Code string
}

func (e *EntryValidationError) Error() string {
	if e == nil || e.Code == "" {
		return ErrInvalidEntry.Error()
	}
	return ErrInvalidEntry.Error() + ": " + e.Code
}

func (e *EntryValidationError) Unwrap() error { return ErrInvalidEntry }

func newEntryValidationError(code string) error {
	return &EntryValidationError{Code: code}
}

// EntryValidationFailureCode returns a bounded, content-free failure code.
func EntryValidationFailureCode(err error) string {
	var validationErr *EntryValidationError
	if errors.As(err, &validationErr) && validationErr != nil {
		return validationErr.Code
	}
	return ""
}

const (
	PayloadSchemaFailureEmpty          = "empty"
	PayloadSchemaFailureTopLevelJSON   = "top_level_json"
	PayloadSchemaFailureTopLevelFields = "top_level_fields"
	PayloadSchemaFailureNullArray      = "null_array"
)

// PayloadSchemaError exposes only the fixed contract location that failed.
// It deliberately omits model output and decoded values from logs and metrics.
type PayloadSchemaError struct {
	Code string
}

func (e *PayloadSchemaError) Error() string {
	if e == nil || e.Code == "" {
		return ErrInvalidPayloadSchema.Error()
	}
	return ErrInvalidPayloadSchema.Error() + ": " + e.Code
}

func (e *PayloadSchemaError) Unwrap() error { return ErrInvalidPayloadSchema }

func newPayloadSchemaError(code string) error {
	return &PayloadSchemaError{Code: code}
}

// PayloadSchemaFailureCode returns a bounded, content-free failure code.
func PayloadSchemaFailureCode(err error) string {
	var schemaErr *PayloadSchemaError
	if errors.As(err, &schemaErr) && schemaErr != nil {
		return schemaErr.Code
	}
	return ""
}

// FailureCode returns the bounded, content-free domain code shared by the
// production repair path and offline observability tools.
func FailureCode(err error) string {
	switch {
	case errors.Is(err, ErrUserSourceRequired):
		return "user_source_required"
	case errors.Is(err, ErrSourceOutOfRange):
		return "source_out_of_range"
	case errors.Is(err, ErrUnknownEntryReference):
		return "entry_reference_unknown"
	case errors.Is(err, ErrUnknownStableReference):
		return "stable_reference_unknown"
	case errors.Is(err, ErrSupersedeCycle):
		return "supersede_cycle"
	case errors.Is(err, ErrMultipleActiveEntries):
		return "multiple_active_entries"
	case errors.Is(err, ErrCorrectionTargetRequired):
		return "correction_target_required"
	case errors.Is(err, ErrPayloadTooLarge):
		return "payload_too_large"
	case errors.Is(err, ErrInvalidPayloadSchema):
		if code := PayloadSchemaFailureCode(err); code != "" {
			return "payload_schema_" + code
		}
		return "payload_schema_invalid"
	case errors.Is(err, ErrInvalidEntry):
		if code := EntryValidationFailureCode(err); code != "" {
			return "entry_" + code
		}
		return "entry_invalid"
	case errors.Is(err, ErrInvalidEntryStatus):
		return "entry_status"
	}
	return ""
}

type EntryStatus string

const (
	EntryStatusActive     EntryStatus = "active"
	EntryStatusSuperseded EntryStatus = "superseded"
	EntryStatusOpen       EntryStatus = "open"
	EntryStatusCompleted  EntryStatus = "completed"
	EntryStatusCancelled  EntryStatus = "cancelled"
)

func (s EntryStatus) isMemoryStatus() bool {
	return s == EntryStatusActive || s == EntryStatusSuperseded
}

func (s EntryStatus) isTodoStatus() bool {
	return s == EntryStatusOpen || s == EntryStatusCompleted || s == EntryStatusCancelled
}

// Entry is the common, source-backed unit stored in a structured Snapshot.
// EntryID remains stable across incremental merges. A replacement receives a
// new ID and points to the entry it supersedes.
type Entry struct {
	EntryID           string      `json:"entryId"`
	Content           string      `json:"content"`
	SourceMessageSeqs []int64     `json:"sourceMessageSeqs"`
	Status            EntryStatus `json:"status" jsonschema:"enum=active,enum=superseded,enum=open,enum=completed,enum=cancelled"`
	SupersedesEntryID string      `json:"supersedesEntryId,omitempty"`
}

// ReferenceEntry stores only a stable reference, integrity hash where one is
// available, and a bounded conclusion. It never stores credentials, object
// storage coordinates, or a copied evidence body.
type ReferenceEntry struct {
	Entry
	ReferenceType ReferenceType `json:"referenceType" jsonschema:"enum=knowledge_chunk,enum=attachment,enum=web,enum=diagnosis_task,enum=diagnosis_report"`
	ReferenceID   string        `json:"referenceId"`
	ContentSHA256 string        `json:"contentSha256,omitempty"`
}

type ReferenceType string

const (
	ReferenceTypeKnowledgeChunk  ReferenceType = "knowledge_chunk"
	ReferenceTypeAttachment      ReferenceType = "attachment"
	ReferenceTypeWeb             ReferenceType = "web"
	ReferenceTypeDiagnosisTask   ReferenceType = "diagnosis_task"
	ReferenceTypeDiagnosisReport ReferenceType = "diagnosis_report"
)

// Payload is intentionally a fixed schema. Empty sections are encoded as [] so
// omissions and nulls from a model response can be rejected deterministically.
type Payload struct {
	ConversationGoal   *Entry           `json:"conversationGoal"`
	Facts              []Entry          `json:"facts"`
	Decisions          []Entry          `json:"decisions"`
	Corrections        []Entry          `json:"corrections"`
	EvidenceReferences []ReferenceEntry `json:"evidenceReferences"`
	OpenQuestions      []Entry          `json:"openQuestions"`
	Todos              []Entry          `json:"todos"`
	TaskReferences     []ReferenceEntry `json:"taskReferences"`
	ReportReferences   []ReferenceEntry `json:"reportReferences"`
}

// ValidationContext is the trusted catalog used to verify that model-produced
// memory is grounded in the covered conversation history.
type ValidationContext struct {
	FromSeq                 int64
	ThroughSeq              int64
	MaxPayloadBytes         int
	MessageRoles            map[int64]conversation.MessageRole
	KnownEvidenceReferences map[string]EvidenceReferenceIdentity
	KnownTaskReferences     map[string]StableReferenceIdentity
	KnownReportReferences   map[string]StableReferenceIdentity
	PreviousPayload         *Payload
}

type EvidenceReferenceIdentity struct {
	ReferenceType     ReferenceType
	ContentSHA256     string
	SourceMessageSeqs []int64
}

type StableReferenceIdentity struct {
	SourceMessageSeqs []int64
}

type entrySection string

const (
	sectionGoal       entrySection = "goal"
	sectionFact       entrySection = "fact"
	sectionDecision   entrySection = "decision"
	sectionCorrection entrySection = "correction"
	sectionEvidence   entrySection = "evidence"
	sectionQuestion   entrySection = "question"
	sectionTodo       entrySection = "todo"
	sectionTask       entrySection = "task"
	sectionReport     entrySection = "report"
)

type indexedEntry struct {
	entry   *Entry
	section entrySection
}

var entryIDPattern = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,63}$`)

// DecodePayload performs strict top-level and nested JSON decoding. It rejects
// unknown, missing, duplicated and null array fields before semantic validation.
func DecodePayload(encoded []byte) (Payload, error) {
	if len(bytes.TrimSpace(encoded)) == 0 {
		return Payload{}, newPayloadSchemaError(PayloadSchemaFailureEmpty)
	}
	var raw map[string]json.RawMessage
	if err := decodeStrictJSON(encoded, &raw); err != nil {
		return Payload{}, newPayloadSchemaError(PayloadSchemaFailureTopLevelJSON)
	}
	want := []string{
		"conversationGoal", "facts", "decisions", "corrections", "evidenceReferences",
		"openQuestions", "todos", "taskReferences", "reportReferences",
	}
	for _, field := range want {
		if _, ok := raw[field]; !ok {
			return Payload{}, newPayloadSchemaError("top_level_missing_" + payloadSchemaFieldCode(field))
		}
	}
	if len(raw) != len(want) {
		return Payload{}, newPayloadSchemaError("top_level_extra_fields")
	}
	var payload Payload
	fields := []struct {
		name   string
		target any
	}{
		{"conversation_goal", &payload.ConversationGoal},
		{"facts", &payload.Facts},
		{"decisions", &payload.Decisions},
		{"corrections", &payload.Corrections},
		{"evidence_references", &payload.EvidenceReferences},
		{"open_questions", &payload.OpenQuestions},
		{"todos", &payload.Todos},
		{"task_references", &payload.TaskReferences},
		{"report_references", &payload.ReportReferences},
	}
	jsonNames := []string{
		"conversationGoal", "facts", "decisions", "corrections", "evidenceReferences",
		"openQuestions", "todos", "taskReferences", "reportReferences",
	}
	for index, field := range fields {
		if err := decodeRawField(raw, jsonNames[index], field.target); err != nil {
			return Payload{}, newPayloadSchemaError(payloadFieldFailureCode(field.name, raw[jsonNames[index]]))
		}
	}
	if payload.Facts == nil || payload.Decisions == nil || payload.Corrections == nil ||
		payload.EvidenceReferences == nil || payload.OpenQuestions == nil || payload.Todos == nil ||
		payload.TaskReferences == nil || payload.ReportReferences == nil {
		return Payload{}, newPayloadSchemaError(PayloadSchemaFailureNullArray)
	}
	return payload, nil
}

func payloadSchemaFieldCode(field string) string {
	switch field {
	case "conversationGoal":
		return "conversation_goal"
	case "evidenceReferences":
		return "evidence_references"
	case "openQuestions":
		return "open_questions"
	case "taskReferences":
		return "task_references"
	case "reportReferences":
		return "report_references"
	default:
		return field
	}
}

func payloadFieldFailureCode(field string, encoded json.RawMessage) string {
	trimmed := bytes.TrimSpace(encoded)
	kind := "invalid"
	if len(trimmed) > 0 {
		switch trimmed[0] {
		case '{':
			kind = "object"
		case '[':
			kind = "array"
		case '"':
			kind = "string"
		case 'n':
			kind = "null"
		case 't', 'f':
			kind = "boolean"
		default:
			kind = "number"
		}
	}
	return "field_" + field + "_" + kind
}

func decodeRawField(raw map[string]json.RawMessage, name string, target any) error {
	return decodeStrictJSON(raw[name], target)
}

func decodeStrictJSON(encoded []byte, target any) error {
	if err := rejectDuplicateJSONFields(encoded); err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("trailing JSON value")
		}
		return err
	}
	return nil
}

func rejectDuplicateJSONFields(encoded []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	var walk func() error
	walk = func() error {
		token, err := decoder.Token()
		if err != nil {
			return err
		}
		delimiter, composite := token.(json.Delim)
		if !composite {
			return nil
		}
		switch delimiter {
		case '{':
			seen := make(map[string]struct{})
			for decoder.More() {
				nameToken, err := decoder.Token()
				if err != nil {
					return err
				}
				name, ok := nameToken.(string)
				if !ok {
					return errors.New("JSON object key is not a string")
				}
				if _, duplicate := seen[name]; duplicate {
					return fmt.Errorf("duplicate JSON field %q", name)
				}
				seen[name] = struct{}{}
				if err := walk(); err != nil {
					return err
				}
			}
			_, err = decoder.Token()
			return err
		case '[':
			for decoder.More() {
				if err := walk(); err != nil {
					return err
				}
			}
			_, err = decoder.Token()
			return err
		default:
			return errors.New("unknown JSON delimiter")
		}
	}
	if err := walk(); err != nil {
		return err
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("trailing JSON value")
		}
		return err
	}
	return nil
}

// ValidatePayload verifies the model-produced payload against trusted source
// identities and structural invariants. It does not attempt semantic truth
// judgement; offline evaluation owns that concern.
func ValidatePayload(payload Payload, context ValidationContext) error {
	if context.FromSeq < 1 || context.ThroughSeq < context.FromSeq || context.MaxPayloadBytes < 1 {
		return ErrInvalidPayloadSchema
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("%w: encode payload", ErrInvalidPayloadSchema)
	}
	if len(encoded) > context.MaxPayloadBytes {
		return ErrPayloadTooLarge
	}
	if payload.Facts == nil || payload.Decisions == nil || payload.Corrections == nil ||
		payload.EvidenceReferences == nil || payload.OpenQuestions == nil || payload.Todos == nil ||
		payload.TaskReferences == nil || payload.ReportReferences == nil {
		return ErrInvalidPayloadSchema
	}

	entries := collectEntries(&payload)
	if len(entries) > MaxEntries {
		return ErrInvalidEntry
	}
	trustedPrevious, err := validateIncrementalMerge(payload, context.PreviousPayload)
	if err != nil {
		return err
	}
	byID := make(map[string]indexedEntry, len(entries))
	for _, current := range entries {
		_, trusted := trustedPrevious[current.entry.EntryID]
		if err := validateEntry(current.entry, current.section, context, trusted); err != nil {
			return err
		}
		if _, exists := byID[current.entry.EntryID]; exists {
			return ErrInvalidEntry
		}
		byID[current.entry.EntryID] = current
	}
	if err := validateStableReferences(payload, context); err != nil {
		return err
	}
	if err := validateSupersedeGraph(byID); err != nil {
		return err
	}
	return nil
}

func collectEntries(payload *Payload) []indexedEntry {
	result := make([]indexedEntry, 0, 1+len(payload.Facts)+len(payload.Decisions)+len(payload.Corrections)+
		len(payload.EvidenceReferences)+len(payload.OpenQuestions)+len(payload.Todos)+
		len(payload.TaskReferences)+len(payload.ReportReferences))
	if payload.ConversationGoal != nil {
		result = append(result, indexedEntry{entry: payload.ConversationGoal, section: sectionGoal})
	}
	appendEntries := func(entries []Entry, section entrySection) {
		for index := range entries {
			result = append(result, indexedEntry{entry: &entries[index], section: section})
		}
	}
	appendReferences := func(entries []ReferenceEntry, section entrySection) {
		for index := range entries {
			result = append(result, indexedEntry{entry: &entries[index].Entry, section: section})
		}
	}
	appendEntries(payload.Facts, sectionFact)
	appendEntries(payload.Decisions, sectionDecision)
	appendEntries(payload.Corrections, sectionCorrection)
	appendReferences(payload.EvidenceReferences, sectionEvidence)
	appendEntries(payload.OpenQuestions, sectionQuestion)
	appendEntries(payload.Todos, sectionTodo)
	appendReferences(payload.TaskReferences, sectionTask)
	appendReferences(payload.ReportReferences, sectionReport)
	return result
}

func validateEntry(entry *Entry, section entrySection, context ValidationContext, trustedPrevious bool) error {
	if entry == nil || !entryIDPattern.MatchString(entry.EntryID) {
		return newEntryValidationError("entry_id")
	}
	if strings.TrimSpace(entry.Content) == "" || entry.Content != strings.TrimSpace(entry.Content) ||
		len([]rune(entry.Content)) > MaxEntryContentRunes {
		return newEntryValidationError("content")
	}
	if len(entry.SourceMessageSeqs) == 0 || len(entry.SourceMessageSeqs) > 32 {
		return newEntryValidationError("source_count")
	}
	if section == sectionTodo {
		if !entry.Status.isTodoStatus() {
			return ErrInvalidEntryStatus
		}
	} else if !entry.Status.isMemoryStatus() {
		return ErrInvalidEntryStatus
	}
	if section == sectionCorrection && entry.SupersedesEntryID == "" {
		return ErrCorrectionTargetRequired
	}
	if entry.SupersedesEntryID != "" && !entryIDPattern.MatchString(entry.SupersedesEntryID) {
		return ErrUnknownEntryReference
	}
	if !slices.IsSorted(entry.SourceMessageSeqs) {
		return newEntryValidationError("source_order")
	}
	hasUserSource := false
	var previous int64
	for index, seq := range entry.SourceMessageSeqs {
		if seq < context.FromSeq || seq > context.ThroughSeq {
			return ErrSourceOutOfRange
		}
		role, exists := context.MessageRoles[seq]
		if !exists && !trustedPrevious {
			return ErrSourceOutOfRange
		}
		if index > 0 && seq == previous {
			return newEntryValidationError("source_duplicate")
		}
		previous = seq
		hasUserSource = hasUserSource || role == conversation.MessageRoleUser || trustedPrevious
	}
	if (section == sectionGoal || section == sectionFact || section == sectionCorrection) && !hasUserSource {
		return ErrUserSourceRequired
	}
	return nil
}

type payloadEntryRecord struct {
	section       entrySection
	entry         Entry
	referenceType ReferenceType
	referenceID   string
	contentSHA256 string
}

func validateIncrementalMerge(payload Payload, previous *Payload) (map[string]struct{}, error) {
	trusted := make(map[string]struct{})
	if previous == nil {
		return trusted, nil
	}
	currentRecords := payloadEntryRecords(payload)
	previousRecords := payloadEntryRecords(*previous)
	for id, before := range previousRecords {
		after, exists := currentRecords[id]
		if !exists || before.section != after.section || before.entry.EntryID != after.entry.EntryID ||
			before.entry.Content != after.entry.Content || !slices.Equal(before.entry.SourceMessageSeqs, after.entry.SourceMessageSeqs) ||
			before.entry.SupersedesEntryID != after.entry.SupersedesEntryID || before.referenceType != after.referenceType ||
			before.referenceID != after.referenceID || before.contentSHA256 != after.contentSHA256 {
			return nil, ErrInvalidEntry
		}
		if before.entry.Status != after.entry.Status {
			if before.entry.Status != EntryStatusActive || after.entry.Status != EntryStatusSuperseded {
				return nil, ErrInvalidEntryStatus
			}
			replaced := false
			for replacementID, replacement := range currentRecords {
				if _, existedBefore := previousRecords[replacementID]; !existedBefore &&
					replacement.entry.SupersedesEntryID == id && replacement.entry.Status == EntryStatusActive {
					replaced = true
					break
				}
			}
			if !replaced {
				return nil, ErrInvalidEntry
			}
		}
		trusted[id] = struct{}{}
	}
	return trusted, nil
}

func payloadEntryRecords(payload Payload) map[string]payloadEntryRecord {
	result := make(map[string]payloadEntryRecord)
	addEntry := func(entry Entry, section entrySection) {
		result[entry.EntryID] = payloadEntryRecord{section: section, entry: entry}
	}
	addReference := func(entry ReferenceEntry, section entrySection) {
		result[entry.EntryID] = payloadEntryRecord{
			section: section, entry: entry.Entry, referenceType: entry.ReferenceType,
			referenceID: entry.ReferenceID, contentSHA256: entry.ContentSHA256,
		}
	}
	if payload.ConversationGoal != nil {
		addEntry(*payload.ConversationGoal, sectionGoal)
	}
	for _, entry := range payload.Facts {
		addEntry(entry, sectionFact)
	}
	for _, entry := range payload.Decisions {
		addEntry(entry, sectionDecision)
	}
	for _, entry := range payload.Corrections {
		addEntry(entry, sectionCorrection)
	}
	for _, entry := range payload.EvidenceReferences {
		addReference(entry, sectionEvidence)
	}
	for _, entry := range payload.OpenQuestions {
		addEntry(entry, sectionQuestion)
	}
	for _, entry := range payload.Todos {
		addEntry(entry, sectionTodo)
	}
	for _, entry := range payload.TaskReferences {
		addReference(entry, sectionTask)
	}
	for _, entry := range payload.ReportReferences {
		addReference(entry, sectionReport)
	}
	return result
}

func validateStableReferences(payload Payload, context ValidationContext) error {
	for _, current := range payload.EvidenceReferences {
		identity, exists := context.KnownEvidenceReferences[current.ReferenceID]
		if !exists || current.ReferenceType != identity.ReferenceType || current.ContentSHA256 != identity.ContentSHA256 ||
			!validSHA256(current.ContentSHA256) || !sourcesAllowed(current.SourceMessageSeqs, identity.SourceMessageSeqs) {
			return ErrUnknownStableReference
		}
	}
	for _, current := range payload.TaskReferences {
		identity, exists := context.KnownTaskReferences[current.ReferenceID]
		if current.ReferenceType != ReferenceTypeDiagnosisTask || !exists || current.ContentSHA256 != "" ||
			!sourcesAllowed(current.SourceMessageSeqs, identity.SourceMessageSeqs) {
			return ErrUnknownStableReference
		}
	}
	for _, current := range payload.ReportReferences {
		identity, exists := context.KnownReportReferences[current.ReferenceID]
		if current.ReferenceType != ReferenceTypeDiagnosisReport || !exists || current.ContentSHA256 != "" ||
			!sourcesAllowed(current.SourceMessageSeqs, identity.SourceMessageSeqs) {
			return ErrUnknownStableReference
		}
	}
	return nil
}

func sourcesAllowed(entrySources, trustedSources []int64) bool {
	trusted := make(map[int64]struct{}, len(trustedSources))
	for _, seq := range trustedSources {
		trusted[seq] = struct{}{}
	}
	for _, seq := range entrySources {
		if _, exists := trusted[seq]; !exists {
			return false
		}
	}
	return len(entrySources) > 0
}

func validSHA256(value string) bool {
	if len(value) != sha256.Size*2 || value != strings.ToLower(value) {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func validateSupersedeGraph(entries map[string]indexedEntry) error {
	for id, current := range entries {
		if current.entry.SupersedesEntryID == "" {
			continue
		}
		target, exists := entries[current.entry.SupersedesEntryID]
		if !exists || id == current.entry.SupersedesEntryID {
			if id == current.entry.SupersedesEntryID {
				return ErrSupersedeCycle
			}
			return ErrUnknownEntryReference
		}
		if current.section == sectionCorrection &&
			(target.section == sectionTodo || target.section == sectionEvidence || target.section == sectionTask || target.section == sectionReport) {
			return ErrUnknownEntryReference
		}
		if (current.section == sectionTodo) != (target.section == sectionTodo) {
			return ErrUnknownEntryReference
		}
	}

	state := make(map[string]uint8, len(entries))
	var visit func(string) error
	visit = func(id string) error {
		switch state[id] {
		case 1:
			return ErrSupersedeCycle
		case 2:
			return nil
		}
		state[id] = 1
		if next := entries[id].entry.SupersedesEntryID; next != "" {
			if err := visit(next); err != nil {
				return err
			}
		}
		state[id] = 2
		return nil
	}
	for id := range entries {
		if err := visit(id); err != nil {
			return err
		}
	}

	neighbors := make(map[string][]string, len(entries))
	replacedByCount := make(map[string]int, len(entries))
	for id, current := range entries {
		if next := current.entry.SupersedesEntryID; next != "" {
			replacedByCount[next]++
			if replacedByCount[next] > 1 {
				return ErrMultipleActiveEntries
			}
			neighbors[id] = append(neighbors[id], next)
			neighbors[next] = append(neighbors[next], id)
		}
	}
	seen := make(map[string]struct{}, len(entries))
	for id := range entries {
		if _, ok := seen[id]; ok {
			continue
		}
		currentEntries := 0
		componentIsTodo := entries[id].section == sectionTodo
		queue := []string{id}
		seen[id] = struct{}{}
		for len(queue) > 0 {
			currentID := queue[0]
			queue = queue[1:]
			current := entries[currentID]
			if (current.section == sectionTodo) != componentIsTodo {
				return ErrUnknownEntryReference
			}
			if replacedByCount[currentID] == 0 {
				currentEntries++
				if !componentIsTodo && current.entry.Status != EntryStatusActive {
					return ErrInvalidEntryStatus
				}
			} else if !componentIsTodo && current.entry.Status != EntryStatusSuperseded {
				return ErrMultipleActiveEntries
			}
			for _, next := range neighbors[currentID] {
				if _, ok := seen[next]; !ok {
					seen[next] = struct{}{}
					queue = append(queue, next)
				}
			}
		}
		if currentEntries != 1 {
			return ErrMultipleActiveEntries
		}
	}
	return nil
}
