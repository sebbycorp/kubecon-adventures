// Package defense is Cluster Defense: a co-op space shooter where every enemy is a real
// kagent agent on Agent Substrate. Agents spin up at the edge of space, fly to the
// cluster station and run one real task turn there, each holding a worker. Players fly
// ships and shoot them: two hits suspend the agent for real, freeing its worker as a
// snapshot that drifts to Snapshot Storage. When every worker of the pool is busy the
// cluster is full and the round is over.
//
// This file is the pure world: ships, agents, bullets and rounds. It never talks to
// kagent; Tick returns Intents for the Game to run and the Game reports results back
// through Resolve.
package defense

import (
	"errors"
	"fmt"
	"maps"
	"math"
	"math/rand/v2"
	"slices"
)

// Vec is a point or velocity in arena units.
type Vec struct{ X, Y float64 }

func (a Vec) add(b Vec) Vec            { return Vec{a.X + b.X, a.Y + b.Y} }
func (a Vec) sub(b Vec) Vec            { return Vec{a.X - b.X, a.Y - b.Y} }
func (a Vec) mul(f float64) Vec        { return Vec{a.X * f, a.Y * f} }
func (a Vec) len() float64             { return math.Hypot(a.X, a.Y) }
func dist(a, b Vec) float64            { return a.sub(b).len() }
func finite(f float64) bool            { return !math.IsNaN(f) && !math.IsInf(f, 0) }
func clampf(f, lo, hi float64) float64 { return max(lo, min(hi, f)) }

// The arena, in units. The cluster station sits in the middle; Snapshot Storage in the
// bottom-right corner.
const (
	ArenaW = 160.0
	ArenaH = 100.0
)

var (
	Cluster = Vec{ArenaW / 2, ArenaH / 2}
	Storage = Vec{ArenaW - 14, ArenaH - 12}
)

// Tuning, in units and seconds.
const (
	shipSpeed     = 26.0
	shipAccel     = 70.0
	shipDrag      = 3.0 // per second, when the stick is centred
	shipMargin    = 2.0
	deadZone      = 0.12
	fireCD        = 0.16
	bulletSpeed   = 75.0
	bulletLife    = 0.5 // so a shot reaches 37.5 units
	assistRange   = 36.0
	assistCone    = 22 * math.Pi / 180
	agentRadius   = 2.2
	hitsToSuspend = 2
	orbitMin      = 8.0
	orbitMax      = 15.0
	orbitSpeed    = 1.6 // units per second along the orbit
	capsuleSpeed  = 14.0
	firstSpawn    = 2.0  // seconds into a round
	spawnBase     = 3.2  // seconds between spawns at the start, one player
	spawnFloor    = 0.6  // ... falling to this
	spawnRamp     = 100  // seconds for the gap to shrink by e
	playerBoost   = 0.35 // each extra online player spawns 35% more
	spawnMin      = 0.7  // never faster than this: ~85 task turns a minute, about the 100k token budget
	waveEvery     = 30.0 // a wave of several agents at once
	abandonAfter  = 30.0 // a live round with nobody online ends
	overMin       = 3.0  // PLAY can restart this long after a round ends
	nextRound     = 15.0 // ... and the next round starts on its own after this
	maxEvents     = 64
	DefaultSlots  = 16
)

// Role is a kind of subagent: each has its own look on the client and its own task.
type Role struct {
	Name  string
	Speed float64
}

// Roles are the subagent kinds, in spawn rotation order.
var Roles = []Role{
	{"planner", 5}, {"coder", 6}, {"tester", 7}, {"researcher", 4.5},
	{"reviewer", 5.5}, {"deployer", 8}, {"summarizer", 6.5}, {"scraper", 7.5},
}

func roleSpeed(name string) float64 {
	for _, r := range Roles {
		if r.Name == name {
			return r.Speed
		}
	}
	return 6
}

