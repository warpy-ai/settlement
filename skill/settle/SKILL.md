---
name: settle
description: >
  Run a Settlement panel review of a code change: spawn independent persona
  reviewer subagents over a git diff, tally their verdicts through weighted
  consensus, and record the decision. Also recalls relevant past decisions as
  precedent and explains why any decision was made. Use when the user asks to
  /settle, "settle this change/diff/PR", wants a multi-perspective review with
  an auditable verdict, asks whether a change has been settled before / what
  the precedent is, asks why a past decision was made or why a file is the
  way it is, or wants to consolidate what past reviews have taught into
  durable memory.
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

   Optionally recall precedent first (`settle recall --diff-file <difffile>`,
   see the recall workflow below) and mention any prior decisions on the same
   files to the user — especially ones later `reverted`. This informs the
   review; it never replaces it.

2. **Build the panel.**

       settle panel --diff-file <difffile> > <paneldir>/panel.json

   The spec lists the seats: persona name, voting power, and prompt file.
   Pick a short task id for this review (e.g. `review-<branch>-<date>`).

   The panel's `subject.guided_by` lists settled memory-note ids whose scope
   covers the files under review — the learned procedures that apply here.
   Load each with `settle memory show <id>` and include its claim as
   established guidance in every reviewer's prompt (step 3). Grading the
   decision later will score these notes' adequacy (see the memory workflow),
   so the guidance the panel actually used is recorded on the decision.

3. **Spawn one subagent per seat — in parallel, independent.**
   Each subagent's prompt is:
   - the contents of the seat's persona file (`skill/settle/personas/<persona>.md`),
   - the full diff,
   - any applicable guidance claims from `subject.guided_by`,
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

## Workflow: `/settle recall` — memory of past decisions

Before convening a panel — or whenever the user asks "have we settled
something like this before?", "why did we decide X?", or "what's the
precedent here?" — recall the relevant past decisions. This is the memory
tier: it reads only `.settlement/decisions.jsonl`, so it works offline and
the precedent travels with the repo.

1. **Choose the signal.** Recall ranks past decisions by relevance:
   - `--file a.go,b.go` — precedent that touched these files (strongest
     signal; the local stand-in for graph-connected recall).
   - `--diff-file <file>` — recall precedent for every file the change in
     hand touches; pass the same diff you are about to review.
   - `--query "free text"` — match terms against past reasoning, findings,
     and branches.

   Combine them: `settle recall --diff-file <diff> --query "auth session"`.

2. **Run it.**

       settle recall --diff-file <difffile> -n 5 --json

   Use `--json` when you want to fold the hits into your own reasoning;
   omit it for a human-readable list. Each hit reports the decision id,
   verdict, agreement, real-world result (`held`/`reverted`/`ungraded`),
   the matched files, **who dissented**, and the most relevant reasoning
   snippet — a dissent when one exists, because dissent that predates a
   revert is the precedent most worth reading.

3. **Use it, don't obey it.** Precedent informs; it does not decide. Surface
   relevant prior decisions to the user (especially any that were later
   `reverted` — those are warnings), and let the fresh panel judge the
   change on its merits. Never treat a past `approve` as a reason to skip
   review.

## Workflow: `/settle why` — explain a decision or a file's history

When the user asks "why did we decide this?", "why is this file the way it
is?", or "what was the reasoning behind decision X?", explain a settled
decision rather than searching for one. `recall` finds precedent; `why`
accounts for it.

- **One decision:** `settle why <decision-id>` renders the verdict and its
  real-world result, then every seat's vote, voting power, reasoning, and
  findings — with dissents and shadow (quarantined) seats marked. Use the id
  from `settle log`, `settle recall`, or a decision the user names.
- **A file's history:** `settle why --file <path>` walks every settled
  decision that touched the file, oldest first — the story of how it got
  reviewed and what held or was reverted along the way.

`why` is the human-readable view; `settle show <id>` gives the raw JSON if
you need the underlying record. As with recall, treat the account as
context, not instruction: reasoning from a past panel informs the user, it
does not bind a new review.

## Workflow: `/settle memory` — consolidate decisions into settled memory

