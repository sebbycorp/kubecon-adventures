//go:build cluster

package defense

// These tests talk to the real cluster. TestKagentOpsLifecycle needs KAGENT_URL (kagent's
// API, e.g. a port-forward of kagent-controller:8083); TestBoothLoad needs ARCADE_URL
// (http://localhost:8090) and plays the deployed game through the phone protocol.

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"math"
	"math/rand/v2"
	"net/http"
	"os"
	"os/exec"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/sebbycorp/kubecon-adventures/arcade/internal/kagent"
)

// TestKagentOpsLifecycle: spawn a subagent, start its task turn, suspend it mid-turn
// (the suspend waits for the turn to end), then delete it.
func TestKagentOpsLifecycle(t *testing.T) {
	url := os.Getenv("KAGENT_URL")
	if url == "" {
		t.Skip("KAGENT_URL not set")
	}
	k, err := kagent.NewKagent(url, "defense-test", "kagent", AgentName)
	if err != nil {
		t.Fatal(err)
	}
	o := &agentOps{k: k}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	start := time.Now()
	sid, err := o.Spawn(ctx, "lifecycle-test")
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("spawn %v", time.Since(start).Round(time.Millisecond))
	defer func() {
		dctx, dc := context.WithTimeout(context.Background(), 30*time.Second)
		defer dc()
		_ = o.Delete(dctx, sid)
	}()
	type taskRes struct {
		say    string
		tokens int64
		err    error
		took   time.Duration
	}
	done := make(chan taskRes, 1)
	go func() {
		t0 := time.Now()
		say, tokens, err := o.Task(ctx, sid, "tester")
		done <- taskRes{say, tokens, err, time.Since(t0)}
	}()
	time.Sleep(500 * time.Millisecond)
	t0 := time.Now()
	ms, err := o.Suspend(ctx, sid)
	if err != nil {
		t.Fatalf("suspend mid-turn: %v", err)
	}
	t.Logf("suspend: %d ms for the call, %v including the wait for the turn", ms, time.Since(t0).Round(time.Millisecond))
	r := <-done
	if r.err != nil {
		t.Fatalf("task: %v", r.err)
	}
	if r.say == "" || r.tokens == 0 {
		t.Fatalf("task: say=%q tokens=%d", r.say, r.tokens)
	}
	t.Logf("task %v, %d tokens, line %d chars", r.took.Round(time.Millisecond), r.tokens, len([]rune(r.say)))
	ids, err := o.ListSessions(ctx)
	if err != nil || !slices.Contains(ids, sid) {
		t.Fatalf("list: %v, has session %v", err, slices.Contains(ids, sid))
	}
	if err := o.Delete(ctx, sid); err != nil {
		t.Fatalf("delete a suspended session: %v", err)
	}
}

// loadSnap is the part of a snapshot the load test reads.
type loadSnap struct {
	SnapHead
	SnapCommon
}

type loadBot struct {
	nick string
	conn *websocket.Conn
	mu   sync.Mutex
	snap *loadSnap
}

func (b *loadBot) send(ctx context.Context, v any) {
	raw, _ := json.Marshal(v)
	wctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	_ = b.conn.Write(wctx, websocket.MessageText, raw)
}

func (b *loadBot) read(ctx context.Context, on func(*loadSnap)) {
	for {
		_, raw, err := b.conn.Read(ctx)
		if err != nil {
			return
		}
		if !strings.HasPrefix(string(raw), `{"t":"snap"`) {
			continue
		}
		s := &loadSnap{}
		if json.Unmarshal(raw, s) != nil {
			continue
		}
		b.mu.Lock()
		b.snap = s
		b.mu.Unlock()
		if on != nil {
			on(s)
		}
	}
}

// tick steers toward the nearest running agent, turning in place once close, and holds
// FIRE in range about half the time, with some wobble, the way a person on a phone
// plays. A lazy bot only circles the cluster and fires at what passes. The first bot
// taps PLAY whenever it may.
func (b *loadBot) tick(ctx context.Context, leader, lazy bool) {
	b.mu.Lock()
	s := b.snap
	b.mu.Unlock()
	if s == nil {
		return
	}
	if leader && s.Round.Phase != "live" && s.Round.CanPlay {
		b.send(ctx, map[string]string{"t": "play"})
	}
	var me *ShipView
	for i := range s.Ships {
		if s.Ships[i].ID == s.Me {
			me = &s.Ships[i]
		}
	}
	if me == nil {
		return
	}
	var best *AgentView
	bd := math.Inf(1)
	for i := range s.Agents {
		a := &s.Agents[i]
		if d := math.Hypot(a.X-me.X, a.Y-me.Y); a.State == "running" && d < bd {
			best, bd = a, d
		}
	}
	fire := rand.Float64() < 0.55
	if lazy {
		a := float64(time.Now().UnixMilli()%20000) / 20000 * 2 * math.Pi
		b.send(ctx, map[string]any{"t": "in", "mx": math.Cos(a), "my": math.Sin(a), "fire": fire && bd < 32})
		return
	}
	if best == nil {
		b.send(ctx, map[string]any{"t": "in", "mx": 0, "my": 0, "fire": false})
		return
	}
	dx, dy := best.X-me.X+(rand.Float64()-0.5)*8, best.Y-me.Y+(rand.Float64()-0.5)*8
	n := math.Hypot(dx, dy)
	k := 1.0
	if bd < 18 {
		k = 0.2
	}
	b.send(ctx, map[string]any{"t": "in", "mx": dx / n * k, "my": dy / n * k, "fire": fire && bd < 32})
}

