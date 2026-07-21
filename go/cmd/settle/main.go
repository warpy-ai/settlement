// Command settle is the local settlement CLI: it scaffolds .settlement/
// state, builds persona review panels for diffs, tallies verdicts through
// the shared consensus engine, and maintains the punitive voting-power
// ledger. It runs per-command with no daemon and no network.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"

	"settlement-core/settle"
)

const usage = `settle - local consensus for code changes

Usage:
  settle init                                Scaffold .settlement/ in the current directory
  settle panel  [--diff-file F]              Emit the review panel spec for a diff (stdin default)
  settle tally  --task ID [--panel F]        Tally verdicts from .settlement/verdicts/ID/ and record the decision
  settle outcome --decision ID --result R    Record ground truth (held|reverted) and update the ledger
  settle log    [-n N]                       List recorded decisions (newest first)
  settle show   ID                           Print one decision as JSON
  settle recall [--query Q] [--file F,F]     Recall past decisions relevant to files/terms
                [--diff-file D] [-n N] [--json]
  settle ledger                              Print persona voting powers

Exit codes for tally: 0 approve, 2 reject/revise, 3 no consensus, 1 error.`

func main() {
	os.Exit(run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}

func run(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, usage)
		return 1
	}
	switch args[0] {
	case "init":
		return cmdInit(stdout, stderr)
	case "panel":
		return cmdPanel(args[1:], stdin, stdout, stderr)
	case "tally":
		return cmdTally(args[1:], stdin, stdout, stderr)
	case "outcome":
		return cmdOutcome(args[1:], stdout, stderr)
	case "log":
		return cmdLog(args[1:], stdout, stderr)
	case "show":
		return cmdShow(args[1:], stdout, stderr)
	case "recall":
		return cmdRecall(args[1:], stdin, stdout, stderr)
	case "ledger":
		return cmdLedger(stdout, stderr)
	case "help", "-h", "--help":
		fmt.Fprintln(stdout, usage)
		return 0
	default:
		fmt.Fprintf(stderr, "unknown command %q\n\n%s\n", args[0], usage)
		return 1
	}
}

func fail(stderr io.Writer, err error) int {
	fmt.Fprintln(stderr, "settle:", err)
	return 1
}

func openStore(stderr io.Writer) (*settle.Store, int) {
	cwd, err := os.Getwd()
	if err != nil {
		return nil, fail(stderr, err)
	}
	store, err := settle.FindStore(cwd)
	if err != nil {
		return nil, fail(stderr, err)
	}
	return store, 0
}

func cmdInit(stdout, stderr io.Writer) int {
	cwd, err := os.Getwd()
	if err != nil {
		return fail(stderr, err)
	}
	store, err := settle.Init(cwd)
	if err != nil {
		return fail(stderr, err)
	}
	fmt.Fprintf(stdout, "initialized %s\n", store.Root)
	fmt.Fprintln(stdout, "commit config.json, ledger.json, and decisions.jsonl; verdicts/ stays untracked scratch")
	return 0
}

func cmdPanel(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("panel", flag.ContinueOnError)
	fs.SetOutput(stderr)
	diffFile := fs.String("diff-file", "", "unified diff file (default: stdin)")
	if err := fs.Parse(args); err != nil {
		return 1
	}

	store, code := openStore(stderr)
	if code != 0 {
		return code
	}
	cfg, err := store.LoadConfig()
	if err != nil {
		return fail(stderr, err)
	}
	ledger, err := store.LoadLedger()
	if err != nil {
		return fail(stderr, err)
	}

	diff, err := readInput(*diffFile, stdin)
	if err != nil {
		return fail(stderr, err)
	}

	panel := settle.BuildPanel(cfg, ledger, diff)
	stampBranch(&panel)
	return printJSON(stdout, stderr, panel)
}

// stampBranch records the current branch on the panel subject so decisions
// made in parallel worktrees identify the branch they belong to.
func stampBranch(panel *settle.PanelSpec) {
	if cwd, err := os.Getwd(); err == nil {
		if info, ok := settle.FindGitInfo(cwd); ok {
			panel.Subject.Branch = info.Branch
		}
	}
}

