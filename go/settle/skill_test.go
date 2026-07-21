package settle

import (
	"testing"
	"time"
)

func TestNewDecisionIDUniquePerNanosecond(t *testing.T) {
	base := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	// Same diff, same second, different nanosecond — must not collide.
	a := newDecisionID("deadbeef", base)
	b := newDecisionID("deadbeef", base.Add(time.Nanosecond))
	if a == b {
		t.Fatalf("same-diff same-second decisions collided: %s", a)
	}
	if a[:len("dec_20260101T120000")] != "dec_20260101T120000" {
		t.Fatalf("id lost its readable timestamp prefix: %s", a)
	}
}

func guidedDecision(id string, guidedBy ...string) Decision {
	d := decisionAt(id, 1, []string{"api/handler.go"}, []Verdict{verdict("security", "approve", 0.9)}, nil)
	d.Subject.GuidedBy = guidedBy
	return d
}

func TestApplyAdequacyRevertPenalizes(t *testing.T) {
	sl := SkillLedger{Skills: map[string]*SkillEntry{}}
	if err := ApplyAdequacy(&sl, guidedDecision("d1", "mem-1"), "reverted", DefaultAdequacyParams()); err != nil {
		t.Fatal(err)
	}
	e := sl.Skills["mem-1"]
	if e == nil || e.Adequacy >= 1.0 {
		t.Fatalf("revert should drop adequacy below 1.0, got %+v", e)
	}
	if e.Reverted != 1 || e.Uses != 1 {
		t.Fatalf("expected reverted=1 uses=1, got %+v", e)
	}
}

func TestApplyAdequacyHeldRewards(t *testing.T) {
	sl := SkillLedger{Skills: map[string]*SkillEntry{}}
	_ = ApplyAdequacy(&sl, guidedDecision("d1", "mem-1"), "held", DefaultAdequacyParams())
	if e := sl.Skills["mem-1"]; e == nil || e.Adequacy <= 1.0 || e.Held != 1 {
		t.Fatalf("held should lift adequacy above 1.0, got %+v", e)
	}
}

func TestApplyAdequacyQuarantinesAfterRepeatedReverts(t *testing.T) {
	sl := SkillLedger{Skills: map[string]*SkillEntry{}}
	p := DefaultAdequacyParams()
	for i := 0; i < 8; i++ {
		if err := ApplyAdequacy(&sl, guidedDecision("d", "mem-1"), "reverted", p); err != nil {
			t.Fatal(err)
		}
	}
	e := sl.Skills["mem-1"]
	if !e.Quarantined {
		t.Fatalf("sustained reverts should quarantine, got adequacy %.2f", e.Adequacy)
	}
	if e.Adequacy < p.Min {
		t.Fatalf("adequacy %.2f fell below floor %.2f", e.Adequacy, p.Min)
	}
}

func TestApplyAdequacyIgnoresUnguidedDecisions(t *testing.T) {
	sl := SkillLedger{Skills: map[string]*SkillEntry{}}
	_ = ApplyAdequacy(&sl, guidedDecision("d1"), "reverted", DefaultAdequacyParams())
	if len(sl.Skills) != 0 {
		t.Fatalf("a decision with no guidance should score nothing, got %v", sl.Skills)
	}
}

func TestApplyAdequacyRejectsBadResult(t *testing.T) {
	sl := SkillLedger{Skills: map[string]*SkillEntry{}}
	if err := ApplyAdequacy(&sl, guidedDecision("d1", "mem-1"), "maybe", DefaultAdequacyParams()); err == nil {
		t.Fatal("expected error on invalid result")
	}
}
