// Package fork is Fork Frenzy: a player teaches FORKY secrets, checkpoints it, and forks
// clones from the checkpoint. Clones share everything up to the checkpoint and nothing
// after it. Every original and clone is its own kagent session and Substrate actor.
package fork

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

// Personas the clones take on after forking (one per clone, in order).
var Personas = []string{"pirate", "chef", "robot", "wizard", "astronaut", "ninja"}

const (
	maxClones    = 3
	maxSecretLen = 60
)

type Creature struct {
	ID        string        `json:"id"`
	Name      string        `json:"name"` // FORKY, CLONE 1..3
	Persona   string        `json:"persona,omitempty"`
	SessionID string        `json:"sessionId"`
	Actor     *kagent.Actor `json:"actor,omitempty"`
	Say       string        `json:"say,omitempty"`
	Secrets   []string      `json:"secrets"`
	Busy      bool          `json:"busy"`
	ForkMs    int64         `json:"forkMs,omitempty"`
	lastTask  string
}

type Family struct {
	ID           string      `json:"id"`
	PlayerID     string      `json:"playerId"`
	Nick         string      `json:"nick"`
	Color        string      `json:"color"`
	Phase        string      `json:"phase"` // new, taught, checkpointed, forked
	Original     *Creature   `json:"original"`
	CheckpointID string      `json:"checkpointId,omitempty"`
	CheckpointMs int64       `json:"checkpointMs,omitempty"`
	Clones       []*Creature `json:"clones"`
	Busy         bool        `json:"busy"`
	Error        string      `json:"error,omitempty"`
	Updated      int64       `json:"updated"`
}

type Event struct {
	Type     string `json:"type"` // teach, checkpoint, fork, say
	FamilyID string `json:"familyId"`
	Nick     string `json:"nick"`
	Creature string `json:"creature,omitempty"`
	Ms       int64  `json:"ms,omitempty"`
	Text     string `json:"text,omitempty"`
	At       int64  `json:"at"`
}

type Game struct {
	mu       sync.Mutex
	p        *platform.Platform
	k        *kagent.Kagent
	families map[string]*Family // by player ID
	events   []Event
	forks    int
	timings  *platform.Timings // checkpoint, fork, reply
	hub      *platform.Hub
}

func New(p *platform.Platform, k *kagent.Kagent) *Game {
	g := &Game{p: p, k: k, families: map[string]*Family{}, timings: platform.NewTimings(), hub: platform.NewHub()}
	p.OnChange(g.hub.Poke)
	return g
}

func (g *Game) Run(ctx context.Context) {
	go g.hub.Run(ctx, func() any { return g.Snapshot() })
	go g.runActorPoller(ctx)
}

var blockedWords = []string{"fuck", "shit", "cunt", "nigg", "fag", "bitch", "porn", "nazi", "hitler", "rape", "dick", "cock", "pussy"}

func cleanSecret(s string) (string, error) {
	s = strings.Join(strings.Fields(s), " ")
	if s == "" {
		return "", errors.New("type a secret")
	}
	if len([]rune(s)) > maxSecretLen {
		return "", fmt.Errorf("keep it under %d characters", maxSecretLen)
	}
	lower := strings.ToLower(s)
	for _, b := range blockedWords {
		if strings.Contains(lower, b) {
			return "", errors.New("keep it family-friendly")
		}
	}
	return s, nil
}

// family returns the player's family, creating it, and marks it busy. Caller must call done.
func (g *Game) claim(pl platform.Player, phases ...string) (*Family, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	f, ok := g.families[pl.ID]
	if !ok {
		f = &Family{ID: platform.NewID(), PlayerID: pl.ID, Nick: pl.Nick, Color: pl.Color, Phase: "new",
			Original: &Creature{ID: platform.NewID(), Name: "FORKY", Secrets: []string{}}, Clones: []*Creature{}}
		g.families[pl.ID] = f
	}
	if f.Busy {
		return nil, errors.New("FORKY is busy, one moment")
	}
	if len(phases) > 0 && !slices.Contains(phases, f.Phase) {
		return nil, fmt.Errorf("not now (phase %s)", f.Phase)
	}
	f.Busy, f.Error = true, ""
	g.hub.Poke()
	return f, nil
}

func (g *Game) done(f *Family, err error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	f.Busy, f.Updated = false, time.Now().UnixMilli()
	if err != nil {
		f.Error = err.Error()
		slog.Error("fork frenzy", "family", f.ID, "err", err)
	}
	g.hub.Poke()
}

