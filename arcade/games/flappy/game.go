// Package flappy is Flappy Substrate (at /flappy): a one-tap underwater swim on the phone
// where the player IS an agent. Each run borrows a real kagent session of the "octo" agent
// from a small pool: starting (or PLAY AGAIN) is a real Substrate Resume, so the octopus
// wakes on a worker; crashing is a real Suspend, so it sinks into Snapshot Storage. The
// swim itself runs on the phone; the server checks every reported score against the
// course's physics before it reaches the leaderboard. Play never waits on Substrate: when
// no sandbox is free, or a call fails, the run goes on and says so.
package flappy

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/sebbycorp/kubecon-adventures/arcade/internal/kagent"
	"github.com/sebbycorp/kubecon-adventures/arcade/internal/platform"
)

// AgentName is the kagent Agent whose sessions the octopuses are.
const AgentName = "octo"

// Ops is what the game needs from kagent (a *kagent.Kagent for the octo agent).
type Ops interface {
	CreateSession(ctx context.Context, name string) (string, error)
	Prompt(ctx context.Context, sessionID, text string) (kagent.Reply, error)
	Suspend(ctx context.Context, sessionID string) error
	Resume(ctx context.Context, sessionID string) error
	Delete(ctx context.Context, sessionID string) error
	ListSessionIDs(ctx context.Context) ([]string, error)
}

// Tuning.
const (
	poolSize     = 10 // sessions in the pool: asleep between runs, so more than the workers
	maxAwake     = 6  // octopuses awake at once (the flappy WorkerPool has 6 workers)
	maxRuns      = 48 // runs at once (beyond maxAwake they swim without an agent)
	maxRunAge    = 10 * time.Minute
	wakeTimeout  = 8 * time.Second // a Start waits at most this long for the Resume
	opTimeout    = 30 * time.Second
	createConcur = 2
)

var (
	ErrRunOver = errors.New("run over")
	ErrNoRun   = errors.New("no such run")
	ErrFull    = errors.New("the ocean is full, try again in a minute")
)

type slot struct {
	sid    string
	state  string // asleep, waking, awake, sleeping, broken
	owner  string // the player who last swam it (they get it back if it's free)
	runID  string
	sleeps int
}

type run struct {
	id, player, nick, color string
	started                 time.Time
	slot                    *slot // nil: swimming without an agent
	wokeMs                  int64
	done                    bool
	gaps, bubbles, score    int
}

// best is one player's leaderboard row: their best run.
type best struct {
	nick, color          string
	score, gaps, bubbles int
	runs                 int
}

// Event is a moment the big screen shows: a wake, a crash, a new best.
type Event struct {
	Type  string `json:"type"` // woke, crash, best, nowake
	Nick  string `json:"nick,omitempty"`
	Color string `json:"color,omitempty"`
	Score int    `json:"score,omitempty"`
	Gaps  int    `json:"gaps,omitempty"`
	Ms    int64  `json:"ms,omitempty"`
	Text  string `json:"text,omitempty"`
	At    int64  `json:"at"`
}

type Game struct {
	mu      sync.Mutex
	p       *platform.Platform
	k       Ops
	rules   Rules
	slots   []*slot
	runs    map[string]*run
	bests   map[string]*best // by player ID
	events  []Event
	crashes int
	timings *platform.Timings // resume, suspend
	hub     *platform.Hub
	now     func() time.Time
	stopped chan struct{}
}

// New builds the game on the octo agent's kagent client.
func New(p *platform.Platform, k Ops) *Game {
	g := &Game{p: p, k: k, rules: Default, runs: map[string]*run{}, bests: map[string]*best{},
		timings: platform.NewTimings(), hub: platform.NewHub(), now: time.Now, stopped: make(chan struct{})}
	if p != nil {
		p.OnChange(g.hub.Poke)
	}
	return g
}

// Run sweeps old sessions, fills the pool, and starts housekeeping and the live hub. When
// ctx ends it deletes the pool's sessions and closes Stopped.
func (g *Game) Run(ctx context.Context) {
	go g.hub.Run(ctx, func() any { return g.Snapshot() })
	go func() {
		g.sweep(ctx)
		g.fillPool(ctx)
	}()
	go func() {
		t := time.NewTicker(time.Second)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				g.shutdown()
				return
			case <-t.C:
				g.housekeep()
			}
		}
	}()
}

// Stopped is closed once the game has deleted its sessions on the way out.
func (g *Game) Stopped() <-chan struct{} { return g.stopped }

