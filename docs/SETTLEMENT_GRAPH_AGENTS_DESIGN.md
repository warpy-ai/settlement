# Settlement Graph Agents — Design Proposal

**Turning Settlement into an agentic-skills platform: spin up agents through graphs and nodes, and *settle* changes on a codebase through consensus.**

Companion to [`AI_MODERNIZATION_ROADMAP.md`](./AI_MODERNIZATION_ROADMAP.md). Where the roadmap modernizes what Settlement is, this document proposes what Settlement becomes.

---

## 1. The pitch

Settlement today answers questions by making a panel of diverse LLMs vote. The insight worth expanding: **a code change is just another proposal that needs consent.**

[Graphify](https://github.com/Graphify-Labs/graphify) shows one half of the picture: a codebase can be transformed into a queryable knowledge graph — tree-sitter AST extraction produces typed nodes (functions, classes, modules, docs, ADRs) and edges (`calls`, `imports`, `depends_on`) with explicit provenance (`EXTRACTED` vs `INFERRED`), and Leiden community detection reveals subsystems. But graphify is **read-only**: it helps an assistant *understand* code.

Settlement supplies the missing half: **adjudication**. Fuse them and you get a system where:

1. A **code knowledge graph** describes the codebase — its entities, dependencies, subsystems, and blast radii.
2. **Agents are spawned from the graph** — one maker per affected subsystem, review panels sized by the blast radius of the change.
3. **Changes are settled, not merged** — every proposed diff goes through Settlement's weighted-consensus engine before it touches the tree, with the decision and its full provenance written back into the graph.

Tagline: *graphify tells agents what the code is; Settlement decides what the code becomes.*

No existing framework occupies this spot. LangGraph/CrewAI/AutoGen orchestrate agents through *hand-authored workflow graphs*; graphify builds *code graphs* but takes no actions; SWE-agents produce diffs but review is a single-model afterthought. Settlement's differentiator — multi-provider weighted voting with fallback synthesis — is exactly the missing adjudication layer for autonomous code change.

---

## 2. Core concepts

### 2.1 Two graphs, one system

| | Knowledge Graph (KG) | Agent Graph (AG) |
|---|---|---|
| What | The codebase as nodes/edges (graphify-style) | The execution plan for one change request |
| Nodes | functions, classes, modules, configs, docs, ADRs, communities | planner, makers, reviewers, oracles, settler |
| Edges | `calls`, `imports`, `depends_on`, `references` (+ provenance tag) | data/control flow between agents |
| Lifetime | persistent, rebuilt incrementally per commit | ephemeral, one per proposal |
| Derived from | tree-sitter AST + LLM inference for docs | **derived from the KG** — this is the key move |

The Agent Graph is not hand-authored (the LangGraph approach). It is **computed from the Knowledge Graph** per change request: which KG nodes does the request touch, which communities do they belong to, how central are they, what depends on them. Graph topology decides team topology.

### 2.2 Spawning agents through nodes

```
change request ("add rate limiting to the session API")
      │
      ▼
┌─ Planner ──────────────────────────────────────────────┐
│ KG query: locate target nodes (session_config.go,      │
│ api_server.go), expand 1–2 hop neighborhood,           │
│ collect communities touched, compute blast radius      │
└────────────────────────────────────────────────────────┘
      │  emits Agent Graph
      ▼
┌─ Makers (1 per touched community) ─────────────────────┐
│ context = scoped subgraph + file contents of its       │
│ community only. Produces a diff proposal in a worktree │
└────────────────────────────────────────────────────────┘
      │  proposals
      ▼
┌─ Review panel (Settlement consensus) ──────────────────┐
│ LLM reviewers: correctness / security / API-contract / │
│ style — provider-diverse, persona-differentiated       │
│ Oracle voters: build, tests, linter (deterministic)    │
└────────────────────────────────────────────────────────┘
      │  weighted verdict
      ▼
┌─ Settler ──────────────────────────────────────────────┐
│ consensus reached → apply to branch, open PR,          │
│ write Decision node + edges into the KG                │
│ no consensus → synthesize feedback, loop back to maker │
└────────────────────────────────────────────────────────┘
```

**Blast-radius policy (deterministic, from graph metrics):** panel size and agreement threshold are computed from the KG, not vibes:

- touched node degree (god node? → bigger panel, higher threshold)
- number of communities in the 1-hop neighborhood (cross-cutting change → add an architecture reviewer)
- test-coverage edges present or absent (uncovered code → oracle weight increases, an added test is mandatory for consensus)
- `INFERRED`-edge density (low-confidence region → require a human co-signer)

This replaces/extends today's LLM-guessed `task_analyzer.go` requirements with graph-derived, explainable policy — an LLM meta-call can still adjust within bounds, but the floor comes from topology.

### 2.3 Settling a change: the proposal lifecycle

```
Draft → Deliberation → Settled | Rejected | Escalated
```

- **Draft:** maker produces a diff in an isolated worktree (loop-engineering isolation; Settlement already runs process-per-worker).
- **Deliberation:** the existing consensus machinery adjudicates, with two ballot types:
  - `exact_match` on reviewer verdicts `{approve, reject, revise}` — maps directly onto today's strategy (`types.go:53-58`).
  - competing diffs from multiple makers → judge panel ranks them (a new `rank_match` strategy); `merge_match` + the existing LLM synthesizer composes the *feedback* message for revision rounds.
- **Oracles as voters — hybrid deterministic/LLM voting.** Compilers, test suites, and linters register as agents with high fixed `VotingPower` whose "reasoning" is their raw output. A failing build is a veto (weight = ∞ in practice). This grounds the panel in objective signal — the thing pure-LLM review lacks.
- **Settled:** diff applies to the feature branch, PR opens, and a `Decision` node is written into the KG with edges to every code node it modified, every vote cast, and every precedent cited.
- **Escalated:** two failed revision rounds → human queue (Discord/PR comment), consistent with the roadmap's human-in-the-loop rule.

### 2.4 Proof-of-Adequacy finally gets real ground truth

The whitepaper's punitive feedback loop (`settlement_2024.tex:415-425`) was unimplementable for opinion tasks — there is no oracle for "was this moderation call right?" **Code changes the game:** every settled change eventually reveals its quality — CI on the PR, review outcome, revert/hotfix within N days, defect issues linked back.

Wire those signals into per-agent `VotingPower` (today hardcoded `1.0`, `supervisor.go:370`):

- reviewer approved a change that was later reverted → power down
- reviewer dissented on a change that was later reverted → power up
- maker's proposals settle first-round at high rate → gets harder tasks / larger blast-radius clearance

Agent reputation becomes a *queryable property of the graph* — per subsystem: an agent may be high-trust in `go/core` and low-trust in `frontend/`, because reputation edges attach to communities, not just globally.

### 2.5 Case law for code (GraphRAG in the review loop)

Every Decision node accretes into precedent. Before a panel deliberates, retrieve prior decisions touching the same subgraph and inject the one-hop story: *"the last settled change to `checkConsensus` broke `merge_match` — the dissent that predicted it came from the security reviewer."* Multi-hop, connection-aware retrieval is exactly where graph beats flat RAG, and it gives panels institutional memory — the consistency of a court, not the amnesia of a chat.

### 2.6 Skills: the packaging model

Two directions, both first-class:

**Settlement *as* a skill** — how users touch it:
- `/settle <change request>` — plan from the graph, spawn the panel, deliberate, open a settled PR. Installable in Claude Code/Cursor/Gemini CLI the way `/graphify` is.
- `/settle review <PR|diff>` — adjudicate an existing human diff with a panel (adoptable *today* with zero trust required: humans keep merge rights, Settlement just files a structured multi-model review).
- `/settle why <file|function>` — provenance query: which decisions shaped this node, who voted, what dissents aged well.
- Exposed as an **MCP server** (`settlement-mcp`) with tools: `graph_query`, `plan_change`, `spawn_panel`, `propose`, `settle_status`, `provenance` — so any MCP-capable agent can request consent for its own changes. That's the endgame: **Settlement as the consent layer other agents call before mutating a repo.**

**Skills *from* the graph** — how agents get competence:
- Each KG community compiles into a **subsystem skill**: an auto-generated, auto-refreshed `SKILL.md` containing the community's subgraph summary, entry points, conventions mined from ADR/`# WHY:` nodes, and its decision history. Makers spawned for that community load its skill as context. Skills stop being hand-written docs that rot — they are *views over the graph*, rebuilt on every commit (graphify's hook model).
- Node-type → skill bindings: SQL schema node → migration skill; proto file → codegen skill; CI config → pipeline skill.

