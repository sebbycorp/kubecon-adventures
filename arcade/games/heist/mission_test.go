package heist

import (
	"fmt"
	"math/rand/v2"
	"regexp"
	"testing"
)

// mtest is a world (avatars a, b in squad red; c in blue), a scheduler on fakeOps and
// seeded missions.
type mtest struct {
	t *testing.T
	w *World
	s *Scheduler
	f *fakeOps
	m *Missions
}

func newMTest(t *testing.T, maxSteps int) *mtest {
	t.Helper()
	w := testWorld([]string{"red", "red", "blue"}, Vec{1.5, 1.5}, Vec{1.5, 1.5}, Vec{1.5, 1.5})
	f := newFakeOps()
	s := NewScheduler(f, maxSteps, 8)
	t.Cleanup(s.Close)
	return &mtest{t, w, s, f, NewMissions(rand.New(rand.NewPCG(1, 2)))}
}

// route is what the game loop does with every Result.
func (x *mtest) route(r Result) {
	switch r.Kind {
	case "freeze", "extract", "delete", "spring":
		x.w.Resolve(r.Target, r.Kind, r.OK, r.Ms)
	}
	x.m.OnResult(r, x.w)
}

// pump releases the next started call of kind (others stay blocked), then routes its
// result.
func (x *mtest) pump(kind string) Result {
	x.t.Helper()
	for {
		c := x.f.next(x.t)
		if c.kind == kind {
			close(c.release)
			break
		}
	}
	r := result(x.t, x.s)
	x.route(r)
	return r
}

// start starts a mission and lets all its targets spawn.
func (x *mtest) start(squad string, level int) *Mission {
	x.t.Helper()
	ms, err := x.m.Start(squad, level, x.w, x.s)
	if err != nil {
		x.t.Fatalf("Start(%s, %d): %v", squad, level, err)
	}
	for range ms.Targets {
		x.pump("spawn")
	}
	return ms
}

func (x *mtest) target(ms *Mission, i int) *Target {
	x.t.Helper()
	tg := x.w.Targets[ms.Targets[i]]
	if tg == nil {
		x.t.Fatalf("target %s not in world", ms.Targets[i])
	}
	return tg
}

// tickUntil advances the clock in 0.5 s ticks until cond holds, up to limit seconds,
// and returns the elapsed time (or -1).
func (x *mtest) tickUntil(limit float64, cond func() bool) float64 {
	t0 := x.w.Now
	for x.w.Now-t0+0.5 <= limit+eps {
		x.w.Now += 0.5
		x.m.Tick(x.w, x.s)
		if cond() {
			return x.w.Now - t0
		}
	}
	return -1
}

func (x *mtest) stepsInFlight() int { a, _, q := x.s.InFlight(); return a + q }

// player op: what World does when a player's intent starts, then the scheduler call.
func (x *mtest) op(kind string, tg *Target, player string) {
	x.t.Helper()
	x.w.start(tg, kind, player)
	if !x.s.Submit(kind, tg.ID, tg.SessionID, 0, 0) {
		x.t.Fatalf("submit %s %s refused", kind, tg.ID)
	}
}

func (x *mtest) freeze(tg *Target, player string, hitters ...string) {
	x.t.Helper()
	tg.HitBy = map[string]bool{player: true}
	for _, h := range hitters {
		tg.HitBy[h] = true
	}
	x.op("freeze", tg, player)
	x.pump("freeze")
}

func (x *mtest) row(id string) ScoreRow {
	for _, r := range x.m.Leaderboard() {
		if r.PlayerID == id {
			return r
		}
	}
	return ScoreRow{}
}

