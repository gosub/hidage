package main

import (
	"math"
	"testing"
)

func TestAggregateTransitive(t *testing.T) {
	const minSamples = 2.5

	// No opinion has enough evidence → no transitive score.
	if _, ok := aggregateTransitive([]weightedOpinion{{weight: 1, score: 0.9, samples: 1}}, minSamples); ok {
		t.Fatal("opinion below the evidence threshold should not count")
	}

	// Weighted average, discounting each anchor by my weight for it:
	// (1.0·0.9 + 0.5·0.3) / (1.0+0.5) = 1.05/1.5 = 0.7
	v, ok := aggregateTransitive([]weightedOpinion{
		{weight: 1.0, score: 0.9, samples: 5}, // trusted anchor
		{weight: 0.5, score: 0.3, samples: 5}, // good-but-discounted anchor
	}, minSamples)
	if !ok || math.Abs(v-0.7) > 1e-9 {
		t.Fatalf("aggregate = %v (ok=%v), want 0.7", v, ok)
	}

	// Low-evidence opinions are ignored, leaving only the well-founded one.
	v2, ok2 := aggregateTransitive([]weightedOpinion{
		{weight: 1.0, score: 0.9, samples: 5},
		{weight: 1.0, score: 0.1, samples: 1}, // ignored: too little evidence
	}, minSamples)
	if !ok2 || math.Abs(v2-0.9) > 1e-9 {
		t.Fatalf("aggregate w/ ignored low-evidence = %v, want 0.9", v2)
	}
}
