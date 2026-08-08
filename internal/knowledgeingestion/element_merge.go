package knowledgeingestion

import (
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/chitandabb/GoAgent/internal/knowledge"
)

const elementMergeVersion = "element-merge-v1"

const minimumContainmentRunes = 32

type elementMergeDisposition string

const (
	elementMergeKeep                elementMergeDisposition = "keep"
	elementMergeSuppress            elementMergeDisposition = "suppress_duplicate"
	elementMergeSuppressNonsemantic elementMergeDisposition = "suppress_nonsemantic"
)

type elementMergeDecision struct {
	ElementIndex     int                     `json:"elementIndex"`
	Disposition      elementMergeDisposition `json:"disposition"`
	DuplicateOfIndex *int                    `json:"duplicateOfElementIndex,omitempty"`
	Reason           string                  `json:"reason"`
}

type elementMergeOutput struct {
	Version            string
	Elements           []knowledge.DocumentElement
	SearchableElements []knowledge.DocumentElement
	Decisions          []elementMergeDecision
	SuppressedCount    int
}

type SearchableElementPreparation struct {
	Version         string
	Elements        []knowledge.DocumentElement
	SuppressedCount int
}

// PrepareSearchableElements applies the same deterministic duplicate suppression
// used by the production Executor before Chunking and Embedding.
func PrepareSearchableElements(elements []knowledge.DocumentElement) (SearchableElementPreparation, error) {
	merged, err := mergeElements(elements)
	if err != nil {
		return SearchableElementPreparation{}, err
	}
	return SearchableElementPreparation{
		Version: merged.Version, Elements: append([]knowledge.DocumentElement(nil), merged.SearchableElements...),
		SuppressedCount: merged.SuppressedCount,
	}, nil
}

type mergeCandidate struct {
	element    knowledge.DocumentElement
	normalized string
	runes      int
}

func mergeElements(elements []knowledge.DocumentElement) (elementMergeOutput, error) {
	if len(elements) == 0 || len(elements) > 10_000 {
		return elementMergeOutput{}, errors.New("element merge input is required and bounded")
	}
	candidates := make([]mergeCandidate, 0, len(elements))
	decisions := make([]elementMergeDecision, len(elements))
	for index, element := range elements {
		if element.Index != index {
			return elementMergeOutput{}, errors.New("element merge requires contiguous source indexes")
		}
		if err := element.Validate(); err != nil {
			return elementMergeOutput{}, err
		}
		normalized := normalizeMergeText(element.ContentText)
		if normalized == "" {
			decisions[index] = elementMergeDecision{
				ElementIndex: index, Disposition: elementMergeSuppressNonsemantic,
				Reason: "nonsemantic_content",
			}
			continue
		}
		candidates = append(candidates, mergeCandidate{
			element: element, normalized: normalized, runes: utf8.RuneCountInString(normalized),
		})
	}
	ordered := append([]mergeCandidate(nil), candidates...)
	sort.SliceStable(ordered, func(i, j int) bool {
		leftPriority := mergePriority(ordered[i].element.ElementType)
		rightPriority := mergePriority(ordered[j].element.ElementType)
		if leftPriority != rightPriority {
			return leftPriority > rightPriority
		}
		if ordered[i].runes != ordered[j].runes {
			return ordered[i].runes > ordered[j].runes
		}
		return ordered[i].element.Index < ordered[j].element.Index
	})

	kept := make([]mergeCandidate, 0, len(elements))
	for _, candidate := range ordered {
		duplicateOf, reason := findMergeDuplicate(candidate, kept)
		decision := elementMergeDecision{
			ElementIndex: candidate.element.Index, Disposition: elementMergeKeep,
			Reason: "distinct_content",
		}
		if duplicateOf != nil {
			decision.Disposition = elementMergeSuppress
			decision.DuplicateOfIndex = duplicateOf
			decision.Reason = reason
		} else {
			kept = append(kept, candidate)
		}
		decisions[candidate.element.Index] = decision
	}

	if len(kept) == 0 {
		return elementMergeOutput{}, errors.New("element merge suppressed every element")
	}
	output := elementMergeOutput{
		Version: elementMergeVersion, Elements: make([]knowledge.DocumentElement, 0, len(elements)),
		SearchableElements: make([]knowledge.DocumentElement, 0, len(kept)), Decisions: decisions,
	}
	for index, element := range elements {
		decision := decisions[index]
		annotated, err := annotateMergeDecision(element, decision)
		if err != nil {
			return elementMergeOutput{}, err
		}
		output.Elements = append(output.Elements, annotated)
		if decision.Disposition == elementMergeKeep {
			output.SearchableElements = append(output.SearchableElements, annotated)
		} else {
			output.SuppressedCount++
		}
	}
	return output, nil
}

