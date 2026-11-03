// This file is the running game: one mutex around the World, Missions and Squads, a
// 15 Hz loop that ticks them, submits their ops to the Scheduler and routes the
// results back, and the cleanup that keeps agent sessions from leaking.
package heist

import (
	"context"
	"errors"
	"log/slog"
	"math/rand/v2"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/sebbycorp/kubecon-adventures/arcade/internal/platform"
)

const (
	maxPlayers    = 48
	tickHz        = 15
	stepSlots     = 24   // concurrent heist steps
	resumeSlots   = 8    // concurrent springs and extracts
	cleanupDelay  = 10.0 // seconds after a mission ends before its targets are deleted
	offlineAllies = 60.0 // seconds an owner may be offline before their allies are deleted
	zapTries      = 3    // attempts at a cleanup delete
)

// opKey names a submitted op: its kind and target (or ally, or cleanup key).
type opKey struct{ kind, target string }

// zap is a cleanup delete of a session nobody plays with any more.
type zap struct {
	session string
	tries   int
}

// Game is Grand Theft Agent's server.
type Game struct {
	p     *platform.Platform
	ops   Ops
	city  *City
	every time.Duration // tick interval

	mu       sync.Mutex
	w        *World
	m        *Missions
	q        *Squads
	s        *Scheduler
	inflight map[opKey]int       // ops accepted by s whose result has not been routed
	zaps     map[string]zap      // cleanup deletes waiting to be submitted, by key
	zapping  map[string]zap      // cleanup deletes in flight, by key
	allyMs   map[string]*Mission // the owner's active mission when a rig was built
	ended    map[*Mission]float64
	cleaned  map[*Mission]bool
	offline  map[string]float64 // world time each player went offline
	conns    map[string]int     // open connections per player
	clients  map[*client]bool
	paused   bool
	timings  map[string]int64 // p50 ms, refreshed once a second
	ticks    int
	done     chan struct{} // closed when Run stops
}

// New builds the game on a city with a district per node. Run starts it.
func New(p *platform.Platform, ops Ops, nodes []string) *Game {
	g := &Game{p: p, ops: ops, city: NewCity(nodes), every: time.Second / tickHz,
		conns: map[string]int{}, clients: map[*client]bool{}, timings: map[string]int64{}, done: make(chan struct{})}
	g.fresh()
	return g
}

// fresh replaces the world, missions, squads and scheduler. The caller holds g.mu (or
// has the only reference to g).
func (g *Game) fresh() {
	seed := uint64(time.Now().UnixNano())
	g.w = NewWorld(g.city)
	g.m = NewMissions(rand.New(rand.NewPCG(seed, seed>>1|1)))
	g.q = NewSquads(rand.New(rand.NewPCG(seed>>3, seed|1)))
	s := NewScheduler(g.ops, stepSlots, resumeSlots)
	s.ExtractUsesResume = true // checkpointing a suspended session fails; resume first
	rec := map[opKey]int{}
	s.onSubmit = func(kind, target string) { rec[opKey{kind, target}]++ } // Submit runs under g.mu
	g.s, g.inflight = s, rec
	g.zaps, g.zapping, g.allyMs = map[string]zap{}, map[string]zap{}, map[string]*Mission{}
	g.ended, g.cleaned, g.offline = map[*Mission]float64{}, map[*Mission]bool{}, map[string]float64{}
}

