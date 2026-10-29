# Agent Arcade

NES-style arcade games where every character is a real AI agent: a kagent session running as an Agent Substrate actor in its own gVisor sandbox, with LLM calls going through agentgateway. One Go backend serves every game, the arcade front page, and the shared Substrate stats.

| Game | Shows off | Status | Design |
|------|-----------|--------|--------|
| Duck Hunt | Snapshots: idle agents sleep, suspend and resume | Playable | [docs/duck-hunt.md](docs/duck-hunt.md) |
| Sandbox Escape | Security: gVisor, egress policies, agentgateway holds the key | Next | |
| Fork Frenzy | Checkpoints and forks | Planned | |
| Agent Invaders | Scale and density | Planned | |
| Whack-a-Mole | Cold start (restore latency) | Planned | |

## Start

```bash
../vault/init.sh          # Vault unsealed (OpenAI key)
../kagent/setup.sh        # cluster, kagent + Substrate, agentgateway
./deploy.sh               # gateway, agents, worker pools, arcade image, host port 8090
```

| Page | URL |
|------|-----|
| Arcade front page (players join once, then pick a game) | http://localhost:8090/ |
| Duck Hunt phone / big screen (add `?autostart` for kiosk mode) | http://localhost:8090/duck-hunt, http://localhost:8090/duck-hunt/screen |
| Admin (no sign-in yet, LAN only) | http://localhost:8090/admin |
| Arcade-wide stats (JSON, for Homepage) | http://localhost:8090/api/stats |

`deploy.sh` can be rerun safely. It builds the image, pushes it to the local registry (`localhost:5001`) and rolls out a new version. `DUCK_WORKERS` sets the size of the duck worker pool (default 12). To reach the arcade from other machines, run `sudo ufw route allow proto tcp from any to any port 8090`. `./loadtest.sh [ducks]` runs a Duck Hunt load test.

## Layout

- `main.go`: wiring, the front page, `/api/join`, `/api/me` and `/api/stats`
- `internal/kagent`: the kagent client (sessions, A2A prompts, suspend and resume, Substrate actors and stats)
- `internal/platform`: what games share: players, Substrate stats, atelet timings, SSE hubs, HTTP helpers
- `games/<game>`: one package per game (state, rules, routes)
- `web/shared`: the NES styles, sprites, sounds and the live Substrate diagram; `web/<game>`: that game's pages
- `manifests/`:
  - `gateway.yaml` and `llm.yaml`: agentgateway front door and OpenAI route. The LLM route only matches the in-cluster hostname.
  - `<game>-agent.yaml` and `workerpool.yaml`: each game's Agent, Harness and WorkerPool
  - `app.yaml`: the arcade Deployment and its HTTPRoute
  - `atelet-metrics.yaml`: lets the backend read Substrate's own snapshot and restore timings

## What a sandboxed agent can reach (measured 2026-10-28)

Agents run with kagent's built-in `bash`, `read_file` and `write_file` tools inside their gVisor sandbox. A probe agent found:

- **Blocked:** the internet (no DNS), cloud metadata (169.254.169.254), the Kubernetes API, Substrate's snapshot store, and this host's services (Vault, Homepage, the kagent API). The host services got a bare 403 from Substrate's egress layer.
- **Reachable:** only agentgateway's LLM route.
- **Credentials:** the sandbox's `OPENAI_API_KEY` is a 26-character `kagent…` placeholder. The real key is added by agentgateway.
