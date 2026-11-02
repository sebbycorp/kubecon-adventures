# Agent Arcade

NES-style arcade games where every character is a real AI agent: a kagent session running as an Agent Substrate actor in its own gVisor sandbox, with LLM calls going through agentgateway. One Go backend serves every game, the arcade front page, and the shared Substrate stats.

| Game | Shows off | Status | Design |
|------|-----------|--------|--------|
| Duck Hunt | Snapshots: idle agents sleep, suspend and resume | Playable | [docs/duck-hunt.md](docs/duck-hunt.md) |
| Sandbox Escape | Security: gVisor, egress policies, agentgateway holds the key | Playable | [below](#sandbox-escape) |
| Fork Frenzy | Checkpoints and forks | Playable | [below](#fork-frenzy) |
| Agent Flow | The whole stack, live: every component a request passes | Playable | [below](#agent-flow) |
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
| Agent Flow phone / big screen (QR code on screen; `?join=<url>` sets its target) | http://localhost:8090/agent-flow, http://localhost:8090/agent-flow/screen |
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

## Agent Flow

A booth screen that shows kagent on Agent Substrate working. Visitors scan the QR code, enter a name, and pick a task for their own agent (`manifests/flow-agent.yaml`, the `flowbot` Agent on its own 12-worker `flow` pool):

| Task | What the agent really does |
|------|----------------------------|
| Write code | writes `fizz.sh` with its file tool and runs it with bash |
| Call a webhook | POSTs JSON to the booth webhook (`POST /agent-flow/hook`) with `wget`, through agentgateway |
| Explore the sandbox | runs `uname -a`, `id` and `ls /` and reports the gVisor kernel |
| Roll the dice | rolls three dice with `$RANDOM` |
| Write a haiku | no tools, only the LLM |

The big screen (`/agent-flow/screen`, styled after tessl.io's isometric diagrams, with the kagent logo) is a map of the real components: agentgateway, kagent-controller with postgres, the Substrate control plane (ate-api-server, ate-controller, ate-idp), atelet and the gVisor sandbox, rustfs, and atenet-egress. Every request is a cube with the visitor's name, moved by stages the backend reports as they happen:

- First run: kagent `CreateSession` (a new actor scheduled on the `flow` pool). Later runs: `ResumeSession` (atelet restores the actor from its rustfs snapshot).
- The prompt goes over A2A streaming (`Kagent.PromptStream`), so each LLM call (the cube leaves the screen toward OpenAI), each tool call (back to the sandbox, with the real command) and each webhook call (to atenet-egress) shows when it happens.
- After the answer, the backend suspends the session (snapshot to rustfs, worker freed), retrying while kagent settles the finished turn.

### Suspend and resume, on show

Agent Flow makes Substrate's snapshots the headline, with real data only:

- **Lifecycle strip.** The story card and the phone show each run's path with real timings: New actor (or Asleep for N · 0 CPU → Restoring) → Running → Saving → Asleep.
- **Nothing lost.** The backend remembers the files an agent wrote with `write_file`. After a restore, the screen shows them as kept, and the "Pick up where you left off" task has the agent list `/data/workspace` and continue its work. Files in `/data/workspace` and the conversation survive a snapshot. Tested on this cluster: `/tmp` and background processes do not, so the demo doesn't claim they do.
- **Density.** "N agents on M sandboxes · K asleep using 0 CPU" from Substrate's summary, with one floor tile per sleeping agent next to Snapshot Storage.
- **Node hops.** The actor poller records the node an agent slept on. When it wakes on another node, the screen calls it out (seen on this cluster: kagent-worker → kagent-worker2).
- **Sleep and Wake buttons** on the phone (`POST /agent-flow/api/sleep`, `/api/wake`). A hand-woken agent holds a worker until it is used or put to sleep; after 45 s idle the backend suspends it again ("auto").
- **New vs restored.** New agents are green, restored ones blue; the stats compare the first-answer time of each.

The webhook works because agents already reach agentgateway's in-cluster host for LLM calls, and the arcade route on that Gateway matches any host. Measured on this cluster: new actor about 35 ms, snapshot about 275 ms, restore about 150 ms, a whole run 3 to 7 s.

The QR code points at `https://kubecon.agentpwned.ai/agent-flow` when the screen is opened on localhost, otherwise at the screen's own origin. Open `/agent-flow/screen?join=http://<lan-ip>:8090/agent-flow` to point phones somewhere else. Reset the round with `curl -XPOST localhost:8090/admin/agent-flow/api/reset`.
