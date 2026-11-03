// Package selfie is Substrate Selfie (at /selfie): a photo booth on the phone. A visitor
// takes a selfie; the arcade sends it (downscaled, in memory only) to the model through
// agentgateway's /selfie/v1 route, which writes ten funny captions; the phone draws ten
// scenes with the Agent Substrate octopus and the kagent logo, ready to share. Every
// caption job also borrows a real kagent session from a small pool: it wakes (a real
// Resume) while the captions are written and is snapshotted (a real Suspend) after, so
// the visitor sees their agent's real Substrate timings. Nothing waits on Substrate, and
// the photo is never written to disk or logged.
package selfie

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/sebbycorp/kubecon-adventures/arcade/internal/kagent"
	"github.com/sebbycorp/kubecon-adventures/arcade/internal/platform"
)

// AgentName is the kagent Agent whose sessions are the booth's caption agents.
const AgentName = "selfiebot"

// Ops is what the booth needs from kagent (a *kagent.Kagent for selfiebot).
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
	poolSize     = 4 // caption agents, asleep between jobs
	maxAwake     = 3 // awake at once (the selfie WorkerPool has 3 workers)
	maxJobs      = 6 // caption calls at once across the arcade
	perPlayer    = 3 // caption calls per player
	perWindow    = time.Minute
	maxImage     = 1536 * 1024 // bytes
	captionLimit = 22 * time.Second
	opTimeout    = 30 * time.Second
	keepEvents   = 30
)

var (
	ErrTooBig   = errors.New("that photo is too big; try again")
	ErrNotImage = errors.New("that isn't a JPEG or PNG photo")
	ErrSlowDown = errors.New("three selfies a minute, please; try again shortly")
	ErrBusy     = errors.New("the booth is busy; try again in a few seconds")
)

type slot struct {
	sid   string
	state string // asleep, waking, awake, sleeping
	jobs  int
}

// Agent is the caption agent's part in a job: its real Substrate timings.
type Agent struct {
	State      string `json:"state"`                // snapshotted, or none (no free sandbox, or a call failed)
	WokeMs     int64  `json:"wokeMs,omitempty"`     // the real Resume
	SnapshotMs int64  `json:"snapshotMs,omitempty"` // the real Suspend
	Reason     string `json:"reason,omitempty"`
}

// Result is one caption job: the captions, the model's real token use, and the agent.
type Result struct {
	Captions
	Scenes []Scene `json:"scenes"`
	Usage  Usage   `json:"usage"`
	Agent  Agent   `json:"agent"`
	Ms     int64   `json:"ms"` // the model call, through agentgateway
	Taken  int     `json:"taken"`
}

// Event is what the big screen shows: one caption (text only, never a photo).
type Event struct {
	Nick    string `json:"nick,omitempty"`
	Color   string `json:"color,omitempty"`
	Scene   string `json:"scene"`
	Caption string `json:"caption"`
	Tokens  int    `json:"tokens,omitempty"`
	At      int64  `json:"at"`
}

type Game struct {
	mu       sync.Mutex
	p        *platform.Platform
	k        Ops
	llm      Captioner
	slots    []*slot
	recent   map[string][]time.Time // caption calls per player in the last window
	jobs     int
	taken    int
	fellBack int
	tokens   int
	events   []Event
	timings  *platform.Timings // resume, suspend, captions
	hub      *platform.Hub
	now      func() time.Time
	stopped  chan struct{}
}

// New builds the booth on selfiebot's kagent client and a captioner (agentgateway's route).
func New(p *platform.Platform, k Ops, llm Captioner) *Game {
	g := &Game{p: p, k: k, llm: llm, recent: map[string][]time.Time{}, timings: platform.NewTimings(),
		hub: platform.NewHub(), now: time.Now, stopped: make(chan struct{})}
	if p != nil {
		p.OnChange(g.hub.Poke)
	}
	return g
}

// Run sweeps leftover sessions, fills the pool and serves the big screen. When ctx ends
// it deletes the pool and closes Stopped.
func (g *Game) Run(ctx context.Context) {
	go g.hub.Run(ctx, func() any { return g.Snapshot() })
	go func() {
		g.sweep(ctx)
		g.fillPool(ctx)
	}()
	go func() {
		<-ctx.Done()
		g.shutdown()
	}()
}

