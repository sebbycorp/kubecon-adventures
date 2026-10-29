package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"sort"
	"sync"
	"time"
)

// Prompts are the three fixed buttons; each hatches one duck.
var Prompts = map[string]string{
	"fast":    "You're a speedy duck. Pick your flight.",
	"zigzag":  "You're a sneaky duck dodging hunters. Pick your flight.",
	"showoff": "You're a show-off duck performing for a crowd. Pick your flight.",
}

const (
	quackPrompt  = "Quack! The crowd is cheering. Keep flying, maybe change it up."
	revivePrompt = "You were shot down by the hunter and just got revived. React, and say what you were doing before."
)

var colors = []string{"green", "blue", "red", "purple", "gold", "pink"}

type DuckState string

const (
	Hatching DuckState = "hatching"
	Flying   DuckState = "flying"
	Shot     DuckState = "shot"
	Reviving DuckState = "reviving"
	Failed   DuckState = "failed"
)

type Duck struct {
	ID          string    `json:"id"`
	SessionID   string    `json:"sessionId"`
	PlayerID    string    `json:"playerId"`
	Nick        string    `json:"nick"`
	Color       string    `json:"color"`
	Kind        string    `json:"kind"`
	State       DuckState `json:"state"`
	Busy        bool      `json:"busy"`
	Flight      Flight    `json:"flight"`
	HatchedAt   time.Time `json:"hatchedAt"`
	LastActive  time.Time `json:"lastActive"`
	Shots       int       `json:"shots"`
	Revives     int       `json:"revives"`
	Quacks      int       `json:"quacks"`
	HatchMs     int64     `json:"hatchMs,omitempty"`
	LastShotMs  int64     `json:"lastShotMs,omitempty"`
	LastRevMs   int64     `json:"lastReviveMs,omitempty"`
	LastReplyMs int64     `json:"lastReplyMs,omitempty"`
	Actor       *Actor    `json:"actor,omitempty"` // the duck's Substrate actor (one per session)
	Timeline    []Step    `json:"timeline,omitempty"`
	awakeSince  time.Time
	Error       string    `json:"error,omitempty"`
}

// Step is one line of a duck's life story, shown on the owner's phone.
type Step struct {
	At   int64  `json:"at"`
	Kind string `json:"kind"` // kagent, substrate, llm, hunter, owner
	Text string `json:"text"`
}

// stepLocked appends to a duck's timeline. Caller holds g.mu.
func (d *Duck) stepLocked(kind, text string) {
	d.Timeline = append(d.Timeline, Step{At: time.Now().UnixMilli(), Kind: kind, Text: text})
	if n := len(d.Timeline); n > 12 {
		d.Timeline = d.Timeline[n-12:]
	}
}

type Player struct {
	ID    string            `json:"id"`
	Nick  string            `json:"nick"`
	Color string            `json:"color"`
	Ducks map[string]string `json:"ducks"` // prompt kind -> duck ID
}

// Event is a one-off moment the screens animate (shot, revive, hatch).
type Event struct {
	Type   string `json:"type"`
	DuckID string `json:"duckId"`
	Nick   string `json:"nick"`
	Ms     int64  `json:"ms,omitempty"`
	Text   string `json:"text,omitempty"`
	At     int64  `json:"at"`
}

type Settings struct {
	HunterOn    bool `json:"hunterOn"`
	IdleSeconds int  `json:"idleSeconds"`
}

type Game struct {
	mu       sync.Mutex
	k        *Kagent
	players  map[string]*Player
	ducks    map[string]*Duck
	settings Settings
	timings  map[string][]int64 // hatch, reply, suspend, resume (kagent API); snapshot, restore (Substrate)
	tokens   Usage
	llmCalls int64
	sub      SubstrateStats
	awake    Awake
	events   []Event
	hub      *Hub
}

func NewGame(k *Kagent, idle int) *Game {
	return &Game{
		k: k, players: map[string]*Player{}, ducks: map[string]*Duck{},
		settings: Settings{HunterOn: true, IdleSeconds: idle},
		timings:  map[string][]int64{}, hub: NewHub(),
	}
}

func newID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// --- players --------------------------------------------------------------

