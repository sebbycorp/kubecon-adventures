// This file is the running game: one mutex around the World, a 15 Hz loop that ticks
// it, runs its intents and routes results back, the startup sweep of orphaned sessions,
// and the cleanup on shutdown.
package defense

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"sync"
	"time"

	"github.com/sebbycorp/kubecon-adventures/arcade/internal/platform"
)

const (
	maxPlayers   = 48
	tickHz       = 15
	maxScreens   = 8     // concurrent public screens (LAN screens are exempt)
	idleAfter    = 600.0 // seconds without input before a connected player counts as offline
	evictAfter   = 300.0 // seconds offline before a ship leaves space
	tokenWindow  = 60.0  // seconds of task tokens summed for tokens/min
	shutdownWait = 15 * time.Second
)

// PoolName and AgentName are the WorkerPool and Agent the game runs on.
const (
	PoolName  = "defense"
	AgentName = "subagent"
)

// Game is Cluster Defense's server.
type Game struct {
	p     *platform.Platform
	ops   Ops
	every time.Duration

	mu       sync.Mutex
	w        *World
	r        *Runner
	clients  map[*client]bool
	conns    map[string]int     // open connections per player
	pendIn   map[*client]Input  // coalesced input frames waiting for a token
	lastIn   map[string]float64 // world time of each connected player's last frame
	idle     map[string]bool
	offline  map[string]float64 // world time each player went offline
	listing  bool               // the startup sweep is still listing sessions
	paused   bool
	seq      uint64
	tokens   []tokenMark
	tokenSum int64
	done     chan struct{}
	stopped  chan struct{}
}

type tokenMark struct {
	at float64
	n  int64
}

// New builds the game. Run starts it.
func New(p *platform.Platform, ops Ops) *Game {
	seed := uint64(time.Now().UnixNano())
	return &Game{p: p, ops: ops, every: time.Second / tickHz, w: NewWorld(rand.New(rand.NewPCG(seed, seed>>1|1))),
		r: NewRunner(ops), clients: map[*client]bool{}, conns: map[string]int{}, pendIn: map[*client]Input{},
		lastIn: map[string]float64{}, idle: map[string]bool{}, offline: map[string]float64{},
		done: make(chan struct{}), stopped: make(chan struct{})}
}

// Run starts the startup sweep and the tick loop; both stop when ctx ends.
func (g *Game) Run(ctx context.Context) {
	g.mu.Lock()
	g.listing = true
	g.mu.Unlock()
	go g.sweep(ctx)
	go func() {
		t := time.NewTicker(g.every)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				g.shutdown()
				return
			case <-t.C:
				g.tick()
			}
		}
	}()
}

// Stopped is closed once the game has shut down and deleted its sessions.
func (g *Game) Stopped() <-chan struct{} { return g.stopped }

func (g *Game) tick() {
	g.mu.Lock()
	g.drain()
	g.flushInputs(time.Now())
	g.w.Slots = g.slots()
	for _, in := range g.w.Tick(g.every.Seconds()) {
		g.submit(in)
	}
	g.housekeep()
	f := g.framesLocked(g.w.DrainEvents())
	g.mu.Unlock()
	f.send()
}

// slots is the defense pool's worker count from Substrate, or DefaultSlots before the
// first poll.
func (g *Game) slots() int {
	if g.p == nil {
		return g.w.Slots
	}
	for _, ps := range g.p.Substrate().Pools {
		if ps.Name == PoolName && ps.Replicas > 0 {
			return int(ps.Replicas)
		}
	}
	return DefaultSlots
}

func (g *Game) drain() {
	for {
		select {
		case r := <-g.r.Results():
			g.route(r)
		default:
			return
		}
	}
}

// route applies one Result. g.mu is held.
func (g *Game) route(r Result) {
	if !r.OK && r.Err != "stopped" {
		// Errors carry kagent's status, never an agent's reply text.
		slog.Warn("cluster defense: op failed", "kind", r.Kind, "agent", r.ID, "ms", r.Ms, "err", r.Err)
	}
	switch r.Kind {
	case "suspend":
		if r.OK && g.p != nil {
			g.p.Timings.Record("defense_suspend", r.Ms)
		}
	case "task":
		if r.Tokens > 0 {
			g.tokens = append(g.tokens, tokenMark{g.w.Now, r.Tokens})
			g.tokenSum += r.Tokens
		}
		if !r.OK && len(r.Err) > 7 && r.Err[:7] == "policy:" {
			r.Say, r.OK = "⛔ stopped by agentgateway", true
			g.w.event(Event{Kind: "policy", Agent: r.ID, Text: "rate limited"})
		}
	}
	g.w.Resolve(r)
}

// submit runs an intent. A refused spawn or suspend is resolved as failed at once so the
// world never waits on it; a refused delete is retried by the next sweep.
func (g *Game) submit(in Intent) {
	if in.Kind == "spawn" && g.paused {
		g.w.Resolve(Result{Kind: "spawn", ID: in.ID, Err: "paused"})
		return
	}
	name := fmt.Sprintf("r%d-%s-%s", g.w.Round.N, in.ID, in.Role)
	if g.r.Submit(in, name) {
		return
	}
	switch in.Kind {
	case "spawn", "suspend":
		g.w.Resolve(Result{Kind: in.Kind, ID: in.ID, Session: in.Session, Err: "busy"})
	case "delete":
		slog.Warn("cluster defense: delete refused, the next startup sweep will catch it", "session", in.Session)
	}
}