// AgentState is where an agent is in its life.
type AgentState string

const (
	Running    AgentState = "running"    // holds a worker (flying in, or at the cluster)
	Suspending AgentState = "suspending" // two hits landed, the suspend is in flight
	Suspended  AgentState = "suspended"  // a snapshot capsule drifting to storage
)

// Ship is a player.
type Ship struct {
	ID, Nick, Color string
	Pos, Vel        Vec
	Heading         float64 // radians, 0 = +x
	FireCD          float64
	Online          bool
	Score           int // suspends this round
	Total           int // suspends ever
}

// Agent is one subagent session in space.
type Agent struct {
	ID, Session, Role, Name string
	Pos                     Vec
	State                   AgentState
	Hits                    int
	Say                     string // its task line, once its turn answered
	Done                    bool   // its task turn finished
	Arrived                 bool   // reached its orbit round the cluster
	By                      string // player whose shot started the suspend
	SuspendMs               int64
	orbitR, orbitA, orbitW  float64
	seen                    bool // its task intent has been issued
}

// Bullet is a shot in flight.
type Bullet struct {
	ID       int
	Pos, Vel Vec
	Life     float64
	Owner    string
}

// Event is something the screens and phones are told about.
type Event struct {
	Kind, Agent, Player, Text string
	Ms                        int64
}

// Input is a player's controls: the stick (each axis -1..1) and FIRE.
type Input struct {
	Move Vec
	Fire bool
}

// Intent asks the Game to run an op: "spawn" (a new session for agent ID with Role),
// "task", "suspend" or "delete" (of Session).
type Intent struct {
	Kind, ID, Session, Role, By string
}

// Result is what an op came back with. For "spawn", Session is the new session.
type Result struct {
	Kind, ID, Session string
	OK                bool
	Err               string
	Say               string // "task": the agent's line
	Tokens            int64
	Ms                int64
}

// Phase of the round: idle (no round yet), live, or over.
type Phase string

const (
	Idle Phase = "idle"
	Live Phase = "live"
	Over Phase = "over"
)

// Round is one game: it starts on PLAY and ends when the cluster is full.
type Round struct {
	N              int
	Phase          Phase
	Started, Ended float64
	Suspended      int
	Spawned        int
	Peak           int    // most agents running at once
	PeakHosted     int    // most sessions (running and suspended) at once
	Reason         string // "full" or "abandoned"
	nextSpawn      float64
	nextWave       float64
	waves          int
	empty          float64 // world time the last player went offline, while live
}

// pending is a spawn whose session has not come back yet.
type pending struct {
	role  string
	round int
	pos   Vec
}

// World is the whole simulation. It is not safe for concurrent use.
type World struct {
	Ships   map[string]*Ship
	Agents  map[string]*Agent
	Bullets []*Bullet
	Stored  map[string]string // agent ID -> session, snapshots that reached storage this round
	Round   Round
	Now     float64
	Slots   int // workers in the pool: the cluster is full at this many running agents
	Events  []Event
	Best    float64 // longest round survived, seconds

	rnd      *rand.Rand
	in       map[string]Input
	spawning map[string]pending
	out      []Intent
	seq      int
	bseq     int
	roleI    int
}

// NewWorld is an empty world using rnd for spawns.
func NewWorld(rnd *rand.Rand) *World {
	return &World{Ships: map[string]*Ship{}, Agents: map[string]*Agent{}, Stored: map[string]string{},
		Round: Round{Phase: Idle}, Slots: DefaultSlots, rnd: rnd, in: map[string]Input{}, spawning: map[string]pending{}}
}

// Join brings a player's ship online, creating it next to the cluster.
func (w *World) Join(id, nick, color string) *Ship {
	s := w.Ships[id]
	if s == nil {
		a := w.rnd.Float64() * 2 * math.Pi
		s = &Ship{ID: id, Nick: nick, Color: color, Pos: Cluster.add(Vec{math.Cos(a), math.Sin(a)}.mul(orbitMax + 6)), Heading: a}
		w.Ships[id] = s
	}
	s.Nick, s.Color, s.Online = nick, color, true
	return s
}

