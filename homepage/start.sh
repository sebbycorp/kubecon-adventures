#!/usr/bin/env bash
# Start Homepage. Refreshes its kubeconfig from the kind cluster first.
set -euo pipefail
cd "$(dirname "$0")"
export PATH=$HOME/.local/bin:$PATH
export DOCKER_GID=$(getent group docker | cut -d: -f3)

if kind get clusters 2>/dev/null | grep -qx kagent; then
  (umask 077; kind get kubeconfig --name kagent > config/kubeconfig)
else
  echo "kind cluster 'kagent' not found; Kubernetes widgets will be empty (run ../kagent/setup.sh)."
fi

docker compose up -d
echo "Homepage: http://localhost:3000"
