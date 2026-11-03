package gatewaydef

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"github.com/sebbycorp/kubecon-adventures/arcade/internal/kagent"
	"github.com/sebbycorp/kubecon-adventures/arcade/internal/platform"
)

func TestRulesAreConsistent(t *testing.T) {
	r := TheRules
	m := r.Map
	lane := map[[2]int]bool{}
	for i := 1; i < len(m.Path); i++ {
		a, b := m.Path[i-1], m.Path[i]
		if a[0] != b[0] && a[1] != b[1] {
			t.Fatalf("path leg %v -> %v is not straight", a, b)
		}
		for x, y := a[0], a[1]; ; {
			lane[[2]int{x, y}] = true
			if x == b[0] && y == b[1] {
				break
			}
			x += sign(b[0] - x)
			y += sign(b[1] - y)
		}
	}
	end := m.Path[len(m.Path)-1]
	if end[1] != m.H-1 {
		t.Fatalf("lane ends at %v, not the bottom row", end)
	}
	seen := map[[2]int]bool{}
	for _, p := range m.Pads {
		switch {
		case p[0] < 0 || p[1] < 0 || p[0] >= m.W || p[1] >= m.H:
			t.Fatalf("pad %v is off the map", p)
		case lane[p]:
			t.Fatalf("pad %v is on the lane", p)
		case seen[p]:
			t.Fatalf("pad %v twice", p)
		}
		seen[p] = true
	}
	for i, w := range r.Waves {
		if w.N != i+1 || w.Count < 1 || w.Gap <= 0 || w.HPScale < 1 || len(w.Mix) == 0 {
			t.Fatalf("wave %d is malformed: %+v", i+1, w)
		}
		for _, id := range w.Mix {
			if _, ok := r.creep(id); !ok {
				t.Fatalf("wave %d has unknown creep %q", w.N, id)
			}
		}
	}
	if !r.Waves[len(r.Waves)-1].Boss {
		t.Fatal("the last wave has no boss")
	}
	towers := map[string]bool{}
	for _, tw := range r.Towers {
		towers[tw.ID] = true
	}
	for _, c := range r.Creeps {
		if !towers[c.Weak] {
			t.Fatalf("creep %s is weak to unknown tower %q", c.ID, c.Weak)
		}
	}
	for _, c := range append(slices.Clone(r.Creeps), r.Boss) {
		for _, kid := range []string{c.Split, c.Summon} {
			if _, ok := r.creep(kid); kid != "" && !ok {
				t.Fatalf("creep %s makes unknown creep %q", c.ID, kid)
			}
		}
		if c.Lives < 1 {
			t.Fatalf("creep %s costs no lives", c.ID)
		}
	}
	for _, id := range r.Endless.Mix {
		if _, ok := r.creep(id); !ok {
			t.Fatalf("endless mix has unknown creep %q", id)
		}
	}
	if len(towers) != 4 || len(r.Creeps) != 6 || len(r.Waves) != 15 {
		t.Fatalf("want 4 towers, 6 creeps and 15 waves: %d %d %d", len(towers), len(r.Creeps), len(r.Waves))
	}
	// Every tower is somebody's weakness, so a run needs all four.
	weak := map[string]bool{}
	for _, c := range r.Creeps {
		weak[c.Weak] = true
	}
	if len(weak) != 4 {
		t.Fatalf("towers that are some creep's weakness: %v", weak)
	}
}

func sign(n int) int {
	switch {
	case n > 0:
		return 1
	case n < 0:
		return -1
	}
	return 0
}

func TestMaxScore(t *testing.T) {
	r := TheRules
	// Wave 1 reached: 10 jailbreakers at 10, nothing cleared.
	if got := r.MaxScore(1); got != 100 {
		t.Fatalf("MaxScore(1) = %d, want 100", got)
	}
	// Wave 2 reached: wave 1's creeps and clear bonus, plus wave 2's 14 at 12.
	if got := r.MaxScore(2); got != 100+15+14*12 {
		t.Fatalf("MaxScore(2) = %d, want %d", got, 100+15+14*12)
	}
	// A burst burner is worth its points and the two burners it splits into; the boss,
	// its points and the obfuscated jailbreaks it summons twice.
	if v := r.value("burst", 0); v != 18+2*12 {
		t.Fatalf("burst value %d", v)
	}
	if v := r.value("boss", 0); v != 600+2*4*25 {
		t.Fatalf("boss value %d", v)
	}
	prev := 0
	for w := 1; w <= len(r.Waves)+5; w++ {
		if s := r.MaxScore(w); s <= prev {
			t.Fatalf("MaxScore(%d) = %d is not above %d", w, s, prev)
		} else {
			prev = s
		}
	}
	if r.MaxScore(MaxWave+50) != r.MaxScore(MaxWave) || r.MaxScore(-3) != 0 {
		t.Fatal("MaxScore does not clamp the wave")
	}
	if r.MinSeconds(1) != 0 || r.MinSeconds(2) <= 0 || r.MinSeconds(17) <= r.MinSeconds(16) {
		t.Fatalf("MinSeconds: %v %v %v %v", r.MinSeconds(1), r.MinSeconds(2), r.MinSeconds(16), r.MinSeconds(17))
	}
}

