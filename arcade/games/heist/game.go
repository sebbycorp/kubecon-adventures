// This file is the running game: one mutex around the World, Missions and Squads, a
// 15 Hz loop that ticks them, submits their ops to the Scheduler and routes the
// results back, and the cleanup that keeps agent sessions from leaking.
package heist

import (
	"context"
	"errors"
	"log/slog"
	"math/rand/v2"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/sebbycorp/kubecon-adventures/arcade/internal/platform"
)

const (
	maxPlayers    = 48
	tickHz        = 15
	stepSlots     = 24    // concurrent heist steps
	resumeSlots   = 8     // concurrent springs and extracts
	cleanupDelay  = 10.0  // seconds after a mission ends before its targets are deleted
	offlineAllies = 60.0  // seconds an owner may be offline before their allies are deleted
	evictAfter    = 300.0 // seconds offline before an avatar leaves the city
	maxScreens    = 8     // concurrent public ?screen=1 spectators (LAN screens are exempt)
	idleAfter     = 600.0 // seconds without input before a connected player counts as offline
	zapTries      = 3     // attempts at a cleanup delete
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

	mu         sync.Mutex
	w          *World
	m          *Missions
	q          *Squads
	s          *Scheduler
	inflight   map[opKey]int       // ops accepted by s whose result has not been routed
	zaps       map[string]zap      // cleanup deletes waiting to be submitted, by key
	zapping    map[string]zap      // cleanup deletes in flight, by key
	allyMs     map[string]*Mission // the owner's active mission when a rig was built
	ended      map[*Mission]float64
	cleaned    map[*Mission]bool
	offline    map[string]float64 // world time each player went offline
	lastIn     map[string]float64 // world time of each connected player's last frame
	idle       map[string]bool    // connected players taken offline for idling
	conns      map[string]int     // open connections per player
	seq        uint64             // connections attached so far
	clients    map[*client]bool
	pendIn     map[*client]Input // coalesced input frames waiting for a rate-limit token
	booked     map[string]bool // sessions of Booked targets removed from the world; Reset deletes them
	paused     bool
	listing    bool             // the startup sweep is still listing sessions; no spawns yet
	timings    map[string]int64 // p50 ms, refreshed once a second
	podHourUSD float64          // POD_HOUR_USD, for the big screen's cost estimate
	ticks      int
	done       chan struct{} // closed when Run stops
}

// New builds the game on a city with a district per node. Run starts it.
func New(p *platform.Platform, ops Ops, nodes []string) *Game {
	g := &Game{p: p, ops: ops, city: NewCity(nodes), every: time.Second / tickHz,
		conns: map[string]int{}, booked: map[string]bool{}, clients: map[*client]bool{}, pendIn: map[*client]Input{}, timings: map[string]int64{}, done: make(chan struct{}),
		podHourUSD: podHourFromEnv()}
	g.fresh()
	return g
}

// podHourFromEnv is POD_HOUR_USD, or $0.02 (roughly 0.25 vCPU and 512 MiB on a public
// cloud), the same default Agent Flow uses.
func podHourFromEnv() float64 {
	price, err := strconv.ParseFloat(os.Getenv("POD_HOUR_USD"), 64)
	if err != nil || price <= 0 {
		return 0.02
	}
	return price
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
	if m, ok := g.ops.(interface{ ExtractUsesResume() bool }); ok {
		s.ExtractUsesResume = m.ExtractUsesResume() // HEIST_EXTRACT=direct
	}
	rec := map[opKey]int{}
	s.onSubmit = func(kind, target string) { rec[opKey{kind, target}]++ } // Submit runs under g.mu
	g.s, g.inflight = s, rec
	g.zaps, g.zapping, g.allyMs = map[string]zap{}, map[string]zap{}, map[string]*Mission{}
	g.ended, g.cleaned, g.offline = map[*Mission]float64{}, map[*Mission]bool{}, map[string]float64{}
	g.lastIn, g.idle = map[string]float64{}, map[string]bool{}
}