// Leave takes a player's ship offline (it stays, drifting, and keeps its score).
func (w *World) Leave(id string) {
	if s := w.Ships[id]; s != nil {
		s.Online = false
	}
	delete(w.in, id)
}

// Remove forgets a ship.
func (w *World) Remove(id string) {
	delete(w.Ships, id)
	delete(w.in, id)
}

// Input records a player's controls; the latest value holds until replaced.
func (w *World) Input(id string, in Input) {
	if !finite(in.Move.X) || !finite(in.Move.Y) {
		in.Move = Vec{}
	}
	in.Move = Vec{clampf(in.Move.X, -1, 1), clampf(in.Move.Y, -1, 1)}
	w.in[id] = in
}

// ErrTooSoon refuses PLAY right after a round ends, so the summary can be read.
var ErrTooSoon = errors.New("the next round starts in a moment")

// Play starts a round: from idle at once, after a round ends once overMin has passed.
// During a live round it does nothing.
func (w *World) Play() error {
	switch w.Round.Phase {
	case Live:
		return nil
	case Over:
		if w.Now-w.Round.Ended < overMin {
			return ErrTooSoon
		}
	}
	w.start()
	return nil
}

func (w *World) start() {
	w.Round = Round{N: w.Round.N + 1, Phase: Live, Started: w.Now, nextSpawn: w.Now + firstSpawn, nextWave: w.Now + waveEvery}
	for _, s := range w.Ships {
		s.Score = 0
	}
	w.Bullets = nil
	w.event(Event{Kind: "round", Text: fmt.Sprint(w.Round.N)})
}

// Running counts agents holding a worker: running ones and those whose suspend is still
// in flight (Substrate frees the worker only once the snapshot is taken).
func (w *World) Running() int {
	n := 0
	for _, a := range w.Agents {
		if a.State != Suspended {
			n++
		}
	}
	return n
}

// Spawning counts sessions asked for and not back yet.
func (w *World) Spawning() int { return len(w.spawning) }

// Hosted counts every live session of the round: running, suspending, and snapshots in
// flight or in storage. More than Slots of them is what Substrate makes possible.
func (w *World) Hosted() int { return len(w.Agents) + len(w.Stored) }

func (w *World) online() int {
	n := 0
	for _, s := range w.Ships {
		if s.Online {
			n++
		}
	}
	return n
}

// DrainEvents returns the pending events and clears them.
func (w *World) DrainEvents() []Event {
	ev := w.Events
	w.Events = nil
	return ev
}

func (w *World) event(e Event) {
	if len(w.Events) < maxEvents {
		w.Events = append(w.Events, e)
	}
}

// Tick advances the world by dt seconds and returns the ops to run.
func (w *World) Tick(dt float64) []Intent {
	if !finite(dt) || dt < 0 {
		dt = 0
	}
	w.Now += dt
	for _, id := range slices.Sorted(maps.Keys(w.Ships)) {
		w.tickShip(w.Ships[id], dt)
	}
	w.tickBullets(dt)
	w.tickAgents(dt)
	w.tickRound()
	out := w.out
	w.out = nil
	return out
}

