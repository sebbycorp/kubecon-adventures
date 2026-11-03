// This file is runs, squads and scoring. A run is a squad's 3-minute go at the siege:
// it keeps a few agents walking near the squad, paces their heist steps, deletes the
// frozen ones nobody books, and scores freezes (+1) and bookings (+2) for the player
// who made them. The leaderboard ranks players by their best single run. Like World it
// is pure logic driven by the game loop: Tick submits ops to the Scheduler, and every
// Result comes back through OnResult (after World.Resolve for control kinds).
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

// Mission is one squad's run. Outcome is "" while it runs, then "done". Starter is the
// player who started it; Score is the squad's points this run, Scores each player's.
// Targets lists every agent the run has spawned, in order.
type Mission struct {
	ID, Squad     string
	Starter       string
	Targets       []string
	Started, Ends float64
	Score         int
	Scores        map[string]int
	Outcome       string
}

// ScoreRow is one player on the leaderboard. Best is their best single-run score;
// Freezes and Books count every run.
type ScoreRow struct {
	PlayerID, Nick, Squad string
	Best, Freezes, Books  int
	Rig                   int // best RAM tier of an ally the player deployed
}

// Pacing and scoring.
const (
	runSeconds     = 180.0
	runTargetsMax  = 4    // live agents per run: 1 + online squad players, at most this
	ptsFreeze      = 1
	ptsBook        = 2
	frozenTTL      = 8.0 // a frozen agent nobody books is deleted after this long
	stepMin        = 12.0
	stepJitter     = 4.0
	runSteps       = 4 // an agent that finishes its 4th step escapes
	spawnRetry     = 2.0
	maxStepsBusy   = 24 // no new agents while this many steps are running
	maxLiveTargets = 24 // live agents in the whole city (the LLM token budget)
	sayMax         = 60
)

var targetNames = []string{"Slick Vinnie", "Two-Bit Tony", "Lefty Lou", "Mad Maxine", "Big Sal", "Quiet Quinn", "Fast Freddie", "Dutch Dolores"}

// track is the runs' per-target bookkeeping.
type track struct {
	ms       *Mission
	nextAt   float64 // next step (or spawn retry) is due at this time
	spawned  bool
	spawning bool
	stepping bool // a step is in flight; World's Busy is left alone so shots still land
}

// Missions owns every run and the scores. It is not safe for concurrent use; the game
// loop owns it.
type Missions struct {
	rnd      *rand.Rand
	seq      int
	missions []*Mission          // in start order
	active   map[string]*Mission // by squad
	targets  map[string]*track
	players  map[string]*ScoreRow
	squadTD  map[string]int  // freezes per squad
	retire   map[string]bool // allies to delete after a run ends; true once submitted
	perRun   int             // live agents per run at most (runTargetsMax; tests lower it)
}

// NewMissions uses rnd for step pacing, spawn points and target names.
func NewMissions(rnd *rand.Rand) *Missions {
	return &Missions{rnd: rnd, active: map[string]*Mission{}, targets: map[string]*track{},
		players: map[string]*ScoreRow{}, squadTD: map[string]int{}, retire: map[string]bool{}, perRun: runTargetsMax}
}

// Start begins a run for squad. A squad runs one at a time. Its agents spawn as the
// city's caps allow: now if s is not nil, else on the following Ticks.
func (m *Missions) Start(squad string, w *World, s *Scheduler) (*Mission, error) {
	if m.active[squad] != nil {
		return nil, errors.New("your squad is already playing")
	}
	m.seq++
	ms := &Mission{ID: fmt.Sprintf("m%d", m.seq), Squad: squad, Started: w.Now, Ends: w.Now + runSeconds, Scores: map[string]int{}}
	m.missions = append(m.missions, ms)
	m.active[squad] = ms
	if s != nil {
		m.topUp([]*Mission{ms}, w, s)
	}
	return ms, nil
}

// want is how many live agents ms should have: one more than its online players.
func (m *Missions) want(ms *Mission, w *World) int {
	n := 1
	for _, a := range w.Avatars {
		if a.Online && a.Squad == ms.Squad {
			n++
		}
	}
	return min(n, m.perRun)
}

