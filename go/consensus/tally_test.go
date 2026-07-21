package consensus

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

func vote(id, decision string, confidence, power float64) Result {
	return Result{
		WorkerID:    id,
		VotingPower: power,
		Timestamp:   time.Unix(0, 0),
		Response: &Response{
			Decision:   decision,
			Confidence: confidence,
			Category:   "review",
			Reasoning:  "reasoning from " + id,
		},
	}
}

func rateLimited(id string) Result {
	r := vote(id, "approve", 0.9, 1.0)
	r.Response.Metadata = map[string]interface{}{"error": "Rate limit reached for model"}
	return r
}

func decodeResponse(t *testing.T, raw string) *Response {
	t.Helper()
	var resp Response
	if err := json.Unmarshal([]byte(raw), &resp); err != nil {
		t.Fatalf("result is not valid Response JSON: %v\nraw: %s", err, raw)
	}
	return &resp
}

var exactCfg = Config{
	MinimumAgreement: 0.66,
	MatchStrategy:    ExactMatch,
}

func TestTallyUnanimous(t *testing.T) {
	results := []Result{
		vote("security", "approve", 0.9, 1.0),
		vote("correctness", "approve", 0.8, 1.0),
		vote("tests", "approve", 0.7, 1.0),
	}
	ok, raw := Tally(context.Background(), "review diff", results, exactCfg, nil)
	if !ok {
		t.Fatal("expected consensus")
	}
	resp := decodeResponse(t, raw)
	if resp.Decision != "approve" {
		t.Errorf("decision = %q, want approve", resp.Decision)
	}
	if resp.Metadata["agreeing_workers"].(float64) != 3 {
		t.Errorf("agreeing_workers = %v, want 3", resp.Metadata["agreeing_workers"])
	}
}

func TestTallySplitBelowThresholdNoLead(t *testing.T) {
	// Two groups with identical weighted votes: no threshold met, no lead.
	results := []Result{
		vote("a", "approve", 0.8, 1.0),
		vote("b", "reject", 0.8, 1.0),
	}
	ok, _ := Tally(context.Background(), "q", results, exactCfg, nil)
	if ok {
		t.Fatal("expected no consensus on a 50/50 split with 0.66 threshold")
	}
}

func TestTallySignificantLeadWinsBelowThreshold(t *testing.T) {
	// approve: 0.9+0.8=1.7 of 2.6 (65.4%) < 66% threshold, but lead over
	// reject (34.6%) is >10%, so the significant-lead rule accepts it.
	results := []Result{
		vote("a", "approve", 0.9, 1.0),
		vote("b", "approve", 0.8, 1.0),
		vote("c", "reject", 0.9, 1.0),
	}
	ok, raw := Tally(context.Background(), "q", results, exactCfg, nil)
	if !ok {
		t.Fatal("expected consensus via significant lead")
	}
	if resp := decodeResponse(t, raw); resp.Decision != "approve" {
		t.Errorf("decision = %q, want approve", resp.Decision)
	}
}

func TestTallyVotingPowerFlipsWinner(t *testing.T) {
	// Head-count favors approve (2 vs 1), but the rejector carries 3x power:
	// approve = 2*0.8 = 1.6; reject = 3*0.8 = 2.4 → reject wins.
	results := []Result{
		vote("a", "approve", 0.8, 1.0),
		vote("b", "approve", 0.8, 1.0),
		vote("c", "reject", 0.8, 3.0),
	}
	ok, raw := Tally(context.Background(), "q", results, Config{MinimumAgreement: 0.5, MatchStrategy: ExactMatch}, nil)
	if !ok {
		t.Fatal("expected consensus")
	}
	if resp := decodeResponse(t, raw); resp.Decision != "reject" {
		t.Errorf("decision = %q, want reject (voting power should outweigh head count)", resp.Decision)
	}
}

func TestTallyQuarantinedZeroWeightCannotDecide(t *testing.T) {
	// A quarantined (weight 0) voter's dissent must not block the panel,
	// and its approval must not count toward the winner's share.
	results := []Result{
		vote("healthy1", "approve", 0.9, 1.0),
		vote("healthy2", "approve", 0.8, 1.0),
		vote("shadow", "reject", 1.0, 0.0),
	}
	ok, raw := Tally(context.Background(), "q", results, exactCfg, nil)
	if !ok {
		t.Fatal("expected consensus despite shadow dissent")
	}
	if resp := decodeResponse(t, raw); resp.Decision != "approve" {
		t.Errorf("decision = %q, want approve", resp.Decision)
	}
}

