// This file is missions, squads and scoring. Missions spawn targets, pace their heist
// steps, run the target's crew (revives), decide each mission's outcome, and keep the
// takedown leaderboard, and delete the squad's allies when a mission ends. Like World
// it is pure logic driven by the game loop: Tick submits ops to the Scheduler, and
// every Result comes back through OnResult (after World.Resolve for control kinds).
package heist

import (
	"cmp"
	"errors"
	"fmt"
	"maps"
	"math/rand/v2"
	"slices"
	"strings"
	"sync"
)

// Level is one mission template.
type Level struct {
	N              int
	Name           string
	Targets, Steps int
	Seconds        float64
	MustBook       bool
}

// Levels are the booth levels.
var Levels = []Level{{1, "Rookie", 1, 4, 90, false}, {2, "Booking", 1, 4, 120, true}, {3, "The Crew", 3, 4, 180, true}}

// Mission is one squad's run at a level. Outcome is "", "won", "failed" or "escaped".
type Mission struct {
	ID, Squad     string
	Level         Level
	Targets       []string
	Started, Ends float64
	Stars         int
	Cash          int
	Outcome       string
	Queued        bool

	earned, penalty int             // Cash = earned - penalty (earned doubled on a timely win)
	crew            map[string]bool // squad players who hit, froze, booked or deleted a target
}

// ScoreRow is one player on the leaderboard. Level is the highest level they won.
type ScoreRow struct {
	PlayerID, Nick, Squad  string
	Takedowns, Cash, Level int
	Rig                    int // best RAM tier of an ally the player deployed
}

// Scoring and pacing.
const (
	cashStun        = 50
	cashBook        = 300
	cashDelete      = 100
	cashRivalSpring = 100
	maxStars        = 5
	reviveDelay     = 30.0
	reviveStar      = 5.0 // each star above 1 shortens the revive delay by this much
	reviveMin       = 15.0
	stepMin         = 15.0
	stepJitter      = 10.0
	spawnRetry      = 2.0
	maxStepsBusy    = 24 // missions queue while this many steps are running
	sayMax          = 60
)

var targetNames = []string{"Slick Vinnie", "Two-Bit Tony", "Lefty Lou", "Mad Maxine", "Big Sal", "Quiet Quinn", "Fast Freddie", "Dutch Dolores"}

// track is the missions' per-target bookkeeping.
type track struct {
	ms       *Mission
	nextAt   float64 // next step (or spawn retry) is due at this time
	spawned  bool
	spawning bool
	stepping bool    // a step is in flight; World's Busy is left alone so shots still land
	crewAt   float64 // last crew spring attempt
	crewing  bool    // the spring in flight is the crew's
}

// Missions owns every mission and the scores. It is not safe for concurrent use; the
// game loop owns it.
type Missions struct {
	rnd      *rand.Rand
	seq      int
	missions []*Mission          // in start order
	active   map[string]*Mission // by squad
	targets  map[string]*track
	best     map[string]int // highest level won, by squad
	players  map[string]*ScoreRow
	squadTD  map[string]int
	retire   map[string]bool // allies to delete after a mission end; true once submitted
}

// NewMissions uses rnd for step pacing and target names.
func NewMissions(rnd *rand.Rand) *Missions {
	return &Missions{rnd: rnd, active: map[string]*Mission{}, targets: map[string]*track{},
		best: map[string]int{}, players: map[string]*ScoreRow{}, squadTD: map[string]int{}, retire: map[string]bool{}}
}

// Start begins a mission for squad. Level 1 is always open; higher levels need the
// previous one won, and a squad runs one mission at a time. When the step cap is full
// the mission is Queued and its targets spawn on a later Tick.
func (m *Missions) Start(squad string, level int, w *World, s *Scheduler) (*Mission, error) {
	if level < 1 || level > len(Levels) {
		return nil, fmt.Errorf("no level %d", level)
	}
	if level > m.best[squad]+1 {
		return nil, fmt.Errorf("level %d is locked: win level %d first", level, level-1)
	}
	if m.active[squad] != nil {
		return nil, errors.New("squad already has a mission")
	}
	m.seq++
	lv := Levels[level-1]
	ms := &Mission{ID: fmt.Sprintf("m%d", m.seq), Squad: squad, Level: lv, Stars: 1, Queued: true, crew: map[string]bool{}}
	for i := 1; i <= lv.Targets; i++ {
		id := fmt.Sprintf("%s-%d", ms.ID, i)
		ms.Targets = append(ms.Targets, id)
		m.targets[id] = &track{ms: ms}
	}
	m.missions = append(m.missions, ms)
	m.active[squad] = ms
	if steps, _, _ := s.InFlight(); steps < maxStepsBusy {
		m.launch(ms, w, s)
	}
	return ms, nil
}

