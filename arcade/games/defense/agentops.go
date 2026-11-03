// This file is the kagent-backed Ops: every subagent is a session of the "subagent"
// Agent, an Agent Substrate actor on the "defense" WorkerPool.
package defense

import (
	"context"
	"fmt"
	"hash/fnv"
	"log/slog"
	"strings"
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
	Delete(ctx context.Context, sessionID string) error
	ListSessionIDs(ctx context.Context) ([]string, error)
}

// tasks is the only source of prompts: no player text ever reaches an agent.
var tasks = map[string][]string{
	"planner":    {"Plan the three steps to roll out a new login page.", "Break 'add dark mode' into small tickets.", "Order these jobs: build, test, deploy, announce."},
	"coder":      {"Write a one-line Go function name for adding two numbers.", "Name a variable that counts active users.", "Suggest a commit message for fixing a typo."},
	"tester":     {"Name one edge case for a function that divides two numbers.", "Pick a test name for checking an empty shopping cart.", "Say what you would assert for a login with a wrong password."},
	"researcher": {"Name one benefit of running agents in sandboxes.", "Give one fact about Kubernetes pods.", "Name one reason to snapshot an idle program."},
	"reviewer":   {"Review: a function called doStuff. One short note.", "Review: a 400-line pull request with no tests. One short note.", "Review: a log line that prints a password. One short note."},
	"deployer":   {"Announce a canary rollout to 10% of users.", "Report a deploy of version 1.4.2 to staging.", "Report a rollback of a bad release."},
	"summarizer": {"Summarize: the build passed and two tests were added.", "Summarize: three agents finished their tasks.", "Summarize a stand-up where everyone shipped one thing."},
	"scraper":    {"Report collecting five headlines from a news page.", "Report fetching a product price list.", "Report reading the KubeCon schedule."},
}

const taskReply = " Reply with only a JSON object with one key, \"say\": one cheerful line of at most 60 characters about the result. No other text, and use no tools."

// fallbacks are the line when the agent's turn ran but its reply failed kagent's output
// validation: its own words are never shown.
var fallbacks = map[string]string{
	"planner": "Plan drafted. Next!", "coder": "Code compiled. Ship it!", "tester": "Tests green. Probably.",
	"researcher": "Research filed.", "reviewer": "LGTM with one nit.", "deployer": "Deployed to staging!",
	"summarizer": "TL;DR: all good.", "scraper": "Pages scraped!",
}

const maxSay = 60

// taskFor picks a role's task by session, so a retry asks the same thing.
func taskFor(session, role string) string {
	list := tasks[role]
	if len(list) == 0 {
		list = tasks["planner"]
	}
	h := fnv.New32a()
	_, _ = h.Write([]byte(session))
	return fmt.Sprintf("You are a %s subagent. Your task: %s", role, list[h.Sum32()%uint32(len(list))]) + taskReply
}

func clipRunes(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n])
}

// settling reports errors that mean "wait and retry": kagent's "not yet settled" window
// after a turn (FailedPrecondition), and a session with a turn still in flight (Aborted,
// "active task or checkpoint"), which Suspend answers mid-turn: retrying it is what
// makes a suspend land when the turn ends.
func settling(err error) bool {
	if err == nil {
		return false
	}
	if status.Code(err) == codes.FailedPrecondition {
		return true
	}
	e := err.Error()
	return strings.Contains(e, "not yet settled") || strings.Contains(e, "active task or checkpoint")
}

// settle runs call, retrying with backoff (100 ms up to 500 ms) while kagent is still
// settling, until ctx ends.
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

// policyErr marks errors where an agentgateway policy stopped the LLM call.
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

func validationFailed(err error) bool {
	return err != nil && strings.Contains(err.Error(), "output_validation_failed")
}

type agentOps struct{ k agentAPI }

// NewKagentOps makes the real Ops on the subagent Agent's client.
func NewKagentOps(k *kagent.Kagent) Ops { return &agentOps{k: k} }

// SessionPrefix starts every session name the game makes.
const SessionPrefix = "cd-"

func (o *agentOps) Spawn(ctx context.Context, name string) (string, error) {
	return o.k.CreateSession(ctx, SessionPrefix+name)
}

func (o *agentOps) Task(ctx context.Context, sid, role string) (string, int64, error) {
	var r kagent.Reply
	err := settle(ctx, func() (err error) {
		r, err = o.k.Prompt(ctx, sid, taskFor(sid, role))
		return err
	})
	tokens := r.Usage.Input + r.Usage.Output
	if validationFailed(err) {
		// Never log the reply itself.
		slog.Warn("cluster defense: task reply failed output validation, using a fallback line", "session", sid)
		return fallbacks[role], tokens, nil
	}
	if err != nil {
		return "", tokens, policyErr(err)
	}
	var out struct {
		Say string `json:"say"`
	}
	if err := r.Decode(&out); err != nil || strings.TrimSpace(out.Say) == "" {
		return fallbacks[role], tokens, nil
	}
	return clipRunes(out.Say, maxSay), tokens, nil
}

func (o *agentOps) Suspend(ctx context.Context, sid string) (int64, error) {
	var ms int64
	err := settle(ctx, func() error {
		start := time.Now()
		err := o.k.Suspend(ctx, sid)
		ms = time.Since(start).Milliseconds()
		return err
	})
	return max(ms, 1), err
}

func (o *agentOps) Delete(ctx context.Context, sid string) error {
	return settle(ctx, func() error { return o.k.Delete(ctx, sid) })
}

func (o *agentOps) ListSessions(ctx context.Context) ([]string, error) {
	return o.k.ListSessionIDs(ctx)
}
