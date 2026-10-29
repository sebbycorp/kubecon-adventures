#!/usr/bin/env bash
# Create a kind cluster and install kagent (with Agent Substrate) and agentgateway.
# Safe to rerun: existing cluster, registry, and Helm releases are reused/upgraded.
# The OpenAI key is read from Vault (secret/kubecon/api-keys) unless OPENAI_API_KEY is set.
set -euo pipefail
cd "$(dirname "$0")"

CLUSTER=kagent
KIND_VERSION=v0.33.0
KIND_NODE=kindest/node:v1.35.8@sha256:07b2536e30b803ed61d1677a79df6115f798ce64c80f9e22f6ed45afd09323c0
KUBECTL_VERSION=v1.37.2
HELM_VERSION=v4.3.1
KAGENT_VERSION=1.0.0-alpha17   # pins Substrate v0.6.0-alpha3
AGENTGATEWAY_VERSION=v1.7.0
GATEWAY_API_VERSION=v1.6.3
METALLB_VERSION=v0.15.3
METRICS_SERVER_VERSION=v0.9.0
SOCAT_IMAGE=alpine/socat:1.8.1.3
REG_NAME=kind-registry
REG_PORT=5001

BIN=$HOME/.local/bin
export PATH=$BIN:$PATH
CTX=kind-$CLUSTER
k() { kubectl --context "$CTX" "$@"; }

echo "==> Tools"
mkdir -p "$BIN"
kind version 2>/dev/null | grep -q "$KIND_VERSION" ||
  { curl -fsSLo "$BIN/kind" "https://kind.sigs.k8s.io/dl/$KIND_VERSION/kind-linux-amd64"; chmod +x "$BIN/kind"; }
kubectl version --client 2>/dev/null | grep -q "$KUBECTL_VERSION" ||
  { curl -fsSLo "$BIN/kubectl" "https://dl.k8s.io/release/$KUBECTL_VERSION/bin/linux/amd64/kubectl"; chmod +x "$BIN/kubectl"; }
helm version --short 2>/dev/null | grep -q "$HELM_VERSION" ||
  curl -fsSL "https://get.helm.sh/helm-$HELM_VERSION-linux-amd64.tar.gz" | tar xz -C "$BIN" --strip-components=1 linux-amd64/helm
kagent version 2>/dev/null | grep -q "\"$KAGENT_VERSION\"" ||
  { curl -fsSLo "$BIN/kagent" "https://github.com/kagent-dev/kagent/releases/download/v$KAGENT_VERSION/kagent-linux-amd64"; chmod +x "$BIN/kagent"; }

if [ -z "${OPENAI_API_KEY:-}" ]; then
  echo "==> Reading OPENAI_API_KEY from Vault"
  OPENAI_API_KEY=$(export VAULT_TOKEN=$(jq -r .root_token ../vault/.vault-keys.json)
    docker exec -e VAULT_TOKEN vault vault kv get -mount=secret -field=OPENAI_API_KEY kubecon/api-keys) ||
    { echo "Could not read the key from Vault; run ../vault/init.sh or export OPENAI_API_KEY." >&2; exit 1; }
  export OPENAI_API_KEY
fi

echo "==> Local registry ($REG_NAME on localhost:$REG_PORT)"
if [ "$(docker inspect -f '{{.State.Running}}' $REG_NAME 2>/dev/null || true)" != true ]; then
  docker run -d --restart=always -p "127.0.0.1:$REG_PORT:5000" --network bridge --name $REG_NAME registry:3
fi

echo "==> kind cluster '$CLUSTER'"
if ! kind get clusters | grep -qx $CLUSTER; then
  kind create cluster --name $CLUSTER --config kind-config.yaml --image "$KIND_NODE" --wait 120s
