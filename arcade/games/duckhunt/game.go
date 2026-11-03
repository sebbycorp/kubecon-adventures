// Package duckhunt is Duck Hunt (at /duck-hunt): a landscape phone shooter where every
// flying target is a real kagent session of the "duck" agent on Agent Substrate. A pool
// of sessions sleeps as Substrate snapshots; launching a target is a real Resume (it wakes
// on a worker), a hit is a real Suspend (it goes back into Snapshot Storage), and an
// escaped target is suspended too. Now and then a waking agent says a real line (one LLM
// turn). Runs are timed, scored on the server, and ranked on the leaderboard.
package duckhunt

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/sebbycorp/kubecon-adventures/arcade/internal/kagent"
	"github.com/sebbycorp/kubecon-adventures/arcade/internal/platform"
)

// Ops is what the game needs from kagent (a *kagent.Kagent for the "duck" agent).
type Ops interface {
	CreateSession(ctx context.Context, name string) (string, error)
	Prompt(ctx context.Context, sessionID, text string) (kagent.Reply, error)
	Suspend(ctx context.Context, sessionID string) error
	Resume(ctx context.Context, sessionID string) error
	ListSessionIDs(ctx context.Context) ([]string, error)
	ActorStates(ctx context.Context) (map[string]kagent.Actor, error)
}

// Tuning.
const (
	RunSeconds   = 75  // a run's length
	poolMin      = 24  // sessions the pool tops itself up to
	poolMax      = 60  // sessions it keeps (older duck sessions beyond this are left alone)
	maxAwake     = 10  // targets awake at once across the arcade (the ducks pool has 12 workers)
	maxRuns      = 40  // runs at once
	minFlight    = 250 // ms a target must have flown before a hit counts
	minHitGap    = 110 // ms between two hits of one run
	comboWindow  = 2200 * time.Millisecond
	sayEvery     = 5 // about one launch in sayEvery says a real line
	sayGap       = 4 * time.Second
	opTimeout    = 30 * time.Second
	createConcur = 2
)

// Kind is a target type: its points and how the phone flies it.
type Kind struct {
	ID     string `json:"id"`
	Points int    `json:"points"`
}

var (
	Agent  = Kind{"agent", 100}
	Swift  = Kind{"swift", 150}
	Golden = Kind{"golden", 500}
)

// Mult is the combo multiplier for the c-th hit in a row: 1, 1, 2, 2, 3, 3, 4, 4, 5...
func Mult(c int) int { return 1 + min(4, max(0, c-1)/2) }

// Wave is the run's wave (1 every 15 s); waves fly faster and allow more targets.
func Wave(elapsed time.Duration) int { return 1 + int(elapsed/(15*time.Second)) }

// perRun is how many targets a run may have in the air at once at a given wave.
func perRun(wave int) int { return min(3, 1+(wave-1)/2) }

// pickKind chooses a launch's kind: golden is rare, swift gets likelier each wave.
func pickKind(r *rand.Rand, wave int) Kind {
	switch x := r.Float64(); {
	case x < 1.0/14:
		return Golden
	case x < 1.0/14+min(0.5, 0.1*float64(wave)):
		return Swift
	}
	return Agent
}

var (
	ErrBusy    = errors.New("all agents are busy, retry")
	ErrRunOver = errors.New("run over")
	ErrNoRun   = errors.New("no such run")
	ErrFull    = errors.New("the arcade is full, try again in a minute")
)

type slot struct {
	sid    string
	state  string // asleep, waking, awake, sleeping, broken
	flight string
	// op serializes a say turn and the suspend that ends the flight: a turn sent after
	// the suspend would wake the agent again, untracked.
	op sync.Mutex
}

type flight struct {
	id, run, sid string
	kind         Kind
	launched     time.Time
	state        string // flying, hit, escaped
}

type run struct {
	id, player, nick, color string
	started, ends           time.Time
	score, hits, escapes    int
	combo, bestCombo        int
	golden                  int
	lastHit                 time.Time
	air                     int // flights waking or flying
	done                    bool
}

// best is one player's leaderboard row: their best run.
type best struct {
	nick, color      string
	score, hits      int
	bestCombo, runs  int
	golden, totalHit int
}

