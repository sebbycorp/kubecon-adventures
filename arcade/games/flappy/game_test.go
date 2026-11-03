package flappy

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
	deleted, prompts  int
	failResume        bool
	unsettledSuspends int // the next n suspends answer "not yet settled"
	created           []string
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
	f.created = append(f.created, id)
	return id, nil
}
func (f *fakeOps) Prompt(context.Context, string, string) (kagent.Reply, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.prompts++
	return kagent.Reply{}, nil
}
func (f *fakeOps) Suspend(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.unsettledSuspends > 0 {
		f.unsettledSuspends--
		return errors.New("rpc error: code = FailedPrecondition desc = lifecycle work is not yet settled")
	}
	if _, ok := f.state[id]; !ok {
		return errors.New("no such session")
	}
	f.suspends++
	f.state[id] = "SUSPENDED"
	return nil
}
func (f *fakeOps) Resume(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failResume {
		return errors.New("resume broke")
	}
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
func (f *fakeOps) running() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, s := range f.state {
		if s == "RUNNING" {
			n++
		}
	}
	return n
}

// ready is a game with its pool built, on a clock the test moves.
func ready(t *testing.T, f *fakeOps) (*Game, *time.Time) {
	t.Helper()
	g := New(platform.New(nil), f)
	now := time.Unix(1_800_000_000, 0)
	g.now = func() time.Time { return now }
	g.sweep(context.Background())
	g.fillPool(context.Background())
	if len(g.slots) != poolSize {
		t.Fatalf("pool = %d, want %d", len(g.slots), poolSize)
	}
	return g, &now
}

func pl(id string) platform.Player {
	return platform.Player{ID: id, Nick: "nick-" + id, Color: "green"}
}

func TestRulesPlausible(t *testing.T) {
	r := Default
	if r.MaxGaps(r.Countdown) != 0 {
		t.Fatalf("gaps possible before swimming: %d", r.MaxGaps(r.Countdown))
	}
	// 60 s of swimming covers about 165*60+2.4*1800 = 14220 units: about 55 pillars.
	if n := r.MaxGaps(63); n < 50 || n > 62 {
		t.Fatalf("MaxGaps(63) = %d", n)
	}
	if !r.Plausible(20, 10, 30) || r.Plausible(20, 10, 5) || r.Plausible(5, 7, 30) || r.Plausible(-1, 0, 30) {
		t.Fatal("plausibility checks wrong")
	}
	if Score(12, 3) != 27 {
		t.Fatalf("Score = %d", Score(12, 3))
	}
	if d := r.Distance(1000); d != r.Distance(500)+r.MaxSpeed*500 {
		t.Fatal("speed does not cap")
	}
}

func TestSweepAndPool(t *testing.T) {
	f := newFake(3)
	g, _ := ready(t, f)
	if f.deleted != 3 {
		t.Fatalf("sweep deleted %d of 3 old sessions", f.deleted)
	}
	if f.prompts != poolSize || f.running() != 0 {
		t.Fatalf("pool: %d placing turns, %d left running", f.prompts, f.running())
	}
	g.shutdown()
	if f.deleted != 3+poolSize {
		t.Fatalf("shutdown deleted %d", f.deleted-3)
	}
	select {
	case <-g.Stopped():
	default:
		t.Fatal("Stopped not closed")
	}
}

func TestRunWakesAndCrashSnapshots(t *testing.T) {
	f := newFake(0)
	g, now := ready(t, f)
	ctx := context.Background()
	s, err := g.Start(ctx, pl("a"))
	if err != nil || s.Agent != "awake" || f.resumes != 1 || f.running() != 1 {
		t.Fatalf("start: %+v err %v resumes %d running %d", s, err, f.resumes, f.running())
	}
	*now = now.Add(40 * time.Second)
	res, err := g.Crash(ctx, "a", s.Run, 20, 6)
	if err != nil || !res.Counted || res.Score != 50 || !res.Asleep || f.running() != 0 || res.Rank != 1 || res.Best != 50 || !res.NewBest {
		t.Fatalf("crash: %+v err %v running %d", res, err, f.running())
	}
	if _, err := g.Crash(ctx, "a", s.Run, 21, 6); !errors.Is(err, ErrRunOver) {
		t.Fatalf("second crash: %v", err)
	}
	// Play again: the same octopus wakes for the same player.
	s2, _ := g.Start(ctx, pl("a"))
	if g.runs[s2.Run].slot == nil {
		t.Fatal("play again: no octopus woke")
	}
	mine := 0
	for _, sl := range g.slots {
		if sl.owner == "a" {
			mine++
		}
	}
	if mine != 1 {
		t.Fatalf("player a owns %d octopuses, want their old one back", mine)
	}
}

