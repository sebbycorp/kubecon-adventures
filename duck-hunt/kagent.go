package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	a2atype "github.com/a2aproject/a2a-go/v2/a2a"
	kagenta2a "github.com/kagent-dev/kagent/go/api/a2a"
	kclient "github.com/kagent-dev/kagent/go/api/client"
	apiv1 "github.com/kagent-dev/kagent/go/api/gen/kagent/api/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

// Flight is the duck agent's structured output (see manifests/duck-agent.yaml).
type Flight struct {
	Pattern string `json:"pattern"`
	Speed   int    `json:"speed"`
	Quack   string `json:"quack"`
}

// Usage is the token usage kagent reports for one task.
type Usage struct {
	Input  int64 `json:"inputTokens"`
	Output int64 `json:"outputTokens"`
}

// Kagent wraps the kagent control-plane API for one agent.
type Kagent struct {
	api      *kclient.APIClientSet
	gateway  *kclient.GatewayClientSet
	sessions apiv1.SessionServiceClient
	system   apiv1.SystemServiceClient
	agent    *apiv1.ResourceReference
	userID   string
}

func NewKagent(url, userID, namespace, agent string) (*Kagent, error) {
	api, err := kclient.NewAPI(url, kclient.WithUserID(userID))
	if err != nil {
		return nil, err
	}
	gateway, err := kclient.NewGateway(url, kclient.WithUserID(userID))
	if err != nil {
		return nil, err
	}
	conn, err := api.Conn()
	if err != nil {
		return nil, err
	}
	return &Kagent{
		api:      api,
		gateway:  gateway,
		sessions: apiv1.NewSessionServiceClient(conn),
		system:   apiv1.NewSystemServiceClient(conn),
		agent:    &apiv1.ResourceReference{Namespace: namespace, Name: agent},
		userID:   userID,
	}, nil
}

// ctx adds the caller identity for raw gRPC calls (the typed client adds it itself).
func (k *Kagent) ctx(ctx context.Context) context.Context {
	return metadata.AppendToOutgoingContext(ctx, "x-user-id", k.userID)
}

// CreateSession starts a new actor for the agent and waits until it is ready.
func (k *Kagent) CreateSession(ctx context.Context, name string) (string, error) {
	request := &apiv1.CreateSessionRequest{
		Agent:     k.agent,
		RequestId: fmt.Sprintf("%s-%d", name, time.Now().UnixNano()), // same ID on retries
		Name:      name,
	}
	var r *apiv1.CreateSessionResponse
	err := retryBusy(ctx, func() (err error) {
		r, err = k.api.Session.CreateSession(ctx, request)
		return err
	})
	if err != nil {
		return "", err
	}
	id := r.GetSession().GetId()
	return id, k.waitState(ctx, id, apiv1.RuntimeState_RUNTIME_STATE_READY)
}

