# Duck Hunt: design

The first arcade game. Each duck is a real AI agent running on Agent Substrate. When the Hunter shoots a duck, its session is suspended: the sandbox is snapshotted and its slot is freed. When the owner revives it, the snapshot is restored with the agent's memory intact. The game is a live demo of how fast, dense and safe Substrate is.

## Decisions

| Topic | Decision |
|-------|----------|
| Prompts | 3 fixed buttons per player; each one hatches a duck (max 3 ducks per player) |
| Hunter | Idle timer: a duck nobody has prompted for 20 s is shot (configurable) |
| Revive | The owner taps Revive on their phone, which restores the duck from its snapshot |
| Screens | Phones and the booth big screen show the same live sky (shared `web/sky.js`). Phones mark your ducks with YOU and add your controls; the big screen adds the stats panel |
| Duck = actor | Each duck is one kagent session, which is one Substrate actor (`session-<id>`). The backend polls actor states twice a second: a yellow glow means its sandbox is awake, Zs mean it is asleep as a snapshot. Phones and admin show the actor ID, state and node |
| Domain | `kubecon.agentpwned.ai/duck-hunt` (Cloudflare Tunnel, later); LAN only for now |
| Admin | `/admin` (API under `/admin/api/*`) on the LAN with no sign-in for now. Google sign-in for sebastian.maniak@solo.io goes in front of the `/admin` prefix later |
| Art | NES Duck Hunt look (palette, sky/tree/grass/dirt, SHOT/HIT/SCORE bar, crosshair and flash, falling duck, pop-up Hunter, pixel font) with original sprites and synthesized sounds. No ripped Nintendo assets or logos |

## What Substrate actually does (measured 2026-10-28)

Substrate snapshots an actor **as soon as it goes idle**, about 1 s after each reply, and restores it when the next message arrives. A "flying" duck is therefore almost always a snapshot on disk, which is the density story: with 8 ducks flying, 0 sandboxes were awake.

- The Hunter's shot is a kagent `SuspendSession`. It parks the session so that messages can't wake it until it is revived. The actor is usually already snapshotted by then.
- On screen, shot and revive labels show **Substrate's own timings**, scraped from atelet's `ate_actor_checkpoint_duration_seconds` and `ate_actor_restore_duration_seconds` (phase `total`). The kagent API times appear only on the admin page.

| Substrate (atelet metrics) | Avg |
|----------------------------|-----|
| Snapshot (checkpoint total) | 47–48 ms |
| Restore total | 226 ms (warm) to 401 ms (first wake after hatch) |

## Measured on this cluster (2026-10-28)

| Operation | Time |
|-----------|------|
| Agent ready, golden snapshot built (one time per Agent) | ~30 s |
| New session (hatch) + first LLM reply | ~2.7 s |
| Suspend (shot) until `SUSPENDED` | 29 ms |
| Resume (revive) until `READY` | 184 ms |
| Memory after resume | Conversation intact |

## Player flow

1. **Scan the QR code.** It opens `/duck-hunt`. The player enters a nickname, gets a random duck color, and a player cookie is set.
2. **Hatch.** There are three buttons, and each can be used once:
   - **Fly fast:** "You're a speedy duck. Pick your flight."
   - **Zig-zag:** "You're a sneaky duck dodging hunters. Pick your flight."
   - **Show off:** "You're a show-off duck performing for a crowd. Pick your flight."

   Each tap creates a kagent session on the `duck` Agent and sends the prompt. The agent answers with structured output (see below), and the duck takes off on the big screen.
3. **Keep it alive.** A "Quack!" button sends a short follow-up prompt, which resets the idle timer. The duck answers with a new quack line, and its flight may change.
4. **Shot.** After 20 s with no prompt, the Hunter enters, aims and fires. The backend calls `SuspendSession`. The duck falls, and the screen shows `snapshotted in 29 ms`. The phone shows the duck as down.
5. **Revive.** The owner taps Revive. The backend calls `ResumeSession`, then sends one prompt ("You were shot down and revived. React!"). The duck flies back in, and the screen shows `restored in 184 ms` and the duck's reply, which proves it remembers.

## The duck agent

One `Agent` (`duck`) on the `kagent` Harness and `kagent-default` WorkerPool. Every hatch is a new Session, so every duck is its own Substrate actor in its own gVisor sandbox.

`outputSchema`, which drives the animation:

```yaml
type: object
required: [pattern, speed, quack]
properties:
  pattern: { type: string, enum: [straight, zigzag, loop, dive, wobble] }
  speed:   { type: integer, minimum: 1, maximum: 10 }
  quack:   { type: string, maxLength: 60 }   # speech bubble on the big screen
```

The system prompt keeps answers short and family-friendly and asks for the schema only. The LLM is OpenAI, reached through agentgateway (below).

## Architecture

```
phone ──┐
screen ─┼─► agentgateway Gateway ─► duck-hunt backend (Go) ──gRPC──► kagent controller ──► Substrate actors (gVisor)
admin ──┘     (HTTPRoute /duck-hunt, /admin)        │                                          │
                                                    └── WebSocket fan-out to screens/phones     ▼
                                       kagent ModelConfig baseUrl ─► agentgateway LLM route ─► OpenAI
```

- **Backend:** one Go binary (`duck-hunt/`), using kagent's generated gRPC clients (`go/api/gen/kagent/api/v1`: `SessionService`, `SystemService`). It serves the three pages and their static assets, runs the Hunter loop, and pushes game state over WebSocket. Game state (players, ducks and timers) is kept in memory; kagent and Substrate are the source of truth for sessions.
- **Frontend:** plain HTML, Canvas and JS with no build step. Three pages:
  - `/duck-hunt`: the phone controller
  - `/duck-hunt/screen`: the big screen (sky, Hunter and stats)
  - `/admin`: the duck table with actions
- **agentgateway:**
  - Front door: a Gateway and HTTPRoutes for the game. The Cloudflare Tunnel will point here later.
  - LLM path: the `duck` ModelConfig `baseUrl` points to an agentgateway route that adds the OpenAI key and enforces per-player rate limits. This keeps the OpenAI key out of the agent sandboxes and gives us token and latency metrics.
- **Local access:** a host port (8090) forwards to the Gateway, the same way the kagent UI uses 8080.

## Stats panel (big screen)

| Stat | Source |
|------|--------|
| Ducks flying / down | Game state + `GetSubstrateSummary` (`running_actor_count`, `actor_status_counts`) |
| Hatch, shot and revive times (last, p50, p95) | Backend timers around `CreateSession`/`SuspendSession`/`ResumeSession` |
| Ducks per worker, worker count | `GetSubstrateSummary` (`worker_count`, `busy_worker_count`), `ListSubstrateWorkers` |
| LLM calls and tokens | agentgateway metrics (Prometheus endpoint) |
| Leaderboard: longest-flying duck, most revives | Game state |

## Admin page (`/admin`)

- A live table of ducks: owner, prompt, state, age, idle time, shot and revive counts, and the session ID
- Per-duck actions: shoot, revive, delete
- Global actions:
  - Pause or resume the Hunter
  - Set the idle timeout
  - Start a new round (deletes all sessions)
  - Kill all
- The current Substrate summary, with raw numbers

## Open items

- **Capacity:** the worker pool has 1 worker today. Load-test 50, 100 and 200 ducks, then size `substrateWorkerPool.replicas` and the per-worker actor limits for the booth.
- **Abuse:** the buttons are fixed and nicknames are filtered, so nothing free-form reaches the LLM. agentgateway rate limits per player cookie.
- **Session cleanup:** delete sessions at the end of a round so snapshots don't pile up in rustfs.
