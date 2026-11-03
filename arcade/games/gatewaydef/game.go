// This file is the server side of a run: starting one, the per-player budget of real
// calls, checking a finished run's score, the leaderboard and the big screen's feed.
package gatewaydef

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/sebbycorp/kubecon-adventures/arcade/internal/platform"
)

// Limits.
const (
	maxRuns     = 48 // runs in progress at once (a run older than runTTL no longer counts)
	runTTL      = 30 * time.Minute
	callRate    = 2.0 // real calls per second per player
	callBurst   = 3.0
	maxInFlight = 12 // real calls in flight across every player
	boardRows   = 10
	feedRows    = 24
	maxScreens  = 8 // public big screens (LAN screens without cf-connecting-ip are exempt)
)

// Run is one game in progress or finished.
type Run struct {
	ID       string
	Player   string
	Started  time.Time
	Finished bool
}

// Score is one leaderboard row: a player's best run.
type Score struct {
	PlayerID string `json:"-"`
	Nick     string `json:"nick"`
	Color    string `json:"color"`
	Score    int    `json:"score"`
	Wave     int    `json:"wave"`
	Won      bool   `json:"won"`
	At       int64  `json:"at"`
}

// FeedItem is one real block on the big screen.
type FeedItem struct {
	Nick  string `json:"nick"`
	Color string `json:"color"`
	Creep string `json:"creep"`
	Outcome
}

// bucket is a player's token bucket of real calls.
type bucket struct {
	tokens float64
	at     time.Time
}

// Game is Gateway Defense's server.
type Game struct {
	p      *platform.Platform
	http   WebProber
	agents SubstrateProber
	rules  Rules
	now    func() time.Time

	mu       sync.Mutex
	runs     map[string]*Run // by run ID
	buckets  map[string]*bucket
	inflight int
	board    map[string]Score // best per player
	feed     []FeedItem
	totals   map[string]int // real calls per tower
	misses   map[string]int // real calls the control did not stop
	sink     map[string]int // requests that reached the sink, by kind
	runsDone int
	screens  int
	timings  *platform.Timings
	hub      *platform.Hub
}

// New builds the game. web is the guard/limit prober, agents the Substrate one.
func New(p *platform.Platform, web WebProber, agents SubstrateProber) *Game {
	return &Game{p: p, http: web, agents: agents, rules: TheRules, now: time.Now,
		runs: map[string]*Run{}, buckets: map[string]*bucket{}, board: map[string]Score{},
		totals: map[string]int{}, misses: map[string]int{}, sink: map[string]int{},
		timings: platform.NewTimings(), hub: platform.NewHub()}
}

// Run starts the big screen's stream and, with a real agent prober, its startup sweep.
func (g *Game) Run(ctx context.Context) {
	go g.hub.Run(ctx, func() any { return g.Snapshot() })
	if a, ok := g.agents.(*AgentProber); ok {
		go a.Sweep(ctx)
		go func() {
			<-ctx.Done()
			c, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			a.Close(c)
		}()
	}
}

var (
	errFull    = errors.New("the arcade is full, try again in a minute")
	errNoRun   = errors.New("no such run")
	errBadKill = errors.New("unknown tower or creep")
)

// Start begins a run for pl.
func (g *Game) Start(pl platform.Player) (string, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	now := g.now()
	live := 0
	for id, r := range g.runs {
		switch {
		case now.Sub(r.Started) > runTTL:
			delete(g.runs, id)
		case r.Player == pl.ID && !r.Finished:
			r.Finished = true // a new run abandons the old one
		case !r.Finished:
			live++
		}
	}
	if live >= maxRuns {
		return "", errFull
	}
	r := &Run{ID: platform.NewID(), Player: pl.ID, Started: now}
	g.runs[r.ID] = r
	return r.ID, nil
}

// take spends one of pl's real calls, if the player and the arcade have room.
func (g *Game) take(player string) bool {
	now := g.now()
	b := g.buckets[player]
	if b == nil {
		b = &bucket{tokens: callBurst, at: now}
		g.buckets[player] = b
	}
	b.tokens = min(callBurst, b.tokens+now.Sub(b.at).Seconds()*callRate)
	b.at = now
	if b.tokens < 1 || g.inflight >= maxInFlight {
		return false
	}
	b.tokens--
	g.inflight++
	return true
}

