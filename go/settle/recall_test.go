package settle

import (
	"testing"
	"time"
)

func decisionAt(id string, day int, files []string, verdicts []Verdict, dissents []string) Decision {
	return Decision{
		Schema:    SchemaDecision,
		ID:        id,
		CreatedAt: time.Date(2026, 1, day, 12, 0, 0, 0, time.UTC),
		Subject:   Subject{Type: "diff", Files: files},
		Verdicts:  verdicts,
		Outcome:   Outcome{Reached: true, Decision: VerdictApprove, Agreement: 0.8, Dissents: dissents},
	}
}

func hitIDs(hits []RecallHit) []string {
	ids := make([]string, len(hits))
	for i, h := range hits {
		ids[i] = h.ID
	}
	return ids
}

func TestRecallFileOverlapRanksHighest(t *testing.T) {
	decisions := []Decision{
		decisionAt("d1", 1, []string{"api/handler.go"},
			[]Verdict{verdict("security", "approve", 0.9)}, nil),
		decisionAt("d2", 2, []string{"core/queue.go"},
			[]Verdict{verdict("tests", "approve", 0.8)}, nil),
		decisionAt("d3", 3, []string{"web/api/handler.go"}, // same basename, other dir
			[]Verdict{verdict("simplicity", "approve", 0.7)}, nil),
	}

	hits := Recall(decisions, RecallQuery{Files: []string{"api/handler.go"}}, 0)
	if len(hits) != 2 {
		t.Fatalf("expected 2 file-matched hits, got %d: %v", len(hits), hitIDs(hits))
	}
	// Exact path (d1) must outrank the same-basename match (d3), and the
	// unrelated file (d2) must not appear at all.
	if hits[0].ID != "d1" {
		t.Fatalf("expected exact-path decision d1 first, got %v", hitIDs(hits))
	}
	if hits[1].ID != "d3" {
		t.Fatalf("expected basename match d3 second, got %v", hitIDs(hits))
	}
	if hits[0].Score <= hits[1].Score {
		t.Fatalf("exact match should score higher: %v vs %v", hits[0].Score, hits[1].Score)
	}
}

func TestRecallTermMatch(t *testing.T) {
	v := verdict("correctness", "revise", 0.6)
	v.Reasoning = "The DisallowUnknownFields decoder rejects unmapped payload keys."
	decisions := []Decision{
		decisionAt("d1", 1, []string{"api/decode.go"}, []Verdict{v}, nil),
		decisionAt("d2", 2, []string{"api/other.go"},
			[]Verdict{verdict("tests", "approve", 0.5)}, nil),
	}

	hits := Recall(decisions, RecallQuery{Terms: []string{"DisallowUnknownFields payload"}}, 0)
	if len(hits) != 1 || hits[0].ID != "d1" {
		t.Fatalf("expected only d1 to match terms, got %v", hitIDs(hits))
	}
	if len(hits[0].MatchedTerms) == 0 {
		t.Fatalf("expected matched terms to be reported")
	}
}

func TestRecallGradedBoostAndLimit(t *testing.T) {
	graded := decisionAt("d1", 1, []string{"api/handler.go"},
		[]Verdict{verdict("security", "approve", 0.9)}, nil)
	graded.Result = "reverted"
	ungraded := decisionAt("d2", 2, []string{"api/handler.go"},
		[]Verdict{verdict("security", "approve", 0.9)}, nil)

	hits := Recall([]Decision{ungraded, graded}, RecallQuery{Files: []string{"api/handler.go"}}, 1)
	if len(hits) != 1 {
		t.Fatalf("limit=1 should return one hit, got %d", len(hits))
	}
	// Equal file score, but the graded decision carries real ground truth and
	// wins the tie.
	if hits[0].ID != "d1" {
		t.Fatalf("graded precedent should rank first, got %v", hitIDs(hits))
	}
	if hits[0].Result != "reverted" {
		t.Fatalf("expected result surfaced, got %q", hits[0].Result)
	}
}

func TestRecallSurfacesDissentReasoning(t *testing.T) {
	approver := verdict("security", "approve", 0.9)
	approver.Reasoning = "Looks fine to me."
	dissenter := verdict("correctness", "reject", 0.8)
	dissenter.Reasoning = "This unlocks a race on the shared cache map."

	d := decisionAt("d1", 1, []string{"core/cache.go"},
		[]Verdict{approver, dissenter}, []string{"correctness"})

	hits := Recall([]Decision{d}, RecallQuery{Files: []string{"core/cache.go"}}, 0)
	if len(hits) != 1 {
		t.Fatalf("expected 1 hit, got %d", len(hits))
	}
	if hits[0].Reasoning != dissenter.Reasoning {
		t.Fatalf("expected dissent reasoning surfaced, got %q", hits[0].Reasoning)
	}
}

func TestRecallNoSignalReturnsNothing(t *testing.T) {
	decisions := []Decision{
		decisionAt("d1", 1, []string{"api/handler.go"},
			[]Verdict{verdict("security", "approve", 0.9)}, nil),
	}
	hits := Recall(decisions, RecallQuery{Files: []string{"totally/unrelated.rs"}, Terms: []string{"zzz"}}, 0)
	if len(hits) != 0 {
		t.Fatalf("expected no hits for irrelevant query, got %v", hitIDs(hits))
	}
}

func TestRecallStopwordsIgnored(t *testing.T) {
	if terms := normalizeTerms([]string{"add the fix for a bug"}); len(terms) != 1 || terms[0] != "bug" {
		t.Fatalf("expected only [bug] after stopword/short-token filtering, got %v", terms)
	}
}

func TestDiffFiles(t *testing.T) {
	files := DiffFiles([]byte(sampleDiff))
	if len(files) != 1 || files[0] != "api/handler.go" {
		t.Fatalf("expected [api/handler.go], got %v", files)
	}
}
