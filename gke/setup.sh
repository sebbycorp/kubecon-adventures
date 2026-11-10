#!/usr/bin/env bash
# Run the KubeCon lab on Google Cloud: a GKE Standard cluster with kagent (with Agent
# Substrate) and agentgateway, then the arcade games. Substrate Siege is left out by default.
# Safe to rerun: the cluster, Artifact Registry repo and Helm releases are reused/upgraded.
#
# Needs: gcloud (logged in: gcloud auth login), the gke-gcloud-auth-plugin, docker, jq, curl.
# kubectl, helm and kagent are installed into ~/.local/bin at the lab's pinned versions.
#
# Settings (env):
#   PROJECT         GCP project (default: gcloud's current project)
#   REGION / ZONE   us-central1 / us-central1-a (a zonal cluster: one control plane fee)
#   CLUSTER         kubecon-arcade
#   NODE_COUNT      3 (2 is enough outside a booth)
#   MACHINE_TYPE    e2-standard-8
#   IMAGE_TYPE      UBUNTU_CONTAINERD (Substrate's gVisor workers are tested on Ubuntu kernels)
#   POD_CIDR        10.60.0.0/16 (the in-cluster-only LLM routes match it by its /16 prefix)
#   SERVICES_CIDR   10.64.0.0/20
#   SKIP_GAMES      substrate-siege (set to "" to deploy every game)
#   OPENAI_API_KEY  the key, or OPENAI_SECRET=<Secret Manager secret name> to read it from there
#   TUNNEL=1        also run ../tunnel/setup.sh against this cluster (see README first)
#   GCP_SA_KEY      a service account key (JSON) to log in with instead of your own gcloud login
#   BUILDER         docker | cloudbuild (default: docker if a Docker daemon answers, else Cloud Build)
set -euo pipefail
cd "$(dirname "$0")"

if [ -n "${GCP_SA_KEY:-}" ]; then
  KEYFILE=$(mktemp); trap 'rm -f "$KEYFILE"' EXIT
  printf '%s' "$GCP_SA_KEY" >"$KEYFILE"
  unset CLOUDSDK_AUTH_ACCESS_TOKEN   # would override the key
  gcloud auth activate-service-account --key-file "$KEYFILE" --quiet
  PROJECT=${PROJECT:-$(jq -r .project_id "$KEYFILE")}
fi

PROJECT=${PROJECT:-$(gcloud config get-value project 2>/dev/null)}
REGION=${REGION:-us-central1}
ZONE=${ZONE:-us-central1-a}
CLUSTER=${CLUSTER:-kubecon-arcade}
NODE_COUNT=${NODE_COUNT:-3}
MACHINE_TYPE=${MACHINE_TYPE:-e2-standard-8}
IMAGE_TYPE=${IMAGE_TYPE:-UBUNTU_CONTAINERD}
POD_CIDR=${POD_CIDR:-10.60.0.0/16}
SERVICES_CIDR=${SERVICES_CIDR:-10.64.0.0/20}
AR_REPO=${AR_REPO:-arcade}
export SKIP_GAMES=${SKIP_GAMES-substrate-siege}

# Pinned like ../kagent/setup.sh.
KUBECTL_VERSION=v1.37.2
HELM_VERSION=v4.3.1
KAGENT_VERSION=1.0.0-alpha17   # pins Substrate v0.6.0-alpha3
AGENTGATEWAY_VERSION=v1.7.0
GATEWAY_API_VERSION=v1.6.3

[ -n "$PROJECT" ] || { echo "Set PROJECT or run: gcloud config set project <id>" >&2; exit 1; }
if [ -z "${BUILDER:-}" ]; then docker info >/dev/null 2>&1 && BUILDER=docker || BUILDER=cloudbuild; fi
export BUILDER BUILD_PROJECT=$PROJECT BUILD_REGION=$REGION
for t in gcloud jq curl; do command -v $t >/dev/null || { echo "Missing $t" >&2; exit 1; }; done
gcloud auth print-access-token >/dev/null 2>&1 || { echo "gcloud is not logged in: gcloud auth login" >&2; exit 1; }
CTX=gke_${PROJECT}_${ZONE}_${CLUSTER}
k() { kubectl --context "$CTX" "$@"; }

