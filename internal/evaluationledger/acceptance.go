package evaluationledger

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

const (
	AcceptanceManifestSchemaVersion = "m4_acceptance_manifest_v1"
	AcceptanceReportSchemaVersion   = "m4_acceptance_v1"
	maxAcceptanceArtifactBytes      = 64 << 20
)

var gitRevisionPattern = regexp.MustCompile(`^git:[0-9a-f]{7,40}$`)

type AcceptanceManifest struct {
	SchemaVersion string               `json:"schemaVersion"`
	Evidence      []EvidenceDefinition `json:"evidence"`
}

type EvidenceDefinition struct {
	ID                    string             `json:"id"`
	AssetID               string             `json:"assetId"`
	Artifact              string             `json:"artifact"`
	ArtifactSchemaVersion string             `json:"artifactSchemaVersion"`
	Metrics               []MetricDefinition `json:"metrics"`
}

type MetricDefinition struct {
	Name    string `json:"name"`
	Pointer string `json:"pointer"`
}

func ParseAcceptanceManifest(reader io.Reader) (AcceptanceManifest, error) {
	if reader == nil {
		return AcceptanceManifest{}, errors.New("acceptance manifest reader is required")
	}
	decoder := json.NewDecoder(reader)
	decoder.DisallowUnknownFields()
	var manifest AcceptanceManifest
	if err := decoder.Decode(&manifest); err != nil {
		return AcceptanceManifest{}, fmt.Errorf("decode acceptance manifest: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			err = errors.New("multiple JSON values")
		}
		return AcceptanceManifest{}, fmt.Errorf("decode acceptance manifest: %w", err)
	}
	if err := manifest.Validate(); err != nil {
		return AcceptanceManifest{}, err
	}
	return manifest, nil
}

func (m AcceptanceManifest) Validate() error {
	if m.SchemaVersion != AcceptanceManifestSchemaVersion {
		return fmt.Errorf("unsupported acceptance manifest schemaVersion %q", m.SchemaVersion)
	}
	if len(m.Evidence) == 0 {
		return errors.New("acceptance manifest contains no evidence")
	}
	seenEvidence := make(map[string]struct{}, len(m.Evidence))
	for index, evidence := range m.Evidence {
		if strings.TrimSpace(evidence.ID) == "" || evidence.ID != strings.TrimSpace(evidence.ID) ||
			strings.TrimSpace(evidence.AssetID) == "" || evidence.AssetID != strings.TrimSpace(evidence.AssetID) ||
			strings.TrimSpace(evidence.ArtifactSchemaVersion) == "" {
			return fmt.Errorf("evidence %d identity, assetId, and artifactSchemaVersion are required", index)
		}
		if !validRelativeArtifact(evidence.Artifact) {
			return fmt.Errorf("evidence %q artifact must be a clean relative path", evidence.ID)
		}
		if len(evidence.Metrics) == 0 {
			return fmt.Errorf("evidence %q contains no metrics", evidence.ID)
		}
		if _, exists := seenEvidence[evidence.ID]; exists {
			return fmt.Errorf("duplicate evidence id %q", evidence.ID)
		}
		seenEvidence[evidence.ID] = struct{}{}
		seenMetrics := make(map[string]struct{}, len(evidence.Metrics))
		for metricIndex, metric := range evidence.Metrics {
			if strings.TrimSpace(metric.Name) == "" || metric.Name != strings.TrimSpace(metric.Name) {
				return fmt.Errorf("evidence %q metric %d name is required and must be trimmed", evidence.ID, metricIndex)
			}
			if metric.Pointer == "" || !strings.HasPrefix(metric.Pointer, "/") {
				return fmt.Errorf("evidence %q metric %q pointer must be an absolute JSON Pointer", evidence.ID, metric.Name)
			}
			if _, exists := seenMetrics[metric.Name]; exists {
				return fmt.Errorf("evidence %q has duplicate metric %q", evidence.ID, metric.Name)
			}
			seenMetrics[metric.Name] = struct{}{}
		}
	}
	return nil
}

type AcceptanceReport struct {
	SchemaVersion          string            `json:"schemaVersion"`
	ImplementationRevision string            `json:"implementationRevision"`
	InventorySHA256        string            `json:"inventorySha256"`
	RuntimeConfigSHA256    string            `json:"runtimeConfigSha256"`
	ProviderCalls          int               `json:"providerCalls"`
	Summary                AcceptanceSummary `json:"summary"`
	Assets                 []AssetAudit      `json:"assets"`
	Evidence               []EvidenceResult  `json:"evidence"`
}

type AcceptanceSummary struct {
	TotalAssets           int `json:"totalAssets"`
	ReusableAssets        int `json:"reusableAssets"`
	RecomputedAssets      int `json:"recomputedAssets"`
	RetestNeededAssets    int `json:"retestNeededAssets"`
	ObsoleteAssets        int `json:"obsoleteAssets"`
	CurrentEvidenceAssets int `json:"currentEvidenceAssets"`
}

