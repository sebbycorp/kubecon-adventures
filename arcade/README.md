# Agent Arcade

NES-style arcade games where every character is a real AI agent: a kagent session running as an Agent Substrate actor in its own gVisor sandbox, with LLM calls going through agentgateway. One Go backend serves every game, the arcade front page, and the shared Substrate stats.

| Game | Shows off | Status | Design |
|------|-----------|--------|--------|
| Duck Hunt | Snapshots: idle agents sleep, suspend and resume | Playable | [docs/duck-hunt.md](docs/duck-hunt.md) |
| Agent Flow | The whole stack, live: every component a request passes | Playable | [below](#agent-flow) |
| Substrate Siege | Lifecycle: suspend, snapshot, resume, delete, as a 3-minute multiplayer city shooter | Playable | [below](#substrate-siege), [docs/substrate-siege.md](docs/substrate-siege.md) |
| Cluster Defense | Scale and density: more agents than workers, by suspending the idle ones, as a co-op space shooter | Playable | [below](#cluster-defense) |
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
| Agent Flow phone / big screen (QR code on screen; `?join=<url>` sets its target) | http://localhost:8090/agent-flow, http://localhost:8090/agent-flow/screen |
| Substrate Siege phone / big screen (`?debug=1` on the phone shows frame and network timing) | http://localhost:8090/substrate-siege, http://localhost:8090/substrate-siege/screen |
| Cluster Defense phone / big screen | http://localhost:8090/cluster-defense, http://localhost:8090/cluster-defense/screen |
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

### agentgateway policies

`manifests/flow-policies.yaml` (applied by `deploy.sh`) gives flowbot its own LLM route, `/flow/v1` through the `flow-model` ModelConfig, so these policies don't change the other games:

| Policy | Target | What it does |
|--------|--------|--------------|
| `flow-llm-guard` | `openai-flow` route | Prompt guard (regex, `Reject`, 403): jailbreaks ("ignore previous instructions"), requests to reveal keys, tokens, passwords or credentials, and pasted `sk-…` keys. Scope is `Messages`, so the system prompt and tool output aren't scanned. Plus a local budget of 200k tokens and 300 requests a minute. |
| `front-door` | `arcade` route | 300 requests a minute per visitor, keyed on Cloudflare's `cf-connecting-ip` (LAN requests share one bucket). |
| `llm-in-cluster-only` | `openai` and `openai-flow` routes | LLM calls only from pod IPs (`10.244.0.0/16`). Before this, anyone on the LAN could reach the LLM routes through host port 8090 by faking the Host header. |

Agents only ever hold the placeholder key `via-agentgateway`; agentgateway adds the real one. Every LLM call reaches agentgateway from atenet-egress, the sandboxes' only way out.

kagent reports a guard rejection as `OpenAI API error: 403 Forbidden` (without the guard's message), so the backend treats a 403 from flowbot's LLM route as a block. The blocked prompt stays in the session's history, where the guard would reject every later call, so the backend deletes that visitor's session and the next request starts a fresh agent. The phone's "Try to steal the key" task triggers it on purpose; the big screen's gate flashes red and a toast names the visitor.

### Egress: Substrate's agentgateway and an EgressPolicy

`atenet-egress` is Substrate's own agentgateway build and the sandboxes' only way out. For each connection it resolves the calling actor and asks Substrate (`GetActorEgressPolicy`) whether that actor may reach the host. The sandbox's DNS (`169.254.17.1`) only resolves allowed names, so a blocked host fails with `bad address` before any request is sent. An agent may reach its model's host by default.

`manifests/flow-egress.yaml` grants one outside origin, `https://icanhazdadjoke.com`, through the kagent `EgressPolicy` `dad-jokes`. Agents opt in by name: flowbot lists it under `spec.egressPolicyRefs`, so the other games' agents don't get it. An EgressPolicy can also inject a credential for a header at the gateway, so the sandbox never holds it.

The phone's "Get a joke from the internet" task fetches a real joke (allowed). "Try another website" tries `example.com` and is blocked. On the big screen the agent goes to egress, with a "✓ allowed" or "✗ blocked by egress" bubble.

### Hints

A line under the headline rotates through facts from live data: sandboxes per pool, the share of agents asleep, run time vs sleep, agents per sandbox, and a savings estimate (agents minus sandboxes, times 24 hours, times an assumed $0.02 per always-on pod-hour; override with `POD_HOUR_USD`). `deploy.sh` measures the node count and this host's CPUs and RAM; the kind nodes share one machine, so the host's numbers are the real capacity.

The webhook works because agents already reach agentgateway's in-cluster host for LLM calls, and the arcade route on that Gateway matches any host. Measured on this cluster: new actor about 35 ms, snapshot about 275 ms, restore about 150 ms, a whole run 3 to 7 s.

The QR code points at `https://kubecon.agentpwned.ai/agent-flow` when the screen is opened on localhost, otherwise at the screen's own origin. Open `/agent-flow/screen?join=http://<lan-ip>:8090/agent-flow` to point phones somewhere else. Reset the round with `curl -XPOST localhost:8090/admin/agent-flow/api/reset`.

## Cluster Defense

A co-op space shooter (`games/defense`, 2D canvas, no build step) about density: more agents than workers. Subagents (planner, coder, tester, researcher, reviewer, deployer, summarizer, scraper) spin up at the edge of space as real `subagent` sessions on their own 16-worker `defense` pool (`manifests/defense-agent.yaml`), fly to the cluster station and each run one real task turn (a one-line JSON reply shown in a bubble; a reply that fails output validation gets a fixed line, never the model's text). Every running agent holds a worker. Players fly ships with an on-screen stick, hold FIRE (shots bend toward an agent in a 22° cone), and enough hits suspend the agent for real: Agent Substrate snapshots it, its worker frees, and it drifts to Snapshot Storage as a capsule. When running agents fill every worker the cluster is full and the round is over; every session the round made (running and suspended) is deleted, and the next round starts after 15 s or on PLAY. The fill gauge is the game's count of running sessions against the pool's replicas; the big screen also shows what Substrate itself reports for the `subagent` Agent.

- Kinds: scouts (1 hit, fast), workers (2), orchestrators (6, spawn 2–3 real subagents when they reach the cluster or after 20 s; +5 if caught before), gold runaways (+10, flee ships), and a boss every 5th wave (24 hits, scales with the crowd, spawns 3 subagents every 15 s). Later waves and bigger crowds add armour (25% a wave, compounding; +1 per 3 pilots).
- Named waves with a 3 s banner, combos up to x5 (suspends within 2 s), power-ups from suspended agents (rapid fire, spread, shield that rams, and the Snapshot Bomb: a real batch of suspends around you), bumping into an agent stuns your blaster, the 70%/90% alarm and "SAVED IT!", team streak and SQUAD SAVE (3+ pilots within 2 s), titles (Rookie Admin → SRE → Platform Hero → Substrate Legend), session bests and an all-time board, instant PLAY AGAIN with "beat X to rank #N".
- Spawns, orchestrator and boss subagents included, share one budget (1.4 a second, burst 6: about the 100k tokens/min the `/defense/v1` route allows, `manifests/defense-policies.yaml`) and never ask for more sessions than the pool has free workers.
- 48 pilots at most, 8 public big screens; the startup sweep deletes every `subagent` session left by a previous process. `curl -XPOST localhost:8090/admin/cluster-defense/api/end` ends the live round (and deletes its sessions); `.../api/pause` stops spawns.

Load (`go test -tags cluster -run TestBoothLoad ./games/defense/`, `ARCADE_URL=http://localhost:8090`, 2026-11-03): one task turn is about 1.1k tokens and 1.2 s; a suspend mid-turn waits for the turn to end, then the Substrate call is about 25 ms. 16 bots for 8 minutes: 633 real suspends, none stuck, second hit to confirmed suspend p50 66 ms p95 1.3 s, Substrate suspend call p50 25 ms p95 40 ms, 86–89k tokens a minute on agentgateway with no 429s. Those bots fire nonstop with aim assist and held the cluster to wave 13 (peak 13 of 16 workers, 641 sessions hosted on 16 workers); 2 low-skill bots lost it at 58–66 s (CLUSTER FULL, every session deleted, next round started on its own). How long a real phone crowd lasts is untested.

## Substrate Siege

A 3-minute multiplayer shooter in a stylised night city (`games/heist`, design in [docs/substrate-siege.md](docs/substrate-siege.md)). Every agent on the streets is a real `heister` agent session on its own 40-worker `heist` pool (`manifests/heist-agent.yaml`). A player taps PLAY and has 3 minutes to freeze as many agents as they can; a red arrow on the screen edge points to the nearest one. Players play solo or as a team of up to 4 on one clock (a 4-letter code), with an on-screen joystick, FIRE and BOOK; there is no keyboard. The server runs the world at 15 Hz over one WebSocket per phone (`/substrate-siege/ws`), and every game action is the real Substrate call:

| In the game | Real call |
|-------------|-----------|
| An agent spawns 6–14 tiles from you and works a 4-step job, one turn every 12–16 s | `CreateSession`, then one A2A turn per step (its line is the speech bubble) |
| Two hits freeze it: +1 | `SuspendSession`, which lands when the agent's current turn ends; the agent shows FREEZING at once. A freeze that races the final step wins. |
| Tap BOOK next to a frozen agent: +2 | `ResumeSession`, one "you got booked" turn, `Checkpoint` of that turn, then `DeleteSession` |
| A frozen agent nobody books for 8 s | `DeleteSession` |
| It finishes its 4th step | It escapes; its session is deleted and a new agent takes its place |
| Pick up a board, a CPU, RAM and a drive (DDR1–5) | A real `sidekick` ally session that follows you and shoots agents |

A run keeps 1 + its online players agents on the streets (at most 4), 32 in the whole city; a freed slot goes to the run with the fewest. Agents shoot back; a downed player drops the parts they carry. The leaderboard ranks players by their best run (points; total freezes break ties). Old `/grand-theft-agent` links redirect here.

- A turn whose reply fails the agent's output validation falls back to a fixed line, so a target never stalls on a bad reply.
- Game state lives in memory, so at startup the game deletes every `heister` and `sidekick` session left over from a previous process (with their checkpoints), and refuses new runs and rigs until that list is in. Booked targets' checkpoints are deleted by the admin reset and on shutdown.
- `manifests/heist-policies.yaml` gives the heist agents their own LLM route (`/heist/v1`) with a prompt guard and a budget of 300k tokens and 400 requests a minute; a policy stop shows on the screen.
- The phone switches to low quality (lower pixel ratio, no shadows) by itself on a slow first few seconds; MENU has a switch for it.
- The big screen (`/substrate-siege/screen`) shows the whole city, a QR code, the best-run leaderboard, a live feed and live suspend and booking times.

Booth load (24 bots playing back-to-back 3-minute runs for 5 minutes, `TestBoothLoad`, 2026-11-03): 403 freezes, 240 bookings, no stuck runs and no booking conflicts, but the LLM budget is the limit. All 24 runs starting together burst to 584k tokens a minute in the first minute, then held at 295–325k against the 300k budget, so agentgateway rate-limited about 14% of LLM calls (211 of 1564). A turn caught by that waits out its 45 s timeout, and a freeze has to wait for the agent's turn to end: freeze p95 33 s (p50 3.5 s; the suspend call alone p95 0.3 s), booking p95 10.7 s, 70 steps and bookings timed out. The phone shows FREEZING the moment the hit lands either way. Real players start at different times and book less than bots; raising the budget or spacing steps further is the lever. Reset the round with `curl -XPOST localhost:8090/admin/substrate-siege/api/reset`.
