package heist

import (
	"math"
	"math/rand/v2"
	"testing"
)

func seeded(w *World) *World {
	w.Rand = rand.New(rand.NewPCG(7, 11))
	return w
}

func TestRollPartTable(t *testing.T) {
	rnd := rand.New(rand.NewPCG(1, 2))
	const n = 100000
	got := map[string]int{}
	for range n {
		p := RollPart(rnd)
		if p.ID == "" {
			t.Fatal("part without ID")
		}
		if (p.Kind == "ram") != (p.Tier >= 1 && p.Tier <= 5) || (p.Kind != "ram" && p.Tier != 0) {
			t.Fatalf("bad part %+v", p)
		}
		got[p.Label()]++
	}
	want := map[string]float64{
		"DDR1": 20, "DDR2": 12.5, "DDR3": 9, "DDR4": 6, "DDR5": 2.5,
		"CPU": 20, "drive": 20, "board": 10,
	}
	for k, pct := range want {
		if f := 100 * float64(got[k]) / n; math.Abs(f-pct) > 1 {
			t.Errorf("%s: %.2f%%, want %.1f%% ±1", k, f, pct)
		}
	}
	if len(got) != len(want) {
		t.Errorf("unexpected labels: %v", got)
	}
}

func TestNewAvatarFullHealth(t *testing.T) {
	a := NewAvatar("p", "nick", "#fff", "red", Vec{1.5, 0.5})
	if a.HP != 100 || a.Down || !a.Online || a.PlayerID != "p" {
		t.Fatalf("new avatar %+v", a)
	}
}

func countKind(ev []Event, kind string) int {
	n := 0
	for _, e := range ev {
		if e.Kind == kind {
			n++
		}
	}
	return n
}

func TestHuntersShootNearest(t *testing.T) {
	// Row 0 is open road. A is 4 tiles from the hunter, B is 5.5 on the other side.
	w := testWorld([]string{"red", "red"}, Vec{14.5, 0.5}, Vec{5, 0.5})
	a, b := w.Avatars["a"], w.Avatars["b"]
	tg := addTarget(w, "h", "red", Working, Vec{10.5, 0.5})
	pin := func() { tg.Pos = Vec{10.5, 0.5}; a.Pos = Vec{14.5, 0.5}; b.Pos = Vec{5, 0.5} }

	pin()
	w.Tick(0.1) // first shot right away
	ev := w.DrainEvents()
	if a.HP != 90 || b.HP != 100 || countKind(ev, "hit") != 1 {
		t.Fatalf("first shot: a=%v b=%v events=%v", a.HP, b.HP, ev)
	}
	if ev[0] != (Event{Kind: "hit", Player: "a", Target: "h"}) {
		t.Fatalf("hit event = %+v", ev[0])
	}
	hits := 1
	for i := 0; i < 23; i++ { // 2.3 s more: shots at 1.3 and 2.5 → only one more by 2.4
		pin()
		w.Tick(0.1)
		hits += countKind(w.DrainEvents(), "hit")
	}
	if hits != 2 || a.HP != 80 {
		t.Fatalf("after 2.4 s: hits=%d hp=%v", hits, a.HP)
	}
	pin()
	w.Tick(0.1) // now 2.5
	if a.HP != 70 || b.HP != 100 {
		t.Fatalf("third shot at 2.5 s: a=%v b=%v", a.HP, b.HP)
	}

	for _, st := range []TargetState{Frozen, Dragged} {
		w2 := testWorld([]string{"red"}, Vec{14.5, 0.5})
		addTarget(w2, "h", "red", st, Vec{10.5, 0.5})
		for range 30 {
			w2.Tick(0.1)
		}
		if hp := w2.Avatars["a"].HP; hp != 100 {
			t.Fatalf("%s target shot: hp=%v", st, hp)
		}
	}
	w3 := testWorld([]string{"red"}, Vec{14.5, 0.5})
	addTarget(w3, "h", "red", Working, Vec{10.5, 0.5}).Busy = true
	for range 30 {
		w3.Tick(0.1)
	}
	if hp := w3.Avatars["a"].HP; hp != 100 {
		t.Fatalf("busy target shot: hp=%v", hp)
	}
	// Nobody within 5 tiles: no shot.
	w4 := testWorld([]string{"red"}, Vec{4.5, 0.5})
	h4 := addTarget(w4, "h", "red", Working, Vec{10.5, 0.5})
	for range 30 {
		h4.Pos = Vec{10.5, 0.5}
		w4.Tick(0.1)
	}
	if hp := w4.Avatars["a"].HP; hp != 100 {
		t.Fatalf("out of range shot: hp=%v", hp)
	}
}

