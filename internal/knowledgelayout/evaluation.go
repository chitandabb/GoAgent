package knowledgelayout

import (
	"errors"
	"fmt"
	"math"
	"regexp"
	"sort"
	"strings"
)

var evaluationIDPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,127}$`)

type RoutingEvaluationRegion struct {
	RegionType RegionType      `json:"regionType"`
	Box        BoundingBox     `json:"box"`
	Route      ProcessingRoute `json:"route"`
}

func (r RoutingEvaluationRegion) Validate() error {
	if r.RegionType != RegionTable && r.RegionType != RegionPicture && r.RegionType != RegionFormula {
		return errors.New("routing evaluation high-value region type is invalid")
	}
	if r.Route != RouteTableRecovery && r.Route != RouteCloudVision {
		return errors.New("routing evaluation high-value region route is invalid")
	}
	return r.Box.Validate()
}

type RoutingEvaluationCase struct {
	DatasetVersion    string                    `json:"datasetVersion"`
	CaseID            string                    `json:"caseId"`
	DocumentID        string                    `json:"documentId"`
	PageNumber        int                       `json:"pageNumber"`
	ExpectedPageClass PageClass                 `json:"expectedPageClass"`
	ExpectedRoutes    []ProcessingRoute         `json:"expectedRoutes"`
	HighValueRegions  []RoutingEvaluationRegion `json:"highValueRegions,omitempty"`
	AnnotationNotes   string                    `json:"annotationNotes,omitempty"`
}

func (c RoutingEvaluationCase) Validate() error {
	if !evaluationIDPattern.MatchString(c.DatasetVersion) || !evaluationIDPattern.MatchString(c.CaseID) ||
		!evaluationIDPattern.MatchString(c.DocumentID) || c.PageNumber < 1 || !c.ExpectedPageClass.Valid() {
		return errors.New("routing evaluation case identity is invalid")
	}
	if len(c.ExpectedRoutes) == 0 || len(c.ExpectedRoutes) > 5 {
		return errors.New("routing evaluation expected routes are invalid")
	}
	seen := make(map[ProcessingRoute]struct{}, len(c.ExpectedRoutes))
	for _, route := range c.ExpectedRoutes {
		if !route.Valid() {
			return errors.New("routing evaluation expected route is invalid")
		}
		if _, exists := seen[route]; exists {
			return errors.New("routing evaluation expected routes must be unique")
		}
		seen[route] = struct{}{}
	}
	if len(c.HighValueRegions) > 32 || len([]rune(c.AnnotationNotes)) > 1000 {
		return errors.New("routing evaluation annotations are not bounded")
	}
	for _, region := range c.HighValueRegions {
		if err := region.Validate(); err != nil {
			return err
		}
		if _, exists := seen[region.Route]; !exists {
			return errors.New("high-value region route is absent from expected routes")
		}
	}
	return nil
}

type RoutingEvaluationMatch struct {
	ExpectedIndex  int     `json:"expectedIndex"`
	Matched        bool    `json:"matched"`
	BestIoU        float64 `json:"bestIoU"`
	MatchedOrdinal *int    `json:"matchedOrdinal,omitempty"`
}

type RoutingEvaluationObservation struct {
	DatasetVersion             string                   `json:"datasetVersion"`
	CaseID                     string                   `json:"caseId"`
	RunID                      string                   `json:"runId"`
	Model                      *ModelTrace              `json:"model,omitempty"`
	PredictedPageClass         PageClass                `json:"predictedPageClass"`
	PredictedRoutes            []ProcessingRoute        `json:"predictedRoutes"`
	Regions                    []RegionRoute            `json:"regions"`
	Fallback                   bool                     `json:"fallback"`
	Partial                    bool                     `json:"partial"`
	Rendered                   bool                     `json:"rendered"`
	RequestedDPI               int                      `json:"requestedDpi,omitempty"`
	EffectiveDPI               int                      `json:"effectiveDpi,omitempty"`
	RasterWidth                int                      `json:"rasterWidth,omitempty"`
	RasterHeight               int                      `json:"rasterHeight,omitempty"`
	RasterBytes                int                      `json:"rasterBytes,omitempty"`
	HighValueMatches           []RoutingEvaluationMatch `json:"highValueMatches,omitempty"`
	DurationMillis             float64                  `json:"durationMillis"`
	TotalAllocatedBytes        uint64                   `json:"totalAllocatedBytes"`
	PeakHeapAllocatedBytes     uint64                   `json:"peakHeapAllocatedBytes"`
	BaselineCloudBoundRegions  int                      `json:"baselineCloudBoundRegions"`
	CandidateCloudBoundRegions int                      `json:"candidateCloudBoundRegions"`
	CandidateCloudBoundPage    bool                     `json:"candidateCloudBoundPage"`
}

func NewRoutingEvaluationObservation(
	definition RoutingEvaluationCase,
	runID string,
	plan Plan,
	render *RenderResult,
	durationMillis float64,
	totalAllocatedBytes uint64,
	peakHeapAllocatedBytes uint64,
	minimumIoU float64,
) (RoutingEvaluationObservation, error) {
	if err := definition.Validate(); err != nil {
		return RoutingEvaluationObservation{}, err
	}
	if strings.TrimSpace(runID) == "" || len(runID) > 256 || durationMillis < 0 ||
		math.IsNaN(durationMillis) || math.IsInf(durationMillis, 0) ||
		minimumIoU <= 0 || minimumIoU > 1 || math.IsNaN(minimumIoU) || math.IsInf(minimumIoU, 0) {
		return RoutingEvaluationObservation{}, errors.New("routing evaluation observation metadata is invalid")
	}
	if err := plan.Validate(); err != nil {
		return RoutingEvaluationObservation{}, err
	}
	predictedRoutes := uniqueRoutes(plan.Routes)
	cloudRegions := 0
	for _, route := range plan.Routes {
		if isCloudBoundRoute(route.Route) {
			cloudRegions++
		}
	}
	observation := RoutingEvaluationObservation{
		DatasetVersion: definition.DatasetVersion, CaseID: definition.CaseID, RunID: runID,
		Model: cloneOptionalModelTrace(plan.Model), PredictedPageClass: plan.PageClass,
		PredictedRoutes: predictedRoutes, Regions: append([]RegionRoute(nil), plan.Routes...),
		Fallback: plan.Fallback, Partial: plan.Partial, DurationMillis: durationMillis,
		TotalAllocatedBytes: totalAllocatedBytes, PeakHeapAllocatedBytes: peakHeapAllocatedBytes,
		BaselineCloudBoundRegions: len(plan.Routes), CandidateCloudBoundRegions: cloudRegions,
		CandidateCloudBoundPage: cloudRegions > 0,
	}
	if render != nil {
		observation.Rendered = true
		observation.RequestedDPI = render.RequestedDPI
		observation.EffectiveDPI = render.DPI
		observation.RasterWidth = render.Raster.Width
		observation.RasterHeight = render.Raster.Height
		observation.RasterBytes = len(render.Raster.Content)
	}
	observation.HighValueMatches = matchHighValueRegions(definition.HighValueRegions, plan.Routes, minimumIoU)
	return observation, observation.Validate(definition)
}

func (o RoutingEvaluationObservation) Validate(definition RoutingEvaluationCase) error {
	if err := definition.Validate(); err != nil {
		return err
	}
	if o.DatasetVersion != definition.DatasetVersion || o.CaseID != definition.CaseID ||
		strings.TrimSpace(o.RunID) == "" || !o.PredictedPageClass.Valid() || o.DurationMillis < 0 ||
		math.IsNaN(o.DurationMillis) || math.IsInf(o.DurationMillis, 0) ||
		o.BaselineCloudBoundRegions != len(o.Regions) ||
		o.CandidateCloudBoundRegions < 0 || o.CandidateCloudBoundRegions > len(o.Regions) ||
		o.CandidateCloudBoundPage != (o.CandidateCloudBoundRegions > 0) {
		return errors.New("routing evaluation observation is inconsistent")
	}
	if len(o.PredictedRoutes) == 0 || len(o.HighValueMatches) != len(definition.HighValueRegions) {
		return errors.New("routing evaluation observation predictions are incomplete")
	}
	if o.Rendered {
		if o.RequestedDPI < 72 || o.RequestedDPI > 600 || o.EffectiveDPI < 72 ||
			o.EffectiveDPI > o.RequestedDPI || o.RasterWidth < 1 || o.RasterHeight < 1 || o.RasterBytes < 1 {
			return errors.New("routing evaluation render trace is invalid")
		}
	} else if o.RequestedDPI != 0 || o.EffectiveDPI != 0 || o.RasterWidth != 0 ||
		o.RasterHeight != 0 || o.RasterBytes != 0 {
		return errors.New("routing evaluation absent render trace is inconsistent")
	}
	for _, route := range o.PredictedRoutes {
		if !route.Valid() {
			return errors.New("routing evaluation predicted route is invalid")
		}
	}
	for index, region := range o.Regions {
		if region.Ordinal != index {
			return errors.New("routing evaluation predicted region ordinals are invalid")
		}
		if err := region.Validate(); err != nil {
			return err
		}
	}
	for index, match := range o.HighValueMatches {
		if match.ExpectedIndex != index || match.BestIoU < 0 || match.BestIoU > 1 ||
			math.IsNaN(match.BestIoU) || math.IsInf(match.BestIoU, 0) || match.Matched != (match.MatchedOrdinal != nil) {
			return errors.New("routing evaluation high-value match is invalid")
		}
		if match.MatchedOrdinal != nil && (*match.MatchedOrdinal < 0 || *match.MatchedOrdinal >= len(o.Regions)) {
			return errors.New("routing evaluation matched region ordinal is invalid")
		}
	}
	return nil
}

type RoutingEvaluationSummary struct {
	DatasetVersion                string  `json:"datasetVersion"`
	EvaluatorVersion              string  `json:"evaluatorVersion"`
	MinimumRegionIoU              float64 `json:"minimumRegionIoU"`
	Cases                         int     `json:"cases"`
	PageClassMacroF1              float64 `json:"pageClassMacroF1"`
	RouteMacroF1                  float64 `json:"routeMacroF1"`
	ActionableRouteMacroF1        float64 `json:"actionableRouteMacroF1"`
	HighValueRegions              int     `json:"highValueRegions"`
	HighValueRegionsMissed        int     `json:"highValueRegionsMissed"`
	HighValueVisualMissRate       float64 `json:"highValueVisualMissRate"`
	BaselineCloudBoundPages       int     `json:"baselineCloudBoundPages"`
	CandidateCloudBoundPages      int     `json:"candidateCloudBoundPages"`
	AvoidedCloudBoundPages        int     `json:"avoidedCloudBoundPages"`
	CloudBoundPageAvoidanceRate   float64 `json:"cloudBoundPageAvoidanceRate"`
	BaselineCloudBoundRegions     int     `json:"baselineCloudBoundRegions"`
	CandidateCloudBoundRegions    int     `json:"candidateCloudBoundRegions"`
	AvoidedCloudBoundRegions      int     `json:"avoidedCloudBoundRegions"`
	CloudBoundRegionAvoidanceRate float64 `json:"cloudBoundRegionAvoidanceRate"`
	AveragePageDurationMillis     float64 `json:"averagePageDurationMillis"`
	P50PageDurationMillis         float64 `json:"p50PageDurationMillis"`
	P95PageDurationMillis         float64 `json:"p95PageDurationMillis"`
	TotalAllocatedBytes           uint64  `json:"totalAllocatedBytes"`
	PeakHeapAllocatedBytes        uint64  `json:"peakHeapAllocatedBytes"`
	RequestedRenderDPI            int     `json:"requestedRenderDpi"`
	MaxRasterPixels               int64   `json:"maxRasterPixels"`
	MaxRasterBytes                int64   `json:"maxRasterBytes"`
	CorpusVerificationMillis      float64 `json:"corpusVerificationMillis"`
	RuntimeInitializationMillis   float64 `json:"runtimeInitializationMillis"`
	ParserDurationMillis          float64 `json:"parserDurationMillis"`
	ParsedDocuments               int     `json:"parsedDocuments"`
}

func EvaluateRouting(
	cases []RoutingEvaluationCase,
	observations []RoutingEvaluationObservation,
	evaluatorVersion string,
	minimumIoU float64,
) (RoutingEvaluationSummary, error) {
	if len(cases) == 0 || len(observations) != len(cases) || strings.TrimSpace(evaluatorVersion) == "" ||
		minimumIoU <= 0 || minimumIoU > 1 {
		return RoutingEvaluationSummary{}, errors.New("routing evaluation inputs are incomplete")
	}
	definitions := make(map[string]RoutingEvaluationCase, len(cases))
	version := ""
	for index, definition := range cases {
		if err := definition.Validate(); err != nil {
			return RoutingEvaluationSummary{}, fmt.Errorf("case %d: %w", index, err)
		}
		if version == "" {
			version = definition.DatasetVersion
		} else if definition.DatasetVersion != version {
			return RoutingEvaluationSummary{}, errors.New("routing evaluation mixes dataset versions")
		}
		if _, exists := definitions[definition.CaseID]; exists {
			return RoutingEvaluationSummary{}, fmt.Errorf("duplicate routing evaluation case %q", definition.CaseID)
		}
		definitions[definition.CaseID] = definition
	}

	expectedClasses := make([]string, 0, len(cases))
	predictedClasses := make([]string, 0, len(cases))
	expectedRouteSets := make([]map[string]struct{}, 0, len(cases))
	predictedRouteSets := make([]map[string]struct{}, 0, len(cases))
	durations := make([]float64, 0, len(cases))
	seen := make(map[string]struct{}, len(observations))
	summary := RoutingEvaluationSummary{
		DatasetVersion: version, EvaluatorVersion: evaluatorVersion, MinimumRegionIoU: minimumIoU,
		Cases: len(cases), BaselineCloudBoundPages: len(cases),
	}
	for index, observation := range observations {
		definition, exists := definitions[observation.CaseID]
		if !exists {
			return RoutingEvaluationSummary{}, fmt.Errorf("observation %d references an unknown case", index)
		}
		if _, exists := seen[observation.CaseID]; exists {
			return RoutingEvaluationSummary{}, fmt.Errorf("duplicate observation for case %q", observation.CaseID)
		}
		seen[observation.CaseID] = struct{}{}
		if err := observation.Validate(definition); err != nil {
			return RoutingEvaluationSummary{}, fmt.Errorf("observation %d: %w", index, err)
		}
		expectedClasses = append(expectedClasses, string(definition.ExpectedPageClass))
		predictedClasses = append(predictedClasses, string(observation.PredictedPageClass))
		expectedRouteSets = append(expectedRouteSets, routeSet(definition.ExpectedRoutes))
		predictedRouteSets = append(predictedRouteSets, routeSet(observation.PredictedRoutes))
		for _, match := range observation.HighValueMatches {
			summary.HighValueRegions++
			if !match.Matched {
				summary.HighValueRegionsMissed++
			}
		}
		if observation.CandidateCloudBoundPage {
			summary.CandidateCloudBoundPages++
		}
		summary.BaselineCloudBoundRegions += observation.BaselineCloudBoundRegions
		summary.CandidateCloudBoundRegions += observation.CandidateCloudBoundRegions
		summary.TotalAllocatedBytes += observation.TotalAllocatedBytes
		if observation.PeakHeapAllocatedBytes > summary.PeakHeapAllocatedBytes {
			summary.PeakHeapAllocatedBytes = observation.PeakHeapAllocatedBytes
		}
		durations = append(durations, observation.DurationMillis)
	}
	summary.PageClassMacroF1 = macroF1Single(expectedClasses, predictedClasses)
	summary.RouteMacroF1 = macroF1Sets(expectedRouteSets, predictedRouteSets)
	summary.ActionableRouteMacroF1 = macroF1SetsFiltered(
		expectedRouteSets, predictedRouteSets, map[string]struct{}{
			string(RouteNativeText): {}, string(RouteCloudOCR): {},
			string(RouteTableRecovery): {}, string(RouteCloudVision): {},
		},
	)
	if summary.HighValueRegions > 0 {
		summary.HighValueVisualMissRate = float64(summary.HighValueRegionsMissed) / float64(summary.HighValueRegions)
	}
	summary.AvoidedCloudBoundPages = summary.BaselineCloudBoundPages - summary.CandidateCloudBoundPages
	summary.CloudBoundPageAvoidanceRate = float64(summary.AvoidedCloudBoundPages) / float64(summary.BaselineCloudBoundPages)
	summary.AvoidedCloudBoundRegions = summary.BaselineCloudBoundRegions - summary.CandidateCloudBoundRegions
	if summary.BaselineCloudBoundRegions > 0 {
		summary.CloudBoundRegionAvoidanceRate = float64(summary.AvoidedCloudBoundRegions) /
			float64(summary.BaselineCloudBoundRegions)
	}
	sort.Float64s(durations)
	for _, duration := range durations {
		summary.AveragePageDurationMillis += duration
	}
	summary.AveragePageDurationMillis /= float64(len(durations))
	summary.P50PageDurationMillis = percentile(durations, 0.50)
	summary.P95PageDurationMillis = percentile(durations, 0.95)
	return summary, nil
}

func matchHighValueRegions(
	expected []RoutingEvaluationRegion,
	predicted []RegionRoute,
	minimumIoU float64,
) []RoutingEvaluationMatch {
	matches := make([]RoutingEvaluationMatch, 0, len(expected))
	used := make(map[int]struct{}, len(expected))
	for expectedIndex, target := range expected {
		bestOrdinal := -1
		bestIoU := 0.0
		for _, candidate := range predicted {
			if _, exists := used[candidate.Ordinal]; exists || candidate.RegionType != target.RegionType ||
				candidate.Route != target.Route {
				continue
			}
			value := boxIoU(target.Box, candidate.Box)
			if value > bestIoU {
				bestIoU = value
				bestOrdinal = candidate.Ordinal
			}
		}
		match := RoutingEvaluationMatch{ExpectedIndex: expectedIndex, BestIoU: bestIoU}
		if bestOrdinal >= 0 && bestIoU >= minimumIoU {
			match.Matched = true
			ordinal := bestOrdinal
			match.MatchedOrdinal = &ordinal
			used[bestOrdinal] = struct{}{}
		}
		matches = append(matches, match)
	}
	return matches
}

func boxIoU(left, right BoundingBox) float64 {
	intersectionWidth := math.Max(0, math.Min(left.Right, right.Right)-math.Max(left.Left, right.Left))
	intersectionHeight := math.Max(0, math.Min(left.Bottom, right.Bottom)-math.Max(left.Top, right.Top))
	intersection := intersectionWidth * intersectionHeight
	leftArea := (left.Right - left.Left) * (left.Bottom - left.Top)
	rightArea := (right.Right - right.Left) * (right.Bottom - right.Top)
	union := leftArea + rightArea - intersection
	if union <= 0 {
		return 0
	}
	return intersection / union
}

func uniqueRoutes(regions []RegionRoute) []ProcessingRoute {
	seen := make(map[ProcessingRoute]struct{}, len(regions))
	for _, region := range regions {
		seen[region.Route] = struct{}{}
	}
	result := make([]ProcessingRoute, 0, len(seen))
	for _, route := range []ProcessingRoute{
		RouteNativeText, RouteCloudOCR, RouteTableRecovery, RouteCloudVision, RouteSkip,
	} {
		if _, exists := seen[route]; exists {
			result = append(result, route)
		}
	}
	return result
}

func isCloudBoundRoute(route ProcessingRoute) bool {
	return route == RouteCloudOCR || route == RouteTableRecovery || route == RouteCloudVision
}

func cloneOptionalModelTrace(model *ModelTrace) *ModelTrace {
	if model == nil {
		return nil
	}
	cloned := *model
	return &cloned
}

func routeSet(routes []ProcessingRoute) map[string]struct{} {
	result := make(map[string]struct{}, len(routes))
	for _, route := range routes {
		result[string(route)] = struct{}{}
	}
	return result
}

func macroF1Single(expected, predicted []string) float64 {
	expectedSets := make([]map[string]struct{}, 0, len(expected))
	predictedSets := make([]map[string]struct{}, 0, len(predicted))
	for index := range expected {
		expectedSets = append(expectedSets, map[string]struct{}{expected[index]: {}})
		predictedSets = append(predictedSets, map[string]struct{}{predicted[index]: {}})
	}
	return macroF1Sets(expectedSets, predictedSets)
}

func macroF1Sets(expected, predicted []map[string]struct{}) float64 {
	labels := make(map[string]struct{})
	for index := range expected {
		for label := range expected[index] {
			labels[label] = struct{}{}
		}
		for label := range predicted[index] {
			labels[label] = struct{}{}
		}
	}
	if len(labels) == 0 {
		return 0
	}
	total := 0.0
	for label := range labels {
		tp, fp, fn := 0, 0, 0
		for index := range expected {
			_, wanted := expected[index][label]
			_, got := predicted[index][label]
			switch {
			case wanted && got:
				tp++
			case got:
				fp++
			case wanted:
				fn++
			}
		}
		denominator := 2*tp + fp + fn
		if denominator > 0 {
			total += float64(2*tp) / float64(denominator)
		}
	}
	return total / float64(len(labels))
}

func macroF1SetsFiltered(
	expected, predicted []map[string]struct{},
	labels map[string]struct{},
) float64 {
	filteredExpected := make([]map[string]struct{}, len(expected))
	filteredPredicted := make([]map[string]struct{}, len(predicted))
	for index := range expected {
		filteredExpected[index] = filterLabels(expected[index], labels)
		filteredPredicted[index] = filterLabels(predicted[index], labels)
	}
	return macroF1Sets(filteredExpected, filteredPredicted)
}

func filterLabels(values, allowed map[string]struct{}) map[string]struct{} {
	result := make(map[string]struct{}, len(values))
	for value := range values {
		if _, exists := allowed[value]; exists {
			result[value] = struct{}{}
		}
	}
	return result
}

func percentile(sorted []float64, quantile float64) float64 {
	if len(sorted) == 0 {
		return 0
	}
	index := int(math.Ceil(quantile*float64(len(sorted)))) - 1
	if index < 0 {
		index = 0
	}
	return sorted[index]
}
