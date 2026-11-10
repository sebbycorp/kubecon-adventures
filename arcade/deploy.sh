#!/usr/bin/env bash
# Deploy the agent arcade (all games) into the kind-kagent cluster (run ../kagent/setup.sh first),
# or into GKE with PLATFORM=gke (../gke/setup.sh sets everything below).
# Safe to rerun. DUCK_WORKERS sets the gVisor worker count (default 12). The OpenAI key is read from Vault (secret/kubecon/api-keys) unless OPENAI_API_KEY is set.
#
# Settings (defaults are the local kind lab):
#   PLATFORM         kind | gke
#   KUBE_CONTEXT     kubectl context (kind-kagent)
#   IMAGE_REPO       where the arcade image is pushed (localhost:5001/arcade; on GKE an Artifact Registry repo)
#   POD_CIDR_PREFIX  the pod network prefix the in-cluster-only LLM routes allow ("10.244." on kind)
#   SKIP_GAMES       comma-separated games to leave out; only "substrate-siege" is supported
#   HEIST_NODES      Substrate Siege's district nodes (default: every worker node)
#   BUILDER          docker (default) | cloudbuild (gcloud builds submit; no local Docker needed,
#                    BUILD_PROJECT names the GCP project)
set -euo pipefail
cd "$(dirname "$0")"
export PATH=$HOME/.local/bin:$PATH
PLATFORM=${PLATFORM:-kind}
KUBE_CONTEXT=${KUBE_CONTEXT:-kind-kagent}
IMAGE_REPO=${IMAGE_REPO:-localhost:5001/arcade}
POD_CIDR_PREFIX=${POD_CIDR_PREFIX:-10.244.}
SKIP_GAMES=${SKIP_GAMES:-}
k() { kubectl --context "$KUBE_CONTEXT" "$@"; }
skip() { [[ ",$SKIP_GAMES," == *",$1,"* ]]; }
# The in-cluster-only routes are written for kind's pod network; swap in this cluster's.
pods() { sed "s|source.address.startsWith(\"10.244.\")|source.address.startsWith(\"$POD_CIDR_PREFIX\")|" "$@"; }
for g in ${SKIP_GAMES//,/ }; do
  [ "$g" = substrate-siege ] || { echo "SKIP_GAMES: only substrate-siege can be skipped (got $g)" >&2; exit 1; }
done
[ "$PLATFORM" = kind ] || [ "$PLATFORM" = gke ] || { echo "PLATFORM must be kind or gke" >&2; exit 1; }

if [ -z "${OPENAI_API_KEY:-}" ]; then
  OPENAI_API_KEY=$(export VAULT_TOKEN=$(jq -r .root_token ../vault/.vault-keys.json)
    docker exec -e VAULT_TOKEN vault vault kv get -mount=secret -field=OPENAI_API_KEY kubecon/api-keys) ||
    { echo "Could not read the key from Vault; run ../vault/init.sh or export OPENAI_API_KEY." >&2; exit 1; }
fi

echo "==> agentgateway: arcade Gateway and OpenAI route"
k -n agentgateway-system create secret generic openai --from-literal=OPENAI_API_KEY="$OPENAI_API_KEY" \
  --dry-run=client -o yaml | k apply -f -
if [ "$PLATFORM" = gke ]; then
  # On GKE a LoadBalancer Service gets a public IP, which would bypass the tunnel's /admin
  # block and expose the LLM routes. Keep the Gateway's Service in-cluster (ClusterIP);
  # players come in through the Cloudflare tunnel.
  k apply -f ../gke/manifests/gateway-params.yaml
  awk '{print} /^  gatewayClassName: agentgateway$/ {
    print "  infrastructure:\n    parametersRef:\n      group: agentgateway.dev\n      kind: AgentgatewayParameters\n      name: arcade-internal"}' \
    manifests/gateway.yaml | k apply -f -
else
  k apply -f manifests/gateway.yaml
fi
k apply -f manifests/llm.yaml
k -n agentgateway-system wait gateway/arcade --for=condition=Programmed --timeout 3m

echo "==> duck WorkerPool (${DUCK_WORKERS:=12} gVisor workers)"
sed "s|replicas: REPLICAS|replicas: $DUCK_WORKERS|" manifests/workerpool.yaml | k apply -f -

echo "==> duck Agent"
k apply -f manifests/duck-agent.yaml
k -n kagent wait agent/duck --for=condition=Ready --timeout 5m

echo "==> Agent Flow: flow WorkerPool and flowbot Agent"
k apply -f manifests/flow-egress.yaml -f manifests/flow-agent.yaml
pods manifests/flow-policies.yaml | k apply -f -
k -n kagent wait agent/flowbot --for=condition=Ready --timeout 5m

if skip substrate-siege; then
  echo "==> Substrate Siege: skipped (SKIP_GAMES)"
else
  echo "==> Substrate Siege: heist and siegeplayers WorkerPools, heister, sidekick and siegeplayer Agents"
  k apply -f manifests/heist-agent.yaml -f manifests/heist-policies.yaml
  k -n kagent wait agent/heister agent/sidekick agent/siegeplayer --for=condition=Ready --timeout 5m
fi

echo "==> Cluster Defense: defense WorkerPool and subagent Agent"
k apply -f manifests/defense-agent.yaml -f manifests/defense-policies.yaml
k -n kagent wait agent/subagent --for=condition=Ready --timeout 5m

