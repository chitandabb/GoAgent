// Package evaluationledger defines the shared, domain-neutral record envelope used to replay
// existing MESGuard evaluations. Domain evaluators continue to own their cases and metrics.
package evaluationledger

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
)

const SchemaVersion = "evaluation_ledger_v1"

type AssetStatus string

const (
	AssetReusable     AssetStatus = "reusable"
	AssetRecomputed   AssetStatus = "recomputed"
	AssetRetestNeeded AssetStatus = "retest_needed"
	AssetObsolete     AssetStatus = "obsolete"
)

func (s AssetStatus) Valid() bool {
	return s == AssetReusable || s == AssetRecomputed || s == AssetRetestNeeded || s == AssetObsolete
}

// Asset describes one existing evaluation entry point and the audit decision for its recorded results.
type Asset struct {
	ID                  string      `json:"id"`
	Domain              string      `json:"domain"`
	ObservationKind     string      `json:"observationKind"`
	Status              AssetStatus `json:"status"`
	Reason              string      `json:"reason"`
	EntryPoint          string      `json:"entryPoint"`
	DatasetArtifact     string      `json:"datasetArtifact,omitempty"`
	ObservationArtifact string      `json:"observationArtifact,omitempty"`
	ReportArtifact      string      `json:"reportArtifact,omitempty"`
}

func (a Asset) Validate() error {
	if strings.TrimSpace(a.ID) == "" || strings.TrimSpace(a.Domain) == "" ||
		strings.TrimSpace(a.ObservationKind) == "" || strings.TrimSpace(a.Reason) == "" ||
		strings.TrimSpace(a.EntryPoint) == "" {
		return errors.New("asset identity, reason, and entryPoint are required")
	}
	if !a.Status.Valid() {
		return fmt.Errorf("invalid asset status %q", a.Status)
	}
	return nil
}

type Outcome string

const (
	OutcomeSucceeded Outcome = "succeeded"
	OutcomeFailed    Outcome = "failed"
)

func (o Outcome) Valid() bool {
	return o == OutcomeSucceeded || o == OutcomeFailed
}

// Usage is provider-reported usage. A nil *Usage means that the source observation did not
// provide usage; callers must not replace that absence with a zero-valued Usage.
type Usage struct {
	ModelCalls       int `json:"modelCalls"`
	PromptTokens     int `json:"promptTokens"`
	CompletionTokens int `json:"completionTokens"`
	TotalTokens      int `json:"totalTokens"`
	CachedTokens     int `json:"cachedTokens"`
	ReasoningTokens  int `json:"reasoningTokens"`
}

func (u Usage) Validate() error {
	if u.ModelCalls < 0 || u.PromptTokens < 0 || u.CompletionTokens < 0 || u.TotalTokens < 0 ||
		u.CachedTokens < 0 || u.ReasoningTokens < 0 {
		return errors.New("usage values cannot be negative")
	}
	maxInt := int(^uint(0) >> 1)
	if u.CompletionTokens > maxInt-u.PromptTokens {
		return errors.New("promptTokens plus completionTokens overflow")
	}
	if u.TotalTokens < u.PromptTokens+u.CompletionTokens {
		return errors.New("totalTokens cannot be less than promptTokens plus completionTokens")
	}
	if u.ModelCalls == 0 && (u.PromptTokens > 0 || u.CompletionTokens > 0 || u.TotalTokens > 0 ||
		u.CachedTokens > 0 || u.ReasoningTokens > 0) {
		return errors.New("token usage requires a positive modelCalls value")
	}
	if u.ModelCalls > 0 && u.TotalTokens == 0 {
		return errors.New("positive modelCalls requires positive totalTokens")
	}
	if u.CachedTokens > u.PromptTokens {
		return errors.New("cachedTokens cannot exceed promptTokens")
	}
	if u.ReasoningTokens > u.CompletionTokens {
		return errors.New("reasoningTokens cannot exceed completionTokens")
	}
	return nil
}

// Record contains only fields shared across evaluation domains. Domain-specific Gold and scores
// remain in DomainSummary or the original observation artifact.
type Record struct {
	Domain                 string   `json:"domain"`
	DatasetVersion         string   `json:"datasetVersion"`
	CaseID                 string   `json:"caseId"`
	Variant                string   `json:"variant"`
	RunID                  string   `json:"runId"`
	Operation              string   `json:"operation"`
	Outcome                Outcome  `json:"outcome"`
	ModelProvider          string   `json:"modelProvider,omitempty"`
	ModelID                string   `json:"modelId,omitempty"`
	ModelProfile           string   `json:"modelProfile,omitempty"`
	PromptVersion          string   `json:"promptVersion,omitempty"`
	ReasoningEffort        string   `json:"reasoningEffort,omitempty"`
	Usage                  *Usage   `json:"usage,omitempty"`
	DurationMillis         int64    `json:"durationMillis"`
	ErrorType              string   `json:"errorType,omitempty"`
	DegradationReasons     []string `json:"degradationReasons,omitempty"`
	ConfigFingerprint      string   `json:"configFingerprint"`
	ImplementationRevision string   `json:"implementationRevision"`
}

