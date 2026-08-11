package contextgovernance

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"sort"
	"strings"
)

const maxCanonicalToolContractBytes = 1024 * 1024

var (
	machineLabelPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)
	sha256Pattern       = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

type ToolDefinition struct {
	Name        string
	Description string
	Parameters  json.RawMessage
}

type CanonicalToolContract struct {
	ModelVisibleJSON string
	Fingerprint      string
	ToolNames        []string
}

type canonicalFunctionDefinition struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters,omitempty"`
}

type canonicalToolDefinition struct {
	Type     string                      `json:"type"`
	Function canonicalFunctionDefinition `json:"function"`
}

func NewCanonicalToolContract(definitions []ToolDefinition) (CanonicalToolContract, error) {
	ordered := append([]ToolDefinition(nil), definitions...)
	for index := range ordered {
		ordered[index].Name = strings.TrimSpace(ordered[index].Name)
		ordered[index].Description = strings.TrimSpace(ordered[index].Description)
	}
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].Name < ordered[j].Name })
	tools := make([]canonicalToolDefinition, 0, len(ordered))
	names := make([]string, 0, len(ordered))
	for index, definition := range ordered {
		if !validMachineLabel(definition.Name, 128) ||
			(index > 0 && definition.Name == ordered[index-1].Name) || len(definition.Description) > 32*1024 {
			return CanonicalToolContract{}, errors.New("canonical Tool definition is invalid")
		}
		parameters, err := normalizeJSON(definition.Parameters)
		if err != nil {
			return CanonicalToolContract{}, fmt.Errorf("canonicalize Tool %q parameters: %w", definition.Name, err)
		}
		tools = append(tools, canonicalToolDefinition{
			Type: "function",
			Function: canonicalFunctionDefinition{
				Name: definition.Name, Description: definition.Description, Parameters: parameters,
			},
		})
		names = append(names, definition.Name)
	}
	encoded, err := json.Marshal(tools)
	if err != nil {
		return CanonicalToolContract{}, fmt.Errorf("encode canonical Tool contract: %w", err)
	}
	if len(encoded) > maxCanonicalToolContractBytes {
		return CanonicalToolContract{}, errors.New("canonical Tool contract exceeds one MiB")
	}
	return CanonicalToolContract{
		ModelVisibleJSON: string(encoded), Fingerprint: SHA256Hex(string(encoded)), ToolNames: names,
	}, nil
}

func normalizeJSON(value json.RawMessage) (json.RawMessage, error) {
	if len(bytes.TrimSpace(value)) == 0 {
		return nil, nil
	}
	decoder := json.NewDecoder(bytes.NewReader(value))
	decoder.UseNumber()
	var decoded any
	if err := decoder.Decode(&decoded); err != nil {
		return nil, err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return nil, errors.New("multiple JSON values are not allowed")
	}
	normalizeModelVisibleJSON(decoded)
	encoded, err := json.Marshal(decoded)
	if err != nil {
		return nil, err
	}
	return encoded, nil
}

func normalizeModelVisibleJSON(value any) {
	switch current := value.(type) {
	case map[string]any:
		for key, child := range current {
			normalizeModelVisibleJSON(child)
			if key != "required" {
				continue
			}
			items, ok := child.([]any)
			if !ok {
				continue
			}
			labels := make([]string, len(items))
			for index, item := range items {
				label, stringOK := item.(string)
				if !stringOK {
					labels = nil
					break
				}
				labels[index] = label
			}
			if labels == nil {
				continue
			}
			sort.Strings(labels)
			ordered := make([]any, len(labels))
			for index, label := range labels {
				ordered[index] = label
			}
			current[key] = ordered
		}
	case []any:
		for _, child := range current {
			normalizeModelVisibleJSON(child)
		}
	}
}

type PromptIdentityInput struct {
	ModelProfile          string
	ModelProvider         string
	ModelID               string
	SystemPromptVersion   string
	SystemPrompt          string
	ToolSchemaFingerprint string
	PreloadedSkill        string
	SummaryFingerprint    string
}

type PromptIdentity struct {
	PromptEpochID           string
	StablePrefixFingerprint string
	ModelProfileFingerprint string
	SystemPromptFingerprint string
	ToolSchemaFingerprint   string
	SkillPromptFingerprint  string
	SummaryFingerprint      string
}

func BuildPromptIdentity(input PromptIdentityInput) (PromptIdentity, error) {
	input.ModelProfile = strings.TrimSpace(input.ModelProfile)
	input.ModelProvider = strings.TrimSpace(input.ModelProvider)
	input.ModelID = strings.TrimSpace(input.ModelID)
	input.SystemPromptVersion = strings.TrimSpace(input.SystemPromptVersion)
	if !validMachineLabel(input.ModelProfile, 128) || !validMachineLabel(input.ModelProvider, 64) ||
		!validLabel(input.ModelID, 256) || !validMachineLabel(input.SystemPromptVersion, 128) ||
		!sha256Pattern.MatchString(input.ToolSchemaFingerprint) ||
		(input.SummaryFingerprint != "" && !sha256Pattern.MatchString(input.SummaryFingerprint)) {
		return PromptIdentity{}, errors.New("prompt identity input is invalid")
	}
	modelProfileBytes, err := json.Marshal(struct {
		Profile  string `json:"profile"`
		Provider string `json:"provider"`
		Model    string `json:"model"`
	}{input.ModelProfile, input.ModelProvider, input.ModelID})
	if err != nil {
		return PromptIdentity{}, err
	}
	summaryFingerprint := input.SummaryFingerprint
	if summaryFingerprint == "" {
		summaryFingerprint = SHA256Hex("")
	}
	identity := PromptIdentity{
		ModelProfileFingerprint: SHA256Hex(string(modelProfileBytes)),
		SystemPromptFingerprint: SHA256Hex(input.SystemPromptVersion + "\x00" + input.SystemPrompt),
		ToolSchemaFingerprint:   input.ToolSchemaFingerprint,
		SkillPromptFingerprint:  SHA256Hex(input.PreloadedSkill),
		SummaryFingerprint:      summaryFingerprint,
	}
	stableParts := []string{
		identity.ModelProfileFingerprint,
		identity.SystemPromptFingerprint,
		identity.ToolSchemaFingerprint,
		identity.SkillPromptFingerprint,
		identity.SummaryFingerprint,
	}
	identity.StablePrefixFingerprint = SHA256Hex(strings.Join(stableParts, "\x00"))
	identity.PromptEpochID = identity.StablePrefixFingerprint
	return identity, nil
}

func SHA256Hex(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

func IsSHA256Hex(value string) bool {
	return sha256Pattern.MatchString(value)
}

func validMachineLabel(value string, max int) bool {
	value = strings.TrimSpace(value)
	return value != "" && len(value) <= max && machineLabelPattern.MatchString(value)
}

func validLabel(value string, max int) bool {
	value = strings.TrimSpace(value)
	return value != "" && len(value) <= max && !strings.ContainsAny(value, "\r\n\x00")
}
