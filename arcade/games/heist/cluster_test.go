//go:build cluster

package heist

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"math"
	"net/http"
	"os"
	"os/exec"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/sebbycorp/kubecon-adventures/arcade/internal/kagent"
)

// TestCheckpointSuspended answers: can a SUSPENDED session be checkpointed?
func TestCheckpointSuspended(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	k, err := kagent.NewKagent(os.Getenv("KAGENT_URL"), "heist-test", "kagent", "heister")
	if err != nil {
		t.Fatal(err)
	}
	sid, err := k.CreateSession(ctx, "heist-checkpoint-test")
	if err != nil {
		t.Fatal(err)
	}
	defer k.Delete(context.Background(), sid)

	start := time.Now()
	reply, err := k.Prompt(ctx, sid, "Run this bash command in /data/workspace: echo loot > /data/workspace/loot.txt")
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("prompt 1: %v", time.Since(start))

	start = time.Now()
	// Right after a turn kagent answers FailedPrecondition ("runtime cleanup or lifecycle
	// work is not yet settled") until the turn settles, so retry that.
	for tries := 1; ; tries++ {
		err = k.Suspend(ctx, sid)
		if err == nil {
			t.Logf("suspend: %v (%d tries)", time.Since(start), tries)
			break
		}
		if status.Code(err) != codes.FailedPrecondition || time.Since(start) > 60*time.Second {
			t.Fatal(err)
		}
		time.Sleep(500 * time.Millisecond)
	}

	start = time.Now()
	cp, err := k.Checkpoint(ctx, sid, string(reply.Task.ID))
	if err != nil {
		t.Logf("CHECKPOINT WHILE SUSPENDED: FAILED after %v: %v", time.Since(start), err)
	} else {
		t.Logf("CHECKPOINT WHILE SUSPENDED: WORKED in %v, checkpoint id %s", time.Since(start), cp)
	}

	start = time.Now()
	if err := k.Resume(ctx, sid); err != nil {
		t.Fatal(err)
	}
	t.Logf("resume: %v", time.Since(start))

	// Same "not yet settled" window right after resume: retry.
	for start = time.Now(); ; time.Sleep(500 * time.Millisecond) {
		reply, err = k.Prompt(ctx, sid, "Run this bash command in /data/workspace: cat /data/workspace/loot.txt")
		if err == nil || !strings.Contains(err.Error(), "not yet settled") || time.Since(start) > 60*time.Second {
			break
		}
	}
	if err != nil {
		t.Fatal(err)
	}
	var out strings.Builder
	out.WriteString(reply.Text + reply.JSON)
	for _, c := range reply.Tools {
		out.WriteString(c.Output)
	}
	if !strings.Contains(out.String(), "loot") {
		t.Fatalf("workspace file lost after resume: %q", out.String())
	}
	// Delete hits the same window right after a turn.
	for start = time.Now(); ; time.Sleep(500 * time.Millisecond) {
		err = k.Delete(ctx, sid)
		if err == nil || !strings.Contains(err.Error(), "not yet settled") || time.Since(start) > 60*time.Second {
			break
		}
	}
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("delete ok after %v", time.Since(start))
}

