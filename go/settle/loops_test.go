package settle

import "testing"

func drive(ll *LoopLedger, loop string, results ...string) error {
	p := DefaultLoopParams()
	for _, r := range results {
		if err := ApplyLoopOutcome(ll, loop, r, p); err != nil {
			return err
		}
	}
	return nil
}

func TestLoopStartsProposeOnly(t *testing.T) {
	ll := LoopLedger{Loops: map[string]*LoopEntry{}}
	if err := drive(&ll, "drift", "held"); err != nil {
		t.Fatal(err)
	}
	if ll.Loops["drift"].Trust != TrustProposeOnly {
		t.Fatalf("one hold should not promote off propose-only, got %s", ll.Loops["drift"].Trust)
	}
}

func TestLoopPromotesAfterCleanStreak(t *testing.T) {
	ll := LoopLedger{Loops: map[string]*LoopEntry{}}
	// PromoteAfter=5 holds → one rung, streak resets.
	if err := drive(&ll, "drift", "held", "held", "held", "held", "held"); err != nil {
		t.Fatal(err)
	}
	e := ll.Loops["drift"]
	if e.Trust != TrustAutoTrivial {
		t.Fatalf("five clean holds should promote to %s, got %s", TrustAutoTrivial, e.Trust)
	}
	if e.CleanStreak != 0 {
		t.Fatalf("streak should reset on promotion, got %d", e.CleanStreak)
	}
}

func TestLoopRevertDemotesOneRung(t *testing.T) {
	ll := LoopLedger{Loops: map[string]*LoopEntry{}}
	_ = drive(&ll, "drift", "held", "held", "held", "held", "held") // -> auto-merge-trivial
	if err := drive(&ll, "drift", "reverted"); err != nil {
		t.Fatal(err)
	}
	e := ll.Loops["drift"]
	if e.Trust != TrustProposeOnly {
		t.Fatalf("a revert should demote one rung to %s, got %s", TrustProposeOnly, e.Trust)
	}
	if e.CleanStreak != 0 || e.Reverted != 1 {
		t.Fatalf("revert should reset streak and count, got %+v", e)
	}
}

func TestLoopDemoteFloorsAtProposeOnly(t *testing.T) {
	ll := LoopLedger{Loops: map[string]*LoopEntry{}}
	if err := drive(&ll, "drift", "reverted", "reverted"); err != nil {
		t.Fatal(err)
	}
	if got := ll.Loops["drift"].Trust; got != TrustProposeOnly {
		t.Fatalf("cannot demote below propose-only, got %s", got)
	}
}

func TestLoopPromoteCeilingAtAutoClass(t *testing.T) {
	ll := LoopLedger{Loops: map[string]*LoopEntry{}}
	// 10 holds → two promotions (trivial, then class); further holds hold at top.
	holds := make([]string, 15)
	for i := range holds {
		holds[i] = "held"
	}
	if err := drive(&ll, "drift", holds...); err != nil {
		t.Fatal(err)
	}
	if got := ll.Loops["drift"].Trust; got != TrustAutoClass {
		t.Fatalf("should cap at %s, got %s", TrustAutoClass, got)
	}
}

func TestApplyLoopOutcomeRejectsBadResult(t *testing.T) {
	ll := LoopLedger{Loops: map[string]*LoopEntry{}}
	if err := ApplyLoopOutcome(&ll, "drift", "merged", DefaultLoopParams()); err == nil {
		t.Fatal("expected error on invalid result")
	}
}