func TestEndlessWaves(t *testing.T) {
	r := TheRules
	last := r.Waves[len(r.Waves)-1]
	if w := r.WaveAt(len(r.Waves)); w.N != last.N || w.Name != last.Name {
		t.Fatalf("WaveAt(last) = %+v", w)
	}
	prev := last
	for n := len(r.Waves) + 1; n <= len(r.Waves)+10; n++ {
		w := r.WaveAt(n)
		k := n - len(r.Waves)
		if w.N != n || w.Name != "Endless "+strconv.Itoa(k) || w.Count <= prev.Count && k > 1 || w.HPScale <= prev.HPScale || w.Gap < r.Endless.MinGap {
			t.Fatalf("endless wave %d: %+v after %+v", n, w, prev)
		}
		if w.Boss != (k%r.Endless.BossEvery == 0) {
			t.Fatalf("endless wave %d boss = %v", n, w.Boss)
		}
		prev = w
	}
}

// fakeWeb and fakeSub are probers that count calls.
type fakeWeb struct {
	mu           sync.Mutex
	guard, limit int
}

func (f *fakeWeb) Guard(context.Context) Outcome {
	f.mu.Lock()
	f.guard++
	f.mu.Unlock()
	return Outcome{Tower: "guard", Real: true, OK: true, Status: 403, Label: "403 prompt guard", Ms: 9, At: 1}
}
func (f *fakeWeb) Limit(context.Context, string) Outcome {
	f.mu.Lock()
	f.limit++
	f.mu.Unlock()
	return Outcome{Tower: "limit", Real: true, OK: true, Status: 429, Label: "429 rate limited", Ms: 4, At: 1}
}

type fakeSub struct{ suspend int }

func (f *fakeSub) Suspend(context.Context) Outcome {
	f.suspend++
	return Outcome{Tower: "suspend", Real: true, OK: true, Label: "Substrate Suspend", Ms: 120, At: 1}
}
func (f *fakeSub) Egress(context.Context) Outcome {
	return Outcome{Tower: "egress", Real: true, OK: true, Label: "atenet-egress refused", Ms: 3000, At: 1}
}

type clock struct{ t time.Time }

func (c *clock) now() time.Time      { return c.t }
func (c *clock) add(d time.Duration) { c.t = c.t.Add(d) }

func newTestGame() (*Game, *fakeWeb, *fakeSub, *clock) {
	web, sub, c := &fakeWeb{}, &fakeSub{}, &clock{t: time.Unix(1_800_000_000, 0)}
	g := New(platform.New(nil), web, sub)
	g.now = c.now
	return g, web, sub, c
}

var ann = platform.Player{ID: "p-ann", Nick: "Ann", Color: "green"}
var bob = platform.Player{ID: "p-bob", Nick: "Bob", Color: "blue"}