func (g *Game) AddPlayer(nick string) *Player {
	g.mu.Lock()
	defer g.mu.Unlock()
	p := &Player{ID: newID(), Nick: nick, Color: colors[len(g.players)%len(colors)], Ducks: map[string]string{}}
	g.players[p.ID] = p
	g.changedLocked()
	return p
}

func (g *Game) Player(id string) (Player, bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	p, ok := g.players[id]
	if !ok {
		return Player{}, false
	}
	cp := *p
	cp.Ducks = map[string]string{}
	for k, v := range p.Ducks {
		cp.Ducks[k] = v
	}
	return cp, true
}

// --- duck lifecycle -------------------------------------------------------

var errNotYours = errors.New("not your duck")

// Hatch creates a session for the player's prompt and sends the first prompt.
func (g *Game) Hatch(playerID, kind string) (string, error) {
	prompt, ok := Prompts[kind]
	if !ok {
		return "", errors.New("unknown prompt")
	}
	g.mu.Lock()
	p, ok := g.players[playerID]
	if !ok {
		g.mu.Unlock()
		return "", errors.New("unknown player")
	}
	if _, used := p.Ducks[kind]; used {
		g.mu.Unlock()
		return "", errors.New("you already hatched this duck")
	}
	d := &Duck{ID: newID(), PlayerID: p.ID, Nick: p.Nick, Color: p.Color, Kind: kind,
		State: Hatching, Busy: true, HatchedAt: time.Now(), LastActive: time.Now()}
	p.Ducks[kind] = d.ID
	g.ducks[d.ID] = d
	g.changedLocked()
	g.mu.Unlock()

	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
		defer cancel()
		t0 := time.Now()
		sid, err := g.k.CreateSession(ctx, "duck-"+p.Nick+"-"+kind)
		if err == nil {
			ms := time.Since(t0).Milliseconds()
			g.update(d.ID, func(d *Duck) {
				d.SessionID = sid
				d.stepLocked("kagent", fmt.Sprintf("kagent created session %s in %s; Substrate gave it actor session-…%s", sid[len(sid)-4:], fmtDur(ms), sid[len(sid)-4:]))
			})
			err = g.prompt(ctx, d.ID, sid, prompt)
		}
		ms := time.Since(t0).Milliseconds()
		g.update(d.ID, func(d *Duck) {
			d.Busy = false
			d.LastActive = time.Now()
			if err != nil {
				d.State, d.Error = Failed, err.Error()
				return
			}
			d.State, d.HatchMs = Flying, ms
		})
		if err != nil {
			slog.Error("hatch failed", "duck", d.ID, "err", err)
			return
		}
		g.record("hatch", ms)
		g.emit(Event{Type: "hatch", DuckID: d.ID, Nick: d.Nick, Ms: ms})
	}()
	return d.ID, nil
}

// prompt sends text to the duck's agent and applies the returned flight.
func (g *Game) prompt(ctx context.Context, duckID, sessionID, text string) error {
	t0 := time.Now()
	flight, usage, err := g.k.Prompt(ctx, sessionID, text)
	ms := time.Since(t0).Milliseconds()
	g.mu.Lock()
	g.llmCalls++
	g.tokens.Input += usage.Input
	g.tokens.Output += usage.Output
	g.mu.Unlock()
	if err != nil {
		return err
	}
	g.record("reply", ms)
	g.update(duckID, func(d *Duck) {
		d.Flight, d.LastReplyMs = flight, ms
		d.stepLocked("llm", fmt.Sprintf("Answered in %s: %d tokens to OpenAI through agentgateway, which holds the key", fmtDur(ms), usage.Input+usage.Output))
	})
	g.emit(Event{Type: "quack", DuckID: duckID, Text: flight.Quack, Ms: ms})
	return nil
}

// Quack keeps a flying duck alive with a follow-up prompt.
func (g *Game) Quack(playerID, duckID string) error {
	d, err := g.claim(playerID, duckID, Flying)
	if err != nil {
		return err
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		err := g.prompt(ctx, d.ID, d.SessionID, quackPrompt)
		g.update(d.ID, func(d *Duck) {
			d.Busy, d.LastActive = false, time.Now()
			d.Quacks++
			if err != nil {
				d.Error = err.Error()
			}
		})
	}()
	return nil
}

