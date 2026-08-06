package knowledge

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
)

type AdvancedRetrievalSearcher interface {
	Search(context.Context, uuid.UUID, string, int) (HybridSearch, error)
}

type AdvancedRetrievalRuntimeArm struct {
	Arm           AdvancedRetrievalArm
	Searcher      AdvancedRetrievalSearcher
	FTSEnabled    bool
	VectorEnabled bool
}

func (a AdvancedRetrievalRuntimeArm) validate() error {
	if err := a.Arm.Validate(); err != nil {
		return err
	}
	if a.Searcher == nil || (!a.FTSEnabled && !a.VectorEnabled) {
		return errors.New("advanced retrieval runtime arm is unavailable")
	}
	return nil
}

type AdvancedRetrievalObserver struct {
	baseline        AdvancedRetrievalRuntimeArm
	experiment      AdvancedRetrievalRuntimeArm
	documentKeyByID map[uuid.UUID]string
}

func NewAdvancedRetrievalObserver(
	baseline AdvancedRetrievalRuntimeArm,
	experiment AdvancedRetrievalRuntimeArm,
	documentKeyByID map[uuid.UUID]string,
) (*AdvancedRetrievalObserver, error) {
	if err := baseline.validate(); err != nil {
		return nil, err
	}
	if err := experiment.validate(); err != nil {
		return nil, err
	}
	if baseline.FTSEnabled != experiment.FTSEnabled || baseline.VectorEnabled != experiment.VectorEnabled {
		return nil, errors.New("advanced retrieval runtime arms change retrieval channels")
	}
	if err := validateAdvancedRetrievalArms(baseline.Arm, experiment.Arm); err != nil {
		return nil, err
	}
	keys := make(map[uuid.UUID]string, len(documentKeyByID))
	seenKeys := make(map[string]struct{}, len(documentKeyByID))
	for documentID, documentKey := range documentKeyByID {
		if documentID == uuid.Nil || !retrievalEvaluationIDPattern.MatchString(documentKey) {
			return nil, errors.New("advanced retrieval document identity map is invalid")
		}
		if _, exists := seenKeys[documentKey]; exists {
			return nil, errors.New("advanced retrieval document keys must be unique")
		}
		seenKeys[documentKey] = struct{}{}
		keys[documentID] = documentKey
	}
	if len(keys) == 0 {
		return nil, errors.New("advanced retrieval document identity map is empty")
	}
	return &AdvancedRetrievalObserver{
		baseline: baseline, experiment: experiment, documentKeyByID: keys,
	}, nil
}

func (o *AdvancedRetrievalObserver) Observe(
	ctx context.Context,
	actorID uuid.UUID,
	cases []AdvancedRetrievalEvaluationCase,
) ([]AdvancedRetrievalObservation, error) {
	if o == nil || actorID == uuid.Nil {
		return nil, errors.New("advanced retrieval observer request is invalid")
	}
	if _, _, _, err := indexAdvancedRetrievalCases(cases); err != nil {
		return nil, err
	}
	observations := make([]AdvancedRetrievalObservation, 0, len(cases)*2)
	for _, definition := range cases {
		if err := ctx.Err(); err != nil {
			return observations, err
		}
		baseline, err := o.observeArm(ctx, actorID, definition, AdvancedRetrievalBaseline, o.baseline)
		if err != nil {
			return observations, err
		}
		observations = append(observations, baseline)
		experiment, err := o.observeArm(ctx, actorID, definition, AdvancedRetrievalExperiment, o.experiment)
		if err != nil {
			return observations, err
		}
		observations = append(observations, experiment)
	}
	return observations, nil
}

func (o *AdvancedRetrievalObserver) observeArm(
	ctx context.Context,
	actorID uuid.UUID,
	definition AdvancedRetrievalEvaluationCase,
	variant AdvancedRetrievalVariant,
	arm AdvancedRetrievalRuntimeArm,
) (AdvancedRetrievalObservation, error) {
	startedAt := time.Now()
	result, searchErr := arm.Searcher.Search(ctx, actorID, definition.Query, definition.K)
	durationMillis := float64(time.Since(startedAt).Microseconds()) / 1000
	if searchErr != nil {
		if err := ctx.Err(); err != nil {
			return AdvancedRetrievalObservation{}, err
		}
		observation := newAdvancedRetrievalObservation(definition, variant, arm, durationMillis)
		observation.ErrorType = "search_failed"
		observation.DegradedChannels = []string{"search"}
		if arm.Arm.QueryMode == RetrievalQueryRewrite {
			observation.QueryRewriteStatus = queryRewriteNotObserved
		}
		if err := observation.Validate(); err != nil {
			return AdvancedRetrievalObservation{}, err
		}
		return observation, nil
	}
	observation := newAdvancedRetrievalObservation(definition, variant, arm, durationMillis)
	if err := o.populateSuccessfulObservation(&observation, result, arm); err != nil {
		return AdvancedRetrievalObservation{}, err
	}
	if err := observation.Validate(); err != nil {
		return AdvancedRetrievalObservation{}, err
	}
	return observation, nil
}

