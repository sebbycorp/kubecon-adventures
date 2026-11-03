# Grand Theft Agent (levels 1–3 + big screen) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a playable multiplayer top-down city shooter to the arcade. Players freeze, extract and spring real kagent agents on Agent Substrate across missions at levels 1–3, and a big screen shows the whole city.

**Architecture:** A new package, `arcade/games/heist`. It has three layers:

- **Pure world and mission logic.** No I/O and tested with a fake clock.
- **A scheduler.** It runs agent operations through an `Ops` interface and caps how many run at once. It's tested with a fake `Ops`, and a kagent adapter implements `Ops` for real.
- **A 15 Hz game loop.** It talks to phones and the big screen over one WebSocket each.

The pages are plain HTML/canvas/JS under `arcade/web/grand-theft-agent`, like the other games.

**Tech Stack:** Go 1.27.3 (run Go as `mise exec go@1.27.3 -- go …`), `github.com/coder/websocket v1.8.15`, the existing `internal/kagent` and `internal/platform`, kagent `v1alpha3` Agent/Harness/ModelConfig, Substrate `WorkerPool`, agentgateway `AgentgatewayPolicy`, Three.js `0.170.0` (vendored ES module, no build step).

**Spec:** `arcade/docs/grand-theft-agent.md` (read it first; this plan argues from it)

## Global Constraints

- **Scope:** only levels 1 Rookie, 2 Booking and 3 The Crew, plus the big screen. No levels 4–6 and no cars.
- **Real operations:** every freeze, extract, spring and delete is the real kagent/Substrate call. No animation is shown without the call behind it. If a call fails, the target thaws and shows "it slipped away".
- **Agent calls never block a tick.** Calls run asynchronously, and the world only changes when their results come back.
- **Capacity limits:** WorkerPool `heist` has 40 workers. At most **24** heist steps run at once and at most **8** resumes run at once, with the rest queued. At most **48** players. Squads have up to **4** players and join with a **4-letter** code.
- **Tick and transport:** world tick and snapshots at **15 Hz**; one WebSocket per client at `/grand-theft-agent/ws`.
- **Game numbers:**
  - 3 hits stun a target
  - crew revives an unbooked frozen target after **30 s**
  - Spring cooldown **20 s**
  - steps every **15–25 s**
- **Winner:** the player who froze the most targets (**takedowns**). The freeze is credited to whoever lands the shot that triggers it, and is counted once `Resolve(freeze ok)` confirms it. Cash breaks ties. Squads see their members' individual counts plus a squad total.
- **Cash (tie-break):** extraction **300**, DELETE **100**, stun assist **50**, completed in time **×2**, rival springs your catch **−100**.
- **Mission timers:** Level 1 is 1 target, 4 steps, **90 s**. Level 2 is 1 target, 4 steps, **120 s**, and must be booked in the vault. Level 3 is 3 targets, 4 steps each, **180 s** (my call; the spec leaves it open).
- **Target speech:** `say` ≤ **60** characters. Model `gpt-4.1-mini` through agentgateway route `/heist/v1`.
- **Family-friendly:** no gore. The kill action is labelled **DELETE** and the target de-rezzes into pixels. No player-vs-player damage. No player free text reaches the LLM.
- **Art:** original art only. No Rockstar or GTA names, logos, fonts or assets apart from the parody title "Grand Theft Agent".
- **Hunters, loot and rigs** (spec section "Hunters, loot and rigs"; all values copied from it):
  - hunters shoot within **5** tiles for **10** damage every **1.2 s**
  - players have **100** health, regenerating **5/s** after **4 s** without a hit; when down, they respawn at Dispatch after **3 s**, and their inventory drops as a bag lasting **60 s**
  - a confirmed freeze drops 1 part and a DELETE drops 1 more: RAM 50% (DDR1 40 / DDR2 25 / DDR3 18 / DDR4 12 / DDR5 5%), CPU 20%, drive 20%, motherboard 10%
  - pickup radius **0.8**; inventory **8** slots; entering the Vault banks it into the stash
  - a rig = board + CPU + RAM + drive and deploys an ally: a real session on Agent `sidekick`, range **6**, fire rate DDR1 **1.0/s** to DDR5 **2.2/s** (linear), DDR5 **+20%** speed
  - at most **2** allies per player and **16** in the city
  - an ally's freeze is the owner's takedown; an ally at 0 health is frozen (real suspend) and can be sprung by its owner (real resume)
- **Matrix look** (superseded 2026-11-02 by the user's decision: a normal night city, no Matrix buildings; see the design doc's "Look" section)**:** the 3D city in black and green, with digital rain on the buildings, wireframe glow, dark suited hunters with green outlines, and glowing parts (DDR5 gold). Original assets only.
- **Admin routes** live under `/admin/grand-theft-agent/…`; the tunnel already blocks `^/admin`.
- **Images and dependencies:** pin image versions. New dependencies are limited to `github.com/coder/websocket v1.8.15` (Go) and Three.js `0.170.0` vendored at `web/grand-theft-agent/vendor/three.module.min.js` (browser).
- **Controls:** on-screen joystick + FIRE + ACTION on every device (touch or mouse pointer). **No keyboard controls** (user decision).
- **Look:** cute top-down 3D in the style of escape-from-duckkov (round characters, warm lighting, here at night with neon), all original code and models built from Three.js primitives. That repo has no license: copy nothing from it.
- **Secrets:** never print, cat or commit secrets.

## Review Focus

1. **A phone drops mid-drag or mid-mission** (venue Wi-Fi). The dragged target is released where it stands, still frozen. The squad's mission keeps running. A reconnect with the same cookie gets the same avatar back. Test in Task 6.
2. **A kagent call fails or times out** (FailedPrecondition, an unavailable worker, a 429 from agentgateway). The target thaws or stays put with "it slipped away". The mission never gets stuck waiting. Tests in Tasks 4 and 7.
3. **Two players act on the same target in the same tick** (both drag, or one springs while the other books). Exactly one wins, and no second Resume or Delete is issued for that session. Test in Task 3.
4. **The cluster is saturated** (24 steps in flight, 8 resumes queued). New missions wait at Dispatch, and targets' progress pauses instead of jumping forward. Mission timers still count down. Test in Task 5.
5. **Admin reset or a redeploy while targets are frozen.** Reset deletes every session the game created and leaves no WebSocket or goroutine behind. Test in Task 6.

