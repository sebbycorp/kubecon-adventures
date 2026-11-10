#!/usr/bin/env bash
# Deletes the GKE cluster made by setup.sh (and, with ALL=1, its Artifact Registry repo).
# Stops the billing for nodes. Asks first. Same PROJECT/ZONE/CLUSTER/REGION/AR_REPO settings as setup.sh.
set -euo pipefail
PROJECT=${PROJECT:-$(gcloud config get-value project 2>/dev/null)}
REGION=${REGION:-us-central1}
ZONE=${ZONE:-us-central1-a}
CLUSTER=${CLUSTER:-kubecon-arcade}
AR_REPO=${AR_REPO:-arcade}
[ -n "$PROJECT" ] || { echo "Set PROJECT" >&2; exit 1; }
read -r -p "Delete GKE cluster $CLUSTER in $PROJECT/$ZONE? [y/N] " ok
[ "$ok" = y ] || exit 1
gcloud container clusters delete "$CLUSTER" --zone "$ZONE" --project "$PROJECT" --quiet
if [ "${ALL:-0}" = 1 ]; then
  gcloud artifacts repositories delete "$AR_REPO" --location "$REGION" --project "$PROJECT" --quiet
fi
echo "Deleted. If the tunnel pointed here, kubecon.agentpwned.ai now has no connector until another cluster runs cloudflared."