// Run starts the tick loop. It stops, closing every client and the scheduler, when ctx
// ends.
func (g *Game) Run(ctx context.Context) {
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

func (g *Game) shutdown() {
	g.mu.Lock()
	s := g.s
	for c := range g.clients {
		c.cancel()
	}
	close(g.done)
	g.mu.Unlock()
	s.Close()
}

// tick advances everything by one interval and broadcasts a snapshot. It never waits
// on an op: results are drained without blocking and Submit only queues.
func (g *Game) tick() {
	g.mu.Lock()
	g.drainResults()
	for _, in := range g.w.Tick(g.every.Seconds()) {
		g.submit(in)
	}
	g.m.Tick(g.w, g.s)
	g.housekeep()
	if g.ticks%tickHz == 0 {
		for _, k := range []string{"freeze", "spring", "extract"} {
			g.timings[k] = g.p.Timings.Typical("heist_" + k)
		}
	}
	g.ticks++
	f := g.framesLocked(g.w.DrainEvents())
	g.mu.Unlock()
	f.send()
}

func (g *Game) drainResults() {
	for {
		select {
		case r := <-g.s.Results():
			g.route(r)
		default:
			return
		}
	}
}

// route applies one Result: World.Resolve for control kinds, then Missions.OnResult.
// A result for an op the game has no record of is ignored. g.mu is held.
func (g *Game) route(r Result) {
	k := opKey{r.Kind, r.Target}
	if g.inflight[k] == 0 {
		return
	}
	if g.inflight[k]--; g.inflight[k] == 0 {
		delete(g.inflight, k)
	}
	if z, ok := g.zapping[r.Target]; ok {
		delete(g.zapping, r.Target)
		if z.tries++; !r.OK && z.tries < zapTries {
			g.zaps[r.Target] = z
		} else if !r.OK {
			slog.Warn("grand theft agent: cleanup delete failed", "session", z.session, "err", r.Err)
		}
		return
	}
	switch r.Kind {
	case "freeze", "spring", "extract":
		if r.OK {
			g.p.Timings.Record("heist_"+r.Kind, r.Ms)
		}
	case "spawn":
		if tr := g.m.targets[r.Target]; r.OK && tr != nil && tr.ms.Outcome != "" {
			tr.spawning = false // the mission is over: nobody will play this one
			g.zap(r.Spawned)
			return
		}
	}
	switch r.Kind {
	case "freeze", "extract", "delete", "spring":
		g.w.Resolve(r.Target, r.Kind, r.OK, r.Ms)
	}
	g.m.OnResult(r, g.w)
	if r.Kind == "ally" {
		if ms := g.allyMs[r.Target]; r.OK && ms != nil && ms.Outcome != "" {
			g.retire(r.Target) // its owner's mission ended while it was spawning
		}
		delete(g.allyMs, r.Target)
	}
}

// submit runs a World intent, mapping its target or ally ID to the session. If the
// scheduler refuses it, the busy flag is cleared so the world does not stall.
func (g *Game) submit(in Intent) {
	t, al := g.w.Targets[in.Target], g.w.Allies[in.Target]
	var sess string
	switch {
	case t != nil:
		sess = t.SessionID
	case al != nil:
		sess = al.SessionID
	}
	if g.s.Submit(in.Kind, in.Target, sess, 0, 0) {
		return
	}
	if t != nil {
		t.Busy, t.opBy = false, ""
	}
	if al != nil {
		al.Busy, al.opBy = false, ""
	}
}

// zap queues a cleanup delete of session. g.mu is held.
func (g *Game) zap(session string) {
	if session != "" {
		g.zaps["zap-"+session] = zap{session: session}
	}
}

// retire marks an ally for deletion; Missions.Tick submits it. g.mu is held.
func (g *Game) retire(allyID string) {
	if _, ok := g.m.retire[allyID]; !ok {
		g.m.retire[allyID] = false
	}
}

// housekeep submits cleanup deletes, clears out missions ended cleanupDelay ago, and
// retires the allies of owners offline for offlineAllies. g.mu is held.
func (g *Game) housekeep() {
	for _, ms := range g.m.missions {
		if ms.Outcome == "" || g.cleaned[ms] {
			continue
		}
		at, ok := g.ended[ms]
		if !ok {
			g.ended[ms] = g.w.Now
			continue
		}
		if g.w.Now-at < cleanupDelay-eps {
			continue
		}
		g.cleaned[ms] = true
		for _, id := range ms.Targets {
			t := g.w.Targets[id]
			if t == nil {
				continue
			}
			if t.State != Booked && t.State != Deleted {
				g.zap(t.SessionID)
			}
			g.w.release(id)
			delete(g.w.Targets, id)
		}
	}
	for pid, at := range g.offline {
		if g.w.Now-at < offlineAllies-eps {
			continue
		}
		for id, al := range g.w.Allies {
			if al.Owner == pid {
				g.retire(id)
			}
		}
	}
	for key, z := range g.zaps {
		if g.s.Submit("delete", key, z.session, 0, 0) {
			delete(g.zaps, key)
			g.zapping[key] = z
		}
	}
}

// ensureAvatar returns pl's avatar, creating it at Dispatch in their squad (solo by
// default). g.mu is held.
func (g *Game) ensureAvatar(pl platform.Player) *Avatar {
	if a := g.w.Avatars[pl.ID]; a != nil {
		return a
	}
	sq := g.q.Of(pl.ID)
	if sq == "" {
		sq = g.q.Solo(pl.ID)
	}
	d := g.city.Dispatch
	a := NewAvatar(pl.ID, pl.Nick, pl.Color, sq, Vec{d.X + d.W/2, d.Y + d.H/2})
	g.w.Avatars[pl.ID] = a
	return a
}

// join brings pl online, creating their avatar. It is false when the city is full.
func (g *Game) join(pl platform.Player) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.joinLocked(pl)
}