func cmdTally(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("tally", flag.ContinueOnError)
	fs.SetOutput(stderr)
	taskID := fs.String("task", "", "task id: verdicts are read from .settlement/verdicts/<id>/")
	panelFile := fs.String("panel", "", "panel spec JSON file (default: rebuild from --diff-file)")
	diffFile := fs.String("diff-file", "", "unified diff file, used when --panel is not given")
	noRecord := fs.Bool("no-record", false, "tally without appending to decisions.jsonl or touching the ledger")
	if err := fs.Parse(args); err != nil {
		return 1
	}
	if *taskID == "" {
		fmt.Fprintln(stderr, "settle tally: --task is required")
		return 1
	}

	store, code := openStore(stderr)
	if code != 0 {
		return code
	}
	cfg, err := store.LoadConfig()
	if err != nil {
		return fail(stderr, err)
	}

	var panel settle.PanelSpec
	if *panelFile != "" {
		data, err := os.ReadFile(*panelFile)
		if err != nil {
			return fail(stderr, err)
		}
		if err := json.Unmarshal(data, &panel); err != nil {
			return fail(stderr, fmt.Errorf("invalid panel spec: %w", err))
		}
	} else {
		ledger, err := store.LoadLedger()
		if err != nil {
			return fail(stderr, err)
		}
		diff, err := readInput(*diffFile, stdin)
		if err != nil {
			return fail(stderr, err)
		}
		panel = settle.BuildPanel(cfg, ledger, diff)
		stampBranch(&panel)
	}

	verdicts, err := settle.LoadVerdicts(store.VerdictsDir(*taskID))
	if err != nil {
		return fail(stderr, err)
	}
	decision, err := settle.RunTally(panel, verdicts, cfg)
	if err != nil {
		return fail(stderr, err)
	}

	if !*noRecord {
		if err := store.AppendDecision(decision); err != nil {
			return fail(stderr, err)
		}
	}

	if code := printJSON(stdout, stderr, decision); code != 0 {
		return code
	}
	switch {
	case !decision.Outcome.Reached:
		fmt.Fprintf(stderr, "no consensus reached (%d verdicts)\n", len(decision.Verdicts))
		return 3
	case decision.Outcome.Decision == settle.VerdictApprove:
		fmt.Fprintf(stderr, "APPROVED with %.0f%% agreement (dissents: %d) — recorded as %s\n",
			decision.Outcome.Agreement*100, len(decision.Outcome.Dissents), decision.ID)
		return 0
	default:
		fmt.Fprintf(stderr, "%s with %.0f%% agreement (dissents: %d) — recorded as %s\n",
			decision.Outcome.Decision, decision.Outcome.Agreement*100, len(decision.Outcome.Dissents), decision.ID)
		return 2
	}
}

func cmdOutcome(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("outcome", flag.ContinueOnError)
	fs.SetOutput(stderr)
	decisionID := fs.String("decision", "", "decision id to grade")
	result := fs.String("result", "", "ground truth: held or reverted")
	force := fs.Bool("force", false, "allow grading from a linked worktree (ledger history may diverge)")
	if err := fs.Parse(args); err != nil {
		return 1
	}
	if *decisionID == "" || *result == "" {
		fmt.Fprintln(stderr, "settle outcome: --decision and --result are required")
		return 1
	}

	// Ledger history must stay linear: grading from parallel worktrees
	// produces conflicting ledger.json copies that cannot be merged.
	if cwd, err := os.Getwd(); err == nil && !*force {
		if info, ok := settle.FindGitInfo(cwd); ok && info.IsLinkedWorktree {
			fmt.Fprintln(stderr, "settle outcome: refusing to grade from a linked worktree — run it in the main checkout after the branch merges (or pass --force)")
			return 1
		}
	}

	store, code := openStore(stderr)
	if code != 0 {
		return code
	}
	cfg, err := store.LoadConfig()
	if err != nil {
		return fail(stderr, err)
	}
	decision, err := store.GetDecision(*decisionID)
	if err != nil {
		return fail(stderr, err)
	}
	if decision.Result != "" {
		return fail(stderr, fmt.Errorf("decision %s already graded as %q", decision.ID, decision.Result))
	}
	ledger, err := store.LoadLedger()
	if err != nil {
		return fail(stderr, err)
	}

	if err := settle.ApplyOutcome(&ledger, decision, *result, cfg.Ledger); err != nil {
		return fail(stderr, err)
	}
	if err := store.SaveLedger(ledger); err != nil {
		return fail(stderr, err)
	}
	if err := store.AppendOutcomeEvent(settle.NewOutcomeEvent(decision.ID, *result)); err != nil {
		return fail(stderr, err)
	}

	fmt.Fprintf(stdout, "recorded %s as %s; ledger updated:\n", decision.ID, *result)
	return cmdLedger(stdout, stderr)
}