func TestBlockBudget(t *testing.T) {
	g, web, sub, c := newTestGame()
	ctx := context.Background()
	run, err := g.Start(ann)
	if err != nil {
		t.Fatal(err)
	}
	// The burst: three real calls, then the fourth gets the last real guard outcome.
	for i := range 3 {
		o, err := g.Block(ctx, ann, run, "guard", "jailbreak")
		if err != nil || !o.Real || o.Label != "403 prompt guard" {
			t.Fatalf("call %d: %+v %v", i, o, err)
		}
	}
	o, err := g.Block(ctx, ann, run, "guard", "jailbreak")
	if err != nil || o.Real || !o.Cached || o.Label != "403 prompt guard" || web.guard != 3 {
		t.Fatalf("over budget: %+v %v (guard calls %d)", o, err, web.guard)
	}
	// Half a second refills one call.
	c.add(500 * time.Millisecond)
	if o, _ := g.Block(ctx, ann, run, "suspend", "runaway"); !o.Real || sub.suspend != 1 {
		t.Fatalf("after refill: %+v", o)
	}
	// Another player has their own budget.
	brun, _ := g.Start(bob)
	if o, _ := g.Block(ctx, bob, brun, "limit", "burner"); !o.Real || web.limit != 1 {
		t.Fatalf("bob: %+v", o)
	}
	// Bad input and someone else's run are refused.
	if _, err := g.Block(ctx, ann, run, "laser", "jailbreak"); !errors.Is(err, errBadKill) {
		t.Fatalf("unknown tower: %v", err)
	}
	if _, err := g.Block(ctx, ann, run, "guard", "dragon"); !errors.Is(err, errBadKill) {
		t.Fatalf("unknown creep: %v", err)
	}
	if _, err := g.Block(ctx, ann, brun, "guard", "jailbreak"); !errors.Is(err, errNoRun) {
		t.Fatalf("bob's run: %v", err)
	}
	s := g.Snapshot()
	if s.Totals["guard"] != 3 || s.Totals["suspend"] != 1 || s.Totals["limit"] != 1 || len(s.Feed) != 5 || s.Feed[0].Nick != "Ann" {
		t.Fatalf("snapshot: totals %v feed %d", s.Totals, len(s.Feed))
	}
}

func TestFinishChecksTheScore(t *testing.T) {
	g, _, _, c := newTestGame()
	r := TheRules
	run, _ := g.Start(ann)
	// Too fast: wave 5 can't be reached in 10 s.
	c.add(10 * time.Second)
	res, err := g.Finish(ann, run, Finish{Wave: 5, Score: 100, Lives: 10})
	if err != nil || res.Accepted {
		t.Fatalf("too fast: %+v %v", res, err)
	}
	// A run can only finish once.
	if _, err := g.Finish(ann, run, Finish{Wave: 5, Score: 100}); !errors.Is(err, errNoRun) {
		t.Fatalf("second finish: %v", err)
	}
	// Too high a score for the wave.
	run, _ = g.Start(ann)
	c.add(time.Hour)
	run2, _ := g.Start(ann)
	_ = run
	if res, _ := g.Finish(ann, run2, Finish{Wave: 3, Score: r.MaxScore(3) + 1, Lives: 5}); res.Accepted {
		t.Fatal("impossible score accepted")
	}
	// Good runs rank; the next one up is the score to beat.
	runB, _ := g.Start(bob)
	c.add(10 * time.Minute)
	if res, _ := g.Finish(bob, runB, Finish{Wave: 6, Score: 900, Lives: 12}); !res.Accepted || res.Rank != 1 || res.Best != 900 {
		t.Fatalf("bob: %+v", res)
	}
	runA, _ := g.Start(ann)
	c.add(10 * time.Minute)
	res, _ = g.Finish(ann, runA, Finish{Wave: 5, Score: 700, Lives: 3})
	if !res.Accepted || res.Rank != 2 || res.Beat == nil || res.Beat.Nick != "Bob" || res.Beat.Score != 900 {
		t.Fatalf("ann: %+v", res)
	}
	// A worse run keeps the best; Won needs a full clear with lives left.
	runA, _ = g.Start(ann)
	c.add(10 * time.Minute)
	if res, _ := g.Finish(ann, runA, Finish{Wave: 2, Score: 50, Lives: 0, Won: true}); res.Best != 700 {
		t.Fatalf("worse run: %+v", res)
	}
	b := g.Snapshot().Board
	if len(b) != 2 || b[0].Nick != "Bob" || b[1].Score != 700 || b[1].Won {
		t.Fatalf("board: %+v", b)
	}
}

func TestStartCapsRuns(t *testing.T) {
	g, _, _, c := newTestGame()
	for i := range maxRuns {
		if _, err := g.Start(platform.Player{ID: string(rune('A' + i)), Nick: "x"}); err != nil {
			t.Fatalf("run %d: %v", i, err)
		}
	}
	if _, err := g.Start(ann); !errors.Is(err, errFull) {
		t.Fatalf("full: %v", err)
	}
	c.add(runTTL + time.Second)
	if _, err := g.Start(ann); err != nil {
		t.Fatalf("after the TTL: %v", err)
	}
}

