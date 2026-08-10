package agent

import (
	"encoding/json"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/chitandabb/GoAgent/internal/conversation"
	"github.com/chitandabb/GoAgent/internal/knowledge"
	"github.com/chitandabb/GoAgent/internal/webresearch"
	"github.com/google/uuid"
)

func TestConversationCitationSourcesFromAttachmentAndWebTools(t *testing.T) {
	attachmentID := uuid.New()
	attachmentHash := knowledge.SHA256Hex("attachment snapshot")
	attachmentSnapshot, err := json.Marshal(readAttachmentResponse{
		SourceType: "attachment", SourceRef: "attachment:" + attachmentID.String(),
		AttachmentID: attachmentID.String(), OriginalName: "runbook.pdf", MediaType: "application/pdf",
		SizeBytes: 128, ContentSHA256: attachmentHash, ParserVersion: "test-v1",
	})
	if err != nil {
		t.Fatal(err)
	}
	attachmentSources, ok := conversationCitationSourcesFromTool(ToolReadAttachment, string(attachmentSnapshot))
	if !ok || len(attachmentSources) != 1 ||
		attachmentSources[0].SourceType != conversation.CitationSourceAttachment ||
		attachmentSources[0].SourceRef != "attachment:"+attachmentID.String() ||
		attachmentSources[0].ContentSHA256 != attachmentHash {
		t.Fatalf("attachment sources = %+v, ok=%v", attachmentSources, ok)
	}
	visualOnly := readAttachmentResponse{
		SourceType: "attachment", SourceRef: "attachment:" + attachmentID.String(),
		AttachmentID: attachmentID.String(), OriginalName: "scan.png", MediaType: "image/png",
		SizeBytes: 128, ContentSHA256: attachmentHash, ParserVersion: "test-v1", VisualAssetCount: 1,
	}
	visualSnapshot, err := json.Marshal(visualOnly)
	if err != nil {
		t.Fatal(err)
	}
	trace := &conversationCitationTrace{}
	trace.observeTool(ToolReadAttachment, string(visualSnapshot), nil)
	_, degradedChannels, attempted, _ := trace.observationSnapshot()
	if !attempted || len(degradedChannels) != 1 || degradedChannels[0] != "attachment_visual_only" {
		t.Fatalf("attachment observation attempted=%v degraded=%v", attempted, degradedChannels)
	}

	pageContent := "MESGuard public documentation"
	page := webresearch.PageSnapshot{
		ResultID: "web_123", URL: "https://docs.example.com/mesguard", Domain: "docs.example.com",
		Title: "MESGuard", FetchedAt: time.Date(2026, 8, 9, 8, 0, 0, 0, time.UTC),
		ContentText: pageContent, ContentSHA256: knowledge.SHA256Hex(pageContent),
		SourceTier: webresearch.SourceTierOfficial, UntrustedContent: true,
	}
	webSnapshot, err := json.Marshal(page)
	if err != nil {
		t.Fatal(err)
	}
	webSources, ok := conversationCitationSourcesFromTool(ToolFetchPublicPage, string(webSnapshot))
	if !ok || len(webSources) != 1 || webSources[0].SourceType != conversation.CitationSourceWeb ||
		webSources[0].SourceRef != page.URL || webSources[0].ContentSHA256 != page.ContentSHA256 {
		t.Fatalf("web sources = %+v, ok=%v", webSources, ok)
	}
}

func TestAugmentConversationToolResultWithCitationSourcesIsBoundedAndNonOverwriting(t *testing.T) {
	source := conversation.MessageCitation{
		SourceType: conversation.CitationSourceWeb, SourceRef: "https://docs.example.com/runbook",
		ContentSHA256: strings.Repeat("a", 64),
	}
	augmented, ok := augmentConversationToolResultWithCitationSources(`{"content":"bounded"}`, []conversation.MessageCitation{source}, 4096)
	if !ok {
		t.Fatal("valid tool result was not augmented")
	}
	var payload struct {
		Content         string                              `json:"content"`
		CitationSources []conversationCitationSourcePayload `json:"citationSources"`
	}
	if err := json.Unmarshal([]byte(augmented), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Content != "bounded" || len(payload.CitationSources) != 1 ||
		payload.CitationSources[0].SourceType != source.SourceType ||
		payload.CitationSources[0].SourceRef != source.SourceRef ||
		payload.CitationSources[0].ContentSHA256 != source.ContentSHA256 ||
		payload.CitationSources[0].Marker != "[source:"+source.SourceRef+"]" {
		t.Fatalf("augmented payload = %+v", payload)
	}
	if _, ok := augmentConversationToolResultWithCitationSources(
		`{"citationSources":[]}`, []conversation.MessageCitation{source}, 4096,
	); ok {
		t.Fatal("existing citationSources field was overwritten")
	}
	if _, ok := augmentConversationToolResultWithCitationSources(
		`{"content":"bounded"}`, []conversation.MessageCitation{source}, len(augmented)-1,
	); ok {
		t.Fatal("tool result exceeded the configured byte budget")
	}
}

func TestConversationCitationTraceRejectsSourcesBeyondRunLimit(t *testing.T) {
	trace := &conversationCitationTrace{}
	sources := make([]conversation.MessageCitation, 0, conversation.MaxCitationSourcesPerRun)
	for index := 0; index < conversation.MaxCitationSourcesPerRun; index++ {
		sources = append(sources, conversation.MessageCitation{
			SourceType: conversation.CitationSourceWeb,
			SourceRef:  "https://docs.example.com/page/" + strconv.Itoa(index),
			ContentSHA256: strings.Repeat("a", 63) +
				strconv.FormatInt(int64(index%16), 16),
		})
	}
	if !trace.append(sources) || len(trace.snapshot()) != conversation.MaxCitationSourcesPerRun {
		t.Fatalf("trace accepted %d sources", len(trace.snapshot()))
	}
	if trace.append([]conversation.MessageCitation{{
		SourceType: conversation.CitationSourceWeb,
		SourceRef:  "https://docs.example.com/overflow", ContentSHA256: strings.Repeat("b", 64),
	}}) {
		t.Fatal("trace accepted a source beyond the per-run limit")
	}
	_, channels, _, truncated := trace.observationSnapshot()
	if !truncated || len(channels) != 1 || channels[0] != "retrieved_sources_truncated" {
		t.Fatalf("overflow observation truncated=%v channels=%v", truncated, channels)
	}
}
