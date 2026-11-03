package defense

import (
	"math"
	"math/rand/v2"
	"testing"
)

func testWorld() *World { return NewWorld(rand.New(rand.NewPCG(1, 2))) }

// kinds counts intents by kind.
func countKinds(in []Intent) map[string]int {
	m := map[string]int{}
	for _, i := range in {
		m[i.Kind]++
	}
	return m
}

// addAgent puts a running agent with a session at p.
func addAgent(w *World, id string, p Vec) *Agent {
	a := &Agent{ID: id, Session: "s-" + id, Role: "coder", Kind: "worker", Need: 2, Name: "coder-" + id, Pos: p, State: Running,
		orbitR: 10, orbitW: 1, seen: true, NextKids: 1e9}
	w.Agents[id] = a
	return a
}

func TestShipMovesAndStaysInArena(t *testing.T) {
	w := testWorld()
	s := w.Join("p", "Pat", "blue")
	s.Pos = Vec{5, 5}
	w.Input("p", Input{Move: Vec{-1, 0}})
	for range 60 {
		w.Tick(1.0 / 15)
	}
	if s.Pos.X != shipMargin {
		t.Fatalf("x = %v, want clamped to %v", s.Pos.X, shipMargin)
	}
	if math.Abs(s.Heading-math.Pi) > 1e-9 {
		t.Fatalf("heading = %v, want pi", s.Heading)
	}
	w.Input("p", Input{Move: Vec{math.NaN(), 5}})
	w.Tick(0.1)
	if !finite(s.Pos.X) || !finite(s.Pos.Y) || s.Vel.len() > shipSpeed+1e-9 {
		t.Fatalf("bad input broke the ship: %+v", s)
	}
}

func TestNoFireOutsideRound(t *testing.T) {
	w := testWorld()
	w.Join("p", "Pat", "blue")
	w.Input("p", Input{Fire: true})
	w.Tick(0.1)
	if len(w.Bullets) != 0 {
		t.Fatalf("idle round fired %d bullets", len(w.Bullets))
	}
}

func TestTwoHitsSuspend(t *testing.T) {
	w := testWorld()
	s := w.Join("p", "Pat", "blue")
	_ = w.Play()
	s.Pos, s.Heading, s.Vel = Vec{40, 50}, 0, Vec{}
	a := addAgent(w, "a1", Vec{60, 50})
	a.Arrived, a.orbitR, a.orbitA = true, 20, math.Pi // sits at (60, 50), barely moving
	w.Input("p", Input{Fire: true})
	var sus []Intent
	for range 30 {
		for _, in := range w.Tick(1.0 / 15) {
			if in.Kind == "suspend" {
				sus = append(sus, in)
			}
		}
	}
	if len(sus) != 1 || sus[0].ID != "a1" || sus[0].Session != "s-a1" || sus[0].By != "p" {
		t.Fatalf("suspend intents = %+v", sus)
	}
	if a.State != Suspending || a.Hits != 2 {
		t.Fatalf("agent = %+v", a)
	}
	if w.Running() != 1 {
		t.Fatalf("a suspend in flight still holds its worker: running = %d", w.Running())
	}
	w.Resolve(Result{Kind: "suspend", ID: "a1", OK: true, Ms: 70})
	if a.State != Suspended || s.Score != 1 || s.Total != 1 || w.Round.Suspended != 1 || w.Running() != 0 {
		t.Fatalf("after suspend: agent %+v ship %+v round %+v", a, s, w.Round)
	}
	// The capsule drifts to storage and is kept there for the round's cleanup.
	for range 400 {
		w.Tick(0.1)
	}
	if _, ok := w.Stored["a1"]; !ok || w.Agents["a1"] != nil || w.Hosted() < 1 {
		t.Fatalf("capsule never reached storage: stored=%v", w.Stored)
	}
}

func TestFailedSuspendSlips(t *testing.T) {
	w := testWorld()
	_ = w.Play()
	a := addAgent(w, "a1", Vec{60, 50})
	a.State, a.Hits, a.By = Suspending, 2, "p"
	w.Resolve(Result{Kind: "suspend", ID: "a1", Err: "boom"})
	if a.State != Running || a.Hits != 0 {
		t.Fatalf("agent = %+v", a)
	}
}

