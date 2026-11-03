package kagent

import (
	"context"
	"strings"

	apiv1 "github.com/kagent-dev/kagent/go/api/gen/kagent/api/v1"
)

// ActorInfo is one Agent Substrate actor anywhere in the cluster: the kagent Agent whose
// template it runs, its state (RUNNING, SUSPENDED, ...) and the node holding it.
type ActorInfo struct {
	Agent, State, Node string
}

// AllActors returns every actor Substrate runs, from every game and harness, keyed by
// atespace and actor name.
func (k *Kagent) AllActors(ctx context.Context) (map[string]ActorInfo, error) {
	ctx = k.ctx(ctx)
	out := map[string]ActorInfo{}
	page := &apiv1.PageRequest{}
	for {
		r, err := k.system.ListSubstrateActors(ctx, &apiv1.ListSubstrateActorsRequest{Page: page})
		if err != nil {
			return nil, err
		}
		for _, a := range r.GetActors() {
			key := a.GetMetadata().GetAtespace() + "/" + a.GetMetadata().GetName()
			out[key] = ActorInfo{
				Agent: agentOfTemplate(a.GetActorTemplate().GetName()),
				State: strings.TrimPrefix(a.GetStatus().GetState().String(), "ACTOR_STATE_"),
				Node:  a.GetStatus().GetAssignedNode(),
			}
		}
		next := r.GetPage().GetNextPageToken()
		if next == "" {
			return out, nil
		}
		page = &apiv1.PageRequest{PageToken: next}
	}
}
