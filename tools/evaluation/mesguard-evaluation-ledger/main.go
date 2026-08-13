// Command mesguard-evaluation-ledger replays recorded evaluation observations into the shared
// Evaluation Ledger. It never creates a model or other Provider client.
package main

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"

	mesagent "github.com/chitandabb/GoAgent/internal/agent"
	"github.com/chitandabb/GoAgent/internal/evaluationledger"
)

const maxReplayArtifactBytes = 64 << 20

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

type options struct {
	inventoryPath          string
	assetID                string
	outputPath             string
	modelProfile           string
	configFingerprint      string
	implementationRevision string
}

func run(args []string, stdout, stderr io.Writer) int {
	opts, err := parseOptions(args, stderr)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	report, err := replay(opts)
	if err != nil {
		fmt.Fprintf(stderr, "build evaluation ledger: %v\n", err)
		return 1
	}
	if err := writeNewReport(opts.outputPath, report); err != nil {
		fmt.Fprintf(stderr, "write evaluation ledger: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "evaluation_ledger asset=%s runs=%d provider_calls=0\n", report.Asset.ID, report.Summary.Runs)
	return 0
}

func parseOptions(args []string, stderr io.Writer) (options, error) {
	flags := flag.NewFlagSet("mesguard-evaluation-ledger", flag.ContinueOnError)
	flags.SetOutput(stderr)
	var result options
	flags.StringVar(&result.inventoryPath, "inventory", "", "versioned evaluation asset inventory JSON")
	flags.StringVar(&result.assetID, "asset", "", "asset id from the inventory")
	flags.StringVar(&result.outputPath, "output", "", "new Evaluation Ledger report JSON; existing files are rejected")
	flags.StringVar(&result.modelProfile, "model-profile", "", "recorded model profile identity")
	flags.StringVar(&result.configFingerprint, "config-fingerprint", "", "recorded configuration fingerprint")
	flags.StringVar(&result.implementationRevision, "implementation-revision", "", "recorded implementation revision")
	if err := flags.Parse(args); err != nil {
		return options{}, err
	}
	if flags.NArg() != 0 {
		return options{}, errors.New("positional arguments are not supported")
	}
	if result.inventoryPath == "" || result.assetID == "" || result.outputPath == "" ||
		result.modelProfile == "" || result.configFingerprint == "" ||
		result.implementationRevision == "" {
		return options{}, errors.New("-inventory, -asset, -output, -model-profile, -config-fingerprint, and -implementation-revision are required")
	}
	return result, nil
}

func replay(opts options) (evaluationledger.Report, error) {
	inventoryFile, err := os.Open(opts.inventoryPath)
	if err != nil {
		return evaluationledger.Report{}, fmt.Errorf("open inventory: %w", err)
	}
	defer inventoryFile.Close()
	inventory, err := evaluationledger.ParseInventory(inventoryFile)
	if err != nil {
		return evaluationledger.Report{}, err
	}
	asset, err := inventory.Asset(opts.assetID)
	if err != nil {
		return evaluationledger.Report{}, err
	}
	if asset.DatasetArtifact == "" || asset.ObservationArtifact == "" {
		return evaluationledger.Report{}, fmt.Errorf("asset %q does not declare both dataset and observation artifacts", asset.ID)
	}
	artifactRoot := filepath.Join(filepath.Dir(opts.inventoryPath), inventory.ArtifactRoot)
	datasetPath := resolveInventoryArtifact(artifactRoot, asset.DatasetArtifact)
	observationPath := resolveInventoryArtifact(artifactRoot, asset.ObservationArtifact)
	datasetContents, datasetHash, err := readReplayArtifact(datasetPath)
	if err != nil {
		return evaluationledger.Report{}, fmt.Errorf("read dataset: %w", err)
	}
	observationContents, observationHash, err := readReplayArtifact(observationPath)
	if err != nil {
		return evaluationledger.Report{}, fmt.Errorf("read observations: %w", err)
	}
	metadata := evaluationledger.SourceMetadata{
		ModelProfile: opts.modelProfile, ConfigFingerprint: opts.configFingerprint,
		ImplementationRevision: opts.implementationRevision,
		DatasetSHA256:          datasetHash, ObservationSHA256: observationHash,
	}
	switch asset.ObservationKind {
	case "tool_selection":
		if asset.Domain != "tool_selection" {
			return evaluationledger.Report{}, fmt.Errorf("asset %q domain %q does not match observation kind %q", asset.ID, asset.Domain, asset.ObservationKind)
		}
		return replayToolSelection(asset, metadata, datasetContents, observationContents)
	case "evidence_gate_early_exit":
		if asset.Domain != "evidence_gate_early_exit" {
			return evaluationledger.Report{}, fmt.Errorf("asset %q domain %q does not match observation kind %q", asset.ID, asset.Domain, asset.ObservationKind)
		}
		return replayEvidenceGateEarlyExit(asset, metadata, datasetContents, observationContents)
	default:
		return evaluationledger.Report{}, fmt.Errorf("asset %q observation kind %q is not supported by the ledger replayer", asset.ID, asset.ObservationKind)
	}
}

func replayToolSelection(
	asset evaluationledger.Asset,
	metadata evaluationledger.SourceMetadata,
	datasetContents []byte,
	observationContents []byte,
) (evaluationledger.Report, error) {
	cases, err := readJSONLines(bytes.NewReader(datasetContents), "dataset", func() mesagent.ToolSelectionCase {
		return mesagent.ToolSelectionCase{}
	})
	if err != nil {
		return evaluationledger.Report{}, err
	}
	for index, definition := range cases {
		if err := definition.Validate(); err != nil {
			return evaluationledger.Report{}, fmt.Errorf("dataset item %d: %w", index, err)
		}
	}

	observations, err := readJSONLines(bytes.NewReader(observationContents), "observations", func() mesagent.ToolSelectionObservation {
		return mesagent.ToolSelectionObservation{}
	}, "usage", "durationMillis")
	if err != nil {
		return evaluationledger.Report{}, err
	}
	for index, observation := range observations {
		if err := observation.Validate(); err != nil {
			return evaluationledger.Report{}, fmt.Errorf("observations item %d: %w", index, err)
		}
	}

	return mesagent.BuildToolSelectionLedger(asset, metadata, cases, observations)
}

func replayEvidenceGateEarlyExit(
	asset evaluationledger.Asset,
	metadata evaluationledger.SourceMetadata,
	datasetContents []byte,
	observationContents []byte,
) (evaluationledger.Report, error) {
	cases, err := readJSONLines(bytes.NewReader(datasetContents), "dataset", func() mesagent.EvidenceGateEvaluationCase {
		return mesagent.EvidenceGateEvaluationCase{}
	})
	if err != nil {
		return evaluationledger.Report{}, err
	}
	for index, definition := range cases {
		if err := definition.Validate(); err != nil {
			return evaluationledger.Report{}, fmt.Errorf("dataset item %d: %w", index, err)
		}
	}
	observations, err := readJSONLines(
		bytes.NewReader(observationContents),
		"observations",
		func() mesagent.EvidenceGateEvaluationObservation { return mesagent.EvidenceGateEvaluationObservation{} },
		"usage", "durationMillis", "qualityReviewed", "earlyExitEnabled",
	)
	if err != nil {
		return evaluationledger.Report{}, err
	}
	for index, observation := range observations {
		if err := observation.Validate(); err != nil {
			return evaluationledger.Report{}, fmt.Errorf("observations item %d: %w", index, err)
		}
	}
	return mesagent.BuildEvidenceGateEarlyExitLedger(asset, metadata, cases, observations)
}

func resolveInventoryArtifact(inventoryDir, artifact string) string {
	if filepath.IsAbs(artifact) {
		return filepath.Clean(artifact)
	}
	return filepath.Clean(filepath.Join(inventoryDir, artifact))
}

func readJSONLines[T any](reader io.Reader, kind string, factory func() T, requiredFields ...string) ([]T, error) {
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	var result []T
	line := 0
	for scanner.Scan() {
		line++
		contents := bytes.TrimSpace(scanner.Bytes())
		if len(contents) == 0 {
			continue
		}
		if len(requiredFields) > 0 {
			var fields map[string]json.RawMessage
			if err := json.Unmarshal(contents, &fields); err != nil {
				return nil, fmt.Errorf("%s line %d: %w", kind, line, err)
			}
			for _, field := range requiredFields {
				value, exists := fields[field]
				if !exists || bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
					return nil, fmt.Errorf("%s line %d: required field %q is missing", kind, line, field)
				}
			}
		}
		decoder := json.NewDecoder(bytes.NewReader(contents))
		decoder.DisallowUnknownFields()
		current := factory()
		if err := decoder.Decode(&current); err != nil {
			return nil, fmt.Errorf("%s line %d: %w", kind, line, err)
		}
		var extra any
		if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
			if err == nil {
				err = errors.New("multiple JSON values")
			}
			return nil, fmt.Errorf("%s line %d: %w", kind, line, err)
		}
		result = append(result, current)
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	if len(result) == 0 {
		return nil, fmt.Errorf("%s contains no records", kind)
	}
	return result, nil
}

func readReplayArtifact(path string) ([]byte, string, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, "", err
	}
	defer file.Close()
	contents, err := io.ReadAll(io.LimitReader(file, maxReplayArtifactBytes+1))
	if err != nil {
		return nil, "", err
	}
	if len(contents) > maxReplayArtifactBytes {
		return nil, "", fmt.Errorf("artifact exceeds %d bytes", maxReplayArtifactBytes)
	}
	digest := sha256.Sum256(contents)
	return contents, fmt.Sprintf("sha256:%x", digest[:]), nil
}

func writeNewReport(path string, report evaluationledger.Report) error {
	output, err := os.CreateTemp(filepath.Dir(path), ".evaluation-ledger-*.tmp")
	if err != nil {
		return err
	}
	temporaryPath := output.Name()
	defer func() {
		_ = output.Close()
		_ = os.Remove(temporaryPath)
	}()
	if err := output.Chmod(0o600); err != nil {
		return err
	}
	encoder := json.NewEncoder(output)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(report); err != nil {
		return err
	}
	if err := output.Sync(); err != nil {
		return err
	}
	if err := output.Close(); err != nil {
		return err
	}
	if err := os.Link(temporaryPath, path); err != nil {
		return err
	}
	_ = os.Remove(temporaryPath)
	return nil
}