// Run starts the tick loop and the startup sweep of orphaned sessions (sweep.go). It
// stops, closing every client and the scheduler, when ctx ends.
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
	g.flushInputs(time.Now())
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
	if !r.OK {
		// Errors carry kagent's status reason, never the agent's reply text.
		slog.Warn("grand theft agent: op failed", "kind", r.Kind, "target", r.Target, "ms", r.Ms, "err", r.Err)
	}
	switch r.Kind {
	case "freeze", "spring", "extract":
		if r.OK {
			g.p.Timings.Record("heist_"+r.Kind, r.Ms)
		}
	case "spawn":
		if tr := g.m.targets[r.Target]; r.OK && (tr == nil || tr.ms.Outcome != "") {
			if tr != nil {
				tr.spawning = false
			}
			g.zap(r.Spawned) // the mission is over (or pruned): nobody will play this one
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
		t.Busy, t.opBy, t.opKind = false, "", ""
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

// housekeep submits cleanup deletes, clears out missions ended cleanupDelay ago (and
// forgets them missionShow later), takes connected players who sent nothing for
// idleAfter offline, retires the allies of owners offline for offlineAllies, and evicts
// avatars offline for evictAfter. g.mu is held.
func (g *Game) housekeep() {
	keep := g.m.missions[:0]
	for _, ms := range g.m.missions {
		if g.cleaned[ms] && g.w.Now-g.ended[ms] > missionShow {
			g.prune(ms)
			continue
		}
		keep = append(keep, ms)
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
			switch t.State {
			case Booked:
				g.booked[t.SessionID] = true
			case Deleted:
			default:
				g.zap(t.SessionID)
			}
			g.w.release(id)
			delete(g.w.Targets, id)
		}
	}
	clear(g.m.missions[len(keep):])
	g.m.missions = keep
	for pid, n := range g.conns {
		if a := g.w.Avatars[pid]; n > 0 && a != nil && a.Online && g.w.Now-g.lastIn[pid] >= idleAfter-eps {
			a.Online, g.offline[pid], g.idle[pid] = false, g.w.Now, true // idle: their slot frees
			g.w.Input(pid, Input{})
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
		if g.w.Now-at >= evictAfter-eps {
			g.evict(pid)
		}
	}
	for key, z := range g.zaps {
		if g.s.Submit("delete", key, z.session, 0, 0) {
			delete(g.zaps, key)
			g.zapping[key] = z
		}
	}
}

// prune forgets an ended, cleaned-up mission. Scores live in Missions.players and
// squadTD, so the leaderboard keeps it. g.mu is held.
func (g *Game) prune(ms *Mission) {
	delete(g.ended, ms)
	delete(g.cleaned, ms)
	for _, id := range ms.Targets {
		delete(g.m.targets, id)
	}
}

// evict removes a long-offline avatar from the city (its allies are already retiring).
// Its stash and score stay; coming back gives a fresh avatar at Dispatch. g.mu is held.
func (g *Game) evict(pid string) {
	if a := g.w.Avatars[pid]; a != nil {
		g.w.drop(a)
	}
	delete(g.w.Avatars, pid)
	delete(g.w.ctl, pid)
	delete(g.offline, pid)
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
	if !g.online(pl) {
		return false
	}
	g.conns[pl.ID]++
	return true
}

// online brings pl's avatar online (creating it if needed), unless the city already has
// maxPlayers online. g.mu is held.
func (g *Game) online(pl platform.Player) bool {
	if a := g.w.Avatars[pl.ID]; a == nil || !a.Online {
		online := 0
		for _, o := range g.w.Avatars {
			if o.Online {
				online++
			}
		}
		if online >= maxPlayers {
			return false
		}
	}
	g.ensureAvatar(pl).Online = true
	delete(g.offline, pl.ID)
	delete(g.idle, pl.ID)
	g.lastIn[pl.ID] = g.w.Now
	return true
}

// wake notes a frame from c's player. A player taken offline for idling comes back
// online, as on a reconnect; it is false (and they stay offline) when the city is full.
// g.mu is held.
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

// leave closes one of a player's connections; with none left the avatar goes offline.
func (g *Game) leave(playerID string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.conns[playerID]--; g.conns[playerID] > 0 {
		return
	}
	delete(g.conns, playerID)
	delete(g.lastIn, playerID)
	delete(g.idle, playerID)
	if a := g.w.Avatars[playerID]; a != nil {
		if _, ok := g.offline[playerID]; !ok || a.Online { // an idle player's clock keeps running
			g.offline[playerID] = g.w.Now
		}
		a.Online = false
	}
	g.w.Input(playerID, Input{})
}

// start begins a mission for the player's squad. g.mu is held.
func (g *Game) start(playerID string, level int) (*Mission, error) {
	a := g.w.Avatars[playerID]
	switch {
	case g.paused:
		return nil, errors.New("paused")
	case g.listing:
		return nil, errStartingUp
	case a == nil:
		return nil, errors.New("not in the city")
	}
	for _, ms := range g.m.missions {
		if ms.Outcome == "" && ms.Starter == playerID {
			return nil, errors.New("you already have a mission running")
		}
	}
	ms, err := g.m.Start(a.Squad, level, g.w, g.s)
	if ms != nil {
		ms.Starter = playerID
	}
	return ms, err
}

// build spends a rig from the player's stash and spawns the ally. g.mu is held.
func (g *Game) build(playerID string) error {
	if g.listing {
		return errStartingUp
	}
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

// squad changes the player's squad and returns its code. It is refused while the
// player's squad has a mission running or queued, so hopping squads can't start one
// mission per squad. g.mu is held.
func (g *Game) squad(playerID, op, code string) (string, error) {
	cur := g.q.Of(playerID)
	if a := g.w.Avatars[playerID]; a != nil {
		cur = a.Squad
	}
	if g.m.active[cur] != nil {
		return "", errors.New("finish your mission first")
	}
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