// TestKagentOpsLifecycle runs the real adapter through a target's and an ally's life:
// Spawn, 2 Steps, Freeze, Spring, Step, Extract; SpawnAlly, Freeze, Spring, Delete.
func TestKagentOpsLifecycle(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()
	url := os.Getenv("KAGENT_URL")
	heister, err := kagent.NewKagent(url, "heist-test", "kagent", "heister")
	if err != nil {
		t.Fatal(err)
	}
	sidekick, err := kagent.NewKagent(url, "heist-test", "kagent", "sidekick")
	if err != nil {
		t.Fatal(err)
	}
	o := NewKagentOps(heister, sidekick, sidekick, "resume-first")
	var made []string
	defer func() { // every session this test made is gone afterwards, pass or fail
		for _, sid := range made {
			_ = o.Delete(context.Background(), sid)
		}
	}()
	op := func(name string) func() {
		start := time.Now()
		return func() { t.Logf("TIMING %-14s %v", name, time.Since(start).Round(time.Millisecond)) }
	}
	// Each op gets the game's per-op timeout.
	opCtx := func() (context.Context, context.CancelFunc) { return context.WithTimeout(ctx, opTimeout) }

	c, cc := opCtx()
	done := op("spawn")
	sid, node, err := o.Spawn(c, "lifecycle-test")
	cc()
	done()
	if err != nil {
		t.Fatal(err)
	}
	made = append(made, sid)
	t.Logf("target %s on node %q, job %q", sid, node, jobFor(sid))

	step := func(n int) {
		c, cc := opCtx()
		defer cc()
		done := op(fmt.Sprintf("step %d", n))
		r, err := o.Step(c, sid, n, 4)
		done()
		if err != nil {
			t.Fatalf("step %d: %v", n, err)
		}
		t.Logf("step %d: %+v", n, r)
		if slices.Contains(stepQuips, r.Say) {
			t.Logf("FALLBACK step %d line", n)
		}
	}
	step(1)
	step(2)

	c, cc = opCtx()
	done = op("freeze")
	ms, err := o.Freeze(c, sid)
	cc()
	done()
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("freeze reported %d ms", ms)

	c, cc = opCtx()
	done = op("spring")
	node, say, ms, err := o.Spring(c, sid)
	cc()
	done()
	if err != nil {
		t.Fatal(err)
	}
	if say == "" {
		t.Fatal("spring: empty line")
	}
	if say == sprungFallback { // heister's turn failed output validation: a fallback, not a failure
		t.Logf("FALLBACK spring line")
	}
	t.Logf("spring: node %q, resume %d ms, say %q", node, ms, say)

	step(3)

	c, cc = opCtx()
	done = op("extract")
	var bookedSay string
	var cp string
	bookedSay, cp, ms, err = o.Extract(c, sid)
	cc()
	done()
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("extract reported %d ms, say %q, checkpoint %s", ms, bookedSay, cp)
	ids, err := heister.ListSessionIDs(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range ids {
		if id == sid {
			t.Fatalf("session %s still listed after Extract", sid)
		}
	}

	// The ally: sidekick greets, freezes, is sprung, and is deleted.
	c, cc = opCtx()
	done = op("ally")
	aid, node, say, err := o.SpawnAlly(c, "lifecycle-ally")
	cc()
	done()
	if aid != "" {
		made = append(made, aid)
	}
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("ally %s on node %q says %q", aid, node, say)
	if say == "" {
		t.Fatal("ally: empty greeting")
	}
	if say == allyGreetFallback {
		t.Logf("FALLBACK ally greeting")
	}
	c, cc = opCtx()
	done = op("ally freeze")
	_, err = o.Freeze(c, aid)
	cc()
	done()
	if err != nil {
		t.Fatal(err)
	}
	c, cc = opCtx()
	done = op("ally spring")
	_, say, _, err = o.Spring(c, aid)
	cc()
	done()
	if err != nil || say == "" {
		t.Fatalf("ally spring: %q %v", say, err)
	}
	if say == allyFallback {
		t.Logf("FALLBACK ally spring line")
	}
	t.Logf("ally sprung: %q", say)
	c, cc = opCtx()
	done = op("ally delete")
	err = o.Delete(c, aid)
	cc()
	done()
	if err != nil {
		t.Fatal(err)
	}
	ids, err = sidekick.ListSessionIDs(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range ids {
		if id == aid {
			t.Fatalf("ally session %s still listed after Delete", aid)
		}
	}
}

// clusterOps is the real adapter on heister and sidekick; cleanup deletes every session
// it made, pass or fail, and fails the test if one is still listed afterwards.
func clusterOps(t *testing.T) (*agentOps, *kagent.Kagent, func(string)) {
	t.Helper()
	url := os.Getenv("KAGENT_URL")
	heister, err := kagent.NewKagent(url, "heist-test", "kagent", "heister")
	if err != nil {
		t.Fatal(err)
	}
	sidekick, err := kagent.NewKagent(url, "heist-test", "kagent", "sidekick")
	if err != nil {
		t.Fatal(err)
	}
	o := newAgentOps(heister, sidekick, "resume-first")
	var made []string
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		for _, sid := range made {
			_ = o.Delete(ctx, sid) // already gone after a good Extract: NotFound is fine
			// A booking's checkpoint outlives its session; delete it and check it went.
			cps, err := o.ListCheckpoints(ctx, sid)
			if err != nil {
				t.Errorf("cleanup list checkpoints of %s: %v", sid, err)
			}
			for _, cp := range cps {
				if err := o.DeleteCheckpoint(ctx, cp); err != nil {
					t.Errorf("cleanup delete checkpoint %s: %v", cp, err)
				}
				t.Logf("cleanup deleted checkpoint %s of %s", cp, sid)
			}
			if left, err := o.ListCheckpoints(ctx, sid); err != nil || len(left) != 0 {
				t.Errorf("checkpoints of %s left behind: %v %v", sid, left, err)
			}
		}
		ids, err := heister.ListSessionIDs(ctx)
		if err != nil {
			t.Errorf("cleanup list: %v", err)
		}
		for _, id := range ids {
			for _, sid := range made {
				if id == sid {
					t.Errorf("session %s left behind", sid)
				}
			}
		}
	})
	return o, heister, func(sid string) { made = append(made, sid) }
}

