package settle

import (
	"testing"
	"time"
)

func citedDecision(id string, reasoning string) Decision {
	v := verdict("api-contract", "reject", 0.8)
	v.Reasoning = reasoning
	return decisionAt(id, 1, []string{"api/handler.go"}, []Verdict{v}, []string{"api-contract"})
}

func TestRetrievalOracleGroundedClaim(t *testing.T) {
	decisions := []Decision{
		citedDecision("dec-aaa", "A 405 response must include the Allow header per RFC 9110."),
	}
	res := RetrievalOracle(
		"The Allow header is required on 405 responses.",
		[]string{"dec-aaa"}, decisions, DefaultOracleParams())

	if !res.Supported {
		t.Fatalf("grounded claim should be supported, reasons: %v", res.Reasons)
	}
	if res.Score < 0.5 {
		t.Fatalf("expected score >= 0.5, got %v", res.Score)
	}
}

func TestRetrievalOracleRejectsFabricatedCitation(t *testing.T) {
	decisions := []Decision{citedDecision("dec-aaa", "Allow header required.")}
	res := RetrievalOracle("Anything at all here.", []string{"dec-zzz"}, decisions, DefaultOracleParams())

	if res.Supported {
		t.Fatal("citation to a nonexistent decision must not be supported")
	}
	if len(res.UnknownCitations) != 1 || res.UnknownCitations[0] != "dec-zzz" {
		t.Fatalf("expected dec-zzz flagged as unknown, got %v", res.UnknownCitations)
	}
}

func TestRetrievalOracleRejectsUnsupportedClaim(t *testing.T) {
	decisions := []Decision{citedDecision("dec-aaa", "A 405 response must include the Allow header.")}
	res := RetrievalOracle(
		"Kubernetes deployments need three replicas minimum.",
		[]string{"dec-aaa"}, decisions, DefaultOracleParams())

	if res.Supported {
		t.Fatalf("claim unrelated to the cited decision must be rejected, score %v", res.Score)
	}
}

func TestRetrievalOracleRequiresCitations(t *testing.T) {
	res := RetrievalOracle("Some claim with words.", nil, nil, DefaultOracleParams())
	if res.Supported {
		t.Fatal("a claim with no citations cannot be supported")
	}
}

func TestRetrievalOracleFlagsRevertedCitation(t *testing.T) {
	d := citedDecision("dec-aaa", "A 405 response must include the Allow header per RFC 9110.")
	d.Result = "reverted"
	res := RetrievalOracle(
		"The Allow header is required on 405 responses.",
		[]string{"dec-aaa"}, []Decision{d}, DefaultOracleParams())

	// Still supported on the evidence, but the reverted citation is surfaced.
	if !res.Supported {
		t.Fatalf("claim is grounded; should be supported, reasons: %v", res.Reasons)
	}
	if len(res.RevertedCitations) != 1 {
		t.Fatalf("expected the reverted citation surfaced, got %v", res.RevertedCitations)
	}
}

func TestMemoryNoteRoundTrip(t *testing.T) {
	store := initStore(t)
	prop := MemoryProposal{
		Scope: "go/core",
		Claim: "DisallowUnknownFields means payload changes must update structs and frontend types.",
		Cites: []string{"dec-aaa"},
	}
	oracle := OracleResult{Supported: true, Score: 0.8, MatchedTerms: []string{"payload", "structs"}}
	note := NewMemoryNote(prop, oracle, time.Date(2026, 3, 1, 10, 0, 0, 0, time.UTC))

	if err := store.WriteMemoryNote(note); err != nil {
		t.Fatal(err)
	}
	got, err := store.GetMemoryNote(note.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Claim != prop.Claim || got.Scope != prop.Scope || got.Status != MemProposed {
		t.Fatalf("round-trip mismatch: %+v", got)
	}
	if len(got.Cites) != 1 || got.Cites[0] != "dec-aaa" {
		t.Fatalf("provenance not preserved: %v", got.Cites)
	}

	notes, err := store.ReadMemoryNotes()
	if err != nil {
		t.Fatal(err)
	}
	if len(notes) != 1 || notes[0].ID != note.ID {
		t.Fatalf("expected one listed note, got %v", notes)
	}
}

func TestCuratorFeedNewestFirstAndLimited(t *testing.T) {
	decisions := []Decision{
		decisionAt("d1", 1, []string{"a.go"}, []Verdict{verdict("tests", "approve", 0.9)}, nil),
		decisionAt("d2", 2, []string{"b.go"}, []Verdict{verdict("tests", "approve", 0.9)}, nil),
		decisionAt("d3", 3, []string{"c.go"}, []Verdict{verdict("tests", "approve", 0.9)}, nil),
	}
	feed := CuratorFeed(decisions, 2)
	if len(feed) != 2 || feed[0].ID != "d3" || feed[1].ID != "d2" {
		t.Fatalf("expected newest-first [d3 d2], got %v", feedIDs(feed))
	}
	if len(feed[0].Votes) != 1 || feed[0].Votes[0].Persona != "tests" {
		t.Fatalf("expected vote digest carried through, got %+v", feed[0].Votes)
	}
}

func feedIDs(feed []EpisodeDigest) []string {
	ids := make([]string, len(feed))
	for i, e := range feed {
		ids[i] = e.ID
	}
	return ids
}
