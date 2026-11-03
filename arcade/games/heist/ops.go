package heist

import (
	"context"
	"sync"
	"time"
)

// opTimeout bounds every agent op so a hung call still produces a failure Result.
const opTimeout = 45 * time.Second

// Ops is the agent backend (kagent/Substrate). Every call must honor ctx.
type Ops interface {
	Spawn(ctx context.Context, name string) (sessionID, node string, err error)
	Step(ctx context.Context, sessionID string, step, steps int) (StepResult, error)
	Freeze(ctx context.Context, sessionID string) (ms int64, err error)
	Spring(ctx context.Context, sessionID string) (node string, say string, ms int64, err error)
	Extract(ctx context.Context, sessionID string) (ms int64, err error)
	Delete(ctx context.Context, sessionID string) error
	// SpawnAlly starts a player's ally session (the sidekick Agent) and returns its
	// greeting line.
	SpawnAlly(ctx context.Context, name string) (sessionID, node, say string, err error)
}

// StepResult is what one agent step reports.
type StepResult struct {
	Say    string
	Done   bool
	Loot   int
	Node   string
	Tokens int64
}

// Result is the outcome of one submitted op. Spawned holds the new session ID for
// "spawn" and "ally"; Node and Step.Say carry the node and the sprung line for "spring"
// or the greeting for "ally".
type Result struct {
	Kind, Target string
	OK           bool
	Ms           int64
	Step         StepResult
	Spawned      string
	Node         string
	Err          string
}

type slotClass int

const (
	classNone slotClass = iota
	classStep
	classResume
)

type job struct {
	kind, target, sessionID string
	step, steps             int
	class                   slotClass
}

// Scheduler runs Ops asynchronously under concurrency caps so the game loop never
// blocks. Steps and resumes (springs, and extracts when ExtractUsesResume) are capped
// with a FIFO queue per class; freeze, delete, spawn and ally are never capped. Each
// target has two lanes, "step" (kind step) and "control" (spawn, ally, freeze, spring,
// extract, delete), and at most one op per lane per target is accepted at a time, so a freeze
// can land while a long heist step is still running.
//
// Every accepted op that starts running yields exactly one Result (OK=false and Err
// set on error or timeout). A finished op frees its slot and its lane before its
// result is sent, so a slow reader of Results never stalls the caps; instead the
// finished op's goroutine waits to hand the result over. Nothing is dropped while the
// scheduler is open, and the wait ends when Close is called.
type Scheduler struct {
	// ExtractUsesResume makes "extract" take a resume slot (resume-first extraction).
	ExtractUsesResume bool

	// onSubmit, if set, is told the kind and target of every accepted op (after s.mu
	// is released). The Game uses it to keep its record of submitted ops.
	onSubmit func(kind, target string)

	ops     Ops
	timeout time.Duration
	results chan Result
	ctx     context.Context
	cancel  context.CancelFunc
	wg      sync.WaitGroup

	mu      sync.Mutex
	closed  bool
	busy    map[lane]bool
	running [3]int // by slotClass
	limit   [3]int
	queue   [3][]job
}

// NewScheduler caps concurrent steps at maxSteps and concurrent resumes at maxResumes.
func NewScheduler(ops Ops, maxSteps, maxResumes int) *Scheduler {
	ctx, cancel := context.WithCancel(context.Background())
	s := &Scheduler{
		ops:     ops,
		timeout: opTimeout,
		results: make(chan Result, 256),
		ctx:     ctx,
		cancel:  cancel,
		busy:    map[lane]bool{},
	}
	s.limit[classStep] = max(maxSteps, 1)
	s.limit[classResume] = max(maxResumes, 1)
	return s
}

// Results delivers finished ops. It is never closed.
func (s *Scheduler) Results() <-chan Result { return s.results }

func (s *Scheduler) classOf(kind string) slotClass {
	switch kind {
	case "step":
		return classStep
	case "spring":
		return classResume
	case "extract":
		if s.ExtractUsesResume {
			return classResume
		}
	}
	return classNone
}