// sweep deletes every octo session left from an earlier run of the arcade (the pool is
// rebuilt fresh, so nothing is left awake holding a worker).
func (g *Game) sweep(ctx context.Context) {
	c, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	ids, err := g.k.ListSessionIDs(c)
	if err != nil {
		slog.Warn("flappy: listing octo sessions", "err", err)
		return
	}
	n := 0
	for _, id := range ids {
		if err := g.k.Delete(c, id); err == nil {
			n++
		}
	}
	slog.Info("flappy: startup sweep", "deleted", n, "found", len(ids))
}

// fillPool creates the pool's sessions. A session joins asleep, after one short turn has
// placed its actor on a worker and a suspend has snapshotted it.
func (g *Game) fillPool(ctx context.Context) {
	sem := make(chan struct{}, createConcur)
	var wg sync.WaitGroup
	for i := range poolSize {
		select {
		case <-ctx.Done():
			return
		case sem <- struct{}{}:
		}
		wg.Add(1)
		go func(i int) {
			defer func() { <-sem; wg.Done() }()
			if id, err := g.newSession(ctx, i); err == nil {
				g.mu.Lock()
				g.slots = append(g.slots, &slot{sid: id, state: "asleep"})
				g.mu.Unlock()
				g.hub.Poke()
			} else {
				slog.Warn("flappy: new pool session", "err", err)
			}
		}(i)
	}
	wg.Wait()
	slog.Info("flappy: pool ready", "sessions", len(g.slots))
}

func (g *Game) newSession(ctx context.Context, i int) (string, error) {
	c, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()
	id, err := g.k.CreateSession(c, fmt.Sprintf("flappy-%d", i))
	if err != nil {
		return "", err
	}
	// The reply is never logged or shown: the turn is only there to place the actor.
	_, _ = g.k.Prompt(c, id, "You just hatched in the deep sea. Say hi in three words.")
	if err := settle(c, func() error { return g.k.Suspend(c, id) }); err != nil {
		_ = g.k.Delete(c, id)
		return "", err
	}
	return id, nil
}

func (g *Game) shutdown() {
	defer close(g.stopped)
	g.mu.Lock()
	ids := make([]string, 0, len(g.slots))
	for _, s := range g.slots {
		ids = append(ids, s.sid)
	}
	g.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	for _, id := range ids {
		_ = g.k.Delete(ctx, id)
	}
	slog.Info("flappy: shutdown deleted the pool", "sessions", len(ids))
}

// --- runs ------------------------------------------------------------------

// Started is a new run: its ID, and whether its octopus agent woke (and how fast).
type Started struct {
	Run    string `json:"run"`
	Agent  string `json:"agent"`            // awake, or none (no free sandbox, or the wake failed)
	WokeMs int64  `json:"wokeMs,omitempty"` // the real Resume, when Agent is awake
	Reason string `json:"reason,omitempty"` // why there is no agent
	Rules  Rules  `json:"rules"`
}

// Start begins a run for the player, ending any run they still had, and wakes an octopus
// for it with a real Resume (waiting at most wakeTimeout). Play never waits longer: with
// no free sandbox, or a failed wake, the run starts anyway without an agent.
func (g *Game) Start(ctx context.Context, pl platform.Player) (Started, error) {
	g.mu.Lock()
	live := 0
	for _, r := range g.runs {
		if r.player == pl.ID && !r.done {
			g.abandonLocked(r)
		}
		if !r.done {
			live++
		}
	}
	if live >= maxRuns {
		g.mu.Unlock()
		return Started{}, ErrFull
	}
	r := &run{id: platform.NewID(), player: pl.ID, nick: pl.Nick, color: pl.Color, started: g.now()}
	g.runs[r.id] = r
	s, why := g.pickLocked(pl.ID)
	if s != nil {
		s.state, s.owner, s.runID = "waking", pl.ID, r.id
		r.slot = s
	}
	g.mu.Unlock()

	out := Started{Run: r.id, Agent: "none", Reason: why, Rules: g.rules}
	if s == nil {
		g.emit(Event{Type: "nowake", Nick: r.nick, Color: r.color, Text: why})
		return out, nil
	}
	c, cancel := context.WithTimeout(ctx, wakeTimeout)
	t0 := time.Now()
	err := settle(c, func() error { return g.k.Resume(c, s.sid) })
	ms := time.Since(t0).Milliseconds()
	cancel()

	g.mu.Lock()
	defer g.mu.Unlock()
	if err != nil {
		slog.Warn("flappy: resume", "session", s.sid, "err", err)
		// It may still wake late: put it back to sleep in the background.
		s.runID = ""
		r.slot = nil
		go g.sleepSlot(s)
		out.Reason = "the wake timed out"
		g.emitLocked(Event{Type: "nowake", Nick: r.nick, Color: r.color, Text: out.Reason})
		return out, nil
	}
	if r.done { // a newer Start ended this run while it woke
		go g.sleepSlot(s)
		return out, nil
	}
	s.state = "awake"
	r.wokeMs = ms
	g.timings.Record("resume", ms)
	g.emitLocked(Event{Type: "woke", Nick: r.nick, Color: r.color, Ms: ms})
	out.Agent, out.WokeMs, out.Reason = "awake", ms, ""
	return out, nil
}

