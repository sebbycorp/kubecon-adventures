// This file is PvP and players' own agents. Every player in a run is backed by a real
// Agent Substrate agent (a siegeplayer session): awake on a worker while they are alive
// and online, suspended to a snapshot when they are frozen, knocked down or gone, and
// resumed when they respawn. Players shoot each other: pvpHits hits from another squad
// freeze a player for frozenFor seconds (their agent's real suspend), and they respawn
// at a safe spot with protectFor seconds of spawn protection. Like the rest of World it
// is pure logic: the agent's ops go out as Intents and come back through ResolvePlayer.
package heist

import (
	"maps"
	"slices"
	"strconv"
	"strings"
)

// PvP and player-agent tuning, in tiles, seconds and points.
const (
	pvpHits        = 3    // hits from another squad that freeze a player
	pvpHitDecay    = 6.0  // seconds without a PvP hit before the count resets
	frozenFor      = 5.0  // seconds a frozen player waits to respawn
	protectFor     = 3.0  // spawn protection after a respawn or join
	safeFromEnemy  = 10.0 // a respawn is at least this far from any enemy player
	safeFromHunter = 6.0  // ... and from any Working agent
	agentRetry     = 10.0 // seconds before a failed player-agent op is tried again
	ptsPlayer      = 2    // points for freezing a player
	playerPrefix   = "pa:"
	// PlayerAgentCap is how many player agents may be awake at once: the siegeplayers
	// WorkerPool's workers. Players past it still play; their agent waits asleep.
	PlayerAgentCap = 10
)

// Player-agent states (Avatar.Agent.State).
const (
	agentNone       = ""           // never started
	agentStarting   = "starting"   // session and greeting turn in flight
	agentAwake      = "awake"      // running on a worker
	agentSuspending = "suspending" // suspend in flight
	agentAsleep     = "asleep"     // a snapshot, using no CPU
	agentWaking     = "waking"     // resume in flight
	agentNoSandbox  = "nosandbox"  // wants to be awake, but every player sandbox is taken
)

// PlayerAgent is a player's own Substrate agent.
type PlayerAgent struct {
	SID, State, Say string
	Ms              int64   // the last suspend or resume, in ms
	Busy            bool    // an op is in flight
	RetryAt         float64 // world time a failed op may be tried again
}

// playerKey is the scheduler target for pid's agent; playerOf undoes it.
func playerKey(pid string) string { return playerPrefix + pid }

func playerOf(target string) (string, bool) { return strings.CutPrefix(target, playerPrefix) }

// awake reports whether the agent holds (or is getting) one of the player sandboxes.
func (ag *PlayerAgent) holding() bool {
	switch ag.State {
	case agentStarting, agentAwake, agentWaking, agentSuspending:
		return true
	}
	return false
}

// enemies reports whether a may shoot b: different squads, both in play, b not
// protected.
func (w *World) enemies(a, b *Avatar) bool {
	return a != b && a.Squad != b.Squad && b.Online && !b.Down && !b.Frozen && w.Now >= b.ProtectUntil-eps
}

// nearestEnemy is the closest player a may shoot within r, in line of sight.
func (w *World) nearestEnemy(a *Avatar, r float64) (*Avatar, float64) {
	var best *Avatar
	bestD := r + eps
	for _, id := range slices.Sorted(maps.Keys(w.Avatars)) {
		b := w.Avatars[id]
		if d := dist(a.Pos, b.Pos); w.enemies(a, b) && d < bestD && w.City.LineOfSight(a.Pos, b.Pos) {
			best, bestD = b, d
		}
	}
	return best, bestD
}

// hitPlayer lands one of by's shots on v; the pvpHits-th freezes v.
func (w *World) hitPlayer(v, by *Avatar) {
	if w.Now-v.LastPvP > pvpHitDecay {
		v.PvPHits = 0
	}
	v.PvPHits++
	v.LastPvP = w.Now
	w.Events = append(w.Events, Event{Kind: "pvp-hit", Player: by.PlayerID, Text: v.PlayerID + " " + strconv.Itoa(v.PvPHits)})
	if v.PvPHits >= pvpHits {
		w.freezePlayer(v, by)
	}
}

// freezePlayer freezes v: they drop what they carry, their agent is suspended (the
// next tick asks for it), and they respawn after frozenFor. by scores the takedown.
func (w *World) freezePlayer(v, by *Avatar) {
	v.Frozen, v.FrozenBy, v.FrozenAt, v.RespawnAt, v.PvPHits = true, by.PlayerID, w.Now, w.Now+frozenFor, 0
	for _, p := range v.Inv {
		w.dropPart(p, v.Pos, bagTTL)
	}
	v.Inv = nil
	w.Events = append(w.Events, Event{Kind: "player-frozen", Player: by.PlayerID, Text: v.PlayerID})
	if w.OnPlayerFrozen != nil {
		w.OnPlayerFrozen(by.PlayerID, v.PlayerID)
	}
}

// tickFrozen respawns a frozen player at a safe spot once their time is up.
func (w *World) tickFrozen(a *Avatar, k *control) {
	if w.Now < a.RespawnAt-eps {
		return
	}
	a.Frozen, a.FrozenBy, a.HP, a.LastHit = false, "", maxHP, w.Now
	a.Pos, a.ProtectUntil = w.safeSpawn(a), w.Now+protectFor
	*k = control{}
	w.Events = append(w.Events, Event{Kind: "player-respawn", Player: a.PlayerID})
}