// launch starts a mission's timer and spawns its targets.
func (m *Missions) launch(ms *Mission, w *World, s *Scheduler) {
	ms.Queued, ms.Started, ms.Ends = false, w.Now, w.Now+ms.Level.Seconds
	for _, id := range ms.Targets {
		m.spawn(m.targets[id], id, w, s)
	}
}

func (m *Missions) spawn(tr *track, id string, w *World, s *Scheduler) {
	if s.Submit("spawn", id, "", 0, 0) {
		tr.spawning = true
	} else {
		tr.nextAt = w.Now + spawnRetry
	}
}

// Tick launches the oldest queued mission once a step slot is free, fails missions
// whose time is up, and paces steps and crew revives.
func (m *Missions) Tick(w *World, s *Scheduler) {
	if steps, _, _ := s.InFlight(); steps < maxStepsBusy {
		for _, ms := range m.missions {
			if ms.Queued && ms.Outcome == "" {
				m.launch(ms, w, s)
				break
			}
		}
	}
	for _, ms := range m.missions {
		if ms.Queued || ms.Outcome != "" {
			continue
		}
		if w.Now >= ms.Ends {
			m.finish(ms, "failed", w)
			continue
		}
		for _, id := range ms.Targets {
			m.tickTarget(ms, id, w, s)
		}
	}
	m.submitRetires(w, s)
}

func (m *Missions) tickTarget(ms *Mission, id string, w *World, s *Scheduler) {
	tr := m.targets[id]
	t := w.Targets[id]
	switch {
	case !tr.spawned:
		if !tr.spawning && w.Now >= tr.nextAt {
			m.spawn(tr, id, w, s)
		}
	case t == nil:
	case t.State == Working:
		if !t.Busy && !tr.stepping && w.Now >= tr.nextAt && s.Submit("step", id, t.SessionID, t.Step+1, t.Steps) {
			tr.stepping, tr.nextAt = true, m.pace(w.Now) // start-to-start cadence
		}
	case t.State == Frozen:
		since := max(t.FrozenAt, tr.crewAt)
		if t.Busy || w.Now-since < m.reviveAfter(ms) || !m.crewFree(ms, id, w) {
			return
		}
		tr.crewAt = w.Now
		if s.Submit("spring", id, t.SessionID, 0, 0) {
			t.Busy, tr.crewing = true, true
		}
	}
}

// reviveAfter is the crew's revive delay: 30 s, 5 s less per star above 1, at least 15.
func (m *Missions) reviveAfter(ms *Mission) float64 {
	return max(reviveMin, reviveDelay-reviveStar*float64(ms.Stars-1))
}

// crewFree reports whether another of the mission's targets is still Working.
func (m *Missions) crewFree(ms *Mission, except string, w *World) bool {
	for _, id := range ms.Targets {
		if t := w.Targets[id]; id != except && t != nil && t.State == Working {
			return true
		}
	}
	return false
}

func (m *Missions) pace(now float64) float64 { return now + stepMin + m.rnd.Float64()*stepJitter }

