// This file is what makes Cluster Defense a game on top of the real Substrate core:
// agent kinds with their own behaviour, named waves and boss fleets, combos, power-ups,
// tension (the alarm and "SAVED IT!"), titles, and co-op moments. Every agent, whatever
// its kind, is still one real session holding one real worker, and every suspend is
// still a real Suspend.
package defense

import (
	"fmt"
	"maps"
	"math"
	"slices"
)

// Kind is an agent's class.
type Kind struct {
	Hits   int     // hits to suspend
	Speed  float64 // times its role's speed
	Radius float64
	Points int
}

var kinds = map[string]Kind{
	"scout":        {1, 1.7, 1.5, 1},
	"worker":       {2, 1.0, 2.2, 1},
	"orchestrator": {6, 0.6, 3.2, 3},
	"runaway":      {2, 1.6, 2.0, 10},
	"boss":         {24, 0.35, 5.5, 15},
}

// need is the hits an agent of kind needs now: later waves and bigger crowds bring
// armour (growing 25% a wave, so it compounds; +1 per 3 pilots past the first), and a
// boss scales with the crowd. Spawns are capped by the token budget, so toughness is
// what makes a full booth still lose the cluster in the end (wave ~12 for 16 bots).
func (w *World) need(kind string) int {
	k := kindOf(kind)
	n := w.online()
	if kind == "boss" {
		return int(math.Round(float64(k.Hits) * (1 + 0.25*float64(max(n, 1)-1))))
	}
	return k.Hits + int(math.Pow(1.25, float64(max(w.Round.Wave, 1)-1))) - 1 + (max(n, 1)-1)/3
}

func kindOf(k string) Kind {
	if v, ok := kinds[k]; ok {
		return v
	}
	return kinds["worker"]
}

// Tuning for the fun layer, in units and seconds.
const (
	spawnRate     = 1.4 // spawn tokens a second: ~85 task turns a minute, the 100k token budget
	spawnBurst    = 6.0
	comboWindow   = 2.0
	comboMax      = 5
	earlyBonus    = 5 // an orchestrator suspended before it spawned its subagents
	kidsAfter     = 20.0
	bossKidsEvery = 15.0
	fleeRange     = 18.0
	powerTTL      = 12.0
	pickupRange   = 2.8
	rapidFor      = 8.0
	spreadFor     = 8.0
	shieldFor     = 10.0
	bombRadius    = 26.0
	bumpPush      = 24.0
	stunFor       = 0.6
	ramEvery      = 0.4
	teamGap       = 3.0
	squadWindow   = 2.0
	squadMin      = 3
	squadEvery    = 5.0
	squadBonus    = 2
	alarmAt       = 0.9
	savedBelow    = 0.7
	waveBanner    = 3.0  // seconds between a wave's banner and its first spawn
	waveRest      = 10.0 // after a wave's last spawn, the next comes this soon (or when the field is clear)
)

var waveNames = []string{"HELLO WORLD", "CANARY RELEASE", "CRON JOB RUSH", "DEPLOY STORM", "", "MERGE CONFLICT",
	"FLAKY TESTS", "TRAFFIC SPIKE", "AUTOSCALE PANIC", "", "FRIDAY DEPLOY", "INCIDENT AT 3 AM", "THUNDERING HERD", "PAGER STORM", ""}
var bossNames = []string{"BOSS: MEGA ORCHESTRATOR", "BOSS: THE MONOLITH", "BOSS: SKYNET-ISH"}

// waveName is wave n's name (every 5th is a boss fleet).
func waveName(n int) string {
	if n%5 == 0 {
		return bossNames[(n/5-1)%len(bossNames)]
	}
	return waveNames[(n-1)%len(waveNames)]
}

// waveKinds is wave n's agents in spawn order, for online players.
func (w *World) waveKinds(n, online int) []string {
	scale := min(4.0, 0.7+0.3*float64(max(online, 1)))
	count := int(math.Round((3 + 1.6*float64(n)) * scale))
	var out []string
	if n%5 == 0 {
		out = append(out, "boss")
		count /= 2
	}
	runaway := n >= 2 && w.rnd.Float64() < 0.5
	for i := range count {
		r := w.rnd.Float64()
		switch {
		case runaway && i == count/2:
			out = append(out, "runaway")
		case n >= 2 && r < min(0.05+0.03*float64(n), 0.3):
			out = append(out, "orchestrator")
		case r < 0.45:
			out = append(out, "scout")
		default:
			out = append(out, "worker")
		}
	}
	return out
}