// sessionState is the session's runtime state and operation, as kagent reports it.
func sessionState(t *testing.T, k *kagent.Kagent, sid string) string {
	t.Helper()
	actors, err := k.ActorStates(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	a, ok := actors[sid]
	if !ok {
		return "GONE"
	}
	return a.State
}

// TestExtractFrozenTarget: a frozen target is booked (Resume, booked line, Checkpoint of
// that turn, Delete) and its session is gone afterwards.
func TestExtractFrozenTarget(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	o, heister, track := clusterOps(t)
	opCtx := func() (context.Context, context.CancelFunc) { return context.WithTimeout(ctx, opTimeout) }

	c, cc := opCtx()
	sid, _, err := o.Spawn(c, "extract-frozen-test")
	cc()
	if err != nil {
		t.Fatal(err)
	}
	track(sid)
	c, cc = opCtx()
	start := time.Now()
	r, err := o.Step(c, sid, 1, 4)
	cc()
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("TIMING step     %v (%q)", time.Since(start).Round(time.Millisecond), r.Say)
	c, cc = opCtx()
	start = time.Now()
	ms, err := o.Freeze(c, sid)
	cc()
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("TIMING freeze   %v (reported %d ms), state %s", time.Since(start).Round(time.Millisecond), ms, sessionState(t, heister, sid))
	c, cc = opCtx()
	start = time.Now()
	say, cp, ms, err := o.Extract(c, sid)
	cc()
	if err != nil {
		t.Fatalf("extract of a frozen target: %v", err)
	}
	if cp == "" {
		t.Error("extract returned no checkpoint ID")
	}
	if cps, err := o.ListCheckpoints(ctx, sid); err != nil || !slices.Contains(cps, cp) {
		t.Errorf("checkpoint %s not listed for the deleted session: %v %v", cp, cps, err)
	}
	t.Logf("CHECKPOINT %s (deleted by the test cleanup)", cp)
	t.Logf("TIMING extract  %v (reported %d ms without the LLM turn), say %q", time.Since(start).Round(time.Millisecond), ms, say)
	if say == "" {
		t.Errorf("empty booked line")
	}
	if say == bookedFallback {
		t.Logf("FALLBACK booked line")
	}
	ids, err := heister.ListSessionIDs(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range ids {
		if id == sid {
			t.Fatalf("session %s still listed after Extract", sid)
		}
	}
}

// TestFreezeDuringStep: a Freeze ~300 ms into a step (kagent answers Aborted "session has
// an active task" mid-turn) waits for the turn to end and lands: SUSPENDED.
func TestFreezeDuringStep(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	o, heister, track := clusterOps(t)
	opCtx := func() (context.Context, context.CancelFunc) { return context.WithTimeout(ctx, opTimeout) }

	c, cc := opCtx()
	sid, _, err := o.Spawn(c, "freeze-during-step-test")
	cc()
	if err != nil {
		t.Fatal(err)
	}
	track(sid)
	c, cc = opCtx()
	_, err = o.Step(c, sid, 1, 4) // the first turn places the session (as in the game)
	cc()
	if err != nil {
		t.Fatal(err)
	}

	type stepOut struct {
		err error
		end time.Time
	}
	done := make(chan stepOut, 1)
	stepStart := time.Now()
	go func() {
		c, cc := opCtx()
		defer cc()
		_, err := o.Step(c, sid, 2, 4)
		done <- stepOut{err, time.Now()}
	}()
	time.Sleep(300 * time.Millisecond)
	c, cc = opCtx()
	start := time.Now()
	ms, err := o.Freeze(c, sid)
	cc()
	frozeAt := time.Now()
	if err != nil {
		t.Fatalf("freeze during a step: %v", err)
	}
	st := <-done
	t.Logf("TIMING step ended %v after its start (err %v); freeze called at +%v, returned at +%v (call %v, reported %d ms)",
		st.end.Sub(stepStart).Round(time.Millisecond), st.err, start.Sub(stepStart).Round(time.Millisecond),
		frozeAt.Sub(stepStart).Round(time.Millisecond), frozeAt.Sub(start).Round(time.Millisecond), ms)
	if st.err != nil {
		t.Errorf("the in-flight step should finish first: %v", st.err)
	}
	// The freeze really waited for the turn: it returned after the step ended (and so
	// took well over 100 ms), instead of landing or failing fast mid-turn.
	if frozeAt.Before(st.end) || frozeAt.Sub(start) < 100*time.Millisecond {
		t.Fatalf("freeze did not wait for the turn: returned at +%v, step ended at +%v, call %v",
			frozeAt.Sub(stepStart), st.end.Sub(stepStart), frozeAt.Sub(start))
	}
	if got := sessionState(t, heister, sid); got != "SUSPENDED" {
		t.Fatalf("after freeze: actor %s, want SUSPENDED", got)
	}
}

// ---- TestBoothLoad: 24 phones on the real protocol for 5 minutes ----

// boothCity is GET /substrate-siege/api/city with BFS steering on its tiles.
type boothCity struct {
	CityView
}

func (c *boothCity) open(x, y int) bool {
	return x >= 0 && y >= 0 && x < c.W && y < c.H && c.Tiles[y][x] != '#'
}

// steer is a unit move from (x,y) toward (gx,gy) along the road grid: the next tile of
// a BFS path, or straight at the goal when it is in the same tile.
func (c *boothCity) steer(x, y, gx, gy float64) (float64, float64) {
	sx, sy, tx, ty := int(x), int(y), int(gx), int(gy)
	if !c.open(tx, ty) || (sx == tx && sy == ty) {
		return unit(gx-x, gy-y)
	}
	type pt struct{ x, y int }
	prev := map[pt]pt{{sx, sy}: {-1, -1}}
	q := []pt{{sx, sy}}
	for len(q) > 0 {
		p := q[0]
		q = q[1:]
		if p.x == tx && p.y == ty {
			break
		}
		for _, d := range []pt{{1, 0}, {-1, 0}, {0, 1}, {0, -1}} {
			n := pt{p.x + d.x, p.y + d.y}
			if _, seen := prev[n]; !seen && c.open(n.x, n.y) {
				prev[n] = p
				q = append(q, n)
			}
		}
	}
	cur, ok := pt{tx, ty}, false
	if _, ok = prev[cur]; !ok {
		return unit(gx-x, gy-y)
	}
	for {
		p := prev[cur]
		if p.x == sx && p.y == sy || p.x < 0 {
			break
		}
		cur = p
	}
	return unit(float64(cur.x)+0.5-x, float64(cur.y)+0.5-y)
}

func unit(x, y float64) (float64, float64) {
	m := math.Hypot(x, y)
	if m < 1e-9 {
		return 0, 0
	}
	return x / m, y / m
}

// boothBot is one phone: a joined player on its own WebSocket.
type boothBot struct {
	nick, cookie string
	leader       bool // starts the squad's runs
	books        bool // taps frozen agents to book them (the others leave them be)
	conn         *websocket.Conn

	mu      sync.Mutex
	snap    *Snap
	squad   string
	errs    map[string]int // {"t":"err"} replies by message
	startAt time.Time
	buildAt time.Time
	tapping bool
	grabAt  time.Time
}

func (b *boothBot) send(ctx context.Context, v any) {
	raw, _ := json.Marshal(v)
	wctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	_ = b.conn.Write(wctx, websocket.MessageText, raw)
}

func boothJoin(ctx context.Context, base, nick string) (string, error) {
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, base+"/api/join", strings.NewReader(`{"nick":"`+nick+`"}`))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("join %s: %s", nick, resp.Status)
	}
	for _, c := range resp.Cookies() {
		return c.Name + "=" + c.Value, nil
	}
	return "", fmt.Errorf("join %s: no cookie", nick)
}

