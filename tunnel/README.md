# Tunnel

Public HTTPS access to the arcade at `https://kubecon.agentpwned.ai`, through a remotely managed [Cloudflare Tunnel](https://developers.cloudflare.com/cloudflare-one/connections/connect-networks/). No firewall ports are opened: `cloudflared` runs in the kind cluster (namespace `cloudflared`, 2 replicas, `cloudflare/cloudflared:2026.10.1`) and dials out to Cloudflare.

```
phone ──https──> Cloudflare ──tunnel──> cloudflared (kind) ──> agentgateway "arcade" Gateway ──> arcade
```

| Public URL | What |
|------------|------|
| https://kubecon.agentpwned.ai/ | Arcade front page |
| https://kubecon.agentpwned.ai/agent-flow | Agent Flow (what the big screen's QR code opens) |
| https://kubecon.agentpwned.ai/admin | Blocked (404) at the tunnel; use http://localhost:8090/admin |

## Start

```bash
../arcade/deploy.sh   # the arcade must be running
./setup.sh
```

`setup.sh` can be rerun safely. It:

1. Reads a Cloudflare API token: `CLOUDFLARE_API_TOKEN` if set, otherwise `secret/lab/cloudflare` in the lab Vault (found through `secret/kubecon/lab-vault` in the local Vault). It never prints secrets.
2. Creates (or reuses) the `kubecon-arcade` tunnel and sets its routes: `kubecon.agentpwned.ai` to the arcade Gateway, `^/admin` to 404, everything else to 404.
3. Creates or updates only the `kubecon` CNAME record. Other `agentpwned.ai` records (apex, `www`, `lab`, `desk`) are left alone.
4. Stores the tunnel token in the `cloudflared/tunnel` Secret and rolls out `cloudflared`.

The token needs Account > Cloudflare Tunnel: Edit and Zone > DNS: Edit for `agentpwned.ai`.

## Login (Cloudflare Access)

```bash
./access.sh on    # only @solo.io and @maniak.io emails get in (one-time code by email)
./access.sh off   # public again: run this before the booth opens
```

`access.sh` puts a Cloudflare Access app in front of the whole hostname, including the games' WebSockets and the big screens. Visitors without one of those emails can't play while it is on. It needs a token with Account > Access: Apps and Policies: Edit and Access: Organizations, Identity Providers, and Groups: Edit, and Zero Trust set up on the account. `ACCESS_DOMAINS="solo.io maniak.io"` changes the allowed domains. The LAN screens (`http://localhost:8090`) don't go through Cloudflare and are not affected.

## Notes

- The LLM route on the Gateway only matches the in-cluster hostname, so the public hostname can't reach OpenAI.
- Everything else in the arcade is public, including the Agent Flow webhook (`POST /agent-flow/hook`), which only keeps a cleaned-up name.
- Server-Sent Events (the live screens) work through the tunnel; the arcade sends a heartbeat every 5 s.
- To take it down: `kubectl --context kind-kagent delete namespace cloudflared`, then delete the tunnel and the `kubecon` record in the Cloudflare dashboard.
