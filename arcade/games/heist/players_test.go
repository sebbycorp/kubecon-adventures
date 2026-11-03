package heist

import (
	"context"
	"slices"
	"testing"

	"github.com/sebbycorp/kubecon-adventures/arcade/internal/platform"
)

// fireTicks holds FIRE for a and ticks n fire cooldowns.
func fireTicks(w *World, a string, n int) []Intent {
	var out []Intent
	w.Input(a, Input{Fire: true})
	for range n {
		out = append(out, w.Tick(fireCD)...)
	}
	w.Input(a, Input{})
	return out
}

func events(w *World, kind string) []Event {
	var out []Event
	for _, e := range w.Events {
		if e.Kind == kind {
			out = append(out, e)
		}
	}
	return out
}

// TestPvPThreeHitsFreeze: three hits from another squad freeze a player, who drops
// what they carry; the shooter's takedown is reported once.
func TestPvPThreeHitsFreeze(t *testing.T) {
	w := testWorld([]string{"red", "blue"}, Vec{5.5, 9.5}, Vec{9.5, 9.5})
	w.Avatars["b"].Inv = []Part{{ID: "p1", Kind: "cpu"}}
	var got [][2]string
	w.OnPlayerFrozen = func(by, v string) { got = append(got, [2]string{by, v}) }
	fireTicks(w, "a", 2)
	if b := w.Avatars["b"]; b.Frozen || b.PvPHits != 2 {
		t.Fatalf("after 2 shots: frozen=%v hits=%d", b.Frozen, b.PvPHits)
	}
	fireTicks(w, "a", 1)
	b := w.Avatars["b"]
	if !b.Frozen || b.FrozenBy != "a" || len(b.Inv) != 0 || len(w.Drops) != 1 {
		t.Fatalf("after 3 shots: %+v drops=%d", b, len(w.Drops))
	}
	if len(got) != 1 || got[0] != [2]string{"a", "b"} || len(events(w, "player-frozen")) != 1 {
		t.Fatalf("takedowns %v events %v", got, w.Events)
	}
	// A frozen player can't be shot again, can't move and can't fire.
	fireTicks(w, "a", 3)
	w.Input("b", Input{Move: Vec{1, 0}, Fire: true})
	at := b.Pos
	w.Tick(0.5)
	if len(got) != 1 || b.Pos != at {
		t.Fatalf("frozen player shot again or moved: %v %+v", got, b.Pos)
	}
}

// TestPvPNoFriendlyFireOrThroughWalls: a squadmate is never shot, and nobody is shot
// through a building.
func TestPvPNoFriendlyFireOrThroughWalls(t *testing.T) {
	w := testWorld([]string{"red", "red"}, Vec{5.5, 9.5}, Vec{9.5, 9.5})
	fireTicks(w, "a", 5)
	if w.Avatars["b"].PvPHits != 0 || w.Avatars["b"].Frozen {
		t.Fatal("friendly fire")
	}
	// x=3..7 on row 5 is a downtown block.
	w = testWorld([]string{"red", "blue"}, Vec{2.5, 5.5}, Vec{8.5, 5.5})
	fireTicks(w, "a", 5)
	if w.Avatars["b"].PvPHits != 0 {
		t.Fatal("shot through a building")
	}
}

// TestPvPPrefersClosest: auto-aim takes whichever is nearer, an agent or a player.
func TestPvPPrefersClosest(t *testing.T) {
	w := testWorld([]string{"red", "blue"}, Vec{5.5, 9.5}, Vec{11.5, 9.5})
	tg := addTarget(w, "t1", "", Working, Vec{8.5, 9.5})
	w.Targets["t1"].ShotCD = 99 // it doesn't shoot back here
	fireTicks(w, "a", 1)
	if tg.Hits != 1 || w.Avatars["b"].PvPHits != 0 {
		t.Fatalf("hits: agent %d, player %d", tg.Hits, w.Avatars["b"].PvPHits)
	}
	tg.Pos = Vec{12.5, 9.5}
	fireTicks(w, "a", 1)
	if tg.Hits != 1 || w.Avatars["b"].PvPHits != 1 {
		t.Fatalf("hits: agent %d, player %d", tg.Hits, w.Avatars["b"].PvPHits)
	}
}

