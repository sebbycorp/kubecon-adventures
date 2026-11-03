// This file is the wire: one WebSocket per phone or big screen, JSON text frames.
//
// Client to server: {"t":"in","mx","my","fire"} (~20 Hz; a frame over the limit is
// applied anyway when it changes fire or whether the stick is centred, otherwise the
// newest is applied once the limit allows), {"t":"play"} and {"t":"ping","code"}.
// Unknown frames are ignored; a frame over 4 KiB closes the connection. A screen
// (?screen=1) has no ship and its frames are ignored.
//
// Server to client: {"t":"snap",...} at 15 Hz (see SnapHead and SnapCommon),
// {"t":"pong","id"}, {"t":"err","msg"}, and {"t":"full"} before closing when 48 players
// are online, or to the oldest public screen when a ninth one connects.
package defense

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"math"
	"net/http"
	"slices"
	"time"

	"github.com/coder/websocket"
	"github.com/sebbycorp/kubecon-adventures/arcade/internal/platform"
)

const (
	maxFrame     = 4096
	writeTimeout = 5 * time.Second
	inputRate    = 20.0
	inputBurst   = 2.0
	cmdRate      = 5.0
	cmdBurst     = 5.0
	boardRows    = 10
)

// allowedOrigins may open the socket besides the request's own host.
var allowedOrigins = []string{"kubecon.agentpwned.ai"}

// SnapHead is the per-receiver part of a snapshot. Score is this round's points, Best
// the best round this session, Rank the all-time place (0: unranked) and Next the
// player one place above (whose Best to beat). The *Left fields are seconds.
type SnapHead struct {
	T          string    `json:"t"`
	Me         string    `json:"me"`
	Score      int       `json:"score,omitempty"`
	Suspends   int       `json:"suspends,omitempty"`
	Best       int       `json:"best,omitempty"`
	Total      int       `json:"total,omitempty"`
	Title      string    `json:"title,omitempty"`
	Combo      int       `json:"combo,omitempty"`
	ComboLeft  float64   `json:"comboLeft,omitempty"`
	RapidLeft  float64   `json:"rapidLeft,omitempty"`
	SpreadLeft float64   `json:"spreadLeft,omitempty"`
	ShieldLeft float64   `json:"shieldLeft,omitempty"`
	Rank       int       `json:"rank,omitempty"`
	Next       *NextView `json:"next,omitempty"`
}

// NextView is the player to beat for the next all-time place.
type NextView struct {
	Nick string `json:"nick"`
	Best int    `json:"best"`
	Rank int    `json:"rank"`
}

// SnapCommon is the part every receiver shares.
type SnapCommon struct {
	Now          float64          `json:"now"`
	Wave         WaveView         `json:"wave"`
	TeamStreak   int              `json:"teamStreak"`
	BestStreak   int              `json:"bestStreak"`
	Alarm        bool             `json:"alarm"`
	Powerups     []PowerView      `json:"powerups"`
	AllTime      []ScoreView      `json:"allTime"`
	Arena        [2]float64       `json:"arena"`
	Cluster      [2]float64       `json:"cluster"`
	Storage      [2]float64       `json:"storage"`
	Round        RoundView        `json:"round"`
	Record       float64          `json:"record"` // longest round survived, seconds
	Ships        []ShipView       `json:"ships"`
	Agents       []AgentView      `json:"agents"`
	Bullets      []BulletView     `json:"bullets"`
	Events       []EventView      `json:"events"`
	Board        []ScoreView      `json:"board"`
	Fill         FillView         `json:"fill"`
	Substrate    SubstrateView    `json:"substrate"`
	Timings      map[string]int64 `json:"timings"`
	TokensPerMin int64            `json:"tokensPerMin"`
	Paused       bool             `json:"paused"`
}

type WaveView struct {
	N    int    `json:"n"`
	Name string `json:"name"`
	Boss bool   `json:"boss"`
	Left int    `json:"left"` // agents of the wave still to spawn
}

type PowerView struct {
	ID   string  `json:"id"`
	Kind string  `json:"kind"`
	X    float64 `json:"x"`
	Y    float64 `json:"y"`
	TTL  float64 `json:"ttl"`
}

