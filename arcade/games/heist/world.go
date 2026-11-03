// This file is the pure-logic world: avatars, targets, shots, freezing and booking. It never talks to kagent; Tick returns Intents for the Game to run and the
// Game reports results back through Resolve.
package heist

import (
	"maps"
	"math"
	"math/rand/v2"
	"slices"
	"time"
)

// TargetState is where a target is in its life.
type TargetState string

const (
	Working TargetState = "Working"
	Frozen  TargetState = "Frozen"
	Booked  TargetState = "Booked"
	Deleted TargetState = "Deleted"
	Escaped TargetState = "Escaped"
)

// Tuning, in tiles and seconds.
const (
	avatarSpeed  = 5.0
	targetSpeed  = 3.5
	fireRange    = 7.0
	actionRange  = 1.6
	fleeRange    = 5.0
	fireCD       = 0.25
	hitsToFreeze = 2
	eps          = 1e-9
)

// Avatar is a player on the map.
type Avatar struct {
	PlayerID, Nick, Color, Squad string
	Pos, Facing                  Vec
	FireCD                       float64
	Online                       bool
	HP                           float64 // 0..maxHP; NewAvatar starts it full
	Down                         bool    // knocked out: no move, fire, act or being shot
	RespawnAt, LastHit           float64 // world times
	Inv                          []Part  // parts carried, dropped when Down; a full set builds an ally

	// PvP (players.go). A player is Enrolled from their first run; from then on their own
	// agent (Agent) runs while they are in play.
	Enrolled      bool
	Frozen        bool    // frozen by another player: no move or fire until RespawnAt
	FrozenBy      string  // the player who froze them
	FrozenAt      float64 // world time
	ProtectUntil  float64 // world time spawn protection ends
	PvPHits       int     // hits from other players since LastPvP (reset after pvpHitDecay)
	LastPvP       float64
	Agent         PlayerAgent
}

// NewAvatar is an online avatar at pos with full health.
func NewAvatar(playerID, nick, color, squad string, pos Vec) *Avatar {
	return &Avatar{PlayerID: playerID, Nick: nick, Color: color, Squad: squad, Pos: pos, Online: true, HP: maxHP}
}

// Target is an agent session walking the city.
type Target struct {
	ID, SessionID, MissionID, Name, Node, Say string
	Squad                                     string // owner squad, set by missions at spawn
	Pos                                       Vec
	State                                     TargetState
	Hits                                      int
	HitBy                                     map[string]bool
	FrozenBy                                  string
	Step, Steps                               int
	FrozenAt                                  float64
	Busy                                      bool // an op is in flight; no further intents
	New                                       bool
	ShotCD                                    float64 // hunter fire cooldown, seconds until it may shoot again
	Checkpoint                                string  // its booking's checkpoint ID (the session itself is gone)

	opBy   string // player whose intent is in flight
	opKind string // that intent's kind ("freeze", ...)
	// escapePending: its final step completed while a freeze was in flight. The freeze
	// wins (the shot landed first); if it fails, the escape is applied then.
	escapePending bool
}

// Event is something the big screen and players are told about.
type Event struct {
	Kind, Target, Player, Text string
	Ms                         int64
}

// Input is one player's controls; the latest value is held until replaced.
type Input struct {
	Move         Vec
	Fire, Action bool
}

// Intent asks the Game to run an op: freeze, extract (book), delete or spring.
type Intent struct{ Kind, Target, Player string }

// control is the per-player input state, including ACTION press tracking.
type control struct {
	in   Input
	held bool // ACTION currently down
	tap  bool // a press is waiting for the next Tick
}

// World is the whole simulation.
type World struct {
	City    *City
	Avatars map[string]*Avatar
	Targets map[string]*Target
	Now     float64
	Events  []Event
	Drops   map[string]*Drop
	Rand    *rand.Rand        // loot rolls; NewWorld seeds it from the clock
	Allies  map[string]*Ally  // deployed allies; ones still spawning are kept in pending
	// OnPlayerFrozen, if set, is told every PvP takedown (the Game scores it).
	OnPlayerFrozen func(by, victim string)
	// PlayerAgentCap overrides PlayerAgentCap (tests).
	PlayerAgentCap int

	ctl     map[string]*control
	pending map[string]*pendingAlly // ally spawns submitted, no result yet
	allySeq int
}