---

## File structure

| File | Responsibility |
|------|----------------|
| `games/heist/city.go` | Tile map, districts (node → rect), vault and Dispatch zones, walkability, movement with collision |
| `games/heist/world.go` | Entities (avatars, targets), shots and hits, stun, drag, vault detection, target flee AI |
| `games/heist/mission.go` | Level templates, mission state machine, step pacing, escape, wanted stars, crew revive, scoring, squads |
| `games/heist/ops.go` | `Ops` interface, `Scheduler` (24-step and 8-resume caps, async results) |
| `games/heist/agentops.go` | `kagentOps`, which implements `Ops` with `internal/kagent`, including prompts and output parsing |
| `games/heist/game.go` | `Game`: owns world, missions and scheduler, runs the 15 Hz loop, handles inputs and snapshots |
| `games/heist/ws.go` | WebSocket endpoint: input decode, snapshot fan-out, reconnect |
| `games/heist/routes.go` | Pages, static files, WebSocket, admin |
| `games/heist/*_test.go` | Unit tests per file; `cluster_test.go` (build tag `cluster`) for live checks |
| `web/grand-theft-agent/city.js` | Shared Three.js renderer (city, characters, effects) plus the HTML HUD overlay |
| `web/grand-theft-agent/vendor/three.module.min.js` | Vendored Three.js 0.170.0 |
| `web/grand-theft-agent/net.js` | WebSocket client, interpolation, reconnect |
| `web/grand-theft-agent/phone.html` | Phone: join or squad, follow-cam, joystick, FIRE and ACTION |
| `web/grand-theft-agent/screen.html` | Big screen: whole city, leaderboard, mission feed, Substrate stats, QR, heartbeat |
| `manifests/heist-agent.yaml`, `manifests/heist-policies.yaml` | WorkerPool, Harness, ModelConfig, Agent, plus the LLM route and its policies |
| `main.go`, `deploy.sh`, `README.md`, `web/index.html` | Wiring, deploy, docs, arcade front-page entry |

---

### Task 1: Cluster resources and the extraction check

**Files:**
- Create: `manifests/heist-agent.yaml`, `manifests/heist-policies.yaml`, `games/heist/cluster_test.go`
- Modify: `deploy.sh` (apply both manifests next to the flow ones; wait for the `heister` Agent to be Ready), `docs/grand-theft-agent.md` (record the result of the checkpoint check)

**Interfaces:**
- Produces:
  - Agent `heister` in namespace `kagent`.
  - Model route `http://arcade.agentgateway-system.svc.cluster.local/heist/v1`.
  - A recorded answer to "can a SUSPENDED session be checkpointed?", which Task 7 uses.

- [ ] **Step 1: Write the manifests.** Copy the pattern of `manifests/flow-agent.yaml` and `manifests/flow-policies.yaml`.
  - WorkerPool `heist`: `replicas: 40`, worker image `ghcr.io/kagent-dev/substrate/ateworker-gvisor:v0.6.0-alpha3`, labels `kagent.dev/worker-pool-name: heist` and `kagent.dev/worker-pool-namespace: kagent`.
  - Harness `heist`: snapshots to `s3://ate-snapshots/heist/`.
  - ModelConfig `heist-model`: `gpt-4.1-mini`, secret `duck-llm`, baseUrl `/heist/v1`.
  - Agent `heister`: no `egressPolicyRefs`, and an `outputSchema` of `{say: string ≤60, step_done: boolean, loot: integer ≥0}` with `say` and `step_done` required. The system prompt says:
    - you are a cartoon heist crook in Substrate City
    - run the bash command each step gives you, in `/data/workspace`
    - reply only with the schema
    - stay family-friendly
    - never print keys
  - HTTPRoute `openai-heist`: `/heist/v1/chat/completions` rewritten to `/v1/chat/completions`, hostname in-cluster only.
  - AgentgatewayPolicy `heist-llm-guard`: the same prompt-guard regexes as `flow-llm-guard`; local rate limits `tokens: 300000` and `requests: 400` per minute.
  - Add `openai-heist` to the `targetRefs` of `llm-in-cluster-only` in `flow-policies.yaml`.

- [ ] **Step 2: Apply and wait.** Run: `./deploy.sh`. Expected: it ends with the Agent ready. `kubectl --context kind-kagent -n kagent get workerpool heist` shows `READY 40`.

- [ ] **Step 3: Write `TestCheckpointSuspended`** in `cluster_test.go` (`//go:build cluster`). It uses `kagent.NewKagent(os.Getenv("KAGENT_URL"), "heist-test", "kagent", "heister")` and does:
  1. `CreateSession`
  2. one `Prompt` running `echo loot > /data/workspace/loot.txt`
  3. `Suspend`
  4. `Checkpoint(ctx, sid, string(reply.Task.ID))` and `t.Logf` the result or the error, without failing on a checkpoint error
  5. then `Resume`, a `Prompt` running `cat /data/workspace/loot.txt` that must contain `loot`, and `Delete`

  Run: `kubectl --context kind-kagent -n kagent port-forward svc/kagent-controller 18083:8083` in the background, then `KAGENT_URL=http://localhost:18083 mise exec go@1.27.3 -- go test -tags cluster -run TestCheckpointSuspended -v ./games/heist/`. Expected: PASS. The log line states whether checkpoint-while-suspended worked.

- [ ] **Step 4: Record the result.** In the open items of `docs/grand-theft-agent.md`, replace the checkpoint bullet with the measured answer and the extraction path Task 7 will use:
  - `direct`: checkpoint while suspended
  - `resume-first`: resume, checkpoint, delete

- [ ] **Step 5: Commit.**
```bash
git add manifests/heist-*.yaml manifests/flow-policies.yaml deploy.sh games/heist/cluster_test.go docs/grand-theft-agent.md
git commit -m "Grand Theft Agent: heist worker pool, agent, LLM route and checkpoint check"
```

### Task 2: City map and movement

**Files:**
- Create: `games/heist/city.go`, `games/heist/city_test.go`