// Block is a tower's kill: within the player's budget it makes the real call, otherwise
// it returns that tower's last real outcome.
func (g *Game) Block(ctx context.Context, pl platform.Player, runID, tower, creep string) (Outcome, error) {
	if _, ok := g.rules.tower(tower); !ok {
		return Outcome{}, errBadKill
	}
	if _, ok := g.rules.creep(creep); !ok {
		return Outcome{}, errBadKill
	}
	g.mu.Lock()
	r := g.runs[runID]
	if r == nil || r.Player != pl.ID || r.Finished {
		g.mu.Unlock()
		return Outcome{}, errNoRun
	}
	if !g.take(pl.ID) {
		last := g.lastLocked(tower)
		g.mu.Unlock()
		return last, nil
	}
	g.mu.Unlock()

	var o Outcome
	switch tower {
	case "guard":
		o = g.http.Guard(ctx)
	case "limit":
		o = g.http.Limit(ctx, pl.ID)
	case "suspend":
		o = g.agents.Suspend(ctx)
	case "egress":
		o = g.agents.Egress(ctx)
	}
	o.Tower = tower

	g.mu.Lock()
	defer g.mu.Unlock()
	g.inflight--
	if o.Real {
		g.totals[tower]++
		if !o.OK {
			g.misses[tower]++
		}
		g.timings.Record(tower, o.Ms)
		g.feed = append(g.feed, FeedItem{Nick: pl.Nick, Color: pl.Color, Creep: creep, Outcome: o})
		if n := len(g.feed); n > feedRows {
			g.feed = slices.Clone(g.feed[n-feedRows:])
		}
		g.hub.Poke()
	}
	return o, nil
}

// lastLocked is the newest real outcome for tower, marked as not this kill's.
func (g *Game) lastLocked(tower string) Outcome {
	for i := len(g.feed) - 1; i >= 0; i-- {
		if f := g.feed[i]; f.Tower == tower {
			o := f.Outcome
			o.Real, o.Cached = false, true
			return o
		}
	}
	return Outcome{Tower: tower, Label: "blocked", At: g.now().UnixMilli()}
}

// Finish is a run's end as the phone reports it.
type Finish struct {
	Wave  int  `json:"wave"` // the wave the run ended on (past len(Waves) once the boss is beaten)
	Score int  `json:"score"`
	Lives int  `json:"lives"`
	Won   bool `json:"won"`
}

// Result is what Finish tells the phone.
type Result struct {
	Accepted bool `json:"accepted"`
	Best     int  `json:"best"`
	Rank     int  `json:"rank"`
	Beat     *struct {
		Nick  string `json:"nick"`
		Score int    `json:"score"`
	} `json:"beat,omitempty"` // the score to beat for the next rank up
	Top []Score `json:"top"` // the top topRows, for the end screen
}

// topRows is how much of the board the end screen shows.
const topRows = 5

// Finish ends the run and, if the score is possible, records it on the leaderboard: at
// most MaxScore for the wave reached, and no faster than MinSeconds.
func (g *Game) Finish(pl platform.Player, runID string, f Finish) (Result, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	r := g.runs[runID]
	if r == nil || r.Player != pl.ID || r.Finished {
		return Result{}, errNoRun
	}
	r.Finished = true
	g.runsDone++
	f.Won = f.Won && f.Wave > len(g.rules.Waves)
	ok := f.Wave >= 1 && f.Wave <= MaxWave && f.Score >= 0 && f.Score <= g.rules.MaxScore(f.Wave) &&
		f.Lives >= 0 && f.Lives <= g.rules.Lives &&
		g.now().Sub(r.Started).Seconds() >= g.rules.MinSeconds(f.Wave)*0.9
	res := Result{Accepted: ok}
	if ok {
		if b, had := g.board[pl.ID]; !had || f.Score > b.Score {
			g.board[pl.ID] = Score{PlayerID: pl.ID, Nick: pl.Nick, Color: pl.Color, Score: f.Score, Wave: f.Wave, Won: f.Won, At: g.now().UnixMilli()}
		}
		g.hub.Poke()
	}
	rows := g.boardLocked()
	res.Best = g.board[pl.ID].Score
	for i, s := range rows {
		if s.PlayerID == pl.ID {
			res.Rank = i + 1
			if i > 0 {
				res.Beat = &struct {
					Nick  string `json:"nick"`
					Score int    `json:"score"`
				}{rows[i-1].Nick, rows[i-1].Score}
			}
		}
	}
	res.Top = rows[:min(topRows, len(rows))]
	return res, nil
}