// Shoot suspends a duck's session (the Hunter, or the admin).
func (g *Game) Shoot(duckID string) error {
	d, err := g.claim("", duckID, Flying)
	if err != nil {
		return err
	}
	g.emit(Event{Type: "aim", DuckID: d.ID, Nick: d.Nick})
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		t0 := time.Now()
		err := g.k.Suspend(ctx, d.SessionID)
		ms := time.Since(t0).Milliseconds()
		g.update(d.ID, func(d *Duck) {
			d.Busy = false
			if err != nil {
				d.Error = err.Error()
				return
			}
			d.State, d.LastShotMs = Shot, ms
			d.Shots++
			d.stepLocked("hunter", fmt.Sprintf("The Hunter shot it: kagent suspended the session in %s, so no message can wake it", fmtDur(ms)))
		})
		if err != nil {
			slog.Error("shoot failed", "duck", d.ID, "err", err)
			return
		}
		g.record("suspend", ms)
		// The actor was already snapshotted by Substrate when it went idle; show Substrate's own snapshot time.
		g.emit(Event{Type: "shot", DuckID: d.ID, Nick: d.Nick, Ms: g.typical("snapshot")})
	}()
	return nil
}

// Revive resumes a shot duck from its snapshot and asks it to react.
func (g *Game) Revive(playerID, duckID string) error {
	d, err := g.claim(playerID, duckID, Shot)
	if err != nil {
		return err
	}
	g.update(d.ID, func(d *Duck) { d.State = Reviving })
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		t0 := time.Now()
		err := g.k.Resume(ctx, d.SessionID)
		ms := time.Since(t0).Milliseconds()
		if err == nil {
			g.record("resume", ms)
			g.update(d.ID, func(d *Duck) {
				d.State, d.LastRevMs = Flying, ms
				d.Revives++
				d.stepLocked("owner", fmt.Sprintf("Revived: kagent resumed the session in %s; its memory comes back with the snapshot", fmtDur(ms)))
			})
			// The revive prompt is what restores the actor; show Substrate's own restore time.
			g.emit(Event{Type: "revive", DuckID: d.ID, Nick: d.Nick, Ms: g.typical("restore")})
			err = g.prompt(ctx, d.ID, d.SessionID, revivePrompt)
		}
		g.update(d.ID, func(d *Duck) {
			d.Busy, d.LastActive = false, time.Now()
			if err != nil {
				d.Error = err.Error()
				if d.State == Reviving {
					d.State = Shot
				}
			}
		})
	}()
	return nil
}

// Delete removes a duck and its session (admin).
func (g *Game) Delete(duckID string) error {
	g.mu.Lock()
	d, ok := g.ducks[duckID]
	if !ok {
		g.mu.Unlock()
		return errors.New("no such duck")
	}
	delete(g.ducks, duckID)
	if p, ok := g.players[d.PlayerID]; ok && p.Ducks[d.Kind] == duckID {
		delete(p.Ducks, d.Kind)
	}
	sid := d.SessionID
	g.changedLocked()
	g.mu.Unlock()
	if sid == "" {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	return g.k.Delete(ctx, sid)
}

// Reset deletes every duck session this backend owns and clears the round.
func (g *Game) Reset(ctx context.Context) (int, error) {
	ids, err := g.k.ListSessionIDs(ctx)
	if err != nil {
		return 0, err
	}
	for _, id := range ids {
		if err := g.k.Delete(ctx, id); err != nil {
			slog.Warn("delete session", "session", id, "err", err)
		}
	}
	g.mu.Lock()
	g.players, g.ducks, g.timings = map[string]*Player{}, map[string]*Duck{}, map[string][]int64{}
	g.tokens, g.llmCalls, g.events, g.awake = Usage{}, 0, nil, Awake{}
	g.changedLocked()
	g.mu.Unlock()
	return len(ids), nil
}

func (g *Game) SetSettings(s Settings) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.settings.HunterOn = s.HunterOn
	if s.IdleSeconds >= 5 {
		g.settings.IdleSeconds = s.IdleSeconds
	}
	g.changedLocked()
}