func boothDial(ctx context.Context, base, path, cookie string) (*websocket.Conn, error) {
	h := http.Header{}
	if cookie != "" {
		h.Set("Cookie", cookie)
	}
	conn, _, err := websocket.Dial(ctx, strings.Replace(base, "http", "ws", 1)+path, &websocket.DialOptions{HTTPHeader: h})
	if err != nil {
		return nil, err
	}
	conn.SetReadLimit(8 << 20)
	return conn, nil
}

// read keeps the newest snapshot and the squad code. onSnap sees every snapshot.
func (b *boothBot) read(ctx context.Context, onSnap func(*Snap)) {
	for {
		_, raw, err := b.conn.Read(ctx)
		if err != nil {
			return
		}
		var head struct{ T, Code, Msg string }
		if json.Unmarshal(raw, &head) != nil {
			continue
		}
		switch head.T {
		case "snap":
			s := &Snap{}
			if json.Unmarshal(raw, s) != nil {
				continue
			}
			b.mu.Lock()
			b.snap = s
			b.mu.Unlock()
			if onSnap != nil {
				onSnap(s)
			}
		case "squad":
			b.mu.Lock()
			b.squad = head.Code
			b.mu.Unlock()
		case "err":
			b.mu.Lock()
			b.errs[head.Msg]++
			b.mu.Unlock()
		}
	}
}

