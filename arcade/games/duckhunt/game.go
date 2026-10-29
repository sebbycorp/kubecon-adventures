// Package duckhunt is Duck Hunt: every duck is a kagent session running as an Agent
// Substrate actor; the Hunter suspends idle ducks and owners revive them.
package duckhunt

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"sort"
	"sync"
	"time"

	"github.com/sebbycorp/kubecon-adventures/arcade/internal/kagent"
	"github.com/sebbycorp/kubecon-adventures/arcade/internal/platform"
)

// Flight is the duck agent's structured output (see manifests/duck-agent.yaml).
type Flight struct {
	Pattern string `json:"pattern"`
	Speed   int    `json:"speed"`
	Quack   string `json:"quack"`
}

func (f Flight) clean() Flight {
	switch f.Pattern {
	case "straight", "zigzag", "loop", "dive", "wobble":
	default:
		f.Pattern = "wobble"
	}
	f.Speed = min(max(f.Speed, 1), 10)
	if r := []rune(f.Quack); len(r) > 60 {
		f.Quack = string(r[:59]) + "…"
	}
	return f
}

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
	Actor       *kagent.Actor `json:"actor,omitempty"` // the duck's Substrate actor (one per session)
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
	p        *platform.Platform
	k        *kagent.Kagent
	owned    map[string]map[string]string // player ID -> prompt kind -> duck ID
	ducks    map[string]*Duck
	settings Settings
	timings  *platform.Timings // hatch, reply, suspend, resume (kagent API)
	tokens   kagent.Usage
	llmCalls int64
	awake    Awake
	events   []Event
	hub      *platform.Hub
}

func New(p *platform.Platform, k *kagent.Kagent, idle int) *Game {
	g := &Game{
		p: p, k: k, owned: map[string]map[string]string{}, ducks: map[string]*Duck{},
		settings: Settings{HunterOn: true, IdleSeconds: idle},
		timings:  platform.NewTimings(), hub: platform.NewHub(),
	}
	p.OnChange(g.hub.Poke)
	return g
}

// Run starts the Hunter, the actor poller and the live-update hub.
func (g *Game) Run(ctx context.Context) {
	go g.hub.Run(ctx, func() any { return g.Snapshot() })
	go g.RunHunter(ctx)
	go g.RunActorPoller(ctx)
}

// --- duck lifecycle -------------------------------------------------------

// Hatch creates a session for the player's prompt and sends the first prompt.
func (g *Game) Hatch(p platform.Player, kind string) (string, error) {
	prompt, ok := Prompts[kind]
	if !ok {
		return "", errors.New("unknown prompt")
	}
	g.mu.Lock()
	if g.owned[p.ID] == nil {
		g.owned[p.ID] = map[string]string{}
	}
	if _, used := g.owned[p.ID][kind]; used {
		g.mu.Unlock()
		return "", errors.New("you already hatched this duck")
	}
	d := &Duck{ID: platform.NewID(), PlayerID: p.ID, Nick: p.Nick, Color: p.Color, Kind: kind,
		State: Hatching, Busy: true, HatchedAt: time.Now(), LastActive: time.Now()}
	g.owned[p.ID][kind] = d.ID
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
				d.stepLocked("kagent", fmt.Sprintf("kagent created session %s in %s; Substrate gave it actor session-…%s", sid[len(sid)-4:], platform.FmtDur(ms), sid[len(sid)-4:]))
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
	reply, err := g.k.Prompt(ctx, sessionID, text)
	ms := time.Since(t0).Milliseconds()
	usage := reply.Usage
	var flight Flight
	if err == nil {
		if err = reply.Decode(&flight); err == nil {
			flight = flight.clean()
		}
	}
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
		d.stepLocked("llm", fmt.Sprintf("Answered in %s: %d tokens to OpenAI through agentgateway, which holds the key", platform.FmtDur(ms), usage.Input+usage.Output))
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
			d.stepLocked("hunter", fmt.Sprintf("The Hunter shot it: kagent suspended the session in %s, so no message can wake it", platform.FmtDur(ms)))
		})
		if err != nil {
			slog.Error("shoot failed", "duck", d.ID, "err", err)
			return
		}
		g.record("suspend", ms)
		// The actor was already snapshotted by Substrate when it went idle; show Substrate's own snapshot time.
		g.emit(Event{Type: "shot", DuckID: d.ID, Nick: d.Nick, Ms: g.p.Timings.Typical("snapshot")})
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
				d.stepLocked("owner", fmt.Sprintf("Revived: kagent resumed the session in %s; its memory comes back with the snapshot", platform.FmtDur(ms)))
			})
			// The revive prompt is what restores the actor; show Substrate's own restore time.
			g.emit(Event{Type: "revive", DuckID: d.ID, Nick: d.Nick, Ms: g.p.Timings.Typical("restore")})
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
	if g.owned[d.PlayerID][d.Kind] == duckID {
		delete(g.owned[d.PlayerID], d.Kind)
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
	g.timings.Reset()
	g.mu.Lock()
	g.owned, g.ducks = map[string]map[string]string{}, map[string]*Duck{}
	g.tokens, g.llmCalls, g.events, g.awake = kagent.Usage{}, 0, nil, Awake{}
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
		return Duck{}, platform.ErrNotYours
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

func (g *Game) record(kind string, ms int64) { g.timings.Record(kind, ms) }

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
					g.emitLocked(Event{Type: "wake", DuckID: d.ID, Nick: d.Nick, Ms: g.p.Timings.Typical("restore")})
				case st == "SUSPENDED" && wasAwake:
					up := time.Since(d.awakeSince).Milliseconds()
					d.stepLocked("substrate", fmt.Sprintf("Sandbox ASLEEP after %s awake: snapshot saved, the worker is free for other ducks", platform.FmtDur(up)))
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

type State struct {
	Ducks     []Duck                 `json:"ducks"`
	Players   int                    `json:"players"`
	Settings  Settings               `json:"settings"`
	Timings   map[string]platform.Percentiles `json:"timings"`
	Tokens    kagent.Usage                    `json:"tokens"`
	LLMCalls  int64                           `json:"llmCalls"`
	Substrate kagent.SubstrateStats           `json:"substrate"`
	Awake     Awake                  `json:"awake"`
	Events    []Event                `json:"events"`
	Now       int64                  `json:"now"`
}

func (g *Game) Snapshot() State {
	g.mu.Lock()
	defer g.mu.Unlock()
	s := State{Players: len(g.owned), Settings: g.settings, Tokens: g.tokens, LLMCalls: g.llmCalls,
		Substrate: g.p.Substrate(), Awake: g.awake, Events: slices.Clone(g.events), Now: time.Now().UnixMilli()}
	// Substrate's own snapshot/restore timings are arcade-wide; the rest are this game's.
	s.Timings = g.timings.Snapshot(g.p.Timings.Snapshot(nil))
	s.Awake.History = slices.Clone(g.awake.History)
	for _, d := range g.ducks {
		s.Ducks = append(s.Ducks, *d)
	}
	sort.Slice(s.Ducks, func(i, j int) bool { return s.Ducks[i].HatchedAt.Before(s.Ducks[j].HatchedAt) })
	return s
}

// changedLocked tells the hub to push a fresh snapshot. Caller holds g.mu.
func (g *Game) changedLocked() { g.hub.Poke() }

// Hub serves the live game state to screens.
func (g *Game) Hub() *platform.Hub { return g.hub }