type RoundView struct {
	N         int     `json:"n"`
	Phase     string  `json:"phase"`
	Started   float64 `json:"started"`
	Ended     float64 `json:"ended"`
	Suspended int     `json:"suspended"`
	Spawned   int     `json:"spawned"`
	Peak      int     `json:"peak"`
	Hosted    int     `json:"hosted"` // peak sessions hosted at once, running and suspended
	Reason    string  `json:"reason"`
	Survived  float64 `json:"survived"`
	Next      float64 `json:"next"` // seconds until the next round starts on its own (over)
	CanPlay   bool    `json:"canPlay"`
}

type ShipView struct {
	ID     string  `json:"id"`
	Nick   string  `json:"nick"`
	Color  string  `json:"color"`
	X      float64 `json:"x"`
	Y      float64 `json:"y"`
	VX     float64 `json:"vx"`
	VY     float64 `json:"vy"`
	H      float64 `json:"h"`
	Online bool    `json:"online"`
	Score  int     `json:"score"`
	Combo  int     `json:"combo"`
	Shield bool    `json:"shield,omitempty"`
	Stun   bool    `json:"stun,omitempty"`
	Rapid  bool    `json:"rapid,omitempty"`
	Spread bool    `json:"spread,omitempty"`
	Title  string  `json:"title"`
}

type AgentView struct {
	ID      string  `json:"id"`
	Name    string  `json:"name"`
	Role    string  `json:"role"`
	Kind    string  `json:"kind"`
	Need    int     `json:"need"`
	X       float64 `json:"x"`
	Y       float64 `json:"y"`
	State   string  `json:"state"`
	Hits    int     `json:"hits"`
	Say     string  `json:"say,omitempty"`
	Done    bool    `json:"done"`
	Arrived bool    `json:"arrived"`
}

type BulletView struct {
	ID    int     `json:"id"`
	X     float64 `json:"x"`
	Y     float64 `json:"y"`
	VX    float64 `json:"vx"`
	VY    float64 `json:"vy"`
	Owner string  `json:"owner"`
}

type EventView struct {
	Kind   string `json:"kind"`
	Agent  string `json:"agent,omitempty"`
	Player string `json:"player,omitempty"`
	Text   string `json:"text,omitempty"`
	Ms     int64  `json:"ms,omitempty"`
	Points int    `json:"points,omitempty"` // suspended: points scored; wave: its number; squad: bonus each
	Combo  int    `json:"combo,omitempty"`  // suspended: the multiplier; wave: its agents
}

type ScoreView struct {
	ID       string `json:"id"`
	Nick     string `json:"nick"`
	Color    string `json:"color"`
	Score    int    `json:"score"`
	Suspends int    `json:"suspends"`
	Best     int    `json:"best"`
	Total    int    `json:"total"`
	Title    string `json:"title"`
}

// FillView is the cluster's load: running agents against the pool's workers.
type FillView struct {
	Running  int `json:"running"`
	Slots    int `json:"slots"`
	Spawning int `json:"spawning"`
	Hosted   int `json:"hosted"` // running plus suspended sessions this round
	Stored   int `json:"stored"`
}

// SubstrateView is what Substrate itself reports, as a cross-check of the game's count.
type SubstrateView struct {
	Awake       int   `json:"awake"`     // the subagent Agent's running actors
	Suspended   int   `json:"suspended"` // ... and suspended ones
	Workers     int   `json:"workers"`   // the defense pool's registered workers
	BusyWorkers int64 `json:"busyWorkers"`
	Actors      int64 `json:"actors"`
}

func r2(f float64) float64 { return math.Round(f*100) / 100 }