type AssetAudit struct {
	ID                     string          `json:"id"`
	Domain                 string          `json:"domain"`
	Status                 AssetStatus     `json:"status"`
	Reason                 string          `json:"reason"`
	ImplementationRevision string          `json:"implementationRevision"`
	RuntimeConfigSHA256    string          `json:"runtimeConfigSha256"`
	CurrentEvidenceAllowed bool            `json:"currentEvidenceAllowed"`
	Artifacts              []ArtifactAudit `json:"artifacts"`
}

type ArtifactAudit struct {
	Kind   string `json:"kind"`
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
	Bytes  int64  `json:"bytes"`
}

type EvidenceResult struct {
	ID                    string         `json:"id"`
	AssetID               string         `json:"assetId"`
	Artifact              ArtifactAudit  `json:"artifact"`
	ArtifactSchemaVersion string         `json:"artifactSchemaVersion"`
	Metrics               []MetricResult `json:"metrics"`
}

type MetricResult struct {
	Name  string          `json:"name"`
	Value json.RawMessage `json:"value"`
}

func BuildAcceptanceReport(
	inventory Inventory,
	manifest AcceptanceManifest,
	artifactRoot string,
	inventoryContents []byte,
	runtimeConfigContents []byte,
	implementationRevision string,
) (AcceptanceReport, error) {
	if err := inventory.Validate(); err != nil {
		return AcceptanceReport{}, err
	}
	if err := manifest.Validate(); err != nil {
		return AcceptanceReport{}, err
	}
	if !gitRevisionPattern.MatchString(implementationRevision) {
		return AcceptanceReport{}, errors.New("implementation revision must be git:<7-40 lowercase hexadecimal commit>")
	}
	root, err := filepath.Abs(artifactRoot)
	if err != nil {
		return AcceptanceReport{}, fmt.Errorf("resolve artifact root: %w", err)
	}
	runtimeConfigSHA256 := digestBytes(runtimeConfigContents)
	report := AcceptanceReport{
		SchemaVersion:          AcceptanceReportSchemaVersion,
		ImplementationRevision: implementationRevision,
		InventorySHA256:        digestBytes(inventoryContents),
		RuntimeConfigSHA256:    runtimeConfigSHA256,
		ProviderCalls:          0,
		Summary:                AcceptanceSummary{TotalAssets: len(inventory.Assets)},
	}
	for _, asset := range inventory.Assets {
		audit := AssetAudit{
			ID: asset.ID, Domain: asset.Domain, Status: asset.Status, Reason: asset.Reason,
			ImplementationRevision: implementationRevision, RuntimeConfigSHA256: runtimeConfigSHA256,
			CurrentEvidenceAllowed: asset.Status == AssetReusable || asset.Status == AssetRecomputed,
		}
		switch asset.Status {
		case AssetReusable:
			report.Summary.ReusableAssets++
		case AssetRecomputed:
			report.Summary.RecomputedAssets++
		case AssetRetestNeeded:
			report.Summary.RetestNeededAssets++
		case AssetObsolete:
			report.Summary.ObsoleteAssets++
		}
		artifacts := []struct{ kind, path string }{
			{kind: "dataset", path: asset.DatasetArtifact},
			{kind: "observations", path: asset.ObservationArtifact},
			{kind: "report", path: asset.ReportArtifact},
		}
		for _, artifact := range artifacts {
			if artifact.path == "" {
				continue
			}
			artifactAudit, _, err := auditArtifact(root, artifact.kind, artifact.path)
			if err != nil {
				return AcceptanceReport{}, fmt.Errorf("asset %q: %w", asset.ID, err)
			}
			audit.Artifacts = append(audit.Artifacts, artifactAudit)
		}
		if len(audit.Artifacts) == 0 {
			return AcceptanceReport{}, fmt.Errorf("asset %q declares no artifacts", asset.ID)
		}
		report.Assets = append(report.Assets, audit)
	}

	evidenceAssets := make(map[string]struct{})
	for _, definition := range manifest.Evidence {
		asset, err := inventory.Asset(definition.AssetID)
		if err != nil {
			return AcceptanceReport{}, fmt.Errorf("evidence %q: %w", definition.ID, err)
		}
		if asset.Status != AssetReusable && asset.Status != AssetRecomputed {
			return AcceptanceReport{}, fmt.Errorf("evidence %q references asset %q with status %q", definition.ID, asset.ID, asset.Status)
		}
		artifact, contents, err := auditArtifact(root, "acceptance_evidence", definition.Artifact)
		if err != nil {
			return AcceptanceReport{}, fmt.Errorf("evidence %q: %w", definition.ID, err)
		}
		result, err := extractEvidence(definition, artifact, contents)
		if err != nil {
			return AcceptanceReport{}, err
		}
		report.Evidence = append(report.Evidence, result)
		evidenceAssets[asset.ID] = struct{}{}
	}
	report.Summary.CurrentEvidenceAssets = len(evidenceAssets)
	return report, nil
}

