package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"strings"
	"time"

	"settlement-core/settle"
)

const memoryUsage = `settle memory - consolidate episodic decisions into settled memory

Usage:
  settle memory candidates [-n N] [--json]   Recent decisions, as the curator's feed
  settle memory propose --file P             Oracle-gate a proposal and record it (proposed)
  settle memory panel --id ID                Emit the panel that settles a proposed note
  settle memory settle --id ID [--panel F]   Tally verdicts and settle/reject the note
  settle memory list [--status S]            List memory notes (newest first)
  settle memory show ID                      Print one note

A proposal is JSON: {"scope":"go/core","claim":"...","cites":["dec_..."]}.
propose runs the retrieval oracle; a proposal whose claim is not grounded in
its cited decisions is refused, not saved. settle reads verdicts from
.settlement/verdicts/mem-<id>/ (exit 0 settled, 2 rejected, 3 no consensus).`

func cmdMemory(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, memoryUsage)
		return 1
	}
	switch args[0] {
	case "candidates":
		return cmdMemoryCandidates(args[1:], stdout, stderr)
	case "propose":
		return cmdMemoryPropose(args[1:], stdin, stdout, stderr)
	case "panel":
		return cmdMemoryPanel(args[1:], stdout, stderr)
	case "settle":
		return cmdMemorySettle(args[1:], stdout, stderr)
	case "list":
		return cmdMemoryList(args[1:], stdout, stderr)
	case "show":
		return cmdMemoryShow(args[1:], stdout, stderr)
	case "help", "-h", "--help":
		fmt.Fprintln(stdout, memoryUsage)
		return 0
	default:
		fmt.Fprintf(stderr, "unknown memory subcommand %q\n\n%s\n", args[0], memoryUsage)
		return 1
	}
}

func cmdMemoryCandidates(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("memory candidates", flag.ContinueOnError)
	fs.SetOutput(stderr)
	limit := fs.Int("n", 10, "maximum decisions to include")
	asJSON := fs.Bool("json", false, "emit the feed as JSON (for the curator agent)")
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
	feed := settle.CuratorFeed(decisions, *limit)
	if *asJSON {
		return printJSON(stdout, stderr, feed)
	}
	if len(feed) == 0 {
		fmt.Fprintln(stdout, "no decisions to consolidate yet")
		return 0
	}
	for _, e := range feed {
		result := e.Result
		if result == "" {
			result = "ungraded"
		}
		fmt.Fprintf(stdout, "%s  %s  %-12s  result=%s  files=%s\n",
			e.ID, e.CreatedAt, e.Verdict, result, strings.Join(e.Files, ","))
	}
	return 0
}

