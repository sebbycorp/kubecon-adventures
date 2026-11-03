// This file is the running game: one mutex around the World, Missions and Squads, a
// 15 Hz loop that ticks them, submits their ops to the Scheduler and routes the
// results back, and the cleanup that keeps agent sessions from leaking.
package heist

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"math/rand/v2"
	"os"
	"slices"
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
	cleanupDelay  = 10.0  // seconds after a run ends before its remaining agents are deleted
	goneDelay     = 3.0   // seconds an escaped, booked or deleted agent stays on the map
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

// Game is Substrate Siege's server.
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
	gone       map[string]float64 // world time each agent became escaped, booked or deleted
	offline    map[string]float64 // world time each player went offline
	lastIn     map[string]float64 // world time of each connected player's last frame
	idle       map[string]bool    // connected players taken offline for idling
	conns      map[string]int     // open connections per player
	seq        uint64             // connections attached so far
	clients    map[*client]bool
	pendIn     map[*client]Input // coalesced input frames waiting for a rate-limit token
	booked     map[string]string // checkpoint IDs of bookings no longer in the world, by session; Reset deletes them
	paused     bool
	listing    bool             // the startup sweep is still listing sessions; no spawns yet
	timings    map[string]int64 // p50 ms, refreshed once a second
	podHourUSD float64          // POD_HOUR_USD, for the big screen's cost estimate
	ticks      int
	done       chan struct{} // closed when Run stops
	stopped    chan struct{} // closed once shutdown has deleted the booked checkpoints
}