fi
kind export kubeconfig --name $CLUSTER
for node in $(kind get nodes --name $CLUSTER); do
  docker exec "$node" mkdir -p "/etc/containerd/certs.d/localhost:$REG_PORT"
  printf '[host."http://%s:5000"]\n  skip_verify = true\n' $REG_NAME |
    docker exec -i "$node" cp /dev/stdin "/etc/containerd/certs.d/localhost:$REG_PORT/hosts.toml"
done
[ "$(docker inspect -f '{{json .NetworkSettings.Networks.kind}}' $REG_NAME)" = null ] && docker network connect kind $REG_NAME
k apply -f - <<EOF
apiVersion: v1
kind: ConfigMap
metadata:
  name: local-registry-hosting
  namespace: kube-public
data:
  localRegistryHosting.v1: |
    host: "localhost:$REG_PORT"
    hostFromClusterNetwork: "$REG_NAME:5000"
EOF

echo "==> MetalLB $METALLB_VERSION"
k apply -f "https://raw.githubusercontent.com/metallb/metallb/$METALLB_VERSION/config/manifests/metallb-native.yaml"
k rollout status -n metallb-system deployment/controller --timeout 5m
k wait -n metallb-system pod -l app=metallb --for=condition=Ready --timeout 2m
SUBNET=$(docker network inspect kind | jq -r '[.[].IPAM.Config[].Subnet | select(contains(":") | not)][0]' | cut -d. -f1,2)
k apply -f - <<EOF
apiVersion: metallb.io/v1beta1
kind: IPAddressPool
metadata:
  name: address-pool
  namespace: metallb-system
spec:
  addresses: ["$SUBNET.255.0-$SUBNET.255.231"]
---
apiVersion: metallb.io/v1beta1
kind: L2Advertisement
metadata:
  name: advertisement
  namespace: metallb-system
spec:
  ipAddressPools: [address-pool]
EOF

echo "==> Gateway API $GATEWAY_API_VERSION"
k apply --server-side -f "https://github.com/kubernetes-sigs/gateway-api/releases/download/$GATEWAY_API_VERSION/standard-install.yaml"

echo "==> kagent $KAGENT_VERSION + Agent Substrate"
kagent install

echo "==> agentgateway $AGENTGATEWAY_VERSION"
helm --kube-context "$CTX" upgrade --install agentgateway-crds oci://cr.agentgateway.dev/charts/agentgateway-crds \
  --version $AGENTGATEWAY_VERSION -n agentgateway-system --create-namespace --wait
helm --kube-context "$CTX" upgrade --install agentgateway oci://cr.agentgateway.dev/charts/agentgateway \
  --version $AGENTGATEWAY_VERSION -n agentgateway-system --wait

echo "==> metrics-server $METRICS_SERVER_VERSION"
k apply -f "https://github.com/kubernetes-sigs/metrics-server/releases/download/$METRICS_SERVER_VERSION/components.yaml"
# kind kubelets serve self-signed certs.
k -n kube-system get deploy metrics-server -o jsonpath='{.spec.template.spec.containers[0].args}' | grep -q kubelet-insecure-tls ||
  k -n kube-system patch deploy metrics-server --type=json \
    -p '[{"op":"add","path":"/spec/template/spec/containers/0/args/-","value":"--kubelet-insecure-tls"}]'
k -n kube-system rollout status deploy/metrics-server --timeout 3m

echo "==> kagent UI on host port 8080"
k apply -f manifests/kagent-ui-nodeport.yaml
if [ "$(docker inspect -f '{{.State.Running}}' kagent-ui-port 2>/dev/null || true)" != true ]; then
  docker rm -f kagent-ui-port >/dev/null 2>&1 || true
  docker run -d --restart=always --name kagent-ui-port --network kind -p 0.0.0.0:8080:8080 $SOCAT_IMAGE \
    TCP-LISTEN:8080,fork,reuseaddr TCP:$CLUSTER-control-plane:30080
fi

echo
echo "Done. kubectl context: $CTX"
echo "kagent UI: http://localhost:8080"
