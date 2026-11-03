package heist

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"regexp"
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
	mu       sync.Mutex
	calls    []string
	block    map[string]bool
	sessions    []string            // what ListSessions returns (the startup sweep's orphans)
	checkpoints map[string][]string // what ListCheckpoints returns, by session
	listErr     error
	gone        map[string]bool // sessions Extract deleted
	cpDelay     time.Duration   // each DeleteCheckpoint takes this long
	cpNow       int             // DeleteCheckpoint calls in flight
	cpPeak      int
}

func (o *liveOps) ListSessions(ctx context.Context) ([]string, error) {
	if err := o.do(ctx, "list", ""); err != nil {
		return nil, err
	}
	return o.sessions, o.listErr
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
// Extract, like the real adapter, checkpoints the session, deletes it and returns the
// checkpoint's ID ("cp-<session>").
func (o *liveOps) Extract(ctx context.Context, id string) (string, string, int64, error) {
	if err := o.do(ctx, "extract", id); err != nil {
		return "", "", 0, err
	}
	o.mu.Lock()
	if o.gone == nil {
		o.gone = map[string]bool{}
	}
	o.gone[id] = true
	o.mu.Unlock()
	return "booked", "cp-" + id, 11, nil
}

// Delete fails for a session that is already gone, as kagent does.
func (o *liveOps) Delete(ctx context.Context, id string) error {
	if err := o.do(ctx, "delete", id); err != nil {
		return err
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.gone[id] {
		return errors.New("not found: session " + id)
	}
	return nil
}

func (o *liveOps) DeleteCheckpoint(ctx context.Context, id string) error {
	o.mu.Lock()
	o.cpNow++
	o.cpPeak = max(o.cpPeak, o.cpNow)
	o.mu.Unlock()
	defer func() { o.mu.Lock(); o.cpNow--; o.mu.Unlock() }()
	if o.cpDelay > 0 {
		time.Sleep(o.cpDelay)
	}
	return o.do(ctx, "delete-checkpoint", id)
}

func (o *liveOps) ListCheckpoints(ctx context.Context, sid string) ([]string, error) {
	if err := o.do(ctx, "list-checkpoints", sid); err != nil {
		return nil, err
	}
	return o.checkpoints[sid], nil
}

var testWeb = fstest.MapFS{
	"phone.html":  {Data: []byte(`<script src="/substrate-siege/static/app.js"></script>`)},
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
	c, resp, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(s.srv.URL, "http")+"/substrate-siege/ws"+query, &websocket.DialOptions{HTTPHeader: hdr})
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
	if got.Me != "" || got.Inv != nil {
		t.Fatalf("screen snap has player fields: %+v", got)
	}
	send(t, c, map[string]any{"t": "in", "mx": 1, "fire": true})
	send(t, c, map[string]any{"t": "start"})
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
	a.Pos = Vec{10.5, 9.5}
	addAlly(g.w, "al", "a", 1, Vec{}).SessionID = "s-al"
	a.Inv = rig("p", 2)
	g.m.player("a", g.w).Best = 3
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
	if g.m.Leaderboard()[0].Best != 3 {
		t.Errorf("score lost: %v", g.m.Leaderboard())
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

// publicScreen dials a big screen as the tunnel delivers it (with cf-connecting-ip).
func (s *server) publicScreen(t *testing.T) *websocket.Conn {
	t.Helper()
	c, _, err := s.dial(t, nil, "?screen=1", http.Header{"Cf-Connecting-Ip": {"203.0.113.7"}})
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	return c
}

// TestScreenCap: a public screen beyond maxScreens evicts the oldest public screen, which
// is told {"t":"full"} and closed; the others keep watching. Players are not affected.
func TestScreenCap(t *testing.T) {
	s := newServer(t, &liveOps{})
	var cs []*websocket.Conn
	for range maxScreens {
		c := s.publicScreen(t)
		snap(t, c, time.Second, nil)
		cs = append(cs, c)
	}
	late := s.publicScreen(t)
	snap(t, late, time.Second, nil)
	frame(t, cs[0], "full", time.Second)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	for {
		if _, _, err := cs[0].Read(ctx); err != nil {
			if ctx.Err() != nil {
				t.Fatal("evicted screen not closed")
			}
			break
		}
	}
	for _, c := range cs[1:] {
		snap(t, c, time.Second, nil)
	}
	pl := s.g.p.Players.Join("Ada")
	snap(t, s.mustDial(t, &pl, ""), time.Second, nil)
}

// TestLANScreensExempt: screens without cf-connecting-ip (the booth's own, on the LAN)
// don't count toward the public cap and are never evicted.
func TestLANScreensExempt(t *testing.T) {
	s := newServer(t, &liveOps{})
	var lan []*websocket.Conn
	for range 3 {
		c := s.mustDial(t, nil, "?screen=1")
		snap(t, c, time.Second, nil)
		lan = append(lan, c)
	}
	for range maxScreens + 2 { // public screens churn among themselves
		snap(t, s.publicScreen(t), time.Second, nil)
	}
	for range 3 { // more LAN screens are still welcome
		snap(t, s.mustDial(t, nil, "?screen=1"), time.Second, nil)
	}
	for _, c := range lan {
		sn := snap(t, c, time.Second, nil)
		_ = sn
	}
}

// advance runs manual ticks until world time reaches at.
func advance(g *Game, at float64) {
	for {
		g.mu.Lock()
		now := g.w.Now
		g.mu.Unlock()
		if now >= at {
			return
		}
		g.tick()
	}
}

// TestIdlePlayerGoesOffline: a connected player who sends nothing for idleAfter counts
// as offline: their slot frees and the offline clock starts (allies at 60 s, eviction at
// 300 s). Their next frame brings them back if there is room; if not, they are told the
// city is full and closed, and reconnect like anyone else.
func TestIdlePlayerGoesOffline(t *testing.T) {
	g, _ := newManual(t) // a joins at 0
	cl := testClient("a")
	advance(g, 300)
	for i := range maxPlayers - 1 { // the rest join at 300 and stay active enough
		g.join(platform.Player{ID: fmt.Sprintf("p%02d", i)})
	}
	late := platform.Player{ID: "late"}
	if g.join(late) {
		t.Fatal("city not full")
	}
	advance(g, idleAfter-1)
	g.mu.Lock()
	if !g.w.Avatars["a"].Online {
		t.Fatal("a offline before idleAfter")
	}
	g.mu.Unlock()
	advance(g, idleAfter+1)
	g.mu.Lock()
	_, off := g.offline["a"]
	if g.w.Avatars["a"].Online || !off || !g.w.Avatars["p00"].Online {
		t.Fatalf("after %v s idle: a online=%v offline-clock=%v", idleAfter, g.w.Avatars["a"].Online, off)
	}
	g.mu.Unlock()
	if !g.join(late) {
		t.Fatal("idle player's slot not freed")
	}
	g.handle(cl, []byte(`{"t":"in","mx":1}`)) // the city is full again
	select {
	case <-cl.kick:
	default:
		t.Fatal("idle player in a full city not told full")
	}
	g.mu.Lock()
	if g.w.Avatars["a"].Online {
		t.Fatal("idle player came back past the cap")
	}
	g.mu.Unlock()
	g.leave("late")
	g.handle(cl, []byte(`{"t":"in","mx":1}`)) // room again: back online
	g.mu.Lock()
	defer g.mu.Unlock()
	if _, off := g.offline["a"]; !g.w.Avatars["a"].Online || off {
		t.Fatalf("a online=%v offline-clock=%v after input with room", g.w.Avatars["a"].Online, off)
	}
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
		ms, err := g.m.Start(fmt.Sprintf("s%d", i), g.w, nil)
		if err != nil {
			t.Fatal(err)
		}
		g.m.finish(ms, g.w)
	}
	g.m.player("a", g.w).Best = 7
	g.m.squadTD["s1"] = 2
	end := g.w.Now
	g.mu.Unlock()
	pumpUntil(t, g, "pruned", func() bool { return g.w.Now > end+cleanupDelay+missionShow+1 })
	g.mu.Lock()
	defer g.mu.Unlock()
	if len(g.m.missions) != 0 || len(g.ended) != 0 || len(g.cleaned) != 0 || len(g.m.targets) != 0 {
		t.Fatalf("left: %d missions, %d ended, %d cleaned, %d targets", len(g.m.missions), len(g.ended), len(g.cleaned), len(g.m.targets))
	}
	if g.m.Leaderboard()[0].Best != 7 || g.m.SquadTotals()["s1"] != 2 {
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
	send(t, c, map[string]any{"t": "start"})
	eventually(t, s.g, "two agents", func() bool { return len(s.g.w.Targets) == 2 })

	n, _, err := s.g.Reset(context.Background())
	if err != nil || n != 2 {
		t.Fatalf("Reset = %d, %v; want 2", n, err)
	}
	dels := ops.called("delete")
	for i := 1; i <= 2; i++ {
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

func TestSquadStartMessages(t *testing.T) {
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

	pause := func(on bool) {
		resp, err := http.Post(s.srv.URL+"/admin/substrate-siege/api/pause", "application/json", strings.NewReader(fmt.Sprintf(`{"paused":%v}`, on)))
		if err != nil || resp.StatusCode != 200 {
			t.Fatalf("pause: %v %v", err, resp)
		}
		resp.Body.Close()
	}
	pause(true)
	send(t, ca, map[string]any{"t": "start"})
	var msg string
	_ = json.Unmarshal(frame(t, ca, "err", time.Second)["msg"], &msg)
	if msg != "paused" {
		t.Fatalf("err = %q, want paused", msg)
	}
	pause(false)
	send(t, ca, map[string]any{"t": "start"})
	sn := snap(t, cb, time.Second, func(sn Snap) bool { return len(sn.Missions) == 1 })
	if sn.Missions[0].Squad != code || sn.Missions[0].Outcome != "" {
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

// testClient is a connection for driving g.handle directly.
func testClient(player string) *client {
	return &client{pl: platform.Player{ID: player}, player: player, snaps: make(chan []byte, 2),
		ctrl: make(chan []byte, 16), kick: make(chan struct{}, 1), cancel: func() {}}
}

// TestReleaseFrameNeverDropped: a burst of ACTION-down frames over the input limit ends
// in the release. The release changes act, so it is applied, not dropped: ACTION is not
// left held, and the tap books the agent once and does nothing else.
func TestReleaseFrameNeverDropped(t *testing.T) {
	g, f := newManual(t)
	g.mu.Lock()
	a := g.w.Avatars["a"]
	tg := addTarget(g.w, "t1", "", Frozen, Vec{a.Pos.X + 1, a.Pos.Y})
	tg.SessionID = "s1"
	g.mu.Unlock()
	cl := testClient("a")
	for range 10 {
		g.handle(cl, []byte(`{"t":"in","act":true}`))
	}
	g.handle(cl, []byte(`{"t":"in","act":false}`))
	g.mu.Lock()
	held := g.w.ctl["a"].held
	g.mu.Unlock()
	if held {
		t.Fatal("release frame dropped: ACTION still held")
	}
	for range 2 * tickHz {
		g.tick()
	}
	f.noStartOf(t, "delete")
	g.mu.Lock()
	defer g.mu.Unlock()
	if tg.State != Frozen || tg.opKind != "extract" {
		t.Fatalf("target %s op %q: want one booking", tg.State, tg.opKind)
	}
	// The same for FIRE: a dropped release would keep shooting.
	for range 10 {
		g.mu.Unlock()
		g.handle(cl, []byte(`{"t":"in","fire":true}`))
		g.mu.Lock()
	}
	g.mu.Unlock()
	g.handle(cl, []byte(`{"t":"in","fire":false}`))
	g.mu.Lock()
	if g.w.ctl["a"].in.Fire {
		t.Fatal("FIRE release dropped")
	}
}

// TestInputCoalesced: frames over the limit that change only the stick direction are
// coalesced: the newest is applied once a token frees, even if no frame follows.
func TestInputCoalesced(t *testing.T) {
	g, _ := newManual(t)
	cl := testClient("a")
	for i := 1; i <= 10; i++ {
		g.handle(cl, fmt.Appendf(nil, `{"t":"in","mx":%g}`, float64(i)/10))
	}
	g.mu.Lock()
	got := g.w.ctl["a"].in.Move.X
	g.mu.Unlock()
	if got == 1 {
		t.Fatal("every frame applied: no rate limit")
	}
	time.Sleep(120 * time.Millisecond) // tokens free up
	g.tick()
	g.mu.Lock()
	got = g.w.ctl["a"].in.Move.X
	g.mu.Unlock()
	if got != 1 {
		t.Fatalf("move %v, want the newest frame (1) applied once a token frees", got)
	}
}

func TestRoutes(t *testing.T) {
	s := newServer(t, &liveOps{})
	for _, p := range []string{"/substrate-siege", "/substrate-siege/screen", "/substrate-siege/static/app.js", "/admin/substrate-siege/api/state"} {
		resp, err := http.Get(s.srv.URL + p)
		if err != nil || resp.StatusCode != 200 {
			t.Fatalf("GET %s: %v %v", p, err, resp)
		}
		resp.Body.Close()
	}
	resp, _ := http.Get(s.srv.URL + "/substrate-siege")
	var b strings.Builder
	buf := make([]byte, 512)
	nr, _ := resp.Body.Read(buf)
	b.Write(buf[:nr])
	resp.Body.Close()
	if !strings.Contains(b.String(), "/substrate-siege/static/app.js?v=") || resp.Header.Get("Cache-Control") != "no-cache" {
		t.Fatalf("page not versioned: %s %v", b.String(), resp.Header)
	}
	resp, err := http.Post(s.srv.URL+"/admin/substrate-siege/api/reset", "", nil)
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
	g.m.perRun = 1 // one agent per run keeps the call order predictable
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
	g.w.Avatars["a"].Inv = rig("p", 3)
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
	if len(g.w.Avatars["a"].Inv) != 4 || len(g.w.pending) != 0 {
		t.Fatalf("rig not refunded: inv %v, pending %d", g.w.Avatars["a"].Inv, len(g.w.pending))
	}
}

func TestLateSpawnDeleted(t *testing.T) {
	g, f := newManual(t)
	g.mu.Lock()
	ms, err := g.start("a")
	g.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	c := f.next(t) // the spawn
	g.mu.Lock()
	g.m.finish(ms, g.w)
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
	ms, _ := g.start("a")
	g.mu.Unlock()
	release(t, f, "spawn")
	pumpUntil(t, g, "target", func() bool { return g.w.Targets["m1-1"] != nil })
	g.mu.Lock()
	g.m.finish(ms, g.w)
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

// bookTarget freezes the run's agent m1-1 and books it through the scheduler, as a tap
// next to it does.
func bookTarget(t *testing.T, g *Game) {
	t.Helper()
	pumpUntil(t, g, "target", func() bool { return g.w.Targets["m1-1"] != nil })
	g.mu.Lock()
	tg := g.w.Targets["m1-1"]
	tg.State = Frozen
	g.w.start(tg, "extract", "a")
	if !g.s.Submit("extract", tg.ID, tg.SessionID, 0, 0) {
		t.Fatal("extract refused")
	}
	g.mu.Unlock()
	pumpUntil(t, g, "booked", func() bool { return tg.State == Booked })
}

// TestResetDeletesBookedCheckpoints: a booking keeps its checkpoint (the session itself
// is already deleted by Extract). Mission cleanup leaves both alone; Reset deletes the
// checkpoint and never re-deletes the gone session.
func TestResetDeletesBookedCheckpoints(t *testing.T) {
	ops := &liveOps{}
	g := New(platform.New(nil), ops, nil)
	t.Cleanup(func() { g.s.Close() })
	g.m.perRun = 1
	g.join(platform.Player{ID: "a"})
	g.mu.Lock()
	_, err := g.start("a")
	g.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	bookTarget(t, g)
	g.mu.Lock()
	end := g.w.Now
	g.mu.Unlock()
	pumpUntil(t, g, "removed", func() bool { return g.w.Now > end+goneDelay && g.w.Targets["m1-1"] == nil })
	pumpUntil(t, g, "replacement", func() bool { return g.w.Targets["m1-2"] != nil })
	g.mu.Lock()
	if g.booked["sess-m1-1"] != "cp-sess-m1-1" {
		t.Fatalf("booked = %v, want the checkpoint kept", g.booked)
	}
	g.mu.Unlock()
	if slices.Contains(ops.called("delete"), "sess-m1-1") || len(ops.called("delete-checkpoint")) != 0 {
		t.Fatalf("deleted at cleanup: %v %v", ops.called("delete"), ops.called("delete-checkpoint"))
	}
	sessions, checkpoints, err := g.Reset(context.Background())
	if err != nil || sessions != 1 || checkpoints != 1 { // the replacement agent, the booking
		t.Fatalf("Reset = %d sessions, %d checkpoints, %v; want 1, 1", sessions, checkpoints, err)
	}
	if got := ops.called("delete-checkpoint"); !slices.Equal(got, []string{"cp-sess-m1-1"}) {
		t.Fatalf("checkpoint deletes %v", got)
	}
	if slices.Contains(ops.called("delete"), "sess-m1-1") {
		t.Fatalf("Reset deleted the already-gone session: %v", ops.called("delete"))
	}
}

// TestResetBookedStillInWorld: a target booked moments ago (its mission not yet cleaned
// up) is handled the same way.
func TestResetBookedStillInWorld(t *testing.T) {
	ops := &liveOps{}
	g := New(platform.New(nil), ops, nil)
	t.Cleanup(func() { g.s.Close() })
	g.join(platform.Player{ID: "a"})
	g.mu.Lock()
	g.start("a") // two agents
	g.mu.Unlock()
	bookTarget(t, g)
	sessions, checkpoints, err := g.Reset(context.Background())
	if err != nil || sessions < 1 || checkpoints != 1 {
		t.Fatalf("Reset = %d sessions, %d checkpoints, %v; want 1+, 1", sessions, checkpoints, err)
	}
	if slices.Contains(ops.called("delete"), "sess-m1-1") {
		t.Fatal("Reset deleted the booked (gone) session")
	}
}

// TestLateExtractCheckpointKept: an extract that lands after its target was cleaned up
// still records the checkpoint, so Reset deletes it.
func TestLateExtractCheckpointKept(t *testing.T) {
	ops := &liveOps{}
	g := New(platform.New(nil), ops, nil)
	t.Cleanup(func() { g.s.Close() })
	g.mu.Lock()
	g.inflight[opKey{"extract", "m9-1"}] = 1
	g.route(Result{Kind: "extract", Target: "m9-1", OK: true, Checkpoint: "cp-late"})
	g.mu.Unlock()
	if _, n, _ := g.Reset(context.Background()); n != 1 || !slices.Equal(ops.called("delete-checkpoint"), []string{"cp-late"}) {
		t.Fatalf("deleted %d checkpoints: %v", n, ops.called("delete-checkpoint"))
	}
}

// TestResetCheckpointParallelism: Reset deletes checkpoints at most 8 at a time.
func TestResetCheckpointParallelism(t *testing.T) {
	ops := &liveOps{cpDelay: 20 * time.Millisecond}
	g := New(platform.New(nil), ops, nil)
	t.Cleanup(func() { g.s.Close() })
	g.mu.Lock()
	for i := range 30 {
		g.booked[fmt.Sprintf("s%d", i)] = fmt.Sprintf("cp-%d", i)
	}
	g.mu.Unlock()
	if _, n, err := g.Reset(context.Background()); n != 30 || err != nil {
		t.Fatalf("deleted %d checkpoints, %v; want 30", n, err)
	}
	if ops.cpPeak > resetParallel || ops.cpPeak < 2 {
		t.Fatalf("peak %d checkpoint deletes at once; want 2..%d", ops.cpPeak, resetParallel)
	}
}

func TestLateAllyDeleted(t *testing.T) {
	g, f := newManual(t)
	g.mu.Lock()
	ms, _ := g.start("a")
	g.w.Avatars["a"].Inv = rig("p", 2)
	if err := g.build("a"); err != nil {
		t.Fatal(err)
	}
	g.m.finish(ms, g.w)
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

func TestCityAPI(t *testing.T) {
	s := newServer(t, &liveOps{})
	resp, err := http.Get(s.srv.URL + "/substrate-siege/api/city")
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("GET city: %v %v", err, resp)
	}
	defer resp.Body.Close()
	var c CityView
	if err := json.NewDecoder(resp.Body).Decode(&c); err != nil {
		t.Fatal(err)
	}
	if c.W != 59 || c.H != 35 || len(c.Tiles) != c.H || len(c.Tiles[0]) != c.W || c.Tiles[11][27] != DispatchTile {
		t.Fatalf("city = %d x %d, %d rows", c.W, c.H, len(c.Tiles))
	}
	if c.Vault != (RectView{51, 27, 5, 5}) || c.Dispatch != (RectView{27, 11, 5, 5}) {
		t.Fatalf("vault %+v dispatch %+v", c.Vault, c.Dispatch)
	}
	if len(c.Districts) != 2 || c.Districts["node-2"] != (RectView{29, 0, 30, 35}) {
		t.Fatalf("districts = %+v", c.Districts)
	}
}

func TestSnapHeadScore(t *testing.T) {
	s := newServer(t, &liveOps{})
	ada := s.g.p.Players.Join("Ada")
	c := s.mustDial(t, &ada, "")
	if sn := snap(t, c, time.Second, nil); sn.Best != 0 || sn.Score != 0 || sn.Rank != 0 {
		t.Fatalf("fresh head = %+v", sn.SnapHead)
	}
	s.g.mu.Lock()
	ms, err := s.g.start(ada.ID)
	if err != nil {
		t.Fatal(err)
	}
	ms.Scores[ada.ID] = 4
	s.g.m.player(ada.ID, s.g.w).Best = 6
	s.g.mu.Unlock()
	snap(t, c, time.Second, func(sn Snap) bool { return sn.Score == 4 && sn.Best == 6 && sn.Rank == 1 })
}

// TestModuleImportsVersioned: a module's relative imports carry the hash of what is
// served for them, and a page's link to the module changes when only a dependency does.
func TestModuleImportsVersioned(t *testing.T) {
	web := func(dep string) fstest.MapFS {
		return fstest.MapFS{
			"phone.html":      {Data: []byte(`<script type="module" src="/substrate-siege/static/city.js"></script>`)},
			"screen.html":     {Data: []byte(`screen`)},
			"city.js":         {Data: []byte(`import * as T from "./vendor/t.js";` + "\n" + `import { town } from "./town.js";`)},
			"town.js":         {Data: []byte(dep)},
			"vendor/t.js":     {Data: []byte(`export const t = 1;`)},
			"kagent-icon.svg": {Data: []byte(`<svg/>`)},
		}
	}
	get := func(w fstest.MapFS, p string) string {
		g := New(platform.New(nil), &liveOps{}, nil)
		mux := http.NewServeMux()
		g.Routes(mux, w)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest("GET", p, nil))
		if rec.Code != 200 {
			t.Fatalf("GET %s = %d", p, rec.Code)
		}
		return rec.Body.String()
	}
	a, b := web(`export const town = 1;`), web(`export const town = 2;`)
	js := get(a, "/substrate-siege/static/city.js")
	if !regexp.MustCompile(`from "\./vendor/t\.js\?v=[0-9a-f]{8}"`).MatchString(js) || !regexp.MustCompile(`from "\./town\.js\?v=[0-9a-f]{8}"`).MatchString(js) {
		t.Fatalf("imports not versioned: %s", js)
	}
	if js == get(b, "/substrate-siege/static/city.js") {
		t.Fatal("a changed dependency did not change the importing module")
	}
	if get(a, "/substrate-siege") == get(b, "/substrate-siege") {
		t.Fatal("a changed dependency did not change the page's link to the module")
	}
	if get(a, "/substrate-siege/static/town.js") != `export const town = 1;` {
		t.Fatal("a module without imports must be served as is")
	}
}

func TestPingPong(t *testing.T) {
	s := newServer(t, &liveOps{})
	pl := s.g.p.Players.Join("Ada")
	c := s.mustDial(t, &pl, "")
	send(t, c, map[string]any{"t": "ping", "code": "42"})
	var id string
	_ = json.Unmarshal(frame(t, c, "pong", time.Second)["id"], &id)
	if id != "42" {
		t.Fatalf("pong id = %q, want 42", id)
	}
}

// TestStartupSweep: Run deletes every session the agents have (orphans of an earlier
// process) and their checkpoints, off the tick loop.
func TestStartupSweep(t *testing.T) {
	ops := &liveOps{sessions: []string{"orphan-1", "orphan-2", "orphan-3"},
		checkpoints: map[string][]string{"orphan-2": {"cp-x", "cp-y"}}}
	s := newServer(t, ops)
	deadline := time.Now().Add(3 * time.Second)
	for len(ops.called("delete")) < 3 || len(ops.called("delete-checkpoint")) < 2 {
		if time.Now().After(deadline) {
			t.Fatalf("deleted %v, want the 3 orphans", ops.called("delete"))
		}
		time.Sleep(10 * time.Millisecond)
	}
	if got := ops.called("delete"); !slices.Equal(slices.Sorted(slices.Values(got)), []string{"orphan-1", "orphan-2", "orphan-3"}) {
		t.Fatalf("deleted %v", got)
	}
	if got := ops.called("delete-checkpoint"); !slices.Equal(slices.Sorted(slices.Values(got)), []string{"cp-x", "cp-y"}) {
		t.Fatalf("deleted checkpoints %v", got)
	}
	// The game ticks on and plays as usual.
	pl := s.g.p.Players.Join("Ada")
	c := s.mustDial(t, &pl, "")
	send(t, c, map[string]any{"t": "start"})
	snap(t, c, time.Second, func(sn Snap) bool { return len(sn.Missions) == 1 })
}

// TestStartupSweepListError: a failed list is logged and the game still runs.
func TestStartupSweepListError(t *testing.T) {
	ops := &liveOps{listErr: errors.New("kagent down")}
	s := newServer(t, ops)
	pl := s.g.p.Players.Join("Ada")
	c := s.mustDial(t, &pl, "")
	send(t, c, map[string]any{"t": "start"})
	snap(t, c, time.Second, func(sn Snap) bool { return len(sn.Missions) == 1 })
	if got := ops.called("delete"); len(got) != 0 {
		t.Fatalf("deleted %v after a failed list", got)
	}
}

// TestStartupSweepGatesStarts: while the sweep is still listing, missions and rigs wait
// (a session spawned now could be listed and deleted as an orphan).
func TestStartupSweepGatesStarts(t *testing.T) {
	defer func(d time.Duration) { sweepListTimeout = d }(sweepListTimeout)
	sweepListTimeout = 300 * time.Millisecond
	ops := &liveOps{block: map[string]bool{"list": true}}
	s := newServer(t, ops)
	pl := s.g.p.Players.Join("Ada")
	c := s.mustDial(t, &pl, "")
	send(t, c, map[string]any{"t": "start"})
	var msg string
	_ = json.Unmarshal(frame(t, c, "err", time.Second)["msg"], &msg)
	if msg != "starting up, try again" {
		t.Fatalf("err = %q", msg)
	}
	time.Sleep(400 * time.Millisecond) // the list times out; play opens
	send(t, c, map[string]any{"t": "start"})
	snap(t, c, time.Second, func(sn Snap) bool { return len(sn.Missions) == 1 })
}

// ---- mission limits (squad hopping) ----

// TestSquadChangeRefusedDuringMission: while the player's squad has a run going,
// create, join and solo are refused; once it ends they work again.
func TestSquadChangeRefusedDuringMission(t *testing.T) {
	g, _ := newManual(t)
	g.join(platform.Player{ID: "b"})
	g.mu.Lock()
	defer g.mu.Unlock()
	code, err := g.squad("b", "create", "")
	if err != nil {
		t.Fatal(err)
	}
	ms, err := g.start("a")
	if err != nil {
		t.Fatal(err)
	}
	for _, op := range []string{"create", "join", "solo"} {
		if _, err := g.squad("a", op, code); err == nil {
			t.Errorf("squad %s accepted while a's mission runs", op)
		}
	}
	if a := g.w.Avatars["a"]; a.Squad != "solo-a" || g.q.Of("a") != "solo-a" {
		t.Fatalf("squad changed to %q / %q", a.Squad, g.q.Of("a"))
	}
	g.m.finish(ms, g.w)
	if got, err := g.squad("a", "join", code); err != nil || got != code {
		t.Fatalf("join after the mission = %q, %v", got, err)
	}
}

// TestOneRunPerSquad: a squad has at most one run going; once it ends, play again.
func TestOneRunPerSquad(t *testing.T) {
	g, _ := newManual(t)
	g.mu.Lock()
	defer g.mu.Unlock()
	ms, err := g.start("a")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := g.start("a"); err == nil {
		t.Fatal("second run started for the same squad")
	}
	g.m.finish(ms, g.w)
	if _, err := g.start("a"); err != nil {
		t.Fatalf("start after the first ended: %v", err)
	}
}

// TestLiveTargetCap: at most maxLiveTargets agents live in the city; a freed slot goes
// to the run with the fewest agents.
func TestLiveTargetCap(t *testing.T) {
	ops := &liveOps{}
	g := New(platform.New(nil), ops, nil)
	t.Cleanup(func() { g.s.Close() })
	var ms []*Mission
	g.mu.Lock()
	for i := range maxLiveTargets + 6 { // more solo runs than the city has agents for
		id := fmt.Sprintf("p%02d", i)
		g.joinLocked(platform.Player{ID: id})
		m, err := g.start(id)
		if err != nil {
			t.Fatal(err)
		}
		ms = append(ms, m)
	}
	g.mu.Unlock()
	pumpUntil(t, g, "city full", func() bool { return len(g.w.Targets) == maxLiveTargets })
	for range 30 {
		g.tick()
	}
	g.mu.Lock()
	if n := len(g.w.Targets); n != maxLiveTargets || len(ops.called("spawn")) != maxLiveTargets {
		t.Fatalf("%d agents, %d spawns; want %d", n, len(ops.called("spawn")), maxLiveTargets)
	}
	empty := func() int {
		n := 0
		for _, m := range ms {
			if len(m.Targets) == 0 {
				n++
			}
		}
		return n
	}
	before := empty()
	// The first run's agent gets away: its free slot goes to a run with none.
	g.w.Targets[ms[0].Targets[0]].State = Escaped
	g.mu.Unlock()
	pumpUntil(t, g, "refilled", func() bool { return len(ops.called("spawn")) > maxLiveTargets })
	g.mu.Lock()
	defer g.mu.Unlock()
	if before == 0 || empty() != before-1 || len(ms[0].Targets) != 2 {
		t.Fatalf("empty runs %d → %d, first run has %d agents", before, empty(), len(ms[0].Targets))
	}
}

// TestShutdownDeletesBookedCheckpoints: a booked target's session is gone, so no later
// startup sweep can find its checkpoint; shutdown deletes it.
func TestShutdownDeletesBookedCheckpoints(t *testing.T) {
	ops := &liveOps{}
	s := newServer(t, ops)
	s.g.mu.Lock()
	s.g.booked["s-old"] = "cp-old"
	addTarget(s.g.w, "t1", "", Booked, Vec{}).Checkpoint = "cp-new"
	s.g.mu.Unlock()
	s.cancel()
	select {
	case <-s.g.Stopped():
	case <-time.After(3 * time.Second):
		t.Fatal("not stopped")
	}
	if got := slices.Sorted(slices.Values(ops.called("delete-checkpoint"))); !slices.Equal(got, []string{"cp-new", "cp-old"}) {
		t.Fatalf("checkpoint deletes %v", got)
	}
}

// TestEscapedAgentCleanedUp: an agent that got away stays on the map goneDelay, then its
// session is deleted while the run goes on.
func TestEscapedAgentCleanedUp(t *testing.T) {
	g, f := newManual(t)
	g.mu.Lock()
	ms, _ := g.start("a")
	g.mu.Unlock()
	release(t, f, "spawn")
	pumpUntil(t, g, "agent", func() bool { return g.w.Targets["m1-1"] != nil })
	g.mu.Lock()
	g.w.Targets["m1-1"].State = Escaped
	at := g.w.Now
	g.mu.Unlock()
	pumpUntil(t, g, "removed", func() bool { return g.w.Targets["m1-1"] == nil })
	g.mu.Lock()
	if g.w.Now-at < goneDelay-0.2 || ms.Outcome != "" {
		t.Fatalf("removed after %.1f s, outcome %q", g.w.Now-at, ms.Outcome)
	}
	g.mu.Unlock()
	for {
		if c := f.next(t); c.kind == "delete" {
			if c.id != "sess-m1-1" {
				t.Fatalf("deleted %s", c.id)
			}
			return
		}
	}
}

// TestFullRigAutoBuilds: carrying a board, a CPU, RAM and a drive deploys an ally
// without a button.
func TestFullRigAutoBuilds(t *testing.T) {
	g, f := newManual(t)
	g.mu.Lock()
	g.w.Avatars["a"].Inv = append(rig("p", 3), Part{ID: "spare", Kind: "cpu"})
	g.mu.Unlock()
	g.tick()
	release(t, f, "ally")
	pumpUntil(t, g, "ally", func() bool { return len(g.w.Allies) == 1 })
	g.mu.Lock()
	defer g.mu.Unlock()
	if inv := g.w.Avatars["a"].Inv; len(inv) != 1 || inv[0].ID != "spare" {
		t.Fatalf("inventory after build = %v", inv)
	}
}
