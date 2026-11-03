# Grand Theft Agent: design

A multiplayer, top-down city shooter in the style of the original 1997 top-down GTA games. Players scan a QR code and drop into "Substrate City", where every wanted target is a real kagent agent running as an Agent Substrate actor in its own gVisor sandbox. Missions send players to hunt a target before a timer runs out. They then **suspend** it (stun shot), **extract** it to the snapshot vault, or **kill** it (delete). Rival crews can **resume** a stunned target before it's extracted. Every game action is the real Substrate operation, and the targets keep talking back the whole time.

Working title, my call: "Grand Theft Agent". It's a parody name. The art, sprites, fonts and sounds are original, with no Rockstar names, logos or assets. That's the same rule as Duck Hunt.

## Decisions (agreed 2026-11-02)

| Topic | Decision |
|-------|----------|
| World | **One shared city, separate missions.** Each solo player or squad starts its own mission at its own level. Its targets spawn in the shared city, where rivals can stun, spring or steal them. When the 24-step limit is full, or a mission's targets would take the city past 40 live targets, new missions wait in a queue at Dispatch ("waiting for a free safehouse") and launch oldest first. A player has at most one mission running or queued, and can't change squad while their squad's mission runs or waits. |
| Booth scope | **Levels 1–3 and the big screen** must be ready for the booth: Rookie, Booking, The Crew. Levels 4–6 and cars come after. |
| Winner | **Whoever freezes the most agents.** The leaderboard ranks players by takedowns (freezes they landed). Cash from extractions and missions only breaks ties. Squads see each member's count and a squad total. |
| Booth date | Not set yet |
| Status | **Playable, levels 1–3** (2026-11-02), live at `https://kubecon.agentpwned.ai/grand-theft-agent`; booth load measured below |

## Why

Duck Hunt shows that suspend and resume are fast. Agent Flow shows the components one request passes through. This game makes the **lifecycle** something you play with:

- a long-running agent can be frozen mid-task
- it's cheap while frozen
- it can be brought back with its memory intact

The game shows this at booth scale (tens of players, tens of agents) on one small host.

Success at the booth: a stranger scans the code, is shooting a target within 30 s, and after one mission can say "a suspended agent costs nothing and comes back where it left off".

## Goals, non-goals, anti-goals

**Goals**
- QR to playing in under 30 s, on a phone, with no install and no account (the arcade nickname).
- Solo or squad play (up to 4) in one shared city; rival crews compete for the same targets.
- Missions in levels, each teaching one Substrate idea.
- Targets are real agents that do real work and **respond at every level**: taunts, radio chatter, and reactions when hit or revived.
- The big screen shows the whole city plus live Substrate numbers. Phones show a follow-cam on the player plus controls.

**Non-goals**
- Persistent progress across redeploys (the same as the other games: state lives in memory).
- A physics engine, vehicles that matter, or a large map. A car can be scenery or a later stretch goal.
- Native apps, or anything that needs an install.

**Anti-goals** (a failure even if it works)
- **Gore or realistic violence.** It's a family-friendly booth. Shots are cartoon "suspend rays", and killed agents de-rez into pixels. "Kill" appears on screen as **DELETE**.
- **Fake mechanics.** If a target freezes, its session really was suspended. If it comes back, it was really resumed. No animation is shown without the real API call behind it.
- **Lag that makes it unplayable on venue Wi-Fi.** Movement must feel instant even when agent calls take seconds.
- **A token bill out of control.** The LLM is used for flavour and task steps, never per frame.

## Measured capacity on this host (2026-11-02)

One host (8 CPUs, 30 GiB), kind cluster with 3 nodes, gVisor workers. This is a throwaway load test: each agent did one real LLM turn and wrote a 20 MiB file, then everyone was suspended and resumed at the same moment.

| | 40 agents | 80 agents |
|---|---|---|
| Create session (16 at once) | 0.1–0.4 s | 0.2–0.4 s |
| One work turn, all at once | 9–11 s | 26–37 s (host CPU 98%) |
| Suspend, all at once | ~0.25 s each | ~0.5 s each |
| Resume, all at once | 4.4–4.7 s | 9.6–10.7 s (CPU 98%) |
| Extra host memory | ~0.6 GiB | ~3.9 GiB |
| Workspace file intact after resume | 40/40 | 76/80 (4 errors, cause not checked) |
| Node changed on resume | 0/40 | 0/80 |