// Stopped is closed once the booth has deleted its sessions on the way out.
func (g *Game) Stopped() <-chan struct{} { return g.stopped }

func (g *Game) sweep(ctx context.Context) {
	c, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	ids, err := g.k.ListSessionIDs(c)
	if err != nil {
		slog.Warn("selfie: listing sessions", "err", err)
		return
	}
	n := 0
	for _, id := range ids {
		if g.k.Delete(c, id) == nil {
			n++
		}
	}
	slog.Info("selfie: startup sweep", "deleted", n, "found", len(ids))
}

// fillPool creates the pool. A session joins asleep, after one short turn has placed its
// actor on a worker (the reply is never shown or logged) and a suspend has snapshotted it.
func (g *Game) fillPool(ctx context.Context) {
	for i := range poolSize {
		if ctx.Err() != nil {
			return
		}
		c, cancel := context.WithTimeout(ctx, 3*time.Minute)
		id, err := g.k.CreateSession(c, fmt.Sprintf("selfie-%d", i))
		if err == nil {
			_, _ = g.k.Prompt(c, id, "You just joined a KubeCon photo booth. Say hi in three words.")
			if err = settle(c, func() error { return g.k.Suspend(c, id) }); err != nil {
				_ = g.k.Delete(c, id)
			}
		}
		cancel()
		if err != nil {
			slog.Warn("selfie: new pool session", "err", err)
			continue
		}
		g.mu.Lock()
		g.slots = append(g.slots, &slot{sid: id, state: "asleep"})
		g.mu.Unlock()
		g.hub.Poke()
	}
	slog.Info("selfie: pool ready", "sessions", len(g.slots))
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
}

// IsImage reports whether b starts like a JPEG or a PNG.
func IsImage(b []byte) bool {
	return len(b) > 8 && (b[0] == 0xFF && b[1] == 0xD8 || string(b[:8]) == "\x89PNG\r\n\x1a\n")
}

