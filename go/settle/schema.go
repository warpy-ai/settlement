// Package settle implements the local-first settlement store and commands
// used by the settle CLI and the /settle skill: persona review panels over
// git diffs, adjudicated by the shared consensus engine, with decisions and
// a punitive voting-power ledger recorded under .settlement/ in the repo.
package settle

import (
	"fmt"
	"time"
)

// Verdict decisions a persona reviewer may return.
const (
	VerdictApprove = "approve"
	VerdictReject  = "reject"
	VerdictRevise  = "revise"
)

// Config is .settlement/config.json.
type Config struct {
	Schema    string          `json:"schema"`
	Panel     PanelConfig     `json:"panel"`
	Consensus ConsensusParams `json:"consensus"`
	Ledger    LedgerParams    `json:"ledger"`
}

type PanelConfig struct {
	// Personas in priority order; small diffs seat the first MinSeats,
	// large diffs seat everyone.
	Personas []string `json:"personas"`
	MinSeats int      `json:"min_seats"`
	// Diffs at or above either threshold seat the full panel.
	LargeDiffFiles int `json:"large_diff_files"`
	LargeDiffLines int `json:"large_diff_lines"`
}

type ConsensusParams struct {
	Strategy         string  `json:"strategy"`
	MinimumAgreement float64 `json:"minimum_agreement"`
}

// LedgerParams tunes the punitive voting-power rule
// (docs/SETTLE_SKILL_MEMORY_AND_PUNITIVE.md §3.1).
type LedgerParams struct {
	Reward          float64 `json:"reward"`           // α: vote aligned with ground truth
	Penalty         float64 `json:"penalty"`          // β: vote misjudged (β > α punishes rubber-stamping)
	Decay           float64 `json:"decay"`            // γ: pull toward 1.0 each update
	Min             float64 `json:"min"`              // voting-power floor
	Max             float64 `json:"max"`              // voting-power cap
	QuarantineBelow float64 `json:"quarantine_below"` // below this, persona shadow-votes at weight 0
}

// Ledger is .settlement/ledger.json.
type Ledger struct {
	Schema   string                  `json:"schema"`
	Personas map[string]*LedgerEntry `json:"personas"`
}

type LedgerEntry struct {
	VotingPower float64   `json:"voting_power"`
	Quarantined bool      `json:"quarantined"`
	Reviews     int       `json:"reviews"`
	Aligned     int       `json:"aligned"`
	Misjudged   int       `json:"misjudged"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// PanelSpec is the output of `settle panel`: which personas review this diff.
type PanelSpec struct {
	Schema    string          `json:"schema"`
	Subject   Subject         `json:"subject"`
	Consensus ConsensusParams `json:"consensus"`
	Seats     []PanelSeat     `json:"seats"`
}

type PanelSeat struct {
	Persona     string  `json:"persona"`
	VotingPower float64 `json:"voting_power"`
	Quarantined bool    `json:"quarantined"` // shadow seat: still reviews, weight 0
	PromptFile  string  `json:"prompt_file,omitempty"`
}

// Subject identifies what was reviewed.
type Subject struct {
	Type       string   `json:"type"` // "diff"
	DiffSHA256 string   `json:"diff_sha256,omitempty"`
	Files      []string `json:"files,omitempty"`
	Lines      int      `json:"lines,omitempty"` // changed (+/-) line count
}

// Verdict is one persona's review, written by the host assistant's subagent.
type Verdict struct {
	Schema     string    `json:"schema"`
	Persona    string    `json:"persona"`
	Decision   string    `json:"decision"` // approve | reject | revise
	Confidence float64   `json:"confidence"`
	Reasoning  string    `json:"reasoning"`
	Findings   []Finding `json:"findings,omitempty"`
}

type Finding struct {
	Severity string `json:"severity"` // low | medium | high
	File     string `json:"file,omitempty"`
	Line     int    `json:"line,omitempty"`
	Summary  string `json:"summary"`
}

// Validate checks the verdict contract.
func (v *Verdict) Validate() error {
	if v.Persona == "" {
		return fmt.Errorf("verdict missing persona")
	}
	switch v.Decision {
	case VerdictApprove, VerdictReject, VerdictRevise:
	default:
		return fmt.Errorf("verdict %q for persona %s: decision must be approve, reject, or revise", v.Decision, v.Persona)
	}
	if v.Confidence < 0 || v.Confidence > 1 {
		return fmt.Errorf("verdict for persona %s: confidence %v outside [0,1]", v.Persona, v.Confidence)
	}
	return nil
}

// Decision is one adjudicated review, appended to .settlement/decisions.jsonl.
type Decision struct {
	Schema    string      `json:"schema"`
	ID        string      `json:"id"`
	CreatedAt time.Time   `json:"created_at"`
	Subject   Subject     `json:"subject"`
	Panel     []PanelSeat `json:"panel"`
	Verdicts  []Verdict   `json:"verdicts"`
	Outcome   Outcome     `json:"outcome"`
	// Result is ground truth recorded later by `settle outcome`
	// ("held" or "reverted"); empty until then.
	Result string `json:"result,omitempty"`
}

// Outcome summarizes the consensus result for a decision.
type Outcome struct {
	Reached   bool     `json:"reached"`
	Decision  string   `json:"decision,omitempty"`
	Agreement float64  `json:"agreement,omitempty"` // winning group's weighted share
	Dissents  []string `json:"dissents,omitempty"`  // personas that voted against the winner
	// ResponseJSON is the full consensus Response as emitted by the engine.
	ResponseJSON string `json:"response_json,omitempty"`
}

// OutcomeEvent is appended to decisions.jsonl by `settle outcome`,
// keeping the log append-only while recording ground truth.
type OutcomeEvent struct {
	Schema     string    `json:"schema"`
	DecisionID string    `json:"decision_id"`
	Result     string    `json:"result"` // held | reverted
	RecordedAt time.Time `json:"recorded_at"`
}

// NewOutcomeEvent builds the append-only ground-truth record for a decision.
func NewOutcomeEvent(decisionID, result string) OutcomeEvent {
	return OutcomeEvent{
		Schema:     SchemaOutcome,
		DecisionID: decisionID,
		Result:     result,
		RecordedAt: time.Now().UTC(),
	}
}

// Schema tags.
const (
	SchemaConfig   = "settle/config@1"
	SchemaLedger   = "settle/ledger@1"
	SchemaPanel    = "settle/panel@1"
	SchemaVerdict  = "settle/verdict@1"
	SchemaDecision = "settle/decision@1"
	SchemaOutcome  = "settle/outcome@1"
)

// DefaultConfig returns the config written by `settle init`.
func DefaultConfig() Config {
	return Config{
		Schema: SchemaConfig,
		Panel: PanelConfig{
			Personas:       []string{"correctness", "security", "api-contract", "simplicity", "tests"},
			MinSeats:       3,
			LargeDiffFiles: 3,
			LargeDiffLines: 50,
		},
		Consensus: ConsensusParams{
			Strategy:         "exact_match",
			MinimumAgreement: 0.6,
		},
		Ledger: LedgerParams{
			Reward:          0.05,
			Penalty:         0.15,
			Decay:           0.02,
			Min:             0.1,
			Max:             2.5,
			QuarantineBelow: 0.4,
		},
	}
}