func TestRegenAfterQuiet(t *testing.T) {
	w := testWorld([]string{"red"}, Vec{1.5, 0.5})
	a := w.Avatars["a"]
	a.HP, a.LastHit = 50, 0
	w.Tick(3.9)
	if a.HP != 50 {
		t.Fatalf("regen before 4 s: %v", a.HP)
	}
	w.Tick(0.2) // 0.1 s of it is past the 4 s mark
	if math.Abs(a.HP-50.5) > 1e-9 {
		t.Fatalf("regen at 4.1 s: %v, want 50.5", a.HP)
	}
	w.Tick(1)
	if math.Abs(a.HP-55.5) > 1e-9 {
		t.Fatalf("regen at 5.1 s: %v, want 55.5", a.HP)
	}
	w.Tick(100)
	if a.HP != 100 {
		t.Fatalf("regen cap: %v", a.HP)
	}
	// A hit restarts the quiet clock.
	a.HP, a.LastHit = 60, w.Now
	w.Tick(3)
	if a.HP != 60 {
		t.Fatalf("regen 3 s after a hit: %v", a.HP)
	}
}

func TestDownDropsBagAndRespawns(t *testing.T) {
	w := seeded(testWorld([]string{"red"}, Vec{14.5, 0.5}))
	a := w.Avatars["a"]
	h := addTarget(w, "h", "red", Working, Vec{10.5, 0.5})
	c := addTarget(w, "c", "red", Dragged, Vec{15.2, 0.5})
	a.Dragging = "c"
	a.HP = 10
	a.Inv = []Part{{ID: "p1", Kind: "cpu"}, {ID: "p2", Kind: "ram", Tier: 5}}
	where := a.Pos
	h.Pos = Vec{10.5, 0.5}
	w.Tick(0.1)
	ev := w.DrainEvents()
	if !a.Down || a.HP != 0 || countKind(ev, "down") != 1 {
		t.Fatalf("not down: %+v events=%v", a, ev)
	}
	if a.Dragging != "" || c.State != Frozen {
		t.Fatalf("drag not dropped: dragging=%q target=%v", a.Dragging, c.State)
	}
	if len(a.Inv) != 0 || len(w.Drops) != 2 {
		t.Fatalf("bag: inv=%v drops=%v", a.Inv, w.Drops)
	}
	for _, d := range w.Drops {
		if d.Pos != where || math.Abs(d.Expires-(w.Now+60)) > 1e-9 {
			t.Fatalf("bag drop %+v (pos want %v, now %v)", d, where, w.Now)
		}
	}
	// Down: no hits taken, no movement, no fire, no action.
	other := addTarget(w, "o", "red", Working, Vec{18.5, 0.5})
	frozen := addTarget(w, "f", "red", Frozen, Vec{a.Pos.X, 1})
	_ = frozen
	w.Input("a", Input{Move: Vec{1, 0}, Fire: true})
	pos := a.Pos
	for range 5 {
		h.Pos, other.Pos = Vec{10.5, 0.5}, Vec{18.5, 0.5}
		w.Tick(0.1)
	}
	if a.Pos != pos || other.Hits != 0 || h.Hits != 0 || a.HP != 0 {
		t.Fatalf("down avatar acted: pos=%v→%v hits=%d/%d hp=%v", pos, a.Pos, other.Hits, h.Hits, a.HP)
	}
	tap(w, 0.1, "a")
	if a.Dragging != "" {
		t.Fatalf("down avatar grabbed %q", a.Dragging)
	}
	if countKind(w.DrainEvents(), "hit") != 0 {
		t.Fatal("down avatar was shot")
	}
	// Respawn 3 s after going down.
	w.Input("a", Input{})
	delete(w.Targets, "h")
	delete(w.Targets, "o")
	for a.Down && w.Now < 10 {
		w.Tick(0.1)
	}
	d := w.City.Dispatch
	centre := Vec{d.X + d.W/2, d.Y + d.H/2}
	if a.Down || a.HP != 100 || a.Pos != centre {
		t.Fatalf("respawn: %+v want pos %v", a, centre)
	}
	if countKind(w.DrainEvents(), "respawn") != 1 {
		t.Fatal("no respawn event")
	}
}

