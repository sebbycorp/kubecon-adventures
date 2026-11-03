package heist

import (
	"testing"

	"github.com/sebbycorp/kubecon-adventures/arcade/internal/kagent"
)

// The fields the big screen reads (Task 9): the heist share of Substrate, the vault
// count, the pod price, each board row's best rig and live allies, and policy events.

func TestSubstrateViewHeistShare(t *testing.T) {
	v := substrateView(kagent.SubstrateStats{Actors: 150, Running: 5, Suspended: 140, Workers: 73, BusyWorkers: 6,
		Pools: []kagent.PoolStats{{Name: "flow", Workers: 12}, {Name: "heist", Workers: 40}},
		Agents: []kagent.AgentStats{{Name: "duck", Actors: 100, Awake: 2}, {Name: "heister", Actors: 9, Awake: 3, Suspended: 6},
			{Name: "sidekick", Actors: 2, Awake: 1, Suspended: 1}}})
	want := SubstrateView{Actors: 150, Running: 5, Suspended: 140, Workers: 73, BusyWorkers: 6, Pool: 40, Agents: 11, Awake: 4, Asleep: 7}
	if v != want {
		t.Fatalf("substrateView = %+v, want %+v", v, want)
	}
}

func TestSnapVaultCountsBookedTargets(t *testing.T) {
	g, _ := newManual(t)
	g.mu.Lock()
	addTarget(g.w, "t1", "", Booked, Vec{})
	addTarget(g.w, "t2", "", Frozen, Vec{})
	g.booked["sess-old"] = "cp-old" // booked earlier; its mission was cleaned up
	c := g.commonLocked(nil)
	g.mu.Unlock()
	if c.Vault != 2 {
		t.Fatalf("vault = %d, want 2", c.Vault)
	}
}

func TestSnapPodHourFromEnv(t *testing.T) {
	g, _ := newManual(t)
	if c := g.State(); c.PodHourUSD != 0.02 {
		t.Fatalf("default podHourUsd = %v, want 0.02", c.PodHourUSD)
	}
	t.Setenv("POD_HOUR_USD", "0.05")
	g2, _ := newManual(t)
	if c := g2.State(); c.PodHourUSD != 0.05 {
		t.Fatalf("podHourUsd = %v, want 0.05", c.PodHourUSD)
	}
}

func TestBoardRigAndAllies(t *testing.T) {
	g, _ := newManual(t)
	g.mu.Lock()
	defer g.mu.Unlock()
	build := func(tier int) {
		g.w.Avatars["a"].Inv = rig("p", tier)
		in, err := g.w.BuildRig("a")
		if err != nil {
			t.Fatal(err)
		}
		g.m.OnResult(Result{Kind: "ally", Target: in.Target, OK: true, Spawned: "s-" + in.Target}, g.w)
	}
	build(4)
	build(2)
	c := g.commonLocked(nil)
	if len(c.Board) != 1 || c.Board[0].Rig != 4 || c.Board[0].Allies != 2 {
		t.Fatalf("board = %+v, want rig 4 and 2 allies", c.Board)
	}
	// A failed rig does not count, and allies are counted live.
	g.w.Avatars["a"].Inv = rig("q", 5)
	in, _ := g.w.BuildRig("a")
	g.m.OnResult(Result{Kind: "ally", Target: in.Target, OK: false, Err: "boom"}, g.w)
	delete(g.w.Allies, "ally-1")
	if c := g.commonLocked(nil); c.Board[0].Rig != 4 || c.Board[0].Allies != 1 {
		t.Fatalf("board = %+v, want rig 4 and 1 ally", c.Board)
	}
}

func TestPolicyEvent(t *testing.T) {
	g, _ := newManual(t)
	g.mu.Lock()
	defer g.mu.Unlock()
	addTarget(g.w, "t1", "", Working, Vec{})
	g.m.OnResult(Result{Kind: "step", Target: "t1", Err: "policy: blocked by agentgateway's prompt guard: 403 Forbidden"}, g.w)
	g.m.OnResult(Result{Kind: "step", Target: "t1", Err: "timeout"}, g.w)
	ev := g.w.DrainEvents()
	if len(ev) != 1 || ev[0].Kind != "policy" || ev[0].Target != "t1" || ev[0].Text != "blocked by agentgateway's prompt guard" {
		t.Fatalf("events = %+v, want one policy event", ev)
	}
}

func TestSnapSquadTotals(t *testing.T) {
	g, _ := newManual(t)
	g.mu.Lock()
	defer g.mu.Unlock()
	g.m.squadTD["ABCD"] = 3
	g.m.squadTD["solo-a"] = 2
	g.m.squadTD["WXYZ"] = 0
	if c := g.commonLocked(nil); len(c.Squads) != 1 || c.Squads["ABCD"] != 3 {
		t.Fatalf("squads = %v, want only ABCD: 3", c.Squads)
	}
}