**Interfaces:**
- Produces:
  - `type Vec struct{ X, Y float64 }` (tile units)
  - `type Rect struct{ X, Y, W, H float64 }` with `Contains(Vec) bool`
  - `type City struct { W, H int; Tiles []byte; Districts map[string]Rect; Vault, Dispatch Rect }`
  - `func NewCity(nodes []string) *City`
  - `func (c *City) Walkable(p Vec) bool`
  - `func (c *City) Move(from, dir Vec, speed, dt float64) Vec`
  - `func (c *City) DistrictOf(node string) Rect`
  - `func (c *City) RandomStreet(r Rect, rnd *rand.Rand) Vec`
  - Tile bytes are `'.'` road, `'#'` building, `'V'` vault, `'D'` Dispatch.

- [ ] **Step 1: Write failing tests:**
  - `TestCityDistrictsSplitByNode`: `NewCity([]string{"kagent-worker","kagent-worker2"})` gives two districts that don't overlap and cover the playable width. `DistrictOf("unknown")` returns the centre district (the Dispatch block).
  - `TestMoveStopsAtBuildings`: moving into a `'#'` tile leaves the position outside it. Sliding along a wall keeps the parallel component.
  - `TestMoveNormalizesDiagonal`: dir `{1,1}` at speed 4 for 1 s moves 4 tiles, not 5.66.
  - `TestVaultAndDispatchReachable`: a BFS over walkable tiles from Dispatch reaches the Vault.
- [ ] **Step 2: Run** `mise exec go@1.27.3 -- go test ./games/heist/ -run City -v`. Expected: FAIL, because `NewCity` is undefined.
- [ ] **Step 3: Implement `city.go`.**
  - The map is a fixed hand-written 48×32 layout stored as a `[]string` constant: a road grid with a 6-tile block pitch, the Vault in the bottom-right and Dispatch in the centre.
  - Districts split the width evenly across the nodes, sorted by name.
  - Collision is axis-separated against a 0.3-tile radius.
- [ ] **Step 4: Run** the same command. Expected: PASS.
- [ ] **Step 5: Commit** `git add games/heist/city*.go && git commit -m "Grand Theft Agent: city map, districts and movement"`

### Task 3: World — avatars, targets, shots, stun, drag

**Files:**
- Create: `games/heist/world.go`, `games/heist/world_test.go`

**Interfaces:**
- Consumes: Task 2's `City`, `Vec`, `Rect`
- Produces:
  - `type TargetState string` with the values `Working`, `Frozen`, `Dragged`, `Booked`, `Deleted`, `Escaped`, `Thawing`
  - `type Avatar struct { PlayerID, Nick, Color, Squad string; Pos, Facing Vec; Dragging string; FireCD, SpringCD float64; Online bool }`
  - `type Target struct { ID, SessionID, MissionID, Name, Node, Say string; Pos Vec; State TargetState; Hits int; HitBy map[string]bool; FrozenBy string; Step, Steps int; FrozenAt float64; Busy bool; New, Restored bool }`
  - `type World struct { City *City; Avatars map[string]*Avatar; Targets map[string]*Target; Now float64; Events []Event }`
  - `type Event struct { Kind, Target, Player, Text string; Ms int64 }`
  - Methods:
    - `func NewWorld(c *City) *World`
    - `func (w *World) Input(playerID string, in Input)`
    - `func (w *World) Tick(dt float64) []Intent`
  - `type Input struct { Move Vec; Fire, Action bool }`
  - `type Intent struct { Kind, Target, Player string }`
    - `Kind` is one of `freeze`, `extract`, `delete`, `spring`.
    - The world never calls kagent. It returns intents, which the Game passes to the scheduler (Task 4).
  - `func (w *World) Resolve(target string, kind string, ok bool, ms int64)`: the Game calls this when an op finishes.

- [ ] **Step 1: Write failing tests:**
  - `TestThreeHitsFreeze`: a target in range (≤ 6 tiles, auto-aim at the nearest Working target) gets hit three times with `FireCD` 0.25 s. The third hit returns exactly one `Intent{freeze}` and sets `Busy`. The state stays Working until `Resolve(..., "freeze", true, 31)`, which sets `Frozen`, records `FrozenBy` = the player whose shot triggered it, and emits `Event{Kind:"frozen", Player:<that player>, Ms:31}`. Every shot that hits once is in `HitBy`; this is the basis for assists.
  - `TestFreezeFailureThaws`: `Resolve(..., "freeze", false, 0)` resets `Hits` to 0 and emits `Event{Kind:"slipped"}`.
  - `TestDragAndBook`: ACTION next to a Frozen target sets `Dragged` and `Avatar.Dragging`. The target follows the avatar at ¾ speed. Entering `City.Vault` returns one `Intent{extract}`. `Resolve(extract ok)` sets `Booked`.
  - `TestDoubleActionOneWinner` (Review Focus 3): two avatars press ACTION on the same Frozen target in one tick. Exactly one gets `Dragging`. A spring and a drag in the same tick also give one winner, and the drag wins. A target with `Busy` set never yields a second intent.
  - `TestDisconnectDropsDrag` (Review Focus 1): `Avatar.Online=false` releases the drag. The target goes back to `Frozen` at its current position.
  - `TestSpringCooldown`: ACTION on a Frozen target that isn't being dragged, while not in your own squad's mission, returns `Intent{spring}` and sets `SpringCD=20`. A second spring within 20 s returns nothing.
  - `TestTargetsFlee`: a Working target within 5 tiles of an avatar moves away from it, and never into buildings.
- [ ] **Step 2: Run** `go test ./games/heist/ -run 'Hit|Freeze|Drag|Double|Disconnect|Spring|Flee' -v`. Expected: FAIL.
- [ ] **Step 3: Implement `world.go`.**
  - Fixed numbers: avatar speed 5 tiles/s, target speed 3.5 tiles/s, fire range 6, ACTION range 1.2.
  - DELETE is ACTION held for 1 s on a Frozen target that you aren't dragging. It emits `Intent{delete}`.
  - Iterate players in sorted ID order so the same-tick winner is deterministic.
- [ ] **Step 4: Run** the same command. Expected: PASS.
- [ ] **Step 5: Commit** `git add games/heist/world*.go && git commit -m "Grand Theft Agent: world, shots, freeze, drag and spring"`

### Task 4: Scheduler with capacity caps

**Files:**
- Create: `games/heist/ops.go`, `games/heist/ops_test.go`

