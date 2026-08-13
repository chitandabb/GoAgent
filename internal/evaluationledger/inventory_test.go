package evaluationledger_test

import (
	"strings"
	"testing"

	"github.com/chitandabb/GoAgent/internal/evaluationledger"
)

func TestParseInventoryValidatesAndFindsVersionedAsset(t *testing.T) {
	t.Parallel()

	input := `{
  "schemaVersion": "evaluation_inventory_v1",
  "artifactRoot": ".",
  "assets": [{
    "id": "tool-selection-v1",
    "domain": "tool_selection",
    "observationKind": "tool_selection",
    "status": "retest_needed",
    "reason": "Current Tool contracts changed after this recorded run.",
    "entryPoint": "mesguard-tool-selection-eval",
    "datasetArtifact": "testdata/tool-selection-v1.jsonl",
    "observationArtifact": "testdata/tool-selection-v1.observations.jsonl",
    "reportArtifact": "testdata/tool-selection-v1.summary.json"
  }]
}`

	inventory, err := evaluationledger.ParseInventory(strings.NewReader(input))
	if err != nil {
		t.Fatalf("ParseInventory() error = %v", err)
	}
	asset, err := inventory.Asset("tool-selection-v1")
	if err != nil {
		t.Fatalf("Asset() error = %v", err)
	}
	if asset.Status != evaluationledger.AssetRetestNeeded || asset.Domain != "tool_selection" {
		t.Fatalf("asset = %+v", asset)
	}
}

func TestParseInventoryRejectsDuplicateAssetID(t *testing.T) {
	t.Parallel()

	input := `{
  "schemaVersion": "evaluation_inventory_v1",
  "artifactRoot": ".",
  "assets": [
    {"id":"duplicate","domain":"a","observationKind":"a","status":"reusable","reason":"first","entryPoint":"first"},
    {"id":"duplicate","domain":"b","observationKind":"b","status":"obsolete","reason":"second","entryPoint":"second"}
  ]
}`

	_, err := evaluationledger.ParseInventory(strings.NewReader(input))
	if err == nil || !strings.Contains(err.Error(), `duplicate asset id "duplicate"`) {
		t.Fatalf("ParseInventory() error = %v", err)
	}
}