// tick decides one input frame: start a run (leader), book a frozen agent next to it,
// pick up drops, or chase and shoot the nearest agent (own run's first). Rigs build
// themselves once a bot carries a full set.
func (b *boothBot) tick(ctx context.Context, c *boothCity, built func(nick string)) {
	b.mu.Lock()
	s, squad := b.snap, b.squad
	b.mu.Unlock()
	if s == nil || squad == "" {
		return
	}
	now := time.Now()
	var cur *MissionView
	for i := range s.Missions {
		if m := &s.Missions[i]; m.Squad == squad && m.Outcome == "" {
			cur = m
		}
	}
	if b.leader && cur == nil && now.Sub(b.startAt) > 3*time.Second {
		b.startAt = now
		b.send(ctx, map[string]any{"t": "start"})
	}
	if s.Inv != nil && hasRig(*s.Inv) && now.Sub(b.buildAt) > 5*time.Second {
		b.buildAt = now
		built(b.nick)
	}
	var me *AvatarView
	for i := range s.Avatars {
		if s.Avatars[i].ID == s.Me {
			me = &s.Avatars[i]
		}
	}
	in := map[string]any{"t": "in", "mx": 0.0, "my": 0.0, "fire": false, "act": false}
	defer func() { b.send(ctx, in) }()
	if me == nil || me.Down {
		return
	}
	move := func(gx, gy float64) { in["mx"], in["my"] = c.steer(me.X, me.Y, gx, gy) }
	d := func(x, y float64) float64 { return math.Hypot(x-me.X, y-me.Y) }
	if b.tapping { // release ACTION
		b.tapping = false
		return
	}
	mine := func(t TargetView) bool { return cur != nil && t.Mission == cur.ID }
	var frozen, working *TargetView
	for i := range s.Targets {
		t := &s.Targets[i]
		switch {
		case t.State == "Frozen" && !t.Busy && d(t.X, t.Y) < 6 && b.books:
			if frozen == nil || d(t.X, t.Y) < d(frozen.X, frozen.Y) {
				frozen = t
			}
		case t.State == "Working" && !t.Busy:
			// Own run's agents first; anyone's within reach otherwise.
			if !mine(*t) && d(t.X, t.Y) > 8 {
				continue
			}
			if working == nil || mine(*t) && !mine(*working) || mine(*t) == mine(*working) && d(t.X, t.Y) < d(working.X, working.Y) {
				working = t
			}
		}
	}
	inv := 0
	if s.Inv != nil {
		inv = len(*s.Inv)
	}
	var drop *DropView
	if inv < invSlots {
		for i := range s.Drops {
			if dd := &s.Drops[i]; d(dd.X, dd.Y) < 6 && (drop == nil || d(dd.X, dd.Y) < d(drop.X, drop.Y)) {
				drop = dd
			}
		}
	}
	switch {
	case frozen != nil:
		if d(frozen.X, frozen.Y) > actionRange-0.4 {
			move(frozen.X, frozen.Y)
		} else if now.Sub(b.grabAt) > 1500*time.Millisecond {
			b.grabAt, b.tapping, in["act"] = now, true, true
		}
	case working != nil:
		dist := d(working.X, working.Y)
		in["fire"] = dist < fireRange-0.5
		if dist > 3.5 {
			move(working.X, working.Y)
		}
	case drop != nil:
		move(drop.X, drop.Y)
	}
}

func hasRig(parts []PartView) bool {
	have := map[string]bool{}
	for _, p := range parts {
		have[p.Kind] = true
	}
	return have["board"] && have["cpu"] && have["ram"] && have["drive"]
}

// boothTiming is wall time (the op asked, from the snapshot where the target turned Busy,
// to its result event) and call time (the event's ms: the op the server timed).
type boothTiming struct{ wall, call []float64 }

func pct(xs []float64, p float64) float64 {
	if len(xs) == 0 {
		return 0
	}
	s := slices.Sorted(slices.Values(xs))
	return s[min(len(s)-1, int(math.Ceil(p*float64(len(s))))-1)]
}

func (bt boothTiming) String() string {
	return fmt.Sprintf("n=%d wall p50/p95 %.0f/%.0f ms, call p50/p95 %.0f/%.0f ms",
		len(bt.wall), pct(bt.wall, .5), pct(bt.wall, .95), pct(bt.call, .5), pct(bt.call, .95))
}

// boothObserver is a ?screen=1 spectator: it sees every event once, so it does the
// counting for the whole city.
type boothObserver struct {
	mu        sync.Mutex
	busyAt    map[string]time.Time // target -> when it turned Busy, by state
	timings   map[string]*boothTiming
	events    map[string]int
	outcomes  map[string]int
	stuck     map[string]string
	builtAt   map[string]time.Time // nick -> last build frame
	nickOf    map[string]string    // player id -> nick
	allyMs    []float64
	capMax    CapView
	cap       CapView
	allies    int
	alliesMax int
	last      *Snap
}

func newBoothObserver() *boothObserver {
	o := &boothObserver{busyAt: map[string]time.Time{}, events: map[string]int{}, outcomes: map[string]int{},
		stuck: map[string]string{}, builtAt: map[string]time.Time{}, nickOf: map[string]string{}}
	o.timings = map[string]*boothTiming{"freeze": {}, "extract": {}}
	return o
}

func (o *boothObserver) built(nick string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if _, ok := o.builtAt[nick]; !ok {
		o.builtAt[nick] = time.Now()
	}
}