// safeSpawn is a random open spot at least safeFromEnemy tiles from every enemy player
// and safeFromHunter from every Working agent, or Dispatch when there is none.
func (w *World) safeSpawn(a *Avatar) Vec {
	spots := w.City.openTiles()
	for range 60 {
		p := spots[w.Rand.IntN(len(spots))]
		ok := !w.City.Dispatch.Contains(p)
		for _, b := range w.Avatars {
			if ok && b != a && b.Online && b.Squad != a.Squad && dist(p, b.Pos) < safeFromEnemy {
				ok = false
			}
		}
		for _, t := range w.Targets {
			if ok && t.State == Working && dist(p, t.Pos) < safeFromHunter {
				ok = false
			}
		}
		if ok {
			return p
		}
	}
	d := w.City.Dispatch
	return Vec{d.X + d.W/2, d.Y + d.H/2}
}

// tickAgents asks for each player agent's next op: start it the first time its player
// is in play, suspend it when they are frozen, down or offline, and resume it when they
// are back. At most PlayerAgentCap agents hold a sandbox at once.
func (w *World) tickAgents() []Intent {
	holding := 0
	for _, a := range w.Avatars {
		if a.Agent.holding() {
			holding++
		}
	}
	var out []Intent
	for _, id := range slices.Sorted(maps.Keys(w.Avatars)) {
		a := w.Avatars[id]
		ag := &a.Agent
		want := a.Enrolled && a.Online && !a.Down && !a.Frozen
		if ag.Busy || w.Now < ag.RetryAt-eps {
			continue
		}
		switch {
		case want && (ag.State == agentNone || ag.State == agentNoSandbox && ag.SID == ""):
			if holding >= w.agentCap() {
				ag.State = agentNoSandbox
				continue
			}
			holding++
			ag.State, ag.Busy = agentStarting, true
			out = append(out, Intent{Kind: "pagent", Target: playerKey(id), Player: id})
		case want && (ag.State == agentAsleep || ag.State == agentNoSandbox):
			if holding >= w.agentCap() {
				ag.State = agentNoSandbox
				continue
			}
			holding++
			ag.State, ag.Busy = agentWaking, true
			out = append(out, Intent{Kind: "pwake", Target: playerKey(id), Player: id})
		case !want && ag.State == agentAwake:
			ag.State, ag.Busy = agentSuspending, true
			out = append(out, Intent{Kind: "freeze", Target: playerKey(id), Player: id})
		case !want && ag.State == agentNoSandbox:
			ag.State = agentAsleep
			if ag.SID == "" {
				ag.State = agentNone
			}
		}
	}
	return out
}

func (w *World) agentCap() int {
	if w.PlayerAgentCap > 0 {
		return w.PlayerAgentCap
	}
	return PlayerAgentCap
}

// CancelPlayerOp undoes a player-agent intent the scheduler refused.
func (w *World) CancelPlayerOp(pid, kind string) {
	a := w.Avatars[pid]
	if a == nil {
		return
	}
	ag := &a.Agent
	ag.Busy = false
	switch kind {
	case "pagent":
		ag.State = agentNone
	case "pwake":
		ag.State = agentAsleep
	case "freeze":
		ag.State = agentAwake
	}
	ag.RetryAt = w.Now + 1
}

// ResolvePlayer applies a player-agent op's Result. It returns the session of an agent
// whose player has left the city, which the Game must delete.
func (w *World) ResolvePlayer(r Result) (orphan string) {
	pid, _ := playerOf(r.Target)
	a := w.Avatars[pid]
	if a == nil {
		if r.Kind == "pagent" && r.OK {
			return r.Spawned
		}
		return ""
	}
	ag := &a.Agent
	ag.Busy = false
	if !r.OK {
		ag.RetryAt = w.Now + agentRetry
		switch r.Kind {
		case "pagent":
			ag.State = agentNone
		case "pwake":
			ag.State = agentAsleep
		case "freeze":
			ag.State = agentAwake
		}
		return ""
	}
	ag.Ms = r.Ms
	switch r.Kind {
	case "pagent":
		ag.SID, ag.State, ag.Say = r.Spawned, agentAwake, clip(r.Step.Say)
		w.Events = append(w.Events, Event{Kind: "agent-up", Player: pid, Text: ag.Say, Ms: r.Ms})
	case "pwake":
		ag.State = agentAwake
		w.Events = append(w.Events, Event{Kind: "player-woke", Player: pid, Ms: r.Ms})
	case "freeze":
		ag.State = agentAsleep
		kind := "agent-asleep"
		if a.Frozen {
			kind = "player-snapshot" // the PvP freeze's real suspend
		}
		w.Events = append(w.Events, Event{Kind: kind, Player: pid, Ms: r.Ms})
	}
	return ""
}

// PlayerSessions is every player agent's session, for cleanup.
func (w *World) PlayerSessions() []string {
	var out []string
	for _, a := range w.Avatars {
		if a.Agent.SID != "" {
			out = append(out, a.Agent.SID)
		}
	}
	return out
}

// openTiles is the centre of every open tile, computed once.
func (c *City) openTiles() []Vec {
	c.openOnce.Do(func() {
		for i, t := range c.Tiles {
			if open(t) {
				c.spots = append(c.spots, Vec{float64(i%c.W) + 0.5, float64(i/c.W) + 0.5})
			}
		}
	})
	return c.spots
}