func TestAimAssistBendsShots(t *testing.T) {
	w := testWorld()
	s := w.Join("p", "Pat", "blue")
	_ = w.Play()
	s.Pos, s.Heading = Vec{40, 50}, 15*math.Pi/180 // 15 degrees off the agent
	a := addAgent(w, "a1", Vec{70, 50})
	a.Arrived, a.orbitR, a.orbitA = true, 10, math.Pi
	a.Pos = Vec{70, 50}
	w.fire(s)
	if len(w.Bullets) != 1 {
		t.Fatal("no bullet")
	}
	if b := w.Bullets[0]; math.Abs(math.Atan2(b.Vel.Y, b.Vel.X)) > 5*math.Pi/180 {
		t.Fatalf("bullet heads %v rad, want bent toward the agent", math.Atan2(b.Vel.Y, b.Vel.X))
	}
	// Outside the cone: straight along the heading.
	w.Bullets = nil
	s.Heading = math.Pi / 2
	w.fire(s)
	if b := w.Bullets[0]; math.Abs(math.Atan2(b.Vel.Y, b.Vel.X)-math.Pi/2) > 1e-9 {
		t.Fatal("a shot outside the cone was bent")
	}
}

func TestSegmentHit(t *testing.T) {
	cases := []struct {
		a, b, c Vec
		hit     bool
	}{
		{Vec{0, 0}, Vec{10, 0}, Vec{5, 1}, true},
		{Vec{0, 0}, Vec{10, 0}, Vec{5, 3}, false},
		{Vec{0, 0}, Vec{1, 0}, Vec{5, 0}, false},
		{Vec{0, 0}, Vec{0, 0}, Vec{0, 1}, true},
	}
	for _, c := range cases {
		if _, got := segmentHit(c.a, c.b, c.c, 2); got != c.hit {
			t.Errorf("segmentHit(%v, %v, %v) = %v", c.a, c.b, c.c, got)
		}
	}
}

func TestSpawnTaskAndPlacement(t *testing.T) {
	w := testWorld()
	w.Join("p", "Pat", "blue")
	_ = w.Play()
	var spawn Intent
	for range 60 {
		for _, in := range w.Tick(0.1) {
			if in.Kind == "spawn" && spawn.ID == "" {
				spawn = in
			}
		}
	}
	if spawn.ID == "" || spawn.Role == "" {
		t.Fatal("no spawn in 6 s")
	}
	w.Resolve(Result{Kind: "spawn", ID: spawn.ID, Session: "sess", OK: true})
	a := w.Agents[spawn.ID]
	if a == nil || a.Role != spawn.Role || a.State != Running {
		t.Fatalf("agent = %+v", a)
	}
	got := w.Tick(0.1)
	if k := countKinds(got); k["task"] != 1 {
		t.Fatalf("intents after placement = %+v", got)
	}
	w.Resolve(Result{Kind: "task", ID: spawn.ID, OK: true, Say: "Shipped!"})
	if !a.Done || a.Say != "Shipped!" {
		t.Fatalf("agent after task = %+v", a)
	}
	// It reaches its orbit round the cluster.
	for range 600 {
		w.Tick(0.1)
	}
	if !a.Arrived || math.Abs(dist(a.Pos, Cluster)-a.orbitR) > 1e-6 {
		t.Fatalf("agent never settled into orbit: %+v", a)
	}
}