// Event is a moment the big screen shows: a hit, a wake, a line, a run's end.
type Event struct {
	Type   string `json:"type"` // hit, woke, say, escape, end
	Flight string `json:"flight,omitempty"`
	Nick   string `json:"nick,omitempty"`
	Color  string `json:"color,omitempty"`
	Kind   string `json:"kind,omitempty"`
	Points int    `json:"points,omitempty"`
	Mult   int    `json:"mult,omitempty"`
	Ms     int64  `json:"ms,omitempty"`
	Text   string `json:"text,omitempty"`
	At     int64  `json:"at"`
}

type Game struct {
	mu      sync.Mutex
	p       *platform.Platform
	k       Ops
	idle    time.Duration
	rnd     *rand.Rand
	slots   []*slot
	flights map[string]*flight
	runs    map[string]*run
	bests   map[string]*best // by player ID
	events  []Event
	lastSay time.Time
	hits    int
	tokens  kagent.Usage
	turns   int64
	timings *platform.Timings // resume, suspend, say
	hub     *platform.Hub
	now     func() time.Time
}

// New builds the game on the duck agent's kagent client. idle (8-30 s) is how long a
// target may fly without a hit or an escape report before the server puts it back to sleep.
func New(p *platform.Platform, k Ops, idle int) *Game {
	g := &Game{p: p, k: k, idle: time.Duration(min(max(idle, 8), 30)) * time.Second, rnd: rand.New(rand.NewPCG(uint64(time.Now().UnixNano()), 7)),
		flights: map[string]*flight{}, runs: map[string]*run{}, bests: map[string]*best{},
		timings: platform.NewTimings(), hub: platform.NewHub(), now: time.Now}
	if p != nil {
		p.OnChange(g.hub.Poke)
	}
	return g
}

// Run fills the pool and starts the housekeeping loop and the live-update hub.
func (g *Game) Run(ctx context.Context) {
	go g.hub.Run(ctx, func() any { return g.Snapshot() })
	go g.fillPool(ctx)
	go func() {
		t := time.NewTicker(time.Second)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				g.housekeep()
			}
		}
	}()
}

// fillPool adopts the duck agent's existing sessions (putting any awake one to sleep),
// then creates sessions until there are poolMin. A session joins the pool asleep, after
// one turn has placed its actor and a suspend has snapshotted it.
func (g *Game) fillPool(ctx context.Context) {
	c, cancel := context.WithTimeout(ctx, time.Minute)
	ids, err := g.k.ListSessionIDs(c)
	states, serr := g.k.ActorStates(c)
	cancel()
	if err != nil || serr != nil {
		slog.Warn("duck hunt: listing duck sessions", "err", errors.Join(err, serr))
	}
	adopted := 0
	for _, id := range ids {
		if adopted >= poolMax {
			break
		}
		a, ok := states[id]
		if !ok {
			continue // never ran a turn: no actor to wake
		}
		if a.State != "SUSPENDED" {
			sc, cancel := context.WithTimeout(ctx, opTimeout)
			err := settle(sc, func() error { return g.k.Suspend(sc, id) })
			cancel()
			if err != nil {
				continue
			}
		}
		g.addSlot(id)
		adopted++
	}
	slog.Info("duck hunt: pool", "adopted", adopted)
	sem := make(chan struct{}, createConcur)
	var wg sync.WaitGroup
	for i := adopted; i < poolMin; i++ {
		select {
		case <-ctx.Done():
			return
		case sem <- struct{}{}:
		}
		wg.Add(1)
		go func(i int) {
			defer func() { <-sem; wg.Done() }()
			if id, err := g.newSession(ctx, i); err == nil {
				g.addSlot(id)
			} else {
				slog.Warn("duck hunt: new pool session", "err", err)
			}
		}(i)
	}
	wg.Wait()
}

func (g *Game) newSession(ctx context.Context, i int) (string, error) {
	c, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()
	id, err := g.k.CreateSession(c, fmt.Sprintf("agent-hunt-%d", i))
	if err != nil {
		return "", err
	}
	g.noteTurn(g.k.Prompt(c, id, "You just hatched. Pick your flight."))
	if err := settle(c, func() error { return g.k.Suspend(c, id) }); err != nil {
		return "", err
	}
	return id, nil
}

func (g *Game) addSlot(id string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if !slices.ContainsFunc(g.slots, func(s *slot) bool { return s.sid == id }) {
		g.slots = append(g.slots, &slot{sid: id, state: "asleep"})
		g.hub.Poke()
	}
}