// pickLocked chooses an asleep slot for player, preferring the one they swam last, unless
// maxAwake are already awake. It says why when there is none.
func (g *Game) pickLocked(player string) (*slot, string) {
	awake := 0
	var mine, any *slot
	for _, s := range g.slots {
		switch s.state {
		case "waking", "awake", "sleeping":
			awake++
		case "asleep":
			if s.owner == player && mine == nil {
				mine = s
			}
			if any == nil || s.sleeps < any.sleeps {
				any = s
			}
		}
	}
	switch {
	case len(g.slots) == 0:
		return nil, "the octopus pool is still hatching"
	case awake >= maxAwake:
		return nil, "every sandbox is busy"
	case mine != nil:
		return mine, ""
	case any != nil:
		return any, ""
	}
	return nil, "every octopus is busy"
}

// Result is a finished run: its score, the real snapshot, and the player's standing.
type Result struct {
	Score   int                 `json:"score"`
	Gaps    int                 `json:"gaps"`
	Bubbles int                 `json:"bubbles"`
	Counted bool                `json:"counted"`          // false: the score didn't pass the server's check
	SnapMs  int64               `json:"snapMs,omitempty"` // the real Suspend
	Asleep  bool                `json:"asleep"`           // the suspend landed
	Agent   bool                `json:"agent"`            // this run had an awake octopus
	Best    int                 `json:"best"`
	NewBest bool                `json:"newBest"`
	Rank    int                 `json:"rank"`
	Beat    *Beat               `json:"beat,omitempty"`
	Top     []platform.BoardRow `json:"top"`
}

// Beat is the player just above: beat Nick by By to reach #Rank.
type Beat struct {
	Nick string `json:"nick"`
	By   int    `json:"by"`
	Rank int    `json:"rank"`
}

// Crash ends the player's run with what the phone counted. A plausible score goes on the
// board; the run's octopus is snapshotted (a real Suspend) either way.
func (g *Game) Crash(ctx context.Context, player, runID string, gaps, bubbles int) (Result, error) {
	g.mu.Lock()
	r := g.runs[runID]
	switch {
	case r == nil:
		g.mu.Unlock()
		return Result{}, ErrNoRun
	case r.player != player:
		g.mu.Unlock()
		return Result{}, platform.ErrNotYours
	case r.done:
		g.mu.Unlock()
		return Result{}, ErrRunOver
	}
	elapsed := g.now().Sub(r.started).Seconds()
	res := Result{Gaps: gaps, Bubbles: bubbles, Agent: r.slot != nil}
	r.done = true
	prev := 0
	if b := g.bests[player]; b != nil {
		prev = b.score
	}
	if g.rules.Plausible(gaps, bubbles, elapsed) {
		res.Counted = true
		r.gaps, r.bubbles, r.score = gaps, bubbles, Score(gaps, bubbles)
		res.Score = r.score
		g.recordBestLocked(r)
	} else {
		slog.Warn("flappy: implausible score", "player", player, "gaps", gaps, "bubbles", bubbles, "seconds", elapsed)
	}
	g.crashes++
	s := r.slot
	r.slot = nil
	g.mu.Unlock()

	if s != nil {
		res.SnapMs, res.Asleep = g.sleepSlotCtx(ctx, s)
	}

	g.mu.Lock()
	defer g.mu.Unlock()
	res.Top = g.topLocked(5)
	if b := g.bests[player]; b != nil {
		res.Best = b.score
		res.NewBest = res.Counted && b.score > prev && b.score == res.Score
	}
	ids := g.rankedLocked()
	for i, id := range ids {
		if id == player {
			res.Rank = i + 1
			if i > 0 {
				above := g.bests[ids[i-1]]
				res.Beat = &Beat{Nick: above.nick, By: above.score - g.bests[player].score + 1, Rank: i}
			}
		}
	}
	g.emitLocked(Event{Type: "crash", Nick: r.nick, Color: r.color, Score: res.Score, Gaps: gaps, Ms: res.SnapMs})
	if res.NewBest && res.Rank == 1 {
		g.emitLocked(Event{Type: "best", Nick: r.nick, Color: r.color, Score: res.Score})
	}
	return res, nil
}