func TestStepPacing(t *testing.T) {
	x := newMTest(t, 24)
	ms := x.start("red", 1)
	tg := x.target(ms, 0)
	if tg.State != Working || !tg.New || tg.Squad != "red" || tg.MissionID != ms.ID ||
		tg.SessionID != "sess-"+tg.ID || tg.Node != "node-1" || tg.Steps != 4 {
		t.Fatalf("spawned target = %+v", tg)
	}
	if !x.w.City.Walkable(tg.Pos) {
		t.Fatalf("spawned off the street at %v", tg.Pos)
	}
	for i := 1; i <= 2; i++ {
		d := x.tickUntil(40, func() bool { return x.stepsInFlight() > 0 })
		if d < 15 || d > 25 {
			t.Fatalf("step %d submitted after %.1f s, want 15-25", i, d)
		}
		x.tickUntil(30, func() bool { return false }) // in flight: no second step
		if n := x.stepsInFlight(); n != 1 {
			t.Fatalf("steps in flight = %d", n)
		}
		x.pump("step")
		if tg.Step != i || tg.Say != "hi" {
			t.Fatalf("after step %d: Step=%d Say=%q", i, tg.Step, tg.Say)
		}
		if ms.Stars != 1+i {
			t.Fatalf("stars = %d, want %d", ms.Stars, 1+i)
		}
		ms.Ends = x.w.Now + 1000 // keep the mission alive for the pacing check
	}
	for _, st := range []TargetState{Frozen, Dragged} {
		tg.State = st
		if x.tickUntil(40, func() bool { return x.stepsInFlight() > 0 }) >= 0 {
			t.Fatalf("step submitted while %s", st)
		}
	}
	tg.State, tg.Busy = Working, true
	if x.tickUntil(40, func() bool { return x.stepsInFlight() > 0 }) >= 0 {
		t.Fatal("step submitted while Busy")
	}
	tg.Busy = false
	if x.tickUntil(1, func() bool { return x.stepsInFlight() > 0 }) < 0 {
		t.Fatal("no step once Working and idle again")
	}
}

func TestProgressPausesWhenQueued(t *testing.T) {
	x := newMTest(t, 1)
	x.s.Submit("step", "other", "s", 1, 4) // the only step slot, never released
	ms := x.start("red", 1)
	tg := x.target(ms, 0)
	x.tickUntil(30, func() bool { _, _, q := x.s.InFlight(); return q == 1 })
	if _, _, q := x.s.InFlight(); q != 1 {
		t.Fatalf("queued = %d, want the target's step waiting", q)
	}
	x.tickUntil(100, func() bool { return ms.Outcome != "" })
	if tg.Step != 0 {
		t.Fatalf("Step = %d while its step waited in the queue", tg.Step)
	}
	if ms.Outcome != "failed" || x.w.Now < ms.Ends {
		t.Fatalf("outcome %q at %.1f (ends %.1f)", ms.Outcome, x.w.Now, ms.Ends)
	}
}

func TestEscapeFailsMission(t *testing.T) {
	x := newMTest(t, 24)
	ms := x.start("red", 1)
	tg := x.target(ms, 0)
	tg.Step = 3
	ms.Stars = 1
	if x.tickUntil(26, func() bool { return x.stepsInFlight() > 0 }) < 0 {
		t.Fatal("last step never submitted")
	}
	x.pump("step")
	if tg.State != Escaped || ms.Outcome != "escaped" || ms.Stars != 2 {
		t.Fatalf("state=%s outcome=%q stars=%d", tg.State, ms.Outcome, ms.Stars)
	}
	// No more steps once escaped.
	if x.tickUntil(40, func() bool { return x.stepsInFlight() > 0 }) >= 0 {
		t.Fatal("step submitted after escape")
	}
}

func TestLevel1WinOnFreeze(t *testing.T) {
	x := newMTest(t, 24)
	if _, err := x.m.Start("red", 2, x.w, x.s); err == nil {
		t.Fatal("level 2 allowed before level 1 was won")
	}
	if _, err := x.m.Start("red", 0, x.w, x.s); err == nil {
		t.Fatal("level 0 allowed")
	}
	ms := x.start("red", 1)
	if _, err := x.m.Start("red", 1, x.w, x.s); err == nil {
		t.Fatal("second active mission allowed")
	}
	x.w.Now += 10
	x.freeze(x.target(ms, 0), "a", "b")
	if ms.Outcome != "won" || ms.Cash != 100 {
		t.Fatalf("outcome %q cash %d, want won 100", ms.Outcome, ms.Cash)
	}
	a, b := x.row("a"), x.row("b")
	if a.Takedowns != 1 || b.Takedowns != 0 || a.Nick != "a" || a.Squad != "red" {
		t.Fatalf("rows a=%+v b=%+v", a, b)
	}
	if a.Cash != 50 || b.Cash != 50 || a.Level != 1 {
		t.Fatalf("cash split a=%+v b=%+v", a, b)
	}
	if _, err := x.m.Start("red", 3, x.w, x.s); err == nil {
		t.Fatal("level 3 allowed after only level 1")
	}
	if _, err := x.m.Start("red", 2, x.w, x.s); err != nil {
		t.Fatalf("level 2 after winning level 1: %v", err)
	}
}

