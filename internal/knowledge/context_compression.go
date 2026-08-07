package knowledge

import (
	"errors"
	"math"
	"sort"
	"strings"

	"github.com/google/uuid"
)

// ContextCompressionConfig bounds parent-context expansion without rewriting
// evidence text. Selected chunks therefore retain their original content hash.
type ContextCompressionConfig struct {
	Enabled   bool
	MaxChunks int
	MaxRunes  int
	MinScore  float64
}

func (c ContextCompressionConfig) Validate() error {
	if !c.Enabled {
		return nil
	}
	if c.MaxChunks < 1 || c.MaxChunks > 40 {
		return errors.New("knowledge context compression maxChunks must be between 1 and 40")
	}
	if c.MaxRunes < 128 || c.MaxRunes > 32_000 {
		return errors.New("knowledge context compression maxRunes must be between 128 and 32000")
	}
	if math.IsNaN(c.MinScore) || math.IsInf(c.MinScore, 0) || c.MinScore < 0 || c.MinScore > 1 {
		return errors.New("knowledge context compression minScore must be between 0 and 1")
	}
	return nil
}

type ContextCompressionStats struct {
	InputChunks   int `json:"inputChunks"`
	OutputChunks  int `json:"outputChunks"`
	InputRunes    int `json:"inputRunes"`
	OutputRunes   int `json:"outputRunes"`
	OmittedChunks int `json:"omittedChunks"`
}

func (s ContextCompressionStats) Validate() error {
	if s.InputChunks < 0 || s.OutputChunks < 0 || s.InputRunes < 0 || s.OutputRunes < 0 ||
		s.OutputChunks > s.InputChunks || s.OutputRunes > s.InputRunes ||
		s.OmittedChunks != s.InputChunks-s.OutputChunks {
		return errors.New("knowledge context compression stats are invalid")
	}
	return nil
}

type contextCompressionCandidate struct {
	groupIndex int
	chunk      SearchContextChunk
	score      float64
	runes      int
}

// CompressSearchContext selects complete, traceable context chunks under one
// global budget. It never truncates or summarizes chunk text.
func CompressSearchContext(
	plan QueryPlan,
	hits []SearchResult,
	groups []SearchContextGroup,
	config ContextCompressionConfig,
) ([]SearchContextGroup, ContextCompressionStats, error) {
	if err := config.Validate(); err != nil {
		return nil, ContextCompressionStats{}, err
	}
	if !config.Enabled || len(groups) == 0 {
		return cloneSearchContextGroups(groups), ContextCompressionStats{}, nil
	}
	if err := plan.Validate(); err != nil {
		return nil, ContextCompressionStats{}, err
	}
	for _, group := range groups {
		if err := group.Validate(hits); err != nil {
			return nil, ContextCompressionStats{}, err
		}
	}

	stats := ContextCompressionStats{}
	hitRank := make(map[uuid.UUID]int, len(hits))
	for index, hit := range hits {
		hitRank[hit.ChunkID] = index + 1
	}
	queryTokens := contextCompressionQueryTokens(plan)
	protectedSignals := ProtectedQuerySignals(plan.OriginalQuery)
	candidatesByHash := make(map[string]contextCompressionCandidate)
	groupPriority := make([]int, len(groups))
	for groupIndex, group := range groups {
		groupPriority[groupIndex] = groupIndex
		hitOrdinals := make([]int, 0, len(group.HitChunkIDs))
		bestRank := len(hits) + 1
		for _, hitID := range group.HitChunkIDs {
			rank := hitRank[hitID]
			if rank > 0 && rank < bestRank {
				bestRank = rank
			}
			for _, hit := range hits {
				if hit.ChunkID == hitID {
					hitOrdinals = append(hitOrdinals, hit.Ordinal)
					break
				}
			}
		}
		for _, chunk := range group.Chunks {
			chunkRunes := len([]rune(chunk.ContentText))
			stats.InputChunks++
			stats.InputRunes += chunkRunes
			candidate := contextCompressionCandidate{
				groupIndex: groupIndex,
				chunk:      chunk,
				runes:      chunkRunes,
				score: contextCompressionScore(
					chunk, group.SectionPath, queryTokens, protectedSignals,
					nearestContextOrdinalDistance(chunk.Ordinal, hitOrdinals), bestRank,
				),
			}
			key := chunk.ContentSHA256
			current, exists := candidatesByHash[key]
			if !exists || contextCompressionCandidateLess(candidate, current) {
				candidatesByHash[key] = candidate
			}
		}
	}

	candidates := make([]contextCompressionCandidate, 0, len(candidatesByHash))
	groupCandidates := make(map[int][]contextCompressionCandidate, len(groups))
	for _, candidate := range candidatesByHash {
		candidates = append(candidates, candidate)
		groupCandidates[candidate.groupIndex] = append(groupCandidates[candidate.groupIndex], candidate)
	}
	sort.SliceStable(candidates, func(left, right int) bool {
		return contextCompressionCandidateLess(candidates[left], candidates[right])
	})
	for groupIndex := range groupCandidates {
		items := groupCandidates[groupIndex]
		sort.SliceStable(items, func(left, right int) bool {
			return contextCompressionCandidateLess(items[left], items[right])
		})
		groupCandidates[groupIndex] = items
	}
	sort.SliceStable(groupPriority, func(left, right int) bool {
		return contextCompressionGroupRank(groups[groupPriority[left]], hitRank) <
			contextCompressionGroupRank(groups[groupPriority[right]], hitRank)
	})

	selected := make(map[uuid.UUID]contextCompressionCandidate)
	usedRunes := 0
	selectCandidate := func(candidate contextCompressionCandidate) bool {
		if candidate.score < config.MinScore || len(selected) >= config.MaxChunks ||
			usedRunes+candidate.runes > config.MaxRunes {
			return false
		}
		if _, exists := selected[candidate.chunk.ChunkID]; exists {
			return false
		}
		selected[candidate.chunk.ChunkID] = candidate
		usedRunes += candidate.runes
		return true
	}
	// First offer each relevant group one slot, ordered by its best hit rank.
	for _, groupIndex := range groupPriority {
		for _, candidate := range groupCandidates[groupIndex] {
			if selectCandidate(candidate) {
				break
			}
		}
		if len(selected) >= config.MaxChunks {
			break
		}
	}
	for _, candidate := range candidates {
		if len(selected) >= config.MaxChunks {
			break
		}
		selectCandidate(candidate)
	}

	compressed := make([]SearchContextGroup, 0, len(groups))
	for groupIndex, group := range groups {
		chunks := make([]SearchContextChunk, 0, len(group.Chunks))
		for _, chunk := range group.Chunks {
			candidate, exists := selected[chunk.ChunkID]
			if !exists || candidate.groupIndex != groupIndex {
				continue
			}
			chunks = append(chunks, chunk)
			stats.OutputChunks++
			stats.OutputRunes += candidate.runes
		}
		if len(chunks) == 0 {
			continue
		}
		copy := group
		copy.SectionPath = append([]string(nil), group.SectionPath...)
		copy.HitChunkIDs = append([]uuid.UUID(nil), group.HitChunkIDs...)
		copy.Chunks = chunks
		copy.Truncated = group.Truncated || len(chunks) < len(group.Chunks)
		compressed = append(compressed, copy)
	}
	stats.OmittedChunks = stats.InputChunks - stats.OutputChunks
	if err := stats.Validate(); err != nil {
		return nil, ContextCompressionStats{}, err
	}
	return compressed, stats, nil
}