// NewWorld is an empty world on c.
func NewWorld(c *City) *World {
	seed := uint64(time.Now().UnixNano())
	return &World{
		City: c, Avatars: map[string]*Avatar{}, Targets: map[string]*Target{}, ctl: map[string]*control{}, pending: map[string]*pendingAlly{},
		Allies: map[string]*Ally{}, Drops: map[string]*Drop{}, Rand: rand.New(rand.NewPCG(seed, seed>>1|1)),
	}
}

// Input records a player's controls. ACTION presses are tracked here so a press and
// release between two ticks still counts as a tap.
func (w *World) Input(playerID string, in Input) {
	k := w.ctl[playerID]
	if k == nil {
		k = &control{}
		w.ctl[playerID] = k
	}
	if in.Action && !k.held {
		k.tap = true
	}
	k.held = in.Action
	k.in = in
}

// DrainEvents returns the pending events and clears them.
func (w *World) DrainEvents() []Event {
	ev := w.Events
	w.Events = nil
	return ev
}

// Tick advances the world by dt seconds and returns the ops to run. Players are handled
// in sorted ID order, so the winner of a same-tick contest for a frozen target is
// deterministic.
func (w *World) Tick(dt float64) []Intent {
	if !finite(dt) || dt < 0 {
		dt = 0
	}
	w.Now += dt
	w.expireDrops()
	var out []Intent
	for _, id := range slices.Sorted(maps.Keys(w.Avatars)) {
		a := w.Avatars[id]
		k := w.ctl[id]
		if k == nil {
			k = &control{}
			w.ctl[id] = k
		}
		a.FireCD = max(0, a.FireCD-dt)
		if a.Down {
			w.tickDown(a, k)
			continue
		}
		if a.Frozen {
			w.tickFrozen(a, k)
			continue
		}
		w.regen(a, dt)
		if !a.Online {
			*k = control{}
			continue
		}
		w.moveAvatar(a, k.in.Move, dt)
		w.pickup(a)
		if k.in.Fire && a.FireCD <= eps {
			out = append(out, w.fire(a)...)
		}
		if k.tap {
			k.tap = false
			if t, al := w.adjacentFrozen(a), w.ownFrozenAlly(a); al != nil && (t == nil || dist(a.Pos, al.Pos) < dist(a.Pos, t.Pos)) {
				out = append(out, w.startAlly(al, "spring", a.PlayerID))
			} else if t != nil {
				out = append(out, w.start(t, "extract", a.PlayerID)) // book it where it stands
			}
		}
	}
	out = append(out, w.tickAllies(dt)...)
	out = append(out, w.huntersFire(dt)...)
	out = append(out, w.tickAgents()...)
	w.moveTargets(dt)
	return out
}

// start marks t busy with an op by player and returns its intent.
func (w *World) start(t *Target, kind, player string) Intent {
	t.Busy, t.opBy, t.opKind = true, player, kind
	return Intent{Kind: kind, Target: t.ID, Player: player}
}

func (w *World) moveAvatar(a *Avatar, dir Vec, dt float64) {
	a.Pos = w.City.Move(a.Pos, dir, avatarSpeed, dt)
	if n := math.Hypot(dir.X, dir.Y); n > 0 && finite(n) {
		a.Facing = Vec{dir.X / n, dir.Y / n}
	}
}

// fire auto-aims a's shot at whatever is closest in range and in line of sight: an
// agent, or a player from another squad.
func (w *World) fire(a *Avatar) []Intent {
	t := w.nearest(a.Pos, fireRange, func(t *Target) bool { return t.State == Working && !t.Busy && w.City.LineOfSight(a.Pos, t.Pos) })
	if e, d := w.nearestEnemy(a, fireRange); e != nil && (t == nil || d < dist(a.Pos, t.Pos)) {
		a.FireCD = fireCD
		w.hitPlayer(e, a)
		return nil
	}
	fired, out := w.shoot(a.Pos, a.PlayerID)
	if fired {
		a.FireCD = fireCD
	}
	return out
}