func TestLevel2NeedsBooking(t *testing.T) {
	x := newMTest(t, 24)
	x.m.best["red"] = 1
	ms := x.start("red", 2)
	tg := x.target(ms, 0)
	x.freeze(tg, "a")
	if ms.Outcome != "" || ms.Cash != 50 {
		t.Fatalf("after freeze: outcome %q cash %d", ms.Outcome, ms.Cash)
	}
	tg.State = Dragged
	x.op("extract", tg, "a")
	x.pump("extract")
	if tg.State != Booked || ms.Outcome != "won" || ms.Cash != 700 {
		t.Fatalf("after booking: state %s outcome %q cash %d", tg.State, ms.Outcome, ms.Cash)
	}
	if r := x.row("a"); r.Cash != 700 || r.Level != 2 {
		t.Fatalf("row a = %+v", r)
	}
}

func TestCrewRevivesAfter30s(t *testing.T) {
	x := newMTest(t, 24)
	x.m.best["red"] = 2
	ms := x.start("red", 3)
	tg := x.target(ms, 0)
	x.freeze(tg, "a")
	resumes := func() bool { _, r, _ := x.s.InFlight(); return r > 0 }
	if x.tickUntil(29.5, resumes) >= 0 {
		t.Fatal("crew spring before 30 s")
	}
	if x.tickUntil(1, resumes) < 0 {
		t.Fatal("no crew spring after 30 s")
	}
	if !tg.Busy || tg.State != Frozen {
		t.Fatalf("springing target = %+v", tg)
	}
	x.pump("spring")
	if tg.State != Working || !tg.Restored || tg.Say != "sprung!" || tg.Busy {
		t.Fatalf("after crew spring: %+v", tg)
	}
	if ms.Cash != 50 || ms.Stars != 1 {
		t.Fatalf("crew spring changed cash %d / stars %d", ms.Cash, ms.Stars)
	}
	if x.row("a").Takedowns != 1 {
		t.Fatal("crew spring removed a takedown")
	}
}

func TestCrewNeedsAFreeMember(t *testing.T) {
	x := newMTest(t, 24)
	x.m.best["red"] = 2
	ms := x.start("red", 3)
	tg := x.target(ms, 0)
	x.freeze(tg, "a")
	x.target(ms, 1).State = Booked
	x.target(ms, 2).State = Frozen
	x.target(ms, 2).FrozenAt = 1e9 // not due yet itself
	if x.tickUntil(40, func() bool { _, r, _ := x.s.InFlight(); return r > 0 }) >= 0 {
		t.Fatal("crew spring with no free crew member")
	}
}

func TestFailedCrewSpringStaysFrozen(t *testing.T) {
	x := newMTest(t, 24)
	x.m.best["red"] = 2
	ms := x.start("red", 3)
	tg := x.target(ms, 0)
	x.freeze(tg, "a")
	x.tickUntil(31, func() bool { _, r, _ := x.s.InFlight(); return r > 0 })
	x.f.err = fmt.Errorf("resume failed")
	x.pump("spring")
	x.f.err = nil
	if tg.State != Frozen || tg.Busy {
		t.Fatalf("after failed spring: %+v", tg)
	}
}

func TestRivalSpringPenalty(t *testing.T) {
	x := newMTest(t, 24)
	x.m.best["red"] = 1
	ms := x.start("red", 2)
	tg := x.target(ms, 0)
	x.freeze(tg, "a")
	x.op("spring", tg, "c")
	x.pump("spring")
	if tg.State != Working || ms.Cash != 50-100 || ms.Stars != 2 {
		t.Fatalf("after rival spring: state %s cash %d stars %d", tg.State, ms.Cash, ms.Stars)
	}
}

