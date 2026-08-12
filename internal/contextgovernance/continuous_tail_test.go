package contextgovernance

import (
	"context"
	"fmt"
	"testing"
)

func TestContinuousTailSelectsByTokenBudgetBeyondOneHundredMessages(t *testing.T) {
	estimator, err := NewLocalTokenEstimator(EstimationMethodLocalCalibrated, nil)
	if err != nil {
		t.Fatal(err)
	}
	selector, err := NewContinuousTailSelector(estimator)
	if err != nil {
		t.Fatal(err)
	}
	messages := make([]TailMessage, 125)
	for index := range messages {
		messages[index] = TailMessage{
			Sequence: int64(index + 1), Content: fmt.Sprintf("message-%03d %s", index+1, "bounded content"),
			Current: index == len(messages)-1,
		}
	}
	selection, err := selector.Select(context.Background(), ContinuousTailRequest{
		Profile: "fixture", MaxTokens: 700, Messages: messages,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(selection.Messages) < 2 || len(selection.Messages) >= len(messages) ||
		selection.Messages[len(selection.Messages)-1].Sequence != 125 || selection.EstimatedUpperBoundTokens > 700 {
		t.Fatalf("selection = %+v", selection)
	}
	if selection.Messages[0].Sequence+int64(len(selection.Messages))-1 != 125 {
		t.Fatalf("selection is not a continuous suffix: first=%d count=%d", selection.Messages[0].Sequence, len(selection.Messages))
	}
}

func TestContinuousTailStopsAtSequenceGap(t *testing.T) {
	estimator, err := NewLocalTokenEstimator(EstimationMethodLocalCalibrated, nil)
	if err != nil {
		t.Fatal(err)
	}
	selector, err := NewContinuousTailSelector(estimator)
	if err != nil {
		t.Fatal(err)
	}
	messages := []TailMessage{
		{Sequence: 1, Content: "old"},
		{Sequence: 3, Content: "recent"},
		{Sequence: 4, Content: "current", Current: true},
	}
	selection, err := selector.Select(context.Background(), ContinuousTailRequest{
		Profile: "fixture", MaxTokens: 1000, Messages: messages,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(selection.Messages) != 2 || selection.Messages[0].Sequence != 3 {
		t.Fatalf("gap selection = %+v", selection.Messages)
	}
}
