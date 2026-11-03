package heist

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"github.com/coder/websocket"
	"github.com/sebbycorp/kubecon-adventures/arcade/internal/platform"
)

// liveOps answers every call at once, except kinds in block, which wait for ctx. It
// records every call as "kind arg".
type liveOps struct {
	mu    sync.Mutex
	calls []string
	block map[string]bool
}

func (o *liveOps) do(ctx context.Context, kind, arg string) error {
	o.mu.Lock()
	o.calls = append(o.calls, kind+" "+arg)
	b := o.block[kind]
	o.mu.Unlock()
	if b {
		<-ctx.Done()
		return ctx.Err()
	}
	return nil
}

func (o *liveOps) called(kind string) []string {
	o.mu.Lock()
	defer o.mu.Unlock()
	var out []string
	for _, c := range o.calls {
		if k, arg, _ := strings.Cut(c, " "); k == kind {
			out = append(out, arg)
		}
	}
	return out
}

func (o *liveOps) Spawn(ctx context.Context, name string) (string, string, error) {
	return "sess-" + name, "node-1", o.do(ctx, "spawn", name)
}
func (o *liveOps) SpawnAlly(ctx context.Context, name string) (string, string, string, error) {
	return "sess-" + name, "node-1", "hi boss", o.do(ctx, "ally", name)
}
func (o *liveOps) Step(ctx context.Context, id string, step, steps int) (StepResult, error) {
	return StepResult{Say: "working"}, o.do(ctx, "step", id)
}
func (o *liveOps) Freeze(ctx context.Context, id string) (int64, error) {
	return 7, o.do(ctx, "freeze", id)
}
func (o *liveOps) Spring(ctx context.Context, id string) (string, string, int64, error) {
	return "node-1", "back", 9, o.do(ctx, "spring", id)
}
func (o *liveOps) Extract(ctx context.Context, id string) (int64, error) {
	return 11, o.do(ctx, "extract", id)
}
func (o *liveOps) Delete(ctx context.Context, id string) error { return o.do(ctx, "delete", id) }

var testWeb = fstest.MapFS{
	"phone.html":  {Data: []byte(`<script src="/grand-theft-agent/static/app.js"></script>`)},
	"screen.html": {Data: []byte(`screen`)},
	"app.js":      {Data: []byte(`1`)},
}