func TestQueuedMissionWaits(t *testing.T) {
	x := newMTest(t, 24)
	for i := 0; i < 24; i++ {
		x.s.Submit("step", fmt.Sprintf("o%d", i), "s", 1, 4)
	}
	calls := x.f.drain(t, 24)
	waitInFlight(t, x.s, 24, 0, 0)
	x.w.Now = 5
	ms, err := x.m.Start("red", 1, x.w, x.s)
	if err != nil || !ms.Queued {
		t.Fatalf("Start = %+v, %v; want queued", ms, err)
	}
	x.tickUntil(5, func() bool { return false })
	x.f.noStart(t)
	if !ms.Queued || len(x.w.Targets) != 0 {
		t.Fatal("queued mission spawned while the step cap was full")
	}
	close(calls[0].release)
	x.route(result(t, x.s))
	x.tickUntil(1, func() bool { return !ms.Queued })
	if ms.Queued || ms.Started != x.w.Now || ms.Ends != x.w.Now+90 {
		t.Fatalf("after a free slot: %+v (now %.1f)", ms, x.w.Now)
	}
	x.pump("spawn")
	if len(x.w.Targets) != 1 {
		t.Fatalf("targets = %d", len(x.w.Targets))
	}
}

func TestLeaderboardRanksTakedowns(t *testing.T) {
	x := newMTest(t, 24)
	n := 0
	takedown := func(player string) *Target {
		n++
		tg := addTarget(x.w, fmt.Sprintf("x%d", n), "", Working, Vec{1.5, 1.5})
		tg.SessionID = tg.ID
		x.freeze(tg, player)
		return tg
	}
	first := takedown("a")
	takedown("a")
	takedown("a")
	takedown("b")
	takedown("b")
	for i := 0; i < 3; i++ {
		takedown("c")
	}
	// A failed freeze counts for nobody.
	miss := addTarget(x.w, "miss", "", Working, Vec{1.5, 1.5})
	x.w.start(miss, "freeze", "b")
	x.s.Submit("freeze", "miss", "miss", 0, 0)
	x.f.err = fmt.Errorf("nope")
	x.pump("freeze")
	x.f.err = nil
	// A crew spring keeps the takedown.
	x.op("spring", first, "")
	x.pump("spring")

	x.m.players["a"].Cash = 200
	x.m.players["b"].Cash = 900
	x.m.players["c"].Cash = 500
	lb := x.m.Leaderboard()
	var order []string
	for _, r := range lb {
		order = append(order, fmt.Sprintf("%s:%d", r.PlayerID, r.Takedowns))
	}
	if got := fmt.Sprint(order); got != "[c:3 a:3 b:2]" {
		t.Fatalf("leaderboard = %s", got)
	}
	if tot := x.m.SquadTotals(); tot["red"] != 5 || tot["blue"] != 3 {
		t.Fatalf("squad totals = %v", tot)
	}
}

func TestSquadJoin(t *testing.T) {
	q := NewSquads(rand.New(rand.NewPCG(3, 4)))
	code := q.Create("p1")
	if !regexp.MustCompile(`^[A-Z]{4}$`).MatchString(code) {
		t.Fatalf("code = %q", code)
	}
	for _, p := range []string{"p2", "p3", "p4"} {
		if err := q.Join(p, code); err != nil {
			t.Fatalf("join %s: %v", p, err)
		}
	}
	if err := q.Join("p5", code); err == nil {
		t.Fatal("fifth player joined")
	}
	bad := "ZZZZ"
	if code == bad {
		bad = "YYYY"
	}
	if err := q.Join("p5", bad); err == nil {
		t.Fatal("unknown code accepted")
	}
	if q.Of("p3") != code || q.Of("p1") != code {
		t.Fatalf("Of = %q, %q", q.Of("p3"), q.Of("p1"))
	}
	if s := q.Solo("p2"); s != "solo-p2" || q.Of("p2") != s {
		t.Fatalf("Solo = %q, Of = %q", s, q.Of("p2"))
	}
	if err := q.Join("p5", code); err != nil { // p2 left, so there is room
		t.Fatalf("join after a leave: %v", err)
	}
}
