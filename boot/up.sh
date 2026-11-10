#!/usr/bin/env bash
# Brings the whole KubeCon lab back after the host boots: the kind cluster (kagent, Agent
# Substrate, agentgateway), every arcade game, the Cloudflare tunnel, Vault and Homepage.
# Safe to run at any time: it only starts what is stopped and waits for what is starting.
# Exits non-zero (so systemd retries) if the arcade isn't serving by the end.
# Secrets are never printed: Vault is unsealed from vault/.vault-keys.json without output.
set -uo pipefail
cd "$(dirname "$0")/.."
REPO=$PWD
export PATH=$HOME/.local/bin:$HOME/.local/share/mise/shims:/usr/local/bin:/usr/bin:$PATH
CTX=kind-kagent
PUBLIC=${PUBLIC_URL:-https://kubecon.agentpwned.ai}

log() { echo "[$(date +%H:%M:%S)] $*"; }
# Docker access: the user is in the docker group, but a session started before that may
# need sg to pick it up.
if docker info >/dev/null 2>&1; then d() { docker "$@"; }; else d() { sg docker -c "docker $(printf '%q ' "$@")"; }; fi
k() { kubectl --context "$CTX" "$@"; }
# wait_for <seconds> <what> <command...>: retries the command every 5 s.
wait_for() {
  local secs=$1 what=$2
  shift 2
  for ((t = 0; t < secs; t += 5)); do
    "$@" >/dev/null 2>&1 && { log "ok: $what"; return 0; }
    sleep 5
  done
  log "TIMEOUT: $what"
  return 1
}

log "==> Docker"
wait_for 300 "docker daemon" d info || exit 1

log "==> containers"
# Order matters a little: the registry and the kind nodes first, then what fronts them.
for c in kind-registry kagent-control-plane kagent-worker kagent-worker2 vault homepage arcade-port kagent-ui-port; do
  state=$(d inspect -f '{{.State.Running}}' "$c" 2>/dev/null) || { log "missing: $c (skipped)"; continue; }
  if [ "$state" != "true" ]; then
    log "starting $c"
    d start "$c" >/dev/null || log "could not start $c"
  fi
done

log "==> Kubernetes"
wait_for 600 "API server ready" k get --raw /readyz || exit 1
wait_for 600 "all nodes Ready" k wait --for=condition=Ready nodes --all --timeout=5s || exit 1

log "==> deployments"
fail=0
for ns in kube-system metallb-system local-path-storage podcertificate-controller-system ate-system kagent agentgateway-system arcade cloudflared; do
  k get ns "$ns" >/dev/null 2>&1 || continue
  for dep in $(k -n "$ns" get deploy -o name 2>/dev/null); do
    k -n "$ns" rollout status "$dep" --timeout=900s >/dev/null 2>&1 && log "ok: $ns/${dep#*/}" || { log "NOT READY: $ns/${dep#*/}"; fail=1; }
  done
done

log "==> Vault"
if [ "$(d inspect -f '{{.State.Running}}' vault 2>/dev/null)" = "true" ]; then
  wait_for 120 "vault answering" bash -c "docker exec vault vault status >/dev/null 2>&1; [ \$? -ne 1 ]"
  if [ "$(d exec vault vault status -format=json 2>/dev/null | jq -r .sealed)" = "true" ]; then
    if [ -f vault/.vault-keys.json ]; then
      d exec vault vault operator unseal "$(jq -r '.unseal_keys_b64[0]' vault/.vault-keys.json)" >/dev/null 2>&1 && log "ok: vault unsealed" || log "could not unseal vault"
    else
      log "vault is sealed and vault/.vault-keys.json is missing"
    fi
  else
    log "ok: vault unsealed"
  fi
fi

log "==> Homepage"
# start.sh refreshes Homepage's kubeconfig from the cluster (its address can change).
if [ -x homepage/start.sh ]; then homepage/start.sh >/dev/null 2>&1 && log "ok: homepage" || log "homepage start failed"; fi

log "==> arcade"
wait_for 600 "arcade on http://localhost:8090" curl -sf -o /dev/null http://localhost:8090/api/stats || fail=1
for p in / /agent-flow/screen /cluster-defense /gateway-defense /flappy; do
  code=$(curl -s -o /dev/null -w '%{http_code}' "http://localhost:8090$p")
  [ "$code" = 200 ] && log "ok: $p" || { log "HTTP $code: $p"; fail=1; }
done
# The arcade can start before kagent's controller is ready after a reboot; its games then
# fail to set up their agent pools and it sees no Substrate workers. If it still sees none
# a minute on, while the worker pools are up, restart it once.
if ! wait_for 60 "arcade sees Substrate workers" bash -c '[ "$(curl -s http://localhost:8090/api/stats | jq -r ".workers // 0")" -gt 0 ]'; then
  log "restarting the arcade (it started before kagent was ready)"
  k -n arcade rollout restart deploy/arcade >/dev/null && k -n arcade rollout status deploy/arcade --timeout=300s >/dev/null
  wait_for 180 "arcade sees Substrate workers after restart" bash -c '[ "$(curl -s http://localhost:8090/api/stats | jq -r ".workers // 0")" -gt 0 ]' || fail=1
fi
# The games' agent pools. Right after a boot the first actor restores are slow; pool
# sessions can time out while their actors keep running with no kagent session, holding
# the small pools' workers so nothing new starts. If the pools are still empty after 4
# minutes, restart those worker pools (freeing the orphans) and the arcade, once.
pools_ok='s=$(curl -s http://localhost:8090/api/stats); [ "$(echo "$s" | jq -r ".flappy_pool // 0")" -gt 0 ] && [ "$(echo "$s" | jq -r ".podPacker_pool // 0")" -gt 0 ] && [ "$(echo "$s" | jq -r ".selfie_pool // 0")" -gt 0 ]'
if ! wait_for 240 "game agent pools filled" bash -c "$pools_ok"; then
  log "restarting the small worker pools and the arcade (pool sessions timed out after boot)"
  k -n kagent rollout restart deploy/flappy deploy/podpacker deploy/selfie deploy/gatedef >/dev/null 2>&1
  for d in flappy podpacker selfie gatedef; do k -n kagent rollout status deploy/$d --timeout=300s >/dev/null 2>&1; done
  k -n arcade rollout restart deploy/arcade >/dev/null && k -n arcade rollout status deploy/arcade --timeout=300s >/dev/null
  wait_for 240 "game agent pools filled after restart" bash -c "$pools_ok" || fail=1
fi
stats=$(curl -s http://localhost:8090/api/stats | jq -c '{actors, running, workers}' 2>/dev/null)
log "substrate: $stats"

log "==> public"
wait_for 300 "public $PUBLIC through the Cloudflare tunnel" curl -sf -o /dev/null "$PUBLIC/" || fail=1

if [ "$fail" -ne 0 ]; then
  log "Some services are not up yet (see above)."
  exit 1
fi
log "Everything is up: $PUBLIC"
