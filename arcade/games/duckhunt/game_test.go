package duckhunt

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/sebbycorp/kubecon-adventures/arcade/internal/kagent"
	"github.com/sebbycorp/kubecon-adventures/arcade/internal/platform"
)

// fakeOps is kagent for the duck agent: sessions and their actor states, in memory.
type fakeOps struct {
	mu       sync.Mutex
	state    map[string]string // session -> RUNNING / SUSPENDED
	created  int
	suspends int
	resumes  int
	prompts  int
	failNext error // returned (once) by the next Suspend
}

func newFake(asleep, awake, noActor int) *fakeOps {
	f := &fakeOps{state: map[string]string{}}
	for i := range asleep {
		f.state[fmt.Sprintf("s%d", i)] = "SUSPENDED"
	}
	for i := range awake {
		f.state[fmt.Sprintf("a%d", i)] = "RUNNING"
	}
	for i := range noActor {
		f.state[fmt.Sprintf("n%d", i)] = "" // a session that never ran a turn
	}
	return f
}

func (f *fakeOps) CreateSession(context.Context, string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.created++
	id := fmt.Sprintf("new%d", f.created)
	f.state[id] = ""
	return id, nil
}
func (f *fakeOps) Prompt(_ context.Context, id, _ string) (kagent.Reply, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.prompts++
	f.state[id] = "RUNNING"
	return kagent.Reply{JSON: `{"pattern":"loop","speed":5,"quack":"Hello hunter"}`, Usage: kagent.Usage{Input: 10, Output: 5}}, nil
}
func (f *fakeOps) Suspend(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.failNext; err != nil {
		f.failNext = nil
		return err
	}
	f.suspends++
	f.state[id] = "SUSPENDED"
	return nil
}
func (f *fakeOps) Resume(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.resumes++
	f.state[id] = "RUNNING"
	return nil
}
func (f *fakeOps) ListSessionIDs(context.Context) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var ids []string
	for id := range f.state {
		ids = append(ids, id)
	}
	return ids, nil
}
func (f *fakeOps) ActorStates(context.Context) (map[string]kagent.Actor, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := map[string]kagent.Actor{}
	for id, st := range f.state {
		if st != "" {
			out[id] = kagent.Actor{State: st}
		}
	}
	return out, nil
}
func (f *fakeOps) count(st string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, s := range f.state {
		if s == st {
			n++
		}
	}
	return n
}

// clock is a settable time for the game (its goroutines read it too).
type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *clock) add(d time.Duration) *clock {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
	return c
}

func testGame(f *fakeOps) (*Game, *clock) {
	g := New(nil, f, 20)
	c := &clock{t: time.Unix(1_800_000_000, 0)}
	g.now = c.now
	return g, c
}

func TestPoolAdoptsSleepsAndTopsUp(t *testing.T) {
	f := newFake(5, 2, 3)
	g, _ := testGame(f)
	g.fillPool(context.Background())
	// 5 asleep + 2 awake (put to sleep) adopted, the 3 without an actor skipped, and new
	// sessions created up to poolMin.
	if len(g.slots) != poolMin {
		t.Fatalf("pool = %d, want %d", len(g.slots), poolMin)
	}
	if f.created != poolMin-7 {
		t.Fatalf("created %d, want %d", f.created, poolMin-7)
	}
	for _, s := range g.slots {
		if s.state != "asleep" || f.state[s.sid] != "SUSPENDED" {
			t.Fatalf("slot %s is %s / %s", s.sid, s.state, f.state[s.sid])
		}
	}
}