func TestFreezeAndDeleteDropLoot(t *testing.T) {
	w := seeded(testWorld([]string{"red"}, Vec{1.5, 0.5}))
	tg := addTarget(w, "t1", "red", Working, Vec{20.5, 1.5})
	tg.Busy, tg.opBy = true, "a"
	w.Resolve("t1", "freeze", false, 1)
	if len(w.Drops) != 0 {
		t.Fatalf("failed freeze dropped %v", w.Drops)
	}
	tg.Busy, tg.opBy = true, "a"
	w.Resolve("t1", "freeze", true, 1)
	if len(w.Drops) != 1 {
		t.Fatalf("freeze drops = %d", len(w.Drops))
	}
	for _, d := range w.Drops {
		if d.Pos != tg.Pos || d.Part.ID == "" || d.ID == "" {
			t.Fatalf("drop %+v", d)
		}
	}
	ev := w.DrainEvents()
	if countKind(ev, "drop") != 1 {
		t.Fatalf("events %v", ev)
	}
	tg.Busy = true
	w.Resolve("t1", "delete", false, 1)
	if len(w.Drops) != 1 {
		t.Fatal("failed delete dropped loot")
	}
	tg.Busy = true
	w.Resolve("t1", "delete", true, 1)
	if len(w.Drops) != 2 {
		t.Fatalf("delete drops = %d", len(w.Drops))
	}
}

func TestPickupAndFullInventory(t *testing.T) {
	w := testWorld([]string{"red"}, Vec{1.5, 0.5})
	a := w.Avatars["a"]
	w.Drops["d1"] = &Drop{ID: "d1", Part: Part{ID: "p1", Kind: "ram", Tier: 5}, Pos: Vec{3, 0.5}, Expires: 100}
	w.Tick(0.1)
	if len(a.Inv) != 0 {
		t.Fatal("picked up from 1.5 tiles")
	}
	a.Pos = Vec{2.25, 0.5} // 0.75 away
	w.Tick(0.1)
	if len(a.Inv) != 1 || a.Inv[0].ID != "p1" || w.Drops["d1"] != nil {
		t.Fatalf("pickup: inv=%v drops=%v", a.Inv, w.Drops)
	}
	ev := w.DrainEvents()
	if countKind(ev, "pickup") != 1 || ev[0].Player != "a" || ev[0].Text != "DDR5" {
		t.Fatalf("pickup events %v", ev)
	}
	for len(a.Inv) < 8 {
		a.Inv = append(a.Inv, Part{ID: "x", Kind: "cpu"})
	}
	w.Drops["d2"] = &Drop{ID: "d2", Part: Part{ID: "p2", Kind: "drive"}, Pos: a.Pos, Expires: 100}
	w.Tick(0.1)
	if len(a.Inv) != 8 || w.Drops["d2"] == nil {
		t.Fatalf("full inventory picked up: inv=%d", len(a.Inv))
	}
	w.Drops["d2"].Expires = w.Now + 0.05
	w.Tick(0.1)
	if w.Drops["d2"] != nil {
		t.Fatal("expired drop not removed")
	}
}

func TestVaultBanks(t *testing.T) {
	v := NewCity(nil).Vault
	w := testWorld([]string{"red"}, Vec{v.X + 1.5, v.Y - 0.5})
	a := w.Avatars["a"]
	w.Tick(0.1)
	if countKind(w.DrainEvents(), "banked") != 0 {
		t.Fatal("banked an empty inventory")
	}
	a.Inv = []Part{{ID: "p1", Kind: "cpu"}, {ID: "p2", Kind: "board"}}
	a.Pos = Vec{v.X + 1.5, v.Y + 1.5}
	w.Tick(0.1)
	ev := w.DrainEvents()
	if len(a.Inv) != 0 || len(w.Stash["a"]) != 2 {
		t.Fatalf("bank: inv=%v stash=%v", a.Inv, w.Stash)
	}
	if countKind(ev, "banked") != 1 || ev[0].Player != "a" || ev[0].Text != "2" {
		t.Fatalf("banked events %v", ev)
	}
	w.Tick(0.1)
	if countKind(w.DrainEvents(), "banked") != 0 {
		t.Fatal("banked again with nothing")
	}
	a.Inv = []Part{{ID: "p3", Kind: "drive"}}
	w.Tick(0.1)
	if len(w.Stash["a"]) != 3 {
		t.Fatalf("stash = %v", w.Stash["a"])
	}
}
