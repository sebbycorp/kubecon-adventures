package heist

import (
	"errors"
	"fmt"
	"math"
	"math/rand/v2"
	"slices"
	"testing"
)

// rig is one full rig's parts with RAM of the given tier, IDs prefixed by pre.
func rig(pre string, tier int) []Part {
	return []Part{{ID: pre + "b", Kind: "board"}, {ID: pre + "c", Kind: "cpu"},
		{ID: pre + "r", Kind: "ram", Tier: tier}, {ID: pre + "d", Kind: "drive"}}
}

func partIDs(ps []Part) []string {
	out := make([]string, 0, len(ps))
	for _, p := range ps {
		out = append(out, p.ID)
	}
	slices.Sort(out)
	return out
}

// addAlly puts a Working ally of owner at p.
func addAlly(w *World, id, owner string, tier int, p Vec) *Ally {
	al := &Ally{ID: id, Owner: owner, SessionID: "sess-" + id, Pos: p, HP: maxHP, Tier: tier, State: Working}
	w.Allies[id] = al
	return al
}

func TestBuildRigSpendsParts(t *testing.T) {
	w := testWorld([]string{"red", "red"}, Vec{1.5, 0.5}, Vec{1.5, 0.5})
	w.Stash["a"] = []Part{{ID: "b1", Kind: "board"}, {ID: "c1", Kind: "cpu"}, {ID: "r2", Kind: "ram", Tier: 2},
		{ID: "r5", Kind: "ram", Tier: 5}, {ID: "d1", Kind: "drive"}}
	in, err := w.BuildRig("a")
	if err != nil {
		t.Fatalf("BuildRig: %v", err)
	}
	if in.Kind != "ally" || in.Player != "a" || in.Target == "" {
		t.Fatalf("intent = %+v", in)
	}
	if got := partIDs(w.Stash["a"]); !slices.Equal(got, []string{"r2"}) {
		t.Fatalf("stash left = %v, want [r2]", got)
	}
	if w.Allies[in.Target] != nil {
		t.Fatal("ally in the map while still spawning")
	}
	NewMissions(rand.New(rand.NewPCG(1, 2))).OnResult(Result{Kind: "ally", Target: in.Target, OK: true, Spawned: "s1"}, w)
	if al := w.Allies[in.Target]; al == nil || al.Tier != 5 {
		t.Fatalf("ally = %+v, want tier 5", al)
	}

	// Missing parts: nothing spent.
	for _, kind := range []string{"board", "cpu", "ram", "drive"} {
		w := testWorld([]string{"red"}, Vec{1.5, 0.5})
		var ps []Part
		for _, p := range rig("x", 3) {
			if p.Kind != kind {
				ps = append(ps, p)
			}
		}
		w.Stash["a"] = slices.Clone(ps)
		if _, err := w.BuildRig("a"); err == nil || err.Error() != "missing parts" {
			t.Fatalf("without %s: err = %v", kind, err)
		}
		if !slices.Equal(partIDs(w.Stash["a"]), partIDs(ps)) {
			t.Fatalf("without %s: stash changed to %v", kind, w.Stash["a"])
		}
	}

	// Per-player limit: 2, counting allies still spawning.
	w = testWorld([]string{"red", "red"}, Vec{1.5, 0.5}, Vec{1.5, 0.5})
	w.Stash["a"] = slices.Concat(rig("x", 1), rig("y", 1), rig("z", 1))
	first, err := w.BuildRig("a")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.BuildRig("a"); err != nil {
		t.Fatal(err)
	}
	if _, err := w.BuildRig("a"); err == nil || err.Error() != "ally limit" {
		t.Fatalf("third rig err = %v", err)
	}
	if len(w.Stash["a"]) != 4 {
		t.Fatalf("stash after refused rig = %v", w.Stash["a"])
	}
	NewMissions(rand.New(rand.NewPCG(1, 2))).OnResult(Result{Kind: "ally", Target: first.Target, OK: true, Spawned: "s"}, w)
	if _, err := w.BuildRig("a"); err == nil || err.Error() != "ally limit" {
		t.Fatalf("third rig with one deployed err = %v", err)
	}

	// City limit: 16, counting allies still spawning.
	w = testWorld([]string{"red", "blue"}, Vec{1.5, 0.5}, Vec{1.5, 0.5})
	for i := range 15 {
		addAlly(w, fmt.Sprintf("x%d", i), fmt.Sprintf("p%d", i), 1, Vec{1.5, 0.5})
	}
	w.Stash["b"] = rig("b", 1)
	if _, err := w.BuildRig("b"); err != nil {
		t.Fatal(err)
	}
	w.Stash["a"] = rig("a", 1)
	if _, err := w.BuildRig("a"); err == nil || err.Error() != "city full" {
		t.Fatalf("17th ally err = %v", err)
	}
	if len(w.Stash["a"]) != 4 {
		t.Fatalf("stash after city full = %v", w.Stash["a"])
	}
}