func (w *World) tickShip(s *Ship, dt float64) {
	s.FireCD = max(0, s.FireCD-dt)
	in := w.in[s.ID]
	if !s.Online {
		in = Input{}
	}
	m := in.Move.len()
	if m > deadZone {
		dir := in.Move.mul(1 / m)
		s.Vel = s.Vel.add(dir.mul(shipAccel * min(m, 1) * dt))
		s.Heading = math.Atan2(dir.Y, dir.X)
	} else {
		s.Vel = s.Vel.mul(math.Exp(-shipDrag * dt))
	}
	if v := s.Vel.len(); v > shipSpeed {
		s.Vel = s.Vel.mul(shipSpeed / v)
	}
	s.Pos = s.Pos.add(s.Vel.mul(dt))
	if s.Pos.X < shipMargin || s.Pos.X > ArenaW-shipMargin {
		s.Pos.X, s.Vel.X = clampf(s.Pos.X, shipMargin, ArenaW-shipMargin), 0
	}
	if s.Pos.Y < shipMargin || s.Pos.Y > ArenaH-shipMargin {
		s.Pos.Y, s.Vel.Y = clampf(s.Pos.Y, shipMargin, ArenaH-shipMargin), 0
	}
	if in.Fire && s.FireCD <= 1e-9 && w.Round.Phase == Live {
		w.fire(s)
	}
}

// fire shoots along the ship's heading, bent toward the nearest running agent inside
// the assist cone (aimed where it will be when the shot arrives).
func (w *World) fire(s *Ship) {
	s.FireCD = fireCD
	dir := Vec{math.Cos(s.Heading), math.Sin(s.Heading)}
	var best *Agent
	bestD := assistRange
	for _, id := range slices.Sorted(maps.Keys(w.Agents)) {
		a := w.Agents[id]
		if a.State != Running {
			continue
		}
		to := a.Pos.sub(s.Pos)
		d := to.len()
		if d >= bestD || d < 1e-9 {
			continue
		}
		if ang := math.Acos(clampf((to.X*dir.X+to.Y*dir.Y)/d, -1, 1)); ang <= assistCone {
			best, bestD = a, d
		}
	}
	if best != nil {
		aim := best.Pos.add(w.agentVel(best).mul(bestD / bulletSpeed)).sub(s.Pos)
		if n := aim.len(); n > 1e-9 {
			dir = aim.mul(1 / n)
		}
	}
	w.bseq++
	w.Bullets = append(w.Bullets, &Bullet{ID: w.bseq, Pos: s.Pos.add(dir.mul(1.5)), Vel: dir.mul(bulletSpeed), Life: bulletLife, Owner: s.ID})
}

// tickBullets moves shots and lands them: a shot hits the first running agent its path
// crosses this tick.
func (w *World) tickBullets(dt float64) {
	keep := w.Bullets[:0]
	ids := slices.Sorted(maps.Keys(w.Agents))
	for _, b := range w.Bullets {
		from, to := b.Pos, b.Pos.add(b.Vel.mul(dt))
		var hit *Agent
		bestT := math.Inf(1)
		for _, id := range ids {
			a := w.Agents[id]
			if a.State != Running {
				continue
			}
			if t, ok := segmentHit(from, to, a.Pos, agentRadius); ok && t < bestT {
				hit, bestT = a, t
			}
		}
		if hit != nil {
			w.hit(hit, b.Owner)
			continue
		}
		b.Pos, b.Life = to, b.Life-dt
		if b.Life > 0 && b.Pos.X > -5 && b.Pos.X < ArenaW+5 && b.Pos.Y > -5 && b.Pos.Y < ArenaH+5 {
			keep = append(keep, b)
		}
	}
	clear(w.Bullets[len(keep):])
	w.Bullets = keep
}

// segmentHit reports whether the segment a->b passes within r of c, and where (0..1).
func segmentHit(a, b, c Vec, r float64) (float64, bool) {
	d := b.sub(a)
	f := a.sub(c)
	A := d.X*d.X + d.Y*d.Y
	if A < 1e-12 {
		return 0, f.len() <= r
	}
	B := 2 * (f.X*d.X + f.Y*d.Y)
	C := f.X*f.X + f.Y*f.Y - r*r
	if C <= 0 {
		return 0, true
	}
	disc := B*B - 4*A*C
	if disc < 0 {
		return 0, false
	}
	t := (-B - math.Sqrt(disc)) / (2 * A)
	return t, t >= 0 && t <= 1
}

