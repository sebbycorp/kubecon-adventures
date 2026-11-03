package heist

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

const wait = 5 * time.Second

// call is one blocked fakeOps call; close(release) lets it return.
type call struct {
	kind, id string
	release  chan struct{}
}

// fakeOps blocks every call until the test releases it (or the op context ends).
type fakeOps struct {
	started chan *call
	err     error
	ms      int64
}

func newFakeOps() *fakeOps { return &fakeOps{started: make(chan *call, 512)} }

func (f *fakeOps) block(ctx context.Context, kind, id string) error {
	c := &call{kind: kind, id: id, release: make(chan struct{})}
	f.started <- c
	select {
	case <-c.release:
		return f.err
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (f *fakeOps) ListSessions(ctx context.Context) ([]string, error) { return nil, nil }

func (f *fakeOps) Spawn(ctx context.Context, name string) (string, string, error) {
	err := f.block(ctx, "spawn", name)
	return "sess-" + name, "node-1", err
}

func (f *fakeOps) SpawnAlly(ctx context.Context, name string) (string, string, string, error) {
	err := f.block(ctx, "ally", name)
	return "sess-" + name, "node-1", "at your service", err
}

func (f *fakeOps) Step(ctx context.Context, id string, step, steps int) (StepResult, error) {
	err := f.block(ctx, "step", id)
	return StepResult{Say: "hi", Loot: 3, Done: step == steps}, err
}

func (f *fakeOps) Freeze(ctx context.Context, id string) (int64, error) {
	return f.ms, f.block(ctx, "freeze", id)
}

func (f *fakeOps) Spring(ctx context.Context, id string) (string, string, int64, error) {
	err := f.block(ctx, "spring", id)
	return "node-2", "sprung!", f.ms, err
}

// Extract, like the real adapter, checkpoints then deletes the session, and returns the
// checkpoint's ID.
func (f *fakeOps) Extract(ctx context.Context, id string) (string, string, int64, error) {
	err := f.block(ctx, "extract", id)
	return "booked!", "cp-" + id, f.ms, err
}

func (f *fakeOps) DeleteCheckpoint(ctx context.Context, id string) error {
	return f.block(ctx, "delete-checkpoint", id)
}

func (f *fakeOps) ListCheckpoints(ctx context.Context, sid string) ([]string, error) { return nil, nil }

func (f *fakeOps) Delete(ctx context.Context, id string) error {
	return f.block(ctx, "delete", id)
}

// next waits for the next call to start.
func (f *fakeOps) next(t *testing.T) *call {
	t.Helper()
	select {
	case c := <-f.started:
		return c
	case <-time.After(wait):
		t.Fatal("timed out waiting for an op to start")
		return nil
	}
}

// drain waits for n calls to start and returns them.
func (f *fakeOps) drain(t *testing.T, n int) []*call {
	t.Helper()
	out := make([]*call, n)
	for i := range out {
		out[i] = f.next(t)
	}
	return out
}

// noStart fails if another call starts within a short window.
func (f *fakeOps) noStart(t *testing.T) {
	t.Helper()
	select {
	case c := <-f.started:
		t.Fatalf("unexpected start of %s %s", c.kind, c.id)
	case <-time.After(50 * time.Millisecond):
	}
}

// noStartOf fails if a call of one of kinds starts within a short window. Calls of
// other kinds (say a step the test left blocked, whose goroutine starts late) are
// ignored.
func (f *fakeOps) noStartOf(t *testing.T, kinds ...string) {
	t.Helper()
	deadline := time.After(50 * time.Millisecond)
	for {
		select {
		case c := <-f.started:
			if slices.Contains(kinds, c.kind) {
				t.Fatalf("unexpected start of %s %s", c.kind, c.id)
			}
		case <-deadline:
			return
		}
	}
}

func result(t *testing.T, s *Scheduler) Result {
	t.Helper()
	select {
	case r := <-s.Results():
		return r
	case <-time.After(wait):
		t.Fatal("timed out waiting for a result")
		return Result{}
	}
}

func waitInFlight(t *testing.T, s *Scheduler, steps, resumes, queued int) {
	t.Helper()
	deadline := time.Now().Add(wait)
	for {
		a, b, c := s.InFlight()
		if a == steps && b == resumes && c == queued {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("InFlight = %d,%d,%d; want %d,%d,%d", a, b, c, steps, resumes, queued)
		}
		runtime.Gosched()
	}
}

func TestStepCap(t *testing.T) {
	f := newFakeOps()
	s := NewScheduler(f, 24, 8)
	defer s.Close()
	for i := 0; i < 30; i++ {
		if !s.Submit("step", fmt.Sprintf("t%d", i), "s", 1, 5) {
			t.Fatalf("submit %d refused", i)
		}
	}
	calls := f.drain(t, 24)
	f.noStart(t)
	waitInFlight(t, s, 24, 0, 6)
	close(calls[0].release)
	r := result(t, s)
	if !r.OK || r.Kind != "step" || r.Step.Say != "hi" || r.Step.Loot != 3 {
		t.Fatalf("result = %+v", r)
	}
	f.next(t) // a queued step took the freed slot
	waitInFlight(t, s, 24, 0, 5)
}

func TestResumeCap(t *testing.T) {
	f := newFakeOps()
	s := NewScheduler(f, 24, 8)
	defer s.Close()
	for i := 0; i < 10; i++ {
		s.Submit("spring", fmt.Sprintf("t%d", i), "s", 0, 0)
	}
	calls := f.drain(t, 8)
	f.noStart(t)
	waitInFlight(t, s, 0, 8, 2)
	close(calls[3].release)
	r := result(t, s)
	if !r.OK || r.Node != "node-2" || r.Step.Say != "sprung!" {
		t.Fatalf("result = %+v", r)
	}
	f.next(t)
	waitInFlight(t, s, 0, 8, 1)
}

func TestExtractUsesResume(t *testing.T) {
	for _, uses := range []bool{false, true} {
		f := newFakeOps()
		s := NewScheduler(f, 24, 2)
		s.ExtractUsesResume = uses
		for i := 0; i < 4; i++ {
			s.Submit("extract", fmt.Sprintf("t%d", i), "s", 0, 0)
		}
		if uses {
			f.drain(t, 2)
			f.noStart(t)
			waitInFlight(t, s, 0, 2, 2)
		} else {
			f.drain(t, 4)
			waitInFlight(t, s, 0, 0, 0)
		}
		s.Close()
	}
}

func TestUncappedKinds(t *testing.T) {
	f := newFakeOps()
	s := NewScheduler(f, 1, 1)
	defer s.Close()
	s.Submit("step", "a", "s", 1, 2) // fills the step cap
	s.Submit("spring", "b", "s", 0, 0)
	for i, k := range []string{"freeze", "delete", "spawn"} {
		if !s.Submit(k, fmt.Sprintf("u%d", i), "", 0, 0) {
			t.Fatalf("%s refused", k)
		}
	}
	f.drain(t, 5)
	waitInFlight(t, s, 1, 1, 0)
}

func TestSpawnUsesTargetAsName(t *testing.T) {
	f := newFakeOps()
	s := NewScheduler(f, 24, 8)
	defer s.Close()
	s.Submit("spawn", "bot-7", "", 0, 0)
	c := f.next(t)
	if c.kind != "spawn" || c.id != "bot-7" {
		t.Fatalf("call = %+v", c)
	}
	close(c.release)
	r := result(t, s)
	if !r.OK || r.Spawned != "sess-bot-7" || r.Node != "node-1" || r.Target != "bot-7" {
		t.Fatalf("result = %+v", r)
	}
}

func TestMsFromOpElseWall(t *testing.T) {
	f := newFakeOps()
	f.ms = 1234
	s := NewScheduler(f, 24, 8)
	defer s.Close()
	s.Submit("freeze", "a", "s", 0, 0)
	close(f.next(t).release)
	if r := result(t, s); r.Ms != 1234 {
		t.Fatalf("freeze ms = %d, want 1234", r.Ms)
	}
	f.ms = 0
	s.Submit("delete", "b", "s", 0, 0)
	close(f.next(t).release)
	if r := result(t, s); !r.OK || r.Ms < 0 {
		t.Fatalf("delete result = %+v", r)
	}
}

func TestStepAndControlLanes(t *testing.T) {
	f := newFakeOps()
	s := NewScheduler(f, 24, 8)
	defer s.Close()
	if !s.Submit("step", "a", "s", 1, 5) {
		t.Fatal("first step refused")
	}
	if s.Submit("step", "a", "s", 2, 5) {
		t.Fatal("second step for the same target accepted")
	}
	if !s.Submit("freeze", "a", "s", 0, 0) {
		t.Fatal("freeze refused while a step is in flight")
	}
	for _, k := range []string{"spawn", "freeze", "spring", "extract", "delete"} {
		if s.Submit(k, "a", "s", 0, 0) {
			t.Fatalf("second control op %s accepted", k)
		}
	}
	calls := f.drain(t, 2)
	for _, c := range calls {
		close(c.release)
	}
	result(t, s)
	result(t, s)
	if !s.Submit("step", "a", "s", 2, 5) || !s.Submit("freeze", "a", "s", 0, 0) {
		t.Fatal("submit after results refused")
	}
}

func TestErrorPassesThrough(t *testing.T) {
	f := newFakeOps()
	f.err = errors.New("policy: denied")
	s := NewScheduler(f, 24, 8)
	defer s.Close()
	s.Submit("step", "a", "s", 1, 5)
	close(f.next(t).release)
	r := result(t, s)
	if r.OK || r.Err != "policy: denied" {
		t.Fatalf("result = %+v", r)
	}
}

func TestTimeoutReturnsFailure(t *testing.T) {
	f := newFakeOps()
	s := NewScheduler(f, 24, 8)
	s.timeout = 50 * time.Millisecond
	defer s.Close()
	s.Submit("step", "a", "s", 1, 5)
	r := result(t, s)
	if r.OK || r.Err == "" || r.Target != "a" {
		t.Fatalf("result = %+v", r)
	}
}

func TestSlowDrainDoesNotBlockSlots(t *testing.T) {
	f := newFakeOps()
	s := NewScheduler(f, 2, 1)
	defer s.Close()
	const n = 300 // more than the results buffer
	for i := 0; i < n; i++ {
		s.Submit("freeze", fmt.Sprintf("t%d", i), "s", 0, 0)
	}
	for _, c := range f.drain(t, n) {
		close(c.release)
	}
	// the game drains late; every result still arrives
	seen := map[string]bool{}
	for i := 0; i < n; i++ {
		seen[result(t, s).Target] = true
	}
	if len(seen) != n {
		t.Fatalf("got %d distinct results, want %d", len(seen), n)
	}
}

func TestCloseStopsWorkers(t *testing.T) {
	runtime.GC()
	base := runtime.NumGoroutine()
	f := newFakeOps()
	s := NewScheduler(f, 4, 2)
	for i := 0; i < 10; i++ {
		s.Submit("step", fmt.Sprintf("t%d", i), "s", 1, 5)
	}
	f.drain(t, 4)
	s.Close()
	if s.Submit("freeze", "x", "s", 0, 0) {
		t.Fatal("Submit after Close accepted")
	}
	f.noStart(t) // queued work was dropped, not started
	deadline := time.Now().Add(time.Second)
	for runtime.NumGoroutine() > base {
		if time.Now().After(deadline) {
			t.Fatalf("goroutines = %d, baseline %d", runtime.NumGoroutine(), base)
		}
		time.Sleep(5 * time.Millisecond)
	}
	s.Close() // idempotent
}

func TestConcurrentSubmit(t *testing.T) {
	f := newFakeOps()
	s := NewScheduler(f, 3, 3)
	defer s.Close()
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s.Submit("step", fmt.Sprintf("t%d", i), "s", 1, 2)
			s.InFlight()
		}()
	}
	wg.Wait()
}