func TestAllySpawnPlacesAlly(t *testing.T) {
	x := newMTest(t, 24)
	a := x.w.Avatars["a"]
	a.Pos = Vec{20.5, 1.5}
	x.w.Stash["a"] = rig("p", 4)
	in, err := x.w.BuildRig("a")
	if err != nil {
		t.Fatal(err)
	}
	if !x.s.Submit(in.Kind, in.Target, "", 0, 0) {
		t.Fatal("ally submit refused")
	}
	r := x.pump("ally")
	if !r.OK || r.Spawned != "sess-"+in.Target || r.Step.Say == "" {
		t.Fatalf("result %+v", r)
	}
	al := x.w.Allies[in.Target]
	if al == nil {
		t.Fatal("ally not placed")
	}
	if al.Owner != "a" || al.SessionID != r.Spawned || al.Say != r.Step.Say || al.HP != 100 ||
		al.State != Working || al.Tier != 4 || dist(al.Pos, a.Pos) > 1 {
		t.Fatalf("ally = %+v", al)
	}
	ev := x.w.DrainEvents()
	if i := slices.IndexFunc(ev, func(e Event) bool { return e.Kind == "ally" }); i < 0 ||
		ev[i].Player != "a" || ev[i].Text != "DDR4" {
		t.Fatalf("events %v", ev)
	}

	// A failed spawn refunds exactly the spent parts.
	x.w.Stash["a"] = append(rig("q", 2), Part{ID: "extra", Kind: "cpu"})
	in, err = x.w.BuildRig("a")
	if err != nil {
		t.Fatal(err)
	}
	x.f.err = errors.New("no capacity")
	x.s.Submit(in.Kind, in.Target, "", 0, 0)
	if r := x.pump("ally"); r.OK {
		t.Fatalf("result %+v", r)
	}
	if x.w.Allies[in.Target] != nil {
		t.Fatal("failed spawn placed an ally")
	}
	if got := partIDs(x.w.Stash["a"]); !slices.Equal(got, []string{"extra", "qb", "qc", "qd", "qr"}) {
		t.Fatalf("stash after refund = %v", got)
	}
	if countKind(x.w.DrainEvents(), "rig-failed") != 1 {
		t.Fatal("no rig-failed event")
	}
	if _, err := x.w.BuildRig("a"); err != nil { // the failed one no longer counts
		t.Fatalf("rebuild after refund: %v", err)
	}
}

func TestAllyFireRate(t *testing.T) {
	for tier, want := range map[int]float64{1: 1.0, 2: 1.3, 3: 1.6, 4: 1.9, 5: 2.2} {
		if got := allyFireRate(tier); math.Abs(got-want) > 1e-9 {
			t.Fatalf("tier %d rate %v, want %v", tier, got, want)
		}
	}
}

