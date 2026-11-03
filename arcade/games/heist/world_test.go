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

func TestThreeHitsFreeze(t *testing.T) {
	w := testWorld([]string{"red", "red"}, Vec{10.5, 12.5}, Vec{10.5, 12.5})
	tg := addTarget(w, "t1", "red", Working, Vec{14.5, 12.5})
	far := addTarget(w, "t2", "red", Working, Vec{3.5, 12.5}) // 7 tiles: out of range

	var got []Intent
	w.Input("a", Input{Fire: true})
	got = append(got, w.Tick(0.25)...)
	got = append(got, w.Tick(0.25)...)
	if tg.Hits != 2 || len(got) != 0 {
		t.Fatalf("after two shots: hits=%d intents=%v", tg.Hits, got)
	}
	w.Input("a", Input{})
	w.Input("b", Input{Fire: true})
	got = w.Tick(0.25)
	if len(got) != 1 || got[0] != (Intent{Kind: "freeze", Target: "t1", Player: "b"}) {
		t.Fatalf("third hit intents = %v", got)
	}
	if !tg.Busy || tg.State != Working || tg.Hits != 3 {
		t.Fatalf("after third hit: %+v", tg)
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
	if tg.Hits != 3 || far.Hits != 0 {
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
	w := testWorld([]string{"red"}, Vec{10.5, 12.5})
	tg := addTarget(w, "t1", "red", Working, Vec{14.5, 12.5})
	w.Input("a", Input{Fire: true})
	var got []Intent
	for i := 0; i < 3; i++ {
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

func TestDragAndBook(t *testing.T) {
	w := testWorld([]string{"red"}, Vec{44.5, 24.5})
	a := w.Avatars["a"]
	tg := addTarget(w, "t1", "red", Frozen, Vec{45.5, 24.5})
	if got := tap(w, 0.1, "a"); len(got) != 0 {
		t.Fatalf("own-squad tap gave intents %v (should drag, not spring)", got)
	}
	if tg.State != Dragged || a.Dragging != "t1" {
		t.Fatalf("not dragging: target %v, avatar %q", tg.State, a.Dragging)
	}
	var got []Intent
	w.Input("a", Input{Move: Vec{0, 1}})
	for i := 0; i < 30 && len(got) == 0; i++ {
		before, tp := a.Pos, tg.Pos
		got = append(got, w.Tick(0.1)...)
		if d := math.Hypot(a.Pos.X-before.X, a.Pos.Y-before.Y); d > 0.375+1e-9 {
			t.Fatalf("dragging avatar moved %v in 0.1s, want <= 0.375 (3/4 speed)", d)
		}
		if d := math.Hypot(tg.Pos.X-tp.X, tg.Pos.Y-tp.Y); d > 0.375+1e-9 {
			t.Fatalf("dragged target moved %v in 0.1s, want <= 0.375", d)
		}
	}
	if len(got) != 1 || got[0] != (Intent{Kind: "extract", Target: "t1", Player: "a"}) {
		t.Fatalf("intents = %v", got)
	}
	if !w.City.Vault.Contains(tg.Pos) || !tg.Busy {
		t.Fatalf("target not busy in vault: %+v", tg)
	}
	for i := 0; i < 5; i++ {
		if got := w.Tick(0.1); len(got) != 0 {
			t.Fatalf("second extract: %v", got)
		}
	}
	w.Resolve("t1", "extract", true, 12)
	if tg.State != Booked || a.Dragging != "" || tg.Busy {
		t.Fatalf("after book: %+v dragging=%q", tg, a.Dragging)
	}
}

func TestDropOnShortPress(t *testing.T) {
	w := testWorld([]string{"red"}, Vec{44.5, 24.5})
	tg := addTarget(w, "t1", "red", Frozen, Vec{45.5, 24.5})
	tap(w, 0.1, "a")
	tap(w, 0.1, "a")
	if tg.State != Frozen || w.Avatars["a"].Dragging != "" {
		t.Fatalf("second tap should drop: %+v", tg)
	}
}

func TestDoubleActionOneWinner(t *testing.T) {
	// Two drags on one target: the lower player ID wins.
	w := testWorld([]string{"red", "red"}, Vec{20.5, 12.5}, Vec{22.5, 12.5})
	tg := addTarget(w, "t1", "red", Frozen, Vec{21.5, 12.5})
	tap(w, 0.1, "b", "a")
	if tg.State != Dragged || w.Avatars["a"].Dragging != "t1" || w.Avatars["b"].Dragging != "" {
		t.Fatalf("drag winner: a=%q b=%q", w.Avatars["a"].Dragging, w.Avatars["b"].Dragging)
	}

	// Spring (a is a rival) and drag (b owns it) in the same tick: the drag wins.
	w = testWorld([]string{"blue", "red"}, Vec{20.5, 12.5}, Vec{22.5, 12.5})
	tg = addTarget(w, "t1", "red", Frozen, Vec{21.5, 12.5})
	if got := tap(w, 0.1, "a", "b"); len(got) != 0 {
		t.Fatalf("spring should lose to drag, got %v", got)
	}
	if w.Avatars["b"].Dragging != "t1" || w.Avatars["a"].SpringCD != 0 {
		t.Fatalf("drag should win: b=%q aCD=%v", w.Avatars["b"].Dragging, w.Avatars["a"].SpringCD)
	}

	// A busy target yields nothing to taps or holds.
	w = testWorld([]string{"blue", "red"}, Vec{20.5, 12.5}, Vec{22.5, 12.5})
	tg = addTarget(w, "t1", "red", Frozen, Vec{21.5, 12.5})
	tg.Busy = true
	if got := tap(w, 0.1, "a", "b"); len(got) != 0 || tg.State != Frozen {
		t.Fatalf("busy target: intents %v state %v", got, tg.State)
	}
	w.Input("a", Input{Action: true})
	for i := 0; i < 15; i++ {
		if got := w.Tick(0.1); len(got) != 0 {
			t.Fatalf("busy target held: %v", got)
		}
	}
}

func TestHoldDeletes(t *testing.T) {
	w := testWorld([]string{"blue"}, Vec{20.5, 12.5})
	tg := addTarget(w, "t1", "red", Frozen, Vec{21.5, 12.5})
	w.Input("a", Input{Action: true})
	var got []Intent
	for i := 0; i < 9; i++ {
		got = append(got, w.Tick(0.1)...)
	}
	if len(got) != 0 {
		t.Fatalf("delete before 1s: %v", got)
	}
	got = w.Tick(0.1)
	got = append(got, w.Tick(0.1)...)
	if len(got) != 1 || got[0] != (Intent{Kind: "delete", Target: "t1", Player: "a"}) {
		t.Fatalf("intents = %v", got)
	}
	w.Input("a", Input{Action: false})
	if got := w.Tick(0.1); len(got) != 0 || tg.State != Frozen || w.Avatars["a"].Dragging != "" {
		t.Fatalf("release after hold should do nothing: %v %+v", got, tg)
	}
	w.Resolve("t1", "delete", true, 5)
	if tg.State != Deleted {
		t.Fatalf("state = %v", tg.State)
	}
}

func TestDisconnectDropsDrag(t *testing.T) {
	w := testWorld([]string{"red"}, Vec{20.5, 12.5})
	tg := addTarget(w, "t1", "red", Frozen, Vec{21.5, 12.5})
	tap(w, 0.1, "a")
	w.Input("a", Input{Move: Vec{-1, 0}})
	for i := 0; i < 5; i++ {
		w.Tick(0.1)
	}
	at := tg.Pos
	w.Avatars["a"].Online = false
	w.Tick(0.1)
	if tg.State != Frozen || w.Avatars["a"].Dragging != "" || tg.Pos != at {
		t.Fatalf("after disconnect: %+v (was at %+v)", tg, at)
	}
}

func TestSpringCooldown(t *testing.T) {
	w := testWorld([]string{"blue"}, Vec{20.5, 12.5})
	a := w.Avatars["a"]
	t1 := addTarget(w, "t1", "red", Frozen, Vec{21.5, 12.5})
	got := tap(w, 0.1, "a")
	if len(got) != 1 || got[0] != (Intent{Kind: "spring", Target: "t1", Player: "a"}) {
		t.Fatalf("intents = %v", got)
	}
	if a.SpringCD != 20 || !t1.Busy {
		t.Fatalf("SpringCD=%v busy=%v", a.SpringCD, t1.Busy)
	}
	t2 := addTarget(w, "t2", "red", Frozen, Vec{19.5, 12.5})
	for i := 0; i < 10; i++ {
		w.Tick(1)
	}
	if got := tap(w, 0.1, "a"); len(got) != 0 || t2.Busy {
		t.Fatalf("second spring within 20s: %v", got)
	}
	w.Resolve("t1", "spring", true, 40)
	if t1.State != Working || t1.Hits != 0 || !t1.Restored {
		t.Fatalf("after spring: %+v", t1)
	}
}

func TestTargetsFlee(t *testing.T) {
	w := testWorld([]string{"red"}, Vec{20.5, 12.5})
	tg := addTarget(w, "t1", "red", Working, Vec{22.5, 12.5})
	// A second target hemmed against a building corner still never enters it.
	t2 := addTarget(w, "t2", "red", Working, Vec{18.5, 11.5})
	d0 := math.Hypot(tg.Pos.X-20.5, tg.Pos.Y-12.5)
	for i := 0; i < 20; i++ {
		w.Tick(0.1)
		for _, x := range []*Target{tg, t2} {
			if !w.City.free(x.Pos) {
				t.Fatalf("%s entered a building at %+v", x.ID, x.Pos)
			}
		}
	}
	if d := math.Hypot(tg.Pos.X-20.5, tg.Pos.Y-12.5); d <= d0 || d < 5 {
		t.Fatalf("target did not flee: distance %v (was %v)", d, d0)
	}
	far := addTarget(w, "t3", "red", Working, Vec{40.5, 12.5})
	w.Tick(0.1)
	if far.Pos != (Vec{40.5, 12.5}) {
		t.Fatalf("target beyond 5 tiles moved: %+v", far.Pos)
	}
}