// commonLocked builds the shared part of a snapshot. g.mu is held.
func (g *Game) commonLocked(ev []Event) SnapCommon {
	w := g.w
	r := w.Round
	c := SnapCommon{Now: r2(w.Now), Arena: [2]float64{ArenaW, ArenaH}, Cluster: [2]float64{Cluster.X, Cluster.Y},
		Storage: [2]float64{Storage.X, Storage.Y}, Record: r2(w.Best), Ships: []ShipView{}, Agents: []AgentView{},
		Bullets: []BulletView{}, Events: []EventView{}, Board: []ScoreView{}, Powerups: []PowerView{}, Timings: map[string]int64{},
		TokensPerMin: g.tokensPerMin(), Paused: g.paused}
	rv := RoundView{N: r.N, Phase: string(r.Phase), Started: r2(r.Started), Ended: r2(r.Ended), Suspended: r.Suspended,
		Spawned: r.Spawned, Peak: r.Peak, Hosted: r.PeakHosted, Reason: r.Reason, CanPlay: !g.listing && !g.paused}
	switch r.Phase {
	case Live:
		rv.Survived = r2(w.Now - r.Started)
	case Over:
		rv.Survived = r2(r.Ended - r.Started)
		rv.Next = r2(max(0, nextRound-(w.Now-r.Ended)))
		rv.CanPlay = rv.CanPlay && w.Now-r.Ended >= overMin
	}
	c.Round = rv
	for _, id := range slices.Sorted(maps.Keys(w.Ships)) {
		s := w.Ships[id]
		c.Ships = append(c.Ships, ShipView{s.ID, s.Nick, s.Color, r2(s.Pos.X), r2(s.Pos.Y), r2(s.Vel.X), r2(s.Vel.Y), r2(s.Heading), s.Online, s.Score,
			s.Combo, w.Now < s.ShieldUntil, w.Now < s.StunUntil, w.Now < s.RapidUntil, w.Now < s.SpreadUntil, Title(s.Total)})
	}
	for _, id := range slices.Sorted(maps.Keys(w.Agents)) {
		a := w.Agents[id]
		c.Agents = append(c.Agents, AgentView{a.ID, a.Name, a.Role, a.Kind, a.Need, r2(a.Pos.X), r2(a.Pos.Y), string(a.State), a.Hits, a.Say, a.Done, a.Arrived})
	}
	for _, b := range w.Bullets {
		c.Bullets = append(c.Bullets, BulletView{b.ID, r2(b.Pos.X), r2(b.Pos.Y), r2(b.Vel.X), r2(b.Vel.Y), b.Owner})
	}
	for _, e := range ev {
		c.Events = append(c.Events, EventView{e.Kind, e.Agent, e.Player, e.Text, e.Ms, e.Points, e.Combo})
	}
	for _, id := range slices.Sorted(maps.Keys(w.Powerups)) {
		p := w.Powerups[id]
		c.Powerups = append(c.Powerups, PowerView{p.ID, p.Kind, r2(p.Pos.X), r2(p.Pos.Y), r2(p.Expires - w.Now)})
	}
	round, allTime := g.boards()
	c.Board = round[:min(len(round), boardRows)]
	c.AllTime = allTime[:min(len(allTime), boardRows)]
	c.Wave = WaveView{r.Wave, r.WaveName, r.Boss, len(r.queue)}
	c.TeamStreak, c.BestStreak, c.Alarm = w.TeamStreak, w.BestStreak, w.alarm
	c.Fill = FillView{Running: w.Running(), Slots: w.Slots, Spawning: w.Spawning(), Hosted: w.Hosted(), Stored: len(w.Stored)}
	if g.p != nil {
		sub := g.p.Substrate()
		c.Substrate = SubstrateView{BusyWorkers: sub.BusyWorkers, Actors: sub.Actors}
		for _, ps := range sub.Pools {
			if ps.Name == PoolName {
				c.Substrate.Workers = ps.Workers
			}
		}
		for _, as := range sub.Agents {
			if as.Name == AgentName {
				c.Substrate.Awake, c.Substrate.Suspended = as.Awake, as.Suspended
			}
		}
		if t, ok := g.p.Timings.Snapshot(nil)["defense_suspend"]; ok {
			c.Timings["suspendP50"], c.Timings["suspendP95"] = t.P50, t.P95
		}
	}
	return c
}

// boards ranks ships by points this round, and all-time by best round (then suspends
// ever, then nick). g.mu is held.
func (g *Game) boards() (round, allTime []ScoreView) {
	for _, s := range g.w.Ships {
		v := ScoreView{s.ID, s.Nick, s.Color, s.Score, s.Suspends, s.Best, s.Total, Title(s.Total)}
		if s.Score > 0 || s.Online {
			round = append(round, v)
		}
		if s.Best > 0 {
			allTime = append(allTime, v)
		}
	}
	slices.SortFunc(round, func(a, b ScoreView) int {
		return cmp.Or(cmp.Compare(b.Score, a.Score), cmp.Compare(b.Suspends, a.Suspends), cmp.Compare(a.Nick, b.Nick), cmp.Compare(a.ID, b.ID))
	})
	slices.SortFunc(allTime, func(a, b ScoreView) int {
		return cmp.Or(cmp.Compare(b.Best, a.Best), cmp.Compare(b.Total, a.Total), cmp.Compare(a.Nick, b.Nick), cmp.Compare(a.ID, b.ID))
	})
	return round, allTime
}