// tokensPerMin is task tokens over the last tokenWindow seconds. g.mu is held.
func (g *Game) tokensPerMin() int64 {
	i := 0
	for i < len(g.tokens) && g.w.Now-g.tokens[i].at > tokenWindow {
		g.tokenSum -= g.tokens[i].n
		i++
	}
	g.tokens = g.tokens[i:]
	return g.tokenSum
}

// housekeep takes idle players offline and evicts long-offline ships. g.mu is held.
func (g *Game) housekeep() {
	for pid, n := range g.conns {
		if s := g.w.Ships[pid]; n > 0 && s != nil && s.Online && g.w.Now-g.lastIn[pid] >= idleAfter {
			g.w.Leave(pid)
			g.offline[pid], g.idle[pid] = g.w.Now, true
		}
	}
	for pid, at := range g.offline {
		if g.w.Now-at >= evictAfter {
			g.w.Remove(pid)
			delete(g.offline, pid)
		}
	}
}

// online brings pl's ship online unless maxPlayers are already. g.mu is held.
func (g *Game) online(pl platform.Player) bool {
	if s := g.w.Ships[pl.ID]; s == nil || !s.Online {
		if g.w.online() >= maxPlayers {
			return false
		}
	}
	g.w.Join(pl.ID, pl.Nick, pl.Color)
	delete(g.offline, pl.ID)
	delete(g.idle, pl.ID)
	g.lastIn[pl.ID] = g.w.Now
	return true
}

// wake notes a frame from c's player; an idled player comes back online, or is refused
// when space filled up meanwhile. g.mu is held.
func (g *Game) wake(c *client) bool {
	if c.screen {
		return true
	}
	if !g.idle[c.player] {
		g.lastIn[c.player] = g.w.Now
		return true
	}
	return g.online(c.pl)
}

func (g *Game) leave(playerID string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.conns[playerID]--; g.conns[playerID] > 0 {
		return
	}
	delete(g.conns, playerID)
	delete(g.lastIn, playerID)
	delete(g.idle, playerID)
	if _, ok := g.offline[playerID]; !ok {
		g.offline[playerID] = g.w.Now
	}
	g.w.Leave(playerID)
}

var errStartingUp = errors.New("starting up, try again")

// play starts a round. g.mu is held.
func (g *Game) play() error {
	switch {
	case g.paused:
		return errors.New("paused")
	case g.listing:
		return errStartingUp
	}
	return g.w.Play()
}

// Pause stops (or allows again) new spawns.
func (g *Game) Pause(on bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.paused = on
}

// Stats is the game's line in /api/stats.
func (g *Game) Stats() map[string]any {
	g.mu.Lock()
	defer g.mu.Unlock()
	return map[string]any{"online": g.w.online(), "round": g.w.Round.N, "phase": string(g.w.Round.Phase),
		"running": g.w.Running(), "hosted": g.w.Hosted(), "slots": g.w.Slots, "suspendedThisRound": g.w.Round.Suspended,
		"tokensPerMin": g.tokensPerMin(), "bestSeconds": int(g.w.Best), "paused": g.paused}
}

// sweep deletes every session the subagent Agent has: in a fresh process nothing owns
// one yet, so they are all orphans of a previous run. Play waits for the list.
func (g *Game) sweep(ctx context.Context) {
	start := time.Now()
	lctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	ids, err := g.ops.ListSessions(lctx)
	cancel()
	g.mu.Lock()
	g.listing = false
	g.mu.Unlock()
	if err != nil {
		slog.Warn("cluster defense startup sweep: list failed", "err", err, "listed", len(ids))
	}
	n := deleteAll(ctx, g.ops, ids)
	slog.Info("cluster defense startup sweep", "orphans", len(ids), "deleted", n, "took", time.Since(start).Round(time.Millisecond))
}

// deleteAll deletes sessions, 8 at a time, and returns how many went.
func deleteAll(ctx context.Context, ops Ops, ids []string) int {
	var mu sync.Mutex
	var wg sync.WaitGroup
	n := 0
	sem := make(chan struct{}, 8)
	for _, id := range ids {
		wg.Add(1)
		go func() {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
			case <-ctx.Done():
				return
			}
			defer func() { <-sem }()
			dctx, cancel := context.WithTimeout(ctx, 10*time.Second)
			defer cancel()
			if err := ops.Delete(dctx, id); err != nil {
				slog.Warn("cluster defense: delete failed", "session", id, "err", err)
				return
			}
			mu.Lock()
			n++
			mu.Unlock()
		}()
	}
	wg.Wait()
	return n
}

// shutdown closes clients and the runner, then deletes the round's sessions (bounded).
func (g *Game) shutdown() {
	defer close(g.stopped)
	g.mu.Lock()
	for c := range g.clients {
		c.cancel()
	}
	close(g.done)
	var ids []string
	for _, a := range g.w.Agents {
		ids = append(ids, a.Session)
	}
	for _, s := range g.w.Stored {
		ids = append(ids, s)
	}
	g.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), shutdownWait)
	defer cancel()
	g.r.Close(ctx)
	if n := deleteAll(ctx, g.ops, ids); len(ids) > 0 {
		slog.Info("cluster defense: shutdown deleted sessions", "deleted", n, "of", len(ids))
	}
}
