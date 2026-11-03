// This file is the startup sweep. Game state lives in memory, so a process that stops
// while missions run leaves its targets' and allies' sessions behind in kagent. When a
// new process starts, nothing in it owns a session yet, so every session of the game's
// agents is an orphan: Run lists them and deletes them all, with any checkpoints they
// have, off the tick loop. (kagent lists checkpoints only per session, so checkpoints
// of sessions that are already deleted can't be found here; the game deletes its
// bookings' checkpoints at the admin reset and on shutdown instead.)
package heist

import (
	"context"
	"errors"
	"log/slog"
	"sync/atomic"
	"time"
)

// Sweep bounds. sweepListTimeout is a var so a test can shorten it.
var sweepListTimeout = 30 * time.Second

const (
	sweepDeleteTimeout = 10 * time.Second
	sweepTimeout       = 2 * time.Minute
)

// errStartingUp refuses missions and rigs while the sweep is still listing: a session
// spawned then could be listed and deleted as an orphan.
var errStartingUp = errors.New("starting up, try again")

// sweep deletes every session the game's agents have, and their checkpoints. It clears
// g.listing once the session list is in (or failed, or timed out), so play can start.
// It relies on the arcade running as one replica (Recreate): a second live process
// would own sessions this one deletes.
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
	var cps atomic.Int64
	var cpsFound atomic.Int64
	deleted := parallel(ctx, ids, func(ctx context.Context, id string) error {
		dctx, dcancel := context.WithTimeout(ctx, sweepDeleteTimeout)
		defer dcancel()
		list, err := g.ops.ListCheckpoints(dctx, id)
		if err != nil {
			slog.Warn("heist startup sweep: list checkpoints failed", "session", id, "err", err)
		}
		cpsFound.Add(int64(len(list)))
		for _, cp := range list {
			if err := g.ops.DeleteCheckpoint(dctx, cp); err != nil {
				slog.Warn("heist startup sweep: delete checkpoint failed", "checkpoint", cp, "err", err)
				continue
			}
			cps.Add(1)
		}
		if err := g.ops.Delete(dctx, id); err != nil {
			slog.Warn("heist startup sweep: delete failed", "session", id, "err", err)
			return err
		}
		return nil
	})
	slog.Info("heist startup sweep", "orphans", len(ids), "deleted", deleted,
		"checkpoints", cpsFound.Load(), "checkpointsDeleted", cps.Load(), "took", time.Since(start).Round(time.Millisecond))
}