// frames is one broadcast: the shared part and each receiver's head.
type frames struct {
	common SnapCommon
	heads  map[*client]SnapHead
}

func (g *Game) framesLocked(ev []Event) frames {
	f := frames{common: g.commonLocked(ev), heads: map[*client]SnapHead{}}
	_, allTime := g.boards()
	rank := map[string]int{}
	for i, row := range allTime {
		rank[row.ID] = i + 1
	}
	now := g.w.Now
	left := func(until float64) float64 { return r2(max(0, until-now)) }
	for c := range g.clients {
		h := SnapHead{T: "snap", Me: c.player}
		if s := g.w.Ships[c.player]; s != nil && !c.screen {
			h.Score, h.Suspends, h.Best, h.Total, h.Title = s.Score, s.Suspends, s.Best, s.Total, Title(s.Total)
			h.Combo, h.RapidLeft, h.SpreadLeft, h.ShieldLeft = s.Combo, left(s.RapidUntil), left(s.SpreadUntil), left(s.ShieldUntil)
			if s.Combo > 1 {
				h.ComboLeft = left(s.LastKill + comboWindow)
			}
			h.Rank = rank[c.player]
			// The player to beat: one place up, or the last place when unranked.
			up := len(allTime) - 1
			if h.Rank > 0 {
				up = h.Rank - 2
			}
			if up >= 0 && up < len(allTime) && allTime[up].ID != s.ID {
				h.Next = &NextView{allTime[up].Nick, allTime[up].Best, up + 1}
			}
		}
		f.heads[c] = h
	}
	return f
}

func (f frames) send() {
	if len(f.heads) == 0 {
		return
	}
	common, err := json.Marshal(f.common)
	if err != nil {
		return
	}
	for c, h := range f.heads {
		head, _ := json.Marshal(h)
		b := make([]byte, 0, len(head)+len(common))
		b = append(append(append(b, head[:len(head)-1]...), ','), common[1:]...)
		c.push(b)
	}
}

// client is one WebSocket connection.
type client struct {
	pl     platform.Player
	player string // "" for a screen
	screen bool
	public bool // reached through the tunnel (has cf-connecting-ip)
	seq    uint64
	snaps  chan []byte
	ctrl   chan []byte
	kick   chan struct{}
	cancel context.CancelFunc

	in      bucket // g.mu
	cmd     bucket // reader goroutine only
	lastIn  Input
	applied bool
}

type bucket struct {
	tokens float64
	last   time.Time
}

func (b *bucket) allow(now time.Time, rate, burst float64) bool {
	if b.last.IsZero() {
		b.tokens = burst
	} else {
		b.tokens = min(burst, b.tokens+now.Sub(b.last).Seconds()*rate)
	}
	b.last = now
	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}

func (c *client) push(b []byte) {
	for {
		select {
		case c.snaps <- b:
			return
		default:
		}
		select {
		case <-c.snaps:
		default:
		}
	}
}

func (c *client) reply(v any) {
	b, _ := json.Marshal(v)
	select {
	case c.ctrl <- b:
	default:
		c.cancel()
	}
}

func (c *client) kickFull() {
	select {
	case c.kick <- struct{}{}:
	default:
	}
}

func (c *client) writeLoop(ctx context.Context, conn *websocket.Conn) {
	for {
		var b []byte
		select {
		case <-ctx.Done():
			return
		case <-c.kick:
			wctx, cancel := context.WithTimeout(ctx, time.Second)
			_ = conn.Write(wctx, websocket.MessageText, []byte(`{"t":"full"}`))
			cancel()
			conn.Close(websocket.StatusTryAgainLater, "space full")
			c.cancel()
			return
		case b = <-c.ctrl:
		case b = <-c.snaps:
		}
		wctx, cancel := context.WithTimeout(ctx, writeTimeout)
		err := conn.Write(wctx, websocket.MessageText, b)
		cancel()
		if err != nil {
			c.cancel()
			return
		}
	}
}

