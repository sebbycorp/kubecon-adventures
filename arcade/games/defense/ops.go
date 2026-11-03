// This file is the op runner: it runs the World's intents against Ops off the game
// loop, a bounded number at a time per kind, and hands back Results on a channel the
// loop drains without blocking.
package defense

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"
)

// Ops is what Cluster Defense needs from kagent (a fake in unit tests).
type Ops interface {
	// Spawn creates a session for one subagent; name is its session name.
	Spawn(ctx context.Context, name string) (session string, err error)
	// Task runs the session's one task turn as role and returns its line.
	Task(ctx context.Context, session, role string) (say string, tokens int64, err error)
	// Suspend suspends the session (waiting out a turn in flight); ms is the suspend alone.
	Suspend(ctx context.Context, session string) (ms int64, err error)
	Delete(ctx context.Context, session string) error
	ListSessions(ctx context.Context) ([]string, error)
}

// opTimeout bounds one op, including a suspend waiting for a turn to end.
const opTimeout = 45 * time.Second

// lanes is how many ops of each kind run at once.
var lanes = map[string]int{"spawn": 8, "task": 16, "suspend": 16, "delete": 8}

// maxQueued bounds the ops waiting or running, so a stuck kagent can't grow memory.
const maxQueued = 512

// Runner runs intents. Submit never blocks the game loop.
type Runner struct {
	ops     Ops
	sem     map[string]chan struct{}
	results chan Result
	ctx     context.Context
	stop    context.CancelFunc

	mu       sync.Mutex
	queued   int
	inflight map[string]int // per kind, running now
	closed   bool
	wg       sync.WaitGroup
}

// NewRunner runs ops with the lane limits.
func NewRunner(ops Ops) *Runner {
	ctx, stop := context.WithCancel(context.Background())
	r := &Runner{ops: ops, sem: map[string]chan struct{}{}, results: make(chan Result, maxQueued), ctx: ctx, stop: stop, inflight: map[string]int{}}
	for k, n := range lanes {
		r.sem[k] = make(chan struct{}, n)
	}
	return r
}

// Results is where finished ops land.
func (r *Runner) Results() <-chan Result { return r.results }

// InFlight is how many ops of kind are running now.
func (r *Runner) InFlight(kind string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.inflight[kind]
}

// Submit queues in. It is false when the runner is closed or full; the caller decides
// what that means (the World treats a refused op as a failed one).
func (r *Runner) Submit(in Intent, name string) bool {
	sem, ok := r.sem[in.Kind]
	if !ok {
		return false
	}
	r.mu.Lock()
	if r.closed || r.queued >= maxQueued {
		r.mu.Unlock()
		return false
	}
	r.queued++
	r.wg.Add(1)
	r.mu.Unlock()
	go func() {
		defer r.wg.Done()
		res := Result{Kind: in.Kind, ID: in.ID, Session: in.Session}
		select {
		case sem <- struct{}{}:
		case <-r.ctx.Done():
			res.Err = "stopped"
			r.done(res)
			return
		}
		r.mu.Lock()
		r.inflight[in.Kind]++
		r.mu.Unlock()
		res = r.run(in, name)
		r.mu.Lock()
		r.inflight[in.Kind]--
		r.mu.Unlock()
		<-sem
		r.done(res)
	}()
	return true
}

func (r *Runner) done(res Result) {
	r.mu.Lock()
	r.queued--
	r.mu.Unlock()
	select {
	case r.results <- res:
	default: // results is as big as the queue; unreachable unless the loop stopped
	}
}

// run runs one op, turning a panic into a failed Result.
func (r *Runner) run(in Intent, name string) (res Result) {
	res = Result{Kind: in.Kind, ID: in.ID, Session: in.Session}
	defer func() {
		if p := recover(); p != nil {
			slog.Error("cluster defense: op panicked", "kind", in.Kind, "panic", p)
			res.OK, res.Err = false, fmt.Sprint("panic: ", p)
		}
	}()
	ctx, cancel := context.WithTimeout(r.ctx, opTimeout)
	defer cancel()
	start := time.Now()
	var err error
	switch in.Kind {
	case "spawn":
		res.Session, err = r.ops.Spawn(ctx, name)
	case "task":
		res.Say, res.Tokens, err = r.ops.Task(ctx, in.Session, in.Role)
	case "suspend":
		res.Ms, err = r.ops.Suspend(ctx, in.Session)
	case "delete":
		err = r.ops.Delete(ctx, in.Session)
	}
	if res.Ms == 0 {
		res.Ms = max(time.Since(start).Milliseconds(), 1)
	}
	if err != nil {
		res.Err = err.Error()
		return res
	}
	res.OK = true
	return res
}

// Close stops new ops and cancels running ones, then waits for them (bounded by ctx).
func (r *Runner) Close(ctx context.Context) {
	r.mu.Lock()
	r.closed = true
	r.mu.Unlock()
	r.stop()
	done := make(chan struct{})
	go func() { r.wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-ctx.Done():
	}
}
