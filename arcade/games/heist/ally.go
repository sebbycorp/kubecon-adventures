// This file is rigs and allies: a player spends a board, a CPU, a RAM stick and a drive
// from their stash to deploy an ally, a real agent session that follows them and shoots
// hunters. An ally's hits count as its owner's, so a freeze it lands is the owner's
// takedown. Hunters shoot allies too; at 0 health an ally is frozen (a real suspend),
// and its owner springs it with a short ACTION next to it.
//
// Allies are deleted when their owner's mission ends (won, failed or escaped). An ally
// deployed while its owner has no active mission stays until the owner's next mission
// ends; deleting allies when a player leaves is the Game's job.
package heist

import (
	"errors"
	"fmt"
	"maps"
	"slices"
)

// Tuning, in tiles and seconds. An ally fires within fireRange (6 tiles), like a player.
const (
	allyFollow    = 1.0 // an ally closes in until it is this close to its owner
	allyFastTier  = 5   // DDR5 allies move allyFastSpeed times faster
	allyFastSpeed = 1.2
	alliesPerUser = 2
	alliesInCity  = 16
)

// Ally is a player's deployed agent. State is Working or Frozen; an ally that is still
// spawning is not in World.Allies.
type Ally struct {
	ID, Owner, SessionID, Say string
	Pos                       Vec
	HP                        float64
	Tier                      int
	State                     TargetState
	Busy                      bool
	FireCD                    float64

	opBy string // player whose intent is in flight
}

// pendingAlly is a rig whose spawn was submitted but has no result yet.
type pendingAlly struct {
	owner string
	tier  int
	parts []Part // refunded if the spawn fails
}

// allyFireRate is shots per second for a RAM tier: 1.0 at DDR1 up to 2.2 at DDR5.
func allyFireRate(tier int) float64 { return 1.0 + 0.3*float64(tier-1) }

// BuildRig spends a board, a CPU, the highest-tier RAM and a drive from playerID's stash
// and returns the intent to spawn the ally. Nothing is spent on error.
func (w *World) BuildRig(playerID string) (Intent, error) {
	stash := w.Stash[playerID]
	pick := map[string]int{"board": -1, "cpu": -1, "ram": -1, "drive": -1}
	for i, p := range stash {
		j, ok := pick[p.Kind]
		if ok && (j < 0 || p.Kind == "ram" && p.Tier > stash[j].Tier) {
			pick[p.Kind] = i
		}
	}
	if slices.Contains(slices.Collect(maps.Values(pick)), -1) {
		return Intent{}, errors.New("missing parts")
	}
	mine, all := 0, len(w.Allies)+len(w.pending)
	for _, al := range w.Allies {
		if al.Owner == playerID {
			mine++
		}
	}
	for _, p := range w.pending {
		if p.owner == playerID {
			mine++
		}
	}
	switch {
	case mine >= alliesPerUser:
		return Intent{}, errors.New("ally limit")
	case all >= alliesInCity:
		return Intent{}, errors.New("city full")
	}
	p := &pendingAlly{owner: playerID, tier: stash[pick["ram"]].Tier}
	var left []Part
	for i, part := range stash {
		if i == pick["board"] || i == pick["cpu"] || i == pick["ram"] || i == pick["drive"] {
			p.parts = append(p.parts, part)
		} else {
			left = append(left, part)
		}
	}
	w.Stash[playerID] = left
	w.allySeq++
	id := fmt.Sprintf("ally-%d", w.allySeq)
	w.pending[id] = p
	return Intent{Kind: "ally", Target: id, Player: playerID}, nil
}

// allySpawned places the ally from an "ally" Result near its owner, or refunds the rig.
func (w *World) allySpawned(r Result) {
	p := w.pending[r.Target]
	if p == nil {
		return
	}
	delete(w.pending, r.Target)
	if !r.OK {
		w.Stash[p.owner] = append(w.Stash[p.owner], p.parts...)
		w.Events = append(w.Events, Event{Kind: "rig-failed", Target: r.Target, Player: p.owner, Text: r.Err})
		return
	}
	d := w.City.Dispatch
	pos := Vec{d.X + d.W/2, d.Y + d.H/2}
	if a := w.Avatars[p.owner]; a != nil {
		pos = a.Pos
	}
	w.Allies[r.Target] = &Ally{ID: r.Target, Owner: p.owner, SessionID: r.Spawned, Say: clip(r.Step.Say),
		Pos: pos, HP: maxHP, Tier: p.tier, State: Working}
	w.Events = append(w.Events, Event{Kind: "ally", Target: r.Target, Player: p.owner, Text: fmt.Sprintf("DDR%d", p.tier)})
}

// startAlly marks al busy with an op by player and returns its intent.
func (w *World) startAlly(al *Ally, kind, player string) Intent {
	al.Busy, al.opBy = true, player
	return Intent{Kind: kind, Target: al.ID, Player: player}
}