// serveWS is GET /cluster-defense/ws.
func (g *Game) serveWS(w http.ResponseWriter, r *http.Request) {
	screen := r.URL.Query().Get("screen") == "1"
	var pl platform.Player
	if !screen {
		var ok bool
		if pl, ok = g.p.Players.FromRequest(r); !ok {
			platform.HTTPError(w, http.StatusUnauthorized, "join first")
			return
		}
	}
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{OriginPatterns: allowedOrigins})
	if err != nil {
		return
	}
	defer conn.CloseNow()
	conn.SetReadLimit(maxFrame)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	c := &client{pl: pl, player: pl.ID, screen: screen, public: r.Header.Get("cf-connecting-ip") != "",
		snaps: make(chan []byte, 2), ctrl: make(chan []byte, 16), kick: make(chan struct{}, 1), cancel: cancel}
	if !g.attach(c) {
		wctx, wc := context.WithTimeout(ctx, time.Second)
		_ = conn.Write(wctx, websocket.MessageText, []byte(`{"t":"full"}`))
		wc()
		conn.Close(websocket.StatusTryAgainLater, "space full")
		return
	}
	wdone := make(chan struct{})
	go func() { defer close(wdone); c.writeLoop(ctx, conn) }()
	for {
		typ, b, err := conn.Read(ctx)
		if err != nil {
			break
		}
		if typ == websocket.MessageText && !screen {
			g.handle(c, b)
		}
	}
	cancel()
	<-wdone
	g.detach(c)
}

// attach registers c, bringing its player online. A public screen beyond maxScreens
// evicts the oldest public screen; LAN screens are not counted.
func (g *Game) attach(c *client) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	select {
	case <-g.done:
		return false
	default:
	}
	switch {
	case c.screen && c.public:
		var public []*client
		for o := range g.clients {
			if o.screen && o.public {
				public = append(public, o)
			}
		}
		if len(public) >= maxScreens {
			old := slices.MinFunc(public, func(a, b *client) int { return cmp.Compare(a.seq, b.seq) })
			delete(g.clients, old)
			old.kickFull()
		}
	case c.screen:
	case !g.online(c.pl):
		return false
	default:
		g.conns[c.player]++
	}
	g.seq++
	c.seq = g.seq
	g.clients[c] = true
	return true
}

func (g *Game) detach(c *client) {
	g.mu.Lock()
	delete(g.pendIn, c)
	delete(g.clients, c)
	g.mu.Unlock()
	if !c.screen {
		g.leave(c.player)
	}
}

// handle applies one client frame.
func (g *Game) handle(c *client, b []byte) {
	var m struct {
		T      string
		MX, MY float64
		Fire   bool
		Code   string
	}
	if json.Unmarshal(b, &m) != nil {
		return
	}
	switch m.T {
	case "play", "ping":
		if !c.cmd.allow(time.Now(), cmdRate, cmdBurst) {
			return
		}
	}
	if m.T == "ping" {
		c.reply(map[string]string{"t": "pong", "id": m.Code})
		return
	}
	var err error
	g.mu.Lock()
	if !g.wake(c) {
		g.mu.Unlock()
		c.kickFull()
		return
	}
	switch m.T {
	case "in":
		g.input(c, Input{Move: Vec{m.MX, m.MY}, Fire: m.Fire}, time.Now())
	case "play":
		err = g.play()
	}
	g.mu.Unlock()
	if err != nil {
		c.reply(map[string]string{"t": "err", "msg": err.Error()})
	}
}

// input applies a frame within the rate limit. A frame that changes fire or whether the
// stick is centred is never dropped (a lost release would hold FIRE); any other frame
// over the limit is kept as pending, replaced by newer ones. g.mu is held.
func (g *Game) input(c *client, in Input, now time.Time) {
	last := c.lastIn
	edge := !c.applied || in.Fire != last.Fire || (in.Move == Vec{}) != (last.Move == Vec{})
	if c.in.allow(now, inputRate, inputBurst) || edge {
		g.applyInput(c, in)
		return
	}
	g.pendIn[c] = in
}

func (g *Game) applyInput(c *client, in Input) {
	g.w.Input(c.player, in)
	c.lastIn, c.applied = in, true
	delete(g.pendIn, c)
}

func (g *Game) flushInputs(now time.Time) {
	for c, in := range g.pendIn {
		if c.in.allow(now, inputRate, inputBurst) {
			g.applyInput(c, in)
		}
	}
}

// Top is the all-time leaderboard for the arcade's /api/boards: the n best round scores.
func (g *Game) Top(n int) []platform.BoardRow {
	g.mu.Lock()
	defer g.mu.Unlock()
	_, all := g.boards()
	out := []platform.BoardRow{}
	for _, v := range all[:min(n, len(all))] {
		out = append(out, platform.BoardRow{Nick: v.Nick, Color: v.Color, Score: v.Best, Detail: fmt.Sprintf("%s · %d suspends", v.Title, v.Total)})
	}
	return out
}
