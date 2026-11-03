# Grand Theft Agent: design

A multiplayer, top-down city shooter in the style of the original 1997 top-down GTA games. Players scan a QR code and drop into "Substrate City", where every wanted target is a real kagent agent running as an Agent Substrate actor in its own gVisor sandbox. Missions send players to hunt a target before a timer runs out. They then **suspend** it (stun shot), **extract** it to the snapshot vault, or **kill** it (delete). Rival crews can **resume** a stunned target before it's extracted. Every game action is the real Substrate operation, and the targets keep talking back the whole time.

Working title, my call: "Grand Theft Agent". It's a parody name. The art, sprites, fonts and sounds are original, with no Rockstar names, logos or assets. That's the same rule as Duck Hunt.

## Decisions (agreed 2026-11-02)

| Topic | Decision |
|-------|----------|
| World | **One shared city, separate missions.** Each solo player or squad starts its own mission at its own level. Its targets spawn in the shared city, where rivals can stun, spring or steal them. When the 24-step limit is full, new missions wait in a queue at Dispatch ("waiting for a free safehouse"). |
| Booth scope | **Levels 1–3 and the big screen** must be ready for the booth: Rookie, Booking, The Crew. Levels 4–6 and cars come after. |
| Winner | **Whoever freezes the most agents.** The leaderboard ranks players by takedowns (freezes they landed). Cash from extractions and missions only breaks ties. Squads see each member's count and a squad total. |
| Booth date | Not set yet |

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
| Stun shot (3 hits) | `SuspendSession` | Target freezes in ice/static; label `suspended in N ms` (atelet timing) |
| Extraction (carry the stunned target to the Impound Vault) | `Checkpoint` of the session (a durable, named snapshot), then the live session is deleted | Target is filed into the vault wall; cash awarded |
| DELETE (finish off a stunned target instead) | `DeleteSession` | De-rez into pixels; fewer points than extraction, no evidence |
| Rival crew or the target's own crew revives it | `ResumeSession` | Ice shatters; **RESTORED** tag; it resumes its heist at the step it was on, and says so |
| Interrogate an extracted target (later level) | `Fork` from its checkpoint plus one prompt | A copy appears in the interview room and answers from memory |

Takedowns decide the winner (see Decisions). Cash, which breaks ties (my call): extraction 300, DELETE 100, stun assist 50, mission completed in time ×2, losing your extraction to a rival revive −100.

## The target agent (`heister`)

One kagent `Agent` on its own Harness and `heist` WorkerPool. Its model goes through its own agentgateway route (`/heist/v1`) with its own prompt guard and token budget, the same pattern as Agent Flow's `flow-model`.

**The heist (long-running work).** Each target gets a job of 4–8 steps (crack a safe = write and run a small script, count the loot = wc/sha256 a file, call the getaway driver = an allowed outbound fetch on later levels). The game server sends one step per turn, every 15–25 s, while the target isn't stunned. The work lands in `/data/workspace` and survives suspend and resume (verified in the load test). If the last step finishes before players stop it, the target **escapes**: the mission fails and its crew's wanted level goes up.

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

**What the user decided:** a 3D **Matrix look**; collected parts let you **deploy your own agents**; and when hunters drop you, you **lose your unextracted loot**. Everything else in this section is my call, and the numbers are starting values to tune in play.

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
- Controls: a joystick to move, FIRE (auto-aims at the nearest target in range), and ACTION (grab or drag a stunned target, or interrogate at the vault).
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
- **Realtime transport (my call): WebSocket**, one per client, at `/grand-theft-agent/ws`. Joystick input goes up at ~15 Hz; world snapshots come down at 15 Hz, and clients interpolate. The other games use SSE plus POSTs, but per-frame POSTs would also hit the front-door limit of 300 requests per minute per IP. A single WebSocket is one request. This adds one Go dependency (`github.com/coder/websocket`, pinned). The Cloudflare Tunnel passes WebSockets through.
- **Agent calls are asynchronous to the game loop.** A shot that triggers `SuspendSession` freezes the sprite at once (the animation starts on the hit). The label shows the real time when the call returns. If the call fails, the target thaws, with an "it slipped away" bubble. The game never waits on an agent call inside a tick.
- **Pages:** `/grand-theft-agent` (phone), `/grand-theft-agent/screen` (big screen with QR), plus admin under `/admin/grand-theft-agent/api/*` (reset, state, pause spawns, set level), hidden from the tunnel like the other admin routes.
- **Manifests:** `manifests/heist-agent.yaml` (WorkerPool `heist`, 40 replicas; Harness with `s3://ate-snapshots/heist/`; ModelConfig; Agent `heister` with `outputSchema`) and `manifests/heist-policies.yaml` (HTTPRoute `/heist/v1`, prompt guard, token and request limits, in-cluster-only authorization). An EgressPolicy `getaway` allows one harmless host for level 5's "allowed" contrast; every other host is blocked.

## Big-screen stats (subtle, as on Agent Flow)

- Targets on the street vs awake sandboxes vs workers ("40 workers, 63 targets, 9 awake")
- Live suspend, resume and checkpoint times (atelet metrics, p50)
- Snapshots in the Impound Vault
- An estimate of what the same targets would cost as always-on pods
- A compute heartbeat strip that spikes on every wake

## Safety and cost

- Prompt guard and token budget on `/heist/v1`. The per-step prompt is small and fixed, with `gpt-4.1-mini`. Target budget: ≤ 24 steps in flight, one step per target every 15–25 s, which is roughly 60–100 turns per minute at peak. This needs measuring in the first build.
- A mission timer and an idle sweeper delete targets nobody is hunting, so snapshots don't pile up. Admin reset deletes everything.
- Rate limits: the front door keys on `cf-connecting-ip`, and venue Wi-Fi often puts everyone behind one IP. The WebSocket keeps the request count low, but the existing 300 per minute per IP is still shared by every game on that network. That needs a look before the booth (see the open items).

## Open items for the builder

- **Checked: `Checkpoint` does not work on a suspended session.** kagent answers `FailedPrecondition: cannot checkpoint in state RUNTIME_STATE_SUSPENDED with operation RUNTIME_OPERATION_NONE: resource conflict`, and `Kagent.Checkpoint` retried that for its full 2 minutes without success. **Extraction mode: `resume-first`**: resume the target off-screen through the queue (measured about 0.2 s), checkpoint it, then delete the live session, so level 6 still has a checkpoint to fork from. Don't call `Checkpoint` on a suspended session: it burns the 2 minute retry. Also: right after a turn, `Suspend`, `Resume`+`Prompt` and `Delete` can answer `FailedPrecondition: runtime cleanup or lifecycle work is not yet settled`; retry for about a second (suspend succeeded on the 2nd try, 0.5 s). The `heister` outputSchema can't use `maxLength` or `minimum` (the portable output profile rejects them), so the 60 character limit lives in the prompt and the game must clip `say`.
- The 4 errors at 80 agents in the load test weren't diagnosed. Rerun at 40 with error logging before sizing for the booth.
- The token use per minute at peak is an estimate; measure it in the first playable build and tune the step interval.
- Node hops on resume never happened in the load test. Draw the district from the real node, and don't promise teleporting.
- Decide whether the front-door limit should key on the player cookie instead of the IP for venue Wi-Fi.
- Car driving (GTA flavour) is a stretch goal after levels 1–3 play well.