// TestPvPRespawnWithProtection: a frozen player respawns after frozenFor at a safe
// spot, protected for protectFor; protection ends on time.
func TestPvPRespawnWithProtection(t *testing.T) {
	w := testWorld([]string{"red", "blue"}, Vec{5.5, 9.5}, Vec{9.5, 9.5})
	fireTicks(w, "a", 3)
	b := w.Avatars["b"]
	for w.Now+0.1 < b.RespawnAt-0.05 {
		w.Tick(0.1)
	}
	if !b.Frozen {
		t.Fatal("respawned early")
	}
	w.Tick(0.2)
	if b.Frozen || b.HP != maxHP || !w.City.Walkable(b.Pos) || !open(w.City.tile(b.Pos)) {
		t.Fatalf("after respawn: %+v", b)
	}
	if d := dist(b.Pos, w.Avatars["a"].Pos); d < safeFromEnemy {
		t.Fatalf("respawned %v tiles from the enemy", d)
	}
	if len(events(w, "player-respawn")) != 1 {
		t.Fatalf("events %v", w.Events)
	}
	// Protected: shots don't land. Move the shooter next to them to be sure of range.
	w.Avatars["a"].Pos = b.Pos
	w.Avatars["a"].Pos.X -= 1
	if !w.City.Walkable(w.Avatars["a"].Pos) {
		w.Avatars["a"].Pos.X += 2
	}
	fireTicks(w, "a", 3)
	if b.PvPHits != 0 {
		t.Fatalf("hit while protected: %d", b.PvPHits)
	}
	for w.Now < b.ProtectUntil {
		w.Tick(0.1)
	}
	fireTicks(w, "a", 1)
	if b.PvPHits != 1 {
		t.Fatalf("not hit after protection: %d", b.PvPHits)
	}
}

// TestPvPHitsDecay: hits older than pvpHitDecay don't add up to a freeze.
func TestPvPHitsDecay(t *testing.T) {
	w := testWorld([]string{"red", "blue"}, Vec{5.5, 9.5}, Vec{9.5, 9.5})
	fireTicks(w, "a", 2)
	for range int(pvpHitDecay*10) + 2 {
		w.Tick(0.1)
	}
	fireTicks(w, "a", 1)
	if b := w.Avatars["b"]; b.Frozen || b.PvPHits != 1 {
		t.Fatalf("hits did not decay: frozen=%v hits=%d", b.Frozen, b.PvPHits)
	}
}

// TestPlayerAgentLifecycle: an enrolled player's agent starts, is suspended when they
// are frozen (the snapshot event), and resumed when they respawn.
func TestPlayerAgentLifecycle(t *testing.T) {
	w := testWorld([]string{"red", "blue"}, Vec{5.5, 9.5}, Vec{9.5, 9.5})
	if in := w.Tick(0.1); slices.ContainsFunc(in, func(i Intent) bool { return i.Kind == "pagent" }) {
		t.Fatal("agent started before the player played")
	}
	w.Avatars["b"].Enrolled = true
	in := w.Tick(0.1)
	if !slices.Contains(in, Intent{Kind: "pagent", Target: "pa:b", Player: "b"}) {
		t.Fatalf("intents %v", in)
	}
	ag := &w.Avatars["b"].Agent
	if ag.State != agentStarting || !ag.Busy {
		t.Fatalf("agent %+v", ag)
	}
	w.ResolvePlayer(Result{Kind: "pagent", Target: "pa:b", OK: true, Spawned: "s-b", Ms: 900, Step: StepResult{Say: "hi"}})
	if ag.SID != "s-b" || ag.State != agentAwake || ag.Say != "hi" {
		t.Fatalf("agent %+v", ag)
	}
	in = fireTicks(w, "a", 3) // freezing b asks for the suspend in the same tick
	if ag.State != agentSuspending || !slices.Contains(in, Intent{Kind: "freeze", Target: "pa:b", Player: "b"}) {
		t.Fatalf("agent %+v intents %v", ag, in)
	}
	w.Events = nil
	w.ResolvePlayer(Result{Kind: "freeze", Target: "pa:b", OK: true, Ms: 140})
	if ag.State != agentAsleep || ag.Ms != 140 || len(events(w, "player-snapshot")) != 1 {
		t.Fatalf("agent %+v events %v", ag, w.Events)
	}
	var woke []Intent
	for i := 0; i < 80 && len(woke) == 0; i++ {
		for _, x := range w.Tick(0.1) {
			if x.Kind == "pwake" {
				woke = append(woke, x)
			}
		}
	}
	if len(woke) != 1 || w.Avatars["b"].Frozen || ag.State != agentWaking {
		t.Fatalf("wake %v agent %+v", woke, ag)
	}
	w.ResolvePlayer(Result{Kind: "pwake", Target: "pa:b", OK: true, Ms: 300})
	if ag.State != agentAwake || len(events(w, "player-woke")) != 1 {
		t.Fatalf("agent %+v", ag)
	}
	// Going offline suspends it too.
	w.Avatars["b"].Online = false
	if in := w.Tick(0.1); !slices.Contains(in, Intent{Kind: "freeze", Target: "pa:b", Player: "b"}) {
		t.Fatalf("offline: intents %v", in)
	}
}

