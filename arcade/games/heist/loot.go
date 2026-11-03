// This file is hunters, health and loot: Working targets shoot players, players go down
// and lose their unbanked parts, takedowns drop computer parts, and the Vault banks them.
package heist

import (
	"maps"
	"math/rand/v2"
	"slices"
	"strconv"

	"github.com/sebbycorp/kubecon-adventures/arcade/internal/platform"
)

// Tuning, in tiles and seconds.
const (
	maxHP        = 100.0
	hunterRange  = 5.0
	hunterDamage = 10.0
	hunterCD     = 1.2
	regenRate    = 5.0 // HP per second
	regenQuiet   = 4.0 // seconds without a hit before regen starts
	respawnDelay = 3.0
	bagTTL       = 60.0  // a downed player's dropped inventory
	lootTTL      = 120.0 // a part dropped by a takedown
	pickupRange  = 0.8
	invSlots     = 8
)

// Part is a computer part. Kind is "ram", "cpu", "drive" or "board"; Tier is 1-5 for
// RAM (DDR1-DDR5) and 0 otherwise.
type Part struct {
	ID, Kind string
	Tier     int
}

// Label is the part's display name: "DDR1".."DDR5", "CPU", "drive" or "board".
func (p Part) Label() string {
	switch p.Kind {
	case "ram":
		return "DDR" + strconv.Itoa(p.Tier)
	case "cpu":
		return "CPU"
	}
	return p.Kind
}

// Drop is a part lying on the street until Expires.
type Drop struct {
	ID      string
	Part    Part
	Pos     Vec
	Expires float64
}

// RollPart picks a part: RAM 50% (DDR1 40%, DDR2 25%, DDR3 18%, DDR4 12%, DDR5 5% of
// RAM), CPU 20%, drive 20%, motherboard 10%.
func RollPart(rnd *rand.Rand) Part {
	p := Part{ID: platform.NewID()}
	switch r := rnd.IntN(100); {
	case r < 50:
		p.Kind = "ram"
		switch t := rnd.IntN(100); {
		case t < 40:
			p.Tier = 1
		case t < 65:
			p.Tier = 2
		case t < 83:
			p.Tier = 3
		case t < 95:
			p.Tier = 4
		default:
			p.Tier = 5
		}
	case r < 70:
		p.Kind = "cpu"
	case r < 90:
		p.Kind = "drive"
	default:
		p.Kind = "board"
	}
	return p
}

// dropPart puts p on the street at pos for ttl seconds.
func (w *World) dropPart(p Part, pos Vec, ttl float64) *Drop {
	d := &Drop{ID: platform.NewID(), Part: p, Pos: pos, Expires: w.Now + ttl}
	w.Drops[d.ID] = d
	return d
}

// lootDrop rolls a part from a takedown of t (by player) and drops it at t.
func (w *World) lootDrop(t *Target, by string) {
	d := w.dropPart(RollPart(w.Rand), t.Pos, lootTTL)
	w.Events = append(w.Events, Event{Kind: "drop", Target: t.ID, Player: by, Text: d.Part.Label()})
}

// expireDrops removes drops whose time is up.
func (w *World) expireDrops() {
	for id, d := range w.Drops {
		if d.Expires <= w.Now+eps {
			delete(w.Drops, id)
		}
	}
}

// regen refills a's health at regenRate once regenQuiet seconds have passed since its
// last hit, counting only the part of this tick that is past the quiet mark.
func (w *World) regen(a *Avatar, dt float64) {
	quiet := min(dt, w.Now-a.LastHit-regenQuiet)
	if quiet > 0 && a.HP < maxHP {
		a.HP = min(maxHP, a.HP+regenRate*quiet)
	}
}

// tickDown handles a Down avatar: nothing happens until RespawnAt, then it comes back
// at the Dispatch centre with full health and fresh controls.
func (w *World) tickDown(a *Avatar, k *control) {
	if w.Now < a.RespawnAt-eps {
		return
	}
	d := w.City.Dispatch
	a.Down, a.HP, a.LastHit = false, maxHP, w.Now
	a.Pos = Vec{d.X + d.W/2, d.Y + d.H/2}
	*k = control{}
	w.Events = append(w.Events, Event{Kind: "respawn", Player: a.PlayerID})
}

// pickup moves drops within pickupRange into a's inventory while it has room.
func (w *World) pickup(a *Avatar) {
	for _, id := range slices.Sorted(maps.Keys(w.Drops)) {
		if len(a.Inv) >= invSlots {
			return
		}
		d := w.Drops[id]
		if dist(a.Pos, d.Pos) <= pickupRange+eps {
			a.Inv = append(a.Inv, d.Part)
			delete(w.Drops, id)
			w.Events = append(w.Events, Event{Kind: "pickup", Player: a.PlayerID, Text: d.Part.Label()})
		}
	}
}

// bank moves a's whole inventory into its stash while it stands in the Vault.
func (w *World) bank(a *Avatar) {
	if len(a.Inv) == 0 || !w.City.Vault.Contains(a.Pos) {
		return
	}
	n := len(a.Inv)
	w.Stash[a.PlayerID] = append(w.Stash[a.PlayerID], a.Inv...)
	a.Inv = nil
	w.Events = append(w.Events, Event{Kind: "banked", Player: a.PlayerID, Text: strconv.Itoa(n)})
}

// huntersFire lets every hunter (a Working target that is not Busy) shoot the nearest
// online, standing avatar or Working, idle ally within hunterRange, once per hunterCD
// (an avatar wins a tie). Target.ShotCD keeps each hunter's cooldown. A shot on a
// player only changes health; an ally shot to 0 health asks for its freeze.
func (w *World) huntersFire(dt float64) []Intent {
	var out []Intent
	for _, id := range slices.Sorted(maps.Keys(w.Targets)) {
		t := w.Targets[id]
		t.ShotCD = max(0, t.ShotCD-dt)
		if t.State != Working || t.Busy || t.ShotCD > eps {
			continue
		}
		var best *Avatar
		bestD := hunterRange + eps
		for _, pid := range slices.Sorted(maps.Keys(w.Avatars)) {
			a := w.Avatars[pid]
			if d := dist(t.Pos, a.Pos); a.Online && !a.Down && d < bestD {
				best, bestD = a, d
			}
		}
		if al, d := w.nearestAlly(t.Pos, hunterRange); al != nil && d < bestD {
			t.ShotCD = hunterCD
			out = append(out, w.hitAlly(al, t)...)
			continue
		}
		if best == nil {
			continue
		}
		t.ShotCD = hunterCD
		w.hit(best, t)
	}
	return out
}

// hit applies one hunter shot from t to a; at 0 health a goes down.
func (w *World) hit(a *Avatar, t *Target) {
	a.HP -= hunterDamage
	a.LastHit = w.Now
	w.Events = append(w.Events, Event{Kind: "hit", Player: a.PlayerID, Target: t.ID})
	if a.HP > eps {
		return
	}
	a.HP, a.Down, a.RespawnAt = 0, true, w.Now+respawnDelay
	w.drop(a)
	n := len(a.Inv)
	for _, p := range a.Inv {
		w.dropPart(p, a.Pos, bagTTL)
	}
	a.Inv = nil
	w.Events = append(w.Events, Event{Kind: "down", Player: a.PlayerID, Target: t.ID, Text: strconv.Itoa(n)})
}