**Interfaces:**
- Produces:
  - `type Ops interface`, with these methods:
    - `Spawn(ctx context.Context, name string) (sessionID, node string, err error)`
    - `Step(ctx context.Context, sessionID string, step, steps int) (StepResult, error)`
    - `Freeze(ctx context.Context, sessionID string) (ms int64, err error)`
    - `Spring(ctx context.Context, sessionID string) (node string, say string, ms int64, err error)`
    - `Extract(ctx context.Context, sessionID string) (ms int64, err error)`
    - `Delete(ctx context.Context, sessionID string) error`
  - `type StepResult struct { Say string; Done bool; Loot int; Node string; Tokens int64 }`
  - `type Result struct { Kind, Target string; OK bool; Ms int64; Step StepResult; Spawned string; Node string; Err string }`
  - `type Scheduler struct` with these methods:
    - `func NewScheduler(ops Ops, maxSteps, maxResumes int) *Scheduler`
    - `func (s *Scheduler) Submit(kind, target, sessionID string, step, steps int) bool`: returns false if `target` already has an op in flight.
    - `func (s *Scheduler) Results() <-chan Result`
    - `func (s *Scheduler) InFlight() (steps, resumes, queued int)`
    - `func (s *Scheduler) Close()`
  - Each op runs with a 45 s context timeout.
  - `spring` and `extract` use the resume slots (extract only in the `resume-first` mode).
  - `freeze`, `delete` and `spawn` are never capped.

- [ ] **Step 1: Write failing tests** with a `fakeOps` that blocks on a per-call channel:
  - `TestStepCap`: Submit 30 steps; `InFlight` reports 24 running and 6 queued. Releasing one starts the next.
  - `TestResumeCap`: 10 springs give 8 running and 2 queued.
  - `TestOneOpPerTarget`: a second Submit for a busy target returns false.
  - `TestTimeoutReturnsFailure` (Review Focus 2): a fake that never returns gives `Result{OK:false}` after the timeout (inject a 50 ms timeout through an unexported field).
  - `TestCloseStopsWorkers`: after `Close`, `runtime.NumGoroutine()` returns to its baseline within 1 s.
- [ ] **Step 2: Run** `go test ./games/heist/ -run 'Cap|OneOp|Timeout|Close' -v`. Expected: FAIL.
- [ ] **Step 3: Implement `ops.go`** with two buffered semaphores and a FIFO queue per class. Results go out on one buffered channel (size 256).
- [ ] **Step 4: Run** the same command. Expected: PASS.
- [ ] **Step 5: Commit** `git add games/heist/ops*.go && git commit -m "Grand Theft Agent: scheduler with step and resume caps"`

### Task 5: Missions, squads, scoring

**Files:**
- Create: `games/heist/mission.go`, `games/heist/mission_test.go`

**Interfaces:**
- Consumes: `World`, `Target`, `Intent`, `Result`
- Produces:
  - `type Level struct { N int; Name string; Targets, Steps int; Seconds float64; MustBook bool }`
  - `var Levels = []Level{{1,"Rookie",1,4,90,false},{2,"Booking",1,4,120,true},{3,"The Crew",3,4,180,true}}`
  - `type Mission struct { ID, Squad string; Level Level; Targets []string; Started, Ends float64; Stars int; Cash int; Outcome string; Queued bool }`
    - `Outcome` is one of `""`, `"won"`, `"failed"` or `"escaped"`.
  - `type Squads struct`, with these methods:
    - `func (q *Squads) Solo(playerID string) string`
    - `func (q *Squads) Create(playerID string) (code string)`
    - `func (q *Squads) Join(playerID, code string) error`: the error covers a full squad (4 players) or an unknown code.
    - `func (q *Squads) Of(playerID string) string`
  - `type Missions struct`, with these methods:
    - `func (m *Missions) Start(squad string, level int, w *World, s *Scheduler) (*Mission, error)`
    - `func (m *Missions) Tick(w *World, s *Scheduler)`
    - `func (m *Missions) OnResult(r Result, w *World)`
    - `func (m *Missions) Leaderboard() []ScoreRow`
  - `type ScoreRow struct { PlayerID, Nick, Squad string; Takedowns, Cash, Level int }`
  - `func (m *Missions) SquadTotals() map[string]int`: takedowns per squad

- [ ] **Step 1: Write failing tests** with a fake clock (`w.Now` advanced by the test):
  - `TestStepPacing`: a Working, non-Busy target gets one `Submit("step", …)` 15–25 s after the last step completed (use a seeded rand). No step is submitted while it is Frozen, Dragged or Busy.
  - `TestProgressPausesWhenQueued` (Review Focus 4): with the scheduler full, `Target.Step` does not advance and the mission timer still counts down.
  - `TestEscapeFailsMission`: `OnResult` with `Step.Done` on the last step sets `Escaped`, makes the mission `"escaped"` and adds +1 star.
  - `TestLevel1WinOnFreeze`: freezing the only target before 90 s gives `"won"`, 1 takedown for the shooter and mission cash 2×50 = 100 (stun assist, doubled for finishing in time).
  - `TestLevel2NeedsBooking`: on Level 2, a freeze alone doesn't win; booking does, giving mission cash (50 + 300)×2 = 700.
  - `TestCrewRevivesAfter30s`: on Level 3, a Frozen target left unbooked 30 s while another target of the same mission is still Working gets `Submit("spring")`. When it resolves, the target is Working, `Restored=true`, and `Say` comes from the result.
  - `TestRivalSpringPenalty`: a spring by a player outside the mission's squad costs the mission's squad 100 cash.
  - `TestQueuedMissionWaits` (Review Focus 4): `Start` with 24 steps in flight returns a mission with `Queued=true`. Targets spawn on a later `Tick` once `InFlight().steps < 24`.
  - `TestLeaderboardRanksTakedowns`: player A freezes 3 targets and has 200 cash; B freezes 2 and has 900 cash; C freezes 3 and has 500 cash. `Leaderboard()` order is C, A, B. A freeze that fails (`Resolve` not ok) counts for nobody, and a crew spring doesn't remove a takedown already earned.
  - `TestSquadJoin`: codes are 4 uppercase letters; a fifth player gets an error; `Of` returns the shared squad.
