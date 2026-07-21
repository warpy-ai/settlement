package settle

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Memory is the procedural/semantic tier of the settlement store: consolidated
// notes distilled from episodic memory (recorded decisions) by a curator, each
// carrying provenance edges back to the decisions that justify it. Learning is
// a change proposal here — a note is written `proposed`, gated by the retrieval
// oracle, and only becomes `settled` after a panel adjudicates it (see
// docs/SETTLE_SKILL_MEMORY_AND_PUNITIVE.md §2–3). Notes live as markdown under
// .settlement/memory/ so they are committed with the code and reviewable in a
// PR like any other change.

// Memory note lifecycle.
const (
	MemProposed   = "proposed"   // drafted, oracle-gated, awaiting settlement
	MemSettled    = "settled"    // a panel approved it; it is active memory
	MemRejected   = "rejected"   // a panel rejected it
	MemSuperseded = "superseded" // a later settled note replaced it
)

// MemoryNote is one consolidation. The struct is serialized as JSON front
// matter; the claim is echoed as prose below it for readability.
type MemoryNote struct {
	Schema     string       `json:"schema"`
	ID         string       `json:"id"`
	CreatedAt  time.Time    `json:"created_at"`
	Scope      string       `json:"scope"`                // subsystem/skill/file the note applies to
	Claim      string       `json:"claim"`                // the consolidated statement
	Status     string       `json:"status"`               // proposed|settled|rejected|superseded
	Cites      []string     `json:"cites"`                // decision ids that justify the claim (provenance)
	Supersedes string       `json:"supersedes,omitempty"` // a prior note id this replaces
	Oracle     OracleResult `json:"oracle"`               // retrieval-oracle verdict at propose time
	Settlement *Outcome     `json:"settlement,omitempty"` // panel result, set when settled/rejected
}

// MemoryProposal is what a curator agent drafts and hands to the CLI.
type MemoryProposal struct {
	Scope      string   `json:"scope"`
	Claim      string   `json:"claim"`
	Cites      []string `json:"cites"`
	Supersedes string   `json:"supersedes,omitempty"`
}

// OracleResult is the retrieval oracle's verdict: does the cited evidence
// actually support the claim, and do the citations exist at all?
type OracleResult struct {
	Supported         bool     `json:"supported"`
	Score             float64  `json:"score"` // fraction of claim terms found in cited decisions
	MatchedTerms      []string `json:"matched_terms,omitempty"`
	MissingTerms      []string `json:"missing_terms,omitempty"`
	UnknownCitations  []string `json:"unknown_citations,omitempty"`  // cited ids not in decisions.jsonl
	RevertedCitations []string `json:"reverted_citations,omitempty"` // cited decisions later reverted
	Reasons           []string `json:"reasons,omitempty"`
}

// OracleParams tunes the retrieval oracle. Defaults are code constants (not
// persisted in config.json) so existing stores need no migration.
type OracleParams struct {
	MinScore float64 // required fraction of claim terms supported by citations
	MinTerms int     // required count of distinct supported terms
}

// DefaultOracleParams is the gate applied by `settle memory propose`.
func DefaultOracleParams() OracleParams {
	return OracleParams{MinScore: 0.5, MinTerms: 2}
}

// RetrievalOracle deterministically checks that a claim is grounded in the
// decisions it cites — the anti-hallucination gate the design doc requires
// before a memory write persists. It fails hard on citations that do not
// exist (fabricated provenance), and otherwise measures how many of the
// claim's substantive terms actually appear in the cited decisions' text.
func RetrievalOracle(claim string, cites []string, decisions []Decision, p OracleParams) OracleResult {
	var res OracleResult

	claimTerms := normalizeTerms([]string{claim})
	if len(claimTerms) == 0 {
		res.Reasons = append(res.Reasons, "claim has no substantive terms to verify")
		return res
	}
	if len(cites) == 0 {
		res.Reasons = append(res.Reasons, "claim cites no decisions; memory must be grounded in episodes")
		return res
	}

	byID := make(map[string]Decision, len(decisions))
	for _, d := range decisions {
		byID[d.ID] = d
	}

	var cited []Decision
	for _, id := range cites {
		d, ok := byID[id]
		if !ok {
			res.UnknownCitations = appendUnique(res.UnknownCitations, id)
			continue
		}
		cited = append(cited, d)
		if d.Result == "reverted" {
			res.RevertedCitations = appendUnique(res.RevertedCitations, id)
		}
	}

	// Fabricated citations are disqualifying regardless of term overlap.
	if len(res.UnknownCitations) > 0 {
		res.Reasons = append(res.Reasons,
			"cites decisions not found in the log: "+strings.Join(res.UnknownCitations, ", "))
		return res
	}

	var corpus strings.Builder
	for _, d := range cited {
		corpus.WriteString(searchText(d))
		corpus.WriteByte('\n')
	}
	haystack := corpus.String()

	for _, t := range claimTerms {
		if strings.Contains(haystack, t) {
			res.MatchedTerms = append(res.MatchedTerms, t)
		} else {
			res.MissingTerms = append(res.MissingTerms, t)
		}
	}
	res.Score = float64(len(res.MatchedTerms)) / float64(len(claimTerms))

	switch {
	case len(res.MatchedTerms) < p.MinTerms:
		res.Reasons = append(res.Reasons, fmt.Sprintf(
			"only %d claim term(s) supported by citations; need %d", len(res.MatchedTerms), p.MinTerms))
	case res.Score < p.MinScore:
		res.Reasons = append(res.Reasons, fmt.Sprintf(
			"support score %.2f below threshold %.2f", res.Score, p.MinScore))
	default:
		res.Supported = true
		res.Reasons = append(res.Reasons, fmt.Sprintf(
			"%d/%d claim terms grounded in %d cited decision(s)",
			len(res.MatchedTerms), len(claimTerms), len(cited)))
	}
	if len(res.RevertedCitations) > 0 {
		res.Reasons = append(res.Reasons,
			"warning: cites reverted decision(s): "+strings.Join(res.RevertedCitations, ", "))
	}
	return res
}

