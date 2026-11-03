package defense

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/sebbycorp/kubecon-adventures/arcade/internal/kagent"
)

// fakeOps is an in-memory Ops.
type fakeOps struct {
	mu       sync.Mutex
	seq      int
	live     map[string]bool
	gate     chan struct{} // when set, Suspend waits for it
	failTask error
	spawned  atomic.Int64
	deleted  atomic.Int64
}

func newFakeOps() *fakeOps { return &fakeOps{live: map[string]bool{}} }

func (f *fakeOps) Spawn(ctx context.Context, name string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.seq++
	id := fmt.Sprintf("sess-%d", f.seq)
	f.live[id] = true
	f.spawned.Add(1)
	return id, nil
}

func (f *fakeOps) Task(ctx context.Context, sid, role string) (string, int64, error) {
	if f.failTask != nil {
		return "", 0, f.failTask
	}
	return role + " done", 300, nil
}

func (f *fakeOps) Suspend(ctx context.Context, sid string) (int64, error) {
	if f.gate != nil {
		select {
		case <-f.gate:
		case <-ctx.Done():
			return 0, ctx.Err()
		}
	}
	return 5, nil
}

func (f *fakeOps) Delete(ctx context.Context, sid string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.live, sid)
	f.deleted.Add(1)
	return nil
}

func (f *fakeOps) ListSessions(ctx context.Context) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var ids []string
	for id := range f.live {
		ids = append(ids, id)
	}
	return ids, nil
}

func (f *fakeOps) liveCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.live)
}

func TestRunnerRunsAndReports(t *testing.T) {
	f := newFakeOps()
	r := NewRunner(f)
	defer r.Close(context.Background())
	if !r.Submit(Intent{Kind: "spawn", ID: "a1"}, "n") || !r.Submit(Intent{Kind: "task", ID: "a1", Session: "x", Role: "coder"}, "") {
		t.Fatal("submit refused")
	}
	if r.Submit(Intent{Kind: "bogus"}, "") {
		t.Fatal("unknown kind accepted")
	}
	got := map[string]Result{}
	for range 2 {
		select {
		case res := <-r.Results():
			got[res.Kind] = res
		case <-time.After(2 * time.Second):
			t.Fatal("no result")
		}
	}
	if s := got["spawn"]; !s.OK || s.Session == "" || s.ID != "a1" {
		t.Fatalf("spawn = %+v", s)
	}
	if tk := got["task"]; !tk.OK || tk.Say != "coder done" || tk.Tokens != 300 {
		t.Fatalf("task = %+v", tk)
	}
}

func TestRunnerLanesAndClose(t *testing.T) {
	f := newFakeOps()
	f.gate = make(chan struct{})
	r := NewRunner(f)
	for i := range 20 {
		r.Submit(Intent{Kind: "suspend", ID: fmt.Sprint(i), Session: "s"}, "")
	}
	deadline := time.Now().Add(2 * time.Second)
	for r.InFlight("suspend") < lanes["suspend"] && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if n := r.InFlight("suspend"); n != lanes["suspend"] {
		t.Fatalf("in flight = %d, want the lane's %d", n, lanes["suspend"])
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	r.Close(ctx)
	if r.Submit(Intent{Kind: "spawn"}, "") {
		t.Fatal("closed runner accepted an op")
	}
	n := 0
	for len(r.Results()) > 0 {
		if res := <-r.Results(); res.OK {
			t.Fatalf("op succeeded after close: %+v", res)
		}
		n++
	}
	if n != 20 {
		t.Fatalf("%d results after close, want 20", n)
	}
}

// fakeAPI is the kagent client the adapter talks to.
type fakeAPI struct {
	mu         sync.Mutex
	busyUntil  time.Time // Suspend answers "active task" until then
	notSettled int       // Delete answers FailedPrecondition this many times
	reply      kagent.Reply
	replyErr   error
	prompts    []string
}

func (a *fakeAPI) CreateSession(ctx context.Context, name string) (string, error) {
	return "sid-" + name, nil
}
func (a *fakeAPI) Prompt(ctx context.Context, sid, text string) (kagent.Reply, error) {
	a.mu.Lock()
	a.prompts = append(a.prompts, text)
	a.mu.Unlock()
	return a.reply, a.replyErr
}
func (a *fakeAPI) Suspend(ctx context.Context, sid string) error {
	if time.Now().Before(a.busyUntil) {
		return status.Error(codes.Aborted, "session has an active task or checkpoint: resource conflict")
	}
	return nil
}
func (a *fakeAPI) Delete(ctx context.Context, sid string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.notSettled > 0 {
		a.notSettled--
		return status.Error(codes.FailedPrecondition, "not yet settled")
	}
	return nil
}
func (a *fakeAPI) ListSessionIDs(ctx context.Context) ([]string, error) { return []string{"x"}, nil }

func TestAgentOpsSuspendWaitsForTurn(t *testing.T) {
	api := &fakeAPI{busyUntil: time.Now().Add(300 * time.Millisecond)}
	o := &agentOps{k: api}
	start := time.Now()
	ms, err := o.Suspend(context.Background(), "s")
	if err != nil || time.Since(start) < 300*time.Millisecond || ms < 1 {
		t.Fatalf("suspend err=%v after %v ms=%d", err, time.Since(start), ms)
	}
	api.busyUntil = time.Now().Add(time.Hour)
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	if _, err := o.Suspend(ctx, "s"); err == nil {
		t.Fatal("suspend of a session busy past the deadline succeeded")
	}
	api.notSettled = 2
	if err := o.Delete(context.Background(), "s"); err != nil {
		t.Fatalf("delete through the settle window: %v", err)
	}
}

func TestAgentOpsTask(t *testing.T) {
	api := &fakeAPI{reply: kagent.Reply{JSON: `{"say":"  Tests   green   across the board, ship it now please ok thanks bye!!  "}`, Usage: kagent.Usage{Input: 100, Output: 20}}}
	o := &agentOps{k: api}
	say, tokens, err := o.Task(context.Background(), "s1", "tester")
	if err != nil || tokens != 120 || say == "" || len([]rune(say)) > maxSay {
		t.Fatalf("say=%q tokens=%d err=%v", say, tokens, err)
	}
	if p := api.prompts[0]; p != taskFor("s1", "tester") {
		t.Fatalf("prompt = %q", p)
	}
	api.replyErr = errors.New("task failed: output_validation_failed")
	if say, _, err := o.Task(context.Background(), "s1", "tester"); err != nil || say != fallbacks["tester"] {
		t.Fatalf("validation failure: say=%q err=%v", say, err)
	}
	api.replyErr = errors.New("LLM error: 429 too many tokens")
	if _, _, err := o.Task(context.Background(), "s1", "tester"); err == nil || err.Error()[:7] != "policy:" {
		t.Fatalf("rate limit err = %v", err)
	}
	api.replyErr, api.reply = nil, kagent.Reply{Text: "prose, not JSON"}
	if say, _, err := o.Task(context.Background(), "s1", "coder"); err != nil || say != fallbacks["coder"] {
		t.Fatalf("prose reply: say=%q err=%v", say, err)
	}
}

func TestTasksCoverEveryRole(t *testing.T) {
	for _, r := range Roles {
		if len(tasks[r.Name]) == 0 || fallbacks[r.Name] == "" {
			t.Errorf("role %s has no task or fallback", r.Name)
		}
	}
}
