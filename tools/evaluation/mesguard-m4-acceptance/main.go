// Command mesguard-m4-acceptance audits versioned evaluation assets and extracts only
// explicitly approved current evidence. It never creates a Provider client.
package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/chitandabb/GoAgent/internal/evaluationledger"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

type options struct {
	inventoryPath          string
	manifestPath           string
	runtimeConfigPath      string
	outputPath             string
	implementationRevision string
}

func run(args []string, stdout, stderr io.Writer) int {
	opts, err := parseOptions(args, stderr)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	report, err := buildReport(opts)
	if err != nil {
		fmt.Fprintf(stderr, "build M4 acceptance report: %v\n", err)
		return 1
	}
	if err := writeNewReport(opts.outputPath, report); err != nil {
		fmt.Fprintf(stderr, "write M4 acceptance report: %v\n", err)
		return 1
	}
	fmt.Fprintf(
		stdout,
		"m4_acceptance assets=%d evidence_assets=%d retest_needed=%d provider_calls=0\n",
		report.Summary.TotalAssets,
		report.Summary.CurrentEvidenceAssets,
		report.Summary.RetestNeededAssets,
	)
	return 0
}

func parseOptions(args []string, stderr io.Writer) (options, error) {
	flags := flag.NewFlagSet("mesguard-m4-acceptance", flag.ContinueOnError)
	flags.SetOutput(stderr)
	var result options
	flags.StringVar(&result.inventoryPath, "inventory", "", "versioned evaluation asset inventory JSON")
	flags.StringVar(&result.manifestPath, "manifest", "", "M4 acceptance evidence manifest JSON")
	flags.StringVar(&result.runtimeConfigPath, "runtime-config", "", "runtime configuration included in the acceptance fingerprint")
	flags.StringVar(&result.outputPath, "output", "", "new M4 acceptance report JSON; existing files are rejected")
	flags.StringVar(&result.implementationRevision, "implementation-revision", "", "git:<revision> used for the acceptance run")
	if err := flags.Parse(args); err != nil {
		return options{}, err
	}
	if flags.NArg() != 0 {
		return options{}, errors.New("positional arguments are not supported")
	}
	if result.inventoryPath == "" || result.manifestPath == "" || result.runtimeConfigPath == "" ||
		result.outputPath == "" || result.implementationRevision == "" {
		return options{}, errors.New("-inventory, -manifest, -runtime-config, -output, and -implementation-revision are required")
	}
	return result, nil
}

func buildReport(opts options) (evaluationledger.AcceptanceReport, error) {
	inventoryContents, err := os.ReadFile(opts.inventoryPath)
	if err != nil {
		return evaluationledger.AcceptanceReport{}, fmt.Errorf("read inventory: %w", err)
	}
	inventory, err := evaluationledger.ParseInventory(bytes.NewReader(inventoryContents))
	if err != nil {
		return evaluationledger.AcceptanceReport{}, err
	}
	manifestContents, err := os.ReadFile(opts.manifestPath)
	if err != nil {
		return evaluationledger.AcceptanceReport{}, fmt.Errorf("read manifest: %w", err)
	}
	manifest, err := evaluationledger.ParseAcceptanceManifest(bytes.NewReader(manifestContents))
	if err != nil {
		return evaluationledger.AcceptanceReport{}, err
	}
	runtimeConfigContents, err := os.ReadFile(opts.runtimeConfigPath)
	if err != nil {
		return evaluationledger.AcceptanceReport{}, fmt.Errorf("read runtime config: %w", err)
	}
	artifactRoot := filepath.Join(filepath.Dir(opts.inventoryPath), inventory.ArtifactRoot)
	return evaluationledger.BuildAcceptanceReport(
		inventory,
		manifest,
		artifactRoot,
		inventoryContents,
		runtimeConfigContents,
		opts.implementationRevision,
	)
}

func writeNewReport(path string, report evaluationledger.AcceptanceReport) error {
	directory := filepath.Dir(path)
	output, err := os.CreateTemp(directory, ".m4-acceptance-*.tmp")
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
	return os.Remove(temporaryPath)
}