// noteTurn counts an LLM turn's tokens (the reply text is never logged).
func (g *Game) noteTurn(r kagent.Reply, err error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.turns++
	g.tokens.Input += r.Usage.Input
	g.tokens.Output += r.Usage.Output
}

// --- runs ------------------------------------------------------------------

// Start begins a run for the player, ending any run they still had.
func (g *Game) Start(pl platform.Player) (string, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	now := g.now()
	live := 0
	for _, r := range g.runs {
		if r.player == pl.ID && !r.done {
			g.endLocked(r)
		}
		if !r.done {
			live++
		}
	}
	if live >= maxRuns {
		return "", ErrFull
	}
	r := &run{id: platform.NewID(), player: pl.ID, nick: pl.Nick, color: pl.Color, started: now, ends: now.Add(RunSeconds * time.Second)}
	g.runs[r.id] = r
	g.hub.Poke()
	return r.id, nil
}

func (g *Game) ownRun(player, runID string) (*run, error) {
	r := g.runs[runID]
	switch {
	case r == nil:
		return nil, ErrNoRun
	case r.player != player:
		return nil, platform.ErrNotYours
	}
	return r, nil
}

// Launched is a target that just woke: the phone flies it.
type Launched struct {
	Flight string `json:"flight"`
	Kind   string `json:"kind"`
	Points int    `json:"points"`
	Wave   int    `json:"wave"`
	WokeMs int64  `json:"wokeMs"`
}

// Launch wakes a sleeping pool session (a real Substrate Resume) as the run's next
// target. ErrBusy means the run or the arcade has as many awake as it may: retry soon.
func (g *Game) Launch(ctx context.Context, player, runID string) (Launched, error) {
	g.mu.Lock()
	r, err := g.ownRun(player, runID)
	if err != nil {
		g.mu.Unlock()
		return Launched{}, err
	}
	now := g.now()
	if r.done || now.After(r.ends) {
		g.mu.Unlock()
		return Launched{}, ErrRunOver
	}
	wave := Wave(now.Sub(r.started))
	awake := 0
	var free []*slot
	for _, s := range g.slots {
		switch s.state {
		case "asleep":
			free = append(free, s)
		case "waking", "awake", "sleeping":
			awake++
		}
	}
	if r.air >= perRun(wave) || awake >= maxAwake || len(free) == 0 {
		g.mu.Unlock()
		return Launched{}, ErrBusy
	}
	s := free[g.rnd.IntN(len(free))]
	f := &flight{id: platform.NewID(), run: r.id, sid: s.sid, kind: pickKind(g.rnd, wave), state: "flying"}
	s.state, s.flight = "waking", f.id
	r.air++
	say := g.rnd.IntN(sayEvery) == 0 && now.Sub(g.lastSay) > sayGap
	if say {
		g.lastSay = now
	}
	g.mu.Unlock()

	c, cancel := context.WithTimeout(ctx, opTimeout)
	t0 := time.Now()
	err = settle(c, func() error { return g.k.Resume(c, s.sid) })
	ms := time.Since(t0).Milliseconds()
	cancel()

	g.mu.Lock()
	defer g.mu.Unlock()
	if err != nil {
		slog.Warn("duck hunt: resume", "session", s.sid, "err", err)
		s.state, s.flight = "asleep", ""
		r.air--
		return Launched{}, ErrBusy
	}
	s.state = "awake"
	f.launched = g.now()
	g.flights[f.id] = f
	g.timings.Record("resume", ms)
	g.emitLocked(Event{Type: "woke", Flight: f.id, Nick: r.nick, Color: r.color, Kind: f.kind.ID, Ms: ms})
	if say {
		go g.say(f.id, s, r.nick)
	}
	return Launched{Flight: f.id, Kind: f.kind.ID, Points: f.kind.Points, Wave: wave, WokeMs: ms}, nil
}

// sayLines are what a target says when its real turn fails validation or errors.
var sayLines = []string{"Can't catch me, I'm stateless!", "Snapshot me if you can!", "Woke up in a sandbox again.", "My worker's warm, back off!", "Ducking your aim!"}