echo "==> Tools"
BIN=$HOME/.local/bin
export PATH=$BIN:$PATH
mkdir -p "$BIN"
OS=$(uname -s | tr '[:upper:]' '[:lower:]')
ARCH=$(uname -m | sed 's/x86_64/amd64/;s/aarch64/arm64/')
kubectl version --client 2>/dev/null | grep -q "$KUBECTL_VERSION" ||
  { curl -fsSLo "$BIN/kubectl" "https://dl.k8s.io/release/$KUBECTL_VERSION/bin/$OS/$ARCH/kubectl"; chmod +x "$BIN/kubectl"; }
helm version --short 2>/dev/null | grep -q "$HELM_VERSION" ||
  curl -fsSL "https://get.helm.sh/helm-$HELM_VERSION-$OS-$ARCH.tar.gz" | tar xz -C "$BIN" --strip-components=1 "$OS-$ARCH/helm"
kagent version 2>/dev/null | grep -q "\"$KAGENT_VERSION\"" ||
  { curl -fsSLo "$BIN/kagent" "https://github.com/kagent-dev/kagent/releases/download/v$KAGENT_VERSION/kagent-$OS-$ARCH"; chmod +x "$BIN/kagent"; }
command -v gke-gcloud-auth-plugin >/dev/null ||
  { echo "Missing gke-gcloud-auth-plugin: gcloud components install gke-gcloud-auth-plugin (or your package manager's google-cloud-cli-gke-gcloud-auth-plugin)" >&2; exit 1; }

if [ -z "${OPENAI_API_KEY:-}" ]; then
  [ -n "${OPENAI_SECRET:-}" ] || { echo "Export OPENAI_API_KEY, or OPENAI_SECRET=<Secret Manager secret>" >&2; exit 1; }
  echo "==> Reading OPENAI_API_KEY from Secret Manager ($OPENAI_SECRET)"
  OPENAI_API_KEY=$(gcloud secrets versions access latest --secret "$OPENAI_SECRET" --project "$PROJECT")
fi
export OPENAI_API_KEY

echo "==> Project $PROJECT: APIs"
gcloud services enable container.googleapis.com artifactregistry.googleapis.com cloudbuild.googleapis.com --project "$PROJECT"

echo "==> Artifact Registry $REGION-docker.pkg.dev/$PROJECT/$AR_REPO"
gcloud artifacts repositories describe "$AR_REPO" --location "$REGION" --project "$PROJECT" >/dev/null 2>&1 ||
  gcloud artifacts repositories create "$AR_REPO" --repository-format docker --location "$REGION" --project "$PROJECT" \
    --description "KubeCon arcade images"
[ "$BUILDER" = docker ] && gcloud auth configure-docker "$REGION-docker.pkg.dev" --quiet >/dev/null
# The nodes run as the default compute service account: let it pull from this repo. Cloud Build
# runs as the same account (or the legacy Cloud Build one in older projects): let those push.
PROJECT_NUMBER=$(gcloud projects describe "$PROJECT" --format 'value(projectNumber)')
NODE_SA=$PROJECT_NUMBER-compute@developer.gserviceaccount.com
gcloud artifacts repositories add-iam-policy-binding "$AR_REPO" --location "$REGION" --project "$PROJECT" \
  --member "serviceAccount:$NODE_SA" --role roles/artifactregistry.writer >/dev/null
gcloud artifacts repositories add-iam-policy-binding "$AR_REPO" --location "$REGION" --project "$PROJECT" \
  --member "serviceAccount:$PROJECT_NUMBER@cloudbuild.gserviceaccount.com" --role roles/artifactregistry.writer >/dev/null 2>&1 || true