// Prompt sends one message to a session and returns the duck's flight.
func (k *Kagent) Prompt(ctx context.Context, sessionID, text string) (Flight, Usage, error) {
	var flight Flight
	var usage Usage
	client, err := k.gateway.A2A.ForSession(ctx, sessionID)
	if err != nil {
		return flight, usage, err
	}
	request := &a2atype.SendMessageRequest{Message: a2atype.NewMessage(a2atype.MessageRoleUser, a2atype.NewTextPart(text))}
	var result a2atype.SendMessageResult
	// kagent rejects sends briefly while a session is resuming; retry those.
	for attempt := 0; ; attempt++ {
		result, err = client.SendMessage(ctx, request)
		if result != nil || !errors.Is(err, a2atype.ErrUnsupportedOperation) || attempt == 30 {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	if err != nil {
		return flight, usage, err
	}
	task, ok := result.(*a2atype.Task)
	if !ok {
		return flight, usage, fmt.Errorf("unexpected A2A result %T", result)
	}
	usage = taskUsage(task)
	if task.Status.State != a2atype.TaskStateCompleted {
		return flight, usage, fmt.Errorf("task ended in %s", task.Status.State)
	}
	for _, artifact := range task.Artifacts {
		for _, part := range artifact.Parts {
			raw := ""
			if kagenta2a.IsStructuredOutputPart(part) {
				if raw, err = kagenta2a.StructuredOutputJSON(part); err != nil {
					return flight, usage, err
				}
			} else if t := strings.TrimSpace(part.Text()); strings.HasPrefix(t, "{") {
				raw = t
			}
			if raw != "" && json.Unmarshal([]byte(raw), &flight) == nil && flight.Pattern != "" {
				return flight.clean(), usage, nil
			}
		}
	}
	return flight, usage, errors.New("no flight in agent reply")
}

func taskUsage(task *a2atype.Task) Usage {
	var u Usage
	if m, ok := task.Metadata["https://kagent.dev/extensions/usage/v1"].(map[string]any); ok {
		if v, ok := m["inputTokens"].(float64); ok {
			u.Input = int64(v)
		}
		if v, ok := m["outputTokens"].(float64); ok {
			u.Output = int64(v)
		}
	}
	return u
}

func (f Flight) clean() Flight {
	switch f.Pattern {
	case "straight", "zigzag", "loop", "dive", "wobble":
	default:
		f.Pattern = "wobble"
	}
	f.Speed = min(max(f.Speed, 1), 10)
	if r := []rune(f.Quack); len(r) > 60 {
		f.Quack = string(r[:59]) + "…"
	}
	return f
}

// Suspend snapshots the session's actor and frees its sandbox.
func (k *Kagent) Suspend(ctx context.Context, sessionID string) error {
	if err := retryBusy(ctx, func() error {
		_, err := k.sessions.SuspendSession(k.ctx(ctx), &apiv1.SuspendSessionRequest{SessionId: sessionID})
		return err
	}); err != nil {
		return err
	}
	return k.waitState(ctx, sessionID, apiv1.RuntimeState_RUNTIME_STATE_SUSPENDED)
}

// Resume restores the session's actor from its snapshot.
func (k *Kagent) Resume(ctx context.Context, sessionID string) error {
	if err := retryBusy(ctx, func() error {
		_, err := k.sessions.ResumeSession(k.ctx(ctx), &apiv1.ResumeSessionRequest{SessionId: sessionID})
		return err
	}); err != nil {
		return err
	}
	return k.waitState(ctx, sessionID, apiv1.RuntimeState_RUNTIME_STATE_READY)
}

func (k *Kagent) Delete(ctx context.Context, sessionID string) error {
	_, err := k.api.Session.DeleteSession(ctx, &apiv1.DeleteSessionRequest{SessionId: sessionID})
	return err
}

// ListSessionIDs returns every session this backend created for the agent.
func (k *Kagent) ListSessionIDs(ctx context.Context) ([]string, error) {
	var ids []string
	page := &apiv1.PageRequest{}
	for {
		r, err := k.api.Session.ListSessions(ctx, &apiv1.ListSessionsRequest{Agent: k.agent, Page: page})
		if err != nil {
			return nil, err
		}
		for _, s := range r.GetSessions() {
			ids = append(ids, s.GetId())
		}
		next := r.GetPage().GetNextPageToken()
		if next == "" {
			return ids, nil
		}
		page = &apiv1.PageRequest{PageToken: next}
	}
}

// retryBusy retries calls that fail with Unavailable or ResourceExhausted, which kagent
// returns while every Substrate worker is busy, backing off up to 2 s for up to 90 s.
func retryBusy(ctx context.Context, call func() error) error {
	ctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	wait := 250 * time.Millisecond
	for {
		err := call()
		if c := status.Code(err); err == nil || (c != codes.Unavailable && c != codes.ResourceExhausted) {
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

// waitState polls until the session settles in the wanted state.
func (k *Kagent) waitState(ctx context.Context, sessionID string, want apiv1.RuntimeState) error {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	for {
		r, err := k.api.Session.GetSession(ctx, &apiv1.GetSessionRequest{SessionId: sessionID})
		if err != nil {
			return err
		}
		s := r.GetSession()
		if s.GetState() == want && s.GetOperation() == apiv1.RuntimeOperation_RUNTIME_OPERATION_NONE {
			return nil
		}
		if s.GetState() == apiv1.RuntimeState_RUNTIME_STATE_FAILED {
			return fmt.Errorf("session failed: %s", s.GetFailure().String())
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("waiting for %s: %w", want, ctx.Err())
		case <-time.After(20 * time.Millisecond):
		}
	}
}

// Actor is a duck's Agent Substrate actor: every kagent session runs as exactly one.
type Actor struct {
	Name     string `json:"name"`     // "session-<session id>"
	State    string `json:"state"`    // RUNNING, SUSPENDED, RESUMING, SUSPENDING, ...
	Node     string `json:"node"`     // Kubernetes node holding its sandbox or local snapshot
	Template string `json:"template"` // ActorTemplate (the duck Agent's prepared revision)
}

// ActorStates returns every session actor in the agent's namespace, keyed by session ID.
func (k *Kagent) ActorStates(ctx context.Context) (map[string]Actor, error) {
	out := map[string]Actor{}
	page := &apiv1.PageRequest{}
	for {
		r, err := k.system.ListSubstrateActors(k.ctx(ctx), &apiv1.ListSubstrateActorsRequest{Atespace: k.agent.Namespace, Page: page})
		if err != nil {
			return nil, err
		}
		for _, a := range r.GetActors() {
			if id, ok := strings.CutPrefix(a.GetMetadata().GetName(), "session-"); ok {
				out[id] = Actor{
					Name:     a.GetMetadata().GetName(),
					State:    strings.TrimPrefix(a.GetStatus().GetState().String(), "ACTOR_STATE_"),
					Node:     a.GetStatus().GetAssignedNode(),
					Template: a.GetActorTemplate().GetName(),
				}
			}
		}
		next := r.GetPage().GetNextPageToken()
		if next == "" {
			return out, nil
		}
		page = &apiv1.PageRequest{PageToken: next}
	}
}

// SubstrateStats is the Substrate summary shown on the big screen.
type SubstrateStats struct {
	Actors      int64            `json:"actors"`
	Running     int64            `json:"running"`
	Workers     int64            `json:"workers"`
	BusyWorkers int64            `json:"busyWorkers"`
	ByState     map[string]int64 `json:"byState"`
	Error       string           `json:"error,omitempty"`
}

func (k *Kagent) Substrate(ctx context.Context) (SubstrateStats, error) {
	r, err := k.system.GetSubstrateSummary(k.ctx(ctx), &apiv1.GetSubstrateSummaryRequest{})
	if err != nil {
		return SubstrateStats{}, err
	}
	s := SubstrateStats{
		Actors: r.GetActorCount(), Running: r.GetRunningActorCount(),
		Workers: r.GetWorkerCount(), BusyWorkers: r.GetBusyWorkerCount(),
		ByState: map[string]int64{}, Error: r.GetAteApiError(),
	}
	for _, c := range r.GetActorStatusCounts() {
		s.ByState[strings.TrimPrefix(c.GetState().String(), "ACTOR_STATE_")] = c.GetCount()
	}
	return s, nil
}