// lane is one target's step or control lane.
type lane struct {
	target string
	step   bool
}

func laneOf(kind, target string) lane { return lane{target: target, step: kind == "step"} }

// Submit queues an op. It returns false if target already has an op in flight in the
// same lane, the kind is unknown, or the scheduler is closed.
func (s *Scheduler) Submit(kind, target, sessionID string, step, steps int) bool {
	switch kind {
	case "spawn", "ally", "step", "freeze", "spring", "extract", "delete":
	default:
		return false
	}
	j := job{kind: kind, target: target, sessionID: sessionID, step: step, steps: steps, class: s.classOf(kind)}
	s.mu.Lock()
	l := laneOf(kind, target)
	if s.closed || s.busy[l] {
		s.mu.Unlock()
		return false
	}
	s.busy[l] = true
	if j.class != classNone && s.running[j.class] >= s.limit[j.class] {
		s.queue[j.class] = append(s.queue[j.class], j)
	} else {
		s.startLocked(j)
	}
	s.mu.Unlock()
	if s.onSubmit != nil {
		s.onSubmit(kind, target)
	}
	return true
}

// startLocked claims a slot and launches the worker. s.mu must be held.
func (s *Scheduler) startLocked(j job) {
	s.running[j.class]++
	s.wg.Add(1)
	go s.work(j)
}

func (s *Scheduler) work(j job) {
	defer s.wg.Done()
	ctx, cancel := context.WithTimeout(s.ctx, s.timeout)
	r := s.run(ctx, j)
	cancel()

	s.mu.Lock()
	delete(s.busy, laneOf(j.kind, j.target))
	s.running[j.class]--
	if !s.closed && len(s.queue[j.class]) > 0 && s.running[j.class] < s.limit[j.class] {
		next := s.queue[j.class][0]
		s.queue[j.class] = s.queue[j.class][1:]
		s.startLocked(next)
	}
	s.mu.Unlock()

	select {
	case s.results <- r:
	case <-s.ctx.Done():
	}
}

// run executes the op and builds its Result.
func (s *Scheduler) run(ctx context.Context, j job) Result {
	r := Result{Kind: j.kind, Target: j.target}
	start := time.Now()
	var ms int64
	var err error
	switch j.kind {
	case "spawn":
		r.Spawned, r.Node, err = s.ops.Spawn(ctx, j.target)
	case "ally":
		r.Spawned, r.Node, r.Step.Say, err = s.ops.SpawnAlly(ctx, j.target)
	case "step":
		r.Step, err = s.ops.Step(ctx, j.sessionID, j.step, j.steps)
		r.Node = r.Step.Node
	case "freeze":
		ms, err = s.ops.Freeze(ctx, j.sessionID)
	case "spring":
		r.Node, r.Step.Say, ms, err = s.ops.Spring(ctx, j.sessionID)
	case "extract":
		ms, err = s.ops.Extract(ctx, j.sessionID)
	case "delete":
		err = s.ops.Delete(ctx, j.sessionID)
	}
	if ms <= 0 {
		ms = time.Since(start).Milliseconds()
	}
	r.Ms = ms
	if err != nil {
		r.Err = err.Error()
		return r
	}
	r.OK = true
	return r
}

// InFlight reports running steps, running resumes, and ops waiting in queues.
func (s *Scheduler) InFlight() (steps, resumes, queued int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.running[classStep], s.running[classResume], len(s.queue[classStep]) + len(s.queue[classResume])
}

// Close cancels in-flight ops, discards queued ones (they produce no results), and
// returns once every worker goroutine has exited. Submit returns false afterwards.
func (s *Scheduler) Close() {
	s.mu.Lock()
	s.closed = true
	s.queue = [3][]job{}
	s.mu.Unlock()
	s.cancel()
	s.wg.Wait()
}