func findMergeDuplicate(candidate mergeCandidate, kept []mergeCandidate) (*int, string) {
	for _, existing := range kept {
		if !sameMergeLocation(candidate.element, existing.element) {
			continue
		}
		if candidate.normalized == existing.normalized {
			index := existing.element.Index
			return &index, "exact_normalized_duplicate"
		}
		if candidate.element.ElementType == knowledge.ElementOCRText &&
			(existing.element.ElementType == knowledge.ElementText || existing.element.ElementType == knowledge.ElementTable) &&
			candidate.runes >= minimumContainmentRunes && strings.Contains(existing.normalized, candidate.normalized) {
			index := existing.element.Index
			return &index, "ocr_covered_by_native"
		}
		if candidate.element.ElementType == knowledge.ElementOCRText &&
			existing.element.ElementType == knowledge.ElementOCRText &&
			candidate.runes >= minimumContainmentRunes && existing.runes >= candidate.runes &&
			float64(candidate.runes)/float64(existing.runes) >= 0.85 &&
			strings.Contains(existing.normalized, candidate.normalized) {
			index := existing.element.Index
			return &index, "overlapping_ocr_containment"
		}
	}
	return nil, ""
}

func sameMergeLocation(left, right knowledge.DocumentElement) bool {
	if left.PageNumber != nil || right.PageNumber != nil {
		return left.PageNumber != nil && right.PageNumber != nil && *left.PageNumber == *right.PageNumber
	}
	if len(left.SectionPath) != len(right.SectionPath) {
		return false
	}
	for index := range left.SectionPath {
		if left.SectionPath[index] != right.SectionPath[index] {
			return false
		}
	}
	return true
}

func mergePriority(elementType knowledge.ElementType) int {
	switch elementType {
	case knowledge.ElementTable:
		return 4
	case knowledge.ElementText:
		return 3
	case knowledge.ElementOCRText:
		return 2
	case knowledge.ElementImageDescription:
		return 1
	default:
		return 0
	}
}

func normalizeMergeText(value string) string {
	var builder strings.Builder
	for _, current := range strings.ToLower(strings.TrimSpace(value)) {
		if unicode.IsLetter(current) || unicode.IsNumber(current) {
			builder.WriteRune(current)
		}
	}
	return builder.String()
}

func annotateMergeDecision(
	element knowledge.DocumentElement,
	decision elementMergeDecision,
) (knowledge.DocumentElement, error) {
	metadata := make(map[string]any)
	if len(element.Metadata) > 0 {
		if err := json.Unmarshal(element.Metadata, &metadata); err != nil {
			return knowledge.DocumentElement{}, err
		}
	}
	metadata["mergeVersion"] = elementMergeVersion
	metadata["indexingDisposition"] = string(decision.Disposition)
	metadata["mergeReason"] = decision.Reason
	if decision.DuplicateOfIndex != nil {
		metadata["duplicateOfElementIndex"] = *decision.DuplicateOfIndex
	}
	encoded, err := json.Marshal(metadata)
	if err != nil {
		return knowledge.DocumentElement{}, err
	}
	cloned := element
	cloned.Metadata = encoded
	return cloned, cloned.Validate()
}
