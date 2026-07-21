package settle

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"settlement-core/consensus"
)

// RunTally adjudicates a panel's verdicts through the shared consensus engine
// and produces the decision record. Fully offline (nil Synthesizer).
func RunTally(panel PanelSpec, verdicts []Verdict, cfg Config) (Decision, error) {
	if len(verdicts) == 0 {
		return Decision{}, fmt.Errorf("no verdicts to tally")
	}

	seatWeight := make(map[string]PanelSeat, len(panel.Seats))
	for _, seat := range panel.Seats {
		seatWeight[seat.Persona] = seat
	}

	results := make([]consensus.Result, 0, len(verdicts))
	for _, v := range verdicts {
		if err := v.Validate(); err != nil {
			return Decision{}, err
		}
		seat, ok := seatWeight[v.Persona]
		if !ok {
			return Decision{}, fmt.Errorf("verdict from persona %q which has no seat on the panel", v.Persona)
		}
		results = append(results, consensus.Result{
			WorkerID:    v.Persona,
			VotingPower: seat.VotingPower,
			Timestamp:   time.Now().UTC(),
			Response: &consensus.Response{
				Decision:   v.Decision,
				Confidence: v.Confidence,
				Category:   "code-review",
				Reasoning:  v.Reasoning,
			},
		})
	}

	question := "code review: " + strings.Join(panel.Subject.Files, ", ")
	ccfg := consensus.Config{
		MinimumAgreement: panel.Consensus.MinimumAgreement,
		MatchStrategy:    consensus.Strategy(panel.Consensus.Strategy),
	}

	reached, raw := consensus.Tally(context.Background(), question, results, ccfg, nil)

	outcome := Outcome{Reached: reached, ResponseJSON: raw}
	if reached {
		var resp consensus.Response
		if err := json.Unmarshal([]byte(raw), &resp); err != nil {
			return Decision{}, fmt.Errorf("consensus engine returned invalid JSON: %w", err)
		}
		outcome.Decision = resp.Decision
		if agreement, ok := resp.Metadata["actual_agreement"].(float64); ok {
			outcome.Agreement = agreement
		}
		for _, v := range verdicts {
			if v.Decision != resp.Decision {
				outcome.Dissents = append(outcome.Dissents, v.Persona)
			}
		}
		sort.Strings(outcome.Dissents)
	}

	return Decision{
		Schema:    SchemaDecision,
		ID:        newDecisionID(panel.Subject.DiffSHA256),
		CreatedAt: time.Now().UTC(),
		Subject:   panel.Subject,
		Panel:     panel.Seats,
		Verdicts:  verdicts,
		Outcome:   outcome,
	}, nil
}

// LoadVerdicts reads every *.json verdict in dir.
func LoadVerdicts(dir string) ([]Verdict, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var verdicts []Verdict
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		var v Verdict
		if err := readJSON(filepath.Join(dir, e.Name()), &v); err != nil {
			return nil, fmt.Errorf("verdict %s: %w", e.Name(), err)
		}
		verdicts = append(verdicts, v)
	}
	sort.Slice(verdicts, func(i, j int) bool { return verdicts[i].Persona < verdicts[j].Persona })
	return verdicts, nil
}

func newDecisionID(diffHash string) string {
	suffix := diffHash
	if len(suffix) > 8 {
		suffix = suffix[:8]
	}
	if suffix == "" {
		suffix = "nodiff"
	}
	return fmt.Sprintf("dec_%s_%s", time.Now().UTC().Format("20060102T150405"), suffix)
}