// nextWaveNow queues the next wave and announces it.
func (w *World) nextWaveNow() {
	r := &w.Round
	r.Wave++
	r.WaveName, r.Boss = waveName(r.Wave), r.Wave%5 == 0
	r.queue = w.waveKinds(r.Wave, w.online())
	r.nextSpawn, r.queuedAt = w.Now+waveBanner, 0
	w.event(Event{Kind: "wave", Text: r.WaveName, Points: r.Wave, Combo: len(r.queue)})
}

// tickWave spawns the wave's agents at its cadence (within room), and starts the next
// wave once this one is out and either the field is nearly clear or waveRest passed.
func (w *World) tickWave(running int) {
	r := &w.Round
	if len(r.queue) > 0 {
		if w.Now < r.nextSpawn || w.room() <= 0 {
			return
		}
		pos := w.edgePoint()
		w.spawn(r.queue[0], pos)
		r.queue = r.queue[1:]
		gap := max(0.35, 1.5-0.08*float64(r.Wave)) / math.Sqrt(float64(max(w.online(), 1)))
		r.nextSpawn = w.Now + gap
		if len(r.queue) == 0 {
			r.queuedAt = w.Now
		}
		return
	}
	if running <= 2 || w.Now-r.queuedAt >= waveRest {
		w.nextWaveNow()
	}
}

// tickKids lets an orchestrator spawn its subagents when it reaches the cluster (or
// after kidsAfter), and a boss every bossKidsEvery. The subagents are real sessions and
// take room like any spawn.
func (w *World) tickKids(a *Agent) {
	n := 0
	switch {
	case a.Kind == "orchestrator" && !a.KidsDone && (a.Arrived || w.Now-a.Born >= kidsAfter):
		a.KidsDone = true
		n = 2 + w.rnd.IntN(2)
	case a.Kind == "boss" && w.Now >= a.NextKids:
		a.NextKids = w.Now + bossKidsEvery
		n = 3
	default:
		return
	}
	n = min(n, w.room())
	for range n {
		kind := "worker"
		if w.rnd.IntN(2) == 0 {
			kind = "scout"
		}
		ang := w.rnd.Float64() * 2 * math.Pi
		w.spawn(kind, a.Pos.add(Vec{math.Cos(ang), math.Sin(ang)}.mul(kindOf(a.Kind).Radius+1)))
	}
	if n > 0 {
		w.event(Event{Kind: "kids", Agent: a.ID, Text: fmt.Sprint(n)})
	}
}

// bump knocks a ship that flies into a running agent back and stuns its blaster; a
// shielded ship rams the agent instead (one hit every ramEvery).
func (w *World) bump(a *Agent) {
	reach := kindOf(a.Kind).Radius + 1.2
	for _, id := range slices.Sorted(maps.Keys(w.Ships)) {
		s := w.Ships[id]
		if !s.Online || dist(s.Pos, a.Pos) > reach {
			continue
		}
		if w.Now < s.ShieldUntil {
			if a.LastRam == 0 || w.Now-a.LastRam >= ramEvery {
				a.LastRam = w.Now
				w.hit(a, s.ID, 1)
			}
			continue
		}
		if w.Now < s.StunUntil {
			continue
		}
		away := s.Pos.sub(a.Pos)
		if n := away.len(); n > 1e-9 {
			s.Vel = away.mul(bumpPush / n)
		}
		s.StunUntil = w.Now + stunFor
		w.event(Event{Kind: "bump", Agent: a.ID, Player: s.ID})
	}
}

// tension raises the alarm at alarmAt of the workers and says "SAVED IT!" when the
// team pulls it back under savedBelow.
func (w *World) tension(running int) {
	f := float64(running) / float64(max(w.Slots, 1))
	switch {
	case !w.alarm && f >= alarmAt:
		w.alarm = true
		w.event(Event{Kind: "alarm"})
	case w.alarm && f < savedBelow:
		w.alarm = false
		w.event(Event{Kind: "saved"})
	}
}

// kill is one confirmed suspend, for SQUAD SAVE.
type kill struct {
	player string
	at     float64
}

// titles are unlocked by suspends ever.
var titles = []struct {
	At   int
	Name string
}{{0, "Rookie Admin"}, {15, "SRE"}, {50, "Platform Hero"}, {150, "Substrate Legend"}}

// Title is the title for total suspends.
func Title(total int) string {
	t := titles[0].Name
	for _, x := range titles {
		if total >= x.At {
			t = x.Name
		}
	}
	return t
}

