#!/usr/bin/env bash
# Agent Hunt load test: N bot players each play one run at once. A bot launches a target
# (a real Substrate resume), waits like a player aiming, and shoots it (a real suspend).
# Few LLM calls: only the occasional line a waking agent says.
# Usage: ./loadtest.sh [players=10] [seconds=75] [base=http://localhost:8090]
set -euo pipefail
N=${1:-10}
SECS=${2:-75}
B=${3:-http://localhost:8090}
A=$B/duck-hunt/api
WORK=$(mktemp -d)
trap 'rm -rf "$WORK"' EXIT

bot() { # bot <i>: one player's run; appends "resume_ms suspend_ms" lines to $WORK/<i>.ms
  local i=$1 jar=$WORK/$1.jar run end flight body
  curl -s -c "$jar" -b "$jar" -H 'Content-Type: application/json' -d "{\"nick\":\"bot$i\"}" "$B/api/join" >/dev/null
  run=$(curl -s -c "$jar" -b "$jar" -X POST "$A/start" | jq -r .run)
  end=$((SECONDS + SECS))
  while [ $SECONDS -lt $end ]; do
    body=$(curl -s -c "$jar" -b "$jar" -H 'Content-Type: application/json' -d "{\"run\":\"$run\"}" "$A/launch")
    if [ "$(jq -r '.busy // false' <<<"$body")" = true ] || [ "$(jq -r '.flight // empty' <<<"$body")" = "" ]; then
      echo busy >>"$WORK/$i.busy"
      sleep 0.45
      continue
    fi
    flight=$(jq -r .flight <<<"$body")
    sleep "0.$((RANDOM % 6 + 4))" # 0.4-0.9 s: aiming
    jq -r --argjson w "$(jq .wokeMs <<<"$body")" '"\($w) \(.ms)"' \
      <(curl -s -c "$jar" -b "$jar" -H 'Content-Type: application/json' -d "{\"run\":\"$run\",\"flight\":\"$flight\"}" "$A/hit") >>"$WORK/$i.ms"
  done
  curl -s -c "$jar" -b "$jar" -H 'Content-Type: application/json' -d "{\"run\":\"$run\"}" "$A/finish" | jq -c '{score, hits, rank}' >"$WORK/$i.res"
}

echo "==> $N bots, ${SECS}s runs against $B"
for i in $(seq 1 "$N"); do bot "$i" & done
wait

pct() { sort -n | awk -v p="$1" '{a[NR]=$1} END {if (NR) print a[int((NR-1)*p)+1]; else print "-"}'; }
cat "$WORK"/*.ms 2>/dev/null >"$WORK/all" || true
hits=$(wc -l <"$WORK/all")
echo "hits: $hits · busy retries: $(cat "$WORK"/*.busy 2>/dev/null | wc -l)"
echo "wake (resume) ms p50 $(awk '{print $1}' "$WORK/all" | pct 0.5) p95 $(awk '{print $1}' "$WORK/all" | pct 0.95)"
echo "snapshot (suspend) ms p50 $(awk '{print $2}' "$WORK/all" | pct 0.5) p95 $(awk '{print $2}' "$WORK/all" | pct 0.95)"
echo "best scores: $(cat "$WORK"/*.res | jq -s -c 'sort_by(-.score) | .[0:3]')"
curl -s "$B/admin/duck-hunt/api/state" | jq -c '{pool, awake, asleep, runs, hits, turns, tokens}'
