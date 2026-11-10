#!/usr/bin/env bash
# Deletes Substrate actors left behind by removed games: actors whose kagent session is
# gone, so kagent's API can no longer reach them (it has no actor delete). It uses
# Substrate's own CLI, kubectl-ate, acting as kagent's controller identity through a
# short-lived ServiceAccount token that is never printed and is removed on exit.
#
#   ./delete-orphan-actors.sh               dry run: list the actors it would delete
#   ./delete-orphan-actors.sh --yes         delete them
#   AGENTS="forky escapee" ./delete-orphan-actors.sh   other agents (default: removed games)
set -euo pipefail
cd "$(dirname "$0")"

CTX=${CTX:-${KUBE_CONTEXT:-kind-kagent}}
ATESPACE=${ATESPACE:-kagent}
AGENTS=${AGENTS:-escapee escape-probe forky}
SUBSTRATE_VERSION=${SUBSTRATE_VERSION:-v0.6.0-alpha3}
YES=false
[ "${1:-}" = "--yes" ] && YES=true

# Go from mise (the newest installed), for building kubectl-ate once.
for d in "$HOME"/.local/share/mise/installs/go/*/bin; do [ -x "$d/go" ] && PATH=$d:$PATH; done
BIN=${XDG_CACHE_HOME:-$HOME/.cache}/kubecon-adventures/kubectl-ate-$SUBSTRATE_VERSION
if [ ! -x "$BIN" ]; then
  echo "==> building kubectl-ate $SUBSTRATE_VERSION"
  mkdir -p "$(dirname "$BIN")"
  src=$(go mod download -json "github.com/kagent-dev/substrate@$SUBSTRATE_VERSION" | python3 -c 'import json,sys; print(json.load(sys.stdin)["Dir"])')
  (cd "$src" && GOFLAGS=-mod=mod go build -o "$BIN" ./cmd/kubectl-ate)
fi

umask 077
TOK=$(mktemp)
trap 'rm -f "$TOK"' EXIT
kubectl --context "$CTX" create token kagent-controller -n kagent --duration=10m >"$TOK"
ate() { "$BIN" --context "$CTX" --token-file "$TOK" "$@"; }

echo "==> actors of: $AGENTS (atespace $ATESPACE)"
mapfile -t NAMES < <(ate get actor --atespace "$ATESPACE" -o json | AGENTS="$AGENTS" python3 -c '
import json, os, re, sys
d = json.load(sys.stdin)
items = d if isinstance(d, list) else next((v for v in d.values() if isinstance(v, list)), [])
want = os.environ["AGENTS"].split()
for a in items:
    tpl = a.get("actorTemplate") or a.get("actor_template") or {}
    tpl = tpl.get("name", "") if isinstance(tpl, dict) else str(tpl)
    agent = re.sub(r"-[0-9a-f]{12}$", "", tpl)  # "<agent>-<12 hex revision>"
    if agent in want:
        state = (a.get("status") or {}).get("state", "")
        print(a["metadata"]["name"], agent, state, sep="\t")
')
if [ ${#NAMES[@]} -eq 0 ]; then
  echo "none left"
  exit 0
fi
printf '%s\n' "${NAMES[@]}" | column -t -s $'\t'
if ! $YES; then
  echo "Dry run: ${#NAMES[@]} actors. Rerun with --yes to delete them."
  exit 0
fi
failed=0
for row in "${NAMES[@]}"; do
  name=${row%%$'\t'*}
  ate delete actor "$name" --atespace "$ATESPACE" --any-state || failed=$((failed + 1))
done
echo "${#NAMES[@]} actors, $failed failed"
[ "$failed" -eq 0 ]