func TestLaunchShootEscape(t *testing.T) {
	f := newFake(10, 0, 0)
	g, c := testGame(f)
	g.fillPool(context.Background())
	pl := platform.Player{ID: "p1", Nick: "Ada", Color: "blue"}
	run, err := g.Start(pl)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	l, err := g.Launch(ctx, "p1", run)
	if err != nil {
		t.Fatal(err)
	}
	if f.count("RUNNING") != 1 || f.resumes != 1 {
		t.Fatalf("after launch: running %d resumes %d", f.count("RUNNING"), f.resumes)
	}
	// Wave 1 allows one target in the air.
	if _, err := g.Launch(ctx, "p1", run); !errors.Is(err, ErrBusy) {
		t.Fatalf("second launch in wave 1: %v", err)
	}
	// Too soon after launch, then a real hit.
	if _, err := g.Shoot(ctx, "p1", run, l.Flight); err == nil {
		t.Fatal("hit right after launch counted")
	}
	c.add(time.Second)
	h, err := g.Shoot(ctx, "p1", run, l.Flight)
	if err != nil {
		t.Fatal(err)
	}
	if h.Points != l.Points || h.Mult != 1 || !h.Asleep || f.count("RUNNING") != 0 {
		t.Fatalf("hit = %+v, running %d", h, f.count("RUNNING"))
	}
	if _, err := g.Shoot(ctx, "p1", run, l.Flight); err == nil {
		t.Fatal("double hit counted")
	}
	// Someone else can't shoot this run's targets.
	l2, _ := g.Launch(ctx, "p1", run)
	c.add(time.Second)
	if _, err := g.Shoot(ctx, "p2", run, l2.Flight); !errors.Is(err, platform.ErrNotYours) {
		t.Fatalf("other player's hit: %v", err)
	}
	// An escape: no points, back to sleep.
	if err := g.Escape("p1", run, l2.Flight); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return f.count("RUNNING") == 0 })
	res, _ := g.Finish("p1", run)
	if res.Score != h.Points || res.Hits != 1 || res.Escapes != 1 || res.Rank != 1 {
		t.Fatalf("result = %+v", res)
	}
}

func TestComboAndWaves(t *testing.T) {
	for c, want := range map[int]int{1: 1, 2: 1, 3: 2, 4: 2, 5: 3, 7: 4, 9: 5, 20: 5} {
		if got := Mult(c); got != want {
			t.Errorf("Mult(%d) = %d, want %d", c, got, want)
		}
	}
	if Wave(0) != 1 || Wave(14*time.Second) != 1 || Wave(15*time.Second) != 2 || Wave(74*time.Second) != 5 {
		t.Error("waves")
	}
	if perRun(1) != 1 || perRun(3) != 2 || perRun(5) != 3 || perRun(9) != 3 {
		t.Error("perRun")
	}
	f := newFake(20, 0, 0)
	g, c := testGame(f)
	g.fillPool(context.Background())
	run, _ := g.Start(platform.Player{ID: "p1", Nick: "Ada"})
	total := 0
	for i := range 5 {
		l, err := g.Launch(context.Background(), "p1", run)
		if err != nil {
			t.Fatal(err)
		}
		c.add(500 * time.Millisecond)
		h, err := g.Shoot(context.Background(), "p1", run, l.Flight)
		if err != nil {
			t.Fatal(err)
		}
		if h.Combo != i+1 || h.Mult != Mult(i+1) || h.Points != l.Points*Mult(i+1) {
			t.Fatalf("hit %d = %+v (launched %+v)", i, h, l)
		}
		total += h.Points
	}
	c.add(3 * time.Second) // past the combo window: the next hit starts again
	l, _ := g.Launch(context.Background(), "p1", run)
	c.add(time.Second)
	if h, _ := g.Shoot(context.Background(), "p1", run, l.Flight); h.Combo != 1 {
		t.Fatalf("combo after a pause = %d", h.Combo)
	}
	// The run ends at RunSeconds; launches stop, and housekeeping closes it.
	c.add(RunSeconds * time.Second)
	if _, err := g.Launch(context.Background(), "p1", run); !errors.Is(err, ErrRunOver) {
		t.Fatalf("launch after the run: %v", err)
	}
	c.add(5 * time.Second)
	g.housekeep()
	if !g.runs[run].done {
		t.Fatal("housekeep left the run open")
	}
	_ = total
}

