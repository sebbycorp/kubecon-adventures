// This file is the admin reset: delete every session and checkpoint the game made and
// start a fresh city, without dropping connected players.
package heist

import (
	"context"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"
)

// resetParallel bounds the deletes a reset (or shutdown) runs at once.
const resetParallel = 8

// Reset deletes every agent session the game created (targets and allies, and ones
// still spawning when their results land) and every booked target's checkpoint (its
// session is already gone: Extract deleted it), starts a fresh city, and sends every
// client a new snapshot. Connected players keep an avatar. It returns the sessions and
// checkpoints deleted.
func (g *Game) Reset(ctx context.Context) (sessions, checkpoints int, err error) {
	g.mu.Lock()
	seen := map[string]bool{}
	var cps []string
	for _, t := range g.w.Targets {
		seen[t.SessionID] = t.State != Deleted && t.State != Booked && t.Checkpoint == ""
		if t.Checkpoint != "" {
			cps = append(cps, t.Checkpoint)
		}
	}
	for _, al := range g.w.Allies {
		seen[al.SessionID] = true
	}
	for _, cp := range g.booked {
		if cp != "" {
			cps = append(cps, cp)
		}
	}
	g.booked = map[string]string{}
	for _, z := range g.zaps {
		seen[z.session] = true
	}
	for _, z := range g.zapping {
		seen[z.session] = true
	}
	old, rec := g.s, g.inflight
	g.fresh()
	for c := range g.clients {
		if !c.screen {
			g.ensureAvatar(c.pl).Online = true
		}
	}
	g.mu.Unlock()
	go g.drainOld(old, rec)

	var sess []string
	for s, del := range seen {
		if del && s != "" {
			sess = append(sess, s)
		}
	}
	sessions = parallel(ctx, sess, func(ctx context.Context, s string) error {
		err := g.ops.Delete(ctx, s)
		if err != nil {
			slog.Warn("substrate siege: reset delete", "session", s, "err", err)
		}
		return err
	})
	checkpoints = g.deleteCheckpoints(ctx, cps)
	g.mu.Lock()
	f := g.framesLocked(nil)
	g.mu.Unlock()
	f.send()
	return sessions, checkpoints, ctx.Err()
}

// deleteCheckpoints deletes cps, resetParallel at a time, and returns how many went.
func (g *Game) deleteCheckpoints(ctx context.Context, cps []string) int {
	return parallel(ctx, cps, func(ctx context.Context, cp string) error {
		err := g.ops.DeleteCheckpoint(ctx, cp)
		if err != nil {
			slog.Warn("substrate siege: delete checkpoint", "checkpoint", cp, "err", err)
		}
		return err
	})
}

// parallel runs do on every item, resetParallel at a time, each bounded by opTimeout,
// and returns how many succeeded. It stops starting new ones once ctx ends.
func parallel(ctx context.Context, items []string, do func(context.Context, string) error) int {
	var n atomic.Int64
	var wg sync.WaitGroup
	sem := make(chan struct{}, resetParallel)
	for _, it := range items {
		select {
		case sem <- struct{}{}:
		case <-ctx.Done():
		}
		if ctx.Err() != nil {
			break
		}
		wg.Go(func() {
			defer func() { <-sem }()
			c, cancel := context.WithTimeout(ctx, opTimeout)
			defer cancel()
			if do(c, it) == nil {
				n.Add(1)
			}
		})
	}
	wg.Wait()
	return int(n.Load())
}

// drainOld waits for the spawns, rigs and bookings a reset scheduler still has running,
// deletes the sessions (or, for a booking, the checkpoint) they create, then closes it.
func (g *Game) drainOld(old *Scheduler, rec map[opKey]int) {
	defer old.Close()
	pending := 0
	for k, n := range rec {
		if drained(k.kind) {
			pending += n
		}
	}
	timeout := time.NewTimer(opTimeout + 5*time.Second)
	defer timeout.Stop()
	for pending > 0 {
		select {
		case r := <-old.Results():
			k := opKey{r.Kind, r.Target}
			if !drained(r.Kind) || rec[k] == 0 {
				continue
			}
			rec[k]--
			pending--
			if !r.OK {
				continue
			}
			c, cancel := context.WithTimeout(context.Background(), opTimeout)
			if r.Kind == "extract" {
				if r.Checkpoint != "" {
					if err := g.ops.DeleteCheckpoint(c, r.Checkpoint); err != nil {
						slog.Warn("substrate siege: reset delete checkpoint", "checkpoint", r.Checkpoint, "err", err)
					}
				}
			} else if err := g.ops.Delete(c, r.Spawned); err != nil {
				slog.Warn("substrate siege: reset delete", "session", r.Spawned, "err", err)
			}
			cancel()
		case <-timeout.C:
			return
		case <-g.done:
			return
		}
	}
}

// drained reports whether drainOld waits for an op of kind: it creates a session (or a
// checkpoint) the fresh game knows nothing about.
func drained(kind string) bool { return kind == "spawn" || kind == "ally" || kind == "extract" }
