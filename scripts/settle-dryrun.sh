#!/usr/bin/env bash
# End-to-end smoke test of the settle CLI with canned verdicts — no AI, no
# network. Exercises init -> panel -> tally -> outcome -> ledger and asserts
# the expected consensus and punitive-ledger movements.
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "$0")/.." && pwd)"
WORKDIR="$(mktemp -d)"
trap 'rm -rf "$WORKDIR"' EXIT

SETTLE="$WORKDIR/settle"
(cd "$REPO_ROOT/go" && go build -o "$SETTLE" ./cmd/settle)

cd "$WORKDIR"
mkdir repo && cd repo

"$SETTLE" init >/dev/null

cat > diff.patch <<'EOF'
diff --git a/api/handler.go b/api/handler.go
index 1111111..2222222 100644
--- a/api/handler.go
+++ b/api/handler.go
@@ -10,6 +10,8 @@ func handle(w http.ResponseWriter, r *http.Request) {
 	if r.Method != http.MethodPost {
+		w.WriteHeader(http.StatusMethodNotAllowed)
+		return
 	}
 }
EOF

"$SETTLE" panel --diff-file diff.patch > panel.json
grep -q '"persona": "correctness"' panel.json

mkdir -p .settlement/verdicts/dryrun
cat > .settlement/verdicts/dryrun/correctness.json <<'EOF'
{"schema":"settle/verdict@1","persona":"correctness","decision":"approve","confidence":0.9,"reasoning":"Early return for non-POST methods is correct and closes a fallthrough bug."}
EOF
cat > .settlement/verdicts/dryrun/security.json <<'EOF'
{"schema":"settle/verdict@1","persona":"security","decision":"approve","confidence":0.8,"reasoning":"Rejecting unexpected methods reduces attack surface."}
EOF
cat > .settlement/verdicts/dryrun/api-contract.json <<'EOF'
{"schema":"settle/verdict@1","persona":"api-contract","decision":"reject","confidence":0.6,"reasoning":"405 response is missing the Allow header required by RFC 9110.","findings":[{"severity":"medium","file":"api/handler.go","line":11,"summary":"405 without Allow header"}]}
EOF

# Expect approval (exit 0) with one recorded dissent.
"$SETTLE" tally --task dryrun --panel panel.json > decision.json
python3 - <<'PY'
import json
d = json.load(open("decision.json"))
assert d["outcome"]["reached"], d["outcome"]
assert d["outcome"]["decision"] == "approve", d["outcome"]
assert d["outcome"]["dissents"] == ["api-contract"], d["outcome"]
print("tally OK:", d["id"])
PY

DECISION_ID="$(python3 -c 'import json; print(json.load(open("decision.json"))["id"])')"

# Grade the change as reverted: approvers lose power, the dissenter gains.
"$SETTLE" outcome --decision "$DECISION_ID" --result reverted >/dev/null
python3 - <<'PY'
import json
l = json.load(open(".settlement/ledger.json"))["personas"]
assert l["correctness"]["voting_power"] < 1.0, l["correctness"]
assert l["security"]["voting_power"] < 1.0, l["security"]
assert l["api-contract"]["voting_power"] > 1.0, l["api-contract"]
print("ledger OK:", {p: round(e["voting_power"], 2) for p, e in sorted(l.items())})
PY

"$SETTLE" log | grep -q "result=reverted"

# Recall should surface the reverted precedent when the same file changes
# again, and stay silent for an unrelated query.
"$SETTLE" recall --file api/handler.go --json > recall.json
python3 - <<'PY'
import json
hits = json.load(open("recall.json"))
assert len(hits) == 1, hits
assert hits[0]["result"] == "reverted", hits[0]
assert "api/handler.go" in hits[0]["matched_files"], hits[0]
assert hits[0]["dissents"] == ["api-contract"], hits[0]
print("recall OK:", hits[0]["id"])
PY
"$SETTLE" recall --query "kubernetes helm chart" | grep -q "no relevant precedent"

# why should explain the decision — verdict, the dissenting seat, and the
# reverted outcome — and reconstruct the file's history.
WHY="$("$SETTLE" why "$DECISION_ID")"
echo "$WHY" | grep -q "result: reverted"
echo "$WHY" | grep -q "\[DISSENT\]"
"$SETTLE" why --file api/handler.go | grep -q "$DECISION_ID"

# --- consolidation loop: candidates -> propose (oracle) -> settle ---

# The oracle refuses a claim that cites a decision that does not exist.
if echo "{\"scope\":\"api\",\"claim\":\"Grounded claim about the handler method.\",\"cites\":[\"dec-does-not-exist\"]}" \
	| "$SETTLE" memory propose 2>/dev/null; then
  echo "FAIL: oracle accepted a fabricated citation"; exit 1
fi

