package contextgovernance

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestPromptManifestFinalizesUsageWithoutPersistingPromptContent(t *testing.T) {
	manifest := PromptManifest{
		SchemaVersion:             1,
		PreflightStatus:           PreflightStatusSucceeded,
		PromptIdentityAvailable:   true,
		EstimateAvailable:         true,
		PromptEpochID:             SHA256Hex("epoch"),
		StablePrefixFingerprint:   SHA256Hex("prefix"),
		ModelProfile:              "chat-main",
		ModelProfileFingerprint:   SHA256Hex("profile"),
		SystemPromptVersion:       "conversation-v6",
		SystemPromptFingerprint:   SHA256Hex("system"),
		ToolSchemaFingerprint:     SHA256Hex("tools"),
		SkillPromptFingerprint:    SHA256Hex("skill"),
		SummaryFingerprint:        SHA256Hex("summary"),
		TailFromSeq:               3,
		TailThroughSeq:            9,
		AvailableInputTokens:      1000,
		EstimatedPromptTokens:     90,
		EstimatedUpperBoundTokens: 110,
		ToolGrowthReserveTokens:   10,
		EstimationMethod:          EstimationMethodLocalCalibrated,
		SoftThresholdRatio:        0.70,
		HardThresholdRatio:        0.85,
		PreflightDurationMicros:   25,
	}
	manifest.FinalizeUsage(PromptActualUsage{
		Available: true, PromptTokens: 100, CachedTokens: 40, CompletionTokens: 12,
	}, 80)
	if err := manifest.Validate(); err != nil {
		t.Fatalf("Validate(): %v", err)
	}
	if manifest.ActualPromptTokens != 100 || manifest.CacheHitTokens != 40 ||
		manifest.CacheMissTokens != 60 || manifest.EstimationErrorRatio != 0.10 ||
		manifest.RunDurationMillis != 80 {
		t.Fatalf("manifest usage = %+v", manifest)
	}
	encoded, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"full prompt", "raw tool payload", "minio object"} {
		if strings.Contains(string(encoded), secret) {
			t.Fatalf("manifest persisted forbidden content %q: %s", secret, encoded)
		}
	}
}

func TestFailedPromptManifestKeepsActualUsageWithoutInventingEstimateOrIdentity(t *testing.T) {
	manifest := PromptManifest{
		SchemaVersion: 1, PreflightStatus: PreflightStatusFailed,
		FailureStage: "tool_schema_failed", ModelProfile: "chat-main",
		SystemPromptVersion: "conversation-v6", TailFromSeq: 3, TailThroughSeq: 9,
		AvailableInputTokens: 1000, ToolGrowthReserveTokens: 10,
		SoftThresholdRatio: 0.70, HardThresholdRatio: 0.85,
		PreflightDurationMicros: 25, ContextDegraded: true,
		DegradedReasons: []string{"preflight_failed", "tool_schema_failed"},
	}
	manifest.FinalizeUsage(PromptActualUsage{
		Available: true, PromptTokens: 100, CachedTokens: 40, CompletionTokens: 12,
	}, 80)
	if err := manifest.Validate(); err != nil {
		t.Fatalf("Validate(): %v", err)
	}
	if manifest.PromptIdentityAvailable || manifest.EstimateAvailable || manifest.PromptEpochID != "" ||
		manifest.EstimatedPromptTokens != 0 || manifest.EstimationErrorRatio != 0 {
		t.Fatalf("failed manifest invented identity or estimate: %+v", manifest)
	}
}

func TestPromptManifestRequestsAsyncCompactionOnlyForSoftThresholdWithoutHardCompaction(t *testing.T) {
	soft := &PromptManifest{
		PreflightStatus: PreflightStatusSucceeded, SoftThresholdReached: true,
	}
	if !soft.RequestsAsyncCompaction() {
		t.Fatal("soft-threshold manifest did not request async compaction")
	}
	soft.HardCompactionTriggered = true
	if soft.RequestsAsyncCompaction() {
		t.Fatal("hard-compacted manifest requested redundant async compaction")
	}
	if (*PromptManifest)(nil).RequestsAsyncCompaction() {
		t.Fatal("nil manifest requested async compaction")
	}
}
