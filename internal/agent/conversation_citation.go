package agent

import (
	"context"
	"encoding/json"
	"sort"
	"sync"

	"github.com/chitandabb/GoAgent/internal/conversation"
	"github.com/chitandabb/GoAgent/internal/webresearch"
)

const conversationCitationSourcesField = "citationSources"

type conversationCitationSourcePayload struct {
	SourceType    conversation.CitationSourceType `json:"sourceType"`
	SourceRef     string                          `json:"sourceRef"`
	ContentSHA256 string                          `json:"contentSha256"`
	Marker        string                          `json:"marker"`
}

type conversationCitationTrace struct {
	mu                  sync.Mutex
	sources             []conversation.MessageCitation
	byRef               map[string]conversation.MessageCitation
	repairEvidence      []string
	repairEvidenceBytes int
	repairEvidenceLost  bool
	sourceToolAttempted bool
	degradedChannels    map[string]struct{}
	sourcesTruncated    bool
}

func (t *conversationCitationTrace) appendRepairEvidence(snapshot string) {
	if t == nil || snapshot == "" {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.repairEvidenceLost {
		return
	}
	if t.repairEvidenceBytes+len(snapshot) > maxConversationCitationRepairEvidenceBytes {
		t.repairEvidence = nil
		t.repairEvidenceBytes = 0
		t.repairEvidenceLost = true
		return
	}
	t.repairEvidence = append(t.repairEvidence, snapshot)
	t.repairEvidenceBytes += len(snapshot)
}

func (t *conversationCitationTrace) repairSnapshot() ([]string, bool) {
	if t == nil {
		return nil, false
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.repairEvidenceLost || len(t.repairEvidence) == 0 {
		return nil, false
	}
	return append([]string(nil), t.repairEvidence...), true
}

type conversationCitationTraceKey struct{}

func withConversationCitationTrace(ctx context.Context, trace *conversationCitationTrace) context.Context {
	return context.WithValue(ctx, conversationCitationTraceKey{}, trace)
}

func conversationCitationTraceFromContext(ctx context.Context) *conversationCitationTrace {
	trace, _ := ctx.Value(conversationCitationTraceKey{}).(*conversationCitationTrace)
	return trace
}

func (t *conversationCitationTrace) append(sources []conversation.MessageCitation) bool {
	if t == nil || len(sources) == 0 {
		return len(sources) == 0
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.byRef == nil {
		t.byRef = make(map[string]conversation.MessageCitation, len(sources))
	}
	for _, source := range sources {
		if existing, exists := t.byRef[source.SourceRef]; exists &&
			(existing.SourceType != source.SourceType || existing.ContentSHA256 != source.ContentSHA256) {
			return false
		}
	}
	newSourceCount := 0
	for _, source := range sources {
		if _, exists := t.byRef[source.SourceRef]; !exists {
			newSourceCount++
		}
	}
	if len(t.sources)+newSourceCount > conversation.MaxCitationSourcesPerRun {
		if t.degradedChannels == nil {
			t.degradedChannels = make(map[string]struct{})
		}
		t.degradedChannels["retrieved_sources_truncated"] = struct{}{}
		t.sourcesTruncated = true
		return false
	}
	for _, source := range sources {
		if _, exists := t.byRef[source.SourceRef]; exists {
			continue
		}
		source.Position = 0
		t.byRef[source.SourceRef] = source
		t.sources = append(t.sources, source)
	}
	return true
}

func (t *conversationCitationTrace) snapshot() []conversation.MessageCitation {
	if t == nil {
		return nil
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	return append([]conversation.MessageCitation(nil), t.sources...)
}

func (t *conversationCitationTrace) observeTool(toolName, snapshot string, runErr error) {
	if t == nil || !conversationQualitySourceTool(toolName) {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.sourceToolAttempted = true
	if t.degradedChannels == nil {
		t.degradedChannels = make(map[string]struct{})
	}
	if runErr != nil {
		if channel := failedConversationSourceChannel(toolName); channel != "" {
			t.degradedChannels[channel] = struct{}{}
		}
		return
	}
	switch toolName {
	case ToolSearchKnowledge:
		var response searchKnowledgeResponse
		if json.Unmarshal([]byte(snapshot), &response) != nil {
			t.degradedChannels["knowledge_result_invalid"] = struct{}{}
			return
		}
		if response.Degraded {
			if len(response.MissingChannels) == 0 {
				t.degradedChannels["knowledge_degraded"] = struct{}{}
			}
			for _, missing := range response.MissingChannels {
				switch missing {
				case "fts":
					t.degradedChannels["knowledge_fts_missing"] = struct{}{}
				case "vector":
					t.degradedChannels["knowledge_vector_missing"] = struct{}{}
				case "rerank":
					t.degradedChannels["knowledge_rerank_missing"] = struct{}{}
				default:
					t.degradedChannels["knowledge_channel_missing"] = struct{}{}
				}
			}
		}
	case ToolReadAttachment:
		var response readAttachmentResponse
		if json.Unmarshal([]byte(snapshot), &response) != nil {
			t.degradedChannels["attachment_result_invalid"] = struct{}{}
		} else if response.VisualAssetCount > 0 && len(response.Elements) == 0 {
			t.degradedChannels["attachment_visual_only"] = struct{}{}
		} else if response.VisualAssetCount > 0 {
			t.degradedChannels["attachment_visual_unprocessed"] = struct{}{}
		}
	case ToolFetchPublicPage:
		var response webresearch.PageSnapshot
		if json.Unmarshal([]byte(snapshot), &response) != nil {
			t.degradedChannels["web_page_result_invalid"] = struct{}{}
		} else if response.Truncated {
			t.degradedChannels["web_page_truncated"] = struct{}{}
		}
	}
}

func (t *conversationCitationTrace) observationSnapshot() (
	[]conversation.AgentRunSource,
	[]string,
	bool,
	bool,
) {
	if t == nil {
		return nil, nil, false, false
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	sources := make([]conversation.AgentRunSource, 0, len(t.sources))
	for _, source := range t.sources {
		sources = append(sources, conversation.AgentRunSource{
			SourceType: source.SourceType, SourceRef: source.SourceRef, ContentSHA256: source.ContentSHA256,
		})
	}
	channels := make([]string, 0, len(t.degradedChannels))
	for channel := range t.degradedChannels {
		channels = append(channels, channel)
	}
	sort.Strings(channels)
	return sources, channels, t.sourceToolAttempted, t.sourcesTruncated
}

func (t *conversationCitationTrace) markDegraded(channel string) {
	if t == nil || channel == "" {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.degradedChannels == nil {
		t.degradedChannels = make(map[string]struct{})
	}
	t.degradedChannels[channel] = struct{}{}
}

func conversationQualitySourceTool(toolName string) bool {
	return toolName == ToolSearchKnowledge || toolName == ToolReadAttachment ||
		toolName == ToolWebSearch || toolName == ToolFetchPublicPage
}

func conversationCitationProducingTool(toolName string) bool {
	return toolName == ToolSearchKnowledge || toolName == ToolReadAttachment || toolName == ToolFetchPublicPage
}

func failedConversationSourceChannel(toolName string) string {
	switch toolName {
	case ToolSearchKnowledge:
		return "knowledge_search_failed"
	case ToolReadAttachment:
		return "attachment_read_failed"
	case ToolWebSearch:
		return "web_search_failed"
	case ToolFetchPublicPage:
		return "web_page_fetch_failed"
	default:
		return ""
	}
}

func conversationCitationSourcesFromTool(toolName, snapshot string) ([]conversation.MessageCitation, bool) {
	var sources []conversation.MessageCitation
	switch toolName {
	case ToolSearchKnowledge:
		if _, ok := knowledgeSearchEvidenceLocation(snapshot); !ok {
			return nil, false
		}
		var response searchKnowledgeResponse
		if json.Unmarshal([]byte(snapshot), &response) != nil {
			return nil, false
		}
		for _, result := range response.Results {
			sources = append(sources, conversation.MessageCitation{
				SourceType:    conversation.CitationSourceKnowledgeChunk,
				SourceRef:     "knowledge:" + result.DocumentVersionID + "/" + result.ChunkID,
				ContentSHA256: result.ContentSHA256,
			})
		}
		for _, group := range response.ContextGroups {
			for _, chunk := range group.Chunks {
				sources = append(sources, conversation.MessageCitation{
					SourceType:    conversation.CitationSourceKnowledgeChunk,
					SourceRef:     "knowledge:" + group.DocumentVersionID + "/" + chunk.ChunkID,
					ContentSHA256: chunk.ContentSHA256,
				})
			}
		}
	case ToolReadAttachment:
		location, ok := attachmentEvidenceLocation(snapshot)
		if !ok {
			return nil, false
		}
		var response readAttachmentResponse
		if json.Unmarshal([]byte(snapshot), &response) != nil {
			return nil, false
		}
		sources = append(sources, conversation.MessageCitation{
			SourceType: conversation.CitationSourceAttachment,
			SourceRef:  location, ContentSHA256: response.ContentSHA256,
		})
	case ToolFetchPublicPage:
		location, ok := webPageEvidenceLocation(snapshot)
		if !ok {
			return nil, false
		}
		var response webresearch.PageSnapshot
		if json.Unmarshal([]byte(snapshot), &response) != nil {
			return nil, false
		}
		sources = append(sources, conversation.MessageCitation{
			SourceType: conversation.CitationSourceWeb,
			SourceRef:  location, ContentSHA256: response.ContentSHA256,
		})
	default:
		return nil, false
	}
	return normalizeConversationCitationSources(sources)
}

func normalizeConversationCitationSources(
	values []conversation.MessageCitation,
) ([]conversation.MessageCitation, bool) {
	if len(values) == 0 || len(values) > conversation.MaxCitationSourcesPerRun {
		return nil, false
	}
	result := make([]conversation.MessageCitation, 0, len(values))
	byRef := make(map[string]conversation.MessageCitation, len(values))
	for _, value := range values {
		value.Position = 0
		if value.Validate() != nil {
			return nil, false
		}
		if existing, exists := byRef[value.SourceRef]; exists {
			if existing.SourceType != value.SourceType || existing.ContentSHA256 != value.ContentSHA256 {
				return nil, false
			}
			continue
		}
		byRef[value.SourceRef] = value
		result = append(result, value)
	}
	return result, true
}

func augmentConversationToolResultWithCitationSources(
	snapshot string,
	sources []conversation.MessageCitation,
	maxBytes int,
) (string, bool) {
	if len(sources) == 0 || maxBytes < 1 {
		return "", false
	}
	var object map[string]json.RawMessage
	if json.Unmarshal([]byte(snapshot), &object) != nil || object == nil {
		return "", false
	}
	if _, exists := object[conversationCitationSourcesField]; exists {
		return "", false
	}
	payloadSources := make([]conversationCitationSourcePayload, 0, len(sources))
	for _, source := range sources {
		marker, err := conversation.FormatAnswerCitationMarker(source)
		if err != nil {
			return "", false
		}
		payloadSources = append(payloadSources, conversationCitationSourcePayload{
			SourceType: source.SourceType, SourceRef: source.SourceRef,
			ContentSHA256: source.ContentSHA256, Marker: marker,
		})
	}
	encodedSources, err := json.Marshal(payloadSources)
	if err != nil {
		return "", false
	}
	object[conversationCitationSourcesField] = encodedSources
	encoded, err := json.Marshal(object)
	if err != nil || len(encoded) > maxBytes {
		return "", false
	}
	return string(encoded), true
}