- [ ] **Step 2: Run** `go test ./games/heist/ -run 'Pacing|Progress|Escape|Level|Crew|Rival|Queued|Squad' -v`. Expected: FAIL.
- [ ] **Step 3: Implement `mission.go`.**
  - Stars start at 1. +1 when a target finishes a step, +1 on escape, +1 when a rival springs one of your catches. The cap is 5.
  - Each star above 1 shortens the crew revive delay by 5 s (minimum 15 s).
  - On completion the score is multiplied by 2 if `Now < Ends`.
  - Targets spawn at `City.RandomStreet(City.DistrictOf(node))` once `Spawn` returns their node.
- [ ] **Step 4: Run** the same command. Expected: PASS.
- [ ] **Step 5: Commit** `git add games/heist/mission*.go && git commit -m "Grand Theft Agent: missions, squads, stars and scoring"`

### Task 5A: Hunters, health and loot

**Files:**
- Create: `games/heist/loot.go`, `games/heist/loot_test.go`
- Modify: `games/heist/world.go` (Avatar fields, hunter fire in `Tick`, drops in `Resolve`)

**Interfaces:**
- Consumes: Tasks 2–5 (`World`, `Target`, `Avatar`, `Resolve`, `Tick`, `City.Vault`, `City.Dispatch`)
- Produces:
  - `type Part struct { ID, Kind string; Tier int }`. `Kind` is one of `"ram"`, `"cpu"`, `"drive"`, `"board"`. `Tier` is 1–5 for RAM and 0 otherwise.
  - `type Drop struct { ID string; Part Part; Pos Vec; Expires float64 }`
  - `func RollPart(rnd *rand.Rand) Part` (math/rand/v2), using the exact table in Global Constraints
  - `Avatar` gains `HP float64; Down bool; RespawnAt, LastHit float64; Inv []Part`
  - `World` gains `Drops map[string]*Drop; Stash map[string][]Part; Rand *rand.Rand`. `NewWorld` seeds `Rand` from the clock; tests overwrite it.
  - New events: `hit` (Player, Target = the shooter), `down`, `respawn`, `drop` (Text = the part, e.g. "DDR5"), `pickup`, `banked` (Text = count).

- [ ] **Step 1: Write failing tests.**
  - `TestRollPartTable`: over 100000 rolls with a fixed seed, every frequency is within ±1 percentage point of the table.
  - `TestHuntersShootNearest`: a Working target 4 tiles from avatar A and 5.5 from B shoots A for 10 every 1.2 s. Frozen, Dragged and Busy targets don't shoot.
  - `TestRegenAfterQuiet`: health regenerates 5/s only after 4 s without a hit, capped at 100.
  - `TestDownDropsBagAndRespawns`: at 0 health the avatar goes Down, drops its drag (the target goes back to Frozen), and every `Inv` part becomes a Drop where it fell with `Expires = Now+60`. `Inv` empties. After 3 s it respawns at the Dispatch centre with 100 health. A Down avatar can't move, fire or act.
  - `TestFreezeAndDeleteDropLoot`: `Resolve(freeze ok)` creates exactly one Drop at the target. `Resolve(delete ok)` creates one more. Failed resolves create none.
  - `TestPickupAndFullInventory`: walking within 0.8 tile picks a drop up. With 8 parts the drop stays on the ground. Expired drops are removed.
  - `TestVaultBanks`: entering `City.Vault` moves the whole `Inv` into `Stash[playerID]` and emits `banked`.
- [ ] **Step 2: Run** `mise exec go@1.27.3 -- go test ./games/heist/ -run 'Roll|Hunters|Regen|Down|DropLoot|Pickup|Vault' -race -v`. Expected: FAIL.
- [ ] **Step 3: Implement.**
  - Hunter fire runs in `Tick` after avatar movement and uses `World.nearest`-style selection. A hunter shot has no intent; it only changes health in the world.
  - Part IDs use `platform.NewID()`.
- [ ] **Step 4: Run** `go test ./games/heist/ -race -v`. Expected: all tests pass, including the earlier ones.
- [ ] **Step 5: Commit** `git add games/heist && git commit -m "Grand Theft Agent: hunters, health, loot and the stash"`

### Task 5B: Rigs and allies

**Files:**
- Create: `games/heist/ally.go`, `games/heist/ally_test.go`
- Modify:
  - `games/heist/world.go`: allies in `Tick` and `Resolve`
  - `games/heist/ops.go`: the `ally` kind
  - `games/heist/mission.go`: ally spawn results, and deleting allies when a mission ends

**Interfaces:**
- Consumes: Task 5A (`Part`, `Stash`, `HP`), Task 4 (`Ops`, `Scheduler`), Task 5 (`Missions.OnResult`)
- Produces:
  - `type Ally struct { ID, Owner, SessionID, Say string; Pos Vec; HP float64; Tier int; State TargetState; Busy bool; FireCD float64 }`. `State` is `Working` or `Frozen`; an ally that is still spawning is not in the map.
  - `World.Allies map[string]*Ally`
  - `func (w *World) BuildRig(playerID string) (Intent, error)`
    - It returns `Intent{Kind:"ally", Target:<new ally ID>, Player}` and removes a board, a CPU, the highest-tier RAM and a drive from the stash.
    - Errors: `"missing parts"`, `"ally limit"` (2 per player, counting allies still spawning) and `"city full"` (16).
  - `Ops` gains `SpawnAlly(ctx context.Context, name string) (sessionID, node, say string, err error)`.
  - `Scheduler` gains the kind `"ally"`: control lane, uncapped. Its `Result` carries `Spawned`, `Node` and `Step.Say`.
  - Freeze, spring and delete intents and `World.Resolve` accept ally IDs as well as target IDs: `Resolve` looks in `Targets`, then `Allies`.

- [ ] **Step 1: Write failing tests.**
  - `TestBuildRigSpendsParts`: a stash with board, CPU, DDR2, DDR5 and drive spends board, CPU, DDR5 and drive, leaves DDR2, and the ally's tier is 5. Missing parts, the per-player limit and the city limit return the named errors and spend nothing.
  - `TestAllySpawnPlacesAlly` (via `Missions.OnResult` with an `ally` Result): the ally appears near its owner with `Say` set. A failed spawn refunds the parts to the stash.
  - `TestAllyFollowsAndFires`: the ally stays within 2 tiles of a moving owner. It fires at the nearest Working target within 6 tiles at 1.0/s for DDR1 and 2.2/s for DDR5. Its hits go in `HitBy` under the owner. The third hit's freeze, once resolved, sets `FrozenBy = owner`, so the takedown is the owner's.
  - `TestHuntersTargetAllies`: hunters shoot the nearest player or ally. An ally at 0 health yields exactly one `Intent{freeze, allyID}`; `Resolve` sets it Frozen.
  - `TestOwnerSpringsAlly`: the owner's short ACTION next to their frozen ally yields `Intent{spring, allyID}`, with no spring cooldown for your own ally. Another player's ACTION does nothing to it.
  - `TestMissionEndDeletesAllies`: when a mission ends (won, failed or escaped), the owner's allies get `delete` submitted, and `Resolve(delete ok)` removes them.
