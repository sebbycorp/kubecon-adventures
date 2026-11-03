// Package podpacker is Pod Packer (at /pod-packer): Tetris on the phone where the well is
// the cluster (each row a worker node) and the pieces are agent pods. The point is Agent
// Substrate's: run more agents than you have room for. The game keeps a small pool of
// real kagent sessions of the "packer" agent awake while anyone plays; every line a
// player clears snapshots one of them for real (a Substrate Suspend: "node-07 freed · 2
// agents snapshotted in 140 ms"), and the pool wakes them again in the background (a real
// Resume) so the next clear has pods to snapshot. The falling blocks run on the phone; the
// server checks every reported score against the rules before it reaches the leaderboard.
// Play never waits on Substrate: a clear with no pod awake, or a slow call, says so.
package podpacker

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

// AgentName is the kagent Agent whose sessions are the pods.
const AgentName = "packer"

// Ops is what the game needs from kagent (a *kagent.Kagent for the packer agent).
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
	poolSize     = 6                // sessions in the pool
	maxAwake     = 4                // pods awake at once (the podpacker WorkerPool has 4 workers)
	maxRuns      = 48               // runs at once
	maxRunAge    = 30 * time.Minute // a run nobody finished is dropped
	idleAfter    = 60 * time.Second // with no run active this long, the awake pods sleep
	activeWindow = 2 * time.Minute  // a run counts as active while it reported this recently
	clearTimeout = 4 * time.Second  // a clear waits at most this long for its snapshots
	clearGap     = 150 * time.Millisecond
	opTimeout    = 30 * time.Second
	createConcur = 2
)

var (
	ErrRunOver = errors.New("run over")
	ErrNoRun   = errors.New("no such run")
	ErrFull    = errors.New("the cluster is full of players, try again in a minute")
	ErrTooFast = errors.New("too many clears")
)

type slot struct {
	sid   string
	state string // asleep, waking, awake, sleeping, broken
	naps  int
}

type run struct {
	id, player, nick, color  string
	started, last, lastClear time.Time
	done                     bool
	lines                    int     // lines the server saw cleared
	snaps                    int     // real snapshots its clears took
	snapMs                   []int64 // their timings
	score, level, tetrises   int
	clearedLines             int // the reported total, at the end
}

// best is one player's leaderboard row.
type best struct {
	nick, color                   string
	score, lines, level, tetrises int
	runs                          int
}

// Event is a moment the big screen shows: a clear, a game over, a new #1.
type Event struct {
	Type  string `json:"type"` // clear, over, best
	Nick  string `json:"nick,omitempty"`
	Color string `json:"color,omitempty"`
	Lines int    `json:"lines,omitempty"`
	Node  int    `json:"node,omitempty"`
	Snaps int    `json:"snaps,omitempty"`
	Score int    `json:"score,omitempty"`
	Ms    int64  `json:"ms,omitempty"`
	At    int64  `json:"at"`
}

type Game struct {
	mu        sync.Mutex
	p         *platform.Platform
	k         Ops
	rules     Rules
	slots     []*slot
	runs      map[string]*run
	bests     map[string]*best // by player ID
	events    []Event
	snaps     int // real snapshots taken by clears
	clears    int
	idleSince time.Time
	timings   *platform.Timings // suspend, resume
	hub       *platform.Hub
	now       func() time.Time
	stopped   chan struct{}
	waking    int // resumes in flight
}

// New builds the game on the packer agent's kagent client.
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

// sweep deletes every packer session left from an earlier run of the arcade.
func (g *Game) sweep(ctx context.Context) {
	c, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	ids, err := g.k.ListSessionIDs(c)
	if err != nil {
		slog.Warn("pod packer: listing packer sessions", "err", err)
		return
	}
	n := 0
	for _, id := range ids {
		if err := g.k.Delete(c, id); err == nil {
			n++
		}
	}
	slog.Info("pod packer: startup sweep", "deleted", n, "found", len(ids))
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
				slog.Warn("pod packer: new pool session", "err", err)
			}
		}(i)
	}
	wg.Wait()
	slog.Info("pod packer: pool ready", "sessions", len(g.slots))
}