// liveOf counts ms's agents that are spawning, walking or frozen.
func (m *Missions) liveOf(ms *Mission, w *World) int {
	n := 0
	for _, id := range ms.Targets {
		if m.isLive(id, w) {
			n++
		}
	}
	return n
}

func (m *Missions) isLive(id string, w *World) bool {
	tr := m.targets[id]
	if t := w.Targets[id]; t != nil {
		return t.State == Working || t.State == Frozen
	}
	return tr != nil && !tr.spawned && tr.ms.Outcome == ""
}

// live counts every live agent in the city.
func (m *Missions) live(w *World) int {
	n := 0
	for _, ms := range m.missions {
		n += m.liveOf(ms, w)
	}
	return n
}

// spawn submits a spawn for id, or schedules a retry.
func (m *Missions) spawn(tr *track, id string, w *World, s *Scheduler) {
	if s.Submit("spawn", id, "", 0, 0) {
		tr.spawning = true
	} else {
		tr.nextAt = w.Now + spawnRetry
	}
}

// Tick ends runs whose time is up, tops up the running ones with agents (the run with
// the fewest live agents first, one at a time, while the city has room), and paces
// steps and frozen-agent deletes.
func (m *Missions) Tick(w *World, s *Scheduler) {
	var running []*Mission
	for _, ms := range m.missions {
		if ms.Outcome != "" {
			continue
		}
		if w.Now >= ms.Ends {
			m.finish(ms, w)
			continue
		}
		running = append(running, ms)
		for _, id := range ms.Targets {
			m.tickTarget(ms, id, w, s)
		}
	}
	m.topUp(running, w, s)
	m.submitRetires(w, s)
}

func (m *Missions) topUp(running []*Mission, w *World, s *Scheduler) {
	total := m.live(w)
	have := map[*Mission]int{}
	for _, ms := range running {
		have[ms] = m.liveOf(ms, w)
	}
	for total < maxLiveTargets {
		steps, _, _ := s.InFlight()
		if steps >= maxStepsBusy {
			return
		}
		var pick *Mission
		for _, ms := range running {
			if have[ms] < m.want(ms, w) && (pick == nil || have[ms] < have[pick]) {
				pick = ms
			}
		}
		if pick == nil {
			return
		}
		id := fmt.Sprintf("%s-%d", pick.ID, len(pick.Targets)+1)
		pick.Targets = append(pick.Targets, id)
		tr := &track{ms: pick}
		m.targets[id] = tr
		m.spawn(tr, id, w, s)
		have[pick]++
		total++
	}
}

func (m *Missions) tickTarget(ms *Mission, id string, w *World, s *Scheduler) {
	tr := m.targets[id]
	t := w.Targets[id]
	switch {
	case tr == nil:
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
		if t.Busy || w.Now-t.FrozenAt < frozenTTL-eps {
			return
		}
		w.start(t, "delete", "")
		if !s.Submit("delete", id, t.SessionID, 0, 0) {
			t.Busy, t.opBy, t.opKind = false, "", ""
		}
	}
}

func (m *Missions) pace(now float64) float64 { return now + stepMin + m.rnd.Float64()*stepJitter }

// OnResult applies a scheduler Result. For control kinds the game calls World.Resolve
// first. Points go to the player who froze or booked the agent, in their own squad's
// running run, whichever run spawned it.
func (m *Missions) OnResult(r Result, w *World) {
	if m.allyResult(r, w) {
		return
	}
	t := w.Targets[r.Target]
	if r.Kind == "freeze" && r.OK && t != nil && t.FrozenBy != "" && t.State == Frozen {
		p := m.player(t.FrozenBy, w)
		p.Freezes++
		m.squadTD[p.Squad]++
		m.score(p, ptsFreeze, w)
	}
	if r.Kind == "extract" && r.OK && t != nil && t.State == Booked {
		if pid := lastPlayer(w, t.ID); pid != "" {
			p := m.player(pid, w)
			p.Books++
			m.score(p, ptsBook, w)
		}
		if r.Step.Say != "" {
			// The booked agent's last words, from the turn its checkpoint was taken on.
			t.Say = clip(r.Step.Say)
			w.Events = append(w.Events, Event{Kind: "booked-line", Target: t.ID, Text: t.Say})
		}
	}
	if r.Kind == "step" && !r.OK && strings.HasPrefix(r.Err, "policy: ") && t != nil {
		// agentgateway stopped the agent's LLM call (prompt guard or rate limit).
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
			Pos: m.spawnPoint(ms, r.Node, w), State: Working,
			Steps: runSteps, New: true, HitBy: map[string]bool{}}
	case "step":
		tr.stepping = false
		m.onStep(ms, t, r, w)
	case "freeze":
		if !r.OK && t != nil && t.escapePending && t.State == Working {
			m.escape(t, w) // the freeze that held the escape failed
		}
	}
}

