package contextgovernance

import (
	"errors"
	"math"
	"slices"
)

type PreflightStatus string

const (
	PreflightStatusSucceeded PreflightStatus = "succeeded"
	PreflightStatusFailed    PreflightStatus = "failed"
)

func (s PreflightStatus) Valid() bool {
	return s == PreflightStatusSucceeded || s == PreflightStatusFailed
}

type PromptActualUsage struct {
	Available        bool
	PromptTokens     int
	CachedTokens     int
	CompletionTokens int
}

// PromptManifest is a bounded metadata projection. It intentionally has no
// field capable of storing prompt text, raw Tool payloads, reasoning, secrets,
// or object-store coordinates.
type PromptManifest struct {
	SchemaVersion             int              `json:"schemaVersion"`
	PreflightStatus           PreflightStatus  `json:"preflightStatus"`
	FailureStage              string           `json:"failureStage,omitempty"`
	PromptIdentityAvailable   bool             `json:"promptIdentityAvailable"`
	EstimateAvailable         bool             `json:"estimateAvailable"`
	PromptEpochID             string           `json:"promptEpochId"`
	StablePrefixFingerprint   string           `json:"stablePrefixFingerprint"`
	ModelProfile              string           `json:"modelProfile"`
	ModelProfileFingerprint   string           `json:"modelProfileFingerprint"`
	SystemPromptVersion       string           `json:"systemPromptVersion"`
	SystemPromptFingerprint   string           `json:"systemPromptFingerprint"`
	ToolSchemaFingerprint     string           `json:"toolSchemaFingerprint"`
	SkillPromptFingerprint    string           `json:"skillPromptFingerprint"`
	SummaryFingerprint        string           `json:"summaryFingerprint"`
	SummarySnapshotID         string           `json:"summarySnapshotId,omitempty"`
	HardCompactionTriggered   bool             `json:"hardCompactionTriggered"`
	TailFromSeq               int64            `json:"tailFromSeq"`
	TailThroughSeq            int64            `json:"tailThroughSeq"`
	AvailableInputTokens      int              `json:"availableInputTokens"`
	EstimatedPromptTokens     int              `json:"estimatedPromptTokens"`
	EstimatedUpperBoundTokens int              `json:"estimatedUpperBoundTokens"`
	ToolGrowthReserveTokens   int              `json:"toolGrowthReserveTokens"`
	EstimationMethod          EstimationMethod `json:"estimationMethod"`
	SoftThresholdRatio        float64          `json:"softThresholdRatio"`
	HardThresholdRatio        float64          `json:"hardThresholdRatio"`
	SoftThresholdReached      bool             `json:"softThresholdReached"`
	HardThresholdReached      bool             `json:"hardThresholdReached"`
	ExceedsHardWindow         bool             `json:"exceedsHardWindow"`
	ActualUsageAvailable      bool             `json:"actualUsageAvailable"`
	ActualPromptTokens        int              `json:"actualPromptTokens"`
	CacheHitTokens            int              `json:"cacheHitTokens"`
	CacheMissTokens           int              `json:"cacheMissTokens"`
	CompletionTokens          int              `json:"completionTokens"`
	EstimationErrorRatio      float64          `json:"estimationErrorRatio"`
	PreflightDurationMicros   int64            `json:"preflightDurationMicros"`
	RunDurationMillis         int64            `json:"runDurationMillis"`
	ContextDegraded           bool             `json:"contextDegraded"`
	DegradedReasons           []string         `json:"degradedReasons"`
}

func (m *PromptManifest) FinalizeUsage(usage PromptActualUsage, runDurationMillis int64) {
	if m == nil {
		return
	}
	m.RunDurationMillis = runDurationMillis
	m.ActualUsageAvailable = usage.Available
	if !usage.Available {
		return
	}
	m.ActualPromptTokens = usage.PromptTokens
	m.CacheHitTokens = usage.CachedTokens
	m.CacheMissTokens = usage.PromptTokens - usage.CachedTokens
	m.CompletionTokens = usage.CompletionTokens
	if m.EstimateAvailable && usage.PromptTokens > 0 {
		m.EstimationErrorRatio = float64(usage.PromptTokens-m.EstimatedPromptTokens) /
			float64(usage.PromptTokens)
	}
}

// RequestsAsyncCompaction reports the durable scheduling fact produced by a
// successful preflight. Hard compaction already refreshed the Active Snapshot
// synchronously and therefore must not enqueue redundant async work.
func (m *PromptManifest) RequestsAsyncCompaction() bool {
	return m != nil && m.PreflightStatus == PreflightStatusSucceeded &&
		m.SoftThresholdReached && !m.HardCompactionTriggered
}