func (g *Game) newSession(ctx context.Context, i int) (string, error) {
	c, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()
	id, err := g.k.CreateSession(c, fmt.Sprintf("pod-%d", i))
	if err != nil {
		return "", err
	}
	// The reply is never logged or shown: the turn is only there to place the actor.
	_, _ = g.k.Prompt(c, id, "You are an agent pod that just got scheduled. Say hi in three words.")
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
	slog.Info("pod packer: shutdown deleted the pool", "sessions", len(ids))
}

// --- runs ------------------------------------------------------------------

// Started is a new run: its ID, the rules, and how many pods are awake right now.
type Started struct {
	Run   string `json:"run"`
	Rules Rules  `json:"rules"`
	Awake int    `json:"awake"`
	Pool  int    `json:"pool"`
}

// Start begins a run for the player, ending any run they still had. The pool wakes pods
// in the background while anyone plays; Start itself never waits on Substrate.
func (g *Game) Start(pl platform.Player) (Started, error) {
	g.mu.Lock()
	live := 0
	for _, r := range g.runs {
		if r.player == pl.ID && !r.done {
			r.done = true
		}
		if !r.done {
			live++
		}
	}
	if live >= maxRuns {
		g.mu.Unlock()
		return Started{}, ErrFull
	}
	now := g.now()
	r := &run{id: platform.NewID(), player: pl.ID, nick: pl.Nick, color: pl.Color, started: now, last: now}
	g.runs[r.id] = r
	awake := g.countLocked("awake")
	out := Started{Run: r.id, Rules: g.rules, Awake: awake, Pool: len(g.slots)}
	g.mu.Unlock()
	g.replenish()
	return out, nil
}

// Cleared is the server's answer to a line clear: how many real snapshots it took, and
// their median time.
type Cleared struct {
	Lines   int    `json:"lines"`
	Node    int    `json:"node"`
	Snapped int    `json:"snapped"`      // real Suspends that landed
	Ms      int64  `json:"ms,omitempty"` // their median ms
	Awake   int    `json:"awake"`        // pods awake after the clear
	Note    string `json:"note,omitempty"`
}

// Clear records lines (1-4) cleared on node (the lowest cleared row's node number) and
// snapshots one awake pod per line, for real, waiting at most clearTimeout. With no pod
// awake it says so; the pool wakes more in the background either way.
func (g *Game) Clear(ctx context.Context, player, runID string, lines, node int) (Cleared, error) {
	if lines < 1 || lines > 4 {
		return Cleared{}, fmt.Errorf("lines must be 1-4")
	}
	g.mu.Lock()
	r := g.runs[runID]
	switch {
	case r == nil:
		g.mu.Unlock()
		return Cleared{}, ErrNoRun
	case r.player != player:
		g.mu.Unlock()
		return Cleared{}, platform.ErrNotYours
	case r.done:
		g.mu.Unlock()
		return Cleared{}, ErrRunOver
	}
	now := g.now()
	if !r.lastClear.IsZero() && now.Sub(r.lastClear) < clearGap {
		g.mu.Unlock()
		return Cleared{}, ErrTooFast
	}
	r.lastClear, r.last = now, now
	r.lines += lines
	g.clears++
	var batch []*slot
	for _, s := range g.slots {
		if len(batch) == lines {
			break
		}
		if s.state == "awake" {
			s.state = "sleeping"
			batch = append(batch, s)
		}
	}
	g.mu.Unlock()

	out := Cleared{Lines: lines, Node: node}
	if len(batch) == 0 {
		out.Note = "no pod awake to snapshot yet"
	}
	c, cancel := context.WithTimeout(context.WithoutCancel(ctx), clearTimeout)
	defer cancel()
	ms := make([]int64, len(batch))
	ok := make([]bool, len(batch))
	var wg sync.WaitGroup
	for i, s := range batch {
		wg.Add(1)
		go func(i int, s *slot) {
			defer wg.Done()
			ms[i], ok[i] = g.suspendSlot(c, s)
		}(i, s)
	}
	wg.Wait()

	var landed []int64
	for i := range batch {
		if ok[i] {
			landed = append(landed, ms[i])
		}
	}
	out.Snapped = len(landed)
	out.Ms = median(landed)
	if len(batch) > 0 && len(landed) < len(batch) {
		out.Note = "a snapshot was slow; it lands in the background"
	}
	g.mu.Lock()
	r.snaps += len(landed)
	r.snapMs = append(r.snapMs, landed...)
	g.snaps += len(landed)
	out.Awake = g.countLocked("awake")
	g.emitLocked(Event{Type: "clear", Nick: r.nick, Color: r.color, Lines: lines, Node: node, Snaps: len(landed), Ms: out.Ms})
	g.mu.Unlock()
	g.replenish()
	return out, nil
}