// talk prompts one creature and records what it says and knows.
func (g *Game) talk(ctx context.Context, f *Family, c *Creature, text string) error {
	g.mu.Lock()
	c.Busy = true
	sid := c.SessionID
	g.mu.Unlock()
	t0 := time.Now()
	reply, err := g.k.Prompt(ctx, sid, text)
	ms := time.Since(t0).Milliseconds()
	var out struct {
		Say     string   `json:"say"`
		Secrets []string `json:"secrets"`
	}
	if err == nil {
		err = reply.Decode(&out)
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	c.Busy = false
	if err != nil {
		return err
	}
	g.timings.Record("reply", ms)
	c.Say, c.Secrets, c.lastTask = out.Say, out.Secrets, string(reply.Task.ID)
	if c.Secrets == nil {
		c.Secrets = []string{}
	}
	g.emitLocked(Event{Type: "say", FamilyID: f.ID, Nick: f.Nick, Creature: c.ID, Text: c.Say, Ms: ms})
	return nil
}

// Teach tells FORKY (before forking) or one clone (after forking) a secret.
func (g *Game) Teach(pl platform.Player, creatureID, secret string) error {
	secret, err := cleanSecret(secret)
	if err != nil {
		return err
	}
	f, err := g.claim(pl)
	if err != nil {
		return err
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		err := func() error {
			g.mu.Lock()
			phase, c := f.Phase, f.Original
			if phase == "forked" {
				c = f.creature(creatureID)
			}
			g.mu.Unlock()
			switch {
			case phase == "forked" && (c == nil || c == f.Original):
				return errors.New("pick a clone to teach")
			case phase == "checkpointed":
				return errors.New("fork your clones first")
			}
			if c.SessionID == "" {
				sid, err := g.k.CreateSession(ctx, "forky-"+f.Nick)
				if err != nil {
					return err
				}
				g.mu.Lock()
				c.SessionID = sid
				g.mu.Unlock()
			}
			if err := g.talk(ctx, f, c, "Remember this secret: "+secret); err != nil {
				return err
			}
			g.mu.Lock()
			if f.Phase == "new" {
				f.Phase = "taught"
			}
			g.emitLocked(Event{Type: "teach", FamilyID: f.ID, Nick: f.Nick, Creature: c.ID, Text: secret})
			g.mu.Unlock()
			return nil
		}()
		g.done(f, err)
	}()
	return nil
}

// Checkpoint saves FORKY's latest turn as a forkable checkpoint.
func (g *Game) Checkpoint(pl platform.Player) error {
	f, err := g.claim(pl, "taught")
	if err != nil {
		return err
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		t0 := time.Now()
		cp, err := g.k.Checkpoint(ctx, f.Original.SessionID, f.Original.lastTask)
		ms := time.Since(t0).Milliseconds()
		if err == nil {
			g.timings.Record("checkpoint", ms)
			g.mu.Lock()
			f.CheckpointID, f.CheckpointMs, f.Phase = cp, ms, "checkpointed"
			g.emitLocked(Event{Type: "checkpoint", FamilyID: f.ID, Nick: f.Nick, Ms: ms})
			g.mu.Unlock()
		}
		g.done(f, err)
	}()
	return nil
}

// Fork starts three clones from the checkpoint at once and asks each who it is now.
func (g *Game) Fork(pl platform.Player) error {
	f, err := g.claim(pl, "checkpointed")
	if err != nil {
		return err
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
		defer cancel()
		clones := make([]*Creature, maxClones)
		start := int(time.Now().UnixNano()) % len(Personas)
		for i := range clones {
			clones[i] = &Creature{ID: platform.NewID(), Name: fmt.Sprintf("CLONE %d", i+1), Persona: Personas[(start+i)%len(Personas)], Secrets: []string{}, Busy: true}
		}
		g.mu.Lock()
		f.Clones, f.Phase = clones, "forked"
		g.mu.Unlock()
		var wg sync.WaitGroup
		errs := make([]error, len(clones))
		for i, c := range clones {
			wg.Add(1)
			go func() {
				defer wg.Done()
				t0 := time.Now()
				sid, err := g.k.Fork(ctx, f.CheckpointID, c.ID)
				ms := time.Since(t0).Milliseconds()
				if err != nil {
					errs[i] = err
					return
				}
				g.timings.Record("fork", ms)
				g.mu.Lock()
				c.SessionID, c.ForkMs = sid, ms
				g.forks++
				g.emitLocked(Event{Type: "fork", FamilyID: f.ID, Nick: f.Nick, Creature: c.ID, Ms: ms})
				g.mu.Unlock()
				errs[i] = g.talk(ctx, f, c, "You are a clone now. From now on you are a "+c.Persona+
					". Introduce yourself in character and tell me the secrets you remember.")
			}()
		}
		wg.Wait()
		g.mu.Lock()
		for _, c := range clones {
			c.Busy = false
		}
		g.mu.Unlock()
		g.done(f, errors.Join(errs...))
	}()
	return nil
}

// AskAll asks the original and every clone what they know, to show the memory split.
func (g *Game) AskAll(pl platform.Player) error {
	f, err := g.claim(pl, "forked")
	if err != nil {
		return err
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		var wg sync.WaitGroup
		errs := make([]error, 0, 4)
		var mu sync.Mutex
		for _, c := range append([]*Creature{f.Original}, f.Clones...) {
			if c.SessionID == "" {
				continue
			}
			wg.Add(1)
			go func() {
				defer wg.Done()
				if err := g.talk(ctx, f, c, "Quick roll call: which secrets do you know right now?"); err != nil {
					mu.Lock()
					errs = append(errs, err)
					mu.Unlock()
				}
			}()
		}
		wg.Wait()
		g.done(f, errors.Join(errs...))
	}()
	return nil
}

// Restart deletes the player's family sessions so they can start over.
func (g *Game) Restart(pl platform.Player) error {
	f, err := g.claim(pl)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	for _, c := range append([]*Creature{f.Original}, f.Clones...) {
		if c.SessionID != "" {
			_ = g.k.Delete(ctx, c.SessionID)
		}
	}
	g.mu.Lock()
	delete(g.families, pl.ID)
	g.mu.Unlock()
	g.hub.Poke()
	return nil
}

// Reset deletes every FORKY session and clears the round.
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
	g.families, g.events, g.forks = map[string]*Family{}, nil, 0
	g.mu.Unlock()
	g.hub.Poke()
	return len(ids), nil
}

