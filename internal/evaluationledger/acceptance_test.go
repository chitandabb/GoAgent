package evaluationledger

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBuildAcceptanceReportAuditsArtifactsAndExtractsCurrentEvidence(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeAcceptanceFixture(t, root, "dataset.json", []byte("{}\n"))
	writeAcceptanceFixture(t, root, "report.json", []byte(`{"schemaVersion":"metric_v1","holdout":{"precision":1},"calls":0}`))
	inventory := Inventory{SchemaVersion: InventorySchemaVersion, ArtifactRoot: ".", Assets: []Asset{
		{ID: "current", Domain: "cache", ObservationKind: "cache", Status: AssetRecomputed, Reason: "current fixed set", EntryPoint: "eval", DatasetArtifact: "dataset.json", ReportArtifact: "report.json"},
		{ID: "stale", Domain: "tool", ObservationKind: "tool", Status: AssetRetestNeeded, Reason: "contract changed", EntryPoint: "tool-eval", ReportArtifact: "report.json"},
	}}
	manifest := AcceptanceManifest{SchemaVersion: AcceptanceManifestSchemaVersion, Evidence: []EvidenceDefinition{{
		ID: "cache-quality", AssetID: "current", Artifact: "report.json", ArtifactSchemaVersion: "metric_v1",
		Metrics: []MetricDefinition{{Name: "holdout_precision", Pointer: "/holdout/precision"}, {Name: "model_calls", Pointer: "/calls"}},
	}}}
	report, err := BuildAcceptanceReport(inventory, manifest, root, []byte("inventory"), []byte("config"), "git:abc1234")
	if err != nil {
		t.Fatal(err)
	}
	if report.ProviderCalls != 0 || report.Summary.TotalAssets != 2 || report.Summary.RecomputedAssets != 1 ||
		report.Summary.RetestNeededAssets != 1 || report.Summary.CurrentEvidenceAssets != 1 {
		t.Fatalf("summary = %+v providerCalls=%d", report.Summary, report.ProviderCalls)
	}
	if !report.Assets[0].CurrentEvidenceAllowed || report.Assets[1].CurrentEvidenceAllowed {
		t.Fatalf("asset eligibility = %+v", report.Assets)
	}
	if got := string(report.Evidence[0].Metrics[0].Value); got != "1" {
		t.Fatalf("holdout precision = %s", got)
	}
	if !strings.HasPrefix(report.InventorySHA256, "sha256:") || !strings.HasPrefix(report.RuntimeConfigSHA256, "sha256:") {
		t.Fatalf("fingerprints = %q %q", report.InventorySHA256, report.RuntimeConfigSHA256)
	}
}

func TestBuildAcceptanceReportRejectsRetestNeededEvidence(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeAcceptanceFixture(t, root, "report.json", []byte(`{"schemaVersion":"metric_v1","value":1}`))
	inventory := Inventory{SchemaVersion: InventorySchemaVersion, ArtifactRoot: ".", Assets: []Asset{{
		ID: "stale", Domain: "tool", ObservationKind: "tool", Status: AssetRetestNeeded,
		Reason: "contract changed", EntryPoint: "tool-eval", ReportArtifact: "report.json",
	}}}
	manifest := AcceptanceManifest{SchemaVersion: AcceptanceManifestSchemaVersion, Evidence: []EvidenceDefinition{{
		ID: "stale-claim", AssetID: "stale", Artifact: "report.json", ArtifactSchemaVersion: "metric_v1",
		Metrics: []MetricDefinition{{Name: "value", Pointer: "/value"}},
	}}}
	_, err := BuildAcceptanceReport(inventory, manifest, root, nil, nil, "git:abc1234")
	if err == nil || !strings.Contains(err.Error(), `status "retest_needed"`) {
		t.Fatalf("error = %v", err)
	}
}

