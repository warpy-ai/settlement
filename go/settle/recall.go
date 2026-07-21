package settle

import (
	"path"
	"sort"
	"strings"
)

// Recall is memory-tier retrieval over recorded decisions: given files and/or
// free-text terms, it surfaces the past decisions most likely to be relevant
// precedent. It is the local-first stand-in for the design doc's GraphRAG
// recall (docs/SETTLE_SKILL_MEMORY_AND_PUNITIVE.md §2): with no graph store
// yet, "connected precedent" is approximated by file-path overlap — decisions
// that touched the same files as the change in hand — augmented by term
// matching over each decision's reasoning, findings, and branch.
//
// Everything it reads is already committed in .settlement/decisions.jsonl, so
// recall works offline and travels with the repo.

// RecallQuery describes what to recall precedent for.
type RecallQuery struct {
	Files []string // paths of interest; the strongest relevance signal
	Terms []string // free-text terms, matched against decision text
}

// DiffFiles returns the changed files in a unified diff — used to recall
// precedent for the change currently in hand.
func DiffFiles(diff []byte) []string {
	return describeDiff(diff).Files
}

// RecallHit is one relevant past decision, with why it matched.
type RecallHit struct {
	Decision     Decision `json:"-"`
	ID           string   `json:"id"`
	CreatedAt    string   `json:"created_at"`
	Verdict      string   `json:"verdict"`          // approve|reject|revise|no-consensus
	Agreement    float64  `json:"agreement"`        // winning weighted share
	Result       string   `json:"result,omitempty"` // held|reverted, if graded
	Branch       string   `json:"branch,omitempty"` //
	Score        float64  `json:"score"`            // relevance score (higher = closer)
	MatchedFiles []string `json:"matched_files,omitempty"`
	MatchedTerms []string `json:"matched_terms,omitempty"`
	Reasoning    string   `json:"reasoning,omitempty"` // most-relevant reasoning snippet
	Dissents     []string `json:"dissents,omitempty"`  // personas that dissented — read these
}

// Relevance weights. File overlap dominates: a decision over the same file is
// far stronger precedent than one that merely shares a word.
const (
	scoreExactFile    = 3.0  // same path
	scoreBasenameFile = 1.5  // same filename, different directory
	scoreDirFile      = 0.75 // same parent directory
	scoreTerm         = 1.0  // a query term present in the decision text
	scoreGradedBoost  = 0.25 // decision has real-world ground truth attached
)

// Recall ranks decisions by relevance to the query and returns the top limit
// hits (limit <= 0 means all). Decisions with no signal at all are dropped.
func Recall(decisions []Decision, q RecallQuery, limit int) []RecallHit {
	qFiles := normalizeFiles(q.Files)
	qTerms := normalizeTerms(q.Terms)

	hits := make([]RecallHit, 0, len(decisions))
	for _, d := range decisions {
		score, matchedFiles := fileScore(qFiles, d.Subject.Files)
		text, matchedTerms := termScore(qTerms, d)
		score += text
		if score == 0 {
			continue
		}
		if d.Result != "" {
			score += scoreGradedBoost
		}
		hits = append(hits, newHit(d, score, matchedFiles, matchedTerms))
	}

	sort.SliceStable(hits, func(i, j int) bool {
		if hits[i].Score != hits[j].Score {
			return hits[i].Score > hits[j].Score
		}
		// Tie-break: most recent precedent first.
		return hits[i].Decision.CreatedAt.After(hits[j].Decision.CreatedAt)
	})

	if limit > 0 && len(hits) > limit {
		hits = hits[:limit]
	}
	return hits
}

func newHit(d Decision, score float64, files, terms []string) RecallHit {
	verdict := "no-consensus"
	if d.Outcome.Reached {
		verdict = d.Outcome.Decision
	}
	return RecallHit{
		Decision:     d,
		ID:           d.ID,
		CreatedAt:    d.CreatedAt.Format("2006-01-02 15:04"),
		Verdict:      verdict,
		Agreement:    d.Outcome.Agreement,
		Result:       d.Result,
		Branch:       d.Subject.Branch,
		Score:        score,
		MatchedFiles: files,
		MatchedTerms: terms,
		Reasoning:    pickReasoning(d, terms),
		Dissents:     d.Outcome.Dissents,
	}
}

