package heist

import (
	"context"
	"errors"
	"fmt"
	"hash/fnv"
	"log/slog"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/sebbycorp/kubecon-adventures/arcade/internal/kagent"
)

// agentAPI is the part of *kagent.Kagent the adapter uses (a fake in unit tests).
type agentAPI interface {
	CreateSession(ctx context.Context, name string) (string, error)
	Prompt(ctx context.Context, sessionID, text string) (kagent.Reply, error)
	Suspend(ctx context.Context, sessionID string) error
	Resume(ctx context.Context, sessionID string) error
	Checkpoint(ctx context.Context, sessionID, headTaskID string) (string, error)
	Delete(ctx context.Context, sessionID string) error
	ActorStates(ctx context.Context) (map[string]kagent.Actor, error)
	ListSessionIDs(ctx context.Context) ([]string, error)
	DeleteCheckpoint(ctx context.Context, checkpointID string) error
	ListCheckpointIDs(ctx context.Context, sessionID string) ([]string, error)
}

// heistJob is one fixed heist: four bash steps, all under /data/workspace.
type heistJob struct {
	name  string
	steps []string
}

// jobs is the only source of step prompts: no player text ever reaches the agent.
var jobs = []heistJob{
	{"crack the safe", []string{
		"Heist step 1 of 4 (crack the safe). Run this bash command: mkdir -p /data/workspace/safe && for i in 1 2 3 4 5; do echo \"echo gold bar $i\"; done > /data/workspace/safe/safe.sh. Set loot to 0." + replyJSON,
		"Heist step 2 of 4 (crack the safe). Run this bash command: bash /data/workspace/safe/safe.sh > /data/workspace/safe/bag.txt && cat /data/workspace/safe/bag.txt. Set loot to 0." + replyJSON,
		"Heist step 3 of 4 (crack the safe). Run this bash command: wc -l < /data/workspace/safe/bag.txt. Set loot to the number it printed." + replyJSON,
		"Heist step 4 of 4 (crack the safe). Run this bash command: sha256sum /data/workspace/safe/bag.txt. Set loot to 0." + replyJSON,
	}},
	{"rob the bank vault", []string{
		"Heist step 1 of 4 (rob the bank vault). Run this bash command: mkdir -p /data/workspace/vault && for i in 1 2 3 4 5 6; do echo \"coin $i\" > /data/workspace/vault/coin$i.txt; done && ls /data/workspace/vault. Set loot to 0." + replyJSON,
		"Heist step 2 of 4 (rob the bank vault). Run this bash command: cat /data/workspace/vault/coin*.txt > /data/workspace/vault/bag.txt && cat /data/workspace/vault/bag.txt. Set loot to 0." + replyJSON,
		"Heist step 3 of 4 (rob the bank vault). Run this bash command: wc -l < /data/workspace/vault/bag.txt. Set loot to the number it printed." + replyJSON,
		"Heist step 4 of 4 (rob the bank vault). Run this bash command: sha256sum /data/workspace/vault/bag.txt. Set loot to 0." + replyJSON,
	}},
	{"lift the museum diamond", []string{
		"Heist step 1 of 4 (lift the museum diamond). Run this bash command: mkdir -p /data/workspace/museum && seq 1 7 | sed 's/^/diamond /' > /data/workspace/museum/case.txt && cat /data/workspace/museum/case.txt. Set loot to 0." + replyJSON,
		"Heist step 2 of 4 (lift the museum diamond). Run this bash command: grep diamond /data/workspace/museum/case.txt > /data/workspace/museum/bag.txt && ls -l /data/workspace/museum. Set loot to 0." + replyJSON,
		"Heist step 3 of 4 (lift the museum diamond). Run this bash command: wc -l < /data/workspace/museum/bag.txt. Set loot to the number it printed." + replyJSON,
		"Heist step 4 of 4 (lift the museum diamond). Run this bash command: sha256sum /data/workspace/museum/bag.txt. Set loot to 0." + replyJSON,
	}},
}

// replyJSON ends every step prompt: after seeing command output the model otherwise
// tends to answer in prose, which kagent's output validation rejects.
const replyJSON = " Then reply with only the JSON object (say, step_done, loot), no other text."