// say asks a flying target for one line (a real LLM turn) and shows it. It holds the
// slot's op, so the flight's suspend waits for the turn, and skips a target already down.
func (g *Game) say(flightID string, s *slot, nick string) {
	s.op.Lock()
	defer s.op.Unlock()
	g.mu.Lock()
	f := g.flights[flightID]
	flying := f != nil && f.state == "flying" && s.flight == flightID && s.state == "awake"
	g.mu.Unlock()
	if !flying {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), opTimeout)
	defer cancel()
	t0 := time.Now()
	reply, err := g.k.Prompt(ctx, s.sid, "A player is aiming at you. Taunt them in one short line.")
	g.noteTurn(reply, err)
	var out struct{ Quack string }
	text := ""
	if err == nil && reply.Decode(&out) == nil {
		text = clip(strings.TrimSpace(out.Quack), 60)
	}
	if text == "" {
		text = sayLines[int(time.Now().UnixNano()%int64(len(sayLines)))]
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	g.timings.Record("say", time.Since(t0).Milliseconds())
	g.emitLocked(Event{Type: "say", Flight: flightID, Nick: nick, Text: text})
}

// Hit is a confirmed shot: the points (server-scored) and the real suspend.
type Hit struct {
	Points  int    `json:"points"`
	Mult    int    `json:"mult"`
	Combo   int    `json:"combo"`
	Score   int    `json:"score"`
	Kind    string `json:"kind"`
	Ms      int64  `json:"ms"`     // the Substrate suspend, 0 if it failed
	Asleep  bool   `json:"asleep"` // the suspend landed
	Elapsed int64  `json:"elapsed"`
}

// Shoot scores a hit on a flying target and suspends its session (a real snapshot).
func (g *Game) Shoot(ctx context.Context, player, runID, flightID string) (Hit, error) {
	g.mu.Lock()
	r, err := g.ownRun(player, runID)
	if err != nil {
		g.mu.Unlock()
		return Hit{}, err
	}
	f := g.flights[flightID]
	now := g.now()
	switch {
	case f == nil || f.run != r.id:
		g.mu.Unlock()
		return Hit{}, errors.New("no such target")
	case f.state != "flying":
		g.mu.Unlock()
		return Hit{}, errors.New("already down")
	case now.Sub(f.launched) < minFlight*time.Millisecond, now.Sub(r.lastHit) < minHitGap*time.Millisecond:
		g.mu.Unlock()
		return Hit{}, errors.New("too fast")
	case r.done || now.After(r.ends.Add(2*time.Second)):
		g.mu.Unlock()
		return Hit{}, ErrRunOver
	}
	if now.Sub(r.lastHit) <= comboWindow {
		r.combo++
	} else {
		r.combo = 1
	}
	m := Mult(r.combo)
	pts := f.kind.Points * m
	f.state = "hit"
	r.score += pts
	r.hits++
	r.lastHit = now
	r.bestCombo = max(r.bestCombo, r.combo)
	if f.kind == Golden {
		r.golden++
	}
	g.hits++
	g.recordBestLocked(r)
	h := Hit{Points: pts, Mult: m, Combo: r.combo, Score: r.score, Kind: f.kind.ID, Elapsed: now.Sub(r.started).Milliseconds()}
	g.mu.Unlock()

	ms, ok := g.sleep(ctx, flightID)
	h.Ms, h.Asleep = ms, ok
	g.mu.Lock()
	g.emitLocked(Event{Type: "hit", Flight: flightID, Nick: r.nick, Color: r.color, Kind: f.kind.ID, Points: pts, Mult: m, Ms: ms})
	g.mu.Unlock()
	return h, nil
}

// Escape is the phone reporting a target that flew off: no points, combo broken, and its
// session goes back to sleep.
func (g *Game) Escape(player, runID, flightID string) error {
	g.mu.Lock()
	r, err := g.ownRun(player, runID)
	if err != nil {
		g.mu.Unlock()
		return err
	}
	f := g.flights[flightID]
	if f == nil || f.run != r.id || f.state != "flying" {
		g.mu.Unlock()
		return nil
	}
	g.escapeLocked(r, f)
	g.mu.Unlock()
	go g.sleep(context.Background(), flightID)
	return nil
}

func (g *Game) escapeLocked(r *run, f *flight) {
	f.state = "escaped"
	r.escapes++
	r.combo = 0
	g.emitLocked(Event{Type: "escape", Flight: f.id, Nick: r.nick, Kind: f.kind.ID})
}

