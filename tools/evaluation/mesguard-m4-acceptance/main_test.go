package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/chitandabb/GoAgent/internal/evaluationledger"
)

func TestRunBuildsProviderFreeAcceptanceReportAndRefusesOverwrite(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	inventoryPath := filepath.Join(root, "inventory.json")
	manifestPath := filepath.Join(root, "manifest.json")
	configPath := filepath.Join(root, "mesguard.toml")
	reportArtifact := filepath.Join(root, "metric.json")
	outputPath := filepath.Join(root, "acceptance.json")
	writeJSON(t, inventoryPath, evaluationledger.Inventory{
		SchemaVersion: evaluationledger.InventorySchemaVersion, ArtifactRoot: ".",
		Assets: []evaluationledger.Asset{{
			ID: "cache", Domain: "cache", ObservationKind: "cache", Status: evaluationledger.AssetRecomputed,
			Reason: "current fixed set", EntryPoint: "cache-eval", ReportArtifact: "metric.json",
		}},
	})
	writeJSON(t, manifestPath, evaluationledger.AcceptanceManifest{
		SchemaVersion: evaluationledger.AcceptanceManifestSchemaVersion,
		Evidence: []evaluationledger.EvidenceDefinition{{
			ID: "cache-performance", AssetID: "cache", Artifact: "metric.json", ArtifactSchemaVersion: "metric_v1",
			Metrics: []evaluationledger.MetricDefinition{{Name: "p95", Pointer: "/p95"}},
		}},
	})
	writeJSON(t, reportArtifact, map[string]any{"schemaVersion": "metric_v1", "p95": 244.7})
	if err := os.WriteFile(configPath, []byte("[cache]\nenabled = true\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	args := []string{
		"-inventory", inventoryPath, "-manifest", manifestPath, "-runtime-config", configPath,
		"-output", outputPath, "-implementation-revision", "git:abc1234",
	}
	var stdout, stderr bytes.Buffer
	if code := run(args, &stdout, &stderr); code != 0 {
		t.Fatalf("run code=%d stderr=%s", code, stderr.String())
	}
	if stdout.String() != "m4_acceptance assets=1 evidence_assets=1 retest_needed=0 provider_calls=0\n" {
		t.Fatalf("stdout = %q", stdout.String())
	}
	contents, err := os.ReadFile(outputPath)
	if err != nil {
		t.Fatal(err)
	}
	var report evaluationledger.AcceptanceReport
	if err := json.Unmarshal(contents, &report); err != nil {
		t.Fatal(err)
	}
	if report.ProviderCalls != 0 || string(report.Evidence[0].Metrics[0].Value) != "244.7" {
		t.Fatalf("report = %+v", report)
	}
	stdout.Reset()
	stderr.Reset()
	if code := run(args, &stdout, &stderr); code != 1 || !strings.Contains(stderr.String(), "already exists") {
		t.Fatalf("overwrite code=%d stderr=%s", code, stderr.String())
	}
}

func TestRunRequiresExplicitRevisionAndInputs(t *testing.T) {
	t.Parallel()
	var stdout, stderr bytes.Buffer
	if code := run(nil, &stdout, &stderr); code != 2 || !strings.Contains(stderr.String(), "are required") {
		t.Fatalf("code=%d stderr=%s", code, stderr.String())
	}
}

func writeJSON(t *testing.T, path string, value any) {
	t.Helper()
	contents, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append(contents, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
}
