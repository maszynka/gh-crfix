package gate

import (
	"testing"

	triagepkg "github.com/maszynka/gh-crfix/internal/triage"
)

// ---------------------------------------------------------------------------
// BuildGateContext
// ---------------------------------------------------------------------------

func TestBuildGateContext(t *testing.T) {
	defaultWeights := ScoreWeights{
		NeedsLLM:    0.2,
		PRComment:   0.4,
		TestFailure: 1.0,
	}

	t.Run("needs_llm + pr_comment + tests_failed => total=1.6, should_run=true", func(t *testing.T) {
		triage := TriageSummary{
			NeedsLLM: []TriageEntry{
				{ThreadID: "t1", Reason: "complex comment"},
				{ThreadID: "t2", Reason: triagepkg.ReasonPRLevelComment},
			},
		}
		validation := ValidationResult{TestsFailed: true}

		ctx := BuildGateContext(triage, validation, defaultWeights)

		// needs_llm: count=2, score = 1 * weight (because count > 0)
		if ctx.Components.NeedsLLM.Count != 2 {
			t.Errorf("NeedsLLM.Count = %d, want 2", ctx.Components.NeedsLLM.Count)
		}
		// pr_comment: count=1
		if ctx.Components.PRComment.Count != 1 {
			t.Errorf("PRComment.Count = %d, want 1", ctx.Components.PRComment.Count)
		}
		// total = 0.2 + 0.4 + 1.0 = 1.6
		wantTotal := 1.6
		if ctx.TotalScore < wantTotal-1e-9 || ctx.TotalScore > wantTotal+1e-9 {
			t.Errorf("TotalScore = %v, want %v", ctx.TotalScore, wantTotal)
		}
		if !ctx.ShouldRunGate {
			t.Errorf("ShouldRunGate = false, want true")
		}
		if !ctx.Components.TestFailure.Failed {
			t.Errorf("TestFailure.Failed = false, want true")
		}
	})

	t.Run("no needs_llm, no validation failure => total=0, should_run=false", func(t *testing.T) {
		triage := TriageSummary{NeedsLLM: []TriageEntry{}}
		validation := ValidationResult{TestsFailed: false}

		ctx := BuildGateContext(triage, validation, defaultWeights)

		if ctx.TotalScore != 0 {
			t.Errorf("TotalScore = %v, want 0", ctx.TotalScore)
		}
		if ctx.ShouldRunGate {
			t.Errorf("ShouldRunGate = true, want false")
		}
	})

	t.Run("only test failure (weight=1.0) => total=1.0, should_run=true", func(t *testing.T) {
		triage := TriageSummary{NeedsLLM: []TriageEntry{}}
		validation := ValidationResult{TestsFailed: true}

		ctx := BuildGateContext(triage, validation, defaultWeights)

		if ctx.TotalScore < 1.0-1e-9 || ctx.TotalScore > 1.0+1e-9 {
			t.Errorf("TotalScore = %v, want 1.0", ctx.TotalScore)
		}
		if !ctx.ShouldRunGate {
			t.Errorf("ShouldRunGate = false, want true")
		}
	})

	t.Run("needs_llm entry with PR-level reason sets pr_comment.count", func(t *testing.T) {
		triage := TriageSummary{
			NeedsLLM: []TriageEntry{
				{ThreadID: "t1", Reason: triagepkg.ReasonPRLevelComment},
			},
		}
		validation := ValidationResult{TestsFailed: false}

		ctx := BuildGateContext(triage, validation, defaultWeights)

		if ctx.Components.PRComment.Count != 1 {
			t.Errorf("PRComment.Count = %d, want 1", ctx.Components.PRComment.Count)
		}
		wantScore := defaultWeights.PRComment
		if ctx.Components.PRComment.Score < wantScore-1e-9 || ctx.Components.PRComment.Score > wantScore+1e-9 {
			t.Errorf("PRComment.Score = %v, want %v", ctx.Components.PRComment.Score, wantScore)
		}
	})

	t.Run("threshold is always 1", func(t *testing.T) {
		triage := TriageSummary{NeedsLLM: []TriageEntry{}}
		validation := ValidationResult{TestsFailed: false}

		ctx := BuildGateContext(triage, validation, defaultWeights)

		if ctx.Threshold != 1.0 {
			t.Errorf("Threshold = %v, want 1.0", ctx.Threshold)
		}
	})

	t.Run("component weights match input weights", func(t *testing.T) {
		triage := TriageSummary{
			NeedsLLM: []TriageEntry{{ThreadID: "t1", Reason: "complex"}},
		}
		validation := ValidationResult{TestsFailed: true}

		ctx := BuildGateContext(triage, validation, defaultWeights)

		if ctx.Components.NeedsLLM.Weight != defaultWeights.NeedsLLM {
			t.Errorf("NeedsLLM.Weight = %v, want %v", ctx.Components.NeedsLLM.Weight, defaultWeights.NeedsLLM)
		}
		if ctx.Components.PRComment.Weight != defaultWeights.PRComment {
			t.Errorf("PRComment.Weight = %v, want %v", ctx.Components.PRComment.Weight, defaultWeights.PRComment)
		}
		if ctx.Components.TestFailure.Weight != defaultWeights.TestFailure {
			t.Errorf("TestFailure.Weight = %v, want %v", ctx.Components.TestFailure.Weight, defaultWeights.TestFailure)
		}
	})
}

// ---------------------------------------------------------------------------
// GateSchema
// ---------------------------------------------------------------------------

func TestGateSchema(t *testing.T) {
	schema := GateSchema()

	if schema == nil {
		t.Fatal("GateSchema() returned nil")
	}

	// required array
	required, ok := schema["required"]
	if !ok {
		t.Fatal("GateSchema() missing 'required' key")
	}
	requiredSlice, ok := required.([]string)
	if !ok {
		t.Fatalf("GateSchema()['required'] is %T, want []string", required)
	}
	wantRequired := map[string]bool{
		"needs_advanced_model": true,
		"reason":               true,
		"threads_to_fix":       true,
	}
	for _, r := range requiredSlice {
		if !wantRequired[r] {
			t.Errorf("unexpected required field %q", r)
		}
		delete(wantRequired, r)
	}
	for missing := range wantRequired {
		t.Errorf("required field %q missing from schema", missing)
	}

	// properties
	properties, ok := schema["properties"]
	if !ok {
		t.Fatal("GateSchema() missing 'properties' key")
	}
	props, ok := properties.(map[string]interface{})
	if !ok {
		t.Fatalf("GateSchema()['properties'] is %T, want map[string]interface{}", properties)
	}

	// needs_advanced_model must be boolean
	if namProp, ok := props["needs_advanced_model"]; ok {
		namMap, ok := namProp.(map[string]interface{})
		if !ok {
			t.Errorf("needs_advanced_model property is %T, want map[string]interface{}", namProp)
		} else if namMap["type"] != "boolean" {
			t.Errorf("needs_advanced_model.type = %q, want %q", namMap["type"], "boolean")
		}
	} else {
		t.Error("needs_advanced_model missing from properties")
	}

	// threads_to_fix must be array
	if ttfProp, ok := props["threads_to_fix"]; ok {
		ttfMap, ok := ttfProp.(map[string]interface{})
		if !ok {
			t.Errorf("threads_to_fix property is %T, want map[string]interface{}", ttfProp)
		} else if ttfMap["type"] != "array" {
			t.Errorf("threads_to_fix.type = %q, want %q", ttfMap["type"], "array")
		}
	} else {
		t.Error("threads_to_fix missing from properties")
	}
}