const (
	sprungLine   = "You were frozen by the cops and just got sprung. React, then continue your heist."
	allyGreeting = "You just booted up to protect your crew. Greet them in one line."
	allySprung   = "You were frozen protecting your crew and just got rebooted. React in one line."
	// bookedLine is Extract's post-resume prompt: it gives the checkpoint a fresh head
	// (a suspend changes the snapshot, so the pre-freeze head can't be tagged).
	bookedLine = "You just got booked into the Snapshot Vault. React in one line." + replyJSON
	// bookedFallback is Extract's line when the booked reply can't be parsed (the turn
	// itself completed, so it is still the checkpoint's head).
	bookedFallback = "Booked. I'll be back."
	// Spring's line when the post-resume prompt fails (the resume itself worked).
	sprungFallback = "I'm out! Back to the heist."
	// allyGreetFallback is the greeting when the ally's first turn fails validation.
	allyGreetFallback = "Ally online. I've got your back!"
	allyFallback      = "Rebooted and back on duty!"
	maxSay            = 60
	stepsPerJob       = 4
)

// stepQuips are a step's line when heister's turn fails kagent's output validation:
// its own words are never shown (they may echo command output).
var stepQuips = []string{
	"Shh... working on it.",
	"Nothing to see here, officer!",
	"Almost got it, keep watch!",
	"Smooth as silk. Next!",
	"Easy money, easy does it.",
}

// stepQuip picks a fallback line by session and step.
func stepQuip(sid string, step int) string {
	h := fnv.New32a()
	_, _ = h.Write([]byte(sid))
	return stepQuips[(h.Sum32()+uint32(max(step, 0)))%uint32(len(stepQuips))]
}

// jobFor picks a session's job from its session ID, so a sprung session (same ID)
// continues the same job.
func jobFor(sessionID string) string {
	h := fnv.New32a()
	_, _ = h.Write([]byte(sessionID))
	return jobs[h.Sum32()%uint32(len(jobs))].name
}

// stepPrompt is step (1-based) of steps for job, from the fixed table. An unknown job
// falls back to the first one; a game with a different step count is mapped onto the
// job's four steps.
func stepPrompt(job string, step, steps int) string {
	j := jobs[0]
	for _, c := range jobs {
		if c.name == job {
			j = c
		}
	}
	if steps <= 0 {
		steps = stepsPerJob
	}
	step = min(max(step, 1), steps)
	i := (step - 1) * len(j.steps) / steps
	if step == steps {
		i = len(j.steps) - 1
	}
	return j.steps[i]
}

// springPrompt is the post-resume line followed by the next step to run.
func springPrompt(job string, next, steps int) string {
	return sprungLine + " " + stepPrompt(job, next, steps)
}

// parseStep decodes heister's structured output. say is clipped here because kagent's
// outputSchema can't carry maxLength.
func parseStep(r kagent.Reply) (StepResult, error) {
	var out struct {
		Say      string `json:"say"`
		StepDone bool   `json:"step_done"`
		Loot     int    `json:"loot"`
	}
	if err := r.Decode(&out); err != nil {
		return StepResult{}, fmt.Errorf("bad agent reply: %w", err)
	}
	return StepResult{Say: clipRunes(out.Say, maxSay), Done: out.StepDone, Loot: max(out.Loot, 0),
		Tokens: r.Usage.Input + r.Usage.Output}, nil
}

func clipRunes(s string, n int) string {
	s = strings.TrimSpace(s)
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n])
}

// settling reports errors that mean "wait and retry": kagent's short "not yet settled"
// window after a turn or a resume (FailedPrecondition), and a session that still has an
// A2A turn or checkpoint in flight (Aborted, "session has an active task or checkpoint:
// resource conflict", which Suspend answers mid-turn). Retrying the latter is what makes
// a freeze land when the turn ends.
func settling(err error) bool {
	if err == nil {
		return false
	}
	if status.Code(err) == codes.FailedPrecondition {
		return true
	}
	// Only this Aborted means "a turn is in flight": other Aborted conflicts return at
	// once rather than spinning for the op's 45 s while the target stays Busy.
	e := err.Error()
	return strings.Contains(e, "not yet settled") || strings.Contains(e, "active task or checkpoint")
}