func (o *boothObserver) onSnap(s *Snap) {
	now := time.Now()
	o.mu.Lock()
	defer o.mu.Unlock()
	o.last = s
	for _, a := range s.Avatars {
		o.nickOf[a.ID] = a.Nick
	}
	// A Busy target names the op it waits on: freeze, extract or delete.
	busy := map[string]bool{}
	for _, t := range s.Targets {
		if !t.Busy {
			continue
		}
		k := t.ID + "/" + t.Op
		busy[k] = true
		if _, ok := o.busyAt[k]; !ok {
			o.busyAt[k] = now
		}
	}
	take := func(id, state string) (float64, bool) {
		at, ok := o.busyAt[id+"/"+state]
		if !ok {
			return 0, false
		}
		delete(o.busyAt, id+"/"+state)
		return float64(now.Sub(at).Milliseconds()), true
	}
	for _, e := range s.Events {
		o.events[e.Kind]++
		switch e.Kind {
		case "frozen":
			if w, ok := take(e.Target, "freeze"); ok {
				o.timings["freeze"].wall = append(o.timings["freeze"].wall, w)
				o.timings["freeze"].call = append(o.timings["freeze"].call, float64(e.Ms))
			}
		case "booked":
			if w, ok := take(e.Target, "extract"); ok {
				o.timings["extract"].wall = append(o.timings["extract"].wall, w)
				o.timings["extract"].call = append(o.timings["extract"].call, float64(e.Ms))
			}
		case "ally", "rig-failed":
			nick := o.nickOf[e.Player]
			if at, ok := o.builtAt[nick]; ok {
				delete(o.builtAt, nick)
				if e.Kind == "ally" {
					o.allyMs = append(o.allyMs, float64(now.Sub(at).Milliseconds()))
				}
			}
		case "run-done":
			o.outcomes["done"]++
		case "escaped":
			o.outcomes["escaped"]++
		}
	}
	for k := range o.busyAt { // a busy flag that cleared without its event (a failed op)
		if !busy[k] && now.Sub(o.busyAt[k]) > 2*time.Second {
			delete(o.busyAt, k)
		}
	}
	for _, m := range s.Missions {
		if m.Outcome == "" && s.Now > m.Ends+10 {
			o.stuck[m.ID] = fmt.Sprintf("run %s (squad %s) timer ran out at %.0f s, no end at %.0f s", m.ID, m.Squad, m.Ends, s.Now)
		}
	}
	o.cap = s.Cap
	o.capMax = CapView{max(o.capMax.Steps, s.Cap.Steps), max(o.capMax.Resumes, s.Cap.Resumes), max(o.capMax.Queued, s.Cap.Queued)}
	o.allies = len(s.Allies)
	o.alliesMax = max(o.alliesMax, len(s.Allies))
}

// agwTokens reads agentgateway's token counters for the heist route (heister and
// sidekick): input, output and cached input tokens, and LLM requests by status.
func agwTokens(ctx context.Context, url string) (map[string]float64, error) {
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	out := map[string]float64{}
	typ := regexp.MustCompile(`gen_ai_token_type="([^"]+)"`)
	st := regexp.MustCompile(`status="([^"]+)"`)
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		line := sc.Text()
		if !strings.Contains(line, `route="agentgateway-system/openai-heist"`) {
			continue
		}
		f := strings.Fields(line)
		v, err := strconv.ParseFloat(f[len(f)-1], 64)
		if err != nil {
			continue
		}
		switch {
		case strings.HasPrefix(line, "agentgateway_gen_ai_client_token_usage_sum{"):
			if m := typ.FindStringSubmatch(line); m != nil {
				out[m[1]] += v
			}
		case strings.HasPrefix(line, "agentgateway_requests_total{"):
			if m := st.FindStringSubmatch(line); m != nil {
				out["req_"+m[1]] += v
			}
		}
	}
	return out, sc.Err()
}

// opFailures reads the arcade's "op failed" log lines since t0: counts by kind and error
// class, and the failures that are not agentgateway policy stops. Fallback lines for
// replies that failed output validation are counted too (they are not op errors).
func opFailures(ctx context.Context, t0 time.Time) (byKind map[string]int, bad []string, fallbacks int, err error) {
	out, err := exec.CommandContext(ctx, "kubectl", "--context", "kind-kagent", "-n", "arcade", "logs", "deploy/arcade",
		"--since-time="+t0.UTC().Format(time.RFC3339)).Output()
	if err != nil {
		return nil, nil, 0, err
	}
	byKind = map[string]int{}
	kindRe := regexp.MustCompile(`\bkind=(\S+)`)
	errRe := regexp.MustCompile(`\berr=("(?:[^"\\]|\\.)*"|\S+)`)
	for _, line := range strings.Split(string(out), "\n") {
		if strings.Contains(line, "failed output validation") || strings.Contains(line, "unreadable, using fallback") {
			fallbacks++
		}
		if !strings.Contains(line, "substrate siege: op failed") {
			continue
		}
		kind, msg := "?", ""
		if m := kindRe.FindStringSubmatch(line); m != nil {
			kind = m[1]
		}
		if m := errRe.FindStringSubmatch(line); m != nil {
			msg, _ = strconv.Unquote(m[1])
			if msg == "" {
				msg = m[1]
			}
		}
		if strings.HasPrefix(msg, "policy: ") {
			byKind[kind+":policy"]++
			continue
		}
		byKind[kind]++
		bad = append(bad, kind+": "+msg)
	}
	return byKind, bad, fallbacks, nil
}