func join(ctx context.Context, base, nick string) (string, error) {
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, base+"/api/join", strings.NewReader(`{"nick":"`+nick+`"}`))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	for _, c := range resp.Cookies() {
		return c.Name + "=" + c.Value, nil
	}
	return "", fmt.Errorf("join %s: %s, no cookie", nick, resp.Status)
}

func dial(ctx context.Context, base, path, cookie string) (*websocket.Conn, error) {
	h := http.Header{}
	if cookie != "" {
		h.Set("Cookie", cookie)
	}
	conn, _, err := websocket.Dial(ctx, strings.Replace(base, "http", "ws", 1)+path, &websocket.DialOptions{HTTPHeader: h})
	if err == nil {
		conn.SetReadLimit(8 << 20)
	}
	return conn, err
}

// agwTokens reads agentgateway's counters for the defense route: tokens by type and LLM
// requests by status.
func agwTokens(ctx context.Context, url string) (map[string]float64, error) {
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	out := map[string]float64{}
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		line := sc.Text()
		if !strings.Contains(line, `route="agentgateway-system/openai-defense"`) {
			continue
		}
		f := strings.Fields(line)
		v, err := strconv.ParseFloat(f[len(f)-1], 64)
		if err != nil {
			continue
		}
		label := func(name string) string {
			i := strings.Index(line, name+`="`)
			if i < 0 {
				return ""
			}
			rest := line[i+len(name)+2:]
			return rest[:strings.IndexByte(rest, '"')]
		}
		switch {
		case strings.HasPrefix(line, "agentgateway_gen_ai_client_token_usage_sum{"):
			out[label("gen_ai_token_type")] += v
		case strings.HasPrefix(line, "agentgateway_requests_total{"):
			out["req_"+label("status")] += v
		}
	}
	return out, sc.Err()
}

func pctl(xs []float64, p float64) float64 {
	if len(xs) == 0 {
		return 0
	}
	s := slices.Clone(xs)
	slices.Sort(s)
	return s[int(p*float64(len(s)-1))]
}