// NewMemoryNote builds a proposed note from a curator proposal and an oracle
// verdict. The id is content-addressed on scope+claim so identical proposals
// collide rather than duplicate.
func NewMemoryNote(prop MemoryProposal, oracle OracleResult, now time.Time) MemoryNote {
	sum := sha256.Sum256([]byte(prop.Scope + "\x00" + prop.Claim))
	id := fmt.Sprintf("mem_%s_%s", now.UTC().Format("20060102T150405"), hex.EncodeToString(sum[:])[:8])
	return MemoryNote{
		Schema:     SchemaMemory,
		ID:         id,
		CreatedAt:  now.UTC(),
		Scope:      prop.Scope,
		Claim:      prop.Claim,
		Status:     MemProposed,
		Cites:      prop.Cites,
		Supersedes: prop.Supersedes,
		Oracle:     oracle,
	}
}

// --- settlement ---

// MemoryPanelSize is the lightweight panel that settles a consolidation
// (docs §3: "cheap models, 3 voters").
const MemoryPanelSize = 3

// BuildMemoryPanel selects the panel that settles a memory proposal: the first
// MemoryPanelSize configured personas, weighted by the ledger. The subject is
// the note itself rather than a diff.
func BuildMemoryPanel(cfg Config, ledger Ledger, note MemoryNote) PanelSpec {
	n := MemoryPanelSize
	if n > len(cfg.Panel.Personas) {
		n = len(cfg.Panel.Personas)
	}
	seats := make([]PanelSeat, 0, n)
	for _, persona := range cfg.Panel.Personas[:n] {
		seats = append(seats, seatFor(persona, ledger))
	}
	return PanelSpec{
		Schema:    SchemaPanel,
		Subject:   Subject{Type: "memory", Files: []string{note.Scope}},
		Consensus: cfg.Consensus,
		Seats:     seats,
	}
}

// SettleMemory adjudicates a proposed note through a panel and records the
// result: approve settles it into active memory, anything else rejects it,
// and no consensus leaves it proposed for another round. A memory write, like
// a code change, only persists once it is settled.
func SettleMemory(note *MemoryNote, panel PanelSpec, verdicts []Verdict, cfg Config) error {
	if note.Status != MemProposed {
		return fmt.Errorf("note %s is %s, not proposed", note.ID, note.Status)
	}
	outcome, err := Adjudicate(panel.Seats, verdicts, panel.Consensus, "memory consolidation: "+note.Claim)
	if err != nil {
		return err
	}
	note.Settlement = &outcome
	switch {
	case !outcome.Reached:
		// Undecided: keep it proposed; the caller reports the split.
	case outcome.Decision == VerdictApprove:
		note.Status = MemSettled
	default:
		note.Status = MemRejected
	}
	return nil
}

// --- store ---

func (s *Store) memoryDir() string { return filepath.Join(s.Root, "memory") }

func (s *Store) memoryPath(id string) string {
	return filepath.Join(s.memoryDir(), id+".md")
}

// WriteMemoryNote writes (or overwrites) a note as JSON front matter plus a
// prose echo of the claim.
func (s *Store) WriteMemoryNote(note MemoryNote) error {
	if err := os.MkdirAll(s.memoryDir(), 0o755); err != nil {
		return err
	}
	meta, err := json.MarshalIndent(note, "", "  ")
	if err != nil {
		return err
	}
	var buf bytes.Buffer
	buf.WriteString("---\n")
	buf.Write(meta)
	buf.WriteString("\n---\n\n")
	buf.WriteString(note.Claim)
	buf.WriteByte('\n')
	return os.WriteFile(s.memoryPath(note.ID), buf.Bytes(), 0o644)
}

// GetMemoryNote loads one note by id.
func (s *Store) GetMemoryNote(id string) (MemoryNote, error) {
	data, err := os.ReadFile(s.memoryPath(id))
	if err != nil {
		if os.IsNotExist(err) {
			return MemoryNote{}, fmt.Errorf("memory note %s not found", id)
		}
		return MemoryNote{}, err
	}
	return parseMemoryNote(data)
}