// SourceMetadata supplies fingerprints that historical domain observations did not originally
// persist. Replayers must provide them explicitly instead of guessing from local state.
type SourceMetadata struct {
	ModelProfile           string `json:"modelProfile"`
	ConfigFingerprint      string `json:"configFingerprint"`
	ImplementationRevision string `json:"implementationRevision"`
	DatasetSHA256          string `json:"datasetSha256"`
	ObservationSHA256      string `json:"observationSha256"`
}

func (m SourceMetadata) Validate() error {
	if strings.TrimSpace(m.ModelProfile) == "" || strings.TrimSpace(m.ConfigFingerprint) == "" ||
		strings.TrimSpace(m.ImplementationRevision) == "" || strings.TrimSpace(m.DatasetSHA256) == "" ||
		strings.TrimSpace(m.ObservationSHA256) == "" {
		return errors.New("model profile, config fingerprint, implementation revision, and source hashes are required")
	}
	if !knownOrUnavailable(m.ConfigFingerprint, "sha256:") ||
		!knownOrUnavailable(m.ImplementationRevision, "git:") ||
		!strings.HasPrefix(m.DatasetSHA256, "sha256:") || !strings.HasPrefix(m.ObservationSHA256, "sha256:") {
		return errors.New("source metadata fingerprints have invalid formats")
	}
	return nil
}

func knownOrUnavailable(value, knownPrefix string) bool {
	return strings.HasPrefix(value, knownPrefix) || strings.HasPrefix(value, "unavailable:")
}

func (r Record) Validate() error {
	if strings.TrimSpace(r.Domain) == "" || strings.TrimSpace(r.DatasetVersion) == "" ||
		strings.TrimSpace(r.CaseID) == "" || strings.TrimSpace(r.Variant) == "" ||
		strings.TrimSpace(r.RunID) == "" || strings.TrimSpace(r.Operation) == "" ||
		strings.TrimSpace(r.ConfigFingerprint) == "" || strings.TrimSpace(r.ImplementationRevision) == "" {
		return errors.New("record identity, operation, config fingerprint, and implementation revision are required")
	}
	if !r.Outcome.Valid() {
		return fmt.Errorf("invalid outcome %q", r.Outcome)
	}
	if r.DurationMillis < 0 {
		return errors.New("durationMillis cannot be negative")
	}
	if r.Outcome == OutcomeFailed && strings.TrimSpace(r.ErrorType) == "" {
		return errors.New("failed record requires errorType")
	}
	if r.Outcome == OutcomeSucceeded && strings.TrimSpace(r.ErrorType) != "" {
		return errors.New("succeeded record cannot contain errorType")
	}
	if r.Usage != nil {
		if err := r.Usage.Validate(); err != nil {
			return err
		}
	}
	return nil
}

type Summary struct {
	Runs                 int              `json:"runs"`
	SucceededRuns        int              `json:"succeededRuns"`
	FailedRuns           int              `json:"failedRuns"`
	UsageAvailableRuns   int              `json:"usageAvailableRuns"`
	UsageUnavailableRuns int              `json:"usageUnavailableRuns"`
	Usage                Usage            `json:"usage"`
	UsageBreakdown       []UsageBreakdown `json:"usageBreakdown"`
}

type UsageBreakdown struct {
	ModelProvider        string `json:"modelProvider,omitempty"`
	ModelID              string `json:"modelId,omitempty"`
	Operation            string `json:"operation"`
	UsageAvailableRuns   int    `json:"usageAvailableRuns"`
	UsageUnavailableRuns int    `json:"usageUnavailableRuns"`
	Usage                Usage  `json:"usage"`
}

type Report struct {
	SchemaVersion string          `json:"schemaVersion"`
	Asset         Asset           `json:"asset"`
	Source        SourceMetadata  `json:"source"`
	Records       []Record        `json:"records"`
	Summary       Summary         `json:"summary"`
	DomainSummary json.RawMessage `json:"domainSummary"`
}