// score adds pts to p's run (the running run of p's squad) and their best.
func (m *Missions) score(p *ScoreRow, pts int, w *World) {
	ms := m.active[p.Squad]
	if ms == nil || ms.Outcome != "" {
		return
	}
	ms.Score += pts
	ms.Scores[p.PlayerID] += pts
	p.Best = max(p.Best, ms.Scores[p.PlayerID])
}

// spawnPoint is near a random online player of ms's squad, or a hideout in the
// agent's node district when none is online.
func (m *Missions) spawnPoint(ms *Mission, node string, w *World) Vec {
	var near []Vec
	for _, id := range slices.Sorted(maps.Keys(w.Avatars)) {
		if a := w.Avatars[id]; a.Online && !a.Down && a.Squad == ms.Squad {
			near = append(near, a.Pos)
		}
	}
	if len(near) == 0 {
		return w.City.Hideout(node, m.rnd)
	}
	return w.City.Near(near[m.rnd.IntN(len(near))], node, m.rnd)
}

// onStep advances a Working agent. A step only counts when its turn completes; an OK
// last step means the agent escapes (step_done is not required, so it can't loop).
// The next step was already scheduled when this one was submitted.
func (m *Missions) onStep(ms *Mission, t *Target, r Result, w *World) {
	if t == nil || t.State != Working || !r.OK {
		return
	}
	t.Say = clip(r.Step.Say)
	if r.Node != "" {
		t.Node = r.Node
	}
	if t.Step+1 < t.Steps {
		t.Step++
		return
	}
	t.Step = t.Steps
	if t.Busy && t.opKind == "freeze" {
		// The freeze was fired before the escape completed: it wins. OnResult applies
		// the escape only if that freeze fails.
		t.escapePending = true
		return
	}
	m.escape(t, w)
}

// escape marks t gone: it finished its heist. The run goes on.
func (m *Missions) escape(t *Target, w *World) {
	t.State, t.escapePending = Escaped, false
	w.Events = append(w.Events, Event{Kind: "escaped", Target: t.ID, Text: t.Name})
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

// finish ends a run when its time is up and retires the squad's allies.
func (m *Missions) finish(ms *Mission, w *World) {
	ms.Outcome = "done"
	m.retireAllies(ms, w)
	if m.active[ms.Squad] == ms {
		delete(m.active, ms.Squad)
	}
	w.Events = append(w.Events, Event{Kind: "run-done", Player: ms.Squad, Text: ms.ID})
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

// Leaderboard ranks players by best run, then total freezes, then nick.
func (m *Missions) Leaderboard() []ScoreRow {
	out := make([]ScoreRow, 0, len(m.players))
	for _, p := range m.players {
		out = append(out, *p)
	}
	slices.SortFunc(out, func(a, b ScoreRow) int {
		return cmp.Or(cmp.Compare(b.Best, a.Best), cmp.Compare(b.Freezes, a.Freezes),
			cmp.Compare(a.Nick, b.Nick), cmp.Compare(a.PlayerID, b.PlayerID))
	})
	return out
}

// lastRun is squad's running run, or else its newest one still in the list.
func (m *Missions) lastRun(squad string) *Mission {
	if ms := m.active[squad]; ms != nil {
		return ms
	}
	for i := len(m.missions) - 1; i >= 0; i-- {
		if m.missions[i].Squad == squad {
			return m.missions[i]
		}
	}
	return nil
}

// SquadTotals is freezes per squad, counted for the squad the player was in.
func (m *Missions) SquadTotals() map[string]int { return maps.Clone(m.squadTD) }

// clip limits an agent's line to sayMax characters.
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