### 2.7 Autonomous maintenance loops (loop engineering closes the circle)

With durable graph + scheduler (roadmap Phase 2), Settlement stops waiting for requests — loops *find work* in the graph:

- **drift loop:** nightly graph rebuild diff; undocumented new hubs, broken `references` edges → auto-proposals
- **decay loop:** orphan nodes, dependency cycles, dead exports → cleanup proposals settled by low-cost panels
- **coverage loop:** high-centrality nodes with no test edges → test-writing proposals (oracle-verifiable by construction)
- **precedent-consistency loop:** new code contradicting a settled Decision (e.g. reintroducing a rejected pattern) → flag with citation

Each loop is exactly Osmani's shape: automation finds work → maker acts → verifier panel settles → graph is the external memory. Human trust is dialed per loop: start with "file PRs only," graduate to auto-merge for decay-class changes once the loop's revert rate earns it (Proof-of-Adequacy applies to loops too).

---

## 3. Reuse map — what Settlement already has

| Existing asset | Role in the new design |
|---|---|
| Supervisor + gRPC worker pool (`supervisor.go`, `cmd/worker/`) | Agent runtime: spawn/health/scale panels. Workers gain tools (graph query, file read, worktree write) instead of prompt-in/JSON-out |
| Consensus engine (`queue_manager.go`) | The settler: verdict ballots via `exact_match`; revision-feedback synthesis via `merge_match` + the existing LLM synthesizer; add `rank_match` |
| Provider diversity + fresh-worker retries | Panel integrity: no provider majority reviews its own maker's diff |
| Task analyzer (`task_analyzer.go`) | Planner seed: keep LLM adjustment, add graph-derived floors (blast radius) |
| Session API (per-worker prompts/models) | Panel configuration API: sessions become named, versioned panel templates |
| `react-force-graph-2d` frontend | Real UI at last: render the KG + live deliberations + decision provenance instead of worker liveness |
| Whitepaper bias framework (§3) | Reviewer persona design: controlled-perspective panels (security-paranoid, perf-focused, API-conservative…) |