func TestAllyFollowsAndFires(t *testing.T) {
	// Follow: the owner runs along the open top road; the ally keeps up.
	for _, tier := range []int{1, 5} {
		w := testWorld([]string{"red"}, Vec{1.5, 0.5})
		al := addAlly(w, "al", "a", tier, Vec{1.5, 0.5})
		w.Input("a", Input{Move: Vec{1, 0}})
		for range 60 {
			w.Tick(0.1)
			if d := dist(al.Pos, w.Avatars["a"].Pos); d > 2 {
				t.Fatalf("tier %d: ally %.2f tiles behind", tier, d)
			}
		}
		if w.Avatars["a"].Pos.X < 25 {
			t.Fatalf("owner barely moved: %v", w.Avatars["a"].Pos)
		}
	}
	// A frozen ally stays put.
	w := testWorld([]string{"red"}, Vec{1.5, 0.5})
	al := addAlly(w, "al", "a", 1, Vec{1.5, 0.5})
	al.State, al.HP = Frozen, 0
	w.Input("a", Input{Move: Vec{1, 0}})
	for range 20 {
		w.Tick(0.1)
	}
	if al.Pos != (Vec{1.5, 0.5}) {
		t.Fatalf("frozen ally moved to %v", al.Pos)
	}

	// Fire rate: shots over 20 s at a hunter 4 tiles away.
	for tier, rate := range map[int]float64{1: 1.0, 5: 2.2} {
		w := testWorld([]string{"red"}, Vec{10.5, 0.5})
		al := addAlly(w, "al", "a", tier, Vec{10.5, 0.5})
		tg := addTarget(w, "h", "", Working, Vec{14.5, 0.5})
		shots := 0
		for range 200 {
			tg.Pos, tg.ShotCD, al.Pos, w.Avatars["a"].Pos = Vec{14.5, 0.5}, 10, Vec{10.5, 0.5}, Vec{10.5, 0.5}
			w.Tick(0.1)
			shots += tg.Hits
			tg.Hits, tg.Busy = 0, false
		}
		if want := rate * 20; math.Abs(float64(shots)-want) > 1 {
			t.Fatalf("tier %d: %d shots in 20 s, want %v", tier, shots, want)
		}
	}

	// Out of range (7 tiles): no shots. A frozen ally doesn't fire.
	for _, frozen := range []bool{false, true} {
		w := testWorld([]string{"red"}, Vec{10.5, 0.5})
		al := addAlly(w, "al", "a", 5, Vec{10.5, 0.5})
		pos := Vec{17.5, 0.5}
		if frozen {
			al.State, al.HP, pos = Frozen, 0, Vec{13.5, 0.5}
		}
		tg := addTarget(w, "h", "", Working, pos)
		for range 30 {
			tg.Pos, tg.ShotCD, w.Avatars["a"].Pos = pos, 10, Vec{10.5, 0.5}
			w.Tick(0.1)
		}
		if tg.Hits != 0 {
			t.Fatalf("frozen=%v: hits = %d", frozen, tg.Hits)
		}
	}

	// Hits go under the owner; the third hit's freeze is the owner's takedown.
	w = testWorld([]string{"red"}, Vec{10.5, 0.5})
	al = addAlly(w, "al", "a", 5, Vec{10.5, 0.5})
	tg := addTarget(w, "h", "red", Working, Vec{14.5, 0.5})
	var got []Intent
	for i := 0; i < 50 && len(got) == 0; i++ {
		tg.Pos, tg.ShotCD, al.Pos, w.Avatars["a"].Pos = Vec{14.5, 0.5}, 10, Vec{10.5, 0.5}, Vec{10.5, 0.5}
		got = w.Tick(0.1)
	}
	if len(got) != 1 || got[0] != (Intent{Kind: "freeze", Target: "h", Player: "a"}) {
		t.Fatalf("intents = %v", got)
	}
	if tg.Hits != 3 || !tg.HitBy["a"] || len(tg.HitBy) != 1 || !tg.Busy {
		t.Fatalf("target %+v", tg)
	}
	w.Resolve("h", "freeze", true, 5)
	if tg.FrozenBy != "a" || tg.State != Frozen {
		t.Fatalf("FrozenBy %q state %s", tg.FrozenBy, tg.State)
	}
	m := NewMissions(rand.New(rand.NewPCG(1, 2)))
	m.OnResult(Result{Kind: "freeze", Target: "h", OK: true}, w)
	if lb := m.Leaderboard(); len(lb) != 1 || lb[0].PlayerID != "a" || lb[0].Takedowns != 1 {
		t.Fatalf("leaderboard %+v", lb)
	}
}

func TestHuntersTargetAllies(t *testing.T) {
	// Row 0: hunter at 10.5, ally 3 tiles away, owner 6 tiles away (out of range).
	w := testWorld([]string{"red"}, Vec{16.5, 0.5})
	a := w.Avatars["a"]
	al := addAlly(w, "al", "a", 1, Vec{13.5, 0.5})
	h := addTarget(w, "h", "", Working, Vec{10.5, 0.5})
	pin := func() { h.Pos, a.Pos, al.FireCD = Vec{10.5, 0.5}, Vec{16.5, 0.5}, 100; al.Pos = Vec{13.5, 0.5} }
	pin()
	w.Tick(0.1)
	if al.HP != 90 || a.HP != 100 {
		t.Fatalf("first shot: ally %v owner %v", al.HP, a.HP)
	}
	var freezes []Intent
	for range 300 { // 30 s: far more shots than it takes
		pin()
		for _, in := range w.Tick(0.1) {
			if in.Target == "al" {
				freezes = append(freezes, in)
			}
		}
	}
	if len(freezes) != 1 || freezes[0] != (Intent{Kind: "freeze", Target: "al", Player: "a"}) {
		t.Fatalf("freeze intents = %v", freezes)
	}
	if al.HP != 0 || !al.Busy || a.HP != 100 {
		t.Fatalf("ally %+v owner hp %v", al, a.HP)
	}
	w.Resolve("al", "freeze", true, 5)
	if al.State != Frozen || al.Busy || len(w.Drops) != 0 {
		t.Fatalf("after freeze: %+v drops %d", al, len(w.Drops))
	}
	// A frozen ally is not shot and does not move.
	frozenAt := al.Pos
	for range 30 {
		h.Pos, a.Pos = Vec{10.5, 0.5}, Vec{16.5, 0.5}
		w.Tick(0.1)
	}
	if al.HP != 0 || al.Pos != frozenAt {
		t.Fatalf("frozen ally hp %v pos %v", al.HP, al.Pos)
	}

	// The nearer of a player and an ally takes the shot.
	w = testWorld([]string{"red"}, Vec{12.5, 0.5})
	al = addAlly(w, "al", "a", 1, Vec{14.5, 0.5})
	al.FireCD = 100
	addTarget(w, "h", "", Working, Vec{10.5, 0.5})
	w.Tick(0.1)
	if w.Avatars["a"].HP != 90 || al.HP != 100 {
		t.Fatalf("nearer player: owner %v ally %v", w.Avatars["a"].HP, al.HP)
	}
}