// ReadMemoryNotes loads every note, newest first.
func (s *Store) ReadMemoryNotes() ([]MemoryNote, error) {
	entries, err := os.ReadDir(s.memoryDir())
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var notes []MemoryNote
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(s.memoryDir(), e.Name()))
		if err != nil {
			return nil, err
		}
		note, err := parseMemoryNote(data)
		if err != nil {
			return nil, fmt.Errorf("memory note %s: %w", e.Name(), err)
		}
		notes = append(notes, note)
	}
	sort.Slice(notes, func(i, j int) bool { return notes[i].CreatedAt.After(notes[j].CreatedAt) })
	return notes, nil
}

// parseMemoryNote reads the JSON front matter from a note file.
func parseMemoryNote(data []byte) (MemoryNote, error) {
	s := string(data)
	if !strings.HasPrefix(s, "---\n") {
		return MemoryNote{}, fmt.Errorf("malformed note: missing front matter")
	}
	rest := s[len("---\n"):]
	end := strings.Index(rest, "\n---")
	if end < 0 {
		return MemoryNote{}, fmt.Errorf("malformed note: unterminated front matter")
	}
	var note MemoryNote
	if err := json.Unmarshal([]byte(rest[:end]), &note); err != nil {
		return MemoryNote{}, fmt.Errorf("malformed note front matter: %w", err)
	}
	return note, nil
}

// --- guidance ---

// ApplicableNotes returns the settled memory notes whose scope covers any of
// the given files — the guidance that applies to a change under review. Notes
// excluded by `quarantined` (below-adequacy, see the skill ledger) are dropped
// from auto-injection. Results are ordered as stored (newest first).
func ApplicableNotes(notes []MemoryNote, files []string, quarantined map[string]bool) []MemoryNote {
	var out []MemoryNote
	for _, n := range notes {
		if n.Status != MemSettled || quarantined[n.ID] {
			continue
		}
		if scopeCoversAny(n.Scope, files) {
			out = append(out, n)
		}
	}
	return out
}

// scopeCoversAny reports whether a note scope applies to any file. A scope
// covers a file when it is the file, a parent directory, or its basename —
// e.g. scope "api" covers "api/handler.go", scope "go/core" covers
// "go/core/queue.go".
func scopeCoversAny(scope string, files []string) bool {
	scope = normalizePath(scope)
	if scope == "" {
		return false
	}
	for _, f := range files {
		f = normalizePath(f)
		switch {
		case f == scope:
			return true
		case strings.HasPrefix(f, scope+"/"):
			return true
		case path.Dir(f) == scope:
			return true
		case path.Base(f) == scope:
			return true
		}
	}
	return false
}

// --- curator feed ---

// EpisodeDigest is a recorded decision summarized for the curator: enough
// substance (verdict, outcome, dissents, per-seat reasoning) to draft a
// grounded consolidation and cite it, without the full record.
type EpisodeDigest struct {
	ID        string       `json:"id"`
	CreatedAt string       `json:"created_at"`
	Verdict   string       `json:"verdict"`
	Agreement float64      `json:"agreement"`
	Result    string       `json:"result,omitempty"`
	Branch    string       `json:"branch,omitempty"`
	Files     []string     `json:"files,omitempty"`
	Dissents  []string     `json:"dissents,omitempty"`
	Votes     []VoteDigest `json:"votes,omitempty"`
}

// VoteDigest is one seat's stance in an EpisodeDigest.
type VoteDigest struct {
	Persona   string `json:"persona"`
	Decision  string `json:"decision"`
	Reasoning string `json:"reasoning,omitempty"`
}

// CuratorFeed returns the most recent n decisions as digests, newest first,
// as the curator's episodic-memory input for drafting consolidations.
func CuratorFeed(decisions []Decision, n int) []EpisodeDigest {
	feed := make([]EpisodeDigest, 0, len(decisions))
	for i := len(decisions) - 1; i >= 0 && (n <= 0 || len(feed) < n); i-- {
		d := decisions[i]
		verdict := "no-consensus"
		if d.Outcome.Reached {
			verdict = d.Outcome.Decision
		}
		dg := EpisodeDigest{
			ID:        d.ID,
			CreatedAt: d.CreatedAt.Format("2006-01-02 15:04"),
			Verdict:   verdict,
			Agreement: d.Outcome.Agreement,
			Result:    d.Result,
			Branch:    d.Subject.Branch,
			Files:     d.Subject.Files,
			Dissents:  d.Outcome.Dissents,
		}
		for _, v := range d.Verdicts {
			dg.Votes = append(dg.Votes, VoteDigest{
				Persona:   v.Persona,
				Decision:  v.Decision,
				Reasoning: v.Reasoning,
			})
		}
		feed = append(feed, dg)
	}
	return feed
}

// Schema tag for memory notes.
const SchemaMemory = "settle/memory@1"
