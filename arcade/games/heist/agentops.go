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
	// Spring's line when the post-resume prompt fails (the resume itself worked).
	sprungFallback = "I'm out! Back to the heist."
	allyFallback   = "Rebooted and back on duty!"
	// invalidRetries is how many times a prompt is resent when the model's reply fails
	// kagent's output validation (gpt-4.1-mini sometimes answers in prose).
	invalidRetries = 2
	maxSay         = 60
	stepsPerJob    = 4
)

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

// settling reports kagent's short "not yet settled" window after a turn or a resume.
func settling(err error) bool {
	return err != nil && (status.Code(err) == codes.FailedPrecondition || strings.Contains(err.Error(), "not yet settled"))
}

// settle runs call, retrying with backoff while kagent is still settling, until ctx ends.
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
		wait = min(2*wait, 2*time.Second)
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
	ally      bool
	lastTask  string // A2A task ID of the last prompt reply (Checkpoint's head)
	lastStep  int    // last step that completed
	steps     int
	suspended bool
}

// agentOps is the kagent-backed Ops.
type agentOps struct {
	heister, sidekick agentAPI
	resumeFirst       bool

	mu       sync.Mutex
	sessions map[string]*session
}

// NewKagentOps makes the real Ops: heist targets run on heister, allies on sidekick.
// extract is "resume-first" (default: Resume, Checkpoint, Delete) or "direct".
func NewKagentOps(heister, sidekick *kagent.Kagent, extract string) Ops {
	return newAgentOps(heister, sidekick, extract)
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
	if s := o.sessions[sid]; s != nil && s.ally {
		return o.sidekick
	}
	return o.heister
}

// prompt sends text (retrying the settle window, and up to invalidRetries times when the
// reply fails output validation) and remembers the reply's task ID.
func (o *agentOps) prompt(ctx context.Context, k agentAPI, sid, text string) (kagent.Reply, error) {
	var r kagent.Reply
	var err error
	for try := 0; try <= invalidRetries; try++ {
		err = settle(ctx, func() (err error) {
			r, err = k.Prompt(ctx, sid, text)
			return err
		})
		if err == nil && r.Task != nil && r.Task.ID != "" { // a failed task never becomes Checkpoint's head
			o.mu.Lock()
			o.sessLocked(sid).lastTask = string(r.Task.ID)
			o.mu.Unlock()
		}
		if err == nil || !strings.Contains(err.Error(), "output_validation_failed") || ctx.Err() != nil {
			break
		}
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
	if err != nil {
		return StepResult{}, err
	}
	res, err := parseStep(r)
	if err != nil {
		return res, err
	}
	o.mu.Lock()
	s := o.sessLocked(sid)
	s.lastStep, s.steps = step, steps
	o.mu.Unlock()
	// A new session gets its node once its first turn places it, so report it per step.
	res.Node = node(ctx, o.heister, sid)
	return res, nil
}

func (o *agentOps) Freeze(ctx context.Context, sid string) (int64, error) {
	k := o.client(sid)
	start := time.Now()
	if err := settle(ctx, func() error { return k.Suspend(ctx, sid) }); err != nil {
		return 0, err
	}
	ms := time.Since(start).Milliseconds()
	o.mu.Lock()
	o.sessLocked(sid).suspended = true
	o.mu.Unlock()
	return max(ms, 1), nil
}

// Spring resumes the session (ms is the resume alone) and prompts it once: a target
// reacts and continues its next step, an ally reacts.
func (o *agentOps) Spring(ctx context.Context, sid string) (string, string, int64, error) {
	k := o.client(sid)
	start := time.Now()
	if err := k.Resume(ctx, sid); err != nil {
		return "", "", 0, err
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

// Extract checkpoints the session's last turn, then deletes it. In resume-first mode a
// suspended session is resumed first: kagent can't checkpoint a SUSPENDED session.
func (o *agentOps) Extract(ctx context.Context, sid string) (int64, error) {
	k := o.client(sid)
	start := time.Now()
	o.mu.Lock()
	s := o.sessLocked(sid)
	suspended, head := s.suspended, s.lastTask
	o.mu.Unlock()
	if head != "" {
		if suspended {
			if !o.resumeFirst {
				return 0, errors.New("direct extract of a suspended session: kagent can't checkpoint it")
			}
			if err := k.Resume(ctx, sid); err != nil {
				return 0, err
			}
			o.mu.Lock()
			s.suspended = false
			o.mu.Unlock()
		}
		if err := settle(ctx, func() error { _, err := k.Checkpoint(ctx, sid, head); return err }); err != nil {
			return 0, fmt.Errorf("checkpoint: %w", err)
		}
	}
	if err := o.Delete(ctx, sid); err != nil {
		return 0, err
	}
	return max(time.Since(start).Milliseconds(), 1), nil
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
