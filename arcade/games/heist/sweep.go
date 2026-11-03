// This file is the startup sweep. Game state lives in memory, so a process that stops
// while missions run leaves its targets' and allies' sessions behind in kagent. When a
// new process starts, nothing in it owns a session yet, so every session of the game's
// agents is an orphan: Run lists them and deletes them all, off the tick loop.
package heist

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"
)

// Sweep bounds. sweepListTimeout is a var so a test can shorten it.
var sweepListTimeout = 30 * time.Second

const (
	sweepDeleteTimeout = 10 * time.Second
	sweepTimeout       = 2 * time.Minute
	sweepParallel      = 8
)

// errStartingUp refuses missions and rigs while the sweep is still listing: a session
// spawned then could be listed and deleted as an orphan.
var errStartingUp = errors.New("starting up, try again")

// sweep deletes every session the game's agents have. It clears g.listing once the
// list is in (or failed, or timed out), so play can start.
func (g *Game) sweep(ctx context.Context) {
	start := time.Now()
	ctx, cancel := context.WithTimeout(ctx, sweepTimeout)
	defer cancel()
	lctx, lcancel := context.WithTimeout(ctx, sweepListTimeout)
	ids, err := g.ops.ListSessions(lctx)
	lcancel()
	g.mu.Lock()
	g.listing = false
	g.mu.Unlock()
	if err != nil {
		slog.Warn("heist startup sweep: list failed", "err", err, "listed", len(ids))
	}
	var deleted atomic.Int64
	var wg sync.WaitGroup
	sem := make(chan struct{}, sweepParallel)
	for _, id := range ids {
		select {
		case sem <- struct{}{}:
		case <-ctx.Done():
		}
		if ctx.Err() != nil {
			break
		}
		wg.Add(1)
		go func() {
			defer func() { <-sem; wg.Done() }()
			dctx, dcancel := context.WithTimeout(ctx, sweepDeleteTimeout)
			defer dcancel()
			if err := g.ops.Delete(dctx, id); err != nil {
				slog.Warn("heist startup sweep: delete failed", "session", id, "err", err)
				return
			}
			deleted.Add(1)
		}()
	}
	wg.Wait()
	slog.Info("heist startup sweep", "orphans", len(ids), "deleted", deleted.Load(), "took", time.Since(start).Round(time.Millisecond))
}