// OnResult applies a scheduler Result. For control kinds the game calls World.Resolve
// first. Takedowns are credited for any target; the rest only for mission targets.
func (m *Missions) OnResult(r Result, w *World) {
	if m.allyResult(r, w) {
		return
	}
	t := w.Targets[r.Target]
	if r.Kind == "freeze" && r.OK && t != nil && t.FrozenBy != "" {
		p := m.player(t.FrozenBy, w)
		p.Takedowns++
		m.squadTD[p.Squad]++
	}
	if r.Kind == "extract" && r.OK && t != nil && r.Step.Say != "" {
		// The booked target's last words, from the turn its checkpoint was taken on.
		t.Say = clip(r.Step.Say)
		w.Events = append(w.Events, Event{Kind: "booked-line", Target: t.ID, Text: t.Say})
	}
	if (r.Kind == "step" || r.Kind == "spring") && !r.OK && strings.HasPrefix(r.Err, "policy: ") && t != nil {
		// agentgateway stopped the target's LLM call (prompt guard or rate limit).
		why, _, _ := strings.Cut(strings.TrimPrefix(r.Err, "policy: "), ":")
		w.Events = append(w.Events, Event{Kind: "policy", Target: t.ID, Text: why})
	}
	tr := m.targets[r.Target]
	if tr == nil {
		return
	}
	ms := tr.ms
	switch r.Kind {
	case "spawn":
		tr.spawning = false
		if !r.OK {
			tr.nextAt = w.Now + spawnRetry
			return
		}
		tr.spawned, tr.nextAt = true, w.Now // the heist starts on arrival
		w.Targets[r.Target] = &Target{ID: r.Target, SessionID: r.Spawned, MissionID: ms.ID,
			Name: targetNames[m.rnd.IntN(len(targetNames))], Node: r.Node, Squad: ms.Squad,
			Pos: w.City.Hideout(r.Node, m.rnd), State: Working,
			Steps: ms.Level.Steps, New: true, HitBy: map[string]bool{}}
		return
	case "step":
		tr.stepping = false
		m.onStep(ms, tr, t, r, w)
	case "spring":
		crew := tr.crewing
		tr.crewing = false
		if !r.OK || t == nil {
			return
		}
		t.Say = clip(r.Step.Say)
		if r.Node != "" {
			t.Node = r.Node
		}
		tr.nextAt = m.pace(w.Now)
		if !crew && ms.Outcome == "" {
			ms.penalty += cashRivalSpring
			ms.Stars = min(maxStars, ms.Stars+1)
		}
	case "freeze", "extract", "delete":
		if !r.OK || t == nil || ms.Outcome != "" {
			return
		}
		m.onTakedown(ms, t, r.Kind, w)
	}
	ms.Cash = ms.earned - ms.penalty
	m.check(ms, w)
}

// onStep advances a Working target. A step only counts when its turn completes; an OK
// last step means the target escapes (step_done is not required, so it can't loop).
// The next step was already scheduled when this one was submitted.
func (m *Missions) onStep(ms *Mission, tr *track, t *Target, r Result, w *World) {
	if t == nil || t.State != Working || ms.Outcome != "" {
		return
	}
	if !r.OK {
		return
	}
	t.Say = clip(r.Step.Say)
	if r.Node != "" {
		t.Node = r.Node
	}
	if t.Step+1 < t.Steps {
		t.Step++
		ms.Stars = min(maxStars, ms.Stars+1)
		return
	}
	t.Step, t.State = t.Steps, Escaped
	ms.Stars = min(maxStars, ms.Stars+1)
	w.Events = append(w.Events, Event{Kind: "escaped", Target: t.ID, Text: t.Name})
	m.finish(ms, "escaped", w)
}

// onTakedown adds mission cash for a freeze, booking or delete and records which
// squad players took part.
func (m *Missions) onTakedown(ms *Mission, t *Target, kind string, w *World) {
	switch kind {
	case "freeze":
		ms.earned += cashStun
		for p := range t.HitBy {
			m.addCrew(ms, p, w)
		}
		m.addCrew(ms, t.FrozenBy, w)
	case "extract":
		ms.earned += cashBook
		m.addCrew(ms, lastPlayer(w, t.ID), w)
	case "delete":
		ms.earned += cashDelete
		m.addCrew(ms, lastPlayer(w, t.ID), w)
	}
}

func (m *Missions) addCrew(ms *Mission, player string, w *World) {
	if a := w.Avatars[player]; a != nil && a.Squad == ms.Squad {
		ms.crew[player] = true
	}
}

// lastPlayer is the player on the newest event about target (Resolve just added it).
func lastPlayer(w *World, target string) string {
	for i := len(w.Events) - 1; i >= 0; i-- {
		if w.Events[i].Target == target {
			return w.Events[i].Player
		}
	}
	return ""
}

// check ends a mission that is won (every target down, or booked on MustBook levels)
// or lost (a target deleted on a MustBook level).
func (m *Missions) check(ms *Mission, w *World) {
	if ms.Outcome != "" || ms.Queued {
		return
	}
	for _, id := range ms.Targets {
		t := w.Targets[id]
		if t == nil {
			return
		}
		switch {
		case ms.Level.MustBook && t.State == Deleted:
			m.finish(ms, "failed", w)
			return
		case ms.Level.MustBook && t.State != Booked,
			t.State == Working || t.State == Thawing || t.State == Escaped:
			return
		}
	}
	m.finish(ms, "won", w)
}

