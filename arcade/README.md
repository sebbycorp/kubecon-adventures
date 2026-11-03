# Agent Arcade

NES-style arcade games where every character is a real AI agent: a kagent session running as an Agent Substrate actor in its own gVisor sandbox, with LLM calls going through agentgateway. One Go backend serves every game, the arcade front page, and the shared Substrate stats.

| Game | Shows off | Status | Design |
|------|-----------|--------|--------|
| Duck Hunt (at `/duck-hunt`) | Suspend and resume in milliseconds, as a landscape phone shooter: every target is a real agent woken for you, every hit a real snapshot | Playable | [below](#duck-hunt) |
| Agent Flow | The whole stack, live: every component a request passes | Playable | [below](#agent-flow) |
| Substrate Siege | Lifecycle: suspend, snapshot, resume, delete, as a 3-minute multiplayer city shooter | Playable | [below](#substrate-siege), [docs/substrate-siege.md](docs/substrate-siege.md) |
| Cluster Defense | Scale and density: more agents than workers, by suspending the idle ones, as a co-op space shooter | Playable | [below](#cluster-defense) |
| Gateway Defense | The platform's controls (prompt guard, rate limit, egress, suspend) as towers, each kill a real call; solo tower defense | Playable | [below](#gateway-defense) |
| Flappy Substrate (at `/flappy`) | Resume and suspend of your own agent: you are the Agent Substrate octopus, woken to swim and snapshotted when you crash | Playable | [below](#flappy-substrate) |
| Pod Packer (at `/pod-packer`) | Suspend and resume at scale: Tetris where every block is an agent pod; every row cleared snapshots real agents to free a node | Playable | [below](#pod-packer) |
| Substrate Selfie (at `/selfie`) | agentgateway in front of an LLM with a photo: a selfie becomes 10 funny scenes with the Agent Substrate octopus; a real caption agent wakes and is snapshotted per photo | Playable | [below](#substrate-selfie) |
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
| Duck Hunt phone (landscape) / big screen | http://localhost:8090/duck-hunt, http://localhost:8090/duck-hunt/screen |
| Agent Flow phone / big screen (QR code on screen; `?join=<url>` sets its target) | http://localhost:8090/agent-flow, http://localhost:8090/agent-flow/screen |
| Substrate Siege phone / big screen (`?debug=1` on the phone shows frame and network timing) | http://localhost:8090/substrate-siege, http://localhost:8090/substrate-siege/screen |
| Cluster Defense phone / big screen | http://localhost:8090/cluster-defense, http://localhost:8090/cluster-defense/screen |
| Gateway Defense phone / big screen | http://localhost:8090/gateway-defense, http://localhost:8090/gateway-defense/screen |
| Flappy Substrate phone / big screen | http://localhost:8090/flappy, http://localhost:8090/flappy/screen |
| Pod Packer phone / big screen | http://localhost:8090/pod-packer, http://localhost:8090/pod-packer/screen |
| Substrate Selfie phone / big screen | http://localhost:8090/selfie, http://localhost:8090/selfie/screen |
| Admin (no sign-in yet, LAN only) | http://localhost:8090/admin |
| Arcade-wide stats (JSON, for Homepage) | http://localhost:8090/api/stats |

`deploy.sh` can be rerun safely. Game state (players, scores) lives in memory, so a redeploy starts a new round. Agent sessions stay in kagent until a round is reset from admin. It builds the image, pushes it to the local registry (`localhost:5001`) and rolls out a new version. `DUCK_WORKERS` sets the size of the duck worker pool (default 12). To reach the arcade from other machines, run `sudo ufw route allow proto tcp from any to any port 8090`. `./loadtest.sh [players] [seconds]` runs a Duck Hunt load test.

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
| `front-door` | `arcade` route | 3,000 requests a minute per visitor IP, keyed on Cloudflare's `cf-connecting-ip` (LAN requests share one bucket). High because a venue's Wi-Fi puts a whole booth behind one public IP: the combined load test hit 300 at once. |
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

## Gateway Defense

A solo tower defense on the phone, drawn neon-cyber on a 2D canvas. Rogue agent traffic marches down a lane to the player's cluster: jailbreakers, token-burners, escapers (blocked-site callers), armoured runaways (idle agents hogging workers), obfuscated jailbreaks (shielded: only a level-2 Prompt Guard can hurt them), burst burners (split into two burners), and later context floods (fast, from wave 12), data smugglers (armoured, split into escapers, from 18) and zombie agents (armoured, call in runaways, from 30). There are 99 waves and they never stop: each one sends itself after a countdown (6 s before wave 1, 4 s between waves; tapping the countdown sends it early), and the player builds and upgrades while they run. The waves are generated from `Campaign` in `games/gatewaydef/config.go` (served to the phone, so both agree): every wave is harder than the last (HP scaled by (1 + 0.32(n-1)) × 1.012^(n-1), one more creep each wave), each leans on one tower in turn so a run needs all four, every seventh mixes everything unlocked, and every tenth wave and the 99th end in a Rogue Orchestrator, an armoured boss scaled to its wave that summons obfuscated jailbreaks at 66% and 33% health (the 99th, The Last Orchestrator, 2.5× tougher). After wave 99 the waves go on endlessly (a boss every tenth). A run ends only when the 20 lives are gone. The towers are the platform's controls, each 2.5× damage on the traffic it was made for, with five levels that visibly change the tower. The simulation is `web/gateway-defense/sim.js` (no DOM; the rules come from `games/gatewaydef/config.go`, so the phone and the server agree), drawn by `td.js`. The balance is tuned with `node web/gateway-defense/balance.mjs <rules.json>` (dump the rules with `GATEDEF_RULES_OUT=/tmp/rules.json go test -run TestDumpRules ./games/gatewaydef/`): over 80 headless runs a focused build (it covers the next wave's weaknesses) first leaks around wave 10 (the first boss), falls on wave 23 (median), and 100% get past wave 10, 86% past 20 and 10% past 30; a spread build (two of every needed tower) falls on wave 33 (median, 54% past 30). No run comes near wave 99. A tower's kill asks the server for a real call, at most 2 a second per player (burst 3, 12 in flight across the arcade), and the phone shows its real outcome over the kill:

| Tower | The real call | What is real |
|-------|---------------|--------------|
| Prompt Guard | A jailbreak prompt into `/gatedef/guard/v1/chat/completions` | agentgateway's prompt guard answers 403. The route's backend is the arcade's own sink (a custom AI provider on the arcade Service), never OpenAI: even a guard miss can't reach an LLM, and no key is attached. |
| Rate Limiter | `/gatedef/limit`, keyed per player, 1 request a minute | agentgateway's local rate limit answers 429 (up to 3 tries, a burner's burst). |
| Suspend Beam | A Substrate Suspend of a `rogue` session | Three runaway sessions on the `gatedef` WorkerPool, primed once with a one-word turn, are suspended for real and resumed 2 s later (no tokens after priming). |
| Egress Wall | `rogue` runs `wget https://example.com` with its bash tool | atenet-egress refuses it (rogue has no EgressPolicy). It costs an LLM turn, so one shared probe runs at most once a minute; the first kill after a probe shows it as real, later ones as its last real result. |

A kill over the budget shows that tower's last real result, marked as such. Leaks cost lives (two for runaways, obfuscated jailbreaks, smugglers and zombies, ten for the boss) and say what the creep would have done. Scores are checked before the leaderboard: at most the most the wave reached allows, counting splits, summons and endless waves (up to wave 300), and no faster than its creeps can spawn on 2× (`MaxScore`, `MinSeconds`; the countdowns aren't counted, since they can be skipped); a win means all 99 waves were cleared. The leaderboard is today's best run per player (in memory, so a restart clears it): `GET /gateway-defense/api/board` gives the top 10 with the asking player's row and rank, the phone shows it from the start screen and the menu, a live rank ticker during a run ("#4 live · 42 to pass Linus"), and on the end screen the rank, the score to beat for the next place and the top 5. The big screen (`/gateway-defense/screen`) shows a QR code, per-control real call counts and p50 timings, a live feed of the real blocks and an animated top 10 (rows slide on rank changes; new entries and climbs flash). `manifests/gatedef.yaml` holds the WorkerPool, the `rogue` Agent, its guarded LLM route (`/gatedef/v1`, 30k tokens a minute), the guard and limit routes and their policies, all in-cluster only.

## Duck Hunt

A landscape phone shooter at `/duck-hunt` (it replaced Duck Hunt, and keeps its URL, its `duck` agent and its `ducks` worker pool). Agent birds with the kagent face fly over a night city of server racks; tap one to shoot it and it drops into Snapshot Storage. Runs are 75 seconds; a wave every 15 s flies faster and puts more targets in the air (one, then two, then three).

What is real:

- **The targets.** The game keeps a pool of `duck` sessions asleep as Substrate snapshots (it adopts the agent's existing sessions and creates more up to 24). Launching a target is a real `Resume`: Substrate wakes the actor on a worker, and the phone shows "woke in N ms".
- **A hit.** The server scores it (100, 150 for swift, 500 for golden, times the combo: hits within 2.2 s build ×2 up to ×5) and suspends the session: a real snapshot, shown as "snapshotted in N ms" when the agent lands in the bin. A target that escapes is suspended too.
- **Lines.** About one launch in five (at most one every 4 s across the arcade) the agent gets one real LLM turn and its line shows in a speech bubble. A reply that fails the agent's output schema gets a fixed line; replies are never logged. A say turn and the suspend that ends its flight are serialized, so a late turn can't wake a shot agent again.
- **Capacity.** At most 10 targets are awake across the arcade (the `ducks` pool has 12 workers) and 40 runs at once; a launch over the cap is retried by the phone. A target flying longer than `-idle` seconds (default 20) is put back to sleep.

Scores are the server's, so the leaderboard needs no plausibility check: a hit counts only on the run's own target, at least 250 ms after it woke, 110 ms after the previous hit, and before the run ends. The board keeps each player's best run (in memory, like the other games). The big screen shows a QR code, the animated top 10, a live feed of hits with their real suspend times and lines, and the pool's awake and asleep counts with the snapshot and wake-up p50. Admin: `/admin/duck-hunt` (reset the board).

## Flappy Substrate

A one-tap underwater swim at `/flappy` where the player is the agent: the Agent Substrate octopus. Tap to swim up through gaps between coral and kelp pillars, "OOM" walls and "429" gates; the course speeds up, the gaps narrow, and touching a pillar, the surface or the sea floor ends the run. A gap is worth 1, a bubble 5.

What is real:

- **Your agent.** The game keeps a pool of 10 `octo` sessions (agent `octo`, harness and WorkerPool `flappy`, 6 workers) asleep as Substrate snapshots. PLAY (and PLAY AGAIN) is a real `Resume` of one of them, preferring the octopus you swam last, and the phone shows "woke in N ms" before the countdown. A crash is a real `Suspend`: the octopus sinks into the Snapshot Storage chest and the phone shows "snapshotted in N ms".
- **Never blocking.** At most 6 octopuses are awake at once (one per worker). Over that, or if a wake takes longer than 8 s or fails, the run starts anyway and the phone says the player swims without an agent; the score still counts. A run nobody reports a crash for is put back to sleep after 10 minutes.
- **No LLM in play.** Each pool session ran one short placing turn when the arcade started (it puts the actor on a worker); the game makes no other LLM calls. The pool is rebuilt at every start (leftover `octo` sessions are deleted) and deleted on shutdown.

The swim runs on the phone on a fixed 120 Hz step, with the course the server serves (`/flappy/api/rules`, from `games/flappy/rules.go`). The server checks every reported score against those rules before it reaches the board: the gaps must be reachable in the run's time at the course's speed, and there is at most one bubble per gap. The big screen shows a QR code, the animated top 10, a live feed (wakes, crashes with their snapshot times, new #1s), and the awake octopuses with the snapshot and wake-up p50. Admin: `POST /admin/flappy/api/reset` clears the board.

## Pod Packer

Tetris on the phone at `/pod-packer`, where the well is the cluster (each of the 20 rows is a worker node, `node-01` at the bottom) and every block is an agent pod. The point is Agent Substrate's: run more agents than you have room for. Standard guideline rules: a 7-bag, SRS rotation with wall kicks, a ghost piece, HOLD, a 0.5 s lock delay, levels every 10 lines, and 100/300/500/800 times the level for 1-4 lines, with back-to-back tetrises and combos. On-screen buttons (hold, left, rotate, right, soft drop, hard drop) or gestures (tap rotates, drag moves, swipe down drops); arrow keys, space and C work on desktop.

What is real:

- **The pods.** The game keeps a pool of 6 `packer` sessions (agent `packer`, harness and WorkerPool `podpacker`, 4 workers). While anyone plays, up to 4 are awake (a real `Resume` each). Every line a player clears snapshots one awake pod with a real `Suspend`, all of a tetris's four at once, and the phone shows it over the cleared rows: "node-07 freed · 2 agents snapshotted in 140 ms". The pool wakes them again in the background, so the next clear has pods to snapshot. With nobody playing for a minute, the awake pods go back to sleep.
- **Never blocking.** A clear waits at most 4 s for its snapshots; with no pod awake yet it says so ("no pod awake to snapshot yet") and play goes on.
- **No LLM in play.** Each pool session ran one short placing turn when the arcade started; the pool is rebuilt at every start (leftover `packer` sessions are deleted) and deleted on shutdown.

The pieces fall on the phone with the rules the server serves (`/pod-packer/api/rules`, from `games/podpacker/rules.go`). Each clear is reported as it happens (`POST /pod-packer/api/clear`, at most one every 150 ms), and the server checks the final score against the rules before it reaches the board: lines no more than the server saw cleared, every cleared cell coming from a placed piece, pieces no faster than 5 a second, tetrises no more than lines allow, and the score no higher than the scoring table could give at the level reached. For the all-games board, 20,000 points is par (1,000 arcade points). Game over ("cluster full, no room to schedule") shows the run's real snapshots and their median time, the rank and the share button. The big screen shows a QR code, the top 10, a live feed of freed nodes with their real snapshot times, and the pods awake, agents snapshotted and snapshot p50. Admin: `POST /admin/pod-packer/api/reset` clears the board.

## Substrate Selfie

A photo booth on the phone at `/selfie`. After a consent notice ("Your photo is sent to an AI model through agentgateway to write captions. It isn't stored or logged. Images are made on your phone."), the visitor takes a selfie with the front camera (a 3-second countdown, retake) or picks a photo from their library. The phone downscales it to at most 768 px on its long side and sends that JPEG to `POST /selfie/api/captions`.

What is real:

- **agentgateway in front of the model.** The arcade sends the photo (a data URL, held in memory for the call only, never written to disk or logged) to gpt-4.1-mini through its own route, `/selfie/v1/chat/completions` on the in-cluster hostname (`manifests/selfie.yaml`: HTTPRoute `openai-selfie` and policy `selfie-llm-guard`). The route adds the API key, runs the same prompt guard as the other games, allows 60k tokens a minute for the whole booth and 6 requests a minute per player (`x-selfie-player`), and only answers inside the cluster. The arcade itself allows 3 selfies a minute per player, 6 jobs at once, and photos up to 1.5 MB.
- **The captions.** The model returns JSON: a short note on what's visible and one caption per scene (at most 90 characters). The system prompt forbids guessing age, gender, race, body, health, religion or identity. The arcade validates the reply: an unusable caption becomes a stock one, and a blocked word, a 403 from the guard, a 429, a timeout or bad JSON falls back to the stock set, so the visitor always gets their pictures. The phone shows the model's real token use (input, cached, output) and its estimated cost, and the route's tokens count in `/api/tokens` under "Substrate Selfie".
- **A real caption agent.** A pool of 4 `selfiebot` sessions (harness and WorkerPool `selfie`, 3 workers) sleeps between jobs. Each job wakes one (a real `Resume`) while the model writes and snapshots it after (a real `Suspend`); the phone shows both timings. With no free agent the job still runs, and says so.

While the model works, the phone animates the photo travelling through agentgateway (prompt guard, rate limit, in-cluster checks lighting up) to the model and back, with a token meter that counts an estimate and then the real numbers. The phone then draws ten 1080x1350 scenes on canvas with the photo, the octopus and the kagent logo: the KubeCon + CloudNativeCon North America 2026 keynote screen in Salt Lake City (November 9-12, Salt Palace), a WANTED poster ("for running 10,000 agents on 3 nodes"), a trading card, a magazine cover, a Salt Lake City billboard, a selfie at the Great Salt Lake, kagent employee of the month, a Kubernetes pod ID badge, an 8-bit high-score screen and a Substrate snapshot polaroid. Each picture can be shared with the phone's share sheet (as a file, with the post text), downloaded, or posted to X, LinkedIn or Bluesky. There is no leaderboard; `/api/stats` has `selfie_taken`. The big screen at `/selfie/screen` shows a QR code, the count, tokens through agentgateway, the agent wake-up p50 and recent captions as text, never anyone's photo. Admin: `POST /admin/selfie/api/reset` clears the count and captions.