// settle runs call, retrying with backoff (100 ms up to 500 ms, so a freeze lands soon
// after the turn ends) while kagent is still settling, until ctx ends.
func settle(ctx context.Context, call func() error) error {
	wait := 100 * time.Millisecond
	for {
		err := call()
		if !settling(err) {
			return err
		}
		select {
		case <-ctx.Done():
			return err
		case <-time.After(wait):
		}
		wait = min(2*wait, 500*time.Millisecond)
	}
}

// policyErr marks errors where an agentgateway policy stopped the LLM call (403 prompt
// guard, 429 rate limit), the way Agent Flow's policyHit spots them.
func policyErr(err error) error {
	if err == nil {
		return nil
	}
	e := strings.ToLower(err.Error())
	if !strings.Contains(e, "llm error") && !strings.Contains(e, "api error") {
		return err
	}
	switch {
	case strings.Contains(e, "403"):
		return fmt.Errorf("policy: blocked by agentgateway's prompt guard: %w", err)
	case strings.Contains(e, "429"):
		return fmt.Errorf("policy: rate limited by agentgateway: %w", err)
	}
	return err
}

// session is what the adapter remembers about one live session.
type session struct {
	ally     bool
	player   bool // a player's own agent (siegeplayer)
	lastTask string // A2A task ID of the last prompt reply (Checkpoint's head)
	// staleHead: a suspend since lastTask changed the snapshot, so lastTask can't be
	// checkpointed any more ("SnapshotTagFailed: snapshot changed"); a new turn fixes it.
	staleHead bool
	lastStep  int // last step that completed
	steps     int
	suspended bool
}

// agentOps is the kagent-backed Ops.
type agentOps struct {
	heister, sidekick agentAPI
	player            agentAPI // players' own agents; sidekick when unset
	resumeFirst       bool

	mu       sync.Mutex
	sessions map[string]*session
}

// NewKagentOps makes the real Ops: heist targets run on heister, allies on sidekick and
// each player's own agent on player. extract is "resume-first" (default: Resume,
// Checkpoint, Delete) or "direct".
func NewKagentOps(heister, sidekick, player *kagent.Kagent, extract string) Ops {
	o := newAgentOps(heister, sidekick, extract)
	o.player = player
	return o
}

func newAgentOps(heister, sidekick agentAPI, extract string) *agentOps {
	return &agentOps{heister: heister, sidekick: sidekick, resumeFirst: extract != "direct", sessions: map[string]*session{}}
}

// ExtractUsesResume tells the game's Scheduler whether "extract" needs a resume slot.
func (o *agentOps) ExtractUsesResume() bool { return o.resumeFirst }

// sess returns (creating if needed) the record for sid. o.mu must be held.
func (o *agentOps) sessLocked(sid string) *session {
	s, ok := o.sessions[sid]
	if !ok {
		s = &session{steps: stepsPerJob}
		o.sessions[sid] = s
	}
	return s
}

// client is the agent a session belongs to.
func (o *agentOps) client(sid string) agentAPI {
	o.mu.Lock()
	defer o.mu.Unlock()
	if s := o.sessions[sid]; s != nil && s.player {
		return o.playerAPI()
	} else if s != nil && s.ally {
		return o.sidekick
	}
	return o.heister
}

// playerAPI is the players' agent: siegeplayer, or sidekick when it isn't configured.
func (o *agentOps) playerAPI() agentAPI {
	if o.player != nil {
		return o.player
	}
	return o.sidekick
}

// validationFailed reports kagent's output_validation_failed: heister ended its turn
// without valid JSON (often after looping on extra tool calls). The turn itself ran, so
// it is not resent: callers use a fixed fallback line instead.
func validationFailed(err error) bool {
	return err != nil && strings.Contains(err.Error(), "output_validation_failed")
}

// prompt sends text once (retrying only the settle window) and remembers the reply's
// task ID as Checkpoint's head. A turn that failed output validation is a head too: it
// ran to the end and Substrate checkpoints it (probed on the cluster); any other failed
// turn never is.
func (o *agentOps) prompt(ctx context.Context, k agentAPI, sid, text string) (kagent.Reply, error) {
	var r kagent.Reply
	err := settle(ctx, func() (err error) {
		r, err = k.Prompt(ctx, sid, text)
		return err
	})
	if (err == nil || validationFailed(err)) && r.Task != nil && r.Task.ID != "" {
		o.mu.Lock()
		s := o.sessLocked(sid)
		// A turn that ended just before a Freeze landed (Freeze waits for it) is
		// already stale: keep staleHead while the session is suspended.
		s.lastTask, s.staleHead = string(r.Task.ID), s.suspended
		o.mu.Unlock()
	}
	if validationFailed(err) {
		// Never log the reply itself: it may echo command output.
		slog.Warn("heist: agent reply failed output validation, using a fallback line", "session", sid)
	}
	return r, policyErr(err)
}

