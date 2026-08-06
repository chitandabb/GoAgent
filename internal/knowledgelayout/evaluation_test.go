package knowledgelayout

import "testing"

func TestEvaluateRoutingPerfectRun(t *testing.T) {
	cases := []RoutingEvaluationCase{
		{
			DatasetVersion: "layout-v1", CaseID: "mixed-table", DocumentID: "doc-a", PageNumber: 1,
			ExpectedPageClass: PageMixed, ExpectedRoutes: []ProcessingRoute{RouteNativeText, RouteTableRecovery},
			HighValueRegions: []RoutingEvaluationRegion{{
				RegionType: RegionTable,
				Box:        BoundingBox{Left: 0.1, Top: 0.2, Right: 0.9, Bottom: 0.5},
				Route:      RouteTableRecovery,
			}},
		},
		{
			DatasetVersion: "layout-v1", CaseID: "scanned-text", DocumentID: "doc-b", PageNumber: 2,
			ExpectedPageClass: PageScanned, ExpectedRoutes: []ProcessingRoute{RouteCloudOCR},
		},
	}
	plans := []Plan{
		testEvaluationPlan(PageMixed,
			testEvaluationRoute(0, RegionText, RouteNativeText, FullPageBox()),
			testEvaluationRoute(1, RegionTable, RouteTableRecovery,
				BoundingBox{Left: 0.12, Top: 0.2, Right: 0.88, Bottom: 0.5}),
		),
		testEvaluationPlan(PageScanned,
			testEvaluationRoute(0, RegionText, RouteCloudOCR, FullPageBox()),
		),
	}
	observations := make([]RoutingEvaluationObservation, 0, len(cases))
	for index := range cases {
		observation, err := NewRoutingEvaluationObservation(
			cases[index], cases[index].CaseID+"-run", plans[index], nil, float64(index+1), 100, 1000, 0.5,
		)
		if err != nil {
			t.Fatalf("observation %d: %v", index, err)
		}
		observations = append(observations, observation)
	}
	summary, err := EvaluateRouting(cases, observations, "layout-eval-v1", 0.5)
	if err != nil {
		t.Fatal(err)
	}
	if summary.PageClassMacroF1 != 1 || summary.RouteMacroF1 != 1 || summary.ActionableRouteMacroF1 != 1 ||
		summary.HighValueVisualMissRate != 0 || summary.HighValueRegions != 1 ||
		summary.BaselineCloudBoundRegions != 3 || summary.CandidateCloudBoundRegions != 2 ||
		summary.P50PageDurationMillis != 1 || summary.P95PageDurationMillis != 2 ||
		summary.TotalAllocatedBytes != 200 || summary.PeakHeapAllocatedBytes != 1000 {
		t.Fatalf("summary = %+v", summary)
	}
}

func TestEvaluateRoutingSeparatesAdvisorySkipFromActionableRoutes(t *testing.T) {
	cases := []RoutingEvaluationCase{{
		DatasetVersion: "layout-v1", CaseID: "native", DocumentID: "doc-a", PageNumber: 1,
		ExpectedPageClass: PageNativeDigital, ExpectedRoutes: []ProcessingRoute{RouteNativeText},
	}}
	plan := testEvaluationPlan(PageNativeDigital,
		testEvaluationRoute(0, RegionText, RouteNativeText, FullPageBox()),
		RegionRoute{Ordinal: 1, RegionType: RegionDecorative, Box: FullPageBox(), Confidence: 0.4,
			Route: RouteSkip, Reason: ReasonLowConfidenceDecorativeSkip},
	)
	observation, err := NewRoutingEvaluationObservation(cases[0], "run-1", plan, nil, 1, 0, 0, 0.5)
	if err != nil {
		t.Fatal(err)
	}
	summary, err := EvaluateRouting(cases, []RoutingEvaluationObservation{observation}, "layout-eval-v1", 0.5)
	if err != nil {
		t.Fatal(err)
	}
	if summary.RouteMacroF1 >= 1 || summary.ActionableRouteMacroF1 != 1 {
		t.Fatalf("summary = %+v", summary)
	}
}

func TestNewRoutingEvaluationObservationReportsHighValueMiss(t *testing.T) {
	definition := RoutingEvaluationCase{
		DatasetVersion: "layout-v1", CaseID: "diagram", DocumentID: "doc-a", PageNumber: 1,
		ExpectedPageClass: PageMixed, ExpectedRoutes: []ProcessingRoute{RouteNativeText, RouteCloudVision},
		HighValueRegions: []RoutingEvaluationRegion{{
			RegionType: RegionPicture,
			Box:        BoundingBox{Left: 0.1, Top: 0.1, Right: 0.4, Bottom: 0.4},
			Route:      RouteCloudVision,
		}},
	}
	plan := testEvaluationPlan(PageMixed,
		testEvaluationRoute(0, RegionText, RouteNativeText, FullPageBox()),
		testEvaluationRoute(1, RegionPicture, RouteCloudVision,
			BoundingBox{Left: 0.7, Top: 0.7, Right: 0.9, Bottom: 0.9}),
	)
	observation, err := NewRoutingEvaluationObservation(definition, "run-1", plan, nil, 1, 0, 0, 0.5)
	if err != nil {
		t.Fatal(err)
	}
	if len(observation.HighValueMatches) != 1 || observation.HighValueMatches[0].Matched ||
		observation.HighValueMatches[0].BestIoU != 0 {
		t.Fatalf("matches = %+v", observation.HighValueMatches)
	}
}

func TestRoutingEvaluationCaseRejectsDuplicateRoutes(t *testing.T) {
	definition := RoutingEvaluationCase{
		DatasetVersion: "layout-v1", CaseID: "duplicate", DocumentID: "doc-a", PageNumber: 1,
		ExpectedPageClass: PageMixed,
		ExpectedRoutes:    []ProcessingRoute{RouteNativeText, RouteNativeText},
	}
	if err := definition.Validate(); err == nil {
		t.Fatal("expected duplicate route error")
	}
}

func testEvaluationPlan(pageClass PageClass, routes ...RegionRoute) Plan {
	return Plan{PageClass: pageClass, Routes: routes}
}

func testEvaluationRoute(
	ordinal int,
	regionType RegionType,
	route ProcessingRoute,
	box BoundingBox,
) RegionRoute {
	reason := ReasonNativeTextRegion
	switch route {
	case RouteCloudOCR:
		reason = ReasonScannedTextRegion
	case RouteTableRecovery:
		reason = ReasonTableStructureRequired
	case RouteCloudVision:
		reason = ReasonVisualSemanticsRequired
	}
	return RegionRoute{
		Ordinal: ordinal, RegionType: regionType, Box: box, Confidence: 0.9,
		Route: route, Reason: reason,
	}
}