// hit lands one shot by player on a; the second asks for a suspend.
func (w *World) hit(a *Agent, player string) {
	a.Hits++
	w.event(Event{Kind: "hit", Agent: a.ID, Player: player})
	if a.Hits < hitsToSuspend {
		return
	}
	a.State, a.By = Suspending, player
	w.out = append(w.out, Intent{Kind: "suspend", ID: a.ID, Session: a.Session, By: player})
}

// agentVel is where a is heading this instant (for aim assist).
func (w *World) agentVel(a *Agent) Vec {
	if a.State != Running {
		return Vec{}
	}
	if a.Arrived {
		return Vec{-math.Sin(a.orbitA), math.Cos(a.orbitA)}.mul(orbitSpeed * a.orbitW)
	}
	to := w.orbitPoint(a).sub(a.Pos)
	if n := to.len(); n > 1e-9 {
		return to.mul(roleSpeed(a.Role) / n)
	}
	return Vec{}
}

func (w *World) orbitPoint(a *Agent) Vec {
	return Cluster.add(Vec{math.Cos(a.orbitA), math.Sin(a.orbitA)}.mul(a.orbitR))
}

func (w *World) tickAgents(dt float64) {
	for _, id := range slices.Sorted(maps.Keys(w.Agents)) {
		a := w.Agents[id]
		if !a.seen {
			a.seen = true
			w.out = append(w.out, Intent{Kind: "task", ID: a.ID, Session: a.Session, Role: a.Role})
		}
		switch a.State {
		case Running:
			if a.Arrived {
				a.orbitA += a.orbitW * orbitSpeed / a.orbitR * dt
				a.Pos = w.orbitPoint(a)
				continue
			}
			to := w.orbitPoint(a).sub(a.Pos)
			step := roleSpeed(a.Role) * dt
			if d := to.len(); d <= step {
				a.Pos, a.Arrived = w.orbitPoint(a), true
			} else {
				a.Pos = a.Pos.add(to.mul(step / d))
			}
		case Suspended:
			to := Storage.sub(a.Pos)
			if d := to.len(); d <= capsuleSpeed*dt {
				w.Stored[a.ID] = a.Session
				delete(w.Agents, a.ID)
			} else {
				a.Pos = a.Pos.add(to.mul(capsuleSpeed * dt / d))
			}
		}
	}
}

// spawnGap is the seconds between spawns t seconds into a round with n players online.
func spawnGap(t float64, n int) float64 {
	g := max(spawnFloor, spawnBase*math.Exp(-t/spawnRamp))
	return max(spawnMin, g/(1+playerBoost*float64(max(n, 1)-1)))
}

func (w *World) tickRound() {
	r := &w.Round
	if r.Phase == Over && w.Now-r.Ended >= nextRound && w.online() > 0 {
		w.start()
		return
	}
	if r.Phase != Live {
		return
	}
	running := w.Running()
	r.Peak = max(r.Peak, running)
	r.PeakHosted = max(r.PeakHosted, w.Hosted())
	if running >= w.Slots {
		w.end("full")
		return
	}
	if w.online() == 0 {
		if r.empty == 0 {
			r.empty = w.Now
		} else if w.Now-r.empty >= abandonAfter {
			w.end("abandoned")
			return
		}
	} else {
		r.empty = 0
	}
	// Never ask for more sessions than the pool has free workers: the one that fills the
	// last worker ends the round.
	room := w.Slots - running - len(w.spawning)
	if room <= 0 {
		return
	}
	t := w.Now - r.Started
	if w.Now >= r.nextWave {
		r.waves++
		r.nextWave = w.Now + waveEvery
		for range min(room, 2+r.waves) {
			w.spawn()
			room--
		}
	}
	if room > 0 && w.Now >= r.nextSpawn {
		w.spawn()
		r.nextSpawn = w.Now + spawnGap(t, w.online())
	}
}