// node is the session actor's node, or "" if Substrate doesn't say.
func node(ctx context.Context, k agentAPI, sid string) string {
	actors, err := k.ActorStates(ctx)
	if err != nil {
		return ""
	}
	return actors[sid].Node
}

func (o *agentOps) Spawn(ctx context.Context, name string) (string, string, error) {
	sid, err := o.heister.CreateSession(ctx, "gta-"+name)
	if err != nil {
		return "", "", err
	}
	o.mu.Lock()
	o.sessLocked(sid)
	o.mu.Unlock()
	// Nothing after CreateSession can fail here (an unknown node is just ""), so the
	// session always reaches the game, which owns its deletion.
	return sid, node(ctx, o.heister, sid), nil
}

func (o *agentOps) SpawnAlly(ctx context.Context, name string) (string, string, string, error) {
	sid, err := o.sidekick.CreateSession(ctx, "gta-"+name)
	if err != nil {
		return "", "", "", err
	}
	o.mu.Lock()
	o.sessLocked(sid).ally = true
	o.mu.Unlock()
	say, err := func() (string, error) {
		r, err := o.prompt(ctx, o.sidekick, sid, allyGreeting)
		if validationFailed(err) {
			return allyGreetFallback, nil
		}
		if err != nil {
			return "", err
		}
		return parseSay(r)
	}()
	if err != nil {
		// The game never learns this session's ID, so nothing else would delete it.
		o.discard(sid)
		return "", "", "", err
	}
	return sid, node(ctx, o.sidekick, sid), say, nil
}

// playerGreeting is a player agent's first turn: it is what places the actor on a worker.
const (
	playerGreeting = "A player just joined Substrate Siege and you are their agent. Say hi in one short line."
	playerFallback = "Player agent online!"
)

// SpawnPlayer starts a player's own agent and runs its greeting turn.
func (o *agentOps) SpawnPlayer(ctx context.Context, name string) (string, string, string, error) {
	k := o.playerAPI()
	sid, err := k.CreateSession(ctx, "gta-"+name)
	if err != nil {
		return "", "", "", err
	}
	o.mu.Lock()
	o.sessLocked(sid).player = true
	o.mu.Unlock()
	say, err := func() (string, error) {
		r, err := o.prompt(ctx, k, sid, playerGreeting)
		if validationFailed(err) {
			return playerFallback, nil
		}
		if err != nil {
			return "", err
		}
		return parseSay(r)
	}()
	if err != nil {
		o.discard(sid) // the game never learns this session's ID
		return "", "", "", err
	}
	return sid, node(ctx, k, sid), say, nil
}

// Wake resumes a suspended session without prompting it; ms is the resume alone.
func (o *agentOps) Wake(ctx context.Context, sid string) (int64, error) {
	k := o.client(sid)
	var ms int64
	if err := settle(ctx, func() error {
		start := time.Now()
		err := k.Resume(ctx, sid)
		ms = time.Since(start).Milliseconds()
		return err
	}); err != nil {
		return 0, err
	}
	o.mu.Lock()
	o.sessLocked(sid).suspended = false
	o.mu.Unlock()
	return max(ms, 1), nil
}

// discard deletes a session the game will never hear about, on a fresh context (the op's
// may be what failed), and forgets it.
func (o *agentOps) discard(sid string) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := o.Delete(ctx, sid); err != nil {
		slog.Warn("heist: delete of failed ally session", "session", sid, "err", err)
		o.mu.Lock()
		delete(o.sessions, sid)
		o.mu.Unlock()
	}
}

// parseSay decodes sidekick's {say} output.
func parseSay(r kagent.Reply) (string, error) {
	var out struct {
		Say string `json:"say"`
	}
	if err := r.Decode(&out); err != nil {
		return "", fmt.Errorf("bad agent reply: %w", err)
	}
	return clipRunes(out.Say, maxSay), nil
}