func (m PromptManifest) Validate() error {
	if m.SchemaVersion != 1 || !m.PreflightStatus.Valid() ||
		!validMachineLabel(m.ModelProfile, 128) ||
		!validMachineLabel(m.SystemPromptVersion, 128) ||
		m.TailFromSeq < 1 || m.TailThroughSeq < m.TailFromSeq ||
		m.AvailableInputTokens < 1 || m.EstimatedPromptTokens < 0 || m.ToolGrowthReserveTokens < 0 ||
		math.IsNaN(m.SoftThresholdRatio) || math.IsNaN(m.HardThresholdRatio) ||
		m.SoftThresholdRatio <= 0 || m.HardThresholdRatio <= m.SoftThresholdRatio ||
		m.HardThresholdRatio >= 1 || m.PreflightDurationMicros < 0 ||
		m.PreflightDurationMicros > int64((5*60)*1_000_000) ||
		m.RunDurationMillis < 0 || m.RunDurationMillis > 5*60*1000 ||
		len(m.DegradedReasons) > 16 {
		return errors.New("prompt manifest is invalid")
	}
	if m.PreflightStatus == PreflightStatusSucceeded {
		if m.FailureStage != "" || !m.PromptIdentityAvailable || !m.EstimateAvailable {
			return errors.New("successful prompt manifest is incomplete")
		}
	} else if !validMachineLabel(m.FailureStage, 64) || !m.ContextDegraded || m.EstimateAvailable {
		return errors.New("failed prompt manifest state is invalid")
	}
	identityFields := []string{
		m.PromptEpochID, m.StablePrefixFingerprint, m.ModelProfileFingerprint,
		m.SystemPromptFingerprint, m.ToolSchemaFingerprint, m.SkillPromptFingerprint,
		m.SummaryFingerprint,
	}
	for _, fingerprint := range identityFields {
		if m.PromptIdentityAvailable {
			if !sha256Pattern.MatchString(fingerprint) {
				return errors.New("prompt manifest identity is invalid")
			}
		} else if fingerprint != "" {
			return errors.New("unavailable prompt identity must be empty")
		}
	}
	if m.SummarySnapshotID != "" {
		if !m.PromptIdentityAvailable || !IsUUID(m.SummarySnapshotID) ||
			m.SummaryFingerprint == SHA256Hex("") {
			return errors.New("prompt manifest Summary Snapshot identity is invalid")
		}
	}
	if m.EstimateAvailable {
		if m.EstimatedUpperBoundTokens < m.EstimatedPromptTokens+m.ToolGrowthReserveTokens ||
			!m.EstimationMethod.Valid() ||
			m.ExceedsHardWindow != (m.EstimatedUpperBoundTokens > m.AvailableInputTokens) {
			return errors.New("prompt manifest estimate is invalid")
		}
		softReached := float64(m.EstimatedUpperBoundTokens) >= float64(m.AvailableInputTokens)*m.SoftThresholdRatio
		hardReached := float64(m.EstimatedUpperBoundTokens) >= float64(m.AvailableInputTokens)*m.HardThresholdRatio
		if m.SoftThresholdReached != softReached || m.HardThresholdReached != hardReached {
			return errors.New("prompt manifest threshold state is inconsistent")
		}
	} else if m.EstimatedPromptTokens != 0 || m.EstimatedUpperBoundTokens != 0 ||
		m.EstimationMethod != "" || m.SoftThresholdReached || m.HardThresholdReached || m.ExceedsHardWindow {
		return errors.New("unavailable prompt estimate must be empty")
	}
	if m.ActualUsageAvailable {
		if m.ActualPromptTokens < 0 || m.CacheHitTokens < 0 || m.CacheHitTokens > m.ActualPromptTokens ||
			m.CacheMissTokens != m.ActualPromptTokens-m.CacheHitTokens || m.CompletionTokens < 0 ||
			math.IsNaN(m.EstimationErrorRatio) || math.IsInf(m.EstimationErrorRatio, 0) {
			return errors.New("prompt manifest actual usage is invalid")
		}
		expectedErrorRatio := 0.0
		if m.EstimateAvailable && m.ActualPromptTokens > 0 {
			expectedErrorRatio = float64(m.ActualPromptTokens-m.EstimatedPromptTokens) /
				float64(m.ActualPromptTokens)
		}
		if math.Abs(m.EstimationErrorRatio-expectedErrorRatio) > 1e-12 {
			return errors.New("prompt manifest estimation error is inconsistent")
		}
	} else if m.ActualPromptTokens != 0 || m.CacheHitTokens != 0 || m.CacheMissTokens != 0 ||
		m.CompletionTokens != 0 || m.EstimationErrorRatio != 0 {
		return errors.New("prompt manifest unavailable usage must be empty")
	}
	seenReasons := make([]string, 0, len(m.DegradedReasons))
	for _, reason := range m.DegradedReasons {
		if !validMachineLabel(reason, 64) || slices.Contains(seenReasons, reason) {
			return errors.New("prompt manifest degraded reasons are invalid")
		}
		seenReasons = append(seenReasons, reason)
	}
	return nil
}
