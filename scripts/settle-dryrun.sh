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

echo "settle dry run: PASS"