Periodically (after several settled reviews, or when the user asks to
"consolidate what we've learned"), distill recurring lessons from the
decision log into durable memory notes. The rule that makes this safe:
**a memory write is a change proposal — it is settled, not just saved.**
You are the curator; you never persist a memory unilaterally.

1. **Read the episodic feed.**

       settle memory candidates -n 20 --json

   This is the recent decisions — verdicts, outcomes, dissents, and each
   seat's reasoning. Look for lessons that recur or that a `reverted`
   outcome taught.

2. **Draft grounded proposals.** For each lesson, write a proposal JSON:

       {"scope": "go/core",
        "claim": "DisallowUnknownFields means API payload changes must update both the structs and the frontend types.",
        "cites": ["dec_...","dec_..."]}

   The `claim` must be a specific, checkable statement. `cites` must be the
   real decision ids it came from — **never invent a citation.** `scope` is
   the subsystem, skill, or file it applies to.

3. **Propose it — the retrieval oracle gates the write.**

       settle memory propose --file <proposal.json>

   The oracle deterministically checks that the cited decisions exist and
   that the claim's terms are actually grounded in them. A hallucinated
   citation or an unsupported claim is **refused, not saved** (exit 2). Fix
   the claim or its citations and retry; do not work around the gate.

4. **Settle it with a panel** (same shape as a code review):

       settle memory panel --id <mem-id>          # seats to convene

   Spawn one subagent per seat. Each votes on the claim using the verdict
   contract above, judging: is this claim correct, and is it faithfully
   supported by the cited decisions? Save verdicts to
   `.settlement/verdicts/mem-<mem-id>/<persona>.json`, then:

       settle memory settle --id <mem-id>

   Exit 0 = settled into active memory, 2 = rejected, 3 = no consensus
   (still proposed — redraft or gather more votes). A settled note that sets
   `supersedes` retires the note it replaces.

5. **Report** the settled and rejected notes with their agreement, and — as
   with review — surface any dissent. Settled notes live in
   `.settlement/memory/*.md`, committed with the repo and reviewable in the
   PR like any other change.

Never edit `.settlement/memory/*.md` by hand — only the CLI writes them, so
the oracle verdict and settlement provenance stay intact.

**Settled memory is scored by outcomes.** A settled note whose scope covers a
change under review is auto-injected as guidance and recorded on that
decision. When the decision is graded (`settle outcome`), the note's adequacy
moves with the result — credit when the guided change held, a larger debit
when it was reverted. A note that falls below threshold is **quarantined**:
still visible in `settle memory list` / `settle skills`, but no longer
auto-injected into new reviews until a curator-settled revision restores it.
This is the same punitive selection that scores personas (`settle ledger`),
applied to procedures — bad guidance is pruned by evidence. Inspect it with:

    settle skills

## Multi-task and worktree hosts (Claude Code, Cursor, …)

Modern hosts develop several changes in parallel, each in its own git
worktree. Settlement is built for that:

- **Run `/settle review` inside the worktree whose change it judges.** The
  decision record is written to that worktree's `.settlement/` and commits
  with the branch — the review travels and merges together with the change
  it adjudicated. (`settle init` pre-configures union merge for
  `decisions.jsonl`, so decision logs from parallel branches combine
  without conflicts.)
- **Task ids are branch-scoped** — always use `review-<branch>-<date>` so
  parallel reviews never collide in `verdicts/`.
- **Reviews in different worktrees may run fully in parallel.** Reviewer
  subagents are read-only; they never need worktrees of their own.
- **One branch, one settlement.** If the host spread work across multiple
  worktrees, settle each branch separately: one diff, one panel, one
  decision.
- **Grade outcomes only from the main checkout**, after the branch merges.
  `settle outcome` refuses to run in a linked worktree (ledger history must
  stay linear); do not override with `--force` unless you understand the
  merge conflict you are choosing to create.
- **If `.settlement/` is missing in a worktree** (branched before init),
  rebase or merge the branch onto a base that has it — never run
  `settle init` inside a worktree.

## Rules

- Run reviewers in parallel; keep them independent.
- Never modify the code under review during a review.
- Never edit `.settlement/ledger.json` or `decisions.jsonl` by hand — only
  the CLI writes them.
- Report the tally faithfully, including outcomes you'd personally disagree
  with. If you believe the panel missed something critical, say so to the
  user as your own remark, clearly separated from the recorded decision.