func (o *agentOps) Step(ctx context.Context, sid string, step, steps int) (StepResult, error) {
	r, err := o.prompt(ctx, o.heister, sid, stepPrompt(jobFor(sid), step, steps))
	var res StepResult
	switch {
	case validationFailed(err):
		// The step's turn ran; only its reply was unreadable. Count it, with a quip.
		res = StepResult{Say: stepQuip(sid, step), Done: true, Tokens: r.Usage.Input + r.Usage.Output}
	case err != nil:
		return StepResult{}, err
	default:
		if res, err = parseStep(r); err != nil {
			return res, err
		}
	}
	o.mu.Lock()
	s := o.sessLocked(sid)
	s.lastStep, s.steps = step, steps
	o.mu.Unlock()
	// A new session gets its node once its first turn places it, so report it per step.
	res.Node = node(ctx, o.heister, sid)
	return res, nil
}

// Freeze suspends the session. Mid-turn kagent refuses (Aborted, "active task"), so it
// waits, within ctx, for the turn to end: the freeze lands when the turn settles.
func (o *agentOps) Freeze(ctx context.Context, sid string) (int64, error) {
	k := o.client(sid)
	// ms is the Suspend that landed, not the wait for an in-flight turn to end (the
	// screen shows it as the suspend time).
	var ms int64
	if err := settle(ctx, func() error {
		start := time.Now()
		err := k.Suspend(ctx, sid)
		ms = time.Since(start).Milliseconds()
		return err
	}); err != nil {
		return 0, err
	}
	o.mu.Lock()
	s := o.sessLocked(sid)
	s.suspended, s.staleHead = true, true
	o.mu.Unlock()
	return max(ms, 1), nil
}

// Spring resumes the session (ms is the resume alone) and prompts it once: a target
// reacts and continues its next step, an ally reacts.
func (o *agentOps) Spring(ctx context.Context, sid string) (string, string, int64, error) {
	k := o.client(sid)
	o.mu.Lock()
	suspended := o.sessLocked(sid).suspended
	o.mu.Unlock()
	start := time.Now()
	if suspended { // awake already (an Extract that failed after its resume): just prompt
		if err := k.Resume(ctx, sid); err != nil {
			return "", "", 0, err
		}
	}
	ms := max(time.Since(start).Milliseconds(), 1)
	o.mu.Lock()
	s := o.sessLocked(sid)
	s.suspended = false
	ally, next, steps := s.ally, min(s.lastStep+1, s.steps), s.steps
	o.mu.Unlock()

	nd := node(ctx, k, sid)
	text, fallback := springPrompt(jobFor(sid), next, steps), sprungFallback
	if ally {
		text, fallback = allySprung, allyFallback
	}
	r, err := o.prompt(ctx, k, sid, text)
	var say string
	if err == nil {
		if ally {
			say, err = parseSay(r)
		} else {
			var res StepResult
			res, err = parseStep(r)
			say = res.Say
		}
	}
	switch {
	case err != nil && strings.HasPrefix(err.Error(), "policy:"):
		return nd, "", ms, err
	case err != nil || say == "":
		// The session is awake again either way; only its line failed.
		slog.Warn("heist: spring line failed, using fallback", "session", sid, "ally", ally, "err", err)
		say = fallback
	}
	return nd, say, ms, nil
}

