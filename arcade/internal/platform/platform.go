// Package platform holds what every arcade game shares: players, Agent Substrate stats,
// Substrate's own snapshot/restore timings, live-update hubs, and HTTP helpers.
package platform

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/sebbycorp/kubecon-adventures/arcade/internal/kagent"
)

// Platform is the arcade-wide state shared by all games.
type Platform struct {
	K       *kagent.Kagent // control-plane client for arcade-wide (non-agent) calls
	Timings *Timings       // "snapshot" and "restore" from atelet
	Players *Players

	mu    sync.Mutex
	sub   kagent.SubstrateStats
	hooks []func() // called when shared stats change (games poke their hubs)
}

func New(k *kagent.Kagent) *Platform {
	return &Platform{K: k, Timings: NewTimings(), Players: NewPlayers()}
}

// OnChange registers a callback for when shared stats change.
func (p *Platform) OnChange(f func()) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.hooks = append(p.hooks, f)
}

func (p *Platform) changed() {
	p.mu.Lock()
	hooks := slices.Clone(p.hooks)
	p.mu.Unlock()
	for _, f := range hooks {
		f()
	}
}

// Substrate is the latest arcade-wide Agent Substrate summary (all pools, all agents).
func (p *Platform) Substrate() kagent.SubstrateStats {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.sub
}

// RunSubstratePoller refreshes the Substrate summary every 2 s.
func (p *Platform) RunSubstratePoller(ctx context.Context) {
	for {
		c, cancel := context.WithTimeout(ctx, 5*time.Second)
		s, err := p.K.Substrate(c)
		cancel()
		if err != nil {
			s.Error = err.Error()
		}
		p.mu.Lock()
		p.sub = s
		p.mu.Unlock()
		p.changed()
		select {
		case <-ctx.Done():
			return
		case <-time.After(2 * time.Second):
		}
	}
}

// ---- timings ----------------------------------------------------------------

type Percentiles struct {
	Count int   `json:"count"`
	Last  int64 `json:"last"`
	P50   int64 `json:"p50"`
	P95   int64 `json:"p95"`
}

// Timings keeps the last 1000 observations of each kind of operation, in milliseconds.
type Timings struct {
	mu sync.Mutex
	m  map[string][]int64
}

func NewTimings() *Timings { return &Timings{m: map[string][]int64{}} }

func (t *Timings) Record(kind string, ms int64) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.m[kind] = append(t.m[kind], ms)
	if n := len(t.m[kind]); n > 1000 {
		t.m[kind] = t.m[kind][n-1000:]
	}
}

// Typical is the median of recent observations, or 0 if there are none.
func (t *Timings) Typical(kind string) int64 {
	t.mu.Lock()
	defer t.mu.Unlock()
	v := slices.Clone(t.m[kind])
	if len(v) == 0 {
		return 0
	}
	slices.Sort(v)
	return v[len(v)/2]
}

// Snapshot returns percentiles for every kind, merged into into.
func (t *Timings) Snapshot(into map[string]Percentiles) map[string]Percentiles {
	if into == nil {
		into = map[string]Percentiles{}
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	for k, v := range t.m {
		if len(v) == 0 {
			continue
		}
		sorted := slices.Clone(v)
		slices.Sort(sorted)
		pct := func(p float64) int64 { return sorted[int(p*float64(len(sorted)-1))] }
		into[k] = Percentiles{Count: len(v), Last: v[len(v)-1], P50: pct(0.5), P95: pct(0.95)}
	}
	return into
}

func (t *Timings) Reset() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.m = map[string][]int64{}
}

// ---- players ----------------------------------------------------------------

// Player is someone who joined the arcade once (by QR code) and can play every game.
type Player struct {
	ID    string `json:"id"`
	Nick  string `json:"nick"`
	Color string `json:"color"`
}

var colors = []string{"green", "blue", "red", "purple", "gold", "pink"}

const PlayerCookie = "arcade_player"

type Players struct {
	mu sync.Mutex
	m  map[string]Player
}

func NewPlayers() *Players { return &Players{m: map[string]Player{}} }

func (ps *Players) Join(nick string) Player {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	p := Player{ID: NewID(), Nick: nick, Color: colors[len(ps.m)%len(colors)]}
	ps.m[p.ID] = p
	return p
}

