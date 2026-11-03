#!/usr/bin/env bash
# Deploy the agent arcade (all games) into the kind-kagent cluster (run ../kagent/setup.sh first).
# Safe to rerun. DUCK_WORKERS sets the gVisor worker count (default 12). The OpenAI key is read from Vault (secret/kubecon/api-keys) unless OPENAI_API_KEY is set.
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

echo "==> duck WorkerPool (${DUCK_WORKERS:=12} gVisor workers)"
sed "s|replicas: REPLICAS|replicas: $DUCK_WORKERS|" manifests/workerpool.yaml | k apply -f -

echo "==> duck Agent"
k apply -f manifests/duck-agent.yaml
k -n kagent wait agent/duck --for=condition=Ready --timeout 5m

echo "==> Agent Flow: flow WorkerPool and flowbot Agent"
k apply -f manifests/flow-egress.yaml -f manifests/flow-agent.yaml -f manifests/flow-policies.yaml
k -n kagent wait agent/flowbot --for=condition=Ready --timeout 5m

echo "==> Substrate Siege: heist WorkerPool, heister and sidekick Agents"
k apply -f manifests/heist-agent.yaml -f manifests/heist-policies.yaml
k -n kagent wait agent/heister agent/sidekick --for=condition=Ready --timeout 5m

echo "==> Cluster Defense: defense WorkerPool and subagent Agent"
k apply -f manifests/defense-agent.yaml -f manifests/defense-policies.yaml
k -n kagent wait agent/subagent --for=condition=Ready --timeout 5m

echo "==> arcade image"
IMAGE=localhost:5001/arcade:$(date +%Y%m%d-%H%M%S)
docker build -q -t "$IMAGE" .
docker push -q "$IMAGE"

echo "==> arcade app"
k delete namespace duck-hunt --ignore-not-found --wait=false   # pre-arcade name
k apply -f manifests/atelet-metrics.yaml
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
echo "Duck Hunt:  http://localhost:8090/duck-hunt (big screen: /duck-hunt/screen)"
echo "Flow:       http://localhost:8090/agent-flow (big screen: /agent-flow/screen)"
echo "Siege:      http://localhost:8090/substrate-siege (big screen: /substrate-siege/screen)"
echo "Defense:    http://localhost:8090/cluster-defense (big screen: /cluster-defense/screen)"
echo "Admin:      http://localhost:8090/admin"