// claim marks a duck busy if it is in the wanted state and owned by playerID ("" = any).
func (g *Game) claim(playerID, duckID string, want DuckState) (Duck, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	d, ok := g.ducks[duckID]
	switch {
	case !ok:
		return Duck{}, errors.New("no such duck")
	case playerID != "" && d.PlayerID != playerID:
		return Duck{}, errNotYours
	case d.Busy:
		return Duck{}, errors.New("duck is busy")
	case d.State != want:
		return Duck{}, errors.New("duck is " + string(d.State))
	}
	d.Busy = true
	g.changedLocked()
	return *d, nil
}

func (g *Game) update(duckID string, f func(*Duck)) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if d, ok := g.ducks[duckID]; ok {
		f(d)
		g.changedLocked()
	}
}

func (g *Game) record(kind string, ms int64) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.timings[kind] = append(g.timings[kind], ms)
	if n := len(g.timings[kind]); n > 1000 {
		g.timings[kind] = g.timings[kind][n-1000:]
	}
}

// typical is the median of recent timings of a kind, or 0 if there are none.
func (g *Game) typical(kind string) int64 {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.typicalLocked(kind)
}

func (g *Game) typicalLocked(kind string) int64 {
	v := slices.Clone(g.timings[kind])
	if len(v) == 0 {
		return 0
	}
	slices.Sort(v)
	return v[len(v)/2]
}

func (g *Game) emit(e Event) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.emitLocked(e)
}

func (g *Game) emitLocked(e Event) {
	e.At = time.Now().UnixMilli()
	g.events = append(g.events, e)
	if n := len(g.events); n > 80 {
		g.events = g.events[n-80:]
	}
	g.changedLocked()
}

func fmtDur(ms int64) string {
	if ms >= 1000 {
		return fmt.Sprintf("%.1fs", float64(ms)/1000)
	}
	return fmt.Sprintf("%dms", ms)
}

// --- background loops -----------------------------------------------------

// RunHunter shoots ducks nobody has prompted for IdleSeconds.
func (g *Game) RunHunter(ctx context.Context) {
	t := time.NewTicker(time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		g.mu.Lock()
		var targets []string
		if g.settings.HunterOn {
			idle := time.Duration(g.settings.IdleSeconds) * time.Second
			for _, d := range g.ducks {
				if d.State == Flying && !d.Busy && time.Since(d.LastActive) > idle {
					targets = append(targets, d.ID)
				}
			}
		}
		g.mu.Unlock()
		for _, id := range targets {
			_ = g.Shoot(id)
		}
	}
}

// RunSubstratePoller refreshes the Substrate summary for the stats panel.
func (g *Game) RunSubstratePoller(ctx context.Context) {
	for {
		c, cancel := context.WithTimeout(ctx, 5*time.Second)
		s, err := g.k.Substrate(c)
		cancel()
		if err != nil {
			s.Error = err.Error()
		}
		g.mu.Lock()
		g.sub = s
		g.changedLocked()
		g.mu.Unlock()
		select {
		case <-ctx.Done():
			return
		case <-time.After(2 * time.Second):
		}
	}
}

// Awake tracks how many duck sandboxes are running versus asleep as snapshots.
type Awake struct {
	Now     int   `json:"now"`     // duck actors RUNNING, RESUMING or SUSPENDING
	Asleep  int   `json:"asleep"`  // duck actors SUSPENDED (snapshot only)
	Peak    int   `json:"peak"`    // most awake at once this round
	WakeUps int64 `json:"wakeUps"` // SUSPENDED -> awake transitions seen this round
	History []int `json:"history"` // awake count, one sample per second, last 60 s
}

func awakeState(s string) bool { return s == "RUNNING" || s == "RESUMING" || s == "SUSPENDING" }

