---
name: settle
description: >
  Run a Settlement panel review of a code change: spawn independent persona
  reviewer subagents over a git diff, tally their verdicts through weighted
  consensus, and record the decision. Use when the user asks to /settle,
  "settle this change/diff/PR", or wants a multi-perspective review with an
  auditable verdict.
---

# /settle — panel review for code changes

You are the panel orchestrator. You never review the code yourself and you
never edit the code under review. You convene independent persona reviewers,
collect their verdicts, and let the `settle` CLI adjudicate.

## Prerequisites

- The `settle` binary on PATH, or run it as `go run ./cmd/settle` from the
  repo's `go/` directory.
- The repo has a `.settlement/` directory (`settle init` once, at the repo
  root, then commit `config.json`, `ledger.json`, `decisions.jsonl`).

## Workflow: `/settle review`

1. **Capture the diff.** Write the change under review to a temp file:
   `git diff <base>...HEAD` (or `git diff --staged`, or the diff the user
   points at). If the diff is empty, stop and say so.

2. **Build the panel.**

       settle panel --diff-file <difffile> > <paneldir>/panel.json

   The spec lists the seats: persona name, voting power, and prompt file.
   Pick a short task id for this review (e.g. `review-<branch>-<date>`).

3. **Spawn one subagent per seat — in parallel, independent.**
   Each subagent's prompt is:
   - the contents of the seat's persona file (`skill/settle/personas/<persona>.md`),
   - the full diff,
   - the verdict contract below, with the persona name filled in.

   Reviewers must not see each other's verdicts, your opinions, or the
   ledger. Do not tell a reviewer whether its seat is quarantined.

4. **Collect verdicts.** Save each subagent's JSON (and nothing else) to
   `.settlement/verdicts/<task-id>/<persona>.json`. If a subagent returns
   invalid JSON, ask it once to re-emit only the JSON; if it fails again,
   omit that seat and note the omission in your report.

5. **Tally.**

       settle tally --task <task-id> --panel <paneldir>/panel.json

   Exit code 0 = approved, 2 = rejected/revise, 3 = no consensus, 1 = error.

6. **Report to the user.**
   - The verdict, agreement percentage, and the recorded decision id.
   - Every dissent, by persona, with its core reasoning — dissents are the
     valuable part; never bury them.
   - The top findings (file:line) from any reviewer that raised them.
   - On exit 3 (no consensus): present the split and the strongest argument
     of each side; do not cast a deciding vote yourself.

7. **Later, when reality grades the change** (it shipped and held, or it was
   reverted/hotfixed), record it:

       settle outcome --decision <id> --result held|reverted

   This updates each persona's voting power (aligned votes gain, misjudged
   votes lose more — dissent that ages well pays best).

## Verdict contract (give this to every reviewer verbatim)

Return ONLY this JSON object, no prose around it:

```json
{
  "schema": "settle/verdict@1",
  "persona": "<persona-name>",
  "decision": "approve|reject|revise",
  "confidence": 0.0,
  "reasoning": "2-5 sentences: the decisive factors behind your vote.",
  "findings": [
    {"severity": "low|medium|high", "file": "path", "line": 0, "summary": "one sentence"}
  ]
}
```

- `decision`: `approve` = merge as-is; `revise` = right direction, named
  issues must be fixed first; `reject` = wrong approach.
- `confidence` in [0,1] — it weights your vote; do not inflate it.
- `findings` may be empty for a clean approve.

## Rules

- Run reviewers in parallel; keep them independent.
- Never modify the code under review during a review.
- Never edit `.settlement/ledger.json` or `decisions.jsonl` by hand — only
  the CLI writes them.
- Report the tally faithfully, including outcomes you'd personally disagree
  with. If you believe the panel missed something critical, say so to the
  user as your own remark, clearly separated from the recorded decision.