func (ps *Players) Get(id string) (Player, bool) {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	p, ok := ps.m[id]
	return p, ok
}

func (ps *Players) Count() int {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	return len(ps.m)
}

// FromRequest returns the player whose cookie is on the request.
func (ps *Players) FromRequest(r *http.Request) (Player, bool) {
	c, err := r.Cookie(PlayerCookie)
	if err != nil {
		return Player{}, false
	}
	return ps.Get(c.Value)
}

// WithPlayer wraps a handler that needs a joined player.
func (ps *Players) WithPlayer(h func(http.ResponseWriter, *http.Request, Player)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		p, ok := ps.FromRequest(r)
		if !ok {
			HTTPError(w, http.StatusUnauthorized, "join first")
			return
		}
		h(w, r, p)
	}
}

var nickRE = regexp.MustCompile(`^[\p{L}\p{N} _-]{2,16}$`)

// blocked keeps the obvious stuff off the big screens.
var blocked = []string{"fuck", "shit", "cunt", "nigg", "fag", "bitch", "porn", "nazi", "hitler", "rape", "dick", "cock", "pussy"}

// CleanNick validates a nickname: 2-16 letters, digits, spaces, - or _, nothing rude.
func CleanNick(s string) (string, bool) {
	s = strings.Join(strings.Fields(s), " ")
	if !nickRE.MatchString(s) {
		return "", false
	}
	lower := strings.ToLower(strings.NewReplacer("0", "o", "1", "i", "3", "e", "4", "a", "5", "s", "$", "s").Replace(s))
	for _, b := range blocked {
		if strings.Contains(lower, b) {
			return "", false
		}
	}
	return s, true
}

// ---- helpers ----------------------------------------------------------------

func NewID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func FmtDur(ms int64) string {
	if ms >= 1000 {
		return fmt.Sprintf("%.1fs", float64(ms)/1000)
	}
	return fmt.Sprintf("%dms", ms)
}

// ErrNotYours is returned when a player acts on something they don't own (HTTP 403).
var ErrNotYours = errors.New("not yours")

// Result writes v, or maps err to an HTTP error.
func Result(w http.ResponseWriter, v any, err error) {
	switch {
	case errors.Is(err, ErrNotYours):
		HTTPError(w, http.StatusForbidden, err.Error())
	case err != nil:
		HTTPError(w, http.StatusConflict, err.Error())
	case v == nil:
		WriteJSON(w, map[string]bool{"ok": true})
	default:
		WriteJSON(w, v)
	}
}

func WriteJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func HTTPError(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

// ---- live updates -----------------------------------------------------------

// Hub pushes a game's state to every connected screen over Server-Sent Events,
// at most 10 times a second.
type Hub struct {
	mu      sync.Mutex
	clients map[chan []byte]struct{}
	poke    chan struct{}
}

func NewHub() *Hub {
	return &Hub{clients: map[chan []byte]struct{}{}, poke: make(chan struct{}, 1)}
}

func (h *Hub) Poke() {
	select {
	case h.poke <- struct{}{}:
	default:
	}
}

func (h *Hub) Run(ctx context.Context, snapshot func() any) {
	t := time.NewTicker(100 * time.Millisecond)
	defer t.Stop()
	heartbeat := time.NewTicker(5 * time.Second)
	defer heartbeat.Stop()
	dirty := true
	for {
		select {
		case <-ctx.Done():
			return
		case <-h.poke:
			dirty = true
			continue
		case <-heartbeat.C:
			dirty = true
			continue
		case <-t.C:
		}
		if !dirty {
			continue
		}
		dirty = false
		b, _ := json.Marshal(snapshot())
		h.mu.Lock()
		for c := range h.clients {
			select {
			case c <- b:
			default: // slow client; it gets the next one
			}
		}
		h.mu.Unlock()
	}
}

func (h *Hub) ServeSSE(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		HTTPError(w, http.StatusInternalServerError, "streaming unsupported")
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	c := make(chan []byte, 1)
	h.mu.Lock()
	h.clients[c] = struct{}{}
	h.mu.Unlock()
	defer func() {
		h.mu.Lock()
		delete(h.clients, c)
		h.mu.Unlock()
	}()
	h.Poke()
	for {
		select {
		case <-r.Context().Done():
			return
		case b := <-c:
			if _, err := w.Write(append(append([]byte("data: "), b...), '\n', '\n')); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}