func TestTallyNilResponsesSkipped(t *testing.T) {
	results := []Result{
		{WorkerID: "dead", VotingPower: 1.0},
		vote("a", "approve", 0.9, 1.0),
		vote("b", "approve", 0.9, 1.0),
	}
	ok, raw := Tally(context.Background(), "q", results, exactCfg, nil)
	if !ok {
		t.Fatal("expected consensus")
	}
	if resp := decodeResponse(t, raw); resp.Metadata["agreeing_workers"].(float64) != 2 {
		t.Errorf("agreeing_workers = %v, want 2", resp.Metadata["agreeing_workers"])
	}
}

func TestTallyMostlyRateLimitedRetries(t *testing.T) {
	results := []Result{
		rateLimited("a"),
		rateLimited("b"),
		vote("c", "approve", 0.9, 1.0),
	}
	ok, _ := Tally(context.Background(), "q", results, exactCfg, nil)
	if ok {
		t.Fatal("expected no consensus when >50% of responses are rate limited")
	}
}

func TestTallyEmpty(t *testing.T) {
	if ok, _ := Tally(context.Background(), "q", nil, exactCfg, nil); ok {
		t.Fatal("expected no consensus for empty results")
	}
}

func TestTallyNumericGroupsWithinTolerance(t *testing.T) {
	cfg := Config{MinimumAgreement: 0.6, MatchStrategy: NumericMatch, NumericTolerance: 0.5}
	results := []Result{
		vote("a", "42.0", 0.9, 1.0),
		vote("b", "42.3", 0.9, 1.0),
		vote("c", "100", 0.9, 1.0),
	}
	ok, raw := Tally(context.Background(), "q", results, cfg, nil)
	if !ok {
		t.Fatal("expected consensus: 42.0 and 42.3 should group within tolerance")
	}
	resp := decodeResponse(t, raw)
	if v, _ := ParseNumericValue(resp.Decision); v > 43 || v < 42 {
		t.Errorf("decision = %q, want the ~42 group", resp.Decision)
	}
}

func TestTallySemanticGroupsSimilarAnswers(t *testing.T) {
	cfg := Config{MinimumAgreement: 0.6, MatchStrategy: SemanticMatch}
	results := []Result{
		vote("a", "The capital of France is Paris", 0.9, 1.0),
		vote("b", "capital of France: Paris", 0.9, 1.0),
		vote("c", "Berlin", 0.9, 1.0),
	}
	ok, raw := Tally(context.Background(), "q", results, cfg, nil)
	if !ok {
		t.Fatal("expected consensus: similar Paris answers should group")
	}
	resp := decodeResponse(t, raw)
	if resp.Metadata["agreeing_workers"].(float64) != 2 {
		t.Errorf("agreeing_workers = %v, want 2 (Paris group)", resp.Metadata["agreeing_workers"])
	}
}

func TestTallyThresholdRelaxedWithManyGroups(t *testing.T) {
	// 4 distinct answers → adjustedMinAgreement = 0.9*0.6 = 0.54. The 'a'
	// group (2 of 5 voters, 40%) still fails, but a 3-voter group (60%,
	// with >10% lead as well) passes.
	cfg := Config{MinimumAgreement: 0.9, MatchStrategy: ExactMatch}
	results := []Result{
		vote("a1", "alpha", 0.9, 1.0),
		vote("a2", "alpha", 0.9, 1.0),
		vote("a3", "alpha", 0.9, 1.0),
		vote("b", "beta", 0.9, 1.0),
		vote("c", "gamma", 0.9, 1.0),
	}
	ok, raw := Tally(context.Background(), "q", results, cfg, nil)
	if !ok {
		t.Fatal("expected consensus with relaxed threshold across many groups")
	}
	if resp := decodeResponse(t, raw); resp.Decision != "alpha" {
		t.Errorf("decision = %q, want alpha", resp.Decision)
	}
}

