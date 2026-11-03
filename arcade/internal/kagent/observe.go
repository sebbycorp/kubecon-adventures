package kagent

import (
	"context"
	"sync/atomic"
	"time"
)

// Op is one call any game made to kagent and Agent Substrate: a session created, an
// agent turn (reported when it starts and when it ends), a suspend, resume, checkpoint
// or delete. The Agent Flow screen draws them all, so every game's work shows on it.
type Op struct {
	Agent   string `json:"agent"`
	Kind    string `json:"kind"`  // create, turn, suspend, resume, checkpoint, delete
	Phase   string `json:"phase"` // start (turns only) or end
	Session string `json:"session"`
	Ms      int64  `json:"ms,omitempty"`
	Tokens  int64  `json:"tokens,omitempty"`
	Tools   int    `json:"tools,omitempty"`
	Err     bool   `json:"err,omitempty"`
	At      int64  `json:"at"`
}

var observer atomic.Pointer[func(Op)]

// Observe sends every later Op from every Kagent client to f, which must not block.
func Observe(f func(Op)) { observer.Store(&f) }

func (k *Kagent) note(op Op) {
	f := observer.Load()
	if f == nil {
		return
	}
	op.Agent, op.At = k.agent.GetName(), time.Now().UnixMilli()
	(*f)(op)
}

// track reports kind starting on session (turns only) and returns the call that
// reports it ending, with its duration and error.
func (k *Kagent) track(kind, session string) func(err error, r *Reply) {
	t0 := time.Now()
	if kind == "turn" {
		k.note(Op{Kind: kind, Phase: "start", Session: session})
	}
	return func(err error, r *Reply) {
		op := Op{Kind: kind, Phase: "end", Session: session, Ms: time.Since(t0).Milliseconds(), Err: err != nil}
		if r != nil {
			op.Tokens, op.Tools = r.Usage.Input+r.Usage.Output, len(r.Tools)
		}
		k.note(op)
	}
}

// The calls games make, each reported to the Observe hook (observe.go).

// CreateSession starts a new actor for the agent and waits until it is ready.
func (k *Kagent) CreateSession(ctx context.Context, name string) (string, error) {
	t0 := time.Now()
	id, err := k.createSession(ctx, name)
	k.note(Op{Kind: "create", Phase: "end", Session: id, Ms: time.Since(t0).Milliseconds(), Err: err != nil})
	return id, err
}

// Prompt sends one message to a session and waits for the agent's reply.
func (k *Kagent) Prompt(ctx context.Context, sessionID, text string) (Reply, error) {
	done := k.track("turn", sessionID)
	r, err := k.prompt(ctx, sessionID, text)
	done(err, &r)
	return r, err
}

// PromptStream is Prompt over A2A streaming: on sees the agent start working, each
// LLM-chosen tool call, each tool result and the final text as they happen.
func (k *Kagent) PromptStream(ctx context.Context, sessionID, text string, on func(StreamEvent)) (Reply, error) {
	done := k.track("turn", sessionID)
	r, err := k.promptStream(ctx, sessionID, text, on)
	done(err, &r)
	return r, err
}

// Checkpoint saves the session at headTaskID as a named checkpoint and returns its ID.
func (k *Kagent) Checkpoint(ctx context.Context, sessionID, headTaskID string) (string, error) {
	done := k.track("checkpoint", sessionID)
	id, err := k.checkpoint(ctx, sessionID, headTaskID)
	done(err, nil)
	return id, err
}

// Suspend snapshots the session's actor and frees its sandbox.
func (k *Kagent) Suspend(ctx context.Context, sessionID string) error {
	done := k.track("suspend", sessionID)
	err := k.suspend(ctx, sessionID)
	done(err, nil)
	return err
}

// Resume restores the session's actor from its snapshot.
func (k *Kagent) Resume(ctx context.Context, sessionID string) error {
	done := k.track("resume", sessionID)
	err := k.resume(ctx, sessionID)
	done(err, nil)
	return err
}

// Delete deletes the session and its actor.
func (k *Kagent) Delete(ctx context.Context, sessionID string) error {
	done := k.track("delete", sessionID)
	err := k.delete(ctx, sessionID)
	done(err, nil)
	return err
}