- [ ] **Step 2: Run** `go test ./games/heist/ -run 'Rig|Ally|Allies|Springs' -race -v`. Expected: FAIL.
- [ ] **Step 3: Implement.** Ally speed is the avatar speed, ×1.2 at DDR5. Allies don't drag or book. A frozen ally doesn't move or fire.
- [ ] **Step 4: Run** `go test ./games/heist/ -race -v`. Expected: all tests pass.
- [ ] **Step 5: Commit** `git add games/heist && git commit -m "Grand Theft Agent: rigs deploy real ally agents"`

### Task 6: Game loop, WebSocket, routes and wiring

**Files:**
- Create: `games/heist/game.go`, `games/heist/ws.go`, `games/heist/routes.go`, `games/heist/game_test.go`
- Modify: `main.go` (construct, `Run`, `Routes(mux, web("grand-theft-agent"))`, add `heist_` keys to `/api/stats` like `agentFlow_`), `go.mod` and `go.sum` (`github.com/coder/websocket v1.8.15`)

**Interfaces:**
- Consumes: Tasks 2–5, `platform.Platform`, `platform.Players`, `platform.Timings`
- Produces:
  - `func New(p *platform.Platform, ops Ops, nodes []string) *Game`
  - `func (g *Game) Run(ctx context.Context)`
  - `func (g *Game) Routes(mux *http.ServeMux, web fs.FS)`
  - `func (g *Game) Reset(ctx context.Context) (int, error)`
  - `func (g *Game) Stats() map[string]any`
- Wire protocol (JSON text frames):
  - Client to server:
    - `{"t":"in","mx":-1..1,"my":-1..1,"fire":bool,"act":bool}` at ≤ 20 Hz
    - `{"t":"squad","op":"solo"|"create"|"join","code":"ABCD"}`
    - `{"t":"start","level":1..3}`
    - `{"t":"build"}` (build a rig; the reply is an event, or `{"t":"err","msg":…}`)
  - Server to client, 15 Hz:
    - `{"t":"snap","now":…,"me":playerID,"avatars":[…(with hp, down)],"targets":[…],"allies":[…],"drops":[…],"missions":[…],"events":[…],"board":[…],"substrate":{…},"cap":{"steps":n,"resumes":n,"queued":n},"inv":[…],"stash":[…]}`. `inv` and `stash` are the receiving player's only.
    - Phones get every entity. Snapshots are small enough at 48 players and ~60 targets that culling isn't needed.
- Routes:
  - `GET /grand-theft-agent` (`phone.html`)
  - `GET /grand-theft-agent/screen`
  - `GET /grand-theft-agent/static/`
  - `GET /grand-theft-agent/ws`: needs the player cookie, except with `?screen=1`, which is a read-only spectator.
  - `POST /admin/grand-theft-agent/api/reset`
  - `GET /admin/grand-theft-agent/api/state`
  - `POST /admin/grand-theft-agent/api/pause`, with body `{"paused":bool}`, which stops new missions.
  - Pages are versioned with the same `?v=<hash>` rewrite as `games/flow/routes.go` (`versioned`); copy the helper and adapt the prefix.

- [ ] **Step 1: Write failing tests** (using `httptest.Server` and `fakeOps`):
  - `TestWSJoinAndSnapshot`: with a joined player cookie, dial `/grand-theft-agent/ws`. Within 200 ms you receive a `snap` whose `avatars` include your avatar at Dispatch.
  - `TestWSRejectsNoCookie`: dialling without a cookie gives HTTP 401. `?screen=1` is accepted and its input frames are ignored.
  - `TestReconnectKeepsAvatar` (Review Focus 1): disconnect and redial with the same cookie. The avatar ID and position are unchanged, and `Online` goes false and then true.
  - `TestPlayerCap`: the 49th new player gets a `{"t":"full"}` frame and is closed.
  - `TestResetCleansUp` (Review Focus 5): start a Level 3 mission so three sessions are spawned, then `Reset`. `fakeOps.Delete` was called for all three, `Targets` is empty, WebSocket clients get a fresh snapshot, and the goroutine count returns to baseline after `Close` of the test server.
  - `TestTickNeverBlocksOnOps` (Review Focus 2): with a `fakeOps` whose `Freeze` blocks forever, 30 ticks still complete within 2.5 s.
- [ ] **Step 2: Run** `go test ./games/heist/ -run 'WS|Reconnect|PlayerCap|Reset|TickNever' -v`. Expected: FAIL.
- [ ] **Step 3: Implement.**
  - `Game.Run` starts:
    - a 15 Hz ticker that takes the lock, calls `World.Tick`, submits the intents, drains `Scheduler.Results()` into `World.Resolve` and `Missions.OnResult`, then broadcasts
    - an actor poller every 1 s through `p.Substrate()` plus a `Nodes` refresh, the same as `games/flow`
  - Each client has a buffered send channel of 2 that drops stale snapshots.
  - Record `freeze`, `spring` and `extract` timings in `platform.Timings`.
- [ ] **Step 4: Run** `go test ./games/heist/ -v` and `go vet ./...`. Expected: everything passes.
- [ ] **Step 5: Commit** `git add games/heist main.go go.mod go.sum && git commit -m "Grand Theft Agent: game loop, WebSocket and routes"`

### Task 7: kagent adapter

**Files:**
- Create: `games/heist/agentops.go`, `games/heist/agentops_test.go`
- Modify: `games/heist/cluster_test.go` (add `TestKagentOpsLifecycle`), `main.go` (pass `newKagentOps(agentClient("heister"), agentClient("sidekick"), extractMode)`), `manifests/heist-agent.yaml` (add Agent `sidekick` on Harness `heist`: same model config, outputSchema `{say: string}` required, a system prompt as a loyal cartoon bodyguard agent that answers in one family-friendly line ≤ 60 characters)