func (r Report) DecodeDomainSummary(target any) error {
	if target == nil {
		return errors.New("domain summary target is required")
	}
	if len(r.DomainSummary) == 0 {
		return errors.New("domain summary is empty")
	}
	if err := json.Unmarshal(r.DomainSummary, target); err != nil {
		return fmt.Errorf("decode domain summary: %w", err)
	}
	return nil
}

func BuildReport(asset Asset, source SourceMetadata, records []Record, domainSummary any) (Report, error) {
	if err := asset.Validate(); err != nil {
		return Report{}, fmt.Errorf("asset: %w", err)
	}
	if err := source.Validate(); err != nil {
		return Report{}, fmt.Errorf("source: %w", err)
	}
	if len(records) == 0 {
		return Report{}, errors.New("ledger contains no records")
	}
	encodedDomainSummary, err := json.Marshal(domainSummary)
	if err != nil {
		return Report{}, fmt.Errorf("marshal domain summary: %w", err)
	}
	if string(encodedDomainSummary) == "null" {
		return Report{}, errors.New("domain summary is required")
	}

	report := Report{
		SchemaVersion: SchemaVersion,
		Asset:         asset,
		Source:        source,
		Records:       append([]Record(nil), records...),
		DomainSummary: encodedDomainSummary,
	}
	seenRuns := make(map[string]struct{}, len(records))
	seenCaseVariants := make(map[string]string, len(records))
	usageByKey := make(map[string]*UsageBreakdown)
	for index, record := range records {
		if err := record.Validate(); err != nil {
			return Report{}, fmt.Errorf("record %d: %w", index, err)
		}
		if record.Domain != asset.Domain {
			return Report{}, fmt.Errorf("record %q domain %q does not match asset domain %q", record.RunID, record.Domain, asset.Domain)
		}
		if _, exists := seenRuns[record.RunID]; exists {
			return Report{}, fmt.Errorf("duplicate runId %q", record.RunID)
		}
		seenRuns[record.RunID] = struct{}{}
		caseVariant := strings.Join([]string{record.Domain, record.DatasetVersion, record.CaseID, record.Variant}, "\x00")
		if previous, exists := seenCaseVariants[caseVariant]; exists {
			return Report{}, fmt.Errorf("conflicting records %q and %q for case %q variant %q", previous, record.RunID, record.CaseID, record.Variant)
		}
		seenCaseVariants[caseVariant] = record.RunID

		report.Summary.Runs++
		if record.Outcome == OutcomeSucceeded {
			report.Summary.SucceededRuns++
		} else {
			report.Summary.FailedRuns++
		}
		usageKey := strings.Join([]string{record.ModelProvider, record.ModelID, record.Operation}, "\x00")
		breakdown := usageByKey[usageKey]
		if breakdown == nil {
			breakdown = &UsageBreakdown{
				ModelProvider: record.ModelProvider, ModelID: record.ModelID, Operation: record.Operation,
			}
			usageByKey[usageKey] = breakdown
		}
		if record.Usage == nil {
			report.Summary.UsageUnavailableRuns++
			breakdown.UsageUnavailableRuns++
			continue
		}
		report.Summary.UsageAvailableRuns++
		breakdown.UsageAvailableRuns++
		if err := addUsage(&report.Summary.Usage, *record.Usage); err != nil {
			return Report{}, fmt.Errorf("record %q: %w", record.RunID, err)
		}
		if err := addUsage(&breakdown.Usage, *record.Usage); err != nil {
			return Report{}, fmt.Errorf("record %q usage breakdown: %w", record.RunID, err)
		}
	}
	keys := make([]string, 0, len(usageByKey))
	for key := range usageByKey {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		report.Summary.UsageBreakdown = append(report.Summary.UsageBreakdown, *usageByKey[key])
	}
	return report, nil
}

func addUsage(total *Usage, current Usage) error {
	fields := []struct {
		name    string
		target  *int
		current int
	}{
		{name: "modelCalls", target: &total.ModelCalls, current: current.ModelCalls},
		{name: "promptTokens", target: &total.PromptTokens, current: current.PromptTokens},
		{name: "completionTokens", target: &total.CompletionTokens, current: current.CompletionTokens},
		{name: "totalTokens", target: &total.TotalTokens, current: current.TotalTokens},
		{name: "cachedTokens", target: &total.CachedTokens, current: current.CachedTokens},
		{name: "reasoningTokens", target: &total.ReasoningTokens, current: current.ReasoningTokens},
	}
	maxInt := int(^uint(0) >> 1)
	for _, field := range fields {
		if field.current > maxInt-*field.target {
			return fmt.Errorf("usage %s overflow", field.name)
		}
		*field.target += field.current
	}
	return nil
}