// eventually polls cond (under g.mu) until it holds or 3 s pass.
func eventually(t *testing.T, g *Game, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		g.mu.Lock()
		ok := cond()
		g.mu.Unlock()
		if ok {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

// pumpUntil runs manual ticks until cond holds (checked under g.mu), up to 3 s.
func pumpUntil(t *testing.T, g *Game, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for i := 0; ; i++ {
		g.tick()
		g.mu.Lock()
		ok := cond()
		g.mu.Unlock()
		if ok {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		if i%20 == 0 {
			time.Sleep(time.Millisecond) // let op results land
		}
	}
}

type server struct {
	g      *Game
	srv    *httptest.Server
	cancel context.CancelFunc
}

func newServer(t *testing.T, ops Ops) *server {
	t.Helper()
	g := New(platform.New(nil), ops, []string{"node-1", "node-2"})
	mux := http.NewServeMux()
	g.Routes(mux, testWeb)
	srv := httptest.NewServer(mux)
	ctx, cancel := context.WithCancel(context.Background())
	g.Run(ctx)
	t.Cleanup(func() { cancel(); srv.Close() })
	return &server{g, srv, cancel}
}

func (s *server) dial(t *testing.T, pl *platform.Player, query string, hdr http.Header) (*websocket.Conn, *http.Response, error) {
	t.Helper()
	if hdr == nil {
		hdr = http.Header{}
	}
	if pl != nil {
		hdr.Set("Cookie", platform.PlayerCookie+"="+pl.ID)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	c, resp, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(s.srv.URL, "http")+"/grand-theft-agent/ws"+query, &websocket.DialOptions{HTTPHeader: hdr})
	if c != nil {
		t.Cleanup(func() { c.CloseNow() })
	}
	return c, resp, err
}

func (s *server) mustDial(t *testing.T, pl *platform.Player, query string) *websocket.Conn {
	t.Helper()
	c, _, err := s.dial(t, pl, query, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	return c
}

// frame reads the next frame of type typ within d.
func frame(t *testing.T, c *websocket.Conn, typ string, d time.Duration) map[string]json.RawMessage {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), d)
	defer cancel()
	for {
		_, b, err := c.Read(ctx)
		if err != nil {
			t.Fatalf("waiting for %q frame: %v", typ, err)
		}
		var m map[string]json.RawMessage
		if err := json.Unmarshal(b, &m); err != nil {
			t.Fatalf("bad frame %s: %v", b, err)
		}
		if string(m["t"]) == `"`+typ+`"` {
			return m
		}
	}
}

func snap(t *testing.T, c *websocket.Conn, d time.Duration, cond func(Snap) bool) Snap {
	t.Helper()
	deadline := time.Now().Add(d)
	for {
		var s Snap
		raw := frame(t, c, "snap", time.Until(deadline))
		b, _ := json.Marshal(raw)
		if err := json.Unmarshal(b, &s); err != nil {
			t.Fatal(err)
		}
		if cond == nil || cond(s) {
			return s
		}
	}
}

func send(t *testing.T, c *websocket.Conn, v any) {
	t.Helper()
	b, _ := json.Marshal(v)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := c.Write(ctx, websocket.MessageText, b); err != nil {
		t.Fatalf("write: %v", err)
	}
}

func dispatchCentre(g *Game) Vec {
	d := g.city.Dispatch
	return Vec{d.X + d.W/2, d.Y + d.H/2}
}

func TestWSJoinAndSnapshot(t *testing.T) {
	s := newServer(t, &liveOps{})
	pl := s.g.p.Players.Join("Ada")
	c := s.mustDial(t, &pl, "")
	got := snap(t, c, 200*time.Millisecond, nil)
	if got.Me != pl.ID {
		t.Fatalf("me = %q, want %q", got.Me, pl.ID)
	}
	i := slices.IndexFunc(got.Avatars, func(a AvatarView) bool { return a.ID == pl.ID })
	if i < 0 {
		t.Fatalf("own avatar missing: %+v", got.Avatars)
	}
	a, want := got.Avatars[i], dispatchCentre(s.g)
	if a.X != want.X || a.Y != want.Y || a.Nick != "Ada" || a.Color != pl.Color || a.HP != maxHP || a.Down || !a.Online {
		t.Fatalf("avatar = %+v, want online at %v with full health", a, want)
	}
	if a.Squad != "solo-"+pl.ID {
		t.Fatalf("squad = %q, want solo", a.Squad)
	}
}

func TestWSRejectsNoCookie(t *testing.T) {
	s := newServer(t, &liveOps{})
	_, resp, err := s.dial(t, nil, "", nil)
	if err == nil || resp == nil || resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("no cookie: err=%v resp=%v, want 401", err, resp)
	}
	c := s.mustDial(t, nil, "?screen=1")
	got := snap(t, c, 500*time.Millisecond, nil)
	if got.Me != "" || got.Inv != nil || got.Stash != nil {
		t.Fatalf("screen snap has player fields: %+v", got)
	}
	send(t, c, map[string]any{"t": "in", "mx": 1, "fire": true})
	send(t, c, map[string]any{"t": "start", "level": 1})
	send(t, c, map[string]any{"t": "squad", "op": "create"})
	snap(t, c, 500*time.Millisecond, nil)
	s.g.mu.Lock()
	n, m := len(s.g.w.Avatars), len(s.g.m.missions)
	s.g.mu.Unlock()
	if n != 0 || m != 0 {
		t.Fatalf("screen frames had effect: %d avatars, %d missions", n, m)
	}
}

func TestWSOriginCheck(t *testing.T) {
	s := newServer(t, &liveOps{})
	pl := s.g.p.Players.Join("Ada")
	if _, resp, err := s.dial(t, &pl, "", http.Header{"Origin": {"https://evil.example"}}); err == nil || resp.StatusCode != http.StatusForbidden {
		t.Fatalf("foreign origin: err=%v, want 403", err)
	}
	if _, _, err := s.dial(t, &pl, "", http.Header{"Origin": {"https://kubecon.agentpwned.ai"}}); err != nil {
		t.Fatalf("booth origin rejected: %v", err)
	}
}

func TestReconnectKeepsAvatar(t *testing.T) {
	s := newServer(t, &liveOps{})
	pl := s.g.p.Players.Join("Ada")
	c := s.mustDial(t, &pl, "")
	send(t, c, map[string]any{"t": "in", "mx": 1})
	time.Sleep(150 * time.Millisecond)
	send(t, c, map[string]any{"t": "in"})
	time.Sleep(100 * time.Millisecond)
	s.g.mu.Lock()
	before := *s.g.w.Avatars[pl.ID]
	s.g.mu.Unlock()
	if before.Pos == dispatchCentre(s.g) {
		t.Fatal("avatar did not move")
	}
	c.Close(websocket.StatusNormalClosure, "")
	eventually(t, s.g, "offline", func() bool { return !s.g.w.Avatars[pl.ID].Online })

	c2 := s.mustDial(t, &pl, "")
	got := snap(t, c2, time.Second, nil)
	i := slices.IndexFunc(got.Avatars, func(a AvatarView) bool { return a.ID == pl.ID })
	if i < 0 || len(got.Avatars) != 1 {
		t.Fatalf("avatars after reconnect: %+v", got.Avatars)
	}
	if a := got.Avatars[i]; math.Abs(a.X-before.Pos.X) > 0.01 || math.Abs(a.Y-before.Pos.Y) > 0.01 || !a.Online {
		t.Fatalf("avatar after reconnect = %+v, want online at %v", a, before.Pos)
	}
}

func TestPlayerCap(t *testing.T) {
	s := newServer(t, &liveOps{})
	s.g.mu.Lock()
	for i := range maxPlayers { // 48 online
		id := fmt.Sprintf("p%02d", i)
		s.g.w.Avatars[id] = NewAvatar(id, id, "", "", Vec{})
	}
	s.g.mu.Unlock()

	pl := s.g.p.Players.Join("Late")
	c49 := s.mustDial(t, &pl, "")
	frame(t, c49, "full", time.Second)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if _, _, err := c49.Read(ctx); websocket.CloseStatus(err) == -1 && ctx.Err() != nil {
		t.Fatalf("49th connection not closed: %v", err)
	}
	// Once someone goes offline there is room again.
	s.g.mu.Lock()
	s.g.w.Avatars["p00"].Online = false
	s.g.mu.Unlock()
	c2 := s.mustDial(t, &pl, "")
	snap(t, c2, time.Second, func(sn Snap) bool { return sn.Me == pl.ID })
}

func TestPlayerCapCountsOnlineOnly(t *testing.T) {
	g, _ := newManual(t)
	g.leave("a")
	for i := range maxPlayers {
		pl := platform.Player{ID: fmt.Sprintf("p%02d", i)}
		if !g.join(pl) {
			t.Fatalf("player %d refused", i)
		}
		g.leave(pl.ID)
	}
	if !g.join(platform.Player{ID: "new"}) {
		t.Fatal("49th distinct player refused while nobody is online")
	}
}

func TestOfflineAvatarEvicted(t *testing.T) {
	g, f := newManual(t)
	g.mu.Lock()
	a := g.w.Avatars["a"]
	a.Pos = Vec{10.5, 12.5}
	addAlly(g.w, "al", "a", 1, Vec{}).SessionID = "s-al"
	tg := addTarget(g.w, "t1", "", Dragged, a.Pos)
	a.Dragging = "t1"
	g.w.Stash["a"] = rig("p", 2)
	g.m.player("a", g.w).Takedowns = 3
	g.mu.Unlock()
	g.leave("a")
	g.mu.Lock()
	off := g.w.Now
	g.mu.Unlock()
	go func() { // let the ally's retire delete finish
		for c := range f.started {
			close(c.release)
		}
	}()
	pumpUntil(t, g, "evicted", func() bool { return g.w.Avatars["a"] == nil })
	g.mu.Lock()
	if g.w.Now-off < evictAfter-1 {
		t.Errorf("evicted after %.0f s", g.w.Now-off)
	}
	if tg.State != Frozen {
		t.Errorf("drag kept: target %s", tg.State)
	}
	if len(g.w.Stash["a"]) != 4 || g.m.Leaderboard()[0].Takedowns != 3 {
		t.Errorf("stash or score lost: %v %v", g.w.Stash["a"], g.m.Leaderboard())
	}
	if g.w.Allies["al"] != nil {
		t.Error("ally still in the city")
	}
	g.mu.Unlock()
	if !g.join(platform.Player{ID: "a", Nick: "Ada"}) {
		t.Fatal("returning player refused")
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if b := g.w.Avatars["a"]; b == nil || b == a || b.Pos != dispatchCentre(g) || !b.Online {
		t.Fatalf("returning avatar = %+v, want a fresh one at Dispatch", b)
	}
}

func TestScreenCap(t *testing.T) {
	s := newServer(t, &liveOps{})
	for range maxScreens {
		snap(t, s.mustDial(t, nil, "?screen=1"), time.Second, nil)
	}
	frame(t, s.mustDial(t, nil, "?screen=1"), "full", time.Second)
	pl := s.g.p.Players.Join("Ada") // players are not affected
	snap(t, s.mustDial(t, &pl, ""), time.Second, nil)
}

func TestCommandRateLimit(t *testing.T) {
	s := newServer(t, &liveOps{})
	pl := s.g.p.Players.Join("Ada")
	c := s.mustDial(t, &pl, "")
	for range 20 {
		send(t, c, map[string]any{"t": "squad", "op": "solo"})
	}
	n := 0
	ctx, cancel := context.WithTimeout(context.Background(), 400*time.Millisecond)
	defer cancel()
	for {
		_, b, err := c.Read(ctx)
		if err != nil {
			break
		}
		if strings.Contains(string(b), `"t":"squad"`) {
			n++
		}
	}
	if n < 1 || n > int(cmdBurst)+2 {
		t.Fatalf("%d of 20 squad frames answered; want 1..%d", n, int(cmdBurst)+2)
	}
}

func TestEndedMissionsPruned(t *testing.T) {
	g, _ := newManual(t)
	g.mu.Lock()
	for i := range 200 {
		ms, err := g.m.Start(fmt.Sprintf("s%d", i), 1, g.w, g.s)
		if err != nil {
			t.Fatal(err)
		}
		g.m.finish(ms, "failed", g.w)
	}
	g.m.player("a", g.w).Takedowns = 7
	g.m.squadTD["s1"] = 2
	end := g.w.Now
	g.mu.Unlock()
	pumpUntil(t, g, "pruned", func() bool { return g.w.Now > end+cleanupDelay+missionShow+1 })
	g.mu.Lock()
	defer g.mu.Unlock()
	if len(g.m.missions) != 0 || len(g.ended) != 0 || len(g.cleaned) != 0 || len(g.m.targets) != 0 {
		t.Fatalf("left: %d missions, %d ended, %d cleaned, %d targets", len(g.m.missions), len(g.ended), len(g.cleaned), len(g.m.targets))
	}
	if g.m.Leaderboard()[0].Takedowns != 7 || g.m.SquadTotals()["s1"] != 2 {
		t.Fatal("scores lost with the missions")
	}
}

func TestResetCleansUp(t *testing.T) {
	base := runtime.NumGoroutine()
	ops := &liveOps{}
	s := newServer(t, ops)
	pl := s.g.p.Players.Join("Ada")
	c := s.mustDial(t, &pl, "")
	snap(t, c, time.Second, nil)
	s.g.mu.Lock()
	s.g.m.best["solo-"+pl.ID] = 2
	s.g.mu.Unlock()
	send(t, c, map[string]any{"t": "start", "level": 3})
	eventually(t, s.g, "three targets", func() bool { return len(s.g.w.Targets) == 3 })

	n, err := s.g.Reset(context.Background())
	if err != nil || n != 3 {
		t.Fatalf("Reset = %d, %v; want 3", n, err)
	}
	dels := ops.called("delete")
	for i := 1; i <= 3; i++ {
		if want := fmt.Sprintf("sess-m1-%d", i); !slices.Contains(dels, want) {
			t.Errorf("delete calls %v miss %s", dels, want)
		}
	}
	s.g.mu.Lock()
	nt, nm := len(s.g.w.Targets), len(s.g.m.missions)
	s.g.mu.Unlock()
	if nt != 0 || nm != 0 {
		t.Fatalf("after reset: %d targets, %d missions", nt, nm)
	}
	got := snap(t, c, time.Second, func(sn Snap) bool { return len(sn.Targets) == 0 })
	if !slices.ContainsFunc(got.Avatars, func(a AvatarView) bool { return a.ID == pl.ID && a.Online }) {
		t.Fatalf("connected player lost their avatar: %+v", got.Avatars)
	}

	c.Close(websocket.StatusNormalClosure, "")
	s.cancel()
	s.srv.Close()
	deadline := time.Now().Add(3 * time.Second)
	for runtime.NumGoroutine() > base {
		if time.Now().After(deadline) {
			buf := make([]byte, 1<<16)
			t.Fatalf("goroutines %d > baseline %d\n%s", runtime.NumGoroutine(), base, buf[:runtime.Stack(buf, true)])
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestTickNeverBlocksOnOps(t *testing.T) {
	ops := &liveOps{block: map[string]bool{"freeze": true}}
	s := newServer(t, ops)
	pl := s.g.p.Players.Join("Ada")
	c := s.mustDial(t, &pl, "")
	snap(t, c, time.Second, nil)
	s.g.mu.Lock()
	a := s.g.w.Avatars[pl.ID]
	tg := addTarget(s.g.w, "t1", "", Working, Vec{a.Pos.X + 1, a.Pos.Y})
	tg.SessionID, tg.Hits = "sess-t1", hitsToFreeze-1
	s.g.mu.Unlock()
	send(t, c, map[string]any{"t": "in", "fire": true})
	eventually(t, s.g, "freeze started", func() bool { return len(ops.called("freeze")) == 1 })
	start := time.Now()
	for range 30 {
		snap(t, c, time.Second, nil)
	}
	if d := time.Since(start); d > 2500*time.Millisecond {
		t.Fatalf("30 ticks took %v", d)
	}
}

func TestSlowClientDoesNotBlock(t *testing.T) {
	s := newServer(t, &liveOps{})
	pl := s.g.p.Players.Join("Ada")
	slow := s.mustDial(t, &pl, "") // never read
	_ = slow
	pl2 := s.g.p.Players.Join("Bob")
	c := s.mustDial(t, &pl2, "")
	start := time.Now()
	for range 20 {
		snap(t, c, time.Second, nil)
	}
	if d := time.Since(start); d > 2*time.Second {
		t.Fatalf("20 snaps took %v with a stalled client", d)
	}
}

func TestOversizeFrameCloses(t *testing.T) {
	s := newServer(t, &liveOps{})
	pl := s.g.p.Players.Join("Ada")
	c := s.mustDial(t, &pl, "")
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_ = c.Write(ctx, websocket.MessageText, []byte(`{"t":"in","pad":"`+strings.Repeat("x", 5000)+`"}`))
	for {
		if _, _, err := c.Read(ctx); err != nil {
			if ctx.Err() != nil {
				t.Fatal("connection not closed after a 5 KB frame")
			}
			break
		}
	}
	eventually(t, s.g, "offline", func() bool { return !s.g.w.Avatars[pl.ID].Online })
}

func TestSquadStartBuildMessages(t *testing.T) {
	s := newServer(t, &liveOps{})
	ada, bob := s.g.p.Players.Join("Ada"), s.g.p.Players.Join("Bob")
	ca, cb := s.mustDial(t, &ada, ""), s.mustDial(t, &bob, "")
	send(t, ca, map[string]any{"t": "squad", "op": "create"})
	var code string
	_ = json.Unmarshal(frame(t, ca, "squad", time.Second)["code"], &code)
	if len(code) != 4 {
		t.Fatalf("code = %q", code)
	}
	send(t, cb, map[string]any{"t": "squad", "op": "join", "code": strings.ToLower(code)})
	frame(t, cb, "squad", time.Second)
	send(t, cb, map[string]any{"t": "squad", "op": "join", "code": "ZZZZ"})
	frame(t, cb, "err", time.Second)
	eventually(t, s.g, "same squad", func() bool { return s.g.w.Avatars[bob.ID].Squad == code })

	send(t, ca, map[string]any{"t": "start", "level": 3})
	frame(t, ca, "err", time.Second)
	send(t, ca, map[string]any{"t": "build"})
	frame(t, ca, "err", time.Second)

	pause := func(on bool) {
		resp, err := http.Post(s.srv.URL+"/admin/grand-theft-agent/api/pause", "application/json", strings.NewReader(fmt.Sprintf(`{"paused":%v}`, on)))
		if err != nil || resp.StatusCode != 200 {
			t.Fatalf("pause: %v %v", err, resp)
		}
		resp.Body.Close()
	}
	pause(true)
	send(t, ca, map[string]any{"t": "start", "level": 1})
	var msg string
	_ = json.Unmarshal(frame(t, ca, "err", time.Second)["msg"], &msg)
	if msg != "paused" {
		t.Fatalf("err = %q, want paused", msg)
	}
	pause(false)
	send(t, ca, map[string]any{"t": "start", "level": 1})
	sn := snap(t, cb, time.Second, func(sn Snap) bool { return len(sn.Missions) == 1 })
	if sn.Missions[0].Squad != code || sn.Missions[0].Level != 1 {
		t.Fatalf("missions = %+v", sn.Missions)
	}
	// Garbage is ignored, not fatal.
	_ = ca.Write(context.Background(), websocket.MessageText, []byte(`nope`))
	send(t, ca, map[string]any{"t": "dance"})
	snap(t, ca, time.Second, nil)
}

func TestInputRateLimit(t *testing.T) {
	g := New(platform.New(nil), &liveOps{}, nil)
	cl := &client{player: "a"}
	now := time.Now()
	n := 0
	for i := range 100 { // 100 frames in one second
		if cl.allowInput(now.Add(time.Duration(i) * 10 * time.Millisecond)) {
			n++
		}
	}
	if n > 22 {
		t.Fatalf("%d of 100 inputs accepted in 1 s; want ≤ ~20", n)
	}
	_ = g
}

func TestRoutes(t *testing.T) {
	s := newServer(t, &liveOps{})
	for _, p := range []string{"/grand-theft-agent", "/grand-theft-agent/screen", "/grand-theft-agent/static/app.js", "/admin/grand-theft-agent/api/state"} {
		resp, err := http.Get(s.srv.URL + p)
		if err != nil || resp.StatusCode != 200 {
			t.Fatalf("GET %s: %v %v", p, err, resp)
		}
		resp.Body.Close()
	}
	resp, _ := http.Get(s.srv.URL + "/grand-theft-agent")
	var b strings.Builder
	buf := make([]byte, 512)
	nr, _ := resp.Body.Read(buf)
	b.Write(buf[:nr])
	resp.Body.Close()
	if !strings.Contains(b.String(), "/grand-theft-agent/static/app.js?v=") || resp.Header.Get("Cache-Control") != "no-cache" {
		t.Fatalf("page not versioned: %s %v", b.String(), resp.Header)
	}
	resp, err := http.Post(s.srv.URL+"/admin/grand-theft-agent/api/reset", "", nil)
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("reset: %v %v", err, resp)
	}
	resp.Body.Close()
}

// ---- routing and cleanup, driven by manual ticks ----

// newManual is a game (not running) on fakeOps with player a online in squad solo-a.
func newManual(t *testing.T) (*Game, *fakeOps) {
	t.Helper()
	f := newFakeOps()
	g := New(platform.New(nil), f, nil)
	t.Cleanup(func() { g.s.Close() })
	if !g.join(platform.Player{ID: "a", Nick: "Ada", Color: "red"}) {
		t.Fatal("join refused")
	}
	return g, f
}

// release lets the next started call of kind finish (others stay blocked).
func release(t *testing.T, f *fakeOps, kind string) *call {
	t.Helper()
	for {
		c := f.next(t)
		if c.kind == kind {
			close(c.release)
			return c
		}
	}
}

func TestUnrecordedResultIgnored(t *testing.T) {
	g, _ := newManual(t)
	g.mu.Lock()
	tg := addTarget(g.w, "t1", "", Frozen, Vec{})
	g.route(Result{Kind: "spring", Target: "t1", OK: true})
	g.mu.Unlock()
	if tg.State != Frozen {
		t.Fatal("a result the game never submitted was applied")
	}
}

func TestRefusedSubmitClearsBusy(t *testing.T) {
	g, _ := newManual(t)
	g.mu.Lock()
	a := g.w.Avatars["a"]
	tg := addTarget(g.w, "t1", "", Working, Vec{a.Pos.X + 1, a.Pos.Y})
	tg.SessionID, tg.Hits = "s1", hitsToFreeze-1
	g.w.Stash["a"] = rig("p", 3)
	g.s.Close() // every Submit now fails
	g.w.Input("a", Input{Fire: true})
	g.mu.Unlock()
	g.tick()
	g.mu.Lock()
	defer g.mu.Unlock()
	if tg.Busy {
		t.Fatal("target stuck Busy after a refused freeze")
	}
	if err := g.build("a"); err == nil {
		t.Fatal("build with a closed scheduler succeeded")
	}
	if len(g.w.Stash["a"]) != 4 || len(g.w.pending) != 0 {
		t.Fatalf("rig not refunded: stash %v, pending %d", g.w.Stash["a"], len(g.w.pending))
	}
}

func TestLateSpawnDeleted(t *testing.T) {
	g, f := newManual(t)
	g.mu.Lock()
	ms, err := g.start("a", 1)
	g.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	c := f.next(t) // the spawn
	g.mu.Lock()
	g.m.finish(ms, "failed", g.w)
	g.mu.Unlock()
	close(c.release)
	pumpUntil(t, g, "spawn routed", func() bool { return !g.m.targets["m1-1"].spawning })
	d := f.next(t)
	if d.kind != "delete" || d.id != "sess-m1-1" {
		t.Fatalf("next call %s %s, want delete sess-m1-1", d.kind, d.id)
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if len(g.w.Targets) != 0 {
		t.Fatal("late target added to the world")
	}
}

func TestMissionEndCleanup(t *testing.T) {
	g, f := newManual(t)
	g.mu.Lock()
	ms, _ := g.start("a", 1)
	g.mu.Unlock()
	release(t, f, "spawn")
	pumpUntil(t, g, "target", func() bool { return g.w.Targets["m1-1"] != nil })
	g.mu.Lock()
	g.m.finish(ms, "failed", g.w)
	end := g.w.Now
	g.mu.Unlock()
	pumpUntil(t, g, "10 s", func() bool { return g.w.Now >= end+cleanupDelay-0.5 })
	g.mu.Lock()
	still := g.w.Targets["m1-1"] != nil
	g.mu.Unlock()
	if !still {
		t.Fatal("target removed before 10 s")
	}
	pumpUntil(t, g, "target removed", func() bool { return g.w.Targets["m1-1"] == nil })
	for {
		c := f.next(t)
		if c.kind == "delete" {
			if c.id != "sess-m1-1" {
				t.Fatalf("deleted %s", c.id)
			}
			break
		}
	}
}

func TestResetDeletesBookedSessions(t *testing.T) {
	ops := &liveOps{}
	g := New(platform.New(nil), ops, nil)
	t.Cleanup(func() { g.s.Close() })
	g.join(platform.Player{ID: "a"})
	g.mu.Lock()
	ms, _ := g.start("a", 1)
	g.mu.Unlock()
	pumpUntil(t, g, "target", func() bool { return g.w.Targets["m1-1"] != nil })
	g.mu.Lock()
	g.w.Targets["m1-1"].State = Booked
	g.m.finish(ms, "won", g.w)
	end := g.w.Now
	g.mu.Unlock()
	pumpUntil(t, g, "removed", func() bool { return g.w.Now > end+cleanupDelay && g.w.Targets["m1-1"] == nil })
	if slices.Contains(ops.called("delete"), "sess-m1-1") {
		t.Fatal("booked session deleted at mission end")
	}
	if n, _ := g.Reset(context.Background()); n != 1 || !slices.Contains(ops.called("delete"), "sess-m1-1") {
		t.Fatalf("Reset deleted %d, calls %v", n, ops.called("delete"))
	}
}

func TestLateAllyDeleted(t *testing.T) {
	g, f := newManual(t)
	g.mu.Lock()
	ms, _ := g.start("a", 1)
	g.w.Stash["a"] = rig("p", 2)
	if err := g.build("a"); err != nil {
		t.Fatal(err)
	}
	g.m.finish(ms, "won", g.w)
	g.mu.Unlock()
	release(t, f, "ally")
	pumpUntil(t, g, "ally landed", func() bool { return len(g.w.pending) == 0 })
	for {
		c := f.next(t)
		if c.kind == "delete" {
			if c.id != "sess-ally-1" {
				t.Fatalf("deleted %s", c.id)
			}
			break
		}
	}
}

func TestOfflineOwnerAlliesDeleted(t *testing.T) {
	g, f := newManual(t)
	g.mu.Lock()
	addAlly(g.w, "al", "a", 1, Vec{}).SessionID = "s-al"
	g.mu.Unlock()
	g.leave("a")
	g.mu.Lock()
	off := g.w.Now
	g.mu.Unlock()
	pumpUntil(t, g, "59 s", func() bool { return g.w.Now >= off+offlineAllies-1 })
	select {
	case c := <-f.started:
		t.Fatalf("%s %s before 60 s", c.kind, c.id)
	default:
	}
	pumpUntil(t, g, "ally delete", func() bool { return g.w.Allies["al"].Busy })
	if c := f.next(t); c.kind != "delete" || c.id != "s-al" {
		t.Fatalf("call %s %s, want delete s-al", c.kind, c.id)
	}
}

func TestTimingsRecorded(t *testing.T) {
	g, f := newManual(t)
	f.ms = 42
	g.mu.Lock()
	tg := addTarget(g.w, "t1", "", Working, Vec{})
	tg.SessionID = "s1"
	g.submit(g.w.start(tg, "freeze", "a"))
	g.mu.Unlock()
	release(t, f, "freeze")
	pumpUntil(t, g, "frozen", func() bool { return tg.State == Frozen })
	if got := g.p.Timings.Typical("heist_freeze"); got != 42 {
		t.Fatalf("freeze timing = %d", got)
	}
}