func extractEvidence(definition EvidenceDefinition, artifact ArtifactAudit, contents []byte) (EvidenceResult, error) {
	decoder := json.NewDecoder(bytes.NewReader(contents))
	decoder.UseNumber()
	var document any
	if err := decoder.Decode(&document); err != nil {
		return EvidenceResult{}, fmt.Errorf("evidence %q decode artifact: %w", definition.ID, err)
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			err = errors.New("multiple JSON values")
		}
		return EvidenceResult{}, fmt.Errorf("evidence %q decode artifact: %w", definition.ID, err)
	}
	root, ok := document.(map[string]any)
	if !ok {
		return EvidenceResult{}, fmt.Errorf("evidence %q artifact root must be an object", definition.ID)
	}
	if root["schemaVersion"] != definition.ArtifactSchemaVersion {
		return EvidenceResult{}, fmt.Errorf("evidence %q schemaVersion = %v, want %q", definition.ID, root["schemaVersion"], definition.ArtifactSchemaVersion)
	}
	result := EvidenceResult{
		ID: definition.ID, AssetID: definition.AssetID, Artifact: artifact,
		ArtifactSchemaVersion: definition.ArtifactSchemaVersion,
	}
	for _, metric := range definition.Metrics {
		value, err := resolveJSONPointer(document, metric.Pointer)
		if err != nil {
			return EvidenceResult{}, fmt.Errorf("evidence %q metric %q: %w", definition.ID, metric.Name, err)
		}
		switch value.(type) {
		case nil, bool, string, json.Number:
		default:
			return EvidenceResult{}, fmt.Errorf("evidence %q metric %q must resolve to a scalar", definition.ID, metric.Name)
		}
		encoded, err := json.Marshal(value)
		if err != nil {
			return EvidenceResult{}, fmt.Errorf("evidence %q metric %q: %w", definition.ID, metric.Name, err)
		}
		result.Metrics = append(result.Metrics, MetricResult{Name: metric.Name, Value: encoded})
	}
	return result, nil
}

func resolveJSONPointer(document any, pointer string) (any, error) {
	current := document
	for _, rawToken := range strings.Split(strings.TrimPrefix(pointer, "/"), "/") {
		token := strings.ReplaceAll(strings.ReplaceAll(rawToken, "~1", "/"), "~0", "~")
		switch typed := current.(type) {
		case map[string]any:
			value, exists := typed[token]
			if !exists {
				return nil, fmt.Errorf("JSON Pointer %q does not exist", pointer)
			}
			current = value
		case []any:
			index, err := strconv.Atoi(token)
			if err != nil || index < 0 || index >= len(typed) {
				return nil, fmt.Errorf("JSON Pointer %q has invalid array index %q", pointer, token)
			}
			current = typed[index]
		default:
			return nil, fmt.Errorf("JSON Pointer %q traverses a scalar", pointer)
		}
	}
	return current, nil
}

func auditArtifact(root, kind, relativePath string) (ArtifactAudit, []byte, error) {
	if !validRelativeArtifact(relativePath) {
		return ArtifactAudit{}, nil, fmt.Errorf("artifact %q must be a clean relative path", relativePath)
	}
	path := filepath.Join(root, filepath.FromSlash(relativePath))
	contents, err := os.ReadFile(path)
	if err != nil {
		return ArtifactAudit{}, nil, fmt.Errorf("read %s artifact %q: %w", kind, relativePath, err)
	}
	if len(contents) > maxAcceptanceArtifactBytes {
		return ArtifactAudit{}, nil, fmt.Errorf("%s artifact %q exceeds %d bytes", kind, relativePath, maxAcceptanceArtifactBytes)
	}
	return ArtifactAudit{
		Kind: kind, Path: filepath.ToSlash(relativePath), SHA256: digestBytes(contents), Bytes: int64(len(contents)),
	}, contents, nil
}

func validRelativeArtifact(path string) bool {
	if strings.TrimSpace(path) == "" || filepath.IsAbs(path) {
		return false
	}
	clean := filepath.Clean(filepath.FromSlash(path))
	return clean != "." && clean != ".." && !strings.HasPrefix(clean, ".."+string(filepath.Separator)) &&
		filepath.ToSlash(clean) == filepath.ToSlash(filepath.FromSlash(path))
}

func digestBytes(contents []byte) string {
	digest := sha256.Sum256(contents)
	return fmt.Sprintf("sha256:%x", digest[:])
}
