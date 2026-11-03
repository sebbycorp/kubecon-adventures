// This file is the wire: one WebSocket per phone or big screen, JSON text frames.
//
// Client to server: {"t":"in","mx","my","fire","act"} (~20 Hz; a frame over the limit
// is applied anyway when it changes fire, act or whether the stick is centred, and
// otherwise coalesced: the newest is applied once the limit allows), {"t":"squad","op":"solo"|"create"|"join","code"}, {"t":"start","level"},
// {"t":"build"} and {"t":"ping","code"}. Unknown or malformed frames are ignored; a
// frame over 4 KiB closes the connection. A screen spectator (?screen=1) has no avatar
// and its frames are ignored.
//
// Server to client: {"t":"snap",...} at 15 Hz (see Snap), {"t":"squad","code"},
// {"t":"pong","id"} (the answer to a ping), {"t":"err","msg"}, and {"t":"full"} before
// closing when 48 players are online in the city, or to the oldest public screen when a
// ninth public one connects (screens without cf-connecting-ip, on the booth's LAN, are
// exempt). A player who sends nothing for 10 minutes counts as offline; their next
// frame brings them back, or gets {"t":"full"} if the city filled up meanwhile. start,
// build, squad and ping frames share a limit of 5/s.
package heist

import (
	"cmp"
	"context"
	"encoding/json"
	"maps"
	"math"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/coder/websocket"
	"github.com/sebbycorp/kubecon-adventures/arcade/internal/kagent"
	"github.com/sebbycorp/kubecon-adventures/arcade/internal/platform"
)

const (
	maxFrame     = 4096
	writeTimeout = 5 * time.Second
	inputRate    = 20.0 // input frames per second
	inputBurst   = 2.0
	cmdRate      = 5.0 // start and squad frames per second
	cmdBurst     = 5.0
	boardRows    = 10
	missionShow  = 30.0 // an ended run stays in snapshots this long
)

// allowedOrigins are the browser origins (besides the request's own host, which
// coder/websocket always allows) that may open the socket: the public booth domain
// behind the Cloudflare Tunnel. Anything else is refused with 403, so another site
// cannot drive a player's avatar with their cookie.
var allowedOrigins = []string{"kubecon.agentpwned.ai"}

// SnapHead is the per-receiver part of a snapshot. Inv is only the receiving
// player's, and absent for screens. Score is the player's points in their squad's
// current (or last) run, Best their best run, and Rank their leaderboard place (0:
// unranked), which the top-10 board may not show.
type SnapHead struct {
	T     string      `json:"t"`
	Me    string      `json:"me"`
	Inv   *[]PartView `json:"inv,omitempty"`
	Score int         `json:"score"`
	Best  int         `json:"best"`
	Rank  int         `json:"rank,omitempty"`
}

// SnapCommon is the part every receiver gets. Events are the ones since the previous
// snapshot. Times are world seconds.
type SnapCommon struct {
	Now       float64          `json:"now"`
	Avatars   []AvatarView     `json:"avatars"`
	Targets   []TargetView     `json:"targets"`
	Allies    []AllyView       `json:"allies"`
	Drops     []DropView       `json:"drops"`
	Missions  []MissionView    `json:"missions"`
	Events    []EventView      `json:"events"`
	Board     []ScoreView      `json:"board"`
	Substrate SubstrateView    `json:"substrate"`
	Cap       CapView          `json:"cap"`
	Timings   map[string]int64 `json:"timings"` // p50 ms of freeze, spring, extract
	// Vault is how many targets were booked: their sessions are deleted, and their
	// checkpoints stay in Snapshot Storage until the admin reset deletes them.
	// PodHourUSD is the assumed price of one always-on agent pod (POD_HOUR_USD, as on
	// Agent Flow), for the big screen's cost estimate.
	Vault      int     `json:"vault"`
	PodHourUSD float64 `json:"podHourUsd"`
	// Squads is takedowns per shared squad (by code); solo players are left out.
	Squads map[string]int `json:"squads"`
}

// Snap is a whole snapshot as a client decodes it.
type Snap struct {
	SnapHead
	SnapCommon
}

type AvatarView struct {
	ID     string  `json:"id"`
	Nick   string  `json:"nick"`
	Color  string  `json:"color"`
	Squad  string  `json:"squad"`
	X      float64 `json:"x"`
	Y      float64 `json:"y"`
	FX     float64 `json:"fx"`
	FY     float64 `json:"fy"`
	HP     float64 `json:"hp"`
	Down   bool    `json:"down"`
	Online bool    `json:"online"`
	// PvP: frozen by another player (and by whom), spawn-protected, PvP hits taken, and
	// the player's own Substrate agent (its state and last suspend or resume in ms).
	Frozen    bool    `json:"frozen,omitempty"`
	FrozenBy  string  `json:"frozenBy,omitempty"`
	Protected bool    `json:"protected,omitempty"`
	PvPHits   int     `json:"pvpHits,omitempty"`
	Agent     string  `json:"agent,omitempty"`
	AgentMs   int64   `json:"agentMs,omitempty"`
	RespawnIn float64 `json:"respawnIn,omitempty"` // seconds until a frozen player respawns
}