// shoot hits the nearest Working, non-busy target within fireRange of from and in line
// of sight, crediting player by (an ally's shots are its owner's). The hitsToFreeze-th
// hit asks for a freeze.
func (w *World) shoot(from Vec, by string) (bool, []Intent) {
	t := w.nearest(from, fireRange, func(t *Target) bool { return t.State == Working && !t.Busy && w.City.LineOfSight(from, t.Pos) })
	if t == nil {
		return false, nil
	}
	t.Hits++
	if t.HitBy == nil {
		t.HitBy = map[string]bool{}
	}
	t.HitBy[by] = true
	if t.Hits >= hitsToFreeze {
		return true, []Intent{w.start(t, "freeze", by)}
	}
	return true, nil
}

// adjacentFrozen is the nearest idle Frozen target within ACTION range of a.
func (w *World) adjacentFrozen(a *Avatar) *Target {
	return w.nearest(a.Pos, actionRange, func(t *Target) bool { return t.State == Frozen && !t.Busy })
}

// nearest is the closest target to p within r that passes ok; ties go to the lower ID.
func (w *World) nearest(p Vec, r float64, ok func(*Target) bool) *Target {
	var best *Target
	bestD := r + eps
	for _, id := range slices.Sorted(maps.Keys(w.Targets)) {
		t := w.Targets[id]
		if d := dist(p, t.Pos); ok(t) && d < bestD {
			best, bestD = t, d
		}
	}
	return best
}

// moveTargets makes Working targets flee nearby avatars.
func (w *World) moveTargets(dt float64) {
	for _, id := range slices.Sorted(maps.Keys(w.Targets)) {
		t := w.Targets[id]
		if t.State != Working || t.Busy {
			continue
		}
		var away Vec
		for _, a := range w.Avatars {
			if d := dist(t.Pos, a.Pos); a.Online && d < fleeRange && d > eps {
				away.X += (t.Pos.X - a.Pos.X) / (d * d)
				away.Y += (t.Pos.Y - a.Pos.Y) / (d * d)
			}
		}
		t.Pos = w.City.Move(t.Pos, away, targetSpeed, dt)
	}
}

// Resolve applies the result of an op the Game ran for target, a target or an ally
// ID. ms is how long it took.
func (w *World) Resolve(target, kind string, ok bool, ms int64) {
	t := w.Targets[target]
	if t == nil {
		if al := w.Allies[target]; al != nil {
			w.resolveAlly(al, kind, ok, ms)
		}
		return
	}
	by := t.opBy
	t.Busy, t.opBy, t.opKind = false, "", ""
	if t.State == Escaped || t.State == Booked || t.State == Deleted {
		// A late result (say a freeze) never revives a target that is gone; its session
		// is deleted by the mission's normal cleanup.
		return
	}
	ev := Event{Kind: kind + "-failed", Target: t.ID, Player: by, Text: t.Name, Ms: ms}
	loot := false // a confirmed freeze or delete drops one part, after its event
	switch kind {
	case "freeze":
		if ok {
			t.State, t.FrozenBy, t.FrozenAt, t.escapePending = Frozen, by, w.Now, false
			ev.Kind = "frozen"
			loot = true
		} else {
			t.Hits, t.HitBy = 0, map[string]bool{}
			ev.Kind = "slipped"
		}
	case "extract":
		if ok {
			t.State = Booked
			ev.Kind = "booked"
		}
	case "delete":
		if ok {
			t.State = Deleted
			ev.Kind = "deleted"
			loot = true
		}
	}
	w.Events = append(w.Events, ev)
	if loot {
		w.lootDrop(t, by)
	}
}

func dist(a, b Vec) float64 { return math.Hypot(a.X-b.X, a.Y-b.Y) }
