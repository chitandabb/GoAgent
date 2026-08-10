package main

import (
	"math"
	"testing"
)

func TestParseBoxAndEstimate(t *testing.T) {
	box, err := parseBox("0.1,0.2,0.8,0.9")
	if err != nil || box.Left != 0.1 || box.Bottom != 0.9 {
		t.Fatalf("box=%+v err=%v", box, err)
	}
	if _, err := parseBox("0.8,0.2,0.1,0.9"); err == nil {
		t.Fatal("expected invalid box rejection")
	}
	cost := estimatedCost(5000, 4096, 1, 10)
	if math.Abs(cost-0.04596) > 0.0000001 {
		t.Fatalf("cost = %f", cost)
	}
}

func TestParseOptionsRequiresOneExplicitMode(t *testing.T) {
	base := []string{"-input", "page.png", "-box", "0.1,0.2,0.8,0.9"}
	if _, err := parseOptions(base); err == nil {
		t.Fatal("expected missing mode rejection")
	}
	if _, err := parseOptions(append(base, "-validate-only", "-estimate-only")); err == nil {
		t.Fatal("expected multiple mode rejection")
	}
	if _, err := parseOptions(append(base, "-estimate-only")); err != nil {
		t.Fatal(err)
	}
}