type TargetView struct {
	ID      string  `json:"id"`
	Name    string  `json:"name"`
	Mission string  `json:"mission"`
	Squad   string  `json:"squad"`
	Node    string  `json:"node"`
	Say     string  `json:"say"`
	X       float64 `json:"x"`
	Y       float64 `json:"y"`
	State   string  `json:"state"`
	Hits    int     `json:"hits"`
	Step    int     `json:"step"`
	Steps   int     `json:"steps"`
	Busy    bool    `json:"busy"`
	Op      string  `json:"op,omitempty"` // the op in flight: "freeze", "extract" or "delete"
	New     bool    `json:"new"`
}

type AllyView struct {
	ID    string  `json:"id"`
	Owner string  `json:"owner"`
	Say   string  `json:"say"`
	X     float64 `json:"x"`
	Y     float64 `json:"y"`
	HP    float64 `json:"hp"`
	Tier  int     `json:"tier"`
	State string  `json:"state"`
	Busy  bool    `json:"busy"`
}

type PartView struct {
	ID    string `json:"id"`
	Kind  string `json:"kind"`
	Tier  int    `json:"tier"`
	Label string `json:"label"`
}

type DropView struct {
	PartView
	X float64 `json:"x"`
	Y float64 `json:"y"`
}

// MissionView is a run: Score is the squad's points, Outcome "" or "done".
type MissionView struct {
	ID      string  `json:"id"`
	Squad   string  `json:"squad"`
	Started float64 `json:"started"`
	Ends    float64 `json:"ends"`
	Score   int     `json:"score"`
	Outcome string  `json:"outcome"`
}

type EventView struct {
	Kind   string `json:"kind"`
	Target string `json:"target,omitempty"`
	Player string `json:"player,omitempty"`
	Text   string `json:"text,omitempty"`
	Ms     int64  `json:"ms,omitempty"`
}

// ScoreView is a leaderboard row. Best is the player's best run score; Freezes and
// Books count every run. Rig is the best RAM tier (1-5) of any ally the player deployed
// (0: none); Allies is how many of their allies are in the city now.
type ScoreView struct {
	ID      string `json:"id"`
	Nick    string `json:"nick"`
	Squad   string `json:"squad"`
	Best    int    `json:"best"`
	Freezes int    `json:"freezes"`
	Books   int    `json:"books"`
	Players int    `json:"players"` // other players frozen
	Rig     int    `json:"rig"`
	Allies  int    `json:"allies"`
}

// SubstrateView is the whole Substrate (actors, workers) plus this game's share: Pool
// is the heist WorkerPool's registered workers, and Agents, Awake and Asleep count the
// actors of the heister and sidekick Agents.
type SubstrateView struct {
	Actors      int64 `json:"actors"`
	Running     int64 `json:"running"`
	Suspended   int64 `json:"suspended"`
	Workers     int64 `json:"workers"`
	BusyWorkers int64 `json:"busyWorkers"`
	Pool        int   `json:"pool"`
	Agents      int   `json:"agents"`
	Awake       int   `json:"awake"`
	Asleep      int   `json:"asleep"`
}

// heistPool and heistAgents name this game's WorkerPool and Agents (manifests/heist-agent.yaml).
const heistPool = "heist"

var heistAgents = []string{"heister", "sidekick"}

// substrateView is s as snapshots carry it.
func substrateView(s kagent.SubstrateStats) SubstrateView {
	v := SubstrateView{Actors: s.Actors, Running: s.Running, Suspended: s.Suspended, Workers: s.Workers, BusyWorkers: s.BusyWorkers}
	for _, p := range s.Pools {
		if p.Name == heistPool {
			v.Pool = p.Workers
		}
	}
	for _, a := range s.Agents {
		if slices.Contains(heistAgents, a.Name) {
			v.Agents += a.Actors
			v.Awake += a.Awake
			v.Asleep += a.Suspended
		}
	}
	return v
}

type CapView struct {
	Steps   int `json:"steps"`
	Resumes int `json:"resumes"`
	Queued  int `json:"queued"`
}

func r2(f float64) float64 { return math.Round(f*100) / 100 }

// respawnIn is how long a frozen player has left, or 0.
func respawnIn(a *Avatar, now float64) float64 {
	if !a.Frozen {
		return 0
	}
	return r2(max(a.RespawnAt-now, 0.01))
}