// sleep suspends a flight's session and frees its slot; it reports the suspend's ms.
func (g *Game) sleep(ctx context.Context, flightID string) (int64, bool) {
	g.mu.Lock()
	f := g.flights[flightID]
	var s *slot
	if f != nil {
		for _, x := range g.slots {
			if x.flight == flightID && x.state == "awake" {
				s = x
			}
		}
	}
	if s == nil {
		g.mu.Unlock()
		return 0, false
	}
	s.state = "sleeping"
	g.mu.Unlock()

	s.op.Lock() // a say turn in flight finishes first
	defer s.op.Unlock()
	c, cancel := context.WithTimeout(context.WithoutCancel(ctx), opTimeout)
	t0 := time.Now()
	err := settle(c, func() error { return g.k.Suspend(c, s.sid) })
	ms := time.Since(t0).Milliseconds()
	cancel()

	g.mu.Lock()
	defer g.mu.Unlock()
	if r := g.runs[f.run]; r != nil && r.air > 0 {
		r.air--
	}
	delete(g.flights, flightID)
	if err != nil {
		slog.Warn("duck hunt: suspend", "session", s.sid, "err", err)
		s.state, s.flight = "broken", ""
		return 0, false
	}
	s.state, s.flight = "asleep", ""
	g.timings.Record("suspend", ms)
	g.hub.Poke()
	return ms, true
}

// Result is a finished run: score, best, rank and who to beat next.
type Result struct {
	Score     int                 `json:"score"`
	Hits      int                 `json:"hits"`
	Escapes   int                 `json:"escapes"`
	BestCombo int                 `json:"bestCombo"`
	Golden    int                 `json:"golden"`
	Best      int                 `json:"best"`
	Rank      int                 `json:"rank"`
	Beat      *Beat               `json:"beat,omitempty"`
	Top       []platform.BoardRow `json:"top"`
}

// Beat is the player just above: beat Nick by By to reach #Rank.
type Beat struct {
	Nick string `json:"nick"`
	By   int    `json:"by"`
	Rank int    `json:"rank"`
}

// Finish ends the player's run (if it still runs) and returns their standing.
func (g *Game) Finish(player, runID string) (Result, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	r, err := g.ownRun(player, runID)
	if err != nil {
		return Result{}, err
	}
	if !r.done {
		g.endLocked(r)
	}
	res := Result{Score: r.score, Hits: r.hits, Escapes: r.escapes, BestCombo: r.bestCombo, Golden: r.golden, Top: g.topLocked(5)}
	ids := g.rankedLocked()
	if b := g.bests[player]; b != nil {
		res.Best = b.score
	}
	for i, id := range ids {
		if id == player {
			res.Rank = i + 1
			if i > 0 {
				above := g.bests[ids[i-1]]
				res.Beat = &Beat{Nick: above.nick, By: above.score - g.bests[player].score + 1, Rank: i}
			}
		}
	}
	return res, nil
}

// endLocked ends a run: its targets go back to sleep and its result goes on the board.
func (g *Game) endLocked(r *run) {
	r.done = true
	for _, f := range g.flights {
		if f.run == r.id && f.state == "flying" {
			f.state = "escaped"
			go g.sleep(context.Background(), f.id)
		}
	}
	g.recordBestLocked(r)
	if r.score > 0 {
		if b := g.bests[r.player]; b != nil {
			b.runs++
		}
		g.emitLocked(Event{Type: "end", Nick: r.nick, Color: r.color, Points: r.score})
	}
}

func (g *Game) recordBestLocked(r *run) {
	if r.score <= 0 {
		return
	}
	b := g.bests[r.player]
	if b == nil {
		b = &best{}
		g.bests[r.player] = b
	}
	b.nick, b.color = r.nick, r.color
	if r.score > b.score {
		b.score, b.hits, b.bestCombo, b.golden = r.score, r.hits, r.bestCombo, r.golden
	}
}

// housekeep ends runs whose time is up, and puts targets to sleep that flew longer than
// idle or that the phone stopped reporting on (it closed, lost the network).
func (g *Game) housekeep() {
	g.mu.Lock()
	now := g.now()
	var stale []string
	for _, r := range g.runs {
		if !r.done && now.After(r.ends.Add(3*time.Second)) {
			g.endLocked(r)
		}
		if r.done && now.Sub(r.ends) > 30*time.Minute {
			delete(g.runs, r.id)
		}
	}
	for _, f := range g.flights {
		if f.state == "flying" && now.Sub(f.launched) > g.idle {
			if r := g.runs[f.run]; r != nil {
				g.escapeLocked(r, f)
			} else {
				f.state = "escaped"
			}
			stale = append(stale, f.id)
		}
	}
	g.mu.Unlock()
	for _, id := range stale {
		go g.sleep(context.Background(), id)
	}
}