func TestTallyDistinctVotersSurviveGrouping(t *testing.T) {
	// Pins the per-iteration loop-variable capture: each group member must
	// keep its own WorkerID (a shared-pointer bug would repeat one ID).
	results := []Result{
		vote("first", "approve", 0.9, 1.0),
		vote("second", "approve", 0.8, 1.0),
	}
	cfg := Config{MinimumAgreement: 0.5, MatchStrategy: ExactMatch, ExtractMergedReasoning: true}
	ok, raw := Tally(context.Background(), "q", results, cfg, nil)
	if !ok {
		t.Fatal("expected consensus")
	}
	resp := decodeResponse(t, raw)
	if resp.MergedReasoning == nil {
		t.Fatal("expected merged reasoning to be extracted")
	}
	seen := map[string]bool{}
	for _, c := range resp.MergedReasoning.Contributions {
		seen[c.WorkerID] = true
	}
	if len(seen) < 2 {
		t.Errorf("contributions should come from distinct workers, got %v", seen)
	}
}

func TestMergeMatchOfflineFallback(t *testing.T) {
	// merge_match with nil synthesizer and a clear voting winner (>=40%).
	cfg := Config{MinimumAgreement: 0.5, MatchStrategy: MergeMatch}
	results := []Result{
		vote("a", "use postgres", 0.9, 1.0),
		vote("b", "use postgres", 0.8, 1.0),
		vote("c", "use sqlite", 0.6, 1.0),
	}
	ok, raw := Tally(context.Background(), "q", results, cfg, nil)
	if !ok {
		t.Fatal("expected merge consensus")
	}
	resp := decodeResponse(t, raw)
	if resp.Decision != "use postgres" {
		t.Errorf("decision = %q, want 'use postgres'", resp.Decision)
	}
	if resp.Metadata["synthesis_type"] != "voting" {
		t.Errorf("synthesis_type = %v, want voting", resp.Metadata["synthesis_type"])
	}
}

func TestMergeMatchSplitVotesNilSynthFallsBack(t *testing.T) {
	// Four distinct low-share answers (<40% each) force the AI-synthesis
	// path; with nil synthesizer it must fall back, not fail.
	cfg := Config{MinimumAgreement: 0.5, MatchStrategy: MergeMatch}
	results := []Result{
		vote("a", "red green blue", 0.8, 1.0),
		vote("b", "orange purple", 0.7, 1.0),
		vote("c", "silver gold", 0.7, 1.0),
		vote("d", "black white", 0.6, 1.0),
	}
	ok, raw := Tally(context.Background(), "q", results, cfg, nil)
	if !ok {
		t.Fatal("expected fallback merge to still produce a result")
	}
	resp := decodeResponse(t, raw)
	if resp.Metadata["synthesis_type"] != "fallback" {
		t.Errorf("synthesis_type = %v, want fallback", resp.Metadata["synthesis_type"])
	}
}

func TestExtractMergedReasoningAlgorithmic(t *testing.T) {
	workers := []*Result{
		{WorkerID: "w1", Response: &Response{Confidence: 0.9, Reasoning: "The change is safe because it only touches documentation files."}},
		{WorkerID: "w2", Response: &Response{Confidence: 0.8, Reasoning: "Version numbers are updated consistently across every manifest."}},
	}
	got, err := ExtractMergedReasoningAlgorithmic(workers)
	if err != nil {
		t.Fatal(err)
	}
	if got.SynthesisType != "algorithmic" {
		t.Errorf("synthesis type = %q", got.SynthesisType)
	}
	if len(got.Contributions) != 2 {
		t.Errorf("contributions = %d, want 2", len(got.Contributions))
	}
	if _, err := ExtractMergedReasoningAlgorithmic(nil); err == nil {
		t.Error("expected error for empty workers")
	}
}

func TestIsLowQualityMergedReasoning(t *testing.T) {
	if !IsLowQualityMergedReasoning(nil) {
		t.Error("nil should be low quality")
	}
	if !IsLowQualityMergedReasoning(&MergedReasoning{Summary: "short", Contributions: []ReasoningContribution{{Text: "a"}}}) {
		t.Error("single short contribution should be low quality")
	}
	good := &MergedReasoning{
		Summary: "This summary is comfortably longer than fifty characters in total length.",
		Contributions: []ReasoningContribution{
			{Text: "first insight"}, {Text: "second insight"},
		},
	}
	if IsLowQualityMergedReasoning(good) {
		t.Error("good reasoning flagged as low quality")
	}
}
