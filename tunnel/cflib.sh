#!/usr/bin/env bash
# Shared by setup.sh and access.sh: loads CLOUDFLARE_API_TOKEN (from the environment or the
# lab Vault) and defines cfapi. Source it from the tunnel folder. Secrets are never printed.

# pick <json> <regex>: the value of the first key matching regex (case-insensitive).
pick() { jq -r --arg re "$2" '[to_entries[] | select(.key | test($re; "i")) | .value][0] // empty' <<<"$1"; }

if [ -z "${CLOUDFLARE_API_TOKEN:-}" ]; then
  echo "==> reading the Cloudflare token from the lab Vault"
  local_token=$(jq -r .root_token ../vault/.vault-keys.json)
  pointer=$(docker exec -e VAULT_TOKEN="$local_token" vault vault kv get -format=json -mount=secret kubecon/lab-vault | jq .data.data)
  lab_addr=$(pick "$pointer" 'addr|url')
  lab_token=$(pick "$pointer" 'token')
  [ -n "$lab_addr" ] && [ -n "$lab_token" ] || { echo "secret/kubecon/lab-vault needs an address and a token key" >&2; exit 1; }
  cf=$(curl -sf -H "X-Vault-Token: $lab_token" "$lab_addr/v1/secret/data/lab/cloudflare" | jq .data.data) ||
    { echo "Could not read secret/lab/cloudflare from the lab Vault" >&2; exit 1; }
  CLOUDFLARE_API_TOKEN=$(pick "$cf" 'api.?token|^token$|token')
  CLOUDFLARE_ACCOUNT_ID=${CLOUDFLARE_ACCOUNT_ID:-$(pick "$cf" 'account')}
  unset local_token pointer lab_token cf
  [ -n "$CLOUDFLARE_API_TOKEN" ] || { echo "No token key in secret/lab/cloudflare" >&2; exit 1; }
fi

# cfapi <method> <path> [json body]: calls the Cloudflare API and fails on errors.
cfapi() {
  local out
  out=$(curl -s -X "$1" "https://api.cloudflare.com/client/v4$2" \
    -H "Authorization: Bearer $CLOUDFLARE_API_TOKEN" -H "Content-Type: application/json" ${3:+--data "$3"})
  if [ "$(jq -r .success <<<"$out")" != "true" ]; then
    echo "Cloudflare API $1 $2 failed: $(jq -c '.errors' <<<"$out")" >&2
    exit 1
  fi
  jq .result <<<"$out"
}

