#!/usr/bin/env bash
# Removes what Sandbox Escape and Fork Frenzy left in the cluster, after the arcade that
# no longer has them is deployed:
#   1. every kagent session (Substrate actor and snapshots) and checkpoint of the escapee,
#      escape-probe and forky Agents, through the kagent API (cmd/agentclean)
#   2. the Agents escapee and forky, the Harnesses escape and forks, and the WorkerPools
#      escape and forks (namespace kagent)
# Both games used the shared duck-model ModelConfig and the shared openai route, so no
# ModelConfig, EgressPolicy, HTTPRoute or AgentgatewayPolicy is theirs alone; nothing of
# duck, flow, heist, defense or kagent-default is touched.
#
#   ./scripts/remove-escape-forks.sh        dry run: prints what it would delete
#   ./scripts/remove-escape-forks.sh --yes  deletes
set -euo pipefail
cd "$(dirname "$0")/.."
export PATH=$HOME/.local/bin:$PATH
k() { kubectl --context kind-kagent "$@"; }

YES=""
case "${1:-}" in
  --yes) YES="-yes" ;;
  "") ;;
  *) echo "usage: $0 [--yes]" >&2; exit 2 ;;
esac
GO=${GO:-go}
"$GO" version >/dev/null 2>&1 || GO=$(ls -d "$HOME"/.local/share/mise/installs/go/[0-9]*/bin/go 2>/dev/null | sort -V | tail -1)

AGENTS=(escapee escape-probe forky)
HARNESSES=(escape forks)
POOLS=(escape forks)

echo "==> kagent sessions and checkpoints of ${AGENTS[*]}"
k -n kagent port-forward svc/kagent-controller 18083:8083 >/dev/null 2>&1 &
PF=$!
trap 'kill $PF 2>/dev/null' EXIT
for _ in $(seq 50); do (exec 3<>/dev/tcp/127.0.0.1/18083) 2>/dev/null && break; sleep 0.2; done
"$GO" run ./cmd/agentclean -kagent-url http://localhost:18083 $YES "${AGENTS[@]}"

echo "==> Kubernetes resources"
del() { # del <resource> <name>...
  local res=$1; shift
  for n in "$@"; do
    if k -n kagent get "$res" "$n" >/dev/null 2>&1; then
      if [ -n "$YES" ]; then k -n kagent delete "$res" "$n" --wait=false; else echo "would delete $res/$n (namespace kagent)"; fi
    fi
  done
}
# Agents first, so nothing new is scheduled on the pools while they go.
del agents.api.kagent.dev escapee escape-probe forky
del harnesses.api.kagent.dev "${HARNESSES[@]}"
del workerpools.ate.dev "${POOLS[@]}"

[ -n "$YES" ] || echo "Dry run. Rerun with --yes to delete."