func TestImplausibleScoreIsNotCounted(t *testing.T) {
	f := newFake(0)
	g, now := ready(t, f)
	ctx := context.Background()
	s, _ := g.Start(ctx, pl("a"))
	*now = now.Add(5 * time.Second)
	res, err := g.Crash(ctx, "a", s.Run, 40, 0)
	if err != nil || res.Counted || res.Score != 0 || !res.Asleep || len(g.Top(10)) != 0 {
		t.Fatalf("implausible: %+v err %v board %v", res, err, g.Top(10))
	}
}

func TestOthersRunIsNotYours(t *testing.T) {
	f := newFake(0)
	g, _ := ready(t, f)
	s, _ := g.Start(context.Background(), pl("a"))
	if _, err := g.Crash(context.Background(), "b", s.Run, 0, 0); !errors.Is(err, platform.ErrNotYours) {
		t.Fatalf("err = %v", err)
	}
	if _, err := g.Crash(context.Background(), "b", "nope", 0, 0); !errors.Is(err, ErrNoRun) {
		t.Fatalf("err = %v", err)
	}
}

func TestAwakeCapAndFailedWakeStillPlay(t *testing.T) {
	f := newFake(0)
	g, _ := ready(t, f)
	ctx := context.Background()
	for i := range maxAwake {
		if s, _ := g.Start(ctx, pl(fmt.Sprint("p", i))); s.Agent != "awake" {
			t.Fatalf("player %d: %+v", i, s)
		}
	}
	s, err := g.Start(ctx, pl("late"))
	if err != nil || s.Agent != "none" || s.Reason != "every sandbox is busy" || s.Run == "" {
		t.Fatalf("over the cap: %+v err %v", s, err)
	}
	if f.running() != maxAwake {
		t.Fatalf("running = %d", f.running())
	}
	// A run without an agent still scores.
	res, err := g.Crash(ctx, "late", s.Run, 0, 0)
	if err != nil || res.Agent || res.Asleep {
		t.Fatalf("agentless crash: %+v err %v", res, err)
	}

	f2 := newFake(0)
	g2, _ := ready(t, f2)
	f2.failResume = true
	s2, err := g2.Start(ctx, pl("a"))
	if err != nil || s2.Agent != "none" || s2.Reason == "" {
		t.Fatalf("failed wake: %+v err %v", s2, err)
	}
}

func TestNewStartAbandonsTheOldRun(t *testing.T) {
	f := newFake(0)
	g, _ := ready(t, f)
	ctx := context.Background()
	s1, _ := g.Start(ctx, pl("a"))
	s2, _ := g.Start(ctx, pl("a"))
	if !g.runs[s1.Run].done || g.runs[s2.Run].done {
		t.Fatal("the first run should be over")
	}
	deadline := time.Now().Add(2 * time.Second)
	for f.running() != 1 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if f.running() != 1 {
		t.Fatalf("running = %d, want only the new run's octopus", f.running())
	}
}

func TestSuspendRetriesWhileUnsettled(t *testing.T) {
	f := newFake(0)
	g, now := ready(t, f)
	ctx := context.Background()
	s, _ := g.Start(ctx, pl("a"))
	f.mu.Lock()
	f.unsettledSuspends = 2
	f.mu.Unlock()
	*now = now.Add(20 * time.Second)
	res, err := g.Crash(ctx, "a", s.Run, 5, 1)
	if err != nil || !res.Asleep || f.running() != 0 {
		t.Fatalf("crash after unsettled suspends: %+v err %v", res, err)
	}
}

func TestBoardRanksAndBeat(t *testing.T) {
	f := newFake(0)
	g, now := ready(t, f)
	ctx := context.Background()
	play := func(id string, gaps, bubbles int) Result {
		s, _ := g.Start(ctx, pl(id))
		*now = now.Add(2 * time.Minute)
		r, err := g.Crash(ctx, id, s.Run, gaps, bubbles)
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	play("a", 30, 5) // 55
	play("b", 10, 2) // 20
	r := play("c", 20, 3)
	if r.Rank != 2 || r.Beat == nil || r.Beat.Nick != "nick-a" || r.Beat.By != 21 || r.Beat.Rank != 1 {
		t.Fatalf("c: %+v beat %+v", r, r.Beat)
	}
	worse := play("a", 3, 0) // a worse run doesn't replace a's best
	if worse.Best != 55 || worse.NewBest {
		t.Fatalf("a's worse run: %+v", worse)
	}
	top := g.Top(10)
	if len(top) != 3 || top[0].Score != 55 || top[1].Score != 35 || top[2].Score != 20 || top[0].Detail != "30 gaps · 5 bubbles" {
		t.Fatalf("top = %+v", top)
	}
}

func TestHousekeepAbandonsLostRuns(t *testing.T) {
	f := newFake(0)
	g, now := ready(t, f)
	s, _ := g.Start(context.Background(), pl("a"))
	*now = now.Add(maxRunAge + time.Second)
	g.housekeep()
	if !g.runs[s.Run].done {
		t.Fatal("lost run not abandoned")
	}
	deadline := time.Now().Add(2 * time.Second)
	for f.running() != 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if f.running() != 0 {
		t.Fatal("lost run's octopus still awake")
	}
}
