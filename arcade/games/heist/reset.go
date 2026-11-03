// This file is the admin reset: delete every session the game made and start a fresh
// city, without dropping connected players.
package heist

import (
	"context"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"
)

// Reset deletes every agent session the game created (targets and allies, and ones
// still spawning when their results land), starts a fresh city, and sends every client
// a new snapshot. Connected players keep an avatar. It returns the sessions deleted.
func (g *Game) Reset(ctx context.Context) (int, error) {
	g.mu.Lock()
	seen := map[string]bool{}
	for _, t := range g.w.Targets {
		seen[t.SessionID] = t.State != Deleted
	}
	for _, al := range g.w.Allies {
		seen[al.SessionID] = true
	}
	for sess := range g.booked {
		seen[sess] = true
	}
	g.booked = map[string]bool{}
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

	var n atomic.Int64
	var wg sync.WaitGroup
	for sess, del := range seen {
		if !del || sess == "" {
			continue
		}
		wg.Go(func() {
			c, cancel := context.WithTimeout(ctx, opTimeout)
			defer cancel()
			if err := g.ops.Delete(c, sess); err != nil {
				slog.Warn("grand theft agent: reset delete", "session", sess, "err", err)
				return
			}
			n.Add(1)
		})
	}
	wg.Wait()
	g.mu.Lock()
	f := g.framesLocked(nil)
	g.mu.Unlock()
	f.send()
	return int(n.Load()), ctx.Err()
}

// drainOld waits for the spawns and rigs a reset scheduler still has running, deletes
// the sessions they create, then closes it.
func (g *Game) drainOld(old *Scheduler, rec map[opKey]int) {
	defer old.Close()
	pending := 0
	for k, n := range rec {
		if k.kind == "spawn" || k.kind == "ally" {
			pending += n
		}
	}
	timeout := time.NewTimer(opTimeout + 5*time.Second)
	defer timeout.Stop()
	for pending > 0 {
		select {
		case r := <-old.Results():
			k := opKey{r.Kind, r.Target}
			if (r.Kind != "spawn" && r.Kind != "ally") || rec[k] == 0 {
				continue
			}
			rec[k]--
			pending--
			if r.OK {
				c, cancel := context.WithTimeout(context.Background(), opTimeout)
				if err := g.ops.Delete(c, r.Spawned); err != nil {
					slog.Warn("grand theft agent: reset delete", "session", r.Spawned, "err", err)
				}
				cancel()
			}
		case <-timeout.C:
			return
		case <-g.done:
			return
		}
	}
}