// TestClusterFull: spawns never ask past the free workers, and the round ends (deleting
// every session) when running agents fill the pool.
func TestClusterFull(t *testing.T) {
	w := testWorld()
	w.Slots = 4
	w.Join("p", "Pat", "blue")
	_ = w.Play()
	n := 0
	for range 2000 {
		for _, in := range w.Tick(0.1) {
			if in.Kind == "spawn" {
				n++
				if w.Running()+w.Spawning() > w.Slots {
					t.Fatalf("asked for %d sessions with %d slots", w.Running()+w.Spawning(), w.Slots)
				}
				w.Resolve(Result{Kind: "spawn", ID: in.ID, Session: "s-" + in.ID, OK: true})
			}
		}
		if w.Round.Phase == Over {
			break
		}
	}
	if w.Round.Phase != Over || w.Round.Reason != "full" || w.Round.Peak != 4 {
		t.Fatalf("round = %+v after %d spawns", w.Round, n)
	}
	if w.Best <= 0 || len(w.Agents) != 0 {
		t.Fatalf("best=%v agents=%d", w.Best, len(w.Agents))
	}
}

func TestRoundEndDeletesEverything(t *testing.T) {
	w := testWorld()
	w.Join("p", "Pat", "blue")
	_ = w.Play()
	addAgent(w, "a1", Vec{10, 10})
	sus := addAgent(w, "a2", Vec{20, 10})
	sus.State = Suspended
	w.Stored["a3"] = "s-a3"
	w.spawning["a4"] = pending{role: "coder", round: w.Round.N}
	w.end("full")
	got := w.Tick(0)
	if k := countKinds(got); k["delete"] != 3 {
		t.Fatalf("deletes = %+v", got)
	}
	// The spawn in flight is deleted when it lands, and never enters the world.
	w.Resolve(Result{Kind: "spawn", ID: "a4", Session: "s-a4", OK: true})
	got = w.Tick(0)
	if len(got) != 1 || got[0].Kind != "delete" || got[0].Session != "s-a4" || len(w.Agents) != 0 {
		t.Fatalf("late spawn: intents %+v agents %d", got, len(w.Agents))
	}
	// Late results for deleted agents change nothing.
	w.Resolve(Result{Kind: "suspend", ID: "a1", OK: true})
	w.Resolve(Result{Kind: "task", ID: "a1", OK: true, Say: "x"})
	if w.Round.Suspended != 0 {
		t.Fatal("a late suspend scored")
	}
}

func TestPlayAndNextRound(t *testing.T) {
	w := testWorld()
	w.Join("p", "Pat", "blue")
	if err := w.Play(); err != nil || w.Round.Phase != Live || w.Round.N != 1 {
		t.Fatalf("play from idle: %v %+v", err, w.Round)
	}
	w.end("full")
	if err := w.Play(); err != ErrTooSoon {
		t.Fatalf("play right after the end = %v", err)
	}
	w.Tick(overMin)
	if err := w.Play(); err != nil || w.Round.N != 2 {
		t.Fatalf("play after overMin: %v %+v", err, w.Round)
	}
	w.end("full")
	for range int(nextRound/0.5) + 1 {
		w.Tick(0.5)
	}
	if w.Round.N != 3 || w.Round.Phase != Live {
		t.Fatalf("no automatic next round: %+v", w.Round)
	}
	// With nobody online, no automatic round, and a live one is abandoned.
	w.Leave("p")
	for range int(abandonAfter/0.5) + 2 {
		w.Tick(0.5)
	}
	if w.Round.Phase != Over || w.Round.Reason != "abandoned" {
		t.Fatalf("round with nobody = %+v", w.Round)
	}
	for range 100 {
		w.Tick(0.5)
	}
	if w.Round.Phase != Over || w.Round.N != 3 {
		t.Fatalf("a round started with nobody online: %+v", w.Round)
	}
}

func TestBulletsSkipSuspendingAgents(t *testing.T) {
	w := testWorld()
	s := w.Join("p", "Pat", "blue")
	_ = w.Play()
	s.Pos, s.Heading = Vec{40, 50}, 0
	a := addAgent(w, "a1", Vec{50, 50})
	a.State, a.Hits = Suspending, 2
	a.Arrived, a.orbitR, a.orbitA = true, 10, math.Pi
	w.Input("p", Input{Fire: true})
	for range 15 {
		w.Tick(1.0 / 15)
	}
	if a.Hits != 2 {
		t.Fatalf("hits on a suspending agent: %d", a.Hits)
	}
}
