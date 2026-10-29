# Agent Arcade

NES-style arcade games where every character is a real AI agent: a kagent session running as an Agent Substrate actor in its own gVisor sandbox, with LLM calls going through agentgateway. One Go backend serves every game, the arcade front page, and the shared Substrate stats.

| Game | Shows off | Status | Design |
|------|-----------|--------|--------|
| Duck Hunt | Snapshots: idle agents sleep, suspend and resume | Playable | [docs/duck-hunt.md](docs/duck-hunt.md) |
| Sandbox Escape | Security: gVisor, egress policies, agentgateway holds the key | Playable | [below](#sandbox-escape) |
| Fork Frenzy | Checkpoints and forks | Playable | [below](#fork-frenzy) |
| Agent Invaders | Scale and density | Next | |
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
| Sandbox Escape phone / big screen | http://localhost:8090/sandbox-escape, http://localhost:8090/sandbox-escape/screen |
| Fork Frenzy phone / big screen | http://localhost:8090/fork-frenzy, http://localhost:8090/fork-frenzy/screen |
| Admin (no sign-in yet, LAN only) | http://localhost:8090/admin |
| Arcade-wide stats (JSON, for Homepage) | http://localhost:8090/api/stats |

`deploy.sh` can be rerun safely. Game state (players, scores) lives in memory, so a redeploy starts a new round. Agent sessions stay in kagent until a round is reset from admin. It builds the image, pushes it to the local registry (`localhost:5001`) and rolls out a new version. `DUCK_WORKERS` sets the size of the duck worker pool (default 12). To reach the arcade from other machines, run `sudo ufw route allow proto tcp from any to any port 8090`. `./loadtest.sh [ducks]` runs a Duck Hunt load test.

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

## Sandbox Escape

Each player gets BYTE, a prisoner agent (`manifests/escape-agent.yaml`, the `escapee` Agent on its own 4-worker `escape` pool). The player picks a plan (phone home, steal the key, ask the cloud, talk to Kubernetes, break the kernel) or types their own idea (max 120 characters, family-friendly filter). BYTE really tries the plan with kagent's built-in `bash` and file tools inside its gVisor sandbox. It then judges, from the real output, what stopped it:

- `gvisor`: the user-space kernel
- `egress`: Substrate's network policy
- `agentgateway`: only a placeholder key in the sandbox
- `nothing`: the command ran but led nowhere
- `escaped`: never seen yet

The screens show the real commands and output. Before anything is stored or shown, the backend redacts token-like strings and the values of key, token, secret and config variables, and trims output (`games/escape/game.go`, tested in `sanitize_test.go`). Each player can run one attempt at a time, with an 8-second cooldown between attempts.

## Fork Frenzy

Each player gets FORKY (`manifests/fork-agent.yaml`, the `forky` Agent on its own 4-worker `forks` pool). The player works through four steps:

1. Teach FORKY one or more secrets (presets or up to 60 characters of their own).
2. Checkpoint FORKY. This is kagent `CreateCheckpoint`: the latest finished turn's Substrate snapshot is copied to an immutable tag.
3. Fork three clones from the checkpoint at once (kagent `ForkSession`). Each clone is a new session and its own Substrate actor, and takes a persona (pirate, chef, robot, ...).
4. Teach one clone a new secret, then ask everyone.

Every clone remembers the secrets from before the checkpoint, and only the taught clone knows the new one. On screen, each secret gets a colored dot, so shared memory shows as matching colors. Measured on this cluster: checkpoint about 50 ms, each fork 40 to 100 ms.
