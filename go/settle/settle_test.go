package settle

import (
	"math"
	"os"
	"path/filepath"
	"testing"
)

const sampleDiff = `diff --git a/api/handler.go b/api/handler.go
index 1111111..2222222 100644
--- a/api/handler.go
+++ b/api/handler.go
@@ -10,6 +10,8 @@ func handle(w http.ResponseWriter, r *http.Request) {
 	if r.Method != http.MethodPost {
+		w.WriteHeader(http.StatusMethodNotAllowed)
+		return
 	}
 }
`

func verdict(persona, decision string, confidence float64) Verdict {
	return Verdict{
		Schema:     SchemaVerdict,
		Persona:    persona,
		Decision:   decision,
		Confidence: confidence,
		Reasoning:  "reasoning from " + persona + " about the diff under review.",
	}
}

func initStore(t *testing.T) *Store {
	t.Helper()
	store, err := Init(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return store
}

func TestInitAndFindStore(t *testing.T) {
	dir := t.TempDir()
	store, err := Init(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Init(dir); err == nil {
		t.Fatal("second init should refuse to overwrite")
	}

	// FindStore walks up from a nested directory.
	nested := filepath.Join(dir, "a", "b")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	found, err := FindStore(nested)
	if err != nil {
		t.Fatal(err)
	}
	if found.Root != store.Root {
		t.Errorf("found %s, want %s", found.Root, store.Root)
	}

	cfg, err := store.LoadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Schema != SchemaConfig || len(cfg.Panel.Personas) != 5 {
		t.Errorf("unexpected default config: %+v", cfg)
	}
	ledger, err := store.LoadLedger()
	if err != nil {
		t.Fatal(err)
	}
	for p, e := range ledger.Personas {
		if e.VotingPower != 1.0 {
			t.Errorf("persona %s starts at power %v, want 1.0", p, e.VotingPower)
		}
	}
}

func TestBuildPanelSizesByDiff(t *testing.T) {
	store := initStore(t)
	cfg, _ := store.LoadConfig()
	ledger, _ := store.LoadLedger()

	small := BuildPanel(cfg, ledger, []byte(sampleDiff))
	if len(small.Seats) != cfg.Panel.MinSeats {
		t.Errorf("small diff seated %d, want %d", len(small.Seats), cfg.Panel.MinSeats)
	}
	if len(small.Subject.Files) != 1 || small.Subject.Files[0] != "api/handler.go" {
		t.Errorf("files = %v", small.Subject.Files)
	}
	if small.Subject.DiffSHA256 == "" {
		t.Error("diff hash missing")
	}

	// A quarantined persona keeps a shadow seat at weight 0.
	ledger.Personas["security"].Quarantined = true
	big := make([]byte, 0, 4096)
	for i := 0; i < 60; i++ {
		big = append(big, []byte("+new line of code\n")...)
	}
	full := BuildPanel(cfg, ledger, append([]byte(sampleDiff), big...))
	if len(full.Seats) != len(cfg.Panel.Personas) {
		t.Errorf("large diff seated %d, want %d", len(full.Seats), len(cfg.Panel.Personas))
	}
	for _, seat := range full.Seats {
		if seat.Persona == "security" {
			if !seat.Quarantined || seat.VotingPower != 0 {
				t.Errorf("quarantined seat = %+v, want shadow seat at weight 0", seat)
			}
		}
	}
}

func TestRunTallyApproveWithDissent(t *testing.T) {
	store := initStore(t)
	cfg, _ := store.LoadConfig()
	ledger, _ := store.LoadLedger()
	panel := BuildPanel(cfg, ledger, []byte(sampleDiff))

	verdicts := []Verdict{
		verdict("correctness", VerdictApprove, 0.9),
		verdict("security", VerdictApprove, 0.8),
		verdict("api-contract", VerdictReject, 0.6),
	}
	decision, err := RunTally(panel, verdicts, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if !decision.Outcome.Reached || decision.Outcome.Decision != VerdictApprove {
		t.Fatalf("outcome = %+v, want approve", decision.Outcome)
	}
	if len(decision.Outcome.Dissents) != 1 || decision.Outcome.Dissents[0] != "api-contract" {
		t.Errorf("dissents = %v, want [api-contract]", decision.Outcome.Dissents)
	}
	// Weighted votes are power×confidence over total power:
	// approve = (0.9+0.8)/3.0 ≈ 56.7%
	if math.Abs(decision.Outcome.Agreement-1.7/3.0) > 0.01 {
		t.Errorf("agreement = %v, want ≈ %.3f", decision.Outcome.Agreement, 1.7/3.0)
	}
}

func TestRunTallyNoConsensus(t *testing.T) {
	store := initStore(t)
	cfg, _ := store.LoadConfig()
	ledger, _ := store.LoadLedger()
	panel := BuildPanel(cfg, ledger, []byte(sampleDiff))

	verdicts := []Verdict{
		verdict("correctness", VerdictApprove, 0.8),
		verdict("security", VerdictReject, 0.8),
	}
	decision, err := RunTally(panel, verdicts, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if decision.Outcome.Reached {
		t.Fatalf("expected no consensus on a 50/50 split, got %+v", decision.Outcome)
	}
}

func TestRunTallyRejectsUnknownPersonaAndBadVerdicts(t *testing.T) {
	store := initStore(t)
	cfg, _ := store.LoadConfig()
	ledger, _ := store.LoadLedger()
	panel := BuildPanel(cfg, ledger, []byte(sampleDiff))

	if _, err := RunTally(panel, []Verdict{verdict("impostor", VerdictApprove, 0.9)}, cfg); err == nil {
		t.Error("expected error for persona without a seat")
	}
	if _, err := RunTally(panel, []Verdict{verdict("security", "maybe", 0.9)}, cfg); err == nil {
		t.Error("expected error for invalid decision enum")
	}
	if _, err := RunTally(panel, []Verdict{verdict("security", VerdictApprove, 1.5)}, cfg); err == nil {
		t.Error("expected error for out-of-range confidence")
	}
}

func TestDecisionRoundTripAndOutcomeGrading(t *testing.T) {
	store := initStore(t)
	cfg, _ := store.LoadConfig()
	ledger, _ := store.LoadLedger()
	panel := BuildPanel(cfg, ledger, []byte(sampleDiff))

	verdicts := []Verdict{
		verdict("correctness", VerdictApprove, 0.9),
		verdict("security", VerdictApprove, 0.8),
		verdict("api-contract", VerdictReject, 0.6),
	}
	decision, err := RunTally(panel, verdicts, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.AppendDecision(decision); err != nil {
		t.Fatal(err)
	}

	got, err := store.GetDecision(decision.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Result != "" {
		t.Errorf("ungraded decision has result %q", got.Result)
	}

	// The approved change was later reverted: approvers pay, the dissenting
	// rejector gains.
	before := map[string]float64{}
	for p, e := range ledger.Personas {
		before[p] = e.VotingPower
	}
	if err := ApplyOutcome(&ledger, decision, "reverted", cfg.Ledger); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveLedger(ledger); err != nil {
		t.Fatal(err)
	}
	if err := store.AppendOutcomeEvent(NewOutcomeEvent(decision.ID, "reverted")); err != nil {
		t.Fatal(err)
	}

	if ledger.Personas["correctness"].VotingPower >= before["correctness"] {
		t.Error("misjudging approver should lose voting power")
	}
	if ledger.Personas["api-contract"].VotingPower <= before["api-contract"] {
		t.Error("vindicated dissenter should gain voting power")
	}
	// Asymmetry: the penalty outweighs the reward.
	loss := before["correctness"] - ledger.Personas["correctness"].VotingPower
	gain := ledger.Personas["api-contract"].VotingPower - before["api-contract"]
	if loss <= gain {
		t.Errorf("penalty (%v) should exceed reward (%v)", loss, gain)
	}

	// The outcome event folds back into the decision on read.
	graded, err := store.GetDecision(decision.ID)
	if err != nil {
		t.Fatal(err)
	}
	if graded.Result != "reverted" {
		t.Errorf("graded result = %q, want reverted", graded.Result)
	}

	// Double-grading is refused by ApplyOutcome preconditions at CLI level;
	// here we verify the ledger math is repeatable input-wise but the CLI
	// guard relies on Result being set.
	if graded.Outcome.Decision != VerdictApprove {
		t.Errorf("outcome decision = %q", graded.Outcome.Decision)
	}
}

func TestQuarantineAfterRepeatedMisjudgment(t *testing.T) {
	store := initStore(t)
	cfg, _ := store.LoadConfig()
	ledger, _ := store.LoadLedger()
	panel := BuildPanel(cfg, ledger, []byte(sampleDiff))

	verdicts := []Verdict{
		verdict("correctness", VerdictApprove, 0.9),
		verdict("security", VerdictApprove, 0.9),
		verdict("api-contract", VerdictApprove, 0.9),
	}
	decision, err := RunTally(panel, verdicts, cfg)
	if err != nil {
		t.Fatal(err)
	}

	// Repeatedly approve changes that get reverted; power decays toward the
	// floor and quarantine engages below the threshold.
	for i := 0; i < 8; i++ {
		if err := ApplyOutcome(&ledger, decision, "reverted", cfg.Ledger); err != nil {
			t.Fatal(err)
		}
	}
	e := ledger.Personas["correctness"]
	if !e.Quarantined {
		t.Errorf("after 8 misjudgments power=%v, expected quarantine below %v", e.VotingPower, cfg.Ledger.QuarantineBelow)
	}
	if e.VotingPower < cfg.Ledger.Min {
		t.Errorf("power %v fell below floor %v", e.VotingPower, cfg.Ledger.Min)
	}

	// A quarantined persona's next panel seat is a shadow seat.
	panel2 := BuildPanel(cfg, ledger, []byte(sampleDiff))
	for _, seat := range panel2.Seats {
		if seat.Persona == "correctness" && (seat.VotingPower != 0 || !seat.Quarantined) {
			t.Errorf("quarantined persona seat = %+v, want weight 0", seat)
		}
	}
}

func TestLoadVerdictsFromDir(t *testing.T) {
	store := initStore(t)
	dir := store.VerdictsDir("task1")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, v := range []Verdict{
		verdict("security", VerdictApprove, 0.8),
		verdict("correctness", VerdictApprove, 0.9),
	} {
		if err := writeJSON(filepath.Join(dir, v.Persona+".json"), v); err != nil {
			t.Fatal(err)
		}
	}
	got, err := LoadVerdicts(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Persona != "correctness" {
		t.Errorf("verdicts = %+v, want 2 sorted by persona", got)
	}
}