// Extract checkpoints the session's last turn, then deletes it, and returns the
// session's booked line ("" when there is none) and the checkpoint's ID ("" when the
// session never had a turn to checkpoint). The checkpoint outlives the session: the game
// keeps its ID and deletes it at the admin reset. kagent can't checkpoint a SUSPENDED
// session, and once a session has been suspended the pre-freeze head can't be tagged
// either ("SnapshotTagFailed: snapshot changed", for good). So in resume-first mode a
// suspended session is resumed and prompted once (bookedLine); that turn is the fresh
// head that is checkpointed. If that prompt fails the session stays awake and in the
// game, and its head stays stale, so the next Extract prompts again. ms is the Substrate work (resume, checkpoint, delete),
// without the LLM turn.
func (o *agentOps) Extract(ctx context.Context, sid string) (string, string, int64, error) {
	k := o.client(sid)
	o.mu.Lock()
	s := o.sessLocked(sid)
	suspended, stale, head := s.suspended, s.staleHead, s.lastTask
	o.mu.Unlock()
	var say string
	var ms int64
	timed := func(call func() error) error {
		start := time.Now()
		err := call()
		ms += time.Since(start).Milliseconds()
		return err
	}
	if suspended {
		if !o.resumeFirst {
			return "", "", 0, errors.New("direct extract of a suspended session: kagent can't checkpoint it")
		}
		if err := timed(func() error { return k.Resume(ctx, sid) }); err != nil {
			return "", "", 0, err
		}
		o.mu.Lock()
		s.suspended = false
		o.mu.Unlock()
	}
	// fail returns err; when this Extract resumed the session, it is frozen again first,
	// so Substrate matches the game, which puts the target back to Frozen.
	fail := func(err error) (string, string, int64, error) {
		if suspended {
			o.refreeze(ctx, sid)
		}
		return "", "", 0, err
	}
	// Also when awake: a session resumed earlier whose post-resume prompt failed (a
	// spring's line, or a failed booked line) still has the stale pre-freeze head.
	if suspended || stale {
		r, err := o.prompt(ctx, k, sid, bookedLine)
		if validationFailed(err) {
			err = nil // the turn ran and is checkpointable; its line falls back below
			r.JSON, r.Text = "", ""
		}
		if err != nil {
			return fail(fmt.Errorf("booked line: %w", err))
		}
		if r.Task == nil || r.Task.ID == "" {
			return fail(errors.New("booked line: reply has no task to checkpoint"))
		}
		if say, err = parseSay(r); err != nil || say == "" {
			slog.Warn("heist: booked line unreadable, using fallback", "session", sid, "err", err)
			say = bookedFallback
		}
		head = string(r.Task.ID)
	}
	var cp string
	if head != "" {
		if err := timed(func() error {
			return settle(ctx, func() (err error) { cp, err = k.Checkpoint(ctx, sid, head); return err })
		}); err != nil {
			return fail(fmt.Errorf("checkpoint: %w", err))
		}
	}
	if err := timed(func() error { return o.Delete(ctx, sid) }); err != nil {
		return fail(err)
	}
	return say, cp, max(ms, 1), nil
}

// DeleteCheckpoint deletes a booked target's checkpoint. Checkpoints belong to the
// arcade's user, not to an agent, so the heister client serves them all.
func (o *agentOps) DeleteCheckpoint(ctx context.Context, id string) error {
	return o.heister.DeleteCheckpoint(ctx, id)
}

// ListCheckpoints lists a session's checkpoints (kagent lists them only per session; a
// deleted session's checkpoints are still listed).
func (o *agentOps) ListCheckpoints(ctx context.Context, sid string) ([]string, error) {
	return o.heister.ListCheckpointIDs(ctx, sid)
}

// refreeze suspends a session an Extract resumed but could not book (its ctx may be the
// thing that ran out, so this gets its own). It leaves staleHead set: the next Extract
// resumes and prompts again.
func (o *agentOps) refreeze(ctx context.Context, sid string) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()
	if _, err := o.Freeze(ctx, sid); err != nil {
		slog.Warn("heist: re-suspend after a failed extract", "session", sid, "err", err)
	}
}

// ListSessions lists the sessions of both agents (heister, then sidekick). If one list
// fails, the other's sessions still come back, with the error.
func (o *agentOps) ListSessions(ctx context.Context) ([]string, error) {
	var ids []string
	var errs []error
	ks := []agentAPI{o.heister, o.sidekick}
	if o.player != nil {
		ks = append(ks, o.player)
	}
	for _, k := range ks {
		got, err := k.ListSessionIDs(ctx)
		ids = append(ids, got...)
		errs = append(errs, err)
	}
	return ids, errors.Join(errs...)
}

// Delete removes the session and forgets it.
func (o *agentOps) Delete(ctx context.Context, sid string) error {
	k := o.client(sid)
	if err := settle(ctx, func() error { return k.Delete(ctx, sid) }); err != nil {
		return err
	}
	o.mu.Lock()
	delete(o.sessions, sid)
	o.mu.Unlock()
	return nil
}
