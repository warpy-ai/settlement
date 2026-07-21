package settle

import (
	"fmt"
	"time"
)

// The loop ledger is the third subject of the one-ledger mechanism
// (docs/SETTLE_SKILL_MEMORY_AND_PUNITIVE.md §3.3): autonomous loops earn
// autonomy by track record. A loop begins propose-only and is promoted a rung
// after a clean streak of held outcomes; any revert of a change it originated
// demotes it one rung immediately. Trust is earned slowly and lost fast — the
// same asymmetry that governs personas and skills.

// SchemaLoops tags the loop ledger.
const SchemaLoops = "settle/loops@1"

// Trust rungs, lowest to highest.
const (
	TrustProposeOnly = "propose-only"       // may only open proposals for review
	TrustAutoTrivial = "auto-merge-trivial" // may auto-merge trivial changes
	TrustAutoClass   = "auto-merge-class"   // may auto-merge a whole change class
)

// trustRungs is the promotion ladder in order.
var trustRungs = []string{TrustProposeOnly, TrustAutoTrivial, TrustAutoClass}

// LoopLedger is .settlement/loops.json, keyed by loop name.
type LoopLedger struct {
	Schema string                `json:"schema"`
	Loops  map[string]*LoopEntry `json:"loops"`
}

// LoopEntry is one loop's trust record.
type LoopEntry struct {
	Trust       string    `json:"trust"`
	Merges      int       `json:"merges"` // graded outcomes attributed to this loop
	Held        int       `json:"held"`
	Reverted    int       `json:"reverted"`
	CleanStreak int       `json:"clean_streak"` // consecutive holds since the last rung change
	UpdatedAt   time.Time `json:"updated_at"`
}

// LoopParams tunes promotion. Defaults are code constants (not persisted) so
// existing stores need no migration.
type LoopParams struct {
	PromoteAfter int // consecutive held outcomes that earn one promotion
}

// DefaultLoopParams requires a five-clean streak per rung.
func DefaultLoopParams() LoopParams {
	return LoopParams{PromoteAfter: 5}
}

// ApplyLoopOutcome moves a loop's trust from the ground truth of a change it
// originated: a held change extends the clean streak (and promotes a rung once
// the streak is earned); a reverted change resets the streak and demotes a
// rung. Trust never rises on the same outcome that a revert would fall from.
func ApplyLoopOutcome(ll *LoopLedger, loop, result string, p LoopParams) error {
	if result != "held" && result != "reverted" {
		return fmt.Errorf("result must be \"held\" or \"reverted\", got %q", result)
	}
	if ll.Loops == nil {
		ll.Loops = map[string]*LoopEntry{}
	}
	e, ok := ll.Loops[loop]
	if !ok {
		e = &LoopEntry{Trust: TrustProposeOnly}
		ll.Loops[loop] = e
	}

	e.Merges++
	if result == "held" {
		e.Held++
		e.CleanStreak++
		if e.CleanStreak >= p.PromoteAfter && promote(e) {
			e.CleanStreak = 0
		}
	} else {
		e.Reverted++
		e.CleanStreak = 0
		demote(e)
	}
	e.UpdatedAt = time.Now().UTC()
	return nil
}

// promote raises a loop one rung; it reports whether a rung was available.
func promote(e *LoopEntry) bool {
	i := rungIndex(e.Trust)
	if i+1 >= len(trustRungs) {
		return false
	}
	e.Trust = trustRungs[i+1]
	return true
}

// demote lowers a loop one rung (never below propose-only).
func demote(e *LoopEntry) {
	if i := rungIndex(e.Trust); i > 0 {
		e.Trust = trustRungs[i-1]
	}
}

func rungIndex(trust string) int {
	for i, r := range trustRungs {
		if r == trust {
			return i
		}
	}
	return 0 // unknown value is treated as the lowest rung
}