func TestOwnerSpringsAlly(t *testing.T) {
	w := testWorld([]string{"red", "blue"}, Vec{20.5, 9.5}, Vec{20.5, 9.5})
	a := w.Avatars["a"]
	al := addAlly(w, "al", "a", 2, Vec{21.5, 9.5})
	al.State, al.HP = Frozen, 0
	if got := tap(w, 0.1, "b"); len(got) != 0 || al.Busy {
		t.Fatalf("other player's tap: %v", got)
	}
	w.Input("b", Input{Action: true}) // another player's hold does nothing either
	for range 15 {
		al.Pos = Vec{21.5, 9.5}
		if got := w.Tick(0.1); len(got) != 0 {
			t.Fatalf("other player's hold: %v", got)
		}
	}
	w.Input("b", Input{})
	if al.Busy || al.State != Frozen || w.Allies["al"] == nil {
		t.Fatalf("ally touched by b: %+v", al)
	}

	a.SpringCD = 15 // your own ally ignores the spring cooldown
	got := tap(w, 0.1, "a")
	if len(got) != 1 || got[0] != (Intent{Kind: "spring", Target: "al", Player: "a"}) {
		t.Fatalf("intents = %v", got)
	}
	if !al.Busy || a.SpringCD > 15 {
		t.Fatalf("busy=%v SpringCD=%v", al.Busy, a.SpringCD)
	}
	w.Resolve("al", "spring", true, 30)
	NewMissions(rand.New(rand.NewPCG(1, 2))).OnResult(Result{Kind: "spring", Target: "al", OK: true, Step: StepResult{Say: "back online"}}, w)
	if al.State != Working || al.HP != 100 || al.Busy || al.Say != "back online" {
		t.Fatalf("after spring: %+v", al)
	}
}

func TestMissionEndDeletesAllies(t *testing.T) {
	end := map[string]func(x *mtest, ms *Mission){
		"won": func(x *mtest, ms *Mission) { x.freeze(x.target(ms, 0), "a") },
		"failed": func(x *mtest, ms *Mission) {
			x.tickUntil(100, func() bool { return ms.Outcome != "" })
		},
		"escaped": func(x *mtest, ms *Mission) {
			tg := x.target(ms, 0)
			tg.Step = tg.Steps - 1
			x.m.OnResult(Result{Kind: "step", Target: tg.ID, OK: true}, x.w)
		},
	}
	for outcome, fn := range end {
		t.Run(outcome, func(t *testing.T) {
			x := newMTest(t, 24)
			addAlly(x.w, "al-a", "a", 1, Vec{1.5, 0.5})
			addAlly(x.w, "al-b", "b", 1, Vec{1.5, 0.5}).State = Frozen
			addAlly(x.w, "al-c", "c", 1, Vec{1.5, 0.5}) // blue: no mission
			ms := x.start("red", 1)
			fn(x, ms)
			if ms.Outcome != outcome {
				t.Fatalf("outcome %q", ms.Outcome)
			}
			x.m.Tick(x.w, x.s)
			deleted := map[string]bool{}
			for range 2 {
				r := x.pump("delete")
				if !r.OK {
					t.Fatalf("delete %+v", r)
				}
				deleted[r.Target] = true
			}
			if !deleted["al-a"] || !deleted["al-b"] {
				t.Fatalf("deleted %v", deleted)
			}
			if x.w.Allies["al-a"] != nil || x.w.Allies["al-b"] != nil || x.w.Allies["al-c"] == nil {
				t.Fatalf("allies left %v", x.w.Allies)
			}
			x.m.Tick(x.w, x.s)
			x.f.noStart(t) // nothing more to delete
		})
	}
}