// Board is the leaderboard for one player: the top boardRows, with that player's own
// row marked, and their rank when they are further down.
type Board struct {
	Rows []BoardRow `json:"rows"`
	Me   *BoardRow  `json:"me,omitempty"`
}

// BoardRow is a leaderboard row with its rank, and whether it is the asking player's.
type BoardRow struct {
	Rank int  `json:"rank"`
	You  bool `json:"you,omitempty"`
	Score
}

// Leaderboard is the board as player sees it ("" for nobody in particular).
func (g *Game) Leaderboard(player string) Board {
	g.mu.Lock()
	defer g.mu.Unlock()
	b := Board{Rows: []BoardRow{}}
	for i, s := range g.boardLocked() {
		row := BoardRow{Rank: i + 1, You: player != "" && s.PlayerID == player, Score: s}
		if i < boardRows {
			b.Rows = append(b.Rows, row)
		}
		if row.You {
			b.Me = &row
		}
	}
	return b
}

// boardLocked is every best score, highest first (ties: earlier first).
func (g *Game) boardLocked() []Score {
	rows := make([]Score, 0, len(g.board))
	for _, s := range g.board {
		rows = append(rows, s)
	}
	slices.SortFunc(rows, func(a, b Score) int {
		if a.Score != b.Score {
			return b.Score - a.Score
		}
		if a.At != b.At {
			return int(a.At - b.At)
		}
		return strings.Compare(a.Nick, b.Nick)
	})
	return rows
}

// Sink is a request that reached the arcade through a gatedef route: for the guard route
// that means the prompt guard missed one (there is still no LLM behind it).
func (g *Game) Sink(kind string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.sink[kind]++
}

// State is the big screen's view.
type State struct {
	Board    []Score                         `json:"board"`
	Feed     []FeedItem                      `json:"feed"`
	Totals   map[string]int                  `json:"totals"`
	Misses   map[string]int                  `json:"misses"`
	Timings  map[string]platform.Percentiles `json:"timings"`
	Playing  int                             `json:"playing"`
	Runs     int                             `json:"runs"`
	Towers   []Tower                         `json:"towers"`
	BossWave int                             `json:"bossWave"`
	Now      int64                           `json:"now"`
}

// Snapshot is the big screen's state.
func (g *Game) Snapshot() State {
	g.mu.Lock()
	defer g.mu.Unlock()
	rows := g.boardLocked()
	if len(rows) > boardRows {
		rows = rows[:boardRows]
	}
	playing := 0
	for _, r := range g.runs {
		if !r.Finished && g.now().Sub(r.Started) < runTTL {
			playing++
		}
	}
	return State{Board: rows, Feed: slices.Clone(g.feed), Totals: clone(g.totals), Misses: clone(g.misses),
		Timings: g.timings.Snapshot(nil), Playing: playing, Runs: g.runsDone, Towers: g.rules.Towers, BossWave: len(g.rules.Waves), Now: g.now().UnixMilli()}
}

// Stats is the game's line in /api/stats.
func (g *Game) Stats() map[string]any {
	g.mu.Lock()
	defer g.mu.Unlock()
	out := map[string]any{"runs": g.runsDone, "players": len(g.board), "guardMisses": g.sink["guard"]}
	for k, v := range g.totals {
		out["real_"+k] = v
	}
	return out
}

func clone(m map[string]int) map[string]int {
	out := make(map[string]int, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

// screen counts a public big screen in; it is false when maxScreens are open.
func (g *Game) screen(public bool) (func(), bool) {
	if !public {
		return func() {}, true
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.screens >= maxScreens {
		return nil, false
	}
	g.screens++
	return func() { g.mu.Lock(); g.screens--; g.mu.Unlock() }, true
}

// Top is the leaderboard for the arcade's /api/boards: the n best runs.
func (g *Game) Top(n int) []platform.BoardRow {
	g.mu.Lock()
	defer g.mu.Unlock()
	out := []platform.BoardRow{}
	for _, s := range g.boardLocked() {
		if len(out) == n {
			break
		}
		d := fmt.Sprintf("wave %d", s.Wave)
		if s.Won {
			d += " · beat the boss"
		}
		out = append(out, platform.BoardRow{Nick: s.Nick, Color: s.Color, Score: s.Score, Detail: d})
	}
	return out
}