func TestArcadeAwakeCap(t *testing.T) {
	f := newFake(30, 0, 0)
	g, c := testGame(f)
	g.fillPool(context.Background())
	launched := 0
	for i := range 8 {
		run, _ := g.Start(platform.Player{ID: fmt.Sprint("p", i), Nick: fmt.Sprint("P", i)})
		c.add(31 * time.Second) // wave 3: two in the air per run
		for range 2 {
			if _, err := g.Launch(context.Background(), fmt.Sprint("p", i), run); err == nil {
				launched++
			}
		}
		c.add(-31 * time.Second)
	}
	if launched != maxAwake || f.count("RUNNING") != maxAwake {
		t.Fatalf("launched %d, running %d, want %d", launched, f.count("RUNNING"), maxAwake)
	}
}

func TestStaleFlightsSleep(t *testing.T) {
	f := newFake(5, 0, 0)
	g, c := testGame(f)
	g.fillPool(context.Background())
	run, _ := g.Start(platform.Player{ID: "p1", Nick: "Ada"})
	if _, err := g.Launch(context.Background(), "p1", run); err != nil {
		t.Fatal(err)
	}
	c.add(g.idle + time.Second)
	g.housekeep()
	waitFor(t, func() bool { return f.count("RUNNING") == 0 })
	if g.runs[run].escapes != 1 {
		t.Fatalf("escapes = %d", g.runs[run].escapes)
	}
}

func TestFailedSuspendRetires(t *testing.T) {
	f := newFake(3, 0, 0)
	g, c := testGame(f)
	g.fillPool(context.Background())
	run, _ := g.Start(platform.Player{ID: "p1", Nick: "Ada"})
	l, _ := g.Launch(context.Background(), "p1", run)
	c.add(time.Second)
	f.failNext = errors.New("rpc error: Unavailable")
	h, err := g.Shoot(context.Background(), "p1", run, l.Flight)
	if err != nil || h.Asleep || h.Points == 0 {
		t.Fatalf("hit with a failed suspend = %+v, %v", h, err)
	}
	broken := 0
	for _, s := range g.slots {
		if s.state == "broken" {
			broken++
		}
	}
	if broken != 1 {
		t.Fatalf("broken slots = %d", broken)
	}
}

func TestTopAndBeat(t *testing.T) {
	g, _ := testGame(newFake(0, 0, 0))
	for _, x := range []struct {
		id, nick string
		score    int
	}{{"a", "Ada", 900}, {"l", "Linus", 1200}, {"g", "Grace", 300}, {"h", "Hedy", 0}} {
		g.runs[x.id] = &run{id: "r" + x.id, player: x.id, nick: x.nick, score: x.score, hits: x.score / 100}
		g.runs["r"+x.id] = g.runs[x.id]
		g.recordBestLocked(g.runs[x.id])
	}
	top := g.Top(10)
	if len(top) != 3 || top[0].Nick != "Linus" || top[1].Nick != "Ada" || top[2].Nick != "Grace" {
		t.Fatalf("top = %+v", top)
	}
	res, err := g.Finish("a", "ra")
	if err != nil || res.Rank != 2 || res.Beat == nil || res.Beat.Nick != "Linus" || res.Beat.By != 301 || res.Beat.Rank != 1 {
		t.Fatalf("finish = %+v, %v", res, err)
	}
	// A worse run doesn't lower a best.
	g.runs["ra2"] = &run{id: "ra2", player: "a", nick: "Ada", score: 100}
	g.recordBestLocked(g.runs["ra2"])
	if g.Top(1)[0].Score != 1200 || g.bests["a"].score != 900 {
		t.Fatal("best lowered")
	}
}

func waitFor(t *testing.T, ok func() bool) {
	t.Helper()
	for range 200 {
		if ok() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("condition never held")
}
