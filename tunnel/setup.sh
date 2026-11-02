#!/usr/bin/env bash
# Public access for the arcade: a remotely managed Cloudflare Tunnel from kubecon.agentpwned.ai
# to the arcade's agentgateway, with cloudflared running in the kind-kagent cluster.
# Safe to rerun: it reuses the tunnel and DNS record if they exist. /admin is blocked publicly.
#
# Needs a Cloudflare API token with Account > Cloudflare Tunnel: Edit and Zone > DNS: Edit for
# agentpwned.ai. Export CLOUDFLARE_API_TOKEN, or leave it unset to read it from the lab Vault
# (its address and token are in the local Vault at secret/kubecon/lab-vault; the Cloudflare
# token is at secret/lab/cloudflare). Secrets are never printed.
set -euo pipefail
cd "$(dirname "$0")"
export PATH=$HOME/.local/bin:$PATH
k() { kubectl --context kind-kagent "$@"; }

PUBLIC_HOST=${PUBLIC_HOST:-kubecon.agentpwned.ai}
ZONE=${ZONE:-agentpwned.ai}
TUNNEL=${TUNNEL_NAME:-kubecon-arcade}
ORIGIN=${ORIGIN:-http://arcade.agentgateway-system.svc.cluster.local:80}

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

echo "==> zone $ZONE"
zone=$(cfapi GET "/zones?name=$ZONE")
ZONE_ID=$(jq -r '.[0].id // empty' <<<"$zone")
[ -n "$ZONE_ID" ] || { echo "Zone $ZONE not found for this token" >&2; exit 1; }
ACCOUNT=${CLOUDFLARE_ACCOUNT_ID:-$(jq -r '.[0].account.id' <<<"$zone")}

echo "==> tunnel $TUNNEL"
TUNNEL_ID=$(cfapi GET "/accounts/$ACCOUNT/cfd_tunnel?name=$TUNNEL&is_deleted=false" | jq -r '.[0].id // empty')
if [ -z "$TUNNEL_ID" ]; then
  TUNNEL_ID=$(cfapi POST "/accounts/$ACCOUNT/cfd_tunnel" "$(jq -nc --arg n "$TUNNEL" '{name:$n, config_src:"cloudflare"}')" | jq -r .id)
  echo "    created $TUNNEL_ID"
else
  echo "    reusing $TUNNEL_ID"
fi

echo "==> tunnel routes: $PUBLIC_HOST -> $ORIGIN (/admin blocked)"
cfapi PUT "/accounts/$ACCOUNT/cfd_tunnel/$TUNNEL_ID/configurations" "$(jq -nc --arg h "$PUBLIC_HOST" --arg o "$ORIGIN" '{config: {ingress: [
  {hostname:$h, path:"^/admin", service:"http_status:404"},
  {hostname:$h, service:$o, originRequest:{connectTimeout:10}},
  {service:"http_status:404"}]}}')" >/dev/null

echo "==> DNS: $PUBLIC_HOST CNAME $TUNNEL_ID.cfargotunnel.com (other records untouched)"
record=$(jq -nc --arg n "$PUBLIC_HOST" --arg c "$TUNNEL_ID.cfargotunnel.com" '{type:"CNAME", name:$n, content:$c, proxied:true, comment:"kubecon arcade tunnel (tunnel/setup.sh)"}')
REC_ID=$(cfapi GET "/zones/$ZONE_ID/dns_records?name=$PUBLIC_HOST" | jq -r '.[0].id // empty')
if [ -n "$REC_ID" ]; then
  cfapi PUT "/zones/$ZONE_ID/dns_records/$REC_ID" "$record" >/dev/null
else
  cfapi POST "/zones/$ZONE_ID/dns_records" "$record" >/dev/null
fi

echo "==> cloudflared in the cluster"
k create namespace cloudflared --dry-run=client -o yaml | k apply -f - >/dev/null
cfapi GET "/accounts/$ACCOUNT/cfd_tunnel/$TUNNEL_ID/token" | jq -r . |
  k -n cloudflared create secret generic tunnel --from-file=token=/dev/stdin --dry-run=client -o yaml | k apply -f - >/dev/null
k apply -f manifests/cloudflared.yaml
k -n cloudflared rollout restart deploy/cloudflared >/dev/null
k -n cloudflared rollout status deploy/cloudflared --timeout 3m

echo
echo "Public:  https://$PUBLIC_HOST/  (arcade), https://$PUBLIC_HOST/agent-flow (Agent Flow)"
echo "Blocked: https://$PUBLIC_HOST/admin (use http://localhost:8090/admin on the LAN)"