func (f *Family) creature(id string) *Creature {
	if f.Original.ID == id {
		return f.Original
	}
	for _, c := range f.Clones {
		if c.ID == id {
			return c
		}
	}
	return nil
}

func (g *Game) emitLocked(e Event) {
	e.At = time.Now().UnixMilli()
	g.events = append(g.events, e)
	if n := len(g.events); n > 80 {
		g.events = g.events[n-80:]
	}
	g.hub.Poke()
}

func (g *Game) runActorPoller(ctx context.Context) {
	for {
		c, cancel := context.WithTimeout(ctx, 3*time.Second)
		states, err := g.k.ActorStates(c)
		cancel()
		if err == nil {
			g.mu.Lock()
			changed := false
			for _, f := range g.families {
				for _, cr := range append([]*Creature{f.Original}, f.Clones...) {
					if a, ok := states[cr.SessionID]; ok && (cr.Actor == nil || *cr.Actor != a) {
						cp := a
						cr.Actor, changed = &cp, true
					}
				}
			}
			g.mu.Unlock()
			if changed {
				g.hub.Poke()
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(500 * time.Millisecond):
		}
	}
}

type State struct {
	Families  []Family                        `json:"families"`
	Forks     int                             `json:"forks"`
	Timings   map[string]platform.Percentiles `json:"timings"`
	Substrate kagent.SubstrateStats           `json:"substrate"`
	Events    []Event                         `json:"events"`
	Now       int64                           `json:"now"`
}

func (g *Game) Snapshot() State {
	g.mu.Lock()
	defer g.mu.Unlock()
	s := State{Forks: g.forks, Substrate: g.p.Substrate(), Events: slices.Clone(g.events), Now: time.Now().UnixMilli()}
	s.Timings = g.timings.Snapshot(g.p.Timings.Snapshot(nil))
	for _, f := range g.families {
		cp := *f
		o := *f.Original
		o.Secrets = slices.Clone(o.Secrets)
		cp.Original = &o
		cp.Clones = make([]*Creature, len(f.Clones))
		for i, c := range f.Clones {
			cc := *c
			cc.Secrets = slices.Clone(c.Secrets)
			cp.Clones[i] = &cc
		}
		s.Families = append(s.Families, cp)
	}
	sort.Slice(s.Families, func(i, j int) bool { return s.Families[i].Updated > s.Families[j].Updated })
	return s
}

func (g *Game) Stats() map[string]any {
	s := g.Snapshot()
	return map[string]any{"families": len(s.Families), "forks": s.Forks,
		"checkpointP50Ms": s.Timings["checkpoint"].P50, "forkP50Ms": s.Timings["fork"].P50}
}
