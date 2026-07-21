package settle

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
)

// BuildPanel deterministically selects the review panel for a diff: personas
// come from config order with weights from the ledger; small diffs seat the
// first MinSeats personas, large diffs seat everyone. Quarantined personas
// keep their seat as shadow voters (weight 0) so the ledger can keep scoring
// them toward rehabilitation.
func BuildPanel(cfg Config, ledger Ledger, diff []byte) PanelSpec {
	subject := describeDiff(diff)

	seatCount := cfg.Panel.MinSeats
	if seatCount <= 0 || seatCount > len(cfg.Panel.Personas) {
		seatCount = len(cfg.Panel.Personas)
	}
	if len(subject.Files) >= cfg.Panel.LargeDiffFiles || subject.Lines >= cfg.Panel.LargeDiffLines {
		seatCount = len(cfg.Panel.Personas)
	}

	seats := make([]PanelSeat, 0, seatCount)
	for _, persona := range cfg.Panel.Personas[:seatCount] {
		seat := PanelSeat{
			Persona:     persona,
			VotingPower: 1.0,
			PromptFile:  "skill/settle/personas/" + persona + ".md",
		}
		if entry, ok := ledger.Personas[persona]; ok {
			seat.VotingPower = entry.VotingPower
			seat.Quarantined = entry.Quarantined
		}
		if seat.Quarantined {
			seat.VotingPower = 0 // shadow vote: reviewed and scored, never decisive
		}
		seats = append(seats, seat)
	}

	return PanelSpec{
		Schema:    SchemaPanel,
		Subject:   subject,
		Consensus: cfg.Consensus,
		Seats:     seats,
	}
}

// describeDiff extracts changed files, changed-line count, and a content hash
// from a unified diff.
func describeDiff(diff []byte) Subject {
	subject := Subject{Type: "diff"}
	if len(diff) == 0 {
		return subject
	}

	sum := sha256.Sum256(diff)
	subject.DiffSHA256 = hex.EncodeToString(sum[:])

	for _, line := range strings.Split(string(diff), "\n") {
		switch {
		case strings.HasPrefix(line, "+++ b/"):
			subject.Files = append(subject.Files, strings.TrimPrefix(line, "+++ b/"))
		case strings.HasPrefix(line, "+") && !strings.HasPrefix(line, "+++"):
			subject.Lines++
		case strings.HasPrefix(line, "-") && !strings.HasPrefix(line, "---"):
			subject.Lines++
		}
	}
	return subject
}