func cmdLog(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("log", flag.ContinueOnError)
	fs.SetOutput(stderr)
	limit := fs.Int("n", 10, "maximum decisions to show")
	if err := fs.Parse(args); err != nil {
		return 1
	}

	store, code := openStore(stderr)
	if code != 0 {
		return code
	}
	decisions, err := store.ReadDecisions()
	if err != nil {
		return fail(stderr, err)
	}
	if len(decisions) == 0 {
		fmt.Fprintln(stdout, "no decisions recorded")
		return 0
	}
	shown := 0
	for i := len(decisions) - 1; i >= 0 && shown < *limit; i-- {
		d := decisions[i]
		verdict := "no-consensus"
		if d.Outcome.Reached {
			verdict = d.Outcome.Decision
		}
		result := d.Result
		if result == "" {
			result = "ungraded"
		}
		branch := ""
		if d.Subject.Branch != "" {
			branch = "  branch=" + d.Subject.Branch
		}
		fmt.Fprintf(stdout, "%s  %s  %-12s  agreement=%.0f%%  dissents=%d  result=%s%s\n",
			d.ID, d.CreatedAt.Format("2006-01-02 15:04"), verdict, d.Outcome.Agreement*100, len(d.Outcome.Dissents), result, branch)
		shown++
	}
	return 0
}

func cmdShow(args []string, stdout, stderr io.Writer) int {
	if len(args) != 1 {
		fmt.Fprintln(stderr, "usage: settle show <decision-id>")
		return 1
	}
	store, code := openStore(stderr)
	if code != 0 {
		return code
	}
	decision, err := store.GetDecision(args[0])
	if err != nil {
		return fail(stderr, err)
	}
	return printJSON(stdout, stderr, decision)
}

func cmdRecall(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("recall", flag.ContinueOnError)
	fs.SetOutput(stderr)
	query := fs.String("query", "", "free-text terms to match against past decisions")
	files := fs.String("file", "", "comma-separated file paths to recall precedent for")
	diffFile := fs.String("diff-file", "", "recall precedent for the files changed in this diff (\"-\" for stdin)")
	limit := fs.Int("n", 5, "maximum decisions to return")
	asJSON := fs.Bool("json", false, "emit hits as JSON (for the /settle skill)")
	if err := fs.Parse(args); err != nil {
		return 1
	}

	q := settle.RecallQuery{Terms: []string{*query}}
	if *files != "" {
		q.Files = append(q.Files, splitCSV(*files)...)
	}
	if *diffFile != "" {
		diff, err := readInput(strings.TrimPrefix(*diffFile, "-"), stdin)
		if err != nil {
			return fail(stderr, err)
		}
		q.Files = append(q.Files, settle.DiffFiles(diff)...)
	}
	if len(q.Files) == 0 && *query == "" {
		fmt.Fprintln(stderr, "settle recall: give at least one of --query, --file, or --diff-file")
		return 1
	}

	store, code := openStore(stderr)
	if code != 0 {
		return code
	}
	decisions, err := store.ReadDecisions()
	if err != nil {
		return fail(stderr, err)
	}

	hits := settle.Recall(decisions, q, *limit)
	if *asJSON {
		return printJSON(stdout, stderr, hits)
	}
	if len(hits) == 0 {
		fmt.Fprintln(stdout, "no relevant precedent found")
		return 0
	}
	for _, h := range hits {
		result := h.Result
		if result == "" {
			result = "ungraded"
		}
		fmt.Fprintf(stdout, "%s  %s  %-12s  agreement=%.0f%%  result=%s\n",
			h.ID, h.CreatedAt, h.Verdict, h.Agreement*100, result)
		if len(h.MatchedFiles) > 0 {
			fmt.Fprintf(stdout, "    files:   %s\n", strings.Join(h.MatchedFiles, ", "))
		}
		if len(h.Dissents) > 0 {
			fmt.Fprintf(stdout, "    dissent: %s\n", strings.Join(h.Dissents, ", "))
		}
		if h.Reasoning != "" {
			fmt.Fprintf(stdout, "    why:     %s\n", h.Reasoning)
		}
	}
	return 0
}

func splitCSV(s string) []string {
	var out []string
	for _, part := range strings.Split(s, ",") {
		if p := strings.TrimSpace(part); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func cmdLedger(stdout, stderr io.Writer) int {
	store, code := openStore(stderr)
	if code != 0 {
		return code
	}
	ledger, err := store.LoadLedger()
	if err != nil {
		return fail(stderr, err)
	}
	personas := make([]string, 0, len(ledger.Personas))
	for persona := range ledger.Personas {
		personas = append(personas, persona)
	}
	sort.Strings(personas)
	for _, persona := range personas {
		e := ledger.Personas[persona]
		status := ""
		if e.Quarantined {
			status = "  [QUARANTINED: shadow votes only]"
		}
		fmt.Fprintf(stdout, "%-14s power=%.2f reviews=%d aligned=%d misjudged=%d%s\n",
			persona, e.VotingPower, e.Reviews, e.Aligned, e.Misjudged, status)
	}
	return 0
}

func readInput(path string, stdin io.Reader) ([]byte, error) {
	if path != "" {
		return os.ReadFile(path)
	}
	return io.ReadAll(stdin)
}

func printJSON(stdout, stderr io.Writer, v interface{}) int {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return fail(stderr, err)
	}
	fmt.Fprintln(stdout, string(data))
	return 0
}