// --- leaderboard -----------------------------------------------------------

// rankedLocked is player IDs by best score (ties: more hits, then nick).
func (g *Game) rankedLocked() []string {
	ids := make([]string, 0, len(g.bests))
	for id, b := range g.bests {
		if b.score > 0 {
			ids = append(ids, id)
		}
	}
	sort.Slice(ids, func(i, j int) bool {
		a, b := g.bests[ids[i]], g.bests[ids[j]]
		if a.score != b.score {
			return a.score > b.score
		}
		if a.hits != b.hits {
			return a.hits > b.hits
		}
		return a.nick < b.nick
	})
	return ids
}

// Top is the leaderboard: the n best runs, one per player.
func (g *Game) Top(n int) []platform.BoardRow {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.topLocked(n)
}

func (g *Game) topLocked(n int) []platform.BoardRow {
	out := []platform.BoardRow{}
	for _, id := range g.rankedLocked() {
		if len(out) == n {
			break
		}
		b := g.bests[id]
		d := fmt.Sprintf("%d agents snapshotted · best combo ×%d", b.hits, Mult(b.bestCombo))
		if b.golden > 0 {
			d += fmt.Sprintf(" · %d golden", b.golden)
		}
		out = append(out, platform.BoardRow{Nick: b.nick, Color: b.color, Score: b.score, Detail: d})
	}
	return out
}

// Reset clears the board and ends every run (admin). Pool sessions stay.
func (g *Game) Reset() {
	g.mu.Lock()
	defer g.mu.Unlock()
	for _, r := range g.runs {
		if !r.done {
			g.endLocked(r)
		}
	}
	g.runs, g.bests, g.events, g.hits = map[string]*run{}, map[string]*best{}, nil, 0
	g.timings.Reset()
	g.hub.Poke()
}

// --- state -----------------------------------------------------------------

func (g *Game) emitLocked(e Event) {
	e.At = g.now().UnixMilli()
	g.events = append(g.events, e)
	if n := len(g.events); n > 60 {
		g.events = g.events[n-60:]
	}
	g.hub.Poke()
}

// State is what the big screen and phones get over SSE.
type State struct {
	Board   []platform.BoardRow             `json:"board"` // top 10
	Events  []Event                         `json:"events"`
	Pool    int                             `json:"pool"`
	Awake   int                             `json:"awake"`
	Asleep  int                             `json:"asleep"`
	Runs    int                             `json:"runs"` // playing now
	Hits    int                             `json:"hits"`
	Turns   int64                           `json:"turns"`
	Tokens  int64                           `json:"tokens"`
	Timings map[string]platform.Percentiles `json:"timings"`
	Now     int64                           `json:"now"`
}

func (g *Game) Snapshot() State {
	g.mu.Lock()
	defer g.mu.Unlock()
	s := State{Board: g.topLocked(10), Events: slices.Clone(g.events), Pool: len(g.slots), Hits: g.hits, Turns: g.turns,
		Tokens: g.tokens.Input + g.tokens.Output, Timings: g.timings.Snapshot(nil), Now: g.now().UnixMilli()}
	for _, sl := range g.slots {
		switch sl.state {
		case "asleep":
			s.Asleep++
		case "waking", "awake", "sleeping":
			s.Awake++
		}
	}
	for _, r := range g.runs {
		if !r.done {
			s.Runs++
		}
	}
	return s
}

// Hub serves the live state.
func (g *Game) Hub() *platform.Hub { return g.hub }

// --- helpers ---------------------------------------------------------------

// settle runs call, retrying with backoff (100 ms up to 500 ms) while kagent is still
// settling the session's last turn, until ctx ends.
func settle(ctx context.Context, call func() error) error {
	wait := 100 * time.Millisecond
	for {
		err := call()
		if err == nil || !(strings.Contains(err.Error(), "not yet settled") || strings.Contains(err.Error(), "active task or checkpoint")) {
			return err
		}
		select {
		case <-ctx.Done():
			return err
		case <-time.After(wait):
		}
		wait = min(2*wait, 500*time.Millisecond)
	}
}

func clip(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n-1]) + "…"
	}
	return s
}