func (g *Game) joinLocked(pl platform.Player) bool {
	if g.w.Avatars[pl.ID] == nil && len(g.w.Avatars) >= maxPlayers {
		return false
	}
	g.ensureAvatar(pl).Online = true
	g.conns[pl.ID]++
	delete(g.offline, pl.ID)
	return true
}

// leave closes one of a player's connections; with none left the avatar goes offline.
func (g *Game) leave(playerID string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.conns[playerID]--; g.conns[playerID] > 0 {
		return
	}
	delete(g.conns, playerID)
	if a := g.w.Avatars[playerID]; a != nil {
		a.Online = false
		g.offline[playerID] = g.w.Now
	}
	g.w.Input(playerID, Input{})
}

// start begins a mission for the player's squad. g.mu is held.
func (g *Game) start(playerID string, level int) (*Mission, error) {
	a := g.w.Avatars[playerID]
	switch {
	case g.paused:
		return nil, errors.New("paused")
	case a == nil:
		return nil, errors.New("not in the city")
	}
	return g.m.Start(a.Squad, level, g.w, g.s)
}

// build spends a rig from the player's stash and spawns the ally. g.mu is held.
func (g *Game) build(playerID string) error {
	in, err := g.w.BuildRig(playerID)
	if err != nil {
		return err
	}
	if !g.s.Submit("ally", in.Target, "", 0, 0) {
		g.w.cancelRig(in.Target)
		return errors.New("busy, try again")
	}
	if a := g.w.Avatars[playerID]; a != nil && g.m.active[a.Squad] != nil {
		g.allyMs[in.Target] = g.m.active[a.Squad]
	}
	return nil
}

// squad changes the player's squad and returns its code. g.mu is held.
func (g *Game) squad(playerID, op, code string) (string, error) {
	switch op {
	case "solo":
		code = g.q.Solo(playerID)
	case "create":
		code = g.q.Create(playerID)
	case "join":
		code = strings.ToUpper(strings.TrimSpace(code))
		if err := g.q.Join(playerID, code); err != nil {
			return "", err
		}
	default:
		return "", errors.New("unknown squad op")
	}
	if a := g.w.Avatars[playerID]; a != nil {
		a.Squad = code
	}
	return code, nil
}

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

// Pause stops (or allows again) new missions.
func (g *Game) Pause(on bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.paused = on
}

// Stats is the game's line in /api/stats.
func (g *Game) Stats() map[string]any {
	g.mu.Lock()
	defer g.mu.Unlock()
	online, active, takedowns := 0, 0, 0
	for _, a := range g.w.Avatars {
		if a.Online {
			online++
		}
	}
	for _, ms := range g.m.missions {
		if ms.Outcome == "" {
			active++
		}
	}
	for _, n := range g.m.SquadTotals() {
		takedowns += n
	}
	return map[string]any{"players": len(g.w.Avatars), "online": online, "targets": len(g.w.Targets),
		"allies": len(g.w.Allies), "missions": len(g.m.missions), "activeMissions": active,
		"takedowns": takedowns, "paused": g.paused}
}