// New builds the game on a city with a district per node. Run starts it.
func New(p *platform.Platform, ops Ops, nodes []string) *Game {
	g := &Game{p: p, ops: ops, city: NewCity(nodes), every: time.Second / tickHz,
		conns: map[string]int{}, booked: map[string]string{}, clients: map[*client]bool{}, pendIn: map[*client]Input{}, timings: map[string]int64{}, done: make(chan struct{}), stopped: make(chan struct{}),
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
	g.gone = map[string]float64{}
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

// shutdownCleanup bounds shutdown's checkpoint deletes (the pod's grace period is 30 s).
const shutdownCleanup = 15 * time.Second

// shutdown closes every client and the scheduler, then deletes the booked targets'
// checkpoints: their sessions are gone, so the next process's startup sweep can't find
// them (kagent lists checkpoints only per session).
func (g *Game) shutdown() {
	defer close(g.stopped)
	g.mu.Lock()
	s := g.s
	for c := range g.clients {
		c.cancel()
	}
	close(g.done)
	var cps []string
	for _, t := range g.w.Targets {
		if t.Checkpoint != "" {
			cps = append(cps, t.Checkpoint)
		}
	}
	for _, cp := range g.booked {
		if cp != "" {
			cps = append(cps, cp)
		}
	}
	g.booked = map[string]string{}
	g.mu.Unlock()
	s.Close()
	if len(cps) == 0 {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), shutdownCleanup)
	defer cancel()
	n := g.deleteCheckpoints(ctx, cps)
	slog.Info("substrate siege: shutdown deleted booked checkpoints", "deleted", n, "booked", len(cps))
}

// Stopped is closed once the game has shut down (Run's ctx ended) and cleaned up.
func (g *Game) Stopped() <-chan struct{} { return g.stopped }

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
	g.autoBuild()
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
			slog.Warn("substrate siege: cleanup delete failed", "session", z.session, "err", r.Err)
		}
		return
	}
	if !r.OK {
		// Errors carry kagent's status reason, never the agent's reply text.
		slog.Warn("substrate siege: op failed", "kind", r.Kind, "target", r.Target, "ms", r.Ms, "err", r.Err)
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
	if r.Kind == "extract" && r.OK {
		// The session is gone; its checkpoint stays until Reset deletes it.
		if t := g.w.Targets[r.Target]; t != nil {
			t.Checkpoint = r.Checkpoint
		} else {
			g.booked["extract-"+r.Target] = r.Checkpoint // landed after cleanup
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
		if in.Kind == "freeze" {
			g.s.Drop("step", in.Target) // a step still queued would wake it again
		}
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

// housekeep submits cleanup deletes, clears agents out goneDelay after they escaped or
// were booked or deleted, clears out runs ended cleanupDelay ago (and forgets them
// missionShow later), takes connected players who sent nothing for idleAfter offline,
// retires the allies of owners offline for offlineAllies, and evicts avatars offline
// for evictAfter. g.mu is held.
func (g *Game) housekeep() {
	for id, t := range g.w.Targets {
		switch {
		case t.Busy || t.State != Escaped && t.State != Booked && t.State != Deleted:
		case g.gone[id] == 0:
			g.gone[id] = g.w.Now + eps
		case g.w.Now-g.gone[id] >= goneDelay-eps:
			g.clearTarget(id)
		}
	}
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
			g.clearTarget(id)
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

// clearTarget takes an agent off the map: a booked one's checkpoint is kept for Reset,
// a deleted one is already gone, and any other session is deleted. g.mu is held.
func (g *Game) clearTarget(id string) {
	t := g.w.Targets[id]
	if t == nil {
		return
	}
	switch {
	case t.State == Booked || t.Checkpoint != "": // Extract deleted the session
		g.booked[t.SessionID] = t.Checkpoint
	case t.State == Deleted:
	default:
		g.zap(t.SessionID)
	}
	delete(g.w.Targets, id)
	delete(g.gone, id)
}

// prune forgets an ended, cleaned-up run. Scores live in Missions.players and
// squadTD, so the leaderboard keeps it. g.mu is held.
func (g *Game) prune(ms *Mission) {
	delete(g.ended, ms)
	delete(g.cleaned, ms)
	for _, id := range ms.Targets {
		delete(g.m.targets, id)
	}
}

// evict removes a long-offline avatar from the city (its allies are already retiring).
// Its score stays; coming back gives a fresh avatar at Dispatch. g.mu is held.
func (g *Game) evict(pid string) {
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

// start begins a run for the player's squad. g.mu is held.
func (g *Game) start(playerID string) (*Mission, error) {
	a := g.w.Avatars[playerID]
	switch {
	case g.paused:
		return nil, errors.New("paused")
	case g.listing:
		return nil, errStartingUp
	case a == nil:
		return nil, errors.New("not in the city")
	}
	ms, err := g.m.Start(a.Squad, g.w, g.s)
	if ms != nil {
		ms.Starter = playerID
	}
	return ms, err
}

// autoBuild deploys an ally for every online player carrying a full rig, while they
// have room for one. g.mu is held.
func (g *Game) autoBuild() {
	if g.listing || g.ticks%tickHz != 0 { // once a second is plenty
		return
	}
	for _, id := range slices.Sorted(maps.Keys(g.w.Avatars)) {
		if a := g.w.Avatars[id]; a.Online && !a.Down && HasRig(a.Inv) {
			_ = g.build(id) // ally or city limit: the parts wait in the inventory
		}
	}
}

// build spends a rig from the player's inventory and spawns the ally. g.mu is held.
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
// player's squad has a run going, so hopping squads can't start one run per squad.
// g.mu is held.
func (g *Game) squad(playerID, op, code string) (string, error) {
	cur := g.q.Of(playerID)
	if a := g.w.Avatars[playerID]; a != nil {
		cur = a.Squad
	}
	if g.m.active[cur] != nil {
		return "", errors.New("finish your run first")
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

// Pause stops (or allows again) new runs.
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
		"allies": len(g.w.Allies), "runs": len(g.m.missions), "activeRuns": active,
		"freezes": takedowns, "paused": g.paused}
}

// Top is the leaderboard for the arcade's /api/boards: the n best single-run scores.
func (g *Game) Top(n int) []platform.BoardRow {
	g.mu.Lock()
	defer g.mu.Unlock()
	out := []platform.BoardRow{}
	for _, r := range g.m.Leaderboard() {
		if len(out) == n || r.Best == 0 {
			break
		}
		row := platform.BoardRow{Nick: r.Nick, Score: r.Best, Detail: fmt.Sprintf("%d freezes · %d booked", r.Freezes, r.Books)}
		if a := g.w.Avatars[r.PlayerID]; a != nil {
			row.Color = a.Color
		}
		out = append(out, row)
	}
	return out
}
