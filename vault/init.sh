#!/usr/bin/env bash
# Start Vault, initialize it on first run, and unseal it.
# Unseal keys and root token are kept in .vault-keys.json (local lab use only).
# Set VAULT_ROOT_TOKEN in .env to replace the generated root token on first init.
set -euo pipefail
cd "$(dirname "$0")"
[ -f .env ] && . ./.env

KEYS=.vault-keys.json
vexec() { docker exec vault vault "$@"; }

docker compose up -d

until docker exec vault vault status >/dev/null 2>&1 || [ $? -eq 2 ]; do sleep 1; done

if [ "$(vexec status -format=json | jq -r .initialized)" != "true" ]; then
  echo "Initializing Vault..."
  vexec operator init -key-shares=1 -key-threshold=1 -format=json > "$KEYS"
  chmod 600 "$KEYS"
  FRESH=1
fi

if [ "$(vexec status -format=json | jq -r .sealed)" = "true" ]; then
  echo "Unsealing Vault..."
  vexec operator unseal "$(jq -r '.unseal_keys_b64[0]' "$KEYS")" >/dev/null
fi

if [ -n "${FRESH:-}" ] && [ -n "${VAULT_ROOT_TOKEN:-}" ]; then
  echo "Setting custom root token..."
  GEN=$(jq -r .root_token "$KEYS")
  docker exec -e VAULT_TOKEN="$GEN" vault vault token create -id="$VAULT_ROOT_TOKEN" \
    -policy=root -orphan -display-name=root >/dev/null 2>&1
  docker exec -e VAULT_TOKEN="$GEN" vault vault token revoke -self >/dev/null
  jq --arg t "$VAULT_ROOT_TOKEN" '.root_token=$t' "$KEYS" > "$KEYS.tmp" && mv "$KEYS.tmp" "$KEYS"
  chmod 600 "$KEYS"
fi

echo
echo "Vault is up: http://localhost:8200"
echo "export VAULT_ADDR=http://localhost:8200"
echo "export VAULT_TOKEN=$(jq -r .root_token "$KEYS")"