// abandonLocked ends a run without a score (a new Start, or it ran too long) and puts its
// octopus back to sleep in the background.
func (g *Game) abandonLocked(r *run) {
	r.done = true
	if s := r.slot; s != nil {
		r.slot = nil
		go g.sleepSlot(s)
	}
}

func (g *Game) sleepSlot(s *slot) { g.sleepSlotCtx(context.Background(), s) }

// sleepSlotCtx suspends a slot's session (a real snapshot) and frees it; it reports the
// suspend's ms and whether it landed.
func (g *Game) sleepSlotCtx(ctx context.Context, s *slot) (int64, bool) {
	g.mu.Lock()
	s.state = "sleeping"
	g.mu.Unlock()
	c, cancel := context.WithTimeout(context.WithoutCancel(ctx), opTimeout)
	t0 := time.Now()
	err := settle(c, func() error { return g.k.Suspend(c, s.sid) })
	ms := time.Since(t0).Milliseconds()
	cancel()

	g.mu.Lock()
	defer g.mu.Unlock()
	s.runID = ""
	if err != nil {
		slog.Warn("flappy: suspend", "session", s.sid, "err", err)
		s.state = "broken"
		g.hub.Poke()
		return 0, false
	}
	s.state = "asleep"
	s.sleeps++
	g.timings.Record("suspend", ms)
	g.hub.Poke()
	return ms, true
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
	b.runs++
	if r.score > b.score {
		b.score, b.gaps, b.bubbles = r.score, r.gaps, r.bubbles
	}
}

// housekeep abandons runs nobody reported a crash for (the phone closed), drops old runs,
// and retries putting broken slots back to sleep.
func (g *Game) housekeep() {
	g.mu.Lock()
	now := g.now()
	var broken []*slot
	for id, r := range g.runs {
		if !r.done && now.Sub(r.started) > maxRunAge {
			g.abandonLocked(r)
		}
		if r.done && now.Sub(r.started) > maxRunAge+20*time.Minute {
			delete(g.runs, id)
		}
	}
	for _, s := range g.slots {
		if s.state == "broken" {
			s.state = "sleeping"
			broken = append(broken, s)
		}
	}
	g.mu.Unlock()
	for _, s := range broken {
		go g.sleepSlot(s)
	}
}

// --- leaderboard -----------------------------------------------------------

// rankedLocked is player IDs by best score (ties: more gaps, then nick).
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
		if a.gaps != b.gaps {
			return a.gaps > b.gaps
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
		d := plural(b.gaps, "gap")
		if b.bubbles > 0 {
			d += " · " + plural(b.bubbles, "bubble")
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
			g.abandonLocked(r)
		}
	}
	g.runs, g.bests, g.events, g.crashes = map[string]*run{}, map[string]*best{}, nil, 0
	g.timings.Reset()
	g.hub.Poke()
}

// --- state -----------------------------------------------------------------

func (g *Game) emit(e Event) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.emitLocked(e)
}

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
	Workers int                             `json:"workers"` // how many may be awake at once
	Runs    int                             `json:"runs"`    // swimming now
	Crashes int                             `json:"crashes"`
	Timings map[string]platform.Percentiles `json:"timings"`
	Now     int64                           `json:"now"`
}

func (g *Game) Snapshot() State {
	g.mu.Lock()
	defer g.mu.Unlock()
	s := State{Board: g.topLocked(10), Events: slices.Clone(g.events), Pool: len(g.slots), Workers: maxAwake,
		Crashes: g.crashes, Timings: g.timings.Snapshot(nil), Now: g.now().UnixMilli()}
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

// --- helpers ---------------------------------------------------------------

func plural(n int, word string) string {
	if n == 1 {
		return "1 " + word
	}
	return fmt.Sprintf("%d %ss", n, word)
}

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
