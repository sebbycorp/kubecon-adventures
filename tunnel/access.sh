#!/usr/bin/env bash
# Cloudflare Access login in front of kubecon.agentpwned.ai: only @solo.io and @maniak.io
# emails get in, signing in with a one-time code mailed to them.
#   ./access.sh on    add (or update) the login; safe to rerun
#   ./access.sh off   remove it, making the site public again (do this before the booth opens)
#
# Needs a Cloudflare API token with Account > Access: Apps and Policies: Edit and
# Account > Access: Organizations, Identity Providers, and Groups: Edit, on an account with
# Zero Trust set up (a team domain). Token loading is the same as setup.sh (cflib.sh).
set -euo pipefail
cd "$(dirname "$0")"

PUBLIC_HOST=${PUBLIC_HOST:-kubecon.agentpwned.ai}
ZONE=${ZONE:-agentpwned.ai}
DOMAINS=${ACCESS_DOMAINS:-solo.io maniak.io}
APP_NAME="kubecon arcade"
POLICY_NAME="kubecon arcade: ${DOMAINS// / or }"

case "${1:-}" in on | off) ;; *) echo "usage: $0 on|off" >&2; exit 2 ;; esac

. ./cflib.sh

ACCOUNT=${CLOUDFLARE_ACCOUNT_ID:-$(cfapi GET "/zones?name=$ZONE" | jq -r '.[0].account.id // empty')}
[ -n "$ACCOUNT" ] || { echo "Zone $ZONE not found for this token" >&2; exit 1; }
A="/accounts/$ACCOUNT/access"

APP_ID=$(cfapi GET "$A/apps" | jq -r --arg d "$PUBLIC_HOST" '[.[] | select(.domain == $d)][0].id // empty')
POLICY_ID=$(cfapi GET "$A/policies" | jq -r --arg n "$POLICY_NAME" '[.[] | select(.name == $n)][0].id // empty')

if [ "$1" = off ]; then
  [ -n "$APP_ID" ] && cfapi DELETE "$A/apps/$APP_ID" >/dev/null && echo "==> removed the Access app for $PUBLIC_HOST"
  [ -n "$POLICY_ID" ] && cfapi DELETE "$A/policies/$POLICY_ID" >/dev/null && echo "==> removed policy $POLICY_NAME"
  echo "$PUBLIC_HOST is public again."
  exit 0
fi

echo "==> one-time PIN login"
OTP_ID=$(cfapi GET "$A/identity_providers" | jq -r '[.[] | select(.type == "onetimepin")][0].id // empty')
if [ -z "$OTP_ID" ]; then
  OTP_ID=$(cfapi POST "$A/identity_providers" '{"name":"One-time PIN","type":"onetimepin","config":{}}' | jq -r .id)
fi

echo "==> policy: $POLICY_NAME"
include=$(jq -nc --arg d "$DOMAINS" '[$d | split(" ")[] | select(. != "") | {email_domain: {domain: .}}]')
policy=$(jq -nc --arg n "$POLICY_NAME" --argjson inc "$include" '{name: $n, decision: "allow", include: $inc}')
if [ -n "$POLICY_ID" ]; then
  cfapi PUT "$A/policies/$POLICY_ID" "$policy" >/dev/null
else
  POLICY_ID=$(cfapi POST "$A/policies" "$policy" | jq -r .id)
fi

echo "==> Access app for $PUBLIC_HOST"
app=$(jq -nc --arg n "$APP_NAME" --arg d "$PUBLIC_HOST" --arg idp "$OTP_ID" --arg pol "$POLICY_ID" '{
  name: $n, domain: $d, type: "self_hosted", session_duration: "24h",
  allowed_idps: [$idp], auto_redirect_to_identity: true, app_launcher_visible: false,
  policies: [{id: $pol, precedence: 1}]}')
if [ -n "$APP_ID" ]; then
  cfapi PUT "$A/apps/$APP_ID" "$app" >/dev/null
else
  cfapi POST "$A/apps" "$app" >/dev/null
fi

echo "$PUBLIC_HOST now needs a login: @${DOMAINS// /, @} emails, one-time code by email."
echo "Run ./access.sh off to make it public again."
