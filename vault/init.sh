#!/usr/bin/env bash
# Start Vault, initialize it on first run, and unseal it.
# Unseal keys and root token are kept in .vault-keys.json (local lab use only).
set -euo pipefail
cd "$(dirname "$0")"

KEYS=.vault-keys.json
vexec() { docker exec vault vault "$@"; }

docker compose up -d

until docker exec vault vault status >/dev/null 2>&1 || [ $? -eq 2 ]; do sleep 1; done

if [ "$(vexec status -format=json | jq -r .initialized)" != "true" ]; then
  echo "Initializing Vault..."
  vexec operator init -key-shares=1 -key-threshold=1 -format=json > "$KEYS"
  chmod 600 "$KEYS"
fi

if [ "$(vexec status -format=json | jq -r .sealed)" = "true" ]; then
  echo "Unsealing Vault..."
  vexec operator unseal "$(jq -r '.unseal_keys_b64[0]' "$KEYS")" >/dev/null
fi

echo
echo "Vault is up: http://localhost:8200"
echo "export VAULT_ADDR=http://localhost:8200"
echo "export VAULT_TOKEN=$(jq -r .root_token "$KEYS")"