func TestHTTPProber(t *testing.T) {
	var limitHits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/guard"):
			var body struct{ Messages []struct{ Content string } }
			_ = json.NewDecoder(r.Body).Decode(&body)
			if strings.Contains(strings.ToLower(body.Messages[0].Content), "ignore") {
				w.WriteHeader(http.StatusForbidden)
				_, _ = w.Write([]byte("Blocked by agentgateway prompt guard"))
				return
			}
			_, _ = w.Write([]byte(`{}`))
		case strings.HasPrefix(r.URL.Path, "/limit"):
			if r.Header.Get("x-gatedef-player") == "" {
				t.Error("no player header on the limit call")
			}
			if limitHits++; limitHits > 1 {
				w.WriteHeader(http.StatusTooManyRequests)
			}
		}
	}))
	defer srv.Close()
	h := &HTTPProber{GuardURL: srv.URL + "/guard", LimitURL: srv.URL + "/limit", Client: srv.Client()}
	for range len(jailbreaks) {
		if o := h.Guard(context.Background()); !o.Real || !o.OK || o.Status != 403 || o.Label != "403 prompt guard" {
			t.Fatalf("guard: %+v", o)
		}
	}
	o := h.Limit(context.Background(), "p1")
	if !o.Real || !o.OK || o.Status != 429 || o.Detail != "after 1 allowed" {
		t.Fatalf("limit: %+v", o)
	}
	down := &HTTPProber{GuardURL: "http://127.0.0.1:1/guard", LimitURL: "http://127.0.0.1:1/limit", Client: &http.Client{Timeout: time.Second}}
	if o := down.Guard(context.Background()); o.Real || o.OK {
		t.Fatalf("unreachable guard counted: %+v", o)
	}
}

// fakeOps is the rogue agent's kagent, in memory.
type fakeOps struct {
	mu        sync.Mutex
	n         int
	sessions  map[string]string // id -> RUNNING, SUSPENDED
	toolOut   string
	suspendEr error
	prompts   []string
}

func (f *fakeOps) CreateSession(_ context.Context, _ string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.n++
	id := "s" + string(rune('0'+f.n))
	f.sessions[id] = "READY"
	return id, nil
}
func (f *fakeOps) Prompt(_ context.Context, id, text string) (kagent.Reply, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.prompts = append(f.prompts, text)
	f.sessions[id] = "RUNNING"
	return kagent.Reply{Text: "ok", Tools: []kagent.ToolCall{{Name: "bash", Output: f.toolOut}}}, nil
}
func (f *fakeOps) Suspend(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.suspendEr != nil {
		return f.suspendEr
	}
	f.sessions[id] = "SUSPENDED"
	return nil
}
func (f *fakeOps) Resume(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sessions[id] = "RUNNING"
	return nil
}
func (f *fakeOps) Delete(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.sessions, id)
	return nil
}
func (f *fakeOps) ListSessionIDs(context.Context) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var ids []string
	for id := range f.sessions {
		ids = append(ids, id)
	}
	return ids, nil
}
func (f *fakeOps) count(state string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, s := range f.sessions {
		if s == state {
			n++
		}
	}
	return n
}

