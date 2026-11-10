#!/usr/bin/env bash
# Deploy the agent arcade (all games) into the kind-kagent cluster (run ../kagent/setup.sh first).
# Safe to rerun. The OpenAI key is read from Vault (secret/kubecon/api-keys) unless OPENAI_API_KEY is set.
set -euo pipefail
cd "$(dirname "$0")"
export PATH=$HOME/.local/bin:$PATH
k() { kubectl --context kind-kagent "$@"; }

if [ -z "${OPENAI_API_KEY:-}" ]; then
  OPENAI_API_KEY=$(export VAULT_TOKEN=$(jq -r .root_token ../vault/.vault-keys.json)
    docker exec -e VAULT_TOKEN vault vault kv get -mount=secret -field=OPENAI_API_KEY kubecon/api-keys) ||
    { echo "Could not read the key from Vault; run ../vault/init.sh or export OPENAI_API_KEY." >&2; exit 1; }
fi

echo "==> agentgateway: arcade Gateway and OpenAI route"
k -n agentgateway-system create secret generic openai --from-literal=OPENAI_API_KEY="$OPENAI_API_KEY" \
  --dry-run=client -o yaml | k apply -f -
k apply -f manifests/gateway.yaml -f manifests/llm.yaml
k -n agentgateway-system wait gateway/arcade --for=condition=Programmed --timeout 3m

echo "==> shared model (duck-model ModelConfig)"
k apply -f manifests/model.yaml

echo "==> Agent Flow: flow WorkerPool and flowbot Agent"
k apply -f manifests/flow-egress.yaml -f manifests/flow-agent.yaml -f manifests/flow-policies.yaml
k -n kagent wait agent/flowbot --for=condition=Ready --timeout 5m

echo "==> Cluster Defense: defense WorkerPool and subagent Agent"
k apply -f manifests/defense-agent.yaml -f manifests/defense-policies.yaml
k -n kagent wait agent/subagent --for=condition=Ready --timeout 5m

echo "==> Gateway Defense: gatedef WorkerPool, rogue Agent, guard and limit routes"
k apply -f manifests/gatedef.yaml
k -n kagent wait agent/rogue --for=condition=Ready --timeout 5m

echo "==> Flappy Substrate: flappy WorkerPool and octo Agent"
k apply -f manifests/flappy-agent.yaml
k -n kagent wait agent/octo --for=condition=Ready --timeout 5m

echo "==> Pod Packer: podpacker WorkerPool and packer Agent"
k apply -f manifests/podpacker-agent.yaml
k -n kagent wait agent/packer --for=condition=Ready --timeout 5m

echo "==> Substrate Selfie: selfie WorkerPool, selfiebot Agent, /selfie/v1 model route and guard"
k apply -f manifests/selfie.yaml
k -n kagent wait agent/selfiebot --for=condition=Ready --timeout 5m

echo "==> arcade image"
IMAGE=localhost:5001/arcade:$(date +%Y%m%d-%H%M%S)
docker build -q -t "$IMAGE" .
docker push -q "$IMAGE"

echo "==> arcade app"
k delete namespace duck-hunt --ignore-not-found --wait=false   # pre-arcade name
k apply -f manifests/atelet-metrics.yaml -f manifests/gateway-metrics.yaml
# Cluster facts for Agent Flow's hints. The kind nodes share this host, so its CPU and RAM
# are the real capacity (each node reports all of it).
NODES=$(k get nodes --no-headers | wc -l)
CPUS=$(nproc)
MEMGIB=$(free -g | awk '/Mem:/{print $2}')
sed -e "s|image: IMAGE.*|image: $IMAGE|" -e "s|\"NODES\"|\"$NODES\"|" -e "s|\"CPUS\"|\"$CPUS\"|" -e "s|\"MEMGIB\"|\"$MEMGIB\"|" \
  manifests/app.yaml | k apply -f -
k -n arcade rollout status deploy/arcade --timeout 3m

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
echo "Flappy:     http://localhost:8090/flappy (big screen: /flappy/screen)"
echo "Pod Packer: http://localhost:8090/pod-packer (big screen: /pod-packer/screen)"
echo "Selfie:     http://localhost:8090/selfie (big screen: /selfie/screen)"
echo "Flow:       http://localhost:8090/agent-flow (big screen: /agent-flow/screen)"
echo "Defense:    http://localhost:8090/cluster-defense (big screen: /cluster-defense/screen)"
echo "Gateway:    http://localhost:8090/gateway-defense (big screen: /gateway-defense/screen)"
