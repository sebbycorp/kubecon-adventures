package flow

import (
	"testing"

	"github.com/sebbycorp/kubecon-adventures/arcade/internal/kagent"
	"github.com/sebbycorp/kubecon-adventures/arcade/internal/platform"
)

func TestFleetDiff(t *testing.T) {
	g := &Game{}
	a := func(agent, state string) kagent.ActorInfo { return kagent.ActorInfo{Agent: agent, State: state, Node: "n1"} }
	same := func(poll string, got, want []ActorMove) {
		t.Helper()
		if len(got) != len(want) {
			t.Fatalf("%s poll moves = %+v, want %+v", poll, got, want)
		}
		for i := range want {
			if got[i] != want[i] {
				t.Fatalf("%s poll move %d = %+v, want %+v", poll, i, got[i], want[i])
			}
		}
	}

	if g.fleetDiff(map[string]kagent.ActorInfo{"k/d1": a("duck", "SUSPENDED"), "k/f1": a(flowAgent, "SUSPENDED")}, 1) {
		t.Fatal("first poll reported changes")
	}
	if !g.fleetDiff(map[string]kagent.ActorInfo{
		"k/d1": a("duck", "RUNNING"),    // woke
		"k/d2": a("duck", "RUNNING"),    // new
		"k/d3": a("duck", "RUNNING"),    // new
		"k/h1": a("heister", "RUNNING"), // new
		"k/f1": a(flowAgent, "RUNNING"), // Agent Flow's own: skipped
	}, 2) {
		t.Fatal("second poll reported no changes")
	}
	same("second", g.amoves, []ActorMove{{"duck", "new", 2, "n1", 2}, {"duck", "wake", 1, "n1", 2}, {"heister", "new", 1, "n1", 2}})

	g.fleetDiff(map[string]kagent.ActorInfo{"k/d1": a("duck", "SUSPENDING"), "k/f1": a(flowAgent, "RUNNING")}, 3)
	same("third", g.amoves[3:], []ActorMove{{"duck", "gone", 2, "n1", 3}, {"duck", "sleep", 1, "n1", 3}, {"heister", "gone", 1, "n1", 3}})

	if g.fleetDiff(map[string]kagent.ActorInfo{"k/d1": a("duck", "SUSPENDED"), "k/f1": a(flowAgent, "RUNNING")}, 4) {
		t.Fatal("SUSPENDING to SUSPENDED counted as a change")
	}
}

func TestOnOpSkipsFlowAndCaps(t *testing.T) {
	g := &Game{hub: platform.NewHub()}
	g.OnOp(kagent.Op{Agent: flowAgent, Kind: "turn"})
	for i := range 3 * maxOps {
		g.OnOp(kagent.Op{Agent: "duck", Kind: "turn", At: int64(i)})
	}
	if len(g.ops) > 2*maxOps || g.ops[len(g.ops)-1].At != 3*maxOps-1 {
		t.Fatalf("ops = %d, newest at %d", len(g.ops), g.ops[len(g.ops)-1].At)
	}
	for _, op := range g.ops {
		if op.Agent == flowAgent {
			t.Fatal("Agent Flow's own op kept")
		}
	}
}
