package heist

import (
	"math"
	"testing"
)

// testWorld builds a world with online avatars "a", "b", ... at the given positions.
func testWorld(squads []string, pos ...Vec) *World {
	w := NewWorld(NewCity(nil))
	for i, p := range pos {
		id := string(rune('a' + i))
		w.Avatars[id] = NewAvatar(id, id, "", squads[i], p)
	}
	return w
}

func addTarget(w *World, id, squad string, st TargetState, p Vec) *Target {
	t := &Target{ID: id, Name: id, Squad: squad, State: st, Pos: p, HitBy: map[string]bool{}}
	w.Targets[id] = t
	return t
}

// tap presses and releases ACTION for each player before one tick.
func tap(w *World, dt float64, ids ...string) []Intent {
	for _, id := range ids {
		w.Input(id, Input{Action: true})
		w.Input(id, Input{Action: false})
	}
	return w.Tick(dt)
}

func TestTwoHitsFreeze(t *testing.T) {
	w := testWorld([]string{"red", "red"}, Vec{10.5, 9.5}, Vec{10.5, 9.5})
	tg := addTarget(w, "t1", "red", Working, Vec{14.5, 9.5})
	far := addTarget(w, "t2", "red", Working, Vec{2.5, 9.5}) // 8 tiles: out of range

	w.Input("a", Input{Fire: true})
	if got := w.Tick(0.25); tg.Hits != 1 || len(got) != 0 {
		t.Fatalf("after one shot: hits=%d intents=%v", tg.Hits, got)
	}
	w.Input("a", Input{})
	w.Input("b", Input{Fire: true})
	got := w.Tick(0.25)
	if len(got) != 1 || got[0] != (Intent{Kind: "freeze", Target: "t1", Player: "b"}) {
		t.Fatalf("second hit intents = %v", got)
	}
	if !tg.Busy || tg.State != Working || tg.Hits != 2 || tg.opKind != "freeze" {
		t.Fatalf("after second hit: %+v", tg)
	}
	if !tg.HitBy["a"] || !tg.HitBy["b"] {
		t.Fatalf("HitBy = %v", tg.HitBy)
	}
	// Busy targets are not hit again and never yield a second intent.
	w.Input("a", Input{Fire: true})
	for i := 0; i < 4; i++ {
		if got := w.Tick(0.25); len(got) != 0 {
			t.Fatalf("busy target yielded %v", got)
		}
	}
	if tg.Hits != 2 || far.Hits != 0 {
		t.Fatalf("hits after busy: t1=%d t2=%d", tg.Hits, far.Hits)
	}
	w.DrainEvents() // hunter hits from the ticks above
	w.Resolve("t1", "freeze", true, 31)
	if tg.State != Frozen || tg.Busy || tg.FrozenBy != "b" {
		t.Fatalf("after resolve: %+v", tg)
	}
	ev := w.DrainEvents() // the freeze, then its loot drop
	if len(ev) != 2 || ev[0].Kind != "frozen" || ev[0].Player != "b" || ev[0].Ms != 31 || ev[0].Target != "t1" || ev[1].Kind != "drop" {
		t.Fatalf("events = %+v", ev)
	}
	if len(w.DrainEvents()) != 0 {
		t.Fatal("DrainEvents did not clear")
	}
}

func TestFreezeFailureThaws(t *testing.T) {
	w := testWorld([]string{"red"}, Vec{10.5, 9.5})
	tg := addTarget(w, "t1", "red", Working, Vec{14.5, 9.5})
	w.Input("a", Input{Fire: true})
	var got []Intent
	for i := 0; i < 2; i++ {
		got = append(got, w.Tick(0.25)...)
	}
	if len(got) != 1 {
		t.Fatalf("intents = %v", got)
	}
	w.DrainEvents() // hunter hits from the ticks above
	w.Resolve("t1", "freeze", false, 0)
	if tg.Hits != 0 || tg.Busy || tg.State != Working {
		t.Fatalf("after failed freeze: %+v", tg)
	}
	if ev := w.DrainEvents(); len(ev) != 1 || ev[0].Kind != "slipped" {
		t.Fatalf("events = %+v", ev)
	}
}