// ownFrozenAlly is a's nearest idle Frozen ally within ACTION range, if any.
func (w *World) ownFrozenAlly(a *Avatar) *Ally {
	var best *Ally
	bestD := actionRange + eps
	for _, id := range slices.Sorted(maps.Keys(w.Allies)) {
		al := w.Allies[id]
		if d := dist(a.Pos, al.Pos); al.Owner == a.PlayerID && al.State == Frozen && !al.Busy && d < bestD {
			best, bestD = al, d
		}
	}
	return best
}

// tickAllies moves Working allies after their owners and lets idle ones fire. The fire
// cooldown carries its remainder so the rate holds at any tick length.
func (w *World) tickAllies(dt float64) []Intent {
	var out []Intent
	for _, id := range slices.Sorted(maps.Keys(w.Allies)) {
		al := w.Allies[id]
		if al.State != Working {
			continue
		}
		if a := w.Avatars[al.Owner]; a != nil && dt > 0 {
			speed := avatarSpeed
			if al.Tier >= allyFastTier {
				speed *= allyFastSpeed
			}
			if d := dist(al.Pos, a.Pos); d > allyFollow {
				speed = min(speed, (d-allyFollow)/dt)
				al.Pos = w.City.Move(al.Pos, Vec{a.Pos.X - al.Pos.X, a.Pos.Y - al.Pos.Y}, speed, dt)
			}
		}
		if al.Busy {
			continue
		}
		al.FireCD -= dt
		if al.FireCD > eps {
			continue
		}
		fired, in := w.shoot(al.Pos, al.Owner)
		if fired {
			al.FireCD += 1 / allyFireRate(al.Tier)
		} else {
			al.FireCD = 0
		}
		out = append(out, in...)
	}
	return out
}

// nearestAlly is the closest Working, idle ally to p within r; ties go to the lower ID.
func (w *World) nearestAlly(p Vec, r float64) (*Ally, float64) {
	var best *Ally
	bestD := r + eps
	for _, id := range slices.Sorted(maps.Keys(w.Allies)) {
		al := w.Allies[id]
		if d := dist(p, al.Pos); al.State == Working && !al.Busy && d < bestD {
			best, bestD = al, d
		}
	}
	return best, bestD
}

// hitAlly applies one hunter shot from t to al; at 0 health it asks for a freeze.
func (w *World) hitAlly(al *Ally, t *Target) []Intent {
	al.HP = max(0, al.HP-hunterDamage)
	w.Events = append(w.Events, Event{Kind: "ally-hit", Target: al.ID, Player: al.Owner, Text: t.ID})
	if al.HP > eps {
		return nil
	}
	return []Intent{w.startAlly(al, "freeze", al.Owner)}
}

// resolveAlly applies an op result to an ally. A failed freeze leaves it Working at 0
// health, so the next hunter shot asks again.
func (w *World) resolveAlly(al *Ally, kind string, ok bool, ms int64) {
	by := al.opBy
	if by == "" {
		by = al.Owner
	}
	al.Busy, al.opBy = false, ""
	ev := Event{Kind: kind + "-failed", Target: al.ID, Player: by, Ms: ms}
	switch {
	case kind == "freeze" && ok:
		al.State, ev.Kind = Frozen, "ally-frozen"
	case kind == "spring" && ok:
		al.State, al.HP, al.FireCD, ev.Kind = Working, maxHP, 0, "ally-sprung"
	case kind == "delete" && ok:
		delete(w.Allies, al.ID)
		ev.Kind = "ally-deleted"
	}
	w.Events = append(w.Events, ev)
}

// allyResult handles a Result about an ally (after World.Resolve for control kinds)
// and reports whether it was one: spawns, the sprung line, and retry bookkeeping for
// mission-end deletes.
func (m *Missions) allyResult(r Result, w *World) bool {
	if r.Kind == "ally" {
		w.allySpawned(r)
		return true
	}
	if _, ok := m.retire[r.Target]; ok && r.Kind == "delete" {
		if r.OK {
			delete(m.retire, r.Target)
		} else {
			m.retire[r.Target] = false // retry on the next Tick
		}
		return true
	}
	al := w.Allies[r.Target]
	if al == nil {
		return false
	}
	if r.Kind == "spring" && r.OK && r.Step.Say != "" {
		al.Say = clip(r.Step.Say)
	}
	return true
}

// retireAllies marks every ally owned by a player in ms's squad for deletion.
func (m *Missions) retireAllies(ms *Mission, w *World) {
	for id, al := range w.Allies {
		if a := w.Avatars[al.Owner]; a != nil && a.Squad == ms.Squad {
			if _, ok := m.retire[id]; !ok {
				m.retire[id] = false
			}
		}
	}
}

// submitRetires submits delete for retiring allies that are idle (control lane,
// uncapped). One whose delete failed is tried again.
func (m *Missions) submitRetires(w *World, s *Scheduler) {
	for _, id := range slices.Sorted(maps.Keys(m.retire)) {
		al := w.Allies[id]
		switch {
		case al == nil:
			delete(m.retire, id)
		case m.retire[id] || al.Busy:
		case s.Submit("delete", id, al.SessionID, 0, 0):
			w.startAlly(al, "delete", al.Owner)
			m.retire[id] = true
		}
	}
}
