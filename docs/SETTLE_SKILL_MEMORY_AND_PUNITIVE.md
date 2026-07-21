# The `/settle` Skill — Memory, Punitive Algorithms, and Packaging Options

Third document in the series ([roadmap](./AI_MODERNIZATION_ROADMAP.md) → [graph agents design](./SETTLEMENT_GRAPH_AGENTS_DESIGN.md) → this). It answers three questions:

1. What does [hermes-agent](https://github.com/NousResearch/hermes-agent)-style **persistent memory** look like inside Settlement?
2. How do the whitepaper's **punitive algorithms** (Punitive Proof-of-Adequacy, `settlement_2024.tex:415-425`) become a concrete mechanism — not just for agents, but for memory and skills themselves?
3. Can all of this ship as **a single skill**? (Short answer: a single *installable unit*, yes — a single SKILL.md, no. Options below.)

---

## 1. What hermes-agent contributes

Hermes-agent ("the agent that grows with you") demonstrates the memory/skill patterns worth stealing:

- **Agent-curated persistent memory** — conversation history + structured profiles, FTS5 full-text search + LLM summarization for cross-session recall, with periodic *nudges* prompting the agent to consolidate what it learned.
- **Autonomous skill creation** — after completing a complex task, the agent captures the procedure as a reusable skill; skills **self-improve during use** as edge cases are hit.
- **Skills-hub interoperability** — compatible with the agentskills.io open standard.
- **Scheduler + multi-platform gateway** — cron-style unattended loops; one backend reachable from Discord/Telegram/Slack/terminal.

**The gap Settlement is uniquely positioned to fill:** in Hermes, nothing *adjudicates* the learning. The agent writes its own memory and edits its own skills unilaterally — self-improvement with no verifier is drift waiting to happen (a mis-learned procedure silently corrupts every future run that loads it). Settlement's thesis applied to memory:

> **Learning is a change proposal. Memory writes and skill edits get settled, not just saved.**

That single idea fuses all three threads — Hermes memory, graph agents, and punitive algorithms — into one coherent system.

---

## 2. Memory architecture: three tiers, all graph-grounded

All three tiers live in the same Postgres+pgvector store as the knowledge graph (roadmap Phase 0/A) — memory is not a separate database, it's **more nodes and edges**.

| Tier | Contents | Hermes analog | Settlement twist |
|---|---|---|---|
| **Episodic** | Session transcripts, task submissions, every vote/dissent, deliberation traces. FTS + embeddings for recall | conversation history + session search | Append-only; already implied by roadmap Phase 0 decision records |
| **Semantic** | The knowledge graph + `Decision` precedent nodes + entity profiles (repos, subsystems, users, policies) | Honcho-style user modeling | GraphRAG recall: retrieve *connected* precedent, not isolated snippets |
| **Procedural** | Skills: subsystem SKILL.md files compiled from graph communities, plus learned procedures captured after settled changes | autonomous skill creation, self-improving skills | **Skill edits are proposals** — a panel settles them before they persist (see §3) |

**Consolidation loop (Hermes' "nudges" as a Settlement automation).** A scheduled loop (roadmap Phase 2 scheduler) runs after N settled decisions or nightly:

1. A **curator agent** reviews recent episodic memory and drafts consolidation proposals: "add to `go/core` skill: `DisallowUnknownFields` means API payload changes must update both structs and frontend types"; "record precedent link: decision #84 supersedes #61".
2. Each proposal is settled by a lightweight panel (cheap models, 3 voters) with one **retrieval oracle**: a deterministic check that the claim is actually supported by the cited episodes (no hallucinated memories).
3. Settled consolidations write to semantic/procedural tiers **with provenance edges** back to the episodes that justified them. Every memory answers "why do you believe this?" — same auditability as code decisions.

**Forgetting is also settled.** A decay loop proposes archiving memories that are old, uncited, or contradicted by newer settled decisions. Contradiction between a memory and a new decision is detectable as a graph query (both attach to the same code nodes).

---

## 3. Punitive algorithms, concretized

The whitepaper's Punitive Proof-of-Adequacy finally gets teeth, because code outcomes are objective (CI, reverts, hotfixes). Three punitive ledgers, one mechanism:

### 3.1 Agents (voting power)

Per-agent, **per-community** voting power `VP(a, c)` (an edge property in the graph, replacing the hardcoded `1.0` at `supervisor.go:370`):

```
VP(a,c) ← clamp( VP(a,c) + α·aligned − β·misjudged − γ·(VP(a,c) − 1) , VP_min , VP_max )
```

- `aligned`: agent's vote matched eventual ground truth (change held / was rightly rejected)
- `misjudged`: approved a change later reverted, or dissented on one that held. **Asymmetry matters:** β > α for approvals-gone-bad (rubber-stamping is the dangerous failure mode); dissent that ages well earns the biggest single boost — protect the minority reporter
- `γ`-term: slow decay toward 1.0 so reputations are earnable and losable, never permanent
- Suggested starting bounds: `VP ∈ [0.1, 2.5]`, α=0.05, β=0.15, γ=0.02

**Quarantine, not execution.** Below `VP_quarantine` (~0.4), an agent isn't killed — it's demoted to **shadow voting**: it still receives tasks and votes, but its vote weight is 0 in consensus while the ledger keeps scoring it. Sustained shadow alignment → reinstatement. This is the rehabilitation path the whitepaper's "punitive" framing needs to avoid death spirals, and shadow votes are free calibration data.

**Slashing events** (immediate, large penalties): fabricating oracle results, citing nonexistent precedent (retrieval oracle catches it), approving a diff that fails the build oracle it claimed to have checked.

### 3.2 Skills (adequacy scores) — the novel part

Every skill carries an **adequacy score** updated by outcomes of the decisions that *loaded* it:

- Changes made under a skill's guidance get `used_skill` edges to it. Reverts/defects propagate a penalty along those edges; clean outcomes propagate credit.
- A skill dropping below threshold is **quarantined**: still visible, but loaded with a warning banner and excluded from auto-injection until a curator-proposed revision is settled.
- Skill *edits* are themselves scored: if a settled edit correlates with a drop in the skill's outcome quality, the edit's approving panel takes the §3.1 penalty — closing the loop between procedural memory and agent reputation.

This is the answer to Hermes' unchecked self-improvement: **skills evolve under the same punitive selection pressure as agents.** Bad procedures get pruned by evidence, not noticed by accident.

### 3.3 Loops (trust dial)

Autonomous loops (drift/decay/coverage from the design doc) each carry a trust level: `propose-only → auto-merge-trivial → auto-merge-class`. Promotion requires a revert-rate track record; any revert of an auto-merged change demotes the loop one level. Same ledger mechanism, third subject type.

**One implementation, three subjects** — agents, skills, loops are all nodes; adequacy is one edge-property algorithm. That's the payoff of putting everything in the graph.

---

## 4. "All of this in a single skill?" — the options

What must exist somewhere, regardless of packaging: (a) the skill interface (SKILL.md + subcommands), (b) the graph + memory store, (c) the panel runtime (Go supervisor/consensus), (d) the punitive ledger, (e) schedulers. The question is where each lives.

### Option A — One monolithic SKILL.md
Everything in one skill file with bundled scripts; state in files under the repo (`settlement-out/`, graphify-style).

- ✅ Simplest install; works offline; state travels with the repo in git
- ❌ A SKILL.md carrying graph schema + memory rules + punitive math blows the context budget on every load (anti-pattern: skills should progressively disclose)
- ❌ No daemon → no scheduler, no cross-repo memory, no real panel runtime (the Go engine can't live in a markdown file)
- **Verdict:** fine for a `/settle review`-only demo; dead end for the full vision

### Option B — One plugin, many skills (skill *family*)
A single installable plugin (Claude Code plugin / agentskills.io package) containing: a thin router `SKILL.md` for `/settle`, sub-skills (`review`, `plan`, `why`, `memory`) loaded on demand, reference docs, and the MCP server declaration. Auto-generated subsystem skills install into the same namespace.

- ✅ One install, one brand, one version — *feels* like a single skill to the user
- ✅ Progressive disclosure keeps per-invocation context small
- ❌ Still needs a backend for state and panels — a plugin alone can't hold the ledger or run 5-provider voting

### Option C — Thin skill + Settlement daemon (the Hermes shape)
The skill layer is a thin client; the Go service (extended with graph store, memory tiers, ledger, scheduler) is a long-running daemon — exactly Hermes' architecture: persistent backend + many surfaces (terminal skill, MCP, Discord bot, frontend). One daemon serves many repos and many assistants; memory and reputations accumulate centrally.

- ✅ Real persistence, real loops, cross-repo/cross-assistant memory; the punitive ledger is server-side (agents can't edit their own reputations — a real integrity concern if the ledger were repo-local files)
- ✅ Reuses the entire existing Go investment
- ❌ Heavier: something must run 24/7 (though a serverless/hibernating deploy à la Hermes' Modal/Daytona backends mitigates cost)

### ★ Recommendation: B + C — "one skill to the user, layered underneath"

Ship **one plugin** (Option B) as the only thing users install, backed by **one daemon** (Option C) as the only place state lives:

```
 user installs ONE thing: the settlement plugin
 ┌──────────────────────────────────────────────┐
 │  /settle (router SKILL.md — thin)            │
 │   ├─ settle:review   settle:plan   settle:why│
 │   ├─ settle:memory (recall / consolidate)    │
 │   └─ auto-generated subsystem skills         │
 │  MCP client config → settlement daemon       │
 └──────────────┬───────────────────────────────┘
                │ MCP (graph_query, propose, settle,
                │      memory_recall, ledger, provenance)
 ┌──────────────▼───────────────────────────────┐
 │  Settlement daemon (existing Go service +)   │
 │  KG + memory tiers (Postgres/pgvector)       │
 │  panel runtime (supervisor/consensus)        │
 │  punitive ledger (agents/skills/loops)       │
 │  schedulers (consolidation, decay, drift)    │
 └──────────────────────────────────────────────┘
```

**Degraded standalone mode** keeps Option A's virtue: with no daemon reachable, the skill falls back to `/settle review` using the host assistant's own model as a 1-voter panel and repo-local files for episodic notes — useful day one, and an on-ramp that advertises what the daemon adds.

### Sequencing the skill surface

1. `settle:review` against the existing engine (no graph needed) — ships in weeks
2. `settle:why` + `settle:memory recall` once Phase A (graph substrate) lands
3. Ledger + settled consolidation (§2–3) — turns on the punitive machinery
4. Auto-generated subsystem skills + skill adequacy scores — the self-improving-but-adjudicated procedural tier
5. Loop trust dials — full autonomy, earned

---

## 5. Signature ideas (what makes this *ours*)

1. **Settled memory** — an agent platform where learning itself requires consent: every memory consolidation and skill edit is a settled proposal with provenance. Nobody else does this; Hermes proves the memory patterns, Settlement adds the adjudication they lack.
2. **Punitive selection over procedures, not just agents** — skills carry adequacy scores driven by real outcomes of the changes they guided; bad learned procedures are pruned by evidence.
3. **Dissent protection as a first-class incentive** — the reward asymmetry (aging-well dissent pays most, rubber-stamping costs most) makes panels adversarial by economics, not by prompt begging.
4. **One ledger, three subjects** — agents, skills, and loops under a single graph-native adequacy algorithm.
5. **Rehabilitation over exile** — shadow voting turns punishment into calibration data.

---

## 6. References

- [hermes-agent — NousResearch](https://github.com/NousResearch/hermes-agent)
- [Graphify — Graphify-Labs](https://github.com/Graphify-Labs/graphify)
- [Addy Osmani — Loop Engineering](https://addyosmani.com/blog/loop-engineering/)
- [agentskills.io — open skill standard](https://agentskills.io)
- Whitepaper punitive framework: `settlement_2024.tex:415-425`
- Prior docs: [`AI_MODERNIZATION_ROADMAP.md`](./AI_MODERNIZATION_ROADMAP.md), [`SETTLEMENT_GRAPH_AGENTS_DESIGN.md`](./SETTLEMENT_GRAPH_AGENTS_DESIGN.md)