// spawn asks for a new session; the agent enters the world when it comes back.
func (w *World) spawn() {
	w.seq++
	id := fmt.Sprintf("a%d", w.seq)
	role := Roles[w.roleI%len(Roles)].Name
	w.roleI++
	w.spawning[id] = pending{role: role, round: w.Round.N, pos: w.edgePoint()}
	w.Round.Spawned++
	w.out = append(w.out, Intent{Kind: "spawn", ID: id, Role: role})
}

// edgePoint is a random point just outside the arena.
func (w *World) edgePoint() Vec {
	switch w.rnd.IntN(4) {
	case 0:
		return Vec{w.rnd.Float64() * ArenaW, -3}
	case 1:
		return Vec{w.rnd.Float64() * ArenaW, ArenaH + 3}
	case 2:
		return Vec{-3, w.rnd.Float64() * ArenaH}
	}
	return Vec{ArenaW + 3, w.rnd.Float64() * ArenaH}
}

// end finishes the live round and deletes every session it made. Spawns still in flight
// are deleted when they come back.
func (w *World) end(reason string) {
	r := &w.Round
	r.Phase, r.Ended, r.Reason = Over, w.Now, reason
	survived := w.Now - r.Started
	if reason == "full" {
		w.Best = max(w.Best, survived)
	}
	for _, id := range slices.Sorted(maps.Keys(w.Agents)) {
		a := w.Agents[id]
		w.out = append(w.out, Intent{Kind: "delete", ID: a.ID, Session: a.Session})
	}
	for _, id := range slices.Sorted(maps.Keys(w.Stored)) {
		w.out = append(w.out, Intent{Kind: "delete", ID: id, Session: w.Stored[id]})
	}
	clear(w.Agents)
	clear(w.Stored)
	w.Bullets = nil
	w.event(Event{Kind: "over", Text: reason, Ms: int64(survived * 1000)})
}

// Resolve applies an op's result.
func (w *World) Resolve(r Result) {
	switch r.Kind {
	case "spawn":
		p, ok := w.spawning[r.ID]
		if !ok {
			return
		}
		delete(w.spawning, r.ID)
		if !r.OK {
			return
		}
		if w.Round.Phase != Live || p.round != w.Round.N {
			w.out = append(w.out, Intent{Kind: "delete", ID: r.ID, Session: r.Session}) // its round is over
			return
		}
		a := &Agent{ID: r.ID, Session: r.Session, Role: p.role, Name: fmt.Sprintf("%s-%s", p.role, r.ID[1:]), Pos: p.pos, State: Running,
			orbitR: orbitMin + w.rnd.Float64()*(orbitMax-orbitMin), orbitA: w.rnd.Float64() * 2 * math.Pi, orbitW: 1}
		if w.rnd.IntN(2) == 0 {
			a.orbitW = -1
		}
		w.Agents[a.ID] = a
		w.event(Event{Kind: "spawned", Agent: a.ID, Text: a.Role})
	case "task":
		a := w.Agents[r.ID]
		if a == nil {
			return
		}
		a.Done, a.Say = true, r.Say
		if r.Say != "" {
			w.event(Event{Kind: "task", Agent: a.ID, Text: r.Say})
		}
	case "suspend":
		a := w.Agents[r.ID]
		if a == nil || a.State != Suspending {
			return
		}
		if !r.OK {
			a.State, a.Hits = Running, 0
			w.event(Event{Kind: "slipped", Agent: a.ID, Player: a.By})
			return
		}
		a.State, a.SuspendMs = Suspended, r.Ms
		w.Round.Suspended++
		if s := w.Ships[a.By]; s != nil {
			s.Score++
			s.Total++
		}
		w.event(Event{Kind: "suspended", Agent: a.ID, Player: a.By, Text: a.Role, Ms: r.Ms})
	}
}