// TestPlayerAgentCap: past the cap a player still plays, their agent marked as having
// no sandbox, and it starts once a sandbox frees up. A failed start is retried later.
func TestPlayerAgentCap(t *testing.T) {
	w := testWorld([]string{"r", "g", "b"}, Vec{5.5, 9.5}, Vec{9.5, 9.5}, Vec{13.5, 9.5})
	w.PlayerAgentCap = 1
	for _, a := range w.Avatars {
		a.Enrolled = true
	}
	in := w.Tick(0.1)
	if len(in) != 1 || in[0].Target != "pa:a" {
		t.Fatalf("intents %v", in)
	}
	if w.Avatars["b"].Agent.State != agentNoSandbox || w.Avatars["c"].Agent.State != agentNoSandbox {
		t.Fatalf("b %+v c %+v", w.Avatars["b"].Agent, w.Avatars["c"].Agent)
	}
	// a's start fails: it is retried after agentRetry, and its sandbox goes to b.
	w.ResolvePlayer(Result{Kind: "pagent", Target: "pa:a", Err: "boom"})
	in = w.Tick(0.1)
	if len(in) != 1 || in[0].Target != "pa:b" {
		t.Fatalf("after a failed: intents %v", in)
	}
	w.CancelPlayerOp("b", "pagent") // the scheduler refused it
	if ag := w.Avatars["b"].Agent; ag.Busy || ag.State != agentNone {
		t.Fatalf("cancel: %+v", ag)
	}
}

// TestPlayerAgentGame: a player who starts a run gets their own agent through the
// scheduler; when they are evicted it is deleted.
func TestPlayerAgentGame(t *testing.T) {
	ops := &liveOps{}
	g := New(platform.New(nil), ops, nil)
	t.Cleanup(func() { g.s.Close() })
	g.join(platform.Player{ID: "a", Nick: "Ada"})
	g.mu.Lock()
	if _, err := g.start("a"); err != nil {
		t.Fatal(err)
	}
	g.mu.Unlock()
	pumpUntil(t, g, "agent awake", func() bool { return g.w.Avatars["a"].Agent.State == agentAwake })
	if got := ops.called("pagent"); !slices.Equal(got, []string{"pa:a"}) {
		t.Fatalf("pagent calls %v", got)
	}
	g.mu.Lock()
	sid := g.w.Avatars["a"].Agent.SID
	g.evict("a")
	g.mu.Unlock()
	pumpUntil(t, g, "agent deleted", func() bool { return slices.Contains(ops.called("delete"), sid) })
	g.mu.Lock()
	if len(g.w.PlayerSessions()) != 0 {
		t.Fatalf("sessions left: %v", g.w.PlayerSessions())
	}
	g.mu.Unlock()
	_, _, _ = g.Reset(context.Background())
}