// finish sets the outcome and pays out. Earned cash doubles on a win before the timer
// ends; penalties are not doubled. The total is split evenly between the squad's
// players who took part; the remainder goes to the first by player ID.
func (m *Missions) finish(ms *Mission, outcome string, w *World) {
	ms.Outcome = outcome
	m.retireAllies(ms, w)
	if m.active[ms.Squad] == ms {
		delete(m.active, ms.Squad)
	}
	if outcome == "won" && w.Now < ms.Ends {
		ms.earned *= 2
	}
	ms.Cash = ms.earned - ms.penalty
	if outcome == "won" {
		m.best[ms.Squad] = max(m.best[ms.Squad], ms.Level.N)
		for id, a := range w.Avatars {
			if a.Squad == ms.Squad {
				p := m.player(id, w)
				p.Level = max(p.Level, ms.Level.N)
			}
		}
	}
	crew := slices.Sorted(maps.Keys(ms.crew))
	for i, id := range crew {
		share := ms.Cash / len(crew)
		if i == 0 {
			share += ms.Cash % len(crew)
		}
		m.player(id, w).Cash += share
	}
	w.Events = append(w.Events, Event{Kind: "mission-" + outcome, Player: ms.Squad, Text: ms.ID})
}

// player is the score row for id, refreshed with the avatar's nick and squad.
func (m *Missions) player(id string, w *World) *ScoreRow {
	p := m.players[id]
	if p == nil {
		p = &ScoreRow{PlayerID: id, Nick: id}
		m.players[id] = p
	}
	if a := w.Avatars[id]; a != nil {
		p.Nick, p.Squad = a.Nick, a.Squad
	}
	return p
}

// Leaderboard ranks players by takedowns, then cash, then nick.
func (m *Missions) Leaderboard() []ScoreRow {
	out := make([]ScoreRow, 0, len(m.players))
	for _, p := range m.players {
		out = append(out, *p)
	}
	slices.SortFunc(out, func(a, b ScoreRow) int {
		return cmp.Or(cmp.Compare(b.Takedowns, a.Takedowns), cmp.Compare(b.Cash, a.Cash),
			cmp.Compare(a.Nick, b.Nick), cmp.Compare(a.PlayerID, b.PlayerID))
	})
	return out
}

// SquadTotals is takedowns per squad, counted for the squad the player was in.
func (m *Missions) SquadTotals() map[string]int { return maps.Clone(m.squadTD) }

// clip limits a target's line to sayMax characters.
func clip(s string) string {
	if r := []rune(s); len(r) > sayMax {
		return string(r[:sayMax])
	}
	return s
}

// Squads groups players. Solo players get their own squad; shared squads use a
// 4-letter code and hold up to 4 players. Safe for concurrent use.
type Squads struct {
	mu      sync.Mutex
	rnd     *rand.Rand
	of      map[string]string
	members map[string]map[string]bool // shared squads only
}

const squadMax = 4

// NewSquads uses rnd for codes.
func NewSquads(rnd *rand.Rand) *Squads {
	return &Squads{rnd: rnd, of: map[string]string{}, members: map[string]map[string]bool{}}
}

// Solo puts playerID in a squad of their own.
func (q *Squads) Solo(playerID string) string {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.leave(playerID)
	q.of[playerID] = "solo-" + playerID
	return q.of[playerID]
}

// Create makes a new squad led by playerID and returns its code (also its ID).
func (q *Squads) Create(playerID string) string {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.leave(playerID)
	var code string
	for code == "" || q.members[code] != nil {
		b := make([]byte, 4)
		for i := range b {
			b[i] = byte('A' + q.rnd.IntN(26))
		}
		code = string(b)
	}
	q.members[code] = map[string]bool{playerID: true}
	q.of[playerID] = code
	return code
}

// Join adds playerID to the squad with code.
func (q *Squads) Join(playerID, code string) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	mem := q.members[code]
	switch {
	case mem == nil:
		return fmt.Errorf("no squad %q", code)
	case mem[playerID]:
		return nil
	case len(mem) >= squadMax:
		return fmt.Errorf("squad %s is full", code)
	}
	q.leave(playerID)
	mem[playerID] = true
	q.of[playerID] = code
	return nil
}

// Of is playerID's squad, or "" before they chose one.
func (q *Squads) Of(playerID string) string {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.of[playerID]
}

// leave removes playerID from their shared squad, dropping it when empty.
func (q *Squads) leave(playerID string) {
	code := q.of[playerID]
	if mem := q.members[code]; mem != nil {
		delete(mem, playerID)
		if len(mem) == 0 {
			delete(q.members, code)
		}
	}
	delete(q.of, playerID)
}