// admit checks the size, the per-player limit and the arcade-wide cap, and counts the job.
func (g *Game) admit(player string, img []byte) error {
	switch {
	case len(img) > maxImage:
		return ErrTooBig
	case !IsImage(img):
		return ErrNotImage
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	now := g.now()
	keep := g.recent[player][:0]
	for _, t := range g.recent[player] {
		if now.Sub(t) < perWindow {
			keep = append(keep, t)
		}
	}
	g.recent[player] = keep
	switch {
	case len(keep) >= perPlayer:
		return ErrSlowDown
	case g.jobs >= maxJobs:
		return ErrBusy
	}
	g.recent[player] = append(keep, now)
	g.jobs++
	return nil
}

// Make writes the ten captions for one photo. The caption agent wakes while the model
// works and is snapshotted after; the photo lives only in this call.
func (g *Game) Make(ctx context.Context, pl platform.Player, img []byte) (Result, error) {
	if err := g.admit(pl.ID, img); err != nil {
		return Result{}, err
	}
	defer func() { g.mu.Lock(); g.jobs--; g.mu.Unlock() }()

	s := g.pick()
	woke := make(chan Agent, 1)
	if s != nil {
		go func() {
			c, cancel := context.WithTimeout(context.Background(), opTimeout)
			defer cancel()
			t0 := time.Now()
			if err := settle(c, func() error { return g.k.Resume(c, s.sid) }); err != nil {
				slog.Warn("selfie: resume", "session", s.sid, "err", err)
				woke <- Agent{State: "none", Reason: "the wake failed"}
				return
			}
			ms := time.Since(t0).Milliseconds()
			g.timings.Record("resume", ms)
			g.setState(s, "awake")
			woke <- Agent{State: "awake", WokeMs: ms}
		}()
	} else {
		woke <- Agent{State: "none", Reason: "every caption agent is busy"}
	}

	c, cancel := context.WithTimeout(ctx, captionLimit)
	t0 := time.Now()
	caps, usage, err := g.llm.Captions(c, img, pl.ID)
	ms := time.Since(t0).Milliseconds()
	cancel()
	if err != nil {
		// The reason is kagent's or agentgateway's status, never the model's words.
		slog.Warn("selfie: captions", "err", err)
		caps = fallbackCaptions(err.Error())
	} else {
		g.timings.Record("captions", ms)
	}

	agent := <-woke
	if s != nil {
		if agent.State == "awake" {
			c, cancel := context.WithTimeout(context.Background(), opTimeout)
			t1 := time.Now()
			if err := settle(c, func() error { return g.k.Suspend(c, s.sid) }); err == nil {
				agent.SnapshotMs = time.Since(t1).Milliseconds()
				agent.State = "snapshotted"
				g.timings.Record("suspend", agent.SnapshotMs)
			} else {
				slog.Warn("selfie: suspend", "session", s.sid, "err", err)
				agent.Reason = "the snapshot failed"
			}
			cancel()
		}
		g.setState(s, "asleep")
	}

	g.mu.Lock()
	g.taken++
	g.tokens += usage.Input + usage.Output
	if caps.Fallback {
		g.fellBack++
	}
	taken := g.taken
	sc := Scenes[(taken-1)%len(Scenes)]
	g.events = append(g.events, Event{Nick: pl.Nick, Color: pl.Color, Scene: sc.Title, Caption: caps.Captions[sc.ID],
		Tokens: usage.Input + usage.Output, At: g.now().UnixMilli()})
	if n := len(g.events); n > keepEvents {
		g.events = g.events[n-keepEvents:]
	}
	g.mu.Unlock()
	g.hub.Poke()
	return Result{Captions: caps, Scenes: Scenes, Usage: usage, Agent: agent, Ms: ms, Taken: taken}, nil
}

// pick takes an asleep caption agent, or nil when none is free or maxAwake are up.
func (g *Game) pick() *slot {
	g.mu.Lock()
	defer g.mu.Unlock()
	awake := 0
	var best *slot
	for _, s := range g.slots {
		if s.state != "asleep" {
			awake++
		} else if best == nil || s.jobs < best.jobs {
			best = s
		}
	}
	if best == nil || awake >= maxAwake {
		return nil
	}
	best.state = "waking"
	best.jobs++
	return best
}

func (g *Game) setState(s *slot, state string) {
	g.mu.Lock()
	s.state = state
	g.mu.Unlock()
	g.hub.Poke()
}

// State is the big screen's view: counts and recent captions (text only).
type State struct {
	Taken    int                             `json:"taken"`
	Fallback int                             `json:"fallback"`
	Tokens   int                             `json:"tokens"`
	Pool     int                             `json:"pool"`
	Awake    int                             `json:"awake"`
	Events   []Event                         `json:"events"`
	Timings  map[string]platform.Percentiles `json:"timings"`
	Now      int64                           `json:"now"`
}

func (g *Game) Snapshot() State {
	g.mu.Lock()
	defer g.mu.Unlock()
	s := State{Taken: g.taken, Fallback: g.fellBack, Tokens: g.tokens, Pool: len(g.slots), Events: append([]Event(nil), g.events...),
		Timings: g.timings.Snapshot(nil), Now: g.now().UnixMilli()}
	for _, sl := range g.slots {
		if sl.state != "asleep" {
			s.Awake++
		}
	}
	return s
}

// Stats are the booth's numbers for /api/stats.
func (g *Game) Stats() map[string]any {
	s := g.Snapshot()
	return map[string]any{"taken": s.Taken, "fallback": s.Fallback, "tokens": s.Tokens, "pool": s.Pool, "awake": s.Awake,
		"captionsP50Ms": s.Timings["captions"].P50, "resumeP50Ms": s.Timings["resume"].P50, "suspendP50Ms": s.Timings["suspend"].P50}
}

// Reset clears the counts and captions (admin).
func (g *Game) Reset() {
	g.mu.Lock()
	g.taken, g.fellBack, g.tokens, g.events = 0, 0, 0, nil
	g.recent = map[string][]time.Time{}
	g.mu.Unlock()
	g.hub.Poke()
}

// settle runs call, retrying with backoff while kagent is still settling the session's
// last turn, until ctx ends.
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