func parts(ps []Part) *[]PartView {
	out := make([]PartView, 0, len(ps))
	for _, p := range ps {
		out = append(out, PartView{p.ID, p.Kind, p.Tier, p.Label()})
	}
	return &out
}

// commonLocked builds the shared part of a snapshot. g.mu is held.
func (g *Game) commonLocked(ev []Event) SnapCommon {
	w := g.w
	c := SnapCommon{Now: r2(w.Now), Avatars: []AvatarView{}, Targets: []TargetView{}, Allies: []AllyView{},
		Drops: []DropView{}, Missions: []MissionView{}, Events: []EventView{}, Board: []ScoreView{}, Timings: maps.Clone(g.timings),
		Vault: len(g.booked), PodHourUSD: g.podHourUSD, Squads: map[string]int{}}
	for sq, n := range g.m.SquadTotals() {
		if !strings.HasPrefix(sq, "solo-") && n > 0 {
			c.Squads[sq] = n
		}
	}
	allies := map[string]int{}
	for _, al := range w.Allies {
		allies[al.Owner]++
	}
	for _, id := range slices.Sorted(maps.Keys(w.Avatars)) {
		a := w.Avatars[id]
		hits := a.PvPHits
		if w.Now-a.LastPvP > pvpHitDecay {
			hits = 0
		}
		c.Avatars = append(c.Avatars, AvatarView{a.PlayerID, a.Nick, a.Color, a.Squad, r2(a.Pos.X), r2(a.Pos.Y),
			r2(a.Facing.X), r2(a.Facing.Y), r2(a.HP), a.Down, a.Online,
			a.Frozen, a.FrozenBy, w.Now < a.ProtectUntil, hits, a.Agent.State, a.Agent.Ms, respawnIn(a, w.Now)})
	}
	for _, id := range slices.Sorted(maps.Keys(w.Targets)) {
		t := w.Targets[id]
		c.Targets = append(c.Targets, TargetView{t.ID, t.Name, t.MissionID, t.Squad, t.Node, t.Say, r2(t.Pos.X), r2(t.Pos.Y),
			string(t.State), t.Hits, t.Step, t.Steps, t.Busy, t.opKind, t.New})
		if t.State == Booked {
			c.Vault++ // booked but still in the world (its mission has not been cleaned up)
		}
	}
	for _, id := range slices.Sorted(maps.Keys(w.Allies)) {
		al := w.Allies[id]
		c.Allies = append(c.Allies, AllyView{al.ID, al.Owner, al.Say, r2(al.Pos.X), r2(al.Pos.Y), r2(al.HP), al.Tier, string(al.State), al.Busy})
	}
	for _, id := range slices.Sorted(maps.Keys(w.Drops)) {
		d := w.Drops[id]
		c.Drops = append(c.Drops, DropView{PartView{d.Part.ID, d.Part.Kind, d.Part.Tier, d.Part.Label()}, r2(d.Pos.X), r2(d.Pos.Y)})
	}
	for _, ms := range g.m.missions {
		if at, ok := g.ended[ms]; ms.Outcome != "" && ok && w.Now-at > missionShow {
			continue
		}
		c.Missions = append(c.Missions, MissionView{ms.ID, ms.Squad, r2(ms.Started), r2(ms.Ends), ms.Score, ms.Outcome})
	}
	for _, e := range ev {
		c.Events = append(c.Events, EventView{e.Kind, e.Target, e.Player, e.Text, e.Ms})
	}
	for i, r := range g.m.Leaderboard() {
		if i == boardRows {
			break
		}
		c.Board = append(c.Board, ScoreView{r.PlayerID, r.Nick, r.Squad, r.Best, r.Freezes, r.Books, r.Players, r.Rig, allies[r.PlayerID]})
	}
	c.Substrate = substrateView(g.p.Substrate())
	c.Cap.Steps, c.Cap.Resumes, c.Cap.Queued = g.s.InFlight()
	return c
}

// frames is one broadcast: the shared part and each receiver's head.
type frames struct {
	common SnapCommon
	heads  map[*client]SnapHead
}

// framesLocked snapshots the state for every client. g.mu is held.
func (g *Game) framesLocked(ev []Event) frames {
	f := frames{common: g.commonLocked(ev), heads: map[*client]SnapHead{}}
	rank := map[string]int{}
	for i, r := range g.m.Leaderboard() {
		rank[r.PlayerID] = i + 1
	}
	for c := range g.clients {
		h := SnapHead{T: "snap", Me: c.player}
		if !c.screen {
			if a := g.w.Avatars[c.player]; a != nil {
				h.Inv = parts(a.Inv)
				if ms := g.m.lastRun(a.Squad); ms != nil {
					h.Score = ms.Scores[c.player]
				}
			}
			if p := g.m.players[c.player]; p != nil {
				h.Best, h.Rank = p.Best, rank[c.player]
			}
		}
		f.heads[c] = h
	}
	return f
}

