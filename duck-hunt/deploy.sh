#!/usr/bin/env bash
# Deploy Duck Hunt into the kind-kagent cluster (run ../kagent/setup.sh first).
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

echo "==> duck-hunt image"
IMAGE=localhost:5001/duck-hunt:$(date +%Y%m%d-%H%M%S)
docker build -q -t "$IMAGE" .
docker push -q "$IMAGE"

echo "==> duck-hunt app"
k apply -f manifests/atelet-metrics.yaml
sed "s|image: IMAGE.*|image: $IMAGE|" manifests/app.yaml | k apply -f -
k -n duck-hunt rollout status deploy/duck-hunt --timeout 3m

echo "==> host port 8090 -> arcade Gateway"
LB=$(k -n agentgateway-system get svc arcade -o jsonpath='{.status.loadBalancer.ingress[0].ip}')
if [ "$(docker inspect -f '{{.Config.Cmd}}' duck-hunt-port 2>/dev/null)" != "[TCP-LISTEN:8090,fork,reuseaddr TCP:$LB:80]" ]; then
  docker rm -f duck-hunt-port >/dev/null 2>&1 || true
  docker run -d --restart=always --name duck-hunt-port --network kind -p 0.0.0.0:8090:8090 alpine/socat:1.8.1.3 \
    TCP-LISTEN:8090,fork,reuseaddr TCP:$LB:80 >/dev/null
fi

echo
echo "Phone:      http://localhost:8090/duck-hunt"
echo "Big screen: http://localhost:8090/duck-hunt/screen"
echo "Admin:      http://localhost:8090/admin"