// panicOps panics on every call.
type panicOps struct{ fakeOps }

func (panicOps) Step(ctx context.Context, id string, step, steps int) (StepResult, error) {
	panic("kaboom")
}

func TestPanickingOpBecomesFailedResult(t *testing.T) {
	s := NewScheduler(&panicOps{}, 1, 1)
	defer s.Close()
	if !s.Submit("step", "t1", "s1", 1, 4) {
		t.Fatal("submit refused")
	}
	r := result(t, s)
	if r.OK || !strings.HasPrefix(r.Err, "panic: ") || !strings.Contains(r.Err, "kaboom") {
		t.Fatalf("got %+v", r)
	}
	// The lane and the only step slot are free again.
	waitInFlight(t, s, 0, 0, 0)
	if !s.Submit("step", "t1", "s1", 2, 4) {
		t.Fatal("lane still busy after a panic")
	}
	if r := result(t, s); r.OK {
		t.Fatalf("second panic: %+v", r)
	}
}

func TestDropQueuedStep(t *testing.T) {
	f := newFakeOps()
	s := NewScheduler(f, 1, 1)
	t.Cleanup(s.Close)
	s.Submit("step", "a", "sa", 1, 4)
	f.next(t) // a's step runs and holds the only slot
	if !s.Submit("step", "b", "sb", 1, 4) {
		t.Fatal("queue refused")
	}
	if s.Drop("step", "a") {
		t.Fatal("dropped a running step")
	}
	if !s.Drop("step", "b") || s.Drop("step", "b") {
		t.Fatal("drop of the queued step")
	}
	if r := result(t, s); r.Kind != "step" || r.Target != "b" || r.OK || r.Err != "dropped" {
		t.Fatalf("result %+v", r)
	}
	if !s.Submit("step", "b", "sb", 1, 4) { // its lane is free again
		t.Fatal("lane still busy after drop")
	}
	if _, _, q := s.InFlight(); q != 1 {
		t.Fatalf("queued = %d", q)
	}
}