// send marshals the shared part once and hands each client its frame without waiting.
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
	snaps  chan []byte // the newest snapshots; a stale one is dropped for a fresh one
	ctrl   chan []byte // replies; a client that lets these back up is closed
	cancel context.CancelFunc
	kick   chan struct{} // tells writeLoop to send {"t":"full"} and close
	public bool          // a screen reached through the tunnel (has cf-connecting-ip)
	seq    uint64        // attach order, for evicting the oldest public screen

	in  bucket // input rate limiter (g.mu)
	cmd bucket // command rate limiter (reader goroutine only)

	// lastIn is the last input applied for this connection (g.mu).
	lastIn  Input
	applied bool
}

// bucket is a token bucket.
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

// push queues a snapshot, dropping the oldest queued one if the client is behind.
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

// allowInput limits in frames to inputRate a second (burst inputBurst).
func (c *client) allowInput(now time.Time) bool { return c.in.allow(now, inputRate, inputBurst) }

// allowCmd limits start, build and squad frames to cmdRate a second (burst cmdBurst).
func (c *client) allowCmd(now time.Time) bool { return c.cmd.allow(now, cmdRate, cmdBurst) }

// kickFull asks writeLoop to send {"t":"full"} and close the connection.
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
			conn.Close(websocket.StatusTryAgainLater, "city full")
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

// serveWS is GET /substrate-siege/ws.
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
	ctx, cancel := context.WithCancel(context.Background()) // r.Context() is unreliable once hijacked
	defer cancel()
	c := &client{pl: pl, player: pl.ID, screen: screen, public: r.Header.Get("cf-connecting-ip") != "",
		snaps: make(chan []byte, 2), ctrl: make(chan []byte, 16), kick: make(chan struct{}, 1), cancel: cancel}
	if !g.attach(c) {
		wctx, wc := context.WithTimeout(ctx, time.Second)
		_ = conn.Write(wctx, websocket.MessageText, []byte(`{"t":"full"}`))
		wc()
		conn.Close(websocket.StatusTryAgainLater, "city full")
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

// attach registers c, bringing its player online. False when the city is full or the
// game has stopped. A public screen beyond maxScreens evicts the oldest public screen
// (squatters can't lock the booth out); LAN screens are not counted.
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
	case !g.joinLocked(c.pl):
		return false
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

// input applies an input frame within the rate limit. A frame that changes fire, act or
// whether the stick is centred is never dropped (a lost release would hold FIRE, or turn
// a tap into a 1 s hold that deletes a target); any other frame over the limit is kept
// as the connection's pending input, replaced by newer ones, and applied by the tick
// once a token frees. g.mu is held.
func (g *Game) input(c *client, in Input, now time.Time) {
	last := c.lastIn
	edge := !c.applied || in.Fire != last.Fire || in.Action != last.Action || (in.Move == Vec{}) != (last.Move == Vec{})
	if c.allowInput(now) || edge {
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

// flushInputs applies coalesced inputs whose connection has a token again. g.mu is held.
func (g *Game) flushInputs(now time.Time) {
	for c, in := range g.pendIn {
		if c.allowInput(now) {
			g.applyInput(c, in)
		}
	}
}

// handle applies one client frame.
func (g *Game) handle(c *client, b []byte) {
	var m struct {
		T         string
		MX, MY    float64
		Fire, Act bool
		Op, Code  string
	}
	if json.Unmarshal(b, &m) != nil {
		return
	}
	switch m.T {
	case "squad", "start", "ping":
		if !c.allowCmd(time.Now()) {
			return
		}
	}
	if m.T == "ping" { // round-trip probe for the phone's debug overlay; touches no state
		c.reply(map[string]string{"t": "pong", "id": m.Code})
		return
	}
	clamp := func(f float64) float64 { return max(-1, min(1, f)) }
	var err error
	g.mu.Lock()
	if !g.wake(c) {
		g.mu.Unlock()
		c.kickFull()
		return
	}
	switch m.T {
	case "in":
		g.input(c, Input{Move: Vec{clamp(m.MX), clamp(m.MY)}, Fire: m.Fire, Action: m.Act}, time.Now())
	case "squad":
		var code string
		if code, err = g.squad(c.player, m.Op, m.Code); err == nil {
			c.reply(map[string]string{"t": "squad", "code": code})
		}
	case "start":
		_, err = g.start(c.player)
	}
	g.mu.Unlock()
	if err != nil {
		c.reply(map[string]string{"t": "err", "msg": err.Error()})
	}
}
