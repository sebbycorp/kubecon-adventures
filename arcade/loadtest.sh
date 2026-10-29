#!/usr/bin/env bash
# Load test: N players each hatch one duck at once, then the whole flock is shot and
# revived in a burst. Uses real LLM calls (~1.1k tokens per prompt on gpt-4.1-mini).
# Usage: ./loadtest.sh [ducks=50] [base=http://localhost:8090]
# Starts a new round (deletes all sessions) first, and leaves the Hunter off at the end.
set -euo pipefail
N=${1:-50}
B=${2:-http://localhost:8090}
A=$B/duck-hunt/api
export PATH=$HOME/.local/bin:$PATH
WORK=$(mktemp -d)
trap 'rm -rf "$WORK"' EXIT

state() { curl -s "$B/admin/duck-hunt/api/state"; }
count() { state | jq "[.ducks[] | select($1)] | length"; }
wait_for() { # wait_for <jq filter for done ducks> <timeout s>
  local t0=$SECONDS
  while [ "$(count "$1")" -lt "$N" ] && [ $((SECONDS - t0)) -lt "$2" ]; do sleep 1; done
  echo "$((SECONDS - t0))s"
}
summary() {
  state | jq -r '
    "  ducks: \([.ducks[] | .state] | group_by(.) | map("\(.[0])=\(length)") | join(" "))",
    "  errors: \([.ducks[] | select(.error) | .error] | group_by(.) | map("\(length)x \(.[0][0:80])") | join("; ") | if . == "" then "none" else . end)",
    (.timings | to_entries[] | "  \(.key): n=\(.value.count) p50=\(.value.p50)ms p95=\(.value.p95)ms"),
    "  substrate: running=\(.substrate.running) actors=\(.substrate.actors) workers=\(.substrate.workers) busy=\(.substrate.busyWorkers) \(.substrate.byState)",
    "  llm: calls=\(.llmCalls) tokens=\(.tokens.inputTokens + .tokens.outputTokens)"'
  kubectl --context kind-kagent top nodes --no-headers 2>/dev/null | awk '{printf "  node %s cpu=%s mem=%s\n", $1, $2, $4}'
}

echo "==> New round, Hunter off"
curl -s -X POST "$B/admin/duck-hunt/api/reset" >/dev/null
curl -s -X POST "$B/admin/duck-hunt/api/settings" -d '{"hunterOn":false,"idleSeconds":20}' >/dev/null

echo "==> $N players join"
seq "$N" | xargs -P 32 -I{} curl -s -c "$WORK/{}" -X POST "$B/api/join" -d '{"nick":"bot{}"}' -o /dev/null

echo "==> $N ducks hatch at once"
kinds=(fast zigzag showoff)
seq "$N" | xargs -P "$N" -I{} sh -c 'curl -s -b "$1/$2" -X POST "$3/hatch/$4" -o /dev/null' _ "$WORK" {} "$A" "${kinds[0]}"
echo "  all flying/failed after $(wait_for '.state == "flying" or .state == "failed"' 300)"
summary

echo "==> Hunter shoots the whole flock (idle 5s)"
curl -s -X POST "$B/admin/duck-hunt/api/settings" -d '{"hunterOn":true,"idleSeconds":5}' >/dev/null
echo "  all shot after $(wait_for '.state == "shot" or .state == "failed"' 120)"
curl -s -X POST "$B/admin/duck-hunt/api/settings" -d '{"hunterOn":false,"idleSeconds":20}' >/dev/null

echo "==> Every owner revives at once"
state | jq -r '.ducks[] | select(.state == "shot") | "\(.nick | ltrimstr("bot")) \(.id)"' |
  xargs -P "$N" -n 2 sh -c 'curl -s -b "$1/$3" -X POST "$2/ducks/$4/revive" -o /dev/null' _ "$WORK" "$A"
echo "  all flying/failed after $(wait_for '(.state == "flying" and (.busy | not)) or .state == "failed"' 300)"
summary
echo "Hunter left off. Start a new round from /admin when done."
