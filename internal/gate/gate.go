// Package gate builds gate prompts and computes gate scores.
package gate

// TriageEntry is a single entry in the triage summary.
type TriageEntry struct {
	ThreadID string `json:"thread_id"`
	Reason   string `json:"reason"`
}

// TriageSummary holds the list of threads that need LLM attention.
type TriageSummary struct {
	NeedsLLM []TriageEntry `json:"needs_llm"`
}

// ValidationResult holds the outcome of the validation step.
type ValidationResult struct {
	TestsFailed bool `json:"tests_failed"`
}

// ScoreWeights configures the weight of each gate scoring component.
type ScoreWeights struct {
	NeedsLLM    float64
	PRComment   float64
	TestFailure float64
}

// ComponentScore holds the count, weight, and resulting score for one component.
type ComponentScore struct {
	Count  int     `json:"count"`
	Weight float64 `json:"weight"`
	Score  float64 `json:"score"`
}

// TestFailureScore holds the failure flag, weight, and resulting score.
type TestFailureScore struct {
	Failed bool    `json:"failed"`
	Weight float64 `json:"weight"`
	Score  float64 `json:"score"`
}

// GateComponents holds all component scores.
type GateComponents struct {
	NeedsLLM    ComponentScore   `json:"needs_llm"`
	PRComment   ComponentScore   `json:"pr_comment"`
	TestFailure TestFailureScore `json:"test_failure"`
}

// GateContext is the result of BuildGateContext.
type GateContext struct {
	Threshold     float64        `json:"threshold"`
	TotalScore    float64        `json:"total_score"`
	ShouldRunGate bool           `json:"should_run_gate"`
	Components    GateComponents `json:"components"`
}

// BuildGateContext computes the gate context from triage/validation results and weights.
func BuildGateContext(triage TriageSummary, validation ValidationResult, weights ScoreWeights) GateContext {
	needsLLMCount := len(triage.NeedsLLM)
	prCommentCount := 0
	for _, e := range triage.NeedsLLM {
		if e.Reason == "PR-level comment (no file path)" {
			prCommentCount++
		}
	}

	var needsLLMScore, prCommentScore, testFailureScore float64
	if needsLLMCount > 0 {
		needsLLMScore = weights.NeedsLLM
	}
	if prCommentCount > 0 {
		prCommentScore = weights.PRComment
	}
	if validation.TestsFailed {
		testFailureScore = weights.TestFailure
	}

	total := needsLLMScore + prCommentScore + testFailureScore

	return GateContext{
		Threshold:     1.0,
		TotalScore:    total,
		ShouldRunGate: total >= 1.0,
		Components: GateComponents{
			NeedsLLM:    ComponentScore{Count: needsLLMCount, Weight: weights.NeedsLLM, Score: needsLLMScore},
			PRComment:   ComponentScore{Count: prCommentCount, Weight: weights.PRComment, Score: prCommentScore},
			TestFailure: TestFailureScore{Failed: validation.TestsFailed, Weight: weights.TestFailure, Score: testFailureScore},
		},
	}
}

// GateSchema returns the JSON schema for the gate model's output.
func GateSchema() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"needs_advanced_model": map[string]interface{}{"type": "boolean"},
			"reason":               map[string]interface{}{"type": "string"},
			"threads_to_fix": map[string]interface{}{
				"type":  "array",
				"items": map[string]interface{}{"type": "string"},
			},
		},
		"required":             []string{"needs_advanced_model", "reason", "threads_to_fix"},
		"additionalProperties": false,
	}
}