// suspendSlot snapshots a pod (a real Suspend). It reports the ms and whether it landed;
// a pod whose suspend failed is retried by housekeeping.
func (g *Game) suspendSlot(ctx context.Context, s *slot) (int64, bool) {
	t0 := time.Now()
	err := settle(ctx, func() error { return g.k.Suspend(ctx, s.sid) })
	ms := time.Since(t0).Milliseconds()
	g.mu.Lock()
	defer g.mu.Unlock()
	if err != nil {
		slog.Warn("pod packer: suspend", "session", s.sid, "err", err)
		s.state = "broken"
		return 0, false
	}
	s.state = "asleep"
	s.naps++
	g.timings.Record("suspend", ms)
	return ms, true
}

// Result is a finished run: its score, its real snapshots, and the player's standing.
type Result struct {
	Score    int                 `json:"score"`
	Lines    int                 `json:"lines"`
	Level    int                 `json:"level"`
	Tetrises int                 `json:"tetrises"`
	Counted  bool                `json:"counted"` // false: the score didn't pass the server's check
	Snaps    int                 `json:"snaps"`   // real snapshots this run's clears took
	SnapMs   int64               `json:"snapMs,omitempty"`
	Best     int                 `json:"best"`
	NewBest  bool                `json:"newBest"`
	Rank     int                 `json:"rank"`
	Beat     *Beat               `json:"beat,omitempty"`
	Top      []platform.BoardRow `json:"top"`
}

// Beat is the player just above: beat Nick by By to reach #Rank.
type Beat struct {
	Nick string `json:"nick"`
	By   int    `json:"by"`
	Rank int    `json:"rank"`
}

// Finish ends the player's run ("cluster full") with what the phone counted. A plausible
// score goes on the board.
func (g *Game) Finish(player, runID string, score, lines, tetrises, pieces int) (Result, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	r := g.runs[runID]
	switch {
	case r == nil:
		return Result{}, ErrNoRun
	case r.player != player:
		return Result{}, platform.ErrNotYours
	case r.done:
		return Result{}, ErrRunOver
	}
	r.done = true
	elapsed := g.now().Sub(r.started).Seconds()
	res := Result{Score: score, Lines: lines, Level: g.rules.Level(lines), Tetrises: tetrises, Snaps: r.snaps, SnapMs: median(r.snapMs)}
	prev := 0
	if b := g.bests[player]; b != nil {
		prev = b.score
	}
	if g.rules.Plausible(score, lines, tetrises, pieces, r.lines, elapsed) {
		res.Counted = true
		r.score, r.clearedLines, r.level, r.tetrises = score, lines, res.Level, tetrises
		g.recordBestLocked(r)
	} else {
		slog.Warn("pod packer: implausible score", "player", player, "score", score, "lines", lines, "pieces", pieces, "serverLines", r.lines, "seconds", elapsed)
		res.Score = 0
	}
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
	g.emitLocked(Event{Type: "over", Nick: r.nick, Color: r.color, Score: res.Score, Lines: lines, Snaps: r.snaps})
	if res.NewBest && res.Rank == 1 {
		g.emitLocked(Event{Type: "best", Nick: r.nick, Color: r.color, Score: res.Score})
	}
	return res, nil
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
		b.score, b.lines, b.level, b.tetrises = r.score, r.clearedLines, r.level, r.tetrises
	}
}

// --- the pool --------------------------------------------------------------

// activeLocked is how many runs reported something in the last activeWindow.
func (g *Game) activeLocked(now time.Time) int {
	n := 0
	for _, r := range g.runs {
		if !r.done && now.Sub(r.last) < activeWindow {
			n++
		}
	}
	return n
}

func (g *Game) countLocked(state string) int {
	n := 0
	for _, s := range g.slots {
		if s.state == state {
			n++
		}
	}
	return n
}