func contextCompressionQueryTokens(plan QueryPlan) map[string]struct{} {
	values := []string{plan.OriginalQuery, plan.LexicalQuery, plan.SemanticQuery}
	values = append(values, plan.Subqueries...)
	return contextCompressionTokens(strings.Join(values, " "))
}

func contextCompressionTokens(value string) map[string]struct{} {
	tokens := make(map[string]struct{})
	for _, token := range strings.Fields(NormalizeSearchText(value)) {
		tokens[token] = struct{}{}
	}
	return tokens
}

func contextCompressionScore(
	chunk SearchContextChunk,
	sectionPath []string,
	queryTokens map[string]struct{},
	protectedSignals []string,
	distance int,
	hitRank int,
) float64 {
	contentTokens := contextCompressionTokens(chunk.ContentText)
	lexicalCoverage := contextCompressionCoverage(queryTokens, contentTokens)
	sectionCoverage := contextCompressionCoverage(queryTokens, contextCompressionTokens(strings.Join(sectionPath, " ")))
	proximity := 1 / float64(1+distance)
	rankPriority := 0.0
	if hitRank > 0 {
		rankPriority = 1 / float64(hitRank)
	}
	if len(protectedSignals) == 0 {
		return 0.55*lexicalCoverage + 0.20*proximity + 0.15*rankPriority + 0.10*sectionCoverage
	}
	contentLower := strings.ToLower(chunk.ContentText)
	protectedMatches := 0
	for _, signal := range protectedSignals {
		if strings.Contains(contentLower, strings.ToLower(signal)) {
			protectedMatches++
		}
	}
	protectedCoverage := float64(protectedMatches) / float64(len(protectedSignals))
	return 0.45*lexicalCoverage + 0.20*protectedCoverage + 0.20*proximity +
		0.10*rankPriority + 0.05*sectionCoverage
}

func contextCompressionCoverage(expected, actual map[string]struct{}) float64 {
	if len(expected) == 0 {
		return 0
	}
	matches := 0
	for token := range expected {
		if _, exists := actual[token]; exists {
			matches++
		}
	}
	return float64(matches) / float64(len(expected))
}

func contextCompressionCandidateLess(left, right contextCompressionCandidate) bool {
	if left.score != right.score {
		return left.score > right.score
	}
	if left.groupIndex != right.groupIndex {
		return left.groupIndex < right.groupIndex
	}
	if left.chunk.Ordinal != right.chunk.Ordinal {
		return left.chunk.Ordinal < right.chunk.Ordinal
	}
	return left.chunk.ChunkID.String() < right.chunk.ChunkID.String()
}

func contextCompressionGroupRank(group SearchContextGroup, hitRank map[uuid.UUID]int) int {
	best := len(hitRank) + 1
	for _, hitID := range group.HitChunkIDs {
		if rank := hitRank[hitID]; rank > 0 && rank < best {
			best = rank
		}
	}
	return best
}

func nearestContextOrdinalDistance(ordinal int, hits []int) int {
	best := math.MaxInt
	for _, hit := range hits {
		distance := ordinal - hit
		if distance < 0 {
			distance = -distance
		}
		if distance < best {
			best = distance
		}
	}
	if best == math.MaxInt {
		return 1
	}
	return best
}

func cloneSearchContextGroups(groups []SearchContextGroup) []SearchContextGroup {
	cloned := make([]SearchContextGroup, 0, len(groups))
	for _, group := range groups {
		copy := group
		copy.SectionPath = append([]string(nil), group.SectionPath...)
		copy.HitChunkIDs = append([]uuid.UUID(nil), group.HitChunkIDs...)
		copy.Chunks = append([]SearchContextChunk(nil), group.Chunks...)
		cloned = append(cloned, copy)
	}
	return cloned
}