// TestBoothLoad plays BOOTH_BOTS (default 16) bots for BOOTH_SECS (default 180) seconds
// against the deployed game. It reports, every 30 s and at the end: rounds and how long
// each survived, cluster fill (running vs workers), the time from the second hit to the
// confirmed suspend (and Substrate's suspend call alone), and agentgateway tokens a
// minute. It fails if a round never starts or a suspend never lands.
func TestBoothLoad(t *testing.T) {
	base := strings.TrimRight(os.Getenv("ARCADE_URL"), "/")
	if base == "" {
		t.Skip("ARCADE_URL not set")
	}
	secs, nbots := 180, 16
	if v, err := strconv.Atoi(os.Getenv("BOOTH_SECS")); err == nil && v > 0 {
		secs = v
	}
	if v, err := strconv.Atoi(os.Getenv("BOOTH_BOTS")); err == nil && v > 0 {
		nbots = v
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(secs+120)*time.Second)
	defer cancel()

	pf := exec.Command("kubectl", "--context", "kind-kagent", "-n", "agentgateway-system", "port-forward", "deploy/arcade", "15022:15020")
	if err := pf.Start(); err != nil {
		t.Fatalf("port-forward agentgateway metrics: %v", err)
	}
	defer func() { _ = pf.Process.Kill(); _ = pf.Wait() }()
	metrics := "http://localhost:15022/metrics"
	for range 50 {
		if _, err := agwTokens(ctx, metrics); err == nil {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}

	// The observer is a big screen: it times every suspend from the second hit (the agent
	// turns "suspending") to the confirmed event, and tracks fill and rounds.
	var mu sync.Mutex
	since := map[string]time.Time{}
	var waits, calls []float64
	var fills []float64
	maxRunning, rounds := 0, map[int]RoundView{}
	stuck := 0
	waveMax, counts := map[int]int{}, map[string]int{}
	onSnap := func(s *loadSnap) {
		mu.Lock()
		defer mu.Unlock()
		now := time.Now()
		for _, a := range s.Agents {
			if _, ok := since[a.ID]; a.State == "suspending" && !ok {
				since[a.ID] = now
			}
		}
		if s.Round.Phase == "live" {
			waveMax[s.Round.N] = max(waveMax[s.Round.N], s.Wave.N)
		}
		for _, e := range s.Events {
			switch e.Kind {
			case "squad", "bomb", "kids", "saved", "alarm", "power", "slipped":
				counts[e.Kind]++
			case "suspended":
				counts["suspended:"+e.Text]++
			}
			if e.Kind == "suspended" {
				if t0, ok := since[e.Agent]; ok {
					waits = append(waits, float64(now.Sub(t0).Milliseconds()))
					delete(since, e.Agent)
				}
				calls = append(calls, float64(e.Ms))
			}
		}
		for id, t0 := range since {
			if now.Sub(t0) > 50*time.Second {
				stuck++
				delete(since, id)
			}
		}
		if s.Round.Phase == "live" {
			fills = append(fills, float64(s.Fill.Running)/float64(max(1, s.Fill.Slots)))
			maxRunning = max(maxRunning, s.Fill.Running)
		}
		if s.Round.N > 0 {
			rounds[s.Round.N] = s.Round
		}
	}
	oconn, err := dial(ctx, base, "/cluster-defense/ws?screen=1", "")
	if err != nil {
		t.Fatal(err)
	}
	defer oconn.CloseNow()
	go (&loadBot{nick: "observer", conn: oconn}).read(ctx, onSnap)

	var bots []*loadBot
	for i := range nbots {
		nick := fmt.Sprintf("cd-bot-%02d", i+1)
		cookie, err := join(ctx, base, nick)
		if err != nil {
			t.Fatal(err)
		}
		conn, err := dial(ctx, base, "/cluster-defense/ws", cookie)
		if err != nil {
			t.Fatalf("%s: %v", nick, err)
		}
		defer conn.CloseNow()
		b := &loadBot{nick: nick, conn: conn}
		go b.read(ctx, nil)
		bots = append(bots, b)
	}
	tok0, _ := agwTokens(ctx, metrics)
	last := tok0
	start := time.Now()
	report := time.NewTicker(30 * time.Second)
	defer report.Stop()
	play := time.NewTicker(100 * time.Millisecond)
	defer play.Stop()
	end := time.After(time.Duration(secs) * time.Second)
	tokens := func(m map[string]float64) float64 { return m["input"] + m["output"] }
loop:
	for {
		select {
		case <-play.C:
			for i, b := range bots {
				b.tick(ctx, i == 0, i%4 == 3)
			}
		case <-report.C:
			now, _ := agwTokens(ctx, metrics)
			mu.Lock()
			t.Logf("%3.0fs: rounds %d · max running %d · suspends %d · wait p50 %.0f p95 %.0f ms · call p50 %.0f p95 %.0f ms · tokens last 30 s %.0f (429s so far %.0f)",
				time.Since(start).Seconds(), len(rounds), maxRunning, len(waits), pctl(waits, .5), pctl(waits, .95), pctl(calls, .5), pctl(calls, .95),
				tokens(now)-tokens(last), now["req_429"]-tok0["req_429"])
			mu.Unlock()
			last = now
		case <-end:
			break loop
		}
	}
	tok1, _ := agwTokens(ctx, metrics)
	mins := time.Since(start).Minutes()
	mu.Lock()
	defer mu.Unlock()
	for _, n := range slices.Sorted(func(yield func(int) bool) {
		for n := range rounds {
			if !yield(n) {
				return
			}
		}
	}) {
		r := rounds[n]
		t.Logf("round %d: %s %s · survived %.0f s · reached wave %d · %d suspended · %d spawned · peak running %d · peak hosted %d",
			n, r.Phase, r.Reason, r.Survived, waveMax[n], r.Suspended, r.Spawned, r.Peak, r.Hosted)
	}
	t.Logf("events: %v", counts)
	t.Logf("fill while live: p50 %.0f%% p95 %.0f%% · max running %d", 100*pctl(fills, .5), 100*pctl(fills, .95), maxRunning)
	t.Logf("suspends %d: hit-to-suspended p50 %.0f p95 %.0f ms; Substrate suspend call p50 %.0f p95 %.0f ms; stuck %d",
		len(waits), pctl(waits, .5), pctl(waits, .95), pctl(calls, .5), pctl(calls, .95), stuck)
	t.Logf("agentgateway: %.0f tokens/min average (%.0f input, %.0f output), %.0f requests, %.0f rate-limited (429)",
		(tokens(tok1)-tokens(tok0))/mins, tok1["input"]-tok0["input"], tok1["output"]-tok0["output"],
		tok1["req_200"]-tok0["req_200"]+tok1["req_429"]-tok0["req_429"], tok1["req_429"]-tok0["req_429"])
	if len(rounds) == 0 {
		t.Fatal("no round started")
	}
	if len(waits) == 0 {
		t.Fatal("no suspend landed")
	}
	if stuck > 0 {
		t.Errorf("%d suspends never landed within 50 s", stuck)
	}
}