func cmdMemoryPropose(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("memory propose", flag.ContinueOnError)
	fs.SetOutput(stderr)
	file := fs.String("file", "", "proposal JSON file (\"-\" or omitted reads stdin)")
	if err := fs.Parse(args); err != nil {
		return 1
	}

	raw, err := readInput(strings.TrimPrefix(*file, "-"), stdin)
	if err != nil {
		return fail(stderr, err)
	}
	var prop settle.MemoryProposal
	if err := json.Unmarshal(raw, &prop); err != nil {
		return fail(stderr, fmt.Errorf("invalid proposal: %w", err))
	}
	if strings.TrimSpace(prop.Claim) == "" || prop.Scope == "" {
		fmt.Fprintln(stderr, "settle memory propose: proposal needs a scope and a claim")
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

	oracle := settle.RetrievalOracle(prop.Claim, prop.Cites, decisions, settle.DefaultOracleParams())
	if !oracle.Supported {
		fmt.Fprintln(stderr, "settle memory propose: retrieval oracle rejected the claim — not saved")
		for _, r := range oracle.Reasons {
			fmt.Fprintf(stderr, "  - %s\n", r)
		}
		return 2
	}

	note := settle.NewMemoryNote(prop, oracle, time.Now())
	if err := store.WriteMemoryNote(note); err != nil {
		return fail(stderr, err)
	}
	fmt.Fprintf(stdout, "proposed %s (scope=%s, oracle score=%.2f)\n", note.ID, note.Scope, oracle.Score)
	for _, r := range oracle.Reasons {
		fmt.Fprintf(stdout, "  - %s\n", r)
	}
	fmt.Fprintf(stdout, "settle the proposal with a panel before it becomes active memory\n")
	return 0
}

func cmdMemoryPanel(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("memory panel", flag.ContinueOnError)
	fs.SetOutput(stderr)
	id := fs.String("id", "", "note id to build a settlement panel for")
	if err := fs.Parse(args); err != nil {
		return 1
	}
	if *id == "" {
		fmt.Fprintln(stderr, "settle memory panel: --id is required")
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
	note, err := store.GetMemoryNote(*id)
	if err != nil {
		return fail(stderr, err)
	}
	return printJSON(stdout, stderr, settle.BuildMemoryPanel(cfg, ledger, note))
}

func cmdMemorySettle(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("memory settle", flag.ContinueOnError)
	fs.SetOutput(stderr)
	id := fs.String("id", "", "note id to settle")
	panelFile := fs.String("panel", "", "panel spec JSON (default: rebuild from config)")
	if err := fs.Parse(args); err != nil {
		return 1
	}
	if *id == "" {
		fmt.Fprintln(stderr, "settle memory settle: --id is required")
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
	note, err := store.GetMemoryNote(*id)
	if err != nil {
		return fail(stderr, err)
	}

	var panel settle.PanelSpec
	if *panelFile != "" {
		data, err := readInput(*panelFile, nil)
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
		panel = settle.BuildMemoryPanel(cfg, ledger, note)
	}

	verdicts, err := settle.LoadVerdicts(store.VerdictsDir("mem-" + note.ID))
	if err != nil {
		return fail(stderr, err)
	}
	if err := settle.SettleMemory(&note, panel, verdicts, cfg); err != nil {
		return fail(stderr, err)
	}
	if err := store.WriteMemoryNote(note); err != nil {
		return fail(stderr, err)
	}

	// A settled note that supersedes an earlier one retires it.
	if note.Status == settle.MemSettled && note.Supersedes != "" {
		if prior, err := store.GetMemoryNote(note.Supersedes); err == nil {
			prior.Status = settle.MemSuperseded
			if err := store.WriteMemoryNote(prior); err != nil {
				return fail(stderr, err)
			}
		}
	}

	switch {
	case !note.Settlement.Reached:
		fmt.Fprintf(stderr, "no consensus on %s (%d verdicts) — still proposed\n", note.ID, len(verdicts))
		return 3
	case note.Status == settle.MemSettled:
		fmt.Fprintf(stderr, "SETTLED %s into memory with %.0f%% agreement (dissents: %d)\n",
			note.ID, note.Settlement.Agreement*100, len(note.Settlement.Dissents))
		return 0
	default:
		fmt.Fprintf(stderr, "REJECTED %s (%.0f%% for %q) — not written to active memory\n",
			note.ID, note.Settlement.Agreement*100, note.Settlement.Decision)
		return 2
	}
}

func cmdMemoryList(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("memory list", flag.ContinueOnError)
	fs.SetOutput(stderr)
	status := fs.String("status", "", "filter by status: proposed|settled|rejected|superseded")
	if err := fs.Parse(args); err != nil {
		return 1
	}

	store, code := openStore(stderr)
	if code != 0 {
		return code
	}
	notes, err := store.ReadMemoryNotes()
	if err != nil {
		return fail(stderr, err)
	}
	shown := 0
	for _, n := range notes {
		if *status != "" && n.Status != *status {
			continue
		}
		fmt.Fprintf(stdout, "%s  %s  %-10s  scope=%s  cites=%d\n",
			n.ID, n.CreatedAt.Format("2006-01-02 15:04"), n.Status, n.Scope, len(n.Cites))
		fmt.Fprintf(stdout, "    %s\n", n.Claim)
		shown++
	}
	if shown == 0 {
		fmt.Fprintln(stdout, "no memory notes")
	}
	return 0
}

func cmdMemoryShow(args []string, stdout, stderr io.Writer) int {
	if len(args) != 1 {
		fmt.Fprintln(stderr, "usage: settle memory show <note-id>")
		return 1
	}
	store, code := openStore(stderr)
	if code != 0 {
		return code
	}
	note, err := store.GetMemoryNote(args[0])
	if err != nil {
		return fail(stderr, err)
	}
	return printJSON(stdout, stderr, note)
}