echo "==> Gateway Defense: gatedef WorkerPool, rogue Agent, guard and limit routes"
pods manifests/gatedef.yaml | k apply -f -
k -n kagent wait agent/rogue --for=condition=Ready --timeout 5m

echo "==> Flappy Substrate: flappy WorkerPool and octo Agent"
k apply -f manifests/flappy-agent.yaml
k -n kagent wait agent/octo --for=condition=Ready --timeout 5m

echo "==> Pod Packer: podpacker WorkerPool and packer Agent"
k apply -f manifests/podpacker-agent.yaml
k -n kagent wait agent/packer --for=condition=Ready --timeout 5m

echo "==> Substrate Selfie: selfie WorkerPool, selfiebot Agent, /selfie/v1 model route and guard"
pods manifests/selfie.yaml | k apply -f -
k -n kagent wait agent/selfiebot --for=condition=Ready --timeout 5m

echo "==> arcade image"
IMAGE=$IMAGE_REPO:$(date +%Y%m%d-%H%M%S)
if [ "${BUILDER:-docker}" = cloudbuild ]; then
  gcloud builds submit --project "$BUILD_PROJECT" --region "${BUILD_REGION:-us-central1}" --tag "$IMAGE" --quiet .
else
  docker build -q --platform linux/amd64 -t "$IMAGE" .
  docker push -q "$IMAGE"
fi

echo "==> arcade app"
k delete namespace duck-hunt --ignore-not-found --wait=false   # pre-arcade name
k apply -f manifests/atelet-metrics.yaml -f manifests/gateway-metrics.yaml
# Cluster facts for Agent Flow's hints.
NODES=$(k get nodes --no-headers | wc -l)
if [ "$PLATFORM" = kind ]; then
  # The kind nodes share this host, so its CPU and RAM are the real capacity (each node reports all of it).
  CPUS=$(nproc)
  MEMGIB=$(free -g | awk '/Mem:/{print $2}')
else
  # Real nodes: add up what they report.
  CPUS=$(k get nodes -o jsonpath='{range .items[*]}{.status.capacity.cpu}{"\n"}{end}' | awk '{s+=$1} END {print s}')
  MEMGIB=$(k get nodes -o jsonpath='{range .items[*]}{.status.capacity.memory}{"\n"}{end}' |
    awk '{v=$1; sub(/Ki$/,"",v); s+=v} END {printf "%d", s/1024/1024}')
fi
HEIST_NODES=${HEIST_NODES:-$(k get nodes -l '!node-role.kubernetes.io/control-plane' -o jsonpath='{.items[*].metadata.name}' | tr ' ' ',')}
sed -e "s|image: IMAGE.*|image: $IMAGE|" -e "s|\"NODES\"|\"$NODES\"|" -e "s|\"CPUS\"|\"$CPUS\"|" -e "s|\"MEMGIB\"|\"$MEMGIB\"|" \
  -e "s|\"HEIST_NODES\"|\"$HEIST_NODES\"|" -e "s|\"DISABLED_GAMES\"|\"$SKIP_GAMES\"|" \
  manifests/app.yaml | k apply -f -
k -n arcade rollout status deploy/arcade --timeout 3m

if [ "$PLATFORM" = gke ]; then
  echo
  echo "Arcade deployed. It has no public address until the tunnel is up (../tunnel/setup.sh)."
  echo "Locally: kubectl --context $KUBE_CONTEXT -n agentgateway-system port-forward svc/arcade 8090:80"
  echo "         then http://localhost:8090/ (admin: /admin)"
  exit 0
fi

echo "==> host port 8090 -> arcade Gateway"
LB=$(k -n agentgateway-system get svc arcade -o jsonpath='{.status.loadBalancer.ingress[0].ip}')
docker rm -f duck-hunt-port >/dev/null 2>&1 || true   # pre-arcade name
if [ "$(docker inspect -f '{{.Config.Cmd}}' arcade-port 2>/dev/null)" != "[TCP-LISTEN:8090,fork,reuseaddr TCP:$LB:80]" ]; then
  docker rm -f arcade-port >/dev/null 2>&1 || true
  docker run -d --restart=always --name arcade-port --network kind -p 0.0.0.0:8090:8090 alpine/socat:1.8.1.3 \
    TCP-LISTEN:8090,fork,reuseaddr TCP:$LB:80 >/dev/null
fi

echo
echo "Arcade:     http://localhost:8090/"
echo "Duck Hunt:  http://localhost:8090/duck-hunt (big screen: /duck-hunt/screen)"
echo "Flappy:     http://localhost:8090/flappy (big screen: /flappy/screen)"
echo "Pod Packer: http://localhost:8090/pod-packer (big screen: /pod-packer/screen)"
echo "Selfie:     http://localhost:8090/selfie (big screen: /selfie/screen)"
echo "Flow:       http://localhost:8090/agent-flow (big screen: /agent-flow/screen)"
skip substrate-siege ||
echo "Siege:      http://localhost:8090/substrate-siege (big screen: /substrate-siege/screen)"
echo "Defense:    http://localhost:8090/cluster-defense (big screen: /cluster-defense/screen)"
echo "Gateway:    http://localhost:8090/gateway-defense (big screen: /gateway-defense/screen)"
echo "Admin:      http://localhost:8090/admin"