// replenish wakes asleep pods (a real Resume each) until maxAwake are awake or waking,
// while anyone plays.
func (g *Game) replenish() {
	g.mu.Lock()
	if g.activeLocked(g.now()) == 0 {
		g.mu.Unlock()
		return
	}
	busy := g.countLocked("awake") + g.countLocked("waking") + g.countLocked("sleeping")
	var wake []*slot
	for _, s := range g.slots {
		if busy >= maxAwake {
			break
		}
		if s.state == "asleep" {
			s.state = "waking"
			wake = append(wake, s)
			busy++
		}
	}
	g.mu.Unlock()
	for _, s := range wake {
		go g.wakeSlot(s)
	}
}

func (g *Game) wakeSlot(s *slot) {
	c, cancel := context.WithTimeout(context.Background(), opTimeout)
	defer cancel()
	t0 := time.Now()
	err := settle(c, func() error { return g.k.Resume(c, s.sid) })
	ms := time.Since(t0).Milliseconds()
	g.mu.Lock()
	defer g.mu.Unlock()
	if err != nil {
		slog.Warn("pod packer: resume", "session", s.sid, "err", err)
		s.state = "broken" // housekeeping snapshots it again, then it can wake
		return
	}
	s.state = "awake"
	g.timings.Record("resume", ms)
	g.hub.Poke()
}

// housekeep drops old runs, puts the pool to sleep when nobody has played for idleAfter,
// re-snapshots broken pods, and tops up the awake pods while anyone plays.
func (g *Game) housekeep() {
	g.mu.Lock()
	now := g.now()
	for id, r := range g.runs {
		if !r.done && now.Sub(r.started) > maxRunAge {
			r.done = true
		}
		if r.done && now.Sub(r.started) > maxRunAge+20*time.Minute {
			delete(g.runs, id)
		}
	}
	active := g.activeLocked(now)
	if active > 0 || g.idleSince.IsZero() {
		g.idleSince = now
	}
	var sleep []*slot
	for _, s := range g.slots {
		if s.state == "broken" || (s.state == "awake" && active == 0 && now.Sub(g.idleSince) >= idleAfter) {
			s.state = "sleeping"
			sleep = append(sleep, s)
		}
	}
	g.mu.Unlock()
	for _, s := range sleep {
		go func(s *slot) {
			c, cancel := context.WithTimeout(context.Background(), opTimeout)
			defer cancel()
			g.suspendSlot(c, s)
			g.hub.Poke()
		}(s)
	}
	g.replenish()
}

// --- leaderboard -----------------------------------------------------------

// rankedLocked is player IDs by best score (ties: more lines, then nick).
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
		if a.lines != b.lines {
			return a.lines > b.lines
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
		d := plural(b.lines, "line") + fmt.Sprintf(" · level %d", b.level)
		if b.tetrises > 0 {
			d += " · " + plural(b.tetrises, "tetris")
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
		r.done = true
	}
	g.runs, g.bests, g.events, g.snaps, g.clears = map[string]*run{}, map[string]*best{}, nil, 0, 0
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

// State is what the big screen gets over SSE.
type State struct {
	Board   []platform.BoardRow             `json:"board"` // top 10
	Events  []Event                         `json:"events"`
	Pool    int                             `json:"pool"`
	Awake   int                             `json:"awake"`
	Asleep  int                             `json:"asleep"`
	Workers int                             `json:"workers"`
	Runs    int                             `json:"runs"` // playing now
	Snaps   int                             `json:"snaps"`
	Clears  int                             `json:"clears"`
	Timings map[string]platform.Percentiles `json:"timings"`
	Now     int64                           `json:"now"`
}

func (g *Game) Snapshot() State {
	g.mu.Lock()
	defer g.mu.Unlock()
	now := g.now()
	s := State{Board: g.topLocked(10), Events: slices.Clone(g.events), Pool: len(g.slots), Workers: maxAwake,
		Snaps: g.snaps, Clears: g.clears, Timings: g.timings.Snapshot(nil), Now: now.UnixMilli(), Runs: g.activeLocked(now)}
	for _, sl := range g.slots {
		switch sl.state {
		case "asleep":
			s.Asleep++
		case "awake":
			s.Awake++
		}
	}
	return s
}

// --- helpers ---------------------------------------------------------------

func median(ms []int64) int64 {
	if len(ms) == 0 {
		return 0
	}
	s := slices.Clone(ms)
	slices.Sort(s)
	return s[len(s)/2]
}

func plural(n int, word string) string {
	if n == 1 {
		return "1 " + word
	}
	if strings.HasSuffix(word, "s") {
		return fmt.Sprintf("%d %ses", n, word)
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