// TestBoothLoad plays a busy booth for BOOTH_SECS (default 300) seconds through the real
// phone protocol: 24 players (8 solo, 4 squads of 4) join with /api/join, open the
// game's WebSocket with their cookie and play back-to-back 3-minute runs. Bots chase
// and shoot the nearest agent and pick up parts (full rigs deploy allies on their own);
// half of them also book the frozen agents they pass. Every 30 s it logs freeze and
// booking times, the cap, errors by kind, allies and agentgateway tokens a minute.
// It fails on a stuck run or on any op error that is not an agentgateway policy
// stop. It resets the game first and last (BOOTH_RESET=0 skips that).
func TestBoothLoad(t *testing.T) {
	base := strings.TrimRight(os.Getenv("ARCADE_URL"), "/")
	if base == "" {
		t.Skip("ARCADE_URL not set")
	}
	secs := 300
	if v, err := strconv.Atoi(os.Getenv("BOOTH_SECS")); err == nil && v > 0 {
		secs = v
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(secs+300)*time.Second)
	defer cancel()

	reset := func() {
		if os.Getenv("BOOTH_RESET") == "0" {
			return
		}
		rctx, rc := context.WithTimeout(context.Background(), 2*time.Minute)
		defer rc()
		req, _ := http.NewRequestWithContext(rctx, http.MethodPost, base+"/admin/substrate-siege/api/reset", nil)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Logf("reset: %v", err)
			return
		}
		var body bytes.Buffer
		_, _ = body.ReadFrom(resp.Body)
		resp.Body.Close()
		t.Logf("reset: %s %s", resp.Status, strings.TrimSpace(body.String()))
	}
	reset()
	defer reset()

	// agentgateway's metrics port (15020), port-forwarded unless AGW_METRICS_URL is set.
	metrics := os.Getenv("AGW_METRICS_URL")
	if metrics == "" {
		pf := exec.Command("kubectl", "--context", "kind-kagent", "-n", "agentgateway-system", "port-forward", "deploy/arcade", "15021:15020")
		if err := pf.Start(); err != nil {
			t.Fatalf("port-forward agentgateway metrics: %v", err)
		}
		defer func() { _ = pf.Process.Kill(); _ = pf.Wait() }()
		metrics = "http://localhost:15021/metrics"
		for range 50 {
			if _, err := agwTokens(ctx, metrics); err == nil {
				break
			}
			time.Sleep(200 * time.Millisecond)
		}
	}

	var city boothCity
	resp, err := http.Get(base + "/substrate-siege/api/city")
	if err != nil {
		t.Fatal(err)
	}
	err = json.NewDecoder(resp.Body).Decode(&city.CityView)
	resp.Body.Close()
	if err != nil {
		t.Fatal(err)
	}

	obs := newBoothObserver()
	oconn, err := boothDial(ctx, base, "/substrate-siege/ws?screen=1", "")
	if err != nil {
		t.Fatalf("observer: %v", err)
	}
	defer oconn.CloseNow()
	ob := &boothBot{nick: "observer", conn: oconn, errs: map[string]int{}}
	go ob.read(ctx, obs.onSnap)

	// 8 solo players, then 4 squads of 4 (the first of each creates, the rest join).
	var bots []*boothBot
	for i := range 24 {
		nick := fmt.Sprintf("load-%02d", i+1)
		cookie, err := boothJoin(ctx, base, nick)
		if err != nil {
			t.Fatal(err)
		}
		conn, err := boothDial(ctx, base, "/substrate-siege/ws", cookie)
		if err != nil {
			t.Fatalf("%s: %v", nick, err)
		}
		defer conn.CloseNow()
		b := &boothBot{nick: nick, cookie: cookie, conn: conn, errs: map[string]int{}, leader: i < 8 || (i-8)%4 == 0, books: i%2 == 0}
		go b.read(ctx, nil)
		bots = append(bots, b)
	}
	waitSquad := func(b *boothBot) string {
		for range 100 {
			b.mu.Lock()
			sq := b.squad
			b.mu.Unlock()
			if sq != "" {
				return sq
			}
			time.Sleep(50 * time.Millisecond)
		}
		t.Fatalf("%s: no squad code", b.nick)
		return ""
	}
	for i, b := range bots {
		switch {
		case i < 8:
			b.send(ctx, map[string]any{"t": "squad", "op": "solo"})
		case (i-8)%4 == 0:
			b.send(ctx, map[string]any{"t": "squad", "op": "create"})
			code := waitSquad(b)
			for _, m := range bots[i+1 : i+4] {
				m.send(ctx, map[string]any{"t": "squad", "op": "join", "code": code})
			}
		}
	}
	for _, b := range bots {
		waitSquad(b)
	}
	t.Logf("24 players joined: 8 solo, 4 squads of 4")

	t0 := time.Now()
	tok0, err := agwTokens(ctx, metrics)
	if err != nil {
		t.Logf("agentgateway metrics: %v (token use will read n/a)", err)
	}
	type tokSample struct {
		at time.Time
		v  map[string]float64
	}
	toks := []tokSample{{t0, tok0}}
	maxPerMin := 0.0

	var wg sync.WaitGroup
	end := t0.Add(time.Duration(secs) * time.Second)
	for _, b := range bots {
		wg.Go(func() {
			tk := time.NewTicker(100 * time.Millisecond)
			defer tk.Stop()
			for time.Now().Before(end) {
				select {
				case <-ctx.Done():
					return
				case <-tk.C:
				}
				b.tick(ctx, &city, obs.built)
			}
			b.send(ctx, map[string]any{"t": "in"})
		})
	}

	sample := func(final bool) {
		now := time.Now()
		cur, terr := agwTokens(ctx, metrics)
		tokLine := "tokens n/a"
		if terr == nil && tok0 != nil {
			toks = append(toks, tokSample{now, cur})
			// Per minute over the trailing 60 s (or the run so far).
			ref := toks[0]
			for _, s := range toks {
				if now.Sub(s.at) >= 60*time.Second-time.Second {
					ref = s
				}
			}
			io := func(m map[string]float64) float64 { return m["input"] + m["output"] }
			mins := now.Sub(ref.at).Minutes()
			perMin := (io(cur) - io(ref.v)) / max(mins, 1.0/60)
			if now.Sub(t0) >= 59*time.Second {
				maxPerMin = max(maxPerMin, perMin)
			}
			tokLine = fmt.Sprintf("tokens/min %.0f (last %.0fs; in %.0f out %.0f cached-in %.0f since start; LLM requests 200=%.0f 429=%.0f other=%.0f)",
				perMin, now.Sub(ref.at).Seconds(), cur["input"]-tok0["input"], cur["output"]-tok0["output"],
				cur["input_cache_read"]-tok0["input_cache_read"], cur["req_200"]-tok0["req_200"], cur["req_429"]-tok0["req_429"],
				sumOther(cur)-sumOther(tok0))
		}
		byKind, bad, fb, lerr := opFailures(ctx, t0)
		obs.mu.Lock()
		evErr := map[string]int{}
		for k, n := range obs.events {
			if strings.HasSuffix(k, "-failed") && !strings.HasPrefix(k, "mission") || k == "slipped" || k == "policy" {
				evErr[k] = n
			}
		}
		replies := map[string]int{}
		for _, b := range bots {
			b.mu.Lock()
			for k, n := range b.errs {
				replies[k] += n
			}
			b.mu.Unlock()
		}
		online := 0
		if s := obs.last; s != nil {
			for _, a := range s.Avatars {
				if a.Online {
					online++
				}
			}
		}
		subs := SubstrateView{}
		if obs.last != nil {
			subs = obs.last.Substrate
		}
		t.Logf("[+%3.0fs]%s online %d | runs/escapes %v | freeze %s | booking %s",
			now.Sub(t0).Seconds(), map[bool]string{true: " FINAL", false: ""}[final], online, obs.outcomes,
			obs.timings["freeze"], obs.timings["extract"])
		t.Logf("        cap now steps %d resumes %d queued %d (max %d/%d/%d) | allies live %d (max %d), spawn %d p50/p95 %.0f/%.0f ms | heist actors %d awake %d asleep %d",
			obs.cap.Steps, obs.cap.Resumes, obs.cap.Queued, obs.capMax.Steps, obs.capMax.Resumes, obs.capMax.Queued,
			obs.allies, obs.alliesMax, len(obs.allyMs), pct(obs.allyMs, .5), pct(obs.allyMs, .95), subs.Agents, subs.Awake, subs.Asleep)
		t.Logf("        events %v | op errors (arcade log) %v, fallback lines %d%s | replies %v | %s",
			evErr, byKind, fb, map[bool]string{true: " (log read failed: " + fmt.Sprint(lerr) + ")", false: ""}[lerr != nil], replies, tokLine)
		t.Logf("        activity: hits-on-players %d downs %d drops %d pickups %d frozen %d booked %d deleted %d allies %d",
			obs.events["hit"], obs.events["down"], obs.events["drop"], obs.events["pickup"], obs.events["frozen"], obs.events["booked"], obs.events["deleted"], obs.events["ally"])
		obs.mu.Unlock()
		if final {
			for _, b := range bad {
				t.Errorf("op error: %s", b)
			}
		}
	}

	report := time.NewTicker(30 * time.Second)
	defer report.Stop()
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
loop:
	for {
		select {
		case <-report.C:
			sample(false)
		case <-done:
			break loop
		case <-ctx.Done():
			t.Fatal("timed out")
		}
	}
	// Let the last freezes and bookings land so their times count.
	time.Sleep(5 * time.Second)
	sample(true)

	obs.mu.Lock()
	defer obs.mu.Unlock()
	for _, k := range slices.Sorted(maps.Keys(obs.stuck)) {
		t.Errorf("stuck: %s", obs.stuck[k])
	}
	fr, bk := obs.timings["freeze"], obs.timings["extract"]
	verdict := func(ok bool) string { return map[bool]string{true: "PASS", false: "MISS"}[ok] }
	t.Logf("TARGETS tokens/min max %.0f < 300000: %s | p95 freeze wall %.0f ms (call alone %.0f ms) < 1000: %s | p95 booking wall %.0f ms (call %.0f ms) < 8000: %s",
		maxPerMin, verdict(maxPerMin < 300000), pct(fr.wall, .95), pct(fr.call, .95), verdict(pct(fr.wall, .95) < 1000),
		pct(bk.wall, .95), pct(bk.call, .95), verdict(pct(bk.wall, .95) < 8000))
	if len(fr.wall) == 0 {
		t.Errorf("no freeze landed in %d s", secs)
	}
}

func sumOther(m map[string]float64) float64 {
	n := 0.0
	for k, v := range m {
		if strings.HasPrefix(k, "req_") && k != "req_200" && k != "req_429" {
			n += v
		}
	}
	return n
}