func TestParseAcceptanceManifestRejectsDuplicateMetrics(t *testing.T) {
	t.Parallel()
	contents := `{"schemaVersion":"m4_acceptance_manifest_v1","evidence":[{"id":"x","assetId":"a","artifact":"report.json","artifactSchemaVersion":"v1","metrics":[{"name":"value","pointer":"/a"},{"name":"value","pointer":"/b"}]}]}`
	_, err := ParseAcceptanceManifest(bytes.NewBufferString(contents))
	if err == nil || !strings.Contains(err.Error(), "duplicate metric") {
		t.Fatalf("error = %v", err)
	}
}

func TestBuildAcceptanceReportRejectsNonCommitRevision(t *testing.T) {
	t.Parallel()
	inventory := Inventory{SchemaVersion: InventorySchemaVersion, ArtifactRoot: ".", Assets: []Asset{{
		ID: "current", Domain: "cache", ObservationKind: "cache", Status: AssetRecomputed,
		Reason: "current", EntryPoint: "eval", ReportArtifact: "report.json",
	}}}
	manifest := AcceptanceManifest{SchemaVersion: AcceptanceManifestSchemaVersion, Evidence: []EvidenceDefinition{{
		ID: "claim", AssetID: "current", Artifact: "report.json", ArtifactSchemaVersion: "metric_v1",
		Metrics: []MetricDefinition{{Name: "value", Pointer: "/value"}},
	}}}
	_, err := BuildAcceptanceReport(inventory, manifest, t.TempDir(), nil, nil, "git:working-tree")
	if err == nil || !strings.Contains(err.Error(), "hexadecimal commit") {
		t.Fatalf("error = %v", err)
	}
}

func TestBuildAcceptanceReportRejectsMissingMetric(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeAcceptanceFixture(t, root, "report.json", []byte(`{"schemaVersion":"metric_v1","value":1}`))
	inventory := Inventory{SchemaVersion: InventorySchemaVersion, ArtifactRoot: ".", Assets: []Asset{{
		ID: "current", Domain: "cache", ObservationKind: "cache", Status: AssetRecomputed,
		Reason: "current", EntryPoint: "eval", ReportArtifact: "report.json",
	}}}
	manifest := AcceptanceManifest{SchemaVersion: AcceptanceManifestSchemaVersion, Evidence: []EvidenceDefinition{{
		ID: "claim", AssetID: "current", Artifact: "report.json", ArtifactSchemaVersion: "metric_v1",
		Metrics: []MetricDefinition{{Name: "missing", Pointer: "/missing"}},
	}}}
	_, err := BuildAcceptanceReport(inventory, manifest, root, nil, nil, "git:abc1234")
	if err == nil || !strings.Contains(err.Error(), "does not exist") {
		t.Fatalf("error = %v", err)
	}
}

func TestBuildAcceptanceReportRejectsTrailingJSONValue(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	path := filepath.Join(root, "report.json")
	if err := os.WriteFile(path, []byte(`{"schemaVersion":"metric_v1","value":1}{"value":2}`), 0o600); err != nil {
		t.Fatal(err)
	}
	inventory := Inventory{SchemaVersion: InventorySchemaVersion, ArtifactRoot: ".", Assets: []Asset{{
		ID: "current", Domain: "cache", ObservationKind: "cache", Status: AssetRecomputed,
		Reason: "current", EntryPoint: "eval", ReportArtifact: "report.json",
	}}}
	manifest := AcceptanceManifest{SchemaVersion: AcceptanceManifestSchemaVersion, Evidence: []EvidenceDefinition{{
		ID: "claim", AssetID: "current", Artifact: "report.json", ArtifactSchemaVersion: "metric_v1",
		Metrics: []MetricDefinition{{Name: "value", Pointer: "/value"}},
	}}}
	_, err := BuildAcceptanceReport(inventory, manifest, root, nil, nil, "git:abc1234")
	if err == nil || !strings.Contains(err.Error(), "multiple JSON values") {
		t.Fatalf("error = %v", err)
	}
}

func writeAcceptanceFixture(t *testing.T, root, name string, contents []byte) {
	t.Helper()
	path := filepath.Join(root, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if !json.Valid(contents) {
		t.Fatalf("fixture %q is not JSON", name)
	}
	if err := os.WriteFile(path, contents, 0o600); err != nil {
		t.Fatal(err)
	}
}
