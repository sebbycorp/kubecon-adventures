// Package kagent wraps the kagent control-plane API (sessions, A2A, Substrate) for one agent.
package kagent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	a2atype "github.com/a2aproject/a2a-go/v2/a2a"
	kagenta2a "github.com/kagent-dev/kagent/go/api/a2a"
	kclient "github.com/kagent-dev/kagent/go/api/client"
	apiv1 "github.com/kagent-dev/kagent/go/api/gen/kagent/api/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

// Usage is the token usage kagent reports for one task.
type Usage struct {
	Input  int64 `json:"inputTokens"`
	Output int64 `json:"outputTokens"`
}

// Kagent wraps the kagent control-plane API for one agent.
type Kagent struct {
	api      *kclient.APIClientSet
	gateway  *kclient.GatewayClientSet
	conn     grpc.ClientConnInterface
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
		conn:     conn,
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

// Reply is an agent's answer to one prompt.
type Reply struct {
	JSON  string     // structured output (when the Agent declares an outputSchema)
	Text  string     // plain text parts
	Tools []ToolCall // tool calls the agent made, in order, with their results
	Usage Usage
	Task  *a2atype.Task
}

// ToolCall is one tool the agent ran (for example kagent's built-in bash) and its result.
type ToolCall struct {
	Name   string         `json:"name"`
	Args   map[string]any `json:"args,omitempty"`
	Output string         `json:"output"`
}

// toolCalls pairs kagent's function_call and function_response data parts by call ID.
func toolCalls(task *a2atype.Task) []ToolCall {
	var calls []ToolCall
	byID := map[string]int{}
	for _, artifact := range task.Artifacts {
		for _, part := range artifact.Parts {
			data, ok := part.Data().(map[string]any)
			if !ok {
				continue
			}
			id, _ := data["id"].(string)
			switch part.Meta()["kagent.dev/a2a/part-type"] {
			case "function_call":
				name, _ := data["name"].(string)
				args, _ := data["args"].(map[string]any)
				byID[id] = len(calls)
				calls = append(calls, ToolCall{Name: name, Args: args})
			case "function_response":
				i, ok := byID[id]
				if !ok {
					continue
				}
				if resp, ok := data["response"].(map[string]any); ok {
					switch v := resp["result"].(type) {
					case string:
						calls[i].Output = v
					default:
						b, _ := json.Marshal(resp)
						calls[i].Output = string(b)
					}
				}
			}
		}
	}
	return calls
}

// Decode unmarshals the structured output (or JSON-looking text) into v.
func (r Reply) Decode(v any) error {
	raw := r.JSON
	if raw == "" && strings.HasPrefix(strings.TrimSpace(r.Text), "{") {
		raw = strings.TrimSpace(r.Text)
	}
	if raw == "" {
		return errors.New("no structured output in agent reply")
	}
	return json.Unmarshal([]byte(raw), v)
}

// Prompt sends one message to a session and waits for the agent's reply.
func (k *Kagent) Prompt(ctx context.Context, sessionID, text string) (Reply, error) {
	var reply Reply
	client, err := k.gateway.A2A.ForSession(ctx, sessionID)
	if err != nil {
		return reply, err
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
		return reply, err
	}
	task, ok := result.(*a2atype.Task)
	if !ok {
		return reply, fmt.Errorf("unexpected A2A result %T", result)
	}
	reply.Task, reply.Usage = task, taskUsage(task)
	if task.Status.State != a2atype.TaskStateCompleted {
		return reply, fmt.Errorf("task ended in %s", task.Status.State)
	}
	var out strings.Builder
	for _, artifact := range task.Artifacts {
		for _, part := range artifact.Parts {
			if kagenta2a.IsStructuredOutputPart(part) {
				if reply.JSON, err = kagenta2a.StructuredOutputJSON(part); err != nil {
					return reply, err
				}
			} else if t := part.Text(); t != "" {
				out.WriteString(t)
			}
		}
	}
	reply.Text = out.String()
	reply.Tools = toolCalls(task)
	return reply, nil
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

// Checkpoint saves a session's latest finished turn (its Substrate snapshot, copied to a
// tag) so it can be forked. kagent answers FailedPrecondition until the turn's snapshot is
// durable, so this retries until the checkpoint is READY.
func (k *Kagent) Checkpoint(ctx context.Context, sessionID, headTaskID string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	cps := apiv1.NewCheckpointServiceClient(k.conn)
	req := &apiv1.CreateCheckpointRequest{SessionId: sessionID, RequestId: "cp-" + headTaskID, ExpectedHeadTaskId: headTaskID}
	wait := 200 * time.Millisecond
	for {
		r, err := cps.CreateCheckpoint(k.ctx(ctx), req)
		if err == nil {
			cp := r.GetCheckpoint()
			if cp.GetState() == apiv1.CheckpointState_CHECKPOINT_STATE_READY {
				return cp.GetId(), nil
			}
			if cp.GetState() == apiv1.CheckpointState_CHECKPOINT_STATE_FAILED {
				return "", fmt.Errorf("checkpoint failed: %s", cp.GetFailure().String())
			}
		} else if c := status.Code(err); c != codes.FailedPrecondition && c != codes.Unavailable && c != codes.Aborted {
			return "", err
		}
		select {
		case <-ctx.Done():
			return "", fmt.Errorf("waiting for checkpoint: %w", ctx.Err())
		case <-time.After(wait):
		}
		wait = min(2*wait, time.Second)
	}
}

// Fork starts a new session (and Substrate actor) from a checkpoint. The fork shares the
// conversation up to the checkpoint, then diverges.
func (k *Kagent) Fork(ctx context.Context, checkpointID, requestID string) (string, error) {
	var r *apiv1.ForkSessionResponse
	err := retryBusy(ctx, func() (err error) {
		r, err = apiv1.NewCheckpointServiceClient(k.conn).ForkSession(k.ctx(ctx), &apiv1.ForkSessionRequest{CheckpointId: checkpointID, RequestId: requestID})
		return err
	})
	if err != nil {
		return "", err
	}
	id := r.GetSession().GetId()
	return id, k.waitState(ctx, id, apiv1.RuntimeState_RUNTIME_STATE_READY)
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

// SubstrateStats is the arcade-wide Agent Substrate picture: every worker pool and
// every agent (all games), not just ducks.
type SubstrateStats struct {
	Actors      int64            `json:"actors"`
	Running     int64            `json:"running"`
	Suspended   int64            `json:"suspended"`
	Workers     int64            `json:"workers"`
	BusyWorkers int64            `json:"busyWorkers"`
	ByState     map[string]int64 `json:"byState"`
	Pools       []PoolStats      `json:"pools"`
	Agents      []AgentStats     `json:"agents"`
	Error       string           `json:"error,omitempty"`
}

type PoolStats struct {
	Name     string   `json:"name"`
	Replicas int64    `json:"replicas"` // desired workers (WorkerPool spec)
	Workers  int      `json:"workers"`  // workers registered with Substrate
	Nodes    []string `json:"nodes"`
}

// AgentStats groups actors by the kagent Agent whose template they run.
type AgentStats struct {
	Name      string `json:"name"`
	Actors    int    `json:"actors"`
	Awake     int    `json:"awake"`
	Suspended int    `json:"suspended"`
}

func (k *Kagent) Substrate(ctx context.Context) (SubstrateStats, error) {
	ctx = k.ctx(ctx)
	r, err := k.system.GetSubstrateSummary(ctx, &apiv1.GetSubstrateSummaryRequest{})
	if err != nil {
		return SubstrateStats{}, err
	}
	s := SubstrateStats{
		Actors: r.GetActorCount(), Running: r.GetRunningActorCount(),
		Workers: r.GetWorkerCount(), BusyWorkers: r.GetBusyWorkerCount(),
		ByState: map[string]int64{}, Error: r.GetAteApiError(),
	}
	for _, c := range r.GetActorStatusCounts() {
		st := strings.TrimPrefix(c.GetState().String(), "ACTOR_STATE_")
		s.ByState[st] = c.GetCount()
	}
	s.Suspended = s.ByState["SUSPENDED"]

	pools := map[string]*PoolStats{}
	for _, wp := range r.GetWorkerPools() {
		ps := &PoolStats{Name: wp.GetRef().GetName()}
		if spec, ok := wp.GetResource().GetValue().AsMap()["spec"].(map[string]any); ok {
			if n, ok := spec["replicas"].(float64); ok {
				ps.Replicas = int64(n)
			}
		}
		pools[ps.Name] = ps
	}
	page := &apiv1.PageRequest{}
	for {
		w, err := k.system.ListSubstrateWorkers(ctx, &apiv1.ListSubstrateWorkersRequest{Page: page})
		if err != nil {
			return s, err
		}
		for _, wk := range w.GetWorkers() {
			ps, ok := pools[wk.GetWorkerPool()]
			if !ok {
				ps = &PoolStats{Name: wk.GetWorkerPool()}
				pools[ps.Name] = ps
			}
			ps.Workers++
			if n := wk.GetNodeName(); n != "" && !slices.Contains(ps.Nodes, n) {
				ps.Nodes = append(ps.Nodes, n)
			}
		}
		if page = (&apiv1.PageRequest{PageToken: w.GetPage().GetNextPageToken()}); page.PageToken == "" {
			break
		}
	}
	for _, ps := range pools {
		slices.Sort(ps.Nodes)
		s.Pools = append(s.Pools, *ps)
	}
	slices.SortFunc(s.Pools, func(a, b PoolStats) int { return strings.Compare(a.Name, b.Name) })

	agents := map[string]*AgentStats{}
	page = &apiv1.PageRequest{}
	for {
		a, err := k.system.ListSubstrateActors(ctx, &apiv1.ListSubstrateActorsRequest{Page: page})
		if err != nil {
			return s, err
		}
		for _, act := range a.GetActors() {
			name := agentOfTemplate(act.GetActorTemplate().GetName())
			as, ok := agents[name]
			if !ok {
				as = &AgentStats{Name: name}
				agents[name] = as
			}
			as.Actors++
			switch st := strings.TrimPrefix(act.GetStatus().GetState().String(), "ACTOR_STATE_"); {
			case st == "SUSPENDED":
				as.Suspended++
			case st == "RUNNING" || st == "RESUMING" || st == "SUSPENDING":
				as.Awake++
			}
		}
		if page = (&apiv1.PageRequest{PageToken: a.GetPage().GetNextPageToken()}); page.PageToken == "" {
			break
		}
	}
	for _, as := range agents {
		s.Agents = append(s.Agents, *as)
	}
	slices.SortFunc(s.Agents, func(a, b AgentStats) int { return b.Actors - a.Actors })
	return s, nil
}

// agentOfTemplate maps a kagent ActorTemplate ("<agent>-<12 hex revision>") to its Agent.
func agentOfTemplate(t string) string {
	if i := strings.LastIndex(t, "-"); i > 0 && len(t)-i-1 == 12 {
		return t[:i]
	}
	return t
}
