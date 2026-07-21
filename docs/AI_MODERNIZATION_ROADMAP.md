# Settlement — AI Modernization Roadmap

**Bringing Settlement to modern AI development standards: multi-agent tasks, loop engineering, and graph engineering.**

This document is the result of a full audit of the current codebase (`go/`, `frontend/`, `discord/`, whitepaper `settlement_2024.tex`) measured against two emerging practice areas:

- **Loop engineering** — designing autonomous systems that prompt, verify, remember, and schedule agent work instead of a human driving each turn ([Addy Osmani, "Loop Engineering"](https://addyosmani.com/blog/loop-engineering/)).
- **Graph engineering / GraphRAG** — modeling knowledge and provenance as a graph so agents retrieve *connections*, not just documents, enabling multi-hop reasoning and auditability ([Graph Engineering vs RAG guide](https://youmind.com/landing/x-viral-articles/graph-engineering-vs-rag-guide)).

---

## 1. Where Settlement stands today (verified against the code)

### What is real and works

The Go backend (`go/`, ~5,600 LOC, Go 1.24) is a genuine **multi-LLM consensus/ensemble service**:

- **Multi-provider worker pool.** Each worker is a separate OS process wrapping one LLM client behind gRPC (`core/supervisor.go`, `cmd/worker/main.go`). All five providers are implemented with official SDKs: OpenAI, Anthropic, Google Gemini, Cohere, Mistral (`llm-client/*.go`). Workers get randomized provider/model assignments for diversity (`supervisor.go:138-157`).
- **Real consensus engine** (`core/queue_manager.go`, 1,715 LOC): four match strategies (`exact_match`, `semantic_match`, `numeric_match`, `merge_match`), confidence-weighted voting (`vote = VotingPower × Confidence`, `queue_manager.go:1445`), LLM-assisted synthesis for subjective questions ("The Council" persona), retry-with-fresh-workers, rate-limit awareness, layered algorithmic fallbacks.
- **LLM meta-planning.** Task requirements (worker count, agreement threshold, strategy) are themselves decided by an LLM call (`task_analyzer.go:51-133`).
- **Operational features:** health checks, auto-restart, dynamic scaling 3→15 workers, session API with per-worker config, Docker + a manual GHCR deploy workflow.

### What is weak or broken

| Gap | Evidence |
|---|---|
| **No persistence at all** — every task, vote, and result dies on restart | in-memory maps in `api_server.go:19-20`, `pool_manager.go:12-13`, `queue_manager.go:26-34` |
| **Zero tests, no CI on push/PR** | no `*_test.go` anywhere; `.github/workflows/ci.yml` is `workflow_dispatch`-only, single-actor gated |
| **No feedback/evaluation loop** | `VotingPower` is always `1.0` (`supervisor.go:370`) and never adjusted; the whitepaper's Punitive Proof-of-Adequacy (`settlement_2024.tex:415-425`) is unimplemented |
| **Hand-rolled "semantic" matching** | word-frequency Jaccard similarity with stop-words (`queue_manager.go:714-790`) — no embeddings |
| **Frontend contract broken** | `GET /api/v1/tasks` doesn't exist server-side (only POST, `api_server.go:54`); `DisallowUnknownFields` rejects the form payload; sessions unused |
| **Discord bot is a Hello-World stub** | one `/ping` command, zero integration with the engine |
| **No observability** | `log.Printf` only; no metrics, traces, or structured decision records |
| **Streaming unimplemented** | all `CallStreaming` methods return "not yet implemented" |

**Verdict:** the whitepaper's vision is ~20% realized. What exists is a solid ensemble-inference core — a strong foundation — but it is a *stateless request/response service*, not yet a modern agentic system.

---

## 2. Gap analysis against the three pillars

### 2.1 Multi-agent tasks

Settlement already *is* multi-agent in the ensemble sense, but modern multi-agent systems add three things the code lacks:

1. **Role differentiation.** Workers are interchangeable voters. The whitepaper promises persona/bias-differentiated agents (cultural, economic, ethical reviewers — `settlement_2024.tex:301-412`); the code never sets a per-agent system prompt beyond the shared default (`worker_server.go:185-218`).
2. **Verifier separation.** No agent ever checks another agent's output. Consensus is arithmetic over first-pass answers. Modern practice separates *maker* agents from *verifier/critic* agents so "done" means something.
3. **Tool use.** Workers answer from parametric knowledge only — no retrieval, no tools, no grounding. For content-moderation or law-voting use cases, this is disqualifying: decisions must cite policy/precedent.

### 2.2 Loop engineering

Osmani's framework names five components: **automations** (scheduled triggers that find work), **external state/memory** (progress survives between runs), **verifier subagents**, **skills** (codified project knowledge), and **worktrees/parallel isolation**. Settlement's score:

| Component | Status |
|---|---|
| Automations / scheduling | ❌ none — purely reactive HTTP API |
| External state / memory | ❌ none — everything in-memory |
| Verification | ⚠️ partial — consensus is a weak verifier of individual answers, but nothing verifies the *consensus* itself, and there is no evaluation dataset |
| Skills / codified knowledge | ❌ none — no CLAUDE.md/SKILL.md, prompts are hardcoded string literals in Go |
| Parallel isolation | ✅ effectively yes — process-per-worker with fresh-worker retries |

The deepest miss: **the feedback loop**. A loop that runs unattended needs its "it's done" to mean something. Settlement records nothing about whether past decisions were *good*, so it cannot improve, cannot adjust voting power, and cannot be trusted unattended.

### 2.3 Graph engineering

There is **no graph anywhere in the backend** — the only graph is a frontend visualization (`WorkerGraph.tsx`). Yet Settlement's domain is almost uniquely suited to a knowledge graph:

- Every decision produces natural graph structure: `Task —submitted_by→ Session`, `Worker —voted→ Answer —grouped_into→ ConsensusGroup —decided→ Decision`, `Decision —cites→ Policy/Precedent`.
- The whitepaper's core selling points — *auditability, provenance, bias balance* — are exactly what GraphRAG provides: precise tracking of how answers are derived, multi-hop reasoning over prior decisions, and contextual retrieval of connected precedents rather than isolated text chunks.
- The hand-rolled Jaccard similarity is the weakest link in consensus quality; embeddings + a decision graph replace it outright.

---

## 3. Modernization roadmap

### Phase 0 — Foundations (prerequisite for everything else)

Nothing agentic can be trusted without these:

1. **Persistence.** Introduce Postgres (with `pgvector`) as the system of record for tasks, worker votes, consensus outcomes, sessions, and agent performance history. SQLite is acceptable for dev. This is simultaneously the "external state" of loop engineering and the substrate for the decision graph.
2. **Tests + CI on push/PR.** Unit tests for `checkConsensus`, `mergeConsensus`, `calculateSimilarity`, and the task analyzer (these are pure-ish functions — highly testable); a `httptest` suite for the API; `go vet`/`golangci-lint`; frontend `tsc --noEmit` + build. Make `ci.yml` trigger on PRs, not just manual dispatch.
3. **Fix the frontend contract.** Add `GET /api/v1/tasks` (list), align `TaskForm` payload with `TaskSubmission`, adopt the session flow, and actually use the already-installed `react-query`. Alternatively, generate the client from an OpenAPI spec so drift becomes impossible.
4. **Observability.** Structured logging (slog), Prometheus metrics per worker/provider (latency, cost, tokens, agreement rate), and a persisted **decision record** per task: who voted what, with what confidence, which group won, why.

### Phase 1 — Multi-agent tasks done properly

1. **Agent profiles as data, not code.** Move system prompts out of `worker_server.go` into versioned profile definitions (DB or `profiles/*.yaml`): role, persona/bias configuration, provider/model, tools allowed. This directly implements the whitepaper's controlled-bias design (§3 of the README) — a *balanced panel* of profiles per task instead of `random`.
2. **Maker/verifier separation.** Add a verifier agent stage after consensus: an independent model (different provider than the majority) that checks the winning answer against the task rules and, for factual categories, against retrieved evidence. Verifier rejection triggers the existing retry path. This is the single highest-leverage agentic upgrade — the existing retry machinery (`queue_manager.go:112-173`) already supports it.
3. **Implement Proof-of-Adequacy (the whitepaper's promise).** After each decision, score each worker: agreement-with-final-consensus, verifier assessment, and (where available) delayed ground truth. Persist per-agent performance and make `VotingPower` a function of trailing accuracy instead of the constant `1.0`. This converts consensus from a static vote into a learning system.
4. **Tool-enabled workers.** Give workers retrieval tools (see Phase 3) and structured function-calling instead of "reply in strict JSON" prompting — every provider SDK in `go.mod` already supports native structured outputs/tool calls; use them and delete the fragile JSON-parsing path.
5. **Adopt streaming** (the stubs already exist) so the frontend can show live deliberation instead of 5s polling.

### Phase 2 — Loop engineering

1. **Event-driven core.** Replace bare Go channels with a durable queue (NATS JetStream or Postgres-backed river/queue). Tasks become durable jobs; a crash no longer loses work. This unlocks scheduled and recurring work.
2. **Automations.** Add a scheduler (cron-style) so loops can *find work* themselves:
   - **Re-adjudication loop:** periodically re-run past decisions whose verifier confidence was low or where new policy/precedent has landed; flag drift.
   - **Calibration loop:** nightly, replay a golden dataset of tasks with known answers; recompute per-agent accuracy and refresh VotingPower; alert on regression.
   - **Health/cost loop:** hourly provider health + spend rollups; demote flaky providers automatically (today a dead provider is only noticed via rate-limit string matching, `queue_manager.go:1488-1493`).
3. **Evaluation as a first-class artifact.** Build a golden-set eval harness (tasks + expected decisions per category) run in CI and nightly. A loop without an evaluator is a loop making mistakes unattended — this is what makes "done" mean something.
4. **Codified knowledge ("skills").** Add `CLAUDE.md` / agent-readable docs describing the architecture, consensus rules, and prompts-as-data, so both human contributors and coding agents working on this repo operate from the same codified knowledge. Prompt templates move to versioned files with changelogs, enabling prompt A/B evaluation in the eval harness.
5. **Human-in-the-loop escalation.** When consensus fails twice or the verifier rejects twice, escalate to a human queue (this is where the Discord bot finally earns its place — see Phase 4).

### Phase 3 — Graph engineering

1. **Decision knowledge graph.** Model in Postgres (graph tables + pgvector) or Neo4j if traversal depth demands it:
   - Nodes: `Task`, `Agent(profile, provider, model)`, `Vote`, `Decision`, `Category`, `Policy/Rule`, `Session`.
   - Edges: `voted_on`, `agreed_with`, `contradicts`, `cites`, `similar_to (embedding)`, `superseded_by`.
2. **Embeddings replace Jaccard.** Store an embedding per task and per answer; `semantic_match` grouping becomes cosine similarity over embeddings instead of word-frequency overlap (`calculateSimilarity`, `queue_manager.go:747-790`). This is a drop-in quality win for the weakest part of the consensus engine.
3. **GraphRAG for deliberation context.** Before workers vote, retrieve the k nearest *prior decisions* plus their one-hop neighborhood (cited rules, dissenting votes, reversals) and inject as context: *"Similar case #123 was decided X because Y; it was later overturned."* This gives Settlement case-law-style consistency — exactly the multi-hop, connection-aware retrieval where GraphRAG outperforms plain RAG, and it grounds decisions instead of relying on parametric knowledge.
4. **Provenance & audit API.** Expose `GET /decisions/{id}/provenance` returning the subgraph that produced a decision (votes, weights, precedents cited, verifier verdict). This delivers the whitepaper's transparency claims as a queryable artifact, and gives the frontend's force-graph view real data: the *decision graph*, not just worker liveness.
5. **Bias-balance analytics on the graph.** With agents, votes, and profiles as graph data, bias auditing becomes a query: per-profile agreement rates, systematic dissent patterns, provider correlation clusters. This operationalizes the whitepaper's §3 study design.

### Phase 4 — Surfaces

- **Discord bot as a consent surface:** `/decide <question>` submits a task, streams deliberation, posts the decision with a provenance link; escalation queue messages for human review. (Rust bot calls the Go API — the integration that currently doesn't exist.)
- **Frontend:** decision explorer over the provenance graph, agent leaderboard (VotingPower history), eval dashboard.

---

## 4. Sequencing and effort

| Phase | Contents | Effort | Depends on |
|---|---|---|---|
| 0 | Persistence, tests, CI, contract fix, observability | 2–3 weeks | — |
| 1 | Profiles, verifier stage, Proof-of-Adequacy, structured outputs | 2–3 weeks | 0 |
| 2 | Durable queue, automations, eval harness, escalation | 2–3 weeks | 0, partially 1 |
| 3 | Decision graph, embeddings, GraphRAG context, provenance API | 3–4 weeks | 0 |
| 4 | Discord + frontend surfaces | 1–2 weeks | 1–3 |

Phases 1–3 are parallelizable after Phase 0; the embedding work in Phase 3.2 is small and could be pulled forward as a quick win.

**Recommended first PR series:**
1. Postgres schema + repository layer; persist tasks/votes/decisions (write-through alongside the existing in-memory maps to keep risk low).
2. Consensus unit tests + CI on PR.
3. `GET /tasks` + frontend contract fix.
4. Embedding-based `semantic_match`.
5. Verifier agent stage + persisted decision records → then VotingPower feedback.

---

## 5. References

- [Addy Osmani — Loop Engineering](https://addyosmani.com/blog/loop-engineering/) (also on [O'Reilly Radar](https://www.oreilly.com/radar/loop-engineering/))
- [Graph Engineering vs RAG guide](https://youmind.com/landing/x-viral-articles/graph-engineering-vs-rag-guide)
- [Neo4j — GraphRAG and agentic architecture](https://neo4j.com/blog/developer/graphrag-and-agentic-architecture-with-neoconverse/)
- [Memgraph — How to build Agentic GraphRAG](https://memgraph.com/blog/build-agentic-graphrag-ai)
- Whitepaper: `settlement_2024.tex` §§ bias design (301–412), Punitive Proof-of-Adequacy (415–425)