func newAdvancedRetrievalObservation(
	definition AdvancedRetrievalEvaluationCase,
	variant AdvancedRetrievalVariant,
	arm AdvancedRetrievalRuntimeArm,
	durationMillis float64,
) AdvancedRetrievalObservation {
	status := QueryRewriteDisabled
	if arm.Arm.QueryMode == RetrievalQueryRewrite {
		status = queryRewriteNotObserved
	}
	return AdvancedRetrievalObservation{
		DatasetVersion:          definition.DatasetVersion,
		CaseID:                  definition.CaseID,
		Variant:                 variant,
		RunID:                   fmt.Sprintf("%s-%s-%s", variant, definition.CaseID, uuid.NewString()),
		Query:                   definition.Query,
		K:                       definition.K,
		RetrieverVersion:        arm.Arm.RetrieverVersion,
		EmbeddingProfile:        arm.Arm.EmbeddingProfile,
		RerankProfile:           arm.Arm.RerankProfile,
		QueryMode:               arm.Arm.QueryMode,
		QueryRewriteStatus:      status,
		RewriteProvider:         arm.Arm.RewriteProvider,
		RewriteModelID:          arm.Arm.RewriteModelID,
		RewritePromptVersion:    arm.Arm.RewritePromptVersion,
		ContextMode:             arm.Arm.ContextMode,
		ContextExpansionEnabled: arm.Arm.ContextMode == RetrievalContextParent,
		DurationMillis:          durationMillis,
	}
}

func (o *AdvancedRetrievalObserver) populateSuccessfulObservation(
	observation *AdvancedRetrievalObservation,
	result HybridSearch,
	arm AdvancedRetrievalRuntimeArm,
) error {
	if err := result.QueryPlan.Validate(); err != nil {
		return fmt.Errorf("advanced retrieval result query plan: %w", err)
	}
	observation.QueryRewriteStatus = result.QueryRewriteStatus
	observation.RewriteApplied = result.QueryPlan.RewriteApplied
	observation.RewriteUsage = result.QueryRewriteUsage
	if arm.FTSEnabled {
		observation.FTSQueryCount = len(result.QueryPlan.FTSQueries())
	}
	if arm.VectorEnabled {
		observation.VectorQueryCount = len(result.QueryPlan.VectorQueries())
	}
	if arm.Arm.QueryMode == RetrievalQueryRewrite && result.QueryRewritePromptVersion != "" &&
		result.QueryRewritePromptVersion != arm.Arm.RewritePromptVersion {
		return errors.New("advanced retrieval result changed rewrite prompt version")
	}
	seenDocuments := make(map[string]struct{}, len(result.Results))
	for _, hit := range result.Results {
		if err := hit.Validate(); err != nil {
			return err
		}
		documentKey, exists := o.documentKeyByID[hit.DocumentID]
		if !exists {
			return fmt.Errorf("advanced retrieval result references unknown document %s", hit.DocumentID)
		}
		if _, exists := seenDocuments[documentKey]; !exists {
			seenDocuments[documentKey] = struct{}{}
			observation.ReturnedDocumentKeys = append(observation.ReturnedDocumentKeys, documentKey)
		}
		observation.ReturnedHitChunks = append(observation.ReturnedHitChunks, RetrievalEvaluationChunkRef{
			DocumentKey: documentKey, Ordinal: hit.Ordinal, ContentSHA256: hit.ContentSHA256,
		})
		observation.HitContextRunes += len([]rune(hit.ContentText))
	}
	for _, group := range result.ContextGroups {
		if err := group.Validate(result.Results); err != nil {
			return err
		}
		documentKey, exists := o.documentKeyByID[group.DocumentID]
		if !exists {
			return fmt.Errorf("advanced retrieval context references unknown document %s", group.DocumentID)
		}
		for _, chunk := range group.Chunks {
			observation.ReturnedContextChunks = append(observation.ReturnedContextChunks, RetrievalEvaluationChunkRef{
				DocumentKey: documentKey, Ordinal: chunk.Ordinal, ContentSHA256: chunk.ContentSHA256,
			})
			observation.ExpandedContextRunes += len([]rune(chunk.ContentText))
		}
	}
	observation.ContextExpanded = result.ContextExpanded
	observation.DegradedChannels = append([]string(nil), result.MissingChannels...)
	return nil
}