echo "==> GKE Standard cluster $CLUSTER ($NODE_COUNT x $MACHINE_TYPE, $ZONE)"
# Standard, not Autopilot: Substrate's gVisor workers need node access Autopilot doesn't allow.
# On-demand nodes, not Spot: a preempted node takes its games' actors with it mid-event.
if ! gcloud container clusters describe "$CLUSTER" --zone "$ZONE" --project "$PROJECT" >/dev/null 2>&1; then
  gcloud container clusters create "$CLUSTER" --zone "$ZONE" --project "$PROJECT" \
    --release-channel regular --machine-type "$MACHINE_TYPE" --num-nodes "$NODE_COUNT" \
    --image-type "$IMAGE_TYPE" --disk-size 100 --enable-ip-alias \
    --cluster-ipv4-cidr "$POD_CIDR" --services-ipv4-cidr "$SERVICES_CIDR"
fi
gcloud container clusters get-credentials "$CLUSTER" --zone "$ZONE" --project "$PROJECT"
kubectl config use-context "$CTX" >/dev/null   # kagent install uses the current context

# The in-cluster-only LLM routes match the pod network by prefix, so it must be a /16.
CIDR=$(gcloud container clusters describe "$CLUSTER" --zone "$ZONE" --project "$PROJECT" --format 'value(clusterIpv4Cidr)')
[[ "$CIDR" == */16 ]] || { echo "Pod range is $CIDR; the arcade's routes need a /16 (recreate with POD_CIDR=x.y.0.0/16)" >&2; exit 1; }
POD_CIDR_PREFIX=$(cut -d. -f1,2 <<<"$CIDR").

echo "==> Gateway API $GATEWAY_API_VERSION"
k apply --server-side -f "https://github.com/kubernetes-sigs/gateway-api/releases/download/$GATEWAY_API_VERSION/standard-install.yaml"

echo "==> kagent $KAGENT_VERSION + Agent Substrate"
kagent install

echo "==> agentgateway $AGENTGATEWAY_VERSION"
helm --kube-context "$CTX" upgrade --install agentgateway-crds oci://cr.agentgateway.dev/charts/agentgateway-crds \
  --version $AGENTGATEWAY_VERSION -n agentgateway-system --create-namespace --wait
helm --kube-context "$CTX" upgrade --install agentgateway oci://cr.agentgateway.dev/charts/agentgateway \
  --version $AGENTGATEWAY_VERSION -n agentgateway-system --wait

echo "==> arcade (skipping: ${SKIP_GAMES:-nothing})"
PLATFORM=gke KUBE_CONTEXT=$CTX IMAGE_REPO=$REGION-docker.pkg.dev/$PROJECT/$AR_REPO/arcade \
  POD_CIDR_PREFIX=$POD_CIDR_PREFIX ../arcade/deploy.sh

echo "==> arcade Gateway stays private"
TYPE=$(k -n agentgateway-system get svc arcade -o jsonpath='{.spec.type}')
[ "$TYPE" = ClusterIP ] || { echo "WARNING: svc/arcade is $TYPE, not ClusterIP; it may have a public IP. Check gke/manifests/gateway-params.yaml." >&2; exit 1; }
echo "    svc/arcade is ClusterIP"

if [ "${TUNNEL:-0}" = 1 ]; then
  echo "==> Cloudflare tunnel"
  KUBE_CONTEXT=$CTX ../tunnel/setup.sh
fi

echo
echo "Done. kubectl context: $CTX"
echo "Try it:  kubectl --context $CTX -n agentgateway-system port-forward svc/arcade 8090:80   # http://localhost:8090/"
echo "kagent UI: kubectl --context $CTX -n kagent port-forward \$(kubectl --context $CTX -n kagent get pod -l app.kubernetes.io/component=ui -o name | head -1) 8080:8080"
[ "${TUNNEL:-0}" = 1 ] || echo "Public URL: see gke/README.md (Cloudflare tunnel) before running TUNNEL=1."
echo "Tear down: ./teardown.sh"