Also known from Duck Hunt: Substrate snapshots an idle actor about 1 s after each reply on its own. A target between task steps is therefore already a snapshot. The stun shot is a `SuspendSession`, which **parks** it, so nothing can wake it until it is resumed.

**Capacity decisions (my call, from these numbers):**
- One worker pool `heist` with **40 workers**. At most **24 targets doing a task step at the same moment** (a server-side semaphore), which keeps steps around 10 s or less.
- The city can hold **any number of suspended targets**; they cost only snapshot storage.
- **Up to 48 players** in one city. Players are browser connections, not agents, so this cap is about screen readability and fairness, not the cluster.
- Never resume more than **8 targets at the same moment** (queue the rest), because a mass resume takes all 8 CPUs.

## Booth load, measured (2026-11-02)

`TestBoothLoad` (`games/heist/cluster_test.go`, build tag `cluster`) plays a busy booth through the real phone protocol: 24 players join with `/api/join` and open the game's WebSocket with their cookie, 8 solo and 4 squads of 4. Bots play Levels 1–3 in unlock order: they steer along the roads to the nearest hunter and shoot it, pick up parts, bank them in the Vault, build rigs, and on Levels 2–3 drag frozen targets into the Vault. A `?screen=1` spectator counts everything; token use comes from agentgateway's own metrics for the `/heist/v1` route, and op errors from the arcade's log. One 5-minute run:

| | Measured | Target |
|---|---|---|
| Missions | 79 won (12 at Level 1, 12 at Level 2, 55 at Level 3), none failed or escaped | |
| Freeze, third hit to frozen (includes waiting for the target's turn to end) | n=224, p50 69 ms, **p95 4.8 s** | p95 < 1 s: **missed** |
| Freeze, the suspend call alone | p50 28 ms, **p95 66 ms** | p95 < 1 s: met |
| Spring, asked to sprung (resume plus the sprung turn) | n=8, p50 3.4 s, **p95 8.7 s** | p95 < 6 s: **missed** |
| Spring, the resume alone | p50 195 ms, p95 622 ms | |
| Booking, in the Vault to booked (resume, booked turn, checkpoint, delete) | n=187, p50 2.6 s, p95 8.3 s | |
| Booking, the Substrate work alone | p50 501 ms, p95 884 ms | |
| Allies | 8 rigs built, spawn p50 1.6 s, p95 2.4 s; at most 2 alive at once | |
| Scheduler cap (peak) | 16 of 24 steps, 7 of 8 resumes, nothing queued, no queued missions | |
| Heist actors | 24–32 sessions, 1–8 awake | |
| agentgateway tokens (input + output) | **297k a minute on average, 331k in the busiest minute**; 1.46M input (54% cached), 46k output; 1,044 LLM requests, 32 answered 429 by the budget | < 300k a minute: **missed at peak** |
| Op errors | none (no slipped freezes, failed bookings or failed springs); 75 fallback lines for replies that failed output validation | none |
| Stuck missions (timer at 0, no outcome after 10 s) | none | none |

Why the misses:
- **Freeze, with the wait.** By design (since 2026-11-02) a freeze lands when the target's current turn ends, because kagent refuses a suspend mid-turn. A step turn takes 2–8 s, so a freeze that arrives mid-step waits for it; the suspend itself stays under 70 ms. The p50 of 69 ms shows most shots land between turns.
- **Spring.** It includes one full LLM turn (the sprung target's reaction), which takes 3–8 s with gpt-4.1-mini, sometimes with a validation fallback. Only 8 springs happened, so the p95 is the slowest one or two. The resume itself is 0.2–0.6 s.
- **Tokens.** The bots clear a mission every few seconds (a person takes far longer), and every new target starts its first step on arrival, so target churn drives token use. agentgateway enforced the budget: 32 requests got 429, and none of them surfaced as a game error (kagent's model client most likely retried them; not checked). At a real booth, with slower players, the use will be lower, but a full city can hit the budget; the per-step cadence (15–25 s) is the knob.

## The city (big screen and phones)

A cute top-down 3D city in Three.js. Every landmark is a real component; this matches Agent Flow's mapping so the two games tell one story.

| Landmark | Component | What happens there |
|----------|-----------|--------------------|
| Dispatch HQ (police station with a kagent logo) | kagent controller | Missions are issued here; players spawn here |
| Districts (each a city block) | Kubernetes nodes (`kagent-worker`, `kagent-worker2`) | A target is drawn in the district of the node that really holds its sandbox or snapshot |
| Safehouses | gVisor sandboxes (workers) | Lit windows = awake workers; the count matches `busyWorkers` |
| The Impound Vault | Snapshot Storage (rustfs) | Extraction point. Stunned targets dragged here are "booked" |
| Highway checkpoint at the city edge (agentgateway logo) | agentgateway / atenet-egress | Targets that try to reach a blocked site get stopped here, and their wanted level rises |
| Dispatch radio tower | The LLM route | Lights up when a target makes an LLM call |

Art direction (agreed 2026-11-02): a cute top-down 3D look in the spirit of escape-from-duckkov, with round chunky characters and warm lighting, set at night (neon, wet roads). Everything is our own code, with models built from Three.js primitives; that repo has no license, so nothing is copied from it. A chunky HTML HUD sits on top:
- wanted stars
- mission timer
- cash = score
- a minimap

The map (decided 2026-11-02): 3-tile-wide roads between 5×5 blocks on an 8-tile pitch, with roads on every edge, so 7 × 4 blocks on a 59 × 35 tile map. Dispatch takes the block nearest the centre (column 3, row 1) and the Impound Vault the bottom-right block. The server's `city.go` is the only copy of the layout; clients fetch it from `GET /grand-theft-agent/api/city` and draw whatever it says.

Three.js 0.170.0 is vendored as one file, so there is still no build step, like the rest of the arcade. The palette and fonts are in keeping with the arcade; there are no GTA assets.

Phones show a follow-cam on your character, with a virtual joystick (left thumb) and FIRE / ACTION buttons (right thumb). Laptops use the same on-screen controls with the mouse. There are no keyboard controls (agreed 2026-11-02). The big screen shows the whole city, the leaderboard, mission feed, Substrate stats, and the QR code.

## Mapping game actions to Substrate

| Game action | Real call | On screen |
|-------------|-----------|-----------|
| Target spawns ("a new job hits the street") | `CreateSession` on the `heister` Agent | Car drops the target off in the district of its node; **NEW** tag |
| Target works its heist | One A2A turn per step (see below) | Progress bar over its head; speech bubble with the real command output |
| Stun shot (3 hits) | `SuspendSession`, which lands when the target's current turn ends (a suspend mid-turn is refused) | Target freezes in ice/static once the suspend lands; label `suspended in N ms` (the suspend call alone) |
| Extraction (drag the stunned target into the Impound Vault) | `ResumeSession`, one "you got booked" turn, `Checkpoint` of that turn (a durable, named snapshot), then `DeleteSession` | Target is filed into the vault wall with its booked line; cash awarded |
| DELETE (finish off a stunned target instead) | `DeleteSession` | De-rez into pixels; fewer points than extraction, no evidence |
| Rival crew or the target's own crew revives it | `ResumeSession` | Ice shatters; **RESTORED** tag; it resumes its heist at the step it was on, and says so |
| Interrogate an extracted target (later level) | `Fork` from its checkpoint plus one prompt | A copy appears in the interview room and answers from memory |

Takedowns decide the winner (see Decisions). Cash, which breaks ties (my call): extraction 300, DELETE 100, stun assist 50, mission completed in time ×2, losing your extraction to a rival revive −100.

## The target agent (`heister`)

One kagent `Agent` on its own Harness and `heist` WorkerPool. Its model goes through its own agentgateway route (`/heist/v1`) with its own prompt guard and token budget, the same pattern as Agent Flow's `flow-model`.

**The heist (long-running work).** Each target gets a job (built: 3 fixed jobs of 4 bash steps for levels 1–3; later levels may use up to 8) (crack a safe = write and run a small script, count the loot = wc/sha256 a file, call the getaway driver = an allowed outbound fetch on later levels). The game server sends one step per turn, every 15–25 s, while the target isn't stunned. The work lands in `/data/workspace` and survives suspend and resume (verified in the load test). If the last step finishes before players stop it, the target **escapes**: the mission fails and its crew's wanted level goes up.

**Talking back.** Each step's reply carries a short line (≤ 60 characters) in structured output, like Duck Hunt's quack:

```yaml
type: object
required: [say, step_done]
properties:
  say:       { type: string, maxLength: 60 }   # speech bubble and phone radio chatter
  step_done: { type: boolean }
  loot:      { type: integer, minimum: 0 }     # optional flavour, shown in the bubble
```

After a resume, the first prompt is "You were frozen by the cops and just got sprung. React, then continue your heist." The reply has to show it remembers the step and the loot. That's the visible proof of restored memory.

**Fallbacks (built).** gpt-4.1-mini sometimes answers in prose or loops on tool calls, and kagent then fails the turn's output validation. The turn still ran, so the game doesn't resend it: a step counts with a fixed quip ("Shh... working on it."), a spring uses a fixed line ("I'm out! Back to the heist."), and a booking checkpoints that turn with "Booked. I'll be back.". In the booth load run about 7% of LLM requests ended in a fallback line.

**Movement is not the LLM.** Targets walk, run, flee and hide by server-side scripted behaviour (waypoints, flee from the nearest player within range). The LLM is never asked where to move.

## Levels and missions

Every level is a mission template: timer, target count, and rules. A mission is offered to a solo player or a squad from Dispatch.

| Level | Mission | Substrate idea it teaches |
|-------|---------|----------------------------|
| 1. Rookie | Freeze one target before its 4-step heist finishes (90 s) | Suspend: a frozen agent stops mid-task and costs nothing |
| 2. Booking | Freeze it and drag it to the Impound Vault (120 s) | Snapshots are durable: extraction is a checkpoint in Snapshot Storage |
| 3. The Crew | Three targets; un-stunned crew members revive frozen ones | Resume: it comes back exactly where it was, memory and files intact |
| 4. Rush Hour | A wave of 12 targets at once across districts | Density: many agents, few awake sandboxes; the big screen shows workers vs actors |
| 5. Getaway | A target tries to call its getaway driver (an outside host) | Egress: agentgateway blocks it at the checkpoint, and the wanted stars rise |
| 6. Interrogation | Fork an extracted target and ask who hired it | Checkpoints and forks: a copy answers from the original's memory |

**Players resume targets too (your brief: "suspend them and resume them").** ACTION on a frozen target that isn't yours to book is **Spring**, a player-triggered `ResumeSession`. It goes through the 8-at-once resume queue, with a 20 s cooldown per player. You'd spring a target when an Informant mission needs it awake to talk, or to snatch a rival's unbooked catch and stun it yourself (the rival loses 100). My call, to stop griefing: you can't spring a target that is being dragged.

**Task progress pauses while a step waits in the queue (my call).** A target only moves a step forward when a turn really completes. Mission timers are set from measured step times, so a busy cluster makes targets slower, not missions harder.

**Wanted stars belong to the mission (my call).** They start at 1. They go up when a target finishes a step, tries an outside host (level 5), or a rival springs one of your catches. Each star adds a crew agent who tries to revive frozen targets (via the same queue) and is a scoring multiplier on completion.

Rival crews: any player or squad can stun or extract any target. A rival's stunned target that isn't extracted within 30 s gets revived by its own crew if any crew member is still free, which pushes players to finish the job or team up.

## Hunters, loot and rigs (agreed 2026-11-02)

The targets fight back, and players loot hardware from them to run agents of their own. This makes Substrate capacity the thing you play for.

**What the user decided:** a 3D look (first a Matrix look, since replaced by the stylised night city below); collected parts let you **deploy your own agents**; and when hunters drop you, you **lose your unextracted loot**. Everything else in this section is my call, and the numbers are starting values to tune in play.

**Hunters.** Every Working target is also a hunter.
- It shoots the nearest player or ally within 5 tiles: 10 damage per hit, 1.2 s between shots. Frozen, dragged and booked targets don't shoot.
- Players have 100 health, and it refills at 5 per second after 4 s without being hit.
- At 0 health you go down. Your unextracted inventory drops as a bag where you fell, which anyone can pick up for 60 s. You respawn at Dispatch after 3 s with full health.
- Hunter movement and shooting are scripted on the server; the LLM never decides them.

**Loot.** Every confirmed freeze (takedown) drops one part at the target, and a DELETE drops one more. Drop chances:

| Part | Chance | Tiers |
|------|--------|-------|
| RAM stick | 50% | DDR1 40%, DDR2 25%, DDR3 18%, DDR4 12%, **DDR5 5%** (rarest) |
| CPU | 20% | |
| Hard drive | 20% | |
| Motherboard | 10% | |

- Parts glow by rarity (DDR5 is gold).
- You pick a part up by walking over it (0.8 tile).
- Your inventory has 8 slots; when it's full you can't pick anything up.
- Walking into the Impound Vault **banks** your whole inventory into your stash. Only the stash is safe, and it lasts until the next redeploy, like scores.

**Rigs and allies (deploy your own agents).**
- A rig takes one motherboard, one CPU, one RAM stick and one hard drive from your stash. **Build rig** on your phone spends them and deploys an **ally**.
- An ally is a real kagent session on its own Agent (`sidekick`, in the same `heist` worker pool), so it shows up in the Substrate numbers like any other agent.
- The ally follows you and shoots hunters within 6 tiles. Its hits count toward freezes, and **a freeze it lands is your takedown**.
- The RAM's tier sets the ally's level: DDR1 = 1 shot per second, up to DDR5 = 2.2 shots per second and +20% speed.
- An ally makes one LLM turn when deployed (a greeting line) and one when it is sprung. Otherwise it's scripted.
- Limits: 2 allies per player and 16 in the whole city. When the city is full, Build rig waits with "no free safehouse".
- Hunters target allies too. An ally at 0 health is **frozen**, which is a real suspend. You can spring it back with a short ACTION next to it, which is a real resume with its memory intact.
- Allies are deleted when your mission ends or you leave.

**Look: a stylised night city** (2026-11-02: the user found the all-green first build "maybe too much matrix looking", then, after playing on a phone, asked for "like a city", not Matrix buildings):
- towers (2.2–3.5), mid-rises (1.2–2.2) and low shops (0.6–0.9, with awnings) in natural facade colours (concrete, brick, sand, glass), set back on raised sidewalks with curbs
- windows lit warm and cool; the brightest panes are the awake workers
- rooftop clutter (AC units, water tanks, antennas), a few neon signs in cyan, magenta and amber, and street lights with warm pools of light on every block corner
- dark asphalt with yellow centre dashes, white edge lines and white zebra crossings at junctions; no digital rain
- green stays only in the HUD, at Dispatch and on the hunters' shades; hunters are dark figures in suits with a quiet slate outline
- players glow in their own colour, allies in their owner's colour, and parts glow by rarity (DDR5 gold)
- phones render without antialiasing, at a pixel ratio of at most 1.5 and with blob shadows instead of shadow maps

Everything is original: no film assets, names or logos.

## Players, squads and controls

- Join with the arcade nickname (existing `platform.Players` cookie), then either **Go solo** or **Join a squad** with a 4-letter code shown on a squadmate's phone. Squads share score and missions.
- Controls are on-screen only, on phones and laptops alike (no keyboard): a joystick to move, FIRE (auto-aims at the nearest target in range), and ACTION. A tap of ACTION grabs a frozen target to drag (your own mission's), springs a rival's frozen target (20 s cooldown) or springs your own frozen ally; holding it for 1 s DELETEs the frozen target. The STASH button opens your banked parts; BUILD RIG there spends a rig.
- Phones switch to low quality (pixel ratio 1.25, no shadows) by themselves when the first few seconds average over 33 ms a frame; MENU has a GRAPHICS switch, remembered on the phone. `?debug=1` shows an overlay with frame time, snapshot gaps, interpolation buffer, ping and draw calls.
- Players can't hurt players (my call: no PvP damage; rivalry is only over targets). Players never type free text to targets in levels 1–5. Level 6's interrogation question is a fixed menu, so no player text reaches the LLM.

## Architecture

```
phones ─┐
screen ─┼─► Cloudflare Tunnel ─► agentgateway Gateway ─► arcade backend (Go, games/heist)
admin  ─┘                                                  │  world tick (15 Hz), missions, scoring
                                                           │  target scheduler (≤24 steps, ≤8 resumes at once)
                                                           ▼
                                   kagent controller ─► Substrate actors on the `heist` pool (gVisor)
                                                           │
                       heister ModelConfig ─► agentgateway /heist/v1 (prompt guard, token budget) ─► OpenAI
```

- **Backend:** a new package `games/heist` in the existing arcade binary. It reuses `platform` (players, SSE hub, timings) and `internal/kagent`. The game world (positions, hits, timers) is authoritative on the server and in memory. kagent and Substrate are the source of truth for target state; the actor poller (as in Agent Flow) maps each target to its real node and state.
- **Realtime transport (my call): WebSocket**, one per client, at `/grand-theft-agent/ws`. Joystick input goes up at up to 20 Hz while a control is held; world snapshots come down at 15 Hz, and clients interpolate. The other games use SSE plus POSTs, but per-frame POSTs would also hit the front-door limit of 300 requests per minute per IP. A single WebSocket is one request. This adds one Go dependency (`github.com/coder/websocket`, pinned). The Cloudflare Tunnel passes WebSockets through.
- **Agent calls are asynchronous to the game loop.** The game never waits on an agent call inside a tick. A third hit marks the target busy (it stops shooting and can't be hit) and asks for the freeze. kagent refuses a suspend while a turn is in flight, so the freeze retries until that turn ends and then lands; the target shows Frozen only then. If the target's final step completes while its freeze is pending, the freeze wins and it doesn't escape. If the freeze fails, the target thaws with an "it slipped away" bubble.
- **Extraction (booking).** A suspend writes a new snapshot, so the turn before the freeze can no longer be checkpointed ("snapshot changed"). Booking therefore resumes the target, asks it for one booked line, checkpoints that fresh turn and deletes the live session. If any part fails after the resume, the target is suspended again and the dragger can retry.
- **Pages:** `/grand-theft-agent` (phone, `?debug=1` for the timing overlay), `/grand-theft-agent/screen` (big screen with QR), `GET /grand-theft-agent/api/city` (the map), plus admin under `/admin/grand-theft-agent/api/*` (reset, state, pause), hidden from the tunnel like the other admin routes.
- **Manifests:** `manifests/heist-agent.yaml` (WorkerPool `heist`, 40 replicas; Harness with `s3://ate-snapshots/heist/`; ModelConfig; Agent `heister` with `outputSchema`) and `manifests/heist-policies.yaml` (HTTPRoute `/heist/v1`, prompt guard, token and request limits, in-cluster-only authorization). An EgressPolicy `getaway` allows one harmless host for level 5's "allowed" contrast; every other host is blocked.

## Big-screen stats (subtle, as on Agent Flow)

- Targets on the street vs awake sandboxes vs workers ("40 workers, 63 targets, 9 awake")
- Live suspend, resume and checkpoint times (atelet metrics, p50)
- Snapshots in the Impound Vault
- An estimate of what the same targets would cost as always-on pods
- A compute heartbeat strip that spikes on every wake

## Safety and cost

- Prompt guard and token budget on `/heist/v1`. The per-step prompt is small and fixed, with `gpt-4.1-mini`. Target budget: ≤ 24 steps in flight, one step per target every 15–25 s. Measured at booth load: about 205 LLM requests and 297k tokens a minute on average, so the 300k a minute budget is the real ceiling (see the measured results).
- A mission timer deletes the targets 10 s after their mission ends, so snapshots don't pile up; booked checkpoints stay until the admin reset, which deletes everything.
- **Startup sweep (built).** Game state lives in memory, so a redeploy used to orphan the sessions of running missions. On start the game lists every `heister` and `sidekick` session and deletes them all (none can be its own yet), and refuses missions and rigs until the list is in.
- Rate limits: the front door keys on `cf-connecting-ip`, and venue Wi-Fi often puts everyone behind one IP. The WebSocket keeps the request count low, but the existing 300 per minute per IP is still shared by every game on that network. That needs a look before the booth (see the open items).

## Open items for the builder

- **Closed: `Checkpoint` of a suspended session.** It fails (`FailedPrecondition ... RUNTIME_STATE_SUSPENDED`), and after a suspend the pre-freeze turn can't be checkpointed either ("SnapshotTagFailed: snapshot changed"; a suspend writes a new snapshot). Booking resumes the target, prompts it once for its booked line and checkpoints that turn, then deletes it (see Architecture). Right after a turn, `Suspend`, `Resume`+`Prompt` and `Delete` can answer `FailedPrecondition` ("not yet settled") or `Aborted` (a turn in flight); the adapter retries those with backoff capped at 500 ms. The `heister` outputSchema can't use `maxLength` or `minimum` (the portable output profile rejects them), so the 60 character limit lives in the prompt and the game clips `say`.
- **Closed: token use at peak.** Measured at booth load: 297k tokens a minute on average and 331k in the busiest minute, against the 300k budget, which agentgateway enforced (see Booth load, measured). If a real booth hits the budget often, slow the step cadence.
- **Closed: node hops on resume.** None seen; targets are drawn in the district of their real node, with no teleporting.
- The 4 errors at 80 agents in the first capacity test weren't diagnosed. The game stays at 40 workers, and the booth load run at 24–32 actors had no errors.
- Decide whether the front-door limit should key on the player cookie instead of the IP for venue Wi-Fi.
- Car driving (GTA flavour) is a stretch goal after levels 1–3 play well.
