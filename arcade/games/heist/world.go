// This file is the pure-logic world: avatars, targets, shots, freezing, dragging and
// springing. It never talks to kagent; Tick returns Intents for the Game to run and the
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
	Dragged TargetState = "Dragged"
	Booked  TargetState = "Booked"
	Deleted TargetState = "Deleted"
	Escaped TargetState = "Escaped"
	Thawing TargetState = "Thawing"
)

// Tuning, in tiles and seconds.
const (
	avatarSpeed  = 5.0
	targetSpeed  = 3.5
	dragFactor   = 0.75 // a dragging avatar and its catch move at 3/4 avatar speed
	fireRange    = 6.0
	actionRange  = 1.2
	fleeRange    = 5.0
	fireCD       = 0.25
	springCD     = 20.0
	holdTime     = 1.0 // ACTION held this long deletes; shorter is a tap
	dragGap      = 0.7 // a dragged target trails the avatar by this much
	hitsToFreeze = 3
	eps          = 1e-9
)

// Avatar is a player on the map.
type Avatar struct {
	PlayerID, Nick, Color, Squad string
	Pos, Facing                  Vec
	Dragging                     string // target ID, or ""
	FireCD, SpringCD             float64
	Online                       bool
	HP                           float64 // 0..maxHP; NewAvatar starts it full
	Down                         bool    // knocked out: no move, fire, act or being shot
	RespawnAt, LastHit           float64 // world times
	Inv                          []Part  // unextracted loot, lost when Down
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
	New, Restored                             bool
	ShotCD                                    float64 // hunter fire cooldown, seconds until it may shoot again

	opBy string // player whose intent is in flight
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

// Intent asks the Game to run an op: freeze, extract, delete or spring.
type Intent struct{ Kind, Target, Player string }

// control is the per-player input state, including ACTION press tracking.
type control struct {
	in        Input
	pressAt   float64
	held      bool // ACTION currently down
	holdFired bool // this press already produced a delete
	tap       bool // a short press is waiting for the next Tick
}

// World is the whole simulation.
type World struct {
	City    *City
	Avatars map[string]*Avatar
	Targets map[string]*Target
	Now     float64
	Events  []Event
	Drops   map[string]*Drop
	Stash   map[string][]Part // banked parts per player ID
	Rand    *rand.Rand        // loot rolls; NewWorld seeds it from the clock

	ctl map[string]*control
}

// NewWorld is an empty world on c.
func NewWorld(c *City) *World {
	seed := uint64(time.Now().UnixNano())
	return &World{
		City: c, Avatars: map[string]*Avatar{}, Targets: map[string]*Target{}, ctl: map[string]*control{},
		Drops: map[string]*Drop{}, Stash: map[string][]Part{}, Rand: rand.New(rand.NewPCG(seed, seed>>1|1)),
	}
}

// Input records a player's controls. ACTION edges are tracked here so a press and
// release between two ticks still counts as a tap.
func (w *World) Input(playerID string, in Input) {
	k := w.ctl[playerID]
	if k == nil {
		k = &control{}
		w.ctl[playerID] = k
	}
	switch {
	case in.Action && !k.held:
		k.held, k.pressAt, k.holdFired = true, w.Now, false
	case !in.Action && k.held:
		k.held = false
		if !k.holdFired && w.Now-k.pressAt < holdTime-eps {
			k.tap = true
		}
	}
	k.in = in
}

// DrainEvents returns the pending events and clears them.
func (w *World) DrainEvents() []Event {
	ev := w.Events
	w.Events = nil
	return ev
}

// claim is one avatar's ACTION request for a tick.
type claim struct {
	a    *Avatar
	kind string // drag, spring or delete
	t    *Target
}

// Tick advances the world by dt seconds and returns the ops to run. Players are handled
// in sorted ID order, and drags beat springs beat deletes on the same target, so the
// winner of a same-tick contest is deterministic.
func (w *World) Tick(dt float64) []Intent {
	if !finite(dt) || dt < 0 {
		dt = 0
	}
	w.Now += dt
	w.expireDrops()
	var out []Intent
	var claims []claim
	for _, id := range slices.Sorted(maps.Keys(w.Avatars)) {
		a := w.Avatars[id]
		k := w.ctl[id]
		if k == nil {
			k = &control{}
			w.ctl[id] = k
		}
		a.FireCD = max(0, a.FireCD-dt)
		a.SpringCD = max(0, a.SpringCD-dt)
		if a.Down {
			w.tickDown(a, k)
			continue
		}
		w.regen(a, dt)
		if !a.Online {
			w.drop(a)
			*k = control{}
			continue
		}
		w.moveAvatar(a, k.in.Move, dt)
		w.pickup(a)
		w.bank(a)
		if k.in.Fire && a.FireCD <= eps {
			out = append(out, w.fire(a)...)
		}
		if k.tap {
			k.tap = false
			if a.Dragging != "" {
				w.drop(a)
			} else if t := w.adjacentFrozen(a); t != nil {
				kind := "drag"
				if t.Squad != "" && t.Squad != a.Squad && a.SpringCD <= eps {
					kind = "spring"
				}
				claims = append(claims, claim{a, kind, t})
			}
		}
		if k.held && !k.holdFired && w.Now-k.pressAt >= holdTime-eps {
			if t := w.adjacentFrozen(a); t != nil {
				k.holdFired = true
				claims = append(claims, claim{a, "delete", t})
			}
		}
	}
	for _, kind := range []string{"drag", "spring", "delete"} {
		for _, c := range claims {
			if c.kind != kind || c.t.State != Frozen || c.t.Busy {
				continue
			}
			switch kind {
			case "drag":
				c.t.State, c.a.Dragging = Dragged, c.t.ID
			case "spring":
				c.a.SpringCD = springCD
				out = append(out, w.start(c.t, "spring", c.a.PlayerID))
			case "delete":
				out = append(out, w.start(c.t, "delete", c.a.PlayerID))
			}
		}
	}
	w.huntersFire(dt)
	out = append(out, w.moveTargets(dt)...)
	return out
}

// start marks t busy with an op by player and returns its intent.
func (w *World) start(t *Target, kind, player string) Intent {
	t.Busy, t.opBy = true, player
	return Intent{Kind: kind, Target: t.ID, Player: player}
}

func (w *World) moveAvatar(a *Avatar, dir Vec, dt float64) {
	speed := avatarSpeed
	if a.Dragging != "" {
		speed *= dragFactor
	}
	a.Pos = w.City.Move(a.Pos, dir, speed, dt)
	if n := math.Hypot(dir.X, dir.Y); n > 0 && finite(n) {
		a.Facing = Vec{dir.X / n, dir.Y / n}
	}
}

// fire auto-aims at the nearest Working, non-busy target in range. The third hit asks
// for a freeze.
func (w *World) fire(a *Avatar) []Intent {
	t := w.nearest(a.Pos, fireRange, func(t *Target) bool { return t.State == Working && !t.Busy })
	if t == nil {
		return nil
	}
	a.FireCD = fireCD
	t.Hits++
	if t.HitBy == nil {
		t.HitBy = map[string]bool{}
	}
	t.HitBy[a.PlayerID] = true
	if t.Hits >= hitsToFreeze {
		return []Intent{w.start(t, "freeze", a.PlayerID)}
	}
	return nil
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

// drop releases whatever a is dragging; the target stays Frozen where it is.
func (w *World) drop(a *Avatar) {
	if t := w.Targets[a.Dragging]; t != nil && t.State == Dragged {
		t.State = Frozen
	}
	a.Dragging = ""
}

// moveTargets moves dragged targets after their avatars (asking for an extract once in
// the Vault) and makes Working targets flee nearby avatars.
func (w *World) moveTargets(dt float64) []Intent {
	draggers := map[string]*Avatar{}
	for _, a := range w.Avatars {
		if a.Online && a.Dragging != "" {
			draggers[a.Dragging] = a
		}
	}
	var out []Intent
	for _, id := range slices.Sorted(maps.Keys(w.Targets)) {
		t := w.Targets[id]
		switch {
		case t.State == Dragged:
			a := draggers[id]
			if a == nil { // dragger removed: leave the catch where it lies
				t.State = Frozen
				continue
			}
			if t.Busy {
				continue
			}
			if dist(t.Pos, a.Pos) > dragGap {
				t.Pos = w.City.Move(t.Pos, Vec{a.Pos.X - t.Pos.X, a.Pos.Y - t.Pos.Y}, avatarSpeed*dragFactor, dt)
			}
			if w.City.Vault.Contains(t.Pos) {
				out = append(out, w.start(t, "extract", a.PlayerID))
			}
		case t.State == Working && !t.Busy:
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
	return out
}

// Resolve applies the result of an op the Game ran for target. ms is how long it took.
func (w *World) Resolve(target, kind string, ok bool, ms int64) {
	t := w.Targets[target]
	if t == nil {
		return
	}
	by := t.opBy
	t.Busy, t.opBy = false, ""
	ev := Event{Kind: kind + "-failed", Target: t.ID, Player: by, Text: t.Name, Ms: ms}
	loot := false // a confirmed freeze or delete drops one part, after its event
	switch kind {
	case "freeze":
		if ok {
			t.State, t.FrozenBy, t.FrozenAt = Frozen, by, w.Now
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
		} else if t.State == Dragged {
			t.State = Frozen // the dragger must grab it again to retry
		}
		w.release(t.ID)
	case "delete":
		if ok {
			t.State = Deleted
			ev.Kind = "deleted"
			loot = true
		}
	case "spring":
		if ok {
			t.State, t.Hits, t.HitBy, t.FrozenBy, t.Restored = Working, 0, map[string]bool{}, "", true
			ev.Kind = "sprung"
		}
	}
	w.Events = append(w.Events, ev)
	if loot {
		w.lootDrop(t, by)
	}
}

// release clears every avatar dragging target.
func (w *World) release(target string) {
	for _, a := range w.Avatars {
		if a.Dragging == target {
			a.Dragging = ""
		}
	}
}

func dist(a, b Vec) float64 { return math.Hypot(a.X-b.X, a.Y-b.Y) }
