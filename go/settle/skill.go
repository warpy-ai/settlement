package settle

import (
	"fmt"
	"time"
)

// The skill ledger scores the procedural tier — settled memory notes acting as
// learned procedures — by the real-world outcomes of the changes they guided
// (docs/SETTLE_SKILL_MEMORY_AND_PUNITIVE.md §3.2). It is the same punitive
// mechanism as the persona voting-power ledger (§3.1), applied to a second
// subject: "one ledger, three subjects." Guidance that consistently precedes
// reverts is quarantined and stops being auto-injected until it re-earns
// standing — bad procedures pruned by evidence, not noticed by accident.

// SchemaSkills tags the skill ledger.
const SchemaSkills = "settle/skills@1"

// SkillLedger is .settlement/skills.json, keyed by the memory-note id that
// guided the change.
type SkillLedger struct {
	Schema string                 `json:"schema"`
	Skills map[string]*SkillEntry `json:"skills"`
}

// SkillEntry is one note's adequacy record.
type SkillEntry struct {
	Adequacy    float64   `json:"adequacy"`
	Uses        int       `json:"uses"`
	Held        int       `json:"held"`
	Reverted    int       `json:"reverted"`
	Quarantined bool      `json:"quarantined"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// AdequacyParams tunes skill scoring. Defaults are code constants (not
// persisted in config.json) so existing stores need no migration.
type AdequacyParams struct {
	Reward          float64 // credit when a guided change held
	Penalty         float64 // debit when a guided change was reverted (> Reward)
	Decay           float64 // pull toward 1.0 each update
	Min             float64 // adequacy floor
	Max             float64 // adequacy cap
	QuarantineBelow float64 // below this, the note is excluded from auto-injection
}

// DefaultAdequacyParams mirrors the persona ledger's asymmetry: reverts cost
// more than clean outcomes credit.
func DefaultAdequacyParams() AdequacyParams {
	return AdequacyParams{Reward: 0.05, Penalty: 0.15, Decay: 0.02, Min: 0.1, Max: 2.5, QuarantineBelow: 0.4}
}

// ApplyAdequacy propagates a graded decision's outcome to the adequacy scores
// of the memory notes that guided it (decision.Subject.GuidedBy). A note whose
// guided change held gains adequacy; one whose guided change was reverted loses
// more. Below QuarantineBelow the note is quarantined.
func ApplyAdequacy(sl *SkillLedger, decision Decision, result string, p AdequacyParams) error {
	if result != "held" && result != "reverted" {
		return fmt.Errorf("result must be \"held\" or \"reverted\", got %q", result)
	}
	if sl.Skills == nil {
		sl.Skills = map[string]*SkillEntry{}
	}

	good := result == "held"
	for _, id := range decision.Subject.GuidedBy {
		e, ok := sl.Skills[id]
		if !ok {
			e = &SkillEntry{Adequacy: 1.0}
			sl.Skills[id] = e
		}
		a := e.Adequacy
		if good {
			a += p.Reward
			e.Held++
		} else {
			a -= p.Penalty
			e.Reverted++
		}
		a -= p.Decay * (a - 1.0)
		if a < p.Min {
			a = p.Min
		}
		if a > p.Max {
			a = p.Max
		}
		e.Adequacy = a
		e.Quarantined = a < p.QuarantineBelow
		e.Uses++
		e.UpdatedAt = time.Now().UTC()
	}
	return nil
}