// fileScore rewards decisions that touched the query's files, strongest for an
// exact path, weaker for a shared filename or directory. Each query file
// contributes its best single match so one file cannot dominate the score.
func fileScore(qFiles, decFiles []string) (float64, []string) {
	if len(qFiles) == 0 || len(decFiles) == 0 {
		return 0, nil
	}
	var total float64
	var matched []string
	for _, qf := range qFiles {
		best := 0.0
		var bestFile string
		for _, df := range decFiles {
			df = normalizePath(df)
			s := 0.0
			switch {
			case qf == df:
				s = scoreExactFile
			case path.Base(qf) == path.Base(df):
				s = scoreBasenameFile
			case path.Dir(qf) == path.Dir(df) && path.Dir(qf) != ".":
				s = scoreDirFile
			}
			if s > best {
				best, bestFile = s, df
			}
		}
		if best > 0 {
			total += best
			matched = appendUnique(matched, bestFile)
		}
	}
	return total, matched
}

// termScore counts how many distinct query terms appear anywhere in the
// decision's searchable text (reasoning, finding summaries, branch, file
// paths). Presence, not frequency — one strong match should not swamp file
// signal.
func termScore(qTerms []string, d Decision) (float64, []string) {
	if len(qTerms) == 0 {
		return 0, nil
	}
	haystack := searchText(d)
	var total float64
	var matched []string
	for _, t := range qTerms {
		if strings.Contains(haystack, t) {
			total += scoreTerm
			matched = appendUnique(matched, t)
		}
	}
	return total, matched
}

// searchText builds the lowercased corpus for a decision once per call.
func searchText(d Decision) string {
	var b strings.Builder
	b.WriteString(strings.ToLower(d.Subject.Branch))
	b.WriteByte('\n')
	for _, f := range d.Subject.Files {
		b.WriteString(strings.ToLower(f))
		b.WriteByte('\n')
	}
	for _, v := range d.Verdicts {
		b.WriteString(strings.ToLower(v.Reasoning))
		b.WriteByte('\n')
		for _, f := range v.Findings {
			b.WriteString(strings.ToLower(f.Summary))
			b.WriteByte('\n')
		}
	}
	return b.String()
}

// pickReasoning returns the reasoning snippet most useful as precedent: a
// dissent that mentions a matched term if one exists (dissents are the
// valuable part), else the first dissent, else the first reasoning.
func pickReasoning(d Decision, terms []string) string {
	dissenting := map[string]bool{}
	for _, p := range d.Outcome.Dissents {
		dissenting[p] = true
	}
	var firstDissent, firstAny string
	for _, v := range d.Verdicts {
		if v.Reasoning == "" {
			continue
		}
		if firstAny == "" {
			firstAny = v.Reasoning
		}
		if dissenting[v.Persona] {
			if firstDissent == "" {
				firstDissent = v.Reasoning
			}
			low := strings.ToLower(v.Reasoning)
			for _, t := range terms {
				if strings.Contains(low, t) {
					return v.Reasoning
				}
			}
		}
	}
	if firstDissent != "" {
		return firstDissent
	}
	return firstAny
}

// normalizeFiles cleans and de-duplicates query file paths.
func normalizeFiles(files []string) []string {
	var out []string
	for _, f := range files {
		f = normalizePath(f)
		if f != "" {
			out = appendUnique(out, f)
		}
	}
	return out
}

func normalizePath(p string) string {
	p = strings.TrimSpace(p)
	p = strings.TrimPrefix(p, "./")
	p = strings.TrimPrefix(p, "a/")
	p = strings.TrimPrefix(p, "b/")
	return strings.ToLower(p)
}

// stopwords are dropped from free-text queries — too common to be discriminating.
var stopwords = map[string]bool{
	"the": true, "and": true, "for": true, "with": true, "that": true,
	"this": true, "from": true, "are": true, "was": true, "has": true,
	"have": true, "not": true, "but": true, "add": true, "fix": true,
	"use": true, "using": true, "change": true, "changes": true,
}

// normalizeTerms tokenizes free text into lowercase terms >= 3 chars, dropping
// stopwords and duplicates.
func normalizeTerms(terms []string) []string {
	var out []string
	for _, raw := range terms {
		for _, tok := range strings.FieldsFunc(raw, isTermBreak) {
			tok = strings.ToLower(tok)
			if len(tok) < 3 || stopwords[tok] {
				continue
			}
			out = appendUnique(out, tok)
		}
	}
	return out
}

func isTermBreak(r rune) bool {
	return !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9')
}

func appendUnique(s []string, v string) []string {
	for _, x := range s {
		if x == v {
			return s
		}
	}
	return append(s, v)
}