# A claim grounded in a real cited decision is accepted as a proposal.
CLAIM="Rejecting a non-POST method with an early return is the settled handler behavior."
PROP="$(printf '{"scope":"api","claim":"%s","cites":["%s"]}' "$CLAIM" "$DECISION_ID")"
MEMID="$(echo "$PROP" | "$SETTLE" memory propose | sed -n '1s/proposed \([^ ]*\).*/\1/p')"
test -n "$MEMID" || { echo "FAIL: propose did not return a note id"; exit 1; }
"$SETTLE" memory list --status proposed | grep -q "$MEMID"

# Convene the panel and settle the proposal with canned approving verdicts.
mkdir -p ".settlement/verdicts/mem-$MEMID"
for persona in correctness security api-contract; do
  cat > ".settlement/verdicts/mem-$MEMID/$persona.json" <<EOF
{"schema":"settle/verdict@1","persona":"$persona","decision":"approve","confidence":0.9,"reasoning":"Faithful to the cited handler decision."}
EOF
done
"$SETTLE" memory settle --id "$MEMID"
"$SETTLE" memory list --status settled | grep -q "$MEMID"
test -f ".settlement/memory/$MEMID.md"
echo "memory OK: $MEMID"

# --- skill adequacy: the settled note guides a new review, whose reverted
# outcome drops the note's adequacy score. ---
cat > guided.patch <<'EOF'
diff --git a/api/router.go b/api/router.go
index 3333333..4444444 100644
--- a/api/router.go
+++ b/api/router.go
@@ -1,2 +1,3 @@
 func route() {
+	w.Header().Set("Allow", "POST")
 }
EOF
# The panel for an api/ change should be guided by the settled api-scoped note.
"$SETTLE" panel --diff-file guided.patch > guided-panel.json
python3 - <<PY
import json
p = json.load(open("guided-panel.json"))
assert "$MEMID" in (p["subject"].get("guided_by") or []), p["subject"]
print("guidance injected OK")
PY
mkdir -p .settlement/verdicts/guided
for persona in correctness security api-contract; do
  cat > ".settlement/verdicts/guided/$persona.json" <<EOF
{"schema":"settle/verdict@1","persona":"$persona","decision":"approve","confidence":0.9,"reasoning":"Follows the settled Allow-header guidance."}
EOF
done
"$SETTLE" tally --task guided --panel guided-panel.json > guided-decision.json
GID="$(python3 -c 'import json; print(json.load(open("guided-decision.json"))["id"])')"
"$SETTLE" outcome --decision "$GID" --result reverted >/dev/null
"$SETTLE" skills | grep "$MEMID" | grep -q "reverted=1"
python3 - <<PY
import json
# adequacy dropped below the starting 1.0 after one reverted guided change
sl = json.load(open(".settlement/skills.json"))["skills"]["$MEMID"]
assert sl["adequacy"] < 1.0, sl
assert sl["reverted"] == 1 and sl["uses"] == 1, sl
print("adequacy OK:", round(sl["adequacy"], 3))
PY

# --- loop trust: five clean holds promote a loop a rung; a revert demotes it ---
for i in 1 2 3 4 5; do
  printf 'diff --git a/loop%d.go b/loop%d.go\n--- a/loop%d.go\n+++ b/loop%d.go\n@@ -1 +1,2 @@\n+l%d\n' "$i" "$i" "$i" "$i" "$i" > "loop$i.patch"
  "$SETTLE" panel --diff-file "loop$i.patch" > "loop$i-panel.json"
  mkdir -p ".settlement/verdicts/loop$i"
  for persona in correctness security api-contract; do
    echo "{\"schema\":\"settle/verdict@1\",\"persona\":\"$persona\",\"decision\":\"approve\",\"confidence\":0.9,\"reasoning\":\"trivial and correct\"}" > ".settlement/verdicts/loop$i/$persona.json"
  done
  LID="$("$SETTLE" tally --task "loop$i" --panel "loop$i-panel.json" --loop drift 2>/dev/null | python3 -c 'import json,sys; print(json.load(sys.stdin)["id"])')"
  "$SETTLE" outcome --decision "$LID" --result held >/dev/null
done
"$SETTLE" loops | grep drift | grep -q "auto-merge-trivial"
# One revert demotes drift back to propose-only.
printf 'diff --git a/loopr.go b/loopr.go\n--- a/loopr.go\n+++ b/loopr.go\n@@ -1 +1,2 @@\n+bad\n' > loopr.patch
"$SETTLE" panel --diff-file loopr.patch > loopr-panel.json
mkdir -p .settlement/verdicts/loopr
for persona in correctness security api-contract; do
  echo "{\"schema\":\"settle/verdict@1\",\"persona\":\"$persona\",\"decision\":\"approve\",\"confidence\":0.9,\"reasoning\":\"looked fine\"}" > ".settlement/verdicts/loopr/$persona.json"
done
RID="$("$SETTLE" tally --task loopr --panel loopr-panel.json --loop drift 2>/dev/null | python3 -c 'import json,sys; print(json.load(sys.stdin)["id"])')"
"$SETTLE" outcome --decision "$RID" --result reverted >/dev/null
"$SETTLE" loops | grep drift | grep -q "propose-only"
echo "loops OK"

echo "settle dry run: PASS"