// RunActorPoller matches Substrate actors to ducks twice a second, so screens can show
// which ducks have a running sandbox and which are asleep as snapshots.
func (g *Game) RunActorPoller(ctx context.Context) {
	tick := 0
	for {
		c, cancel := context.WithTimeout(ctx, 3*time.Second)
		states, err := g.k.ActorStates(c)
		cancel()
		if err == nil {
			g.mu.Lock()
			now, asleep := 0, 0
			for _, d := range g.ducks {
				a, ok := states[d.SessionID]
				if !ok || d.SessionID == "" {
					continue
				}
				st := a.State
				wasAwake := d.Actor != nil && awakeState(d.Actor.State)
				if d.Actor != nil && awakeState(st) && !wasAwake {
					g.awake.WakeUps++
				}
				switch {
				case awakeState(st) && !wasAwake:
					d.awakeSince = time.Now()
					where := ""
					if a.Node != "" {
						where = " on " + a.Node
					}
					d.stepLocked("substrate", "Sandbox AWAKE"+where+": Substrate restored the gVisor sandbox from its snapshot")
					g.emitLocked(Event{Type: "wake", DuckID: d.ID, Nick: d.Nick, Ms: g.typicalLocked("restore")})
				case st == "SUSPENDED" && wasAwake:
					up := time.Since(d.awakeSince).Milliseconds()
					d.stepLocked("substrate", fmt.Sprintf("Sandbox ASLEEP after %s awake: snapshot saved, the worker is free for other ducks", fmtDur(up)))
					g.emitLocked(Event{Type: "sleep", DuckID: d.ID, Nick: d.Nick, Ms: up})
				}
				if d.Actor == nil || *d.Actor != a {
					cp := a
					d.Actor = &cp
					g.changedLocked()
				}
				if awakeState(st) {
					now++
				} else if st == "SUSPENDED" {
					asleep++
				}
			}
			if now != g.awake.Now || asleep != g.awake.Asleep {
				g.changedLocked()
			}
			g.awake.Now, g.awake.Asleep, g.awake.Peak = now, asleep, max(g.awake.Peak, now)
			if tick%2 == 0 {
				g.awake.History = append(g.awake.History, now)
				if n := len(g.awake.History); n > 60 {
					g.awake.History = g.awake.History[n-60:]
				}
			}
			g.mu.Unlock()
		}
		tick++
		select {
		case <-ctx.Done():
			return
		case <-time.After(500 * time.Millisecond):
		}
	}
}

// --- state snapshot -------------------------------------------------------

type Percentiles struct {
	Count int   `json:"count"`
	Last  int64 `json:"last"`
	P50   int64 `json:"p50"`
	P95   int64 `json:"p95"`
}

type State struct {
	Ducks     []Duck                 `json:"ducks"`
	Players   int                    `json:"players"`
	Settings  Settings               `json:"settings"`
	Timings   map[string]Percentiles `json:"timings"`
	Tokens    Usage                  `json:"tokens"`
	LLMCalls  int64                  `json:"llmCalls"`
	Substrate SubstrateStats         `json:"substrate"`
	Awake     Awake                  `json:"awake"`
	Events    []Event                `json:"events"`
	Now       int64                  `json:"now"`
}

func (g *Game) Snapshot() State {
	g.mu.Lock()
	defer g.mu.Unlock()
	s := State{Players: len(g.players), Settings: g.settings, Tokens: g.tokens, LLMCalls: g.llmCalls,
		Substrate: g.sub, Awake: g.awake, Events: slices.Clone(g.events), Timings: map[string]Percentiles{}, Now: time.Now().UnixMilli()}
	s.Awake.History = slices.Clone(g.awake.History)
	for _, d := range g.ducks {
		s.Ducks = append(s.Ducks, *d)
	}
	sort.Slice(s.Ducks, func(i, j int) bool { return s.Ducks[i].HatchedAt.Before(s.Ducks[j].HatchedAt) })
	for k, v := range g.timings {
		if len(v) == 0 {
			continue
		}
		sorted := slices.Clone(v)
		slices.Sort(sorted)
		pct := func(p float64) int64 { return sorted[int(p*float64(len(sorted)-1))] }
		s.Timings[k] = Percentiles{Count: len(v), Last: v[len(v)-1], P50: pct(0.5), P95: pct(0.95)}
	}
	return s
}

// changedLocked tells the hub to push a fresh snapshot. Caller holds g.mu.
func (g *Game) changedLocked() { g.hub.Poke() }