func TestTapBooksInPlace(t *testing.T) {
	w := testWorld([]string{"blue"}, Vec{20.5, 9.5})
	tg := addTarget(w, "t1", "red", Frozen, Vec{21.5, 9.5}) // any squad's agent
	got := tap(w, 0.1, "a")
	if len(got) != 1 || got[0] != (Intent{Kind: "extract", Target: "t1", Player: "a"}) {
		t.Fatalf("intents = %v", got)
	}
	if !tg.Busy || tg.State != Frozen || tg.Pos != (Vec{21.5, 9.5}) {
		t.Fatalf("after tap: %+v", tg)
	}
	w.Resolve("t1", "extract", true, 900)
	if tg.State != Booked || tg.Busy {
		t.Fatalf("after book: %+v", tg)
	}
	if ev := w.DrainEvents(); countKind(ev, "booked") != 1 || ev[len(ev)-1].Player != "a" {
		t.Fatalf("events = %+v", ev)
	}
	// A failed booking leaves it frozen to tap again.
	t2 := addTarget(w, "t2", "red", Frozen, Vec{21.5, 9.5})
	tap(w, 0.1, "a")
	w.Resolve("t2", "extract", false, 0)
	if t2.State != Frozen || t2.Busy {
		t.Fatalf("after failed book: %+v", t2)
	}
}

func TestTapOutOfRangeOrHeld(t *testing.T) {
	w := testWorld([]string{"blue"}, Vec{20.5, 9.5})
	tg := addTarget(w, "t1", "red", Frozen, Vec{22.5, 9.5}) // 2 tiles: out of reach
	if got := tap(w, 0.1, "a"); len(got) != 0 {
		t.Fatalf("out of range: %v", got)
	}
	// Holding ACTION is one press: it books once, never again until released.
	tg.Pos = Vec{21.5, 9.5}
	w.Input("a", Input{Action: true})
	got := w.Tick(0.1)
	for i := 0; i < 15; i++ {
		got = append(got, w.Tick(0.1)...)
	}
	if len(got) != 1 || got[0].Kind != "extract" {
		t.Fatalf("held ACTION: %v", got)
	}
}

func TestDoubleActionOneWinner(t *testing.T) {
	// Two taps on one target in a tick: the lower player ID wins.
	w := testWorld([]string{"blue", "red"}, Vec{20.5, 9.5}, Vec{22.5, 9.5})
	tg := addTarget(w, "t1", "red", Frozen, Vec{21.5, 9.5})
	got := tap(w, 0.1, "a", "b")
	if len(got) != 1 || got[0].Player != "a" || !tg.Busy {
		t.Fatalf("winner: %v", got)
	}
}

func TestTargetsFlee(t *testing.T) {
	w := testWorld([]string{"red"}, Vec{18.5, 8.5})
	tg := addTarget(w, "t1", "red", Working, Vec{20.5, 8.5})
	// A second target hemmed against a building corner still never enters it.
	t2 := addTarget(w, "t2", "red", Working, Vec{16.5, 7.5})
	d0 := math.Hypot(tg.Pos.X-18.5, tg.Pos.Y-8.5)
	for i := 0; i < 20; i++ {
		w.Tick(0.1)
		for _, x := range []*Target{tg, t2} {
			if !w.City.free(x.Pos) {
				t.Fatalf("%s entered a building at %+v", x.ID, x.Pos)
			}
		}
	}
	if d := math.Hypot(tg.Pos.X-18.5, tg.Pos.Y-8.5); d <= d0 || d < 5 {
		t.Fatalf("target did not flee: distance %v (was %v)", d, d0)
	}
	far := addTarget(w, "t3", "red", Working, Vec{38.5, 8.5})
	w.Tick(0.1)
	if far.Pos != (Vec{38.5, 8.5}) {
		t.Fatalf("target beyond 5 tiles moved: %+v", far.Pos)
	}
}