func waitFor(t *testing.T, what string, ok func() bool) {
	t.Helper()
	for range 200 {
		if ok() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func TestAgentProberSuspendCycle(t *testing.T) {
	old := resumeAfter
	resumeAfter = 50 * time.Millisecond
	defer func() { resumeAfter = old }()
	ops := &fakeOps{sessions: map[string]string{"orphan": "SUSPENDED"}}
	a := NewAgentProber(ops)
	a.Sweep(context.Background())
	waitFor(t, "the pool to prime", func() bool { return ops.count("RUNNING") == runaways })
	if _, ok := ops.sessions["orphan"]; ok {
		t.Fatal("the startup sweep left an orphan")
	}
	for i := range runaways {
		if o := a.Suspend(context.Background()); !o.Real || !o.OK || o.Label != "Substrate Suspend" {
			t.Fatalf("suspend %d: %+v", i, o)
		}
	}
	// Every runaway is asleep: the next kill reuses the last real outcome.
	if o := a.Suspend(context.Background()); o.Real || !o.Cached {
		t.Fatalf("pool empty: %+v", o)
	}
	waitFor(t, "the runaways to resume", func() bool { return ops.count("RUNNING") == runaways })
	// A failed suspend is not counted as real, and the runaway stays usable.
	ops.mu.Lock()
	ops.suspendEr = errors.New("busy")
	ops.mu.Unlock()
	if o := a.Suspend(context.Background()); o.Real {
		t.Fatalf("failed suspend counted: %+v", o)
	}
	a.Close(context.Background())
	if n := len(ops.sessions); n != 0 {
		t.Fatalf("Close left %d sessions", n)
	}
}

func TestAgentProberEgress(t *testing.T) {
	old := probeEvery
	probeEvery = time.Hour
	defer func() { probeEvery = old }()
	ops := &fakeOps{sessions: map[string]string{}, toolOut: "wget: bad address 'example.com'"}
	a := NewAgentProber(ops)
	if o := a.Egress(context.Background()); o.Real || o.Label != "egress probe running…" {
		t.Fatalf("first call: %+v", o)
	}
	var o Outcome
	waitFor(t, "the probe", func() bool {
		o = a.Egress(context.Background())
		return o.At != 0 && o.Label != "egress probe running…"
	})
	if !o.Real || !o.OK || o.Label != "atenet-egress refused" || !strings.Contains(o.Detail, "bad address") {
		t.Fatalf("probe result: %+v", o)
	}
	if o2 := a.Egress(context.Background()); o2.Real || !o2.Cached {
		t.Fatalf("second call should be cached: %+v", o2)
	}
	ops.mu.Lock()
	n, cmd := len(ops.prompts), ops.prompts[0]
	ops.mu.Unlock()
	if n != 1 || !strings.Contains(cmd, "example.com") {
		t.Fatalf("probe prompts: %d %q", n, cmd)
	}
	waitFor(t, "the probe session to sleep", func() bool { return ops.count("SUSPENDED") == 1 })

	// A host that answers is reported as a miss, not a block.
	ops2 := &fakeOps{sessions: map[string]string{}, toolOut: "<!doctype html><title>Example Domain</title>"}
	b := NewAgentProber(ops2)
	b.Egress(context.Background())
	waitFor(t, "the second probe", func() bool {
		o = b.Egress(context.Background())
		return o.At != 0 && o.Label != "egress probe running…"
	})
	if o.OK || !strings.HasPrefix(o.Label, "MISSED") {
		t.Fatalf("leaky egress: %+v", o)
	}
}

func TestRoutes(t *testing.T) {
	g, _, _, _ := newTestGame()
	pl := g.p.Players.Join("Ann")
	mux := http.NewServeMux()
	web := fstest.MapFS{
		"phone.html":  {Data: []byte(`<script src="/gateway-defense/static/td.js"></script>`)},
		"screen.html": {Data: []byte("screen")},
		"td.js":       {Data: []byte("//")},
	}
	g.Routes(mux, web)
	do := func(method, path, body string, cookie bool, hdr ...string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		if cookie {
			req.AddCookie(&http.Cookie{Name: platform.PlayerCookie, Value: pl.ID})
		}
		for i := 0; i+1 < len(hdr); i += 2 {
			req.Header.Set(hdr[i], hdr[i+1])
		}
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		return w
	}
	if w := do("GET", "/gateway-defense", "", false); w.Code != 200 || !strings.Contains(w.Body.String(), "td.js?v=") {
		t.Fatalf("phone page: %d %s", w.Code, w.Body)
	}
	if w := do("GET", "/gateway-defense/api/rules", "", false); w.Code != 200 || !strings.Contains(w.Body.String(), `"Prompt Guard"`) {
		t.Fatalf("rules: %d", w.Code)
	}
	if w := do("POST", "/gateway-defense/api/start", "", false); w.Code != 401 {
		t.Fatalf("start without joining: %d", w.Code)
	}
	w := do("POST", "/gateway-defense/api/start", "", true)
	var st struct{ Run string }
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &st) != nil || st.Run == "" {
		t.Fatalf("start: %d %s", w.Code, w.Body)
	}
	w = do("POST", "/gateway-defense/api/block", `{"run":"`+st.Run+`","tower":"guard","creep":"jailbreak"}`, true)
	if w.Code != 200 || !strings.Contains(w.Body.String(), "403 prompt guard") {
		t.Fatalf("block: %d %s", w.Code, w.Body)
	}
	if w := do("GET", "/gateway-defense/api/board", "", true); w.Code != 200 || !strings.Contains(w.Body.String(), `"rows":[]`) {
		t.Fatalf("board: %d %s", w.Code, w.Body)
	}
	if w := do("GET", "/gateway-defense/api/board", "", false); w.Code != 200 {
		t.Fatalf("board without joining: %d", w.Code)
	}
	if w := do("POST", "/gateway-defense/sink/guard", `{"messages":[]}`, false); w.Code != 200 || g.sink["guard"] != 1 {
		t.Fatalf("sink: %d %v", w.Code, g.sink)
	}
	if w := do("POST", "/gateway-defense/sink/other", ``, false); w.Code != 404 {
		t.Fatalf("unknown sink: %d", w.Code)
	}
	// Public big screens are capped; LAN ones are not.
	for range maxScreens {
		if _, ok := g.screen(true); !ok {
			t.Fatal("screen refused under the cap")
		}
	}
	if w := do("GET", "/gateway-defense/api/events", "", false, "cf-connecting-ip", "1.2.3.4"); w.Code != 429 {
		t.Fatalf("screen over the cap: %d", w.Code)
	}
	if _, ok := g.screen(false); !ok {
		t.Fatal("LAN screen refused")
	}
}

func TestSettleRetriesUnsettled(t *testing.T) {
	n := 0
	err := settle(context.Background(), func() error {
		if n++; n < 3 {
			return errors.New("rpc error: code = FailedPrecondition desc = runtime cleanup or lifecycle work is not yet settled")
		}
		return nil
	})
	if err != nil || n != 3 {
		t.Fatalf("settle: err=%v after %d calls", err, n)
	}
	n = 0
	if err := settle(context.Background(), func() error { n++; return errors.New("boom") }); err == nil || n != 1 {
		t.Fatalf("other errors: err=%v after %d calls", err, n)
	}
}

// TestDumpRules writes the rules as JSON to $GATEDEF_RULES_OUT, for balance.mjs.
func TestDumpRules(t *testing.T) {
	out := os.Getenv("GATEDEF_RULES_OUT")
	if out == "" {
		t.Skip("set GATEDEF_RULES_OUT to dump the rules")
	}
	b, err := json.Marshal(TheRules)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(out, b, 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestFinishEndlessAndBoard(t *testing.T) {
	g, _, _, c := newTestGame()
	r := TheRules
	// A run that beat the boss and fell in endless wave 3 is a win, and possible.
	wave := len(r.Waves) + 3
	run, _ := g.Start(ann)
	c.add(time.Hour)
	res, _ := g.Finish(ann, run, Finish{Wave: wave, Score: r.MaxScore(wave), Lives: 0, Won: true})
	if !res.Accepted || res.Rank != 1 || len(res.Top) != 1 || !res.Top[0].Won {
		t.Fatalf("endless finish: %+v", res)
	}
	// Claiming a win before the boss wave is not one; a wave past MaxWave is refused.
	run, _ = g.Start(bob)
	c.add(time.Hour)
	if res, _ := g.Finish(bob, run, Finish{Wave: 7, Score: 500, Lives: 3, Won: true}); !res.Accepted || res.Top[1].Won {
		t.Fatalf("early win: %+v", res)
	}
	run, _ = g.Start(bob)
	c.add(time.Hour)
	if res, _ := g.Finish(bob, run, Finish{Wave: MaxWave + 1, Score: 1}); res.Accepted {
		t.Fatal("wave past MaxWave accepted")
	}
	// The board marks the asking player's row, and reports their rank.
	b := g.Leaderboard(bob.ID)
	if len(b.Rows) != 2 || b.Rows[0].Nick != "Ann" || b.Rows[0].You || !b.Rows[1].You || b.Me == nil || b.Me.Rank != 2 {
		t.Fatalf("board for Bob: %+v", b)
	}
	if b := g.Leaderboard(""); b.Me != nil || len(b.Rows) != 2 {
		t.Fatalf("board for nobody: %+v", b)
	}
	// More than boardRows players: the rows stop at boardRows; a player further down still
	// gets their rank.
	for i := range boardRows + 2 {
		p := platform.Player{ID: "p" + strconv.Itoa(i), Nick: "P" + strconv.Itoa(i)}
		run, _ := g.Start(p)
		c.add(time.Hour)
		g.Finish(p, run, Finish{Wave: 9, Score: 1000 + i, Lives: 1})
	}
	if b := g.Leaderboard(bob.ID); len(b.Rows) != boardRows || b.Me == nil || b.Me.Rank != boardRows+4 {
		t.Fatalf("long board: %d rows, me %+v", len(b.Rows), b.Me)
	}
}