**Don't rebuild extraction.** Graphify already emits a JSON graph, GraphML, and Cypher, has MCP transports, and auto-rebuild hooks. Phase A should *consume graphify's `graphify-out/` JSON* as the KG import format (subject to license check), with a thin Go importer into Postgres. Reimplementing tree-sitter extraction across 40 languages is wasted motion; Settlement's value-add is everything downstream of the graph.

---

## 4. Build plan

| Phase | Deliverable | Notes |
|---|---|---|
| **A — Graph substrate** (2–3 wk) | KG importer (graphify JSON → Postgres/pgvector), `graph_query`/`provenance` MCP tools, embeddings per node | Depends on roadmap Phase 0 (persistence). Embeddings double as the roadmap's `semantic_match` fix |
| **B — Review-only settlement** (2–3 wk) | `/settle review`: panel adjudicates an existing diff; verdict ballots through the existing consensus engine; posts structured review + dissents to the PR | **Ship this first.** Zero write-trust needed, immediately useful, exercises the whole pipeline, and starts accumulating Decision nodes + reviewer track records |
| **C — Graph-spawned makers** (3–4 wk) | Planner (blast-radius policy), community-scoped makers in worktrees, oracle voters (build/test/lint), revision loop, settled PRs | The full §2.2 pipeline |
| **D — Skills + reputation** (2–3 wk) | Subsystem skills compiled from communities, `/settle` skill packaging for assistants, Proof-of-Adequacy wiring from PR outcomes into `VotingPower` | |
| **E — Autonomous loops** (2 wk) | drift/decay/coverage loops on the scheduler, per-loop trust dial | Requires roadmap Phase 2 scheduler |

Phase B is the wedge: it converts Settlement from "interesting ensemble demo" to "tool teams run on every PR," and every review it performs is training data for the reputation system that later justifies letting makers write code.

---

## 5. Open questions

1. **Graphify licensing/interop** — confirm license permits consuming its output format; otherwise define our own compatible JSON schema (it's a simple typed node/edge list) and support graphify import behind a flag.
2. **Graph store** — Postgres+pgvector is enough through Phase D; revisit Neo4j/Memgraph only if precedent retrieval needs >3-hop traversals at scale.
3. **Diff-level vs hunk-level ballots** — start diff-level (simpler); hunk-level settling enables partial acceptance but complicates the worktree story.
4. **Cost envelope** — a 5-reviewer panel per PR is ~10–20 LLM calls; blast-radius policy is also the *cost* policy (small change → 3-voter cheap-model panel). Track per-decision cost as a first-class metric from day one.
5. **Security** — makers execute code (tests) on proposed diffs; worktrees must run in sandboxed runners, and oracle results must be unforgeable by LLM voters (separate channel, signed results).

---

## 6. References

- [Graphify — knowledge graphs for codebases](https://github.com/Graphify-Labs/graphify)
- [Addy Osmani — Loop Engineering](https://addyosmani.com/blog/loop-engineering/)
- [LangGraph vs CrewAI vs AutoGen — 2026 orchestration guide](https://dev.to/pockit_tools/langgraph-vs-crewai-vs-autogen-the-complete-multi-agent-ai-orchestration-guide-for-2026-2d63)
- [Graph-Based Agent Workflow Orchestration in Production: 2026 Landscape](https://zylos.ai/research/2026-04-14-graph-based-agent-workflow-orchestration-production/)
- Settlement whitepaper: bias panels (`settlement_2024.tex:301-412`), Proof-of-Adequacy (`settlement_2024.tex:415-425`)
- [`AI_MODERNIZATION_ROADMAP.md`](./AI_MODERNIZATION_ROADMAP.md) — foundational phases this design assumes
