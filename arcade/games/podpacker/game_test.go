package podpacker

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

// fakeOps is kagent in memory: sessions are RUNNING or SUSPENDED.
type fakeOps struct {
	mu                sync.Mutex
	state             map[string]string
	seq               int
	resumes, suspends int
	deleted           int
	failSuspend       bool
	unsettled         int // the next n suspends answer "not yet settled"
}

func newFake(existing int) *fakeOps {
	f := &fakeOps{state: map[string]string{}}
	for i := range existing {
		f.state[fmt.Sprintf("old-%d", i)] = "RUNNING"
	}
	return f
}

func (f *fakeOps) CreateSession(_ context.Context, name string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.seq++
	id := fmt.Sprintf("s%d-%s", f.seq, name)
	f.state[id] = "RUNNING"
	return id, nil
}
func (f *fakeOps) Prompt(context.Context, string, string) (kagent.Reply, error) {
	return kagent.Reply{}, nil
}
func (f *fakeOps) Suspend(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.unsettled > 0 {
		f.unsettled--
		return errors.New("rpc error: code = FailedPrecondition desc = lifecycle work is not yet settled")
	}
	if f.failSuspend {
		return errors.New("suspend broke")
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
func (f *fakeOps) Delete(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.state, id)
	f.deleted++
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

// clock is a settable time for the game.
type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *clock) add(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

func newGame(t *testing.T) (*Game, *fakeOps, *clock) {
	t.Helper()
	f := newFake(2)
	g := New(nil, f)
	c := &clock{t: time.Unix(1_800_000_000, 0)}
	g.now = c.now
	g.sweep(context.Background())
	g.fillPool(context.Background())
	return g, f, c
}

// waitAwake waits for n pods to be awake (resumes run in the background).
func waitAwake(t *testing.T, g *Game, n int) {
	t.Helper()
	for range 200 {
		g.mu.Lock()
		a := g.countLocked("awake")
		g.mu.Unlock()
		if a >= n {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("never got %d pods awake", n)
}

func TestRulesLevelsAndMaxScore(t *testing.T) {
	r := Default
	if r.Level(0) != 1 || r.Level(9) != 1 || r.Level(10) != 2 || r.Level(35) != 4 {
		t.Fatal("levels go up every 10 lines")
	}
	// 4 lines, all one back-to-back tetris at level 1: 1200, plus a combo bound and drops.
	if got := r.MaxScore(4, 10); got != 1200+500+10*20*2 {
		t.Fatalf("MaxScore(4,10) = %d", got)
	}
}

func TestPlausible(t *testing.T) {
	r := Default
	cases := []struct {
		name                                string
		score, lines, tetrises, pieces, srv int
		secs                                float64
		ok                                  bool
	}{
		{"a decent game", 4200, 20, 2, 60, 20, 120, true},
		{"nothing cleared", 300, 0, 0, 12, 0, 30, true},
		{"more lines than pieces fill", 1000, 10, 0, 20, 10, 60, false},
		{"pieces faster than a human", 100, 0, 0, 500, 0, 20, false},
		{"lines the server never saw", 1000, 12, 0, 40, 4, 60, false},
		{"too many tetrises", 3000, 8, 3, 30, 8, 60, false},
		{"score off the table", 999999, 8, 2, 30, 8, 60, false},
		{"negative", -5, 0, 0, 0, 0, 10, false},
	}
	for _, c := range cases {
		if got := r.Plausible(c.score, c.lines, c.tetrises, c.pieces, c.srv, c.secs); got != c.ok {
			t.Errorf("%s: Plausible = %v, want %v", c.name, got, c.ok)
		}
	}
}

func TestPoolSweepFillAndShutdown(t *testing.T) {
	g, f, _ := newGame(t)
	if f.deleted != 2 {
		t.Fatalf("sweep deleted %d old sessions, want 2", f.deleted)
	}
	if len(g.slots) != poolSize || g.countLocked("asleep") != poolSize {
		t.Fatalf("pool = %d slots, %d asleep", len(g.slots), g.countLocked("asleep"))
	}
	g.shutdown()
	if f.deleted != 2+poolSize {
		t.Fatalf("shutdown deleted %d, want %d", f.deleted-2, poolSize)
	}
}

func TestClearSnapshotsAwakePodsAndWakesThemAgain(t *testing.T) {
	g, f, c := newGame(t)
	pl := platform.Player{ID: "p1", Nick: "Ada", Color: "blue"}
	s, err := g.Start(pl)
	if err != nil {
		t.Fatal(err)
	}
	waitAwake(t, g, maxAwake)
	if g.countLocked("awake") > maxAwake {
		t.Fatal("more pods awake than the cap")
	}
	c.add(time.Second)
	res, err := g.Clear(context.Background(), "p1", s.Run, 4, 7)
	if err != nil || res.Snapped != 4 || res.Node != 7 {
		t.Fatalf("tetris clear = %+v, %v", res, err)
	}
	if f.suspends < poolSize+4 { // the pool's placing suspends, then 4 for the tetris
		t.Fatalf("suspends = %d", f.suspends)
	}
	// The pool wakes them again in the background.
	waitAwake(t, g, maxAwake)
	// A clear right after another is refused.
	if _, err := g.Clear(context.Background(), "p1", s.Run, 1, 3); !errors.Is(err, ErrTooFast) {
		t.Fatalf("second clear at once: %v", err)
	}
	c.add(time.Second)
	if _, err := g.Clear(context.Background(), "p2", s.Run, 1, 3); !errors.Is(err, platform.ErrNotYours) {
		t.Fatalf("someone else's run: %v", err)
	}
	if _, err := g.Clear(context.Background(), "p1", s.Run, 5, 3); err == nil {
		t.Fatal("5 lines at once accepted")
	}
}

func TestClearWithNoPodAwakeStillPlays(t *testing.T) {
	g, _, c := newGame(t)
	pl := platform.Player{ID: "p1", Nick: "Ada"}
	s, _ := g.Start(pl)
	// Put every pod to sleep before the resumes land.
	g.mu.Lock()
	for _, sl := range g.slots {
		sl.state = "asleep"
	}
	g.mu.Unlock()
	c.add(time.Second)
	res, err := g.Clear(context.Background(), "p1", s.Run, 2, 4)
	if err != nil || res.Snapped != 0 || res.Note == "" {
		t.Fatalf("clear with no pod awake = %+v, %v", res, err)
	}
}

func TestSuspendRetriesWhileSettling(t *testing.T) {
	g, f, c := newGame(t)
	s, _ := g.Start(platform.Player{ID: "p1", Nick: "Ada"})
	waitAwake(t, g, 1)
	f.mu.Lock()
	f.unsettled = 2
	f.mu.Unlock()
	c.add(time.Second)
	res, err := g.Clear(context.Background(), "p1", s.Run, 1, 2)
	if err != nil || res.Snapped != 1 {
		t.Fatalf("clear through 'not yet settled' = %+v, %v", res, err)
	}
}

func TestFinishBoardAndBeat(t *testing.T) {
	g, _, c := newGame(t)
	play := func(id, nick string, score, lines, pieces int) Result {
		t.Helper()
		s, err := g.Start(platform.Player{ID: id, Nick: nick, Color: "green"})
		if err != nil {
			t.Fatal(err)
		}
		for left := lines; left > 0; left -= 4 {
			c.add(2 * time.Second)
			waitAwake(t, g, 1)
			if _, err := g.Clear(context.Background(), id, s.Run, min(4, left), 5); err != nil {
				t.Fatal(err)
			}
		}
		c.add(time.Minute)
		res, err := g.Finish(id, s.Run, score, lines, 0, pieces)
		if err != nil {
			t.Fatal(err)
		}
		return res
	}
	a := play("p1", "Ada", 5000, 20, 60)
	if !a.Counted || a.Rank != 1 || a.Level != 3 || a.Snaps == 0 {
		t.Fatalf("Ada = %+v", a)
	}
	b := play("p2", "Linus", 2000, 8, 30)
	if b.Rank != 2 || b.Beat == nil || b.Beat.Nick != "Ada" || b.Beat.By != 3001 {
		t.Fatalf("Linus = %+v", b)
	}
	// An impossible score doesn't count.
	cheat := play("p3", "Mallory", 900000, 8, 30)
	if cheat.Counted || cheat.Rank != 0 {
		t.Fatalf("cheat = %+v", cheat)
	}
	top := g.Top(10)
	if len(top) != 2 || top[0].Nick != "Ada" || top[0].Score != 5000 || top[0].Detail != "20 lines · level 3" {
		t.Fatalf("top = %+v", top)
	}
	// A finished run can't be finished or cleared again.
	if _, err := g.Finish("p1", "nope", 1, 0, 0, 1); !errors.Is(err, ErrNoRun) {
		t.Fatalf("unknown run: %v", err)
	}
}

func TestIdlePoolGoesToSleep(t *testing.T) {
	g, _, c := newGame(t)
	s, _ := g.Start(platform.Player{ID: "p1", Nick: "Ada"})
	waitAwake(t, g, maxAwake)
	if _, err := g.Finish("p1", s.Run, 0, 0, 0, 1); err != nil {
		t.Fatal(err)
	}
	g.housekeep() // marks idle
	c.add(idleAfter + time.Second)
	g.housekeep()
	for range 200 {
		g.mu.Lock()
		a := g.countLocked("awake") + g.countLocked("sleeping")
		g.mu.Unlock()
		if a == 0 {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("awake pods never went back to sleep after the players left")
}