**Interfaces:**
- Consumes: `kagent.Kagent`, with:
  - `CreateSession`
  - `Prompt(ctx, sid, text) (Reply, error)` and `Reply.Decode`
  - `Suspend`
  - `Resume`
  - `Checkpoint(ctx, sid, headTaskID)`
  - `Delete`
  - `ActorStates`
- Produces:
  - `func newKagentOps(heister, sidekick *kagent.Kagent, extract string) Ops`. `SpawnAlly` = `CreateSession` on sidekick plus one `Prompt` ("You just booted up to protect your crew. Greet them in one line."). `Spring` on an ally session uses the prompt "You were frozen protecting your crew and just got rebooted. React in one line." The adapter remembers which sessions are allies., where `extract` is `"direct"` or `"resume-first"` (from Task 1's finding; `HEIST_EXTRACT` env, default from that finding).
  - `func stepPrompt(job string, step, steps int) string`
  - `func parseStep(r kagent.Reply) (StepResult, error)`

- [ ] **Step 1: Write failing unit tests:**
  - `TestParseStepClipsSay`: a JSON `{"say":"<70 chars>","step_done":true,"loot":5}` gives `Say` clipped to 60 runes, `Done` true and `Loot` 5.
  - `TestParseStepBadJSON`: malformed output gives an error, and the scheduler's result has `OK=false`.
  - `TestStepPromptsAreFixed`: every prompt for steps 1–4 of each of the three jobs comes from a fixed table, and none contains player-supplied text.
  - `TestSpringPromptMentionsFrozen`: the post-resume prompt is exactly "You were frozen by the cops and just got sprung. React, then continue your heist." followed by the next step.
- [ ] **Step 2: Run** `go test ./games/heist/ -run 'Parse|Prompt' -v`. Expected: FAIL.
- [ ] **Step 3: Implement `agentops.go`.**
  - There are three fixed jobs of 4 bash steps each (for example "crack the safe": write `safe.sh`, run it, count the loot with `wc`, `sha256sum` the bag). Each writes in `/data/workspace`.
  - `Freeze` = `Suspend`, retried on FailedPrecondition the way `suspendSettled` does in `games/flow/game.go`. Task 1 found that a Prompt right after Resume and a Delete right after a turn can also return FailedPrecondition "not yet settled": retry those the same way (shared helper).
  - Task 1 found the Agent's `outputSchema` can't use `maxLength` or `minimum` (kagent's portable profile rejects them), so `parseStep` is the only place `say` gets clipped to 60.
  - `Spring` = `Resume`, then one `Prompt` with the sprung line; `node` comes from `ActorStates`.
  - `Extract` = `Checkpoint` with the last task ID (kept per session in the adapter), then `Delete`. Task 1 measured that checkpointing a SUSPENDED session fails ("cannot checkpoint in state RUNTIME_STATE_SUSPENDED"), so the default mode is `resume-first`: Resume, Checkpoint, Delete. Never call `Checkpoint` on a suspended session, because it retries for 2 minutes.
  - A 403 or 429 from agentgateway is returned as an error whose text starts with `policy:`, so the screen can show it.
- [ ] **Step 4: Run** `go test ./games/heist/ -v`. Expected: PASS.
- [ ] **Step 5: Add `TestKagentOpsLifecycle`** (`cluster` tag): Spawn → 2 Steps → Freeze → Spring (`say` is not empty) → Step → Extract → assert the session is gone from `ListSessionIDs`. Run it the same way as Task 1, Step 3. Expected: PASS, with timings logged.
- [ ] **Step 6: Commit** `git add games/heist main.go && git commit -m "Grand Theft Agent: kagent adapter for heist targets"`

### Task 8: Phone page

**Files:**
- Create: `web/grand-theft-agent/city.js`, `web/grand-theft-agent/net.js`, `web/grand-theft-agent/phone.html`

**Interfaces:**
- Consumes: the Task 6 wire protocol.
- Produces:
  - `startCity(canvas, {mode:"phone"|"screen", me})`, which returns `{apply(snap), draw()}`.
  - `connect(url, onSnap)`, which returns `{send(obj)}`, reconnects with a 1 s → 8 s backoff, and interpolates positions between the last two snapshots (100 ms delay).

- [ ] **Step 1: Vendor Three.js.** Download `https://cdn.jsdelivr.net/npm/three@0.170.0/build/three.module.min.js` to `web/grand-theft-agent/vendor/three.module.min.js` and import it with a relative path (no import map, no CDN at runtime).
- [ ] **Step 2: Build `city.js`** as an ES module.
  - Three.js scene, one world unit per tile. A fixed-angle top-down perspective camera (about 55° pitch): it follows the player on phones, and shows the whole city on the screen. Matrix palette (black, green, see below; superseded by the night city) with soft shadows. Cap the device pixel ratio at 2 and keep it under ~150 draw calls (instanced meshes for buildings, windows and road marks) so mid-range phones hold 30+ fps.
  - **Matrix look** (spec; superseded 2026-11-02 by the user's decision: a normal night city, see the design doc's "Look" section): a black void and dark glass buildings, with green digital rain scrolling on their faces (one shared canvas texture of glyphs and digits, animated by UV offset), green wireframe edges (`EdgesGeometry`), emissive green avatars in the player colour, hunters as dark suited figures with green outlines, allies in the owner's colour, and parts as small glowing chips (DDR5 gold).
  - Every model is built from primitives (boxes, capsules, spheres, cylinders). Characters are round and chunky. No external models or textures, except the logos as canvas textures.
  - Roads with lane marks, buildings with lit windows (lit count = `substrate.busyWorkers`), the Vault (the Snapshot Storage logo, `/agent-flow/static/` not reused, so copy the needed SVGs), and Dispatch (the kagent logo).
  - Avatars are round 3D figures in the player colour, with HTML nick tags projected above them.
  - Targets, 12 shapes keyed by ID like Agent Flow's `shapeOf`:
    - green **NEW** and blue **RESTORED** tags
    - a progress bar of `step/steps`
    - a `say` bubble for 4 s
    - Frozen = cyan ice with static
    - Booked = filed into the Vault wall
    - Deleted = de-rez into pixels
  - Effects:
    - suspend-ray tracer
    - "suspended in N ms" and "restored in N ms" floaters from events
    - "it slipped away"
  - HUD (HTML overlay, not WebGL): health bar, wanted stars, mission timer, takedowns, cash and a minimap (a small 2D canvas). An 8-slot inventory strip shows part icons and tiers. A stash drawer shows counts and a **Build rig** button, enabled when the stash has board, CPU, RAM and drive. It sends `{"t":"build"}`. A "DOWN — respawning" overlay shows while you're down.
  - If WebGL is unavailable, show "this device can't run 3D" with a link to the big screen. No 2D fallback renderer.
- [ ] **Step 3: Build `phone.html`.**
  - Join flow: reuse the arcade cookie, sending to `/` to join if there's none. Then a **Go solo** button or a **Squad** button (create, showing the code; or join with a 4-letter input), then a level picker (1–3, with locked levels greyed until the previous one is won by this squad).
  - In game: the follow-cam 3D view, a left-thumb joystick (pointer events, so it works with touch and with a mouse; a dead zone of 0.15) and right-thumb **FIRE** and **ACTION** buttons. No keyboard handlers (hold ACTION for 1 s to DELETE). Send `in` at 20 Hz while anything is held and once on release.
  - Mission banner: "Freeze CRUSHER before the heist finishes", with the timer.
  - "Waiting for a free safehouse" when `Queued`.
  - A link to `/grand-theft-agent/screen`.
- [ ] **Step 4: Verify in a phone-sized headless browser** (run Chromium with `--use-angle=swiftshader` or `--enable-unsafe-swiftshader` so WebGL works headless). Run `./deploy.sh`, then use Playwright at 390×844 to open `http://localhost:8090/`, join as "Tester", open `/grand-theft-agent`, start Level 1, hold FIRE near the target and take screenshots. Expected:
  - the target freezes with a "suspended in … ms" label
  - the console shows no errors
  - a 60-frame `requestAnimationFrame` timing in the page reports an average frame time (log it in the report; flag it if over 33 ms)
  - `kubectl … get` of the actor (via `/admin/grand-theft-agent/api/state`) shows it SUSPENDED
- [ ] **Step 5: Commit** `git add web/grand-theft-agent && git commit -m "Grand Theft Agent: phone controller and city renderer"`

### Task 9: Big screen

**Files:**
- Create: `web/grand-theft-agent/screen.html`

**Interfaces:**
- Consumes: `startCity(canvas, {mode:"screen"})`, `connect("/grand-theft-agent/ws?screen=1", …)`

- [ ] **Step 1: Build `screen.html`.**
  - The whole 3D city fills the left of the screen, from a fixed high camera.
  - The right column has:
    - the QR card for `https://kubecon.agentpwned.ai/grand-theft-agent` (with `?join=` override, as on Agent Flow)
    - the leaderboard: the top 8 **players by takedowns** ("most agents frozen wins"), each with their squad tag and cash, plus a small squad-totals line
    - a mission feed of events (frozen, booked, sprung, escaped, slipped, policy, down, DDR5 drops, rigs deployed)
    - the leaderboard row also shows each player's best rig tier (DDR1–DDR5) and their live allies
  - Subtle stat lines, as on the Agent Flow header:
    - "40 workers · N hunters · A allies · M awake"
    - p50 suspend, resume and checkpoint times from `platform.Timings`
    - snapshots in the vault
    - an always-on pod cost estimate using `POD_HOUR_USD`
  - A compute heartbeat strip along the bottom that spikes on `spawn`, `sprung` and step events (port Agent Flow's `#pulseBox` approach).
  - District labels show real node names.
  - Fixed camera; no camera motion.
- [ ] **Step 2: Verify** with headless Chromium at 1920×1080 while two Playwright phone sessions play Level 3. Expected:
  - both avatars visible
  - the crew revive shows the RESTORED tag and the sprung bubble
  - the leaderboard updates and ranks by takedowns
  - no console errors
- [ ] **Step 3: Commit** `git add web/grand-theft-agent/screen.html && git commit -m "Grand Theft Agent: big screen"`

### Task 10: Ship and booth-load check

**Files:**
- Modify: `README.md` (table row: status Playable, pages, a "Grand Theft Agent" section), `web/index.html` (front-page entry), `docs/grand-theft-agent.md` (measured numbers), `games/heist/cluster_test.go` (add `TestBoothLoad`)

- [ ] **Step 1: Write `TestBoothLoad`** (`cluster` tag).
  - It joins 24 players through `/api/join` against `ARCADE_URL` and opens 24 WebSockets. 8 solo players and 4 squads of 4 each start a mission at levels 1–3.
  - Bots walk toward their nearest target and fire, pick up parts, bank them at the Vault and build a rig whenever they can, for 5 minutes. Also log allies alive and their spawn times.
  - It logs, every 30 s:
    - p50 and p95 freeze and spring times
    - `cap` queue lengths
    - errors by kind
  - It also logs agentgateway token use per minute from its metrics endpoint.
  - It fails on any stuck mission (a timer at 0 with no outcome after 10 s) or any op error that isn't `policy:`.
- [ ] **Step 2: Run** `ARCADE_URL=http://localhost:8090 KAGENT_URL=http://localhost:18083 mise exec go@1.27.3 -- go test -tags cluster -run TestBoothLoad -timeout 15m -v ./games/heist/`. Expected: PASS. Record:
  - tokens per minute below the 300000 limit
  - p95 freeze under 1 s
  - p95 spring under 6 s
- [ ] **Step 3: Update the docs.** Add the measured table to `docs/grand-theft-agent.md` and close the open items it answers. Update the `README.md` row and section, and add the `index.html` card.
- [ ] **Step 4: Deploy and verify publicly.** Run `./deploy.sh`, then `curl -sI https://kubecon.agentpwned.ai/grand-theft-agent` (expected: 200) and `curl -sI https://kubecon.agentpwned.ai/admin/grand-theft-agent/api/state` (expected: 404). Then `POST /admin/grand-theft-agent/api/reset` on the LAN.
- [ ] **Step 5: Commit**
```bash
git add README.md web/index.html docs/grand-theft-agent.md games/heist/cluster_test.go
git commit -m "Grand Theft Agent: playable levels 1-3, booth load check"
```
