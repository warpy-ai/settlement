package settle

import (
	"fmt"
	"time"
)

// ApplyOutcome updates the punitive ledger from ground truth
// (docs/SETTLE_SKILL_MEMORY_AND_PUNITIVE.md §3.1):
//
//	VP ← clamp(VP + reward·aligned − penalty·misjudged − decay·(VP−1), min, max)
//
// A persona is "aligned" when its vote agreed with what reality showed:
// approving a change that held, or dissenting on a change that was reverted.
// The asymmetry (penalty > reward) makes rubber-stamping the expensive
// failure mode, and dissent that ages well pays off. Below QuarantineBelow a
// persona keeps its seat but shadow-votes at weight 0 until it re-earns
// standing; sustained alignment lifts the quarantine.
func ApplyOutcome(ledger *Ledger, decision Decision, result string, p LedgerParams) error {
	if result != "held" && result != "reverted" {
		return fmt.Errorf("result must be \"held\" or \"reverted\", got %q", result)
	}
	if !decision.Outcome.Reached {
		return fmt.Errorf("decision %s reached no consensus; there is no panel position to score", decision.ID)
	}

	changeGood := result == "held"

	for _, v := range decision.Verdicts {
		entry, ok := ledger.Personas[v.Persona]
		if !ok {
			entry = &LedgerEntry{VotingPower: 1.0}
			ledger.Personas[v.Persona] = entry
		}

		// Aligned = voted to accept a change that held, or voted against a
		// change that was (or would have been) reverted.
		aligned := (v.Decision == VerdictApprove) == changeGood

		vp := entry.VotingPower
		if aligned {
			vp += p.Reward
			entry.Aligned++
		} else {
			vp -= p.Penalty
			entry.Misjudged++
		}
		vp -= p.Decay * (vp - 1.0)
		if vp < p.Min {
			vp = p.Min
		}
		if vp > p.Max {
			vp = p.Max
		}
		entry.VotingPower = vp
		entry.Quarantined = vp < p.QuarantineBelow
		entry.Reviews++
		entry.UpdatedAt = time.Now().UTC()
	}
	return nil
}