// scored credits a confirmed suspend: points by kind (an orchestrator caught before its
// subagents is worth more) times the shooter's combo, the team streak, SQUAD SAVE, a
// title unlock, and maybe a power-up drop.
func (w *World) scored(a *Agent, ms int64) {
	k := kindOf(a.Kind)
	pts := k.Points
	early := a.Kind == "orchestrator" && !a.KidsDone
	if early {
		pts += earlyBonus
	}
	combo := 1
	if s := w.Ships[a.By]; s != nil {
		if s.Suspends > 0 && w.Now-s.LastKill <= comboWindow {
			s.Combo = min(comboMax, s.Combo+1)
		} else {
			s.Combo = 1
		}
		s.LastKill, combo = w.Now, s.Combo
		pts *= combo
		before := Title(s.Total)
		s.Score += pts
		s.Suspends++
		s.Total++
		s.Best = max(s.Best, s.Score)
		if t := Title(s.Total); t != before {
			w.event(Event{Kind: "title", Player: s.ID, Text: t})
		}
	}
	text := a.Kind
	if early {
		text = "early"
	}
	w.event(Event{Kind: "suspended", Agent: a.ID, Player: a.By, Text: text, Ms: ms, Points: pts, Combo: combo})
	if w.Now-w.teamLast <= teamGap {
		w.TeamStreak++
	} else {
		w.TeamStreak = 1
	}
	w.teamLast = w.Now
	w.BestStreak = max(w.BestStreak, w.TeamStreak)
	w.squadSave(a.By)
	w.maybeDrop(a)
}

// squadSave: squadMin or more players suspending within squadWindow is a SQUAD SAVE,
// worth squadBonus to each of them.
func (w *World) squadSave(player string) {
	keep := w.recent[:0]
	for _, k := range w.recent {
		if w.Now-k.at <= squadWindow {
			keep = append(keep, k)
		}
	}
	w.recent = append(keep, kill{player, w.Now})
	who := map[string]bool{}
	for _, k := range w.recent {
		who[k.player] = true
	}
	if len(who) < squadMin || w.Now-w.lastSquad < squadEvery {
		return
	}
	w.lastSquad = w.Now
	for id := range who {
		if s := w.Ships[id]; s != nil {
			s.Score += squadBonus
			s.Best = max(s.Best, s.Score)
		}
	}
	w.event(Event{Kind: "squad", Text: fmt.Sprint(len(who)), Points: squadBonus})
}

// Powerup is a pickup lying in space until Expires.
type Powerup struct {
	ID, Kind string // rapid, spread, shield, bomb
	Pos      Vec
	Expires  float64
}

// maybeDrop leaves a power-up where a was suspended: always from a boss or an
// orchestrator, half the time from a runaway, otherwise 12%.
func (w *World) maybeDrop(a *Agent) {
	chance := 0.12
	switch a.Kind {
	case "boss", "orchestrator":
		chance = 1
	case "runaway":
		chance = 0.5
	}
	if w.rnd.Float64() >= chance {
		return
	}
	var kind string
	switch r := w.rnd.IntN(100); {
	case r < 35:
		kind = "rapid"
	case r < 65:
		kind = "spread"
	case r < 90:
		kind = "shield"
	default:
		kind = "bomb"
	}
	w.seq++
	id := fmt.Sprintf("p%d", w.seq)
	w.Powerups[id] = &Powerup{ID: id, Kind: kind, Pos: a.Pos, Expires: w.Now + powerTTL}
}

func (w *World) tickPowerups() {
	for id, p := range w.Powerups {
		if w.Now >= p.Expires {
			delete(w.Powerups, id)
		}
	}
}

// pickup takes power-ups within reach of s.
func (w *World) pickup(s *Ship) {
	for _, id := range slices.Sorted(maps.Keys(w.Powerups)) {
		p := w.Powerups[id]
		if dist(p.Pos, s.Pos) > pickupRange {
			continue
		}
		delete(w.Powerups, id)
		switch p.Kind {
		case "rapid":
			s.RapidUntil = max(s.RapidUntil, w.Now) + rapidFor
		case "spread":
			s.SpreadUntil = max(s.SpreadUntil, w.Now) + spreadFor
		case "shield":
			s.ShieldUntil = max(s.ShieldUntil, w.Now) + shieldFor
		case "bomb":
			w.bomb(s)
		}
		w.event(Event{Kind: "power", Player: s.ID, Text: p.Kind})
	}
}

// bomb is the Snapshot Bomb: every running agent within bombRadius of s is suspended at
// once (a real batch of Suspends); a boss takes 4 hits instead.
func (w *World) bomb(s *Ship) {
	n := 0
	for _, id := range slices.Sorted(maps.Keys(w.Agents)) {
		a := w.Agents[id]
		if a.State != Running || dist(a.Pos, s.Pos) > bombRadius {
			continue
		}
		if a.Kind == "boss" {
			w.hit(a, s.ID, 4)
			continue
		}
		a.Hits = a.Need
		w.suspend(a, s.ID)
		n++
	}
	w.event(Event{Kind: "bomb", Player: s.ID, Text: fmt.Sprint(n)})
}
