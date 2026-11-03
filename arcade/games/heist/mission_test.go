package heist

import (
	"errors"
	"fmt"
	"math/rand/v2"
	"regexp"
	"testing"
)

var errBoom = errors.New("boom")

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

// start starts a run, tops it up and lets all its agents spawn.
func (x *mtest) start(squad string) *Mission {
	x.t.Helper()
	ms, err := x.m.Start(squad, x.w, nil)
	if err != nil {
		x.t.Fatalf("Start(%s): %v", squad, err)
	}
	x.m.Tick(x.w, x.s)
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

func TestRunSpawnsNearItsPlayers(t *testing.T) {
	x := newMTest(t, 24)
	ms := x.start("red") // a and b online: 3 agents
	if len(ms.Targets) != 3 || ms.Ends-ms.Started != runSeconds || ms.Outcome != "" {
		t.Fatalf("run = %+v", ms)
	}
	for i := range ms.Targets {
		tg := x.target(ms, i)
		d := dist(tg.Pos, Vec{1.5, 1.5})
		if tg.State != Working || tg.Squad != "red" || tg.MissionID != ms.ID || tg.Steps != runSteps ||
			!x.w.City.Walkable(tg.Pos) || d < nearMin || d > nearMax+1 {
			t.Fatalf("agent %d = %+v (%.1f tiles away)", i, tg, d)
		}
	}
	blue := x.start("blue") // c alone: 2 agents
	if len(blue.Targets) != 2 {
		t.Fatalf("blue agents = %d", len(blue.Targets))
	}
	// An offline player doesn't count.
	x.w.Avatars["b"].Online = false
	if n := x.m.want(ms, x.w); n != 2 {
		t.Fatalf("want with b offline = %d", n)
	}
	if _, err := x.m.Start("red", x.w, nil); err == nil {
		t.Fatal("second run for one squad")
	}
}

func TestRunWantCapped(t *testing.T) {
	w := NewWorld(NewCity(nil))
	for i := range 6 {
		id := fmt.Sprint("p", i)
		w.Avatars[id] = NewAvatar(id, id, "", "big", Vec{1.5, 1.5})
	}
	m := NewMissions(rand.New(rand.NewPCG(1, 2)))
	ms, _ := m.Start("big", w, nil)
	if n := m.want(ms, w); n != runTargetsMax {
		t.Fatalf("want = %d", n)
	}
}

func TestStepPacingAndEscape(t *testing.T) {
	x := newMTest(t, 24)
	x.w.Avatars["b"].Online = false
	ms := x.start("red")
	tg := x.target(ms, 0)
	if x.tickUntil(1, func() bool { return x.stepsInFlight() > 0 }) < 0 {
		t.Fatal("first step not submitted")
	}
	// Both agents step; release the first agent's step only.
	for x.stepsInFlight() < 2 {
		x.tickUntil(0.5, func() bool { return false })
	}
	x.pump("step")
	stepped := tg
	if tg.Step != 1 {
		stepped = x.target(ms, 1)
	}
	if stepped.Step != 1 || stepped.Say != "hi" {
		t.Fatalf("after one step: %+v", stepped)
	}
	// The next step is 10-14 s after the last one started.
	got := x.tickUntil(stepMin+stepJitter+1, func() bool { return x.stepsInFlight() > 1 })
	if got < stepMin-0.5 || got > stepMin+stepJitter+0.5 {
		t.Fatalf("next step after %v s", got)
	}
	// The last step lets it escape; the run goes on and refills.
	stepped.Step = stepped.Steps - 1
	x.route(Result{Kind: "step", Target: stepped.ID, OK: true, Step: StepResult{Say: "bye"}})
	if stepped.State != Escaped || ms.Outcome != "" || countKind(x.w.Events, "escaped") != 1 {
		t.Fatalf("escape: %+v outcome %q", stepped, ms.Outcome)
	}
	n := len(ms.Targets)
	x.m.Tick(x.w, x.s)
	if len(ms.Targets) != n+1 {
		t.Fatalf("no replacement after escape: %d agents", len(ms.Targets))
	}
}

func TestFreezeAndBookScore(t *testing.T) {
	x := newMTest(t, 24)
	ms := x.start("red")
	tg := x.target(ms, 0)
	x.freeze(tg, "a", "b")
	if tg.State != Frozen || ms.Score != ptsFreeze || ms.Scores["a"] != ptsFreeze || ms.Scores["b"] != 0 {
		t.Fatalf("after freeze: %+v scores %v", tg, ms.Scores)
	}
	if r := x.row("a"); r.Best != 1 || r.Freezes != 1 {
		t.Fatalf("row a = %+v", r)
	}
	x.op("extract", tg, "b")
	x.pump("extract")
	if tg.State != Booked || tg.Say != "booked!" || ms.Score != 3 || ms.Scores["b"] != ptsBook {
		t.Fatalf("after book: %+v score %d %v", tg, ms.Score, ms.Scores)
	}
	if r := x.row("b"); r.Best != 2 || r.Books != 1 || r.Freezes != 0 {
		t.Fatalf("row b = %+v", r)
	}
	// A failed freeze scores nothing.
	t2 := x.target(ms, 1)
	x.op("freeze", t2, "a")
	x.f.err = errBoom
	x.pump("freeze")
	if ms.Score != 3 || t2.State != Working {
		t.Fatalf("failed freeze: score %d %+v", ms.Score, t2)
	}
}

func TestPointsGoToTheShootersRun(t *testing.T) {
	x := newMTest(t, 24)
	red := x.start("red")
	tg := x.target(red, 0)
	// c (blue) has no run: the freeze counts, but no run score or best.
	x.freeze(tg, "c")
	if r := x.row("c"); r.Freezes != 1 || r.Best != 0 || red.Score != 0 {
		t.Fatalf("no-run freeze: %+v red %d", r, red.Score)
	}
	blue := x.start("blue")
	x.freeze(x.target(red, 1), "c")
	if blue.Score != 1 || red.Score != 0 || x.row("c").Best != 1 {
		t.Fatalf("cross-run freeze: blue %d red %d", blue.Score, red.Score)
	}
}

func TestUnbookedFrozenDeleted(t *testing.T) {
	x := newMTest(t, 24)
	ms := x.start("red")
	tg := x.target(ms, 0)
	x.freeze(tg, "a")
	if x.tickUntil(frozenTTL-1, func() bool { return tg.Busy }) >= 0 {
		t.Fatal("deleted too early")
	}
	if x.tickUntil(1.5, func() bool { return tg.Busy && tg.opKind == "delete" }) < 0 {
		t.Fatal("frozen agent not deleted")
	}
	x.pump("delete")
	if tg.State != Deleted || ms.Score != 1 {
		t.Fatalf("after auto delete: %+v score %d", tg, ms.Score)
	}
}

func TestRunEndsAfterThreeMinutes(t *testing.T) {
	x := newMTest(t, 24)
	ms := x.start("red")
	if x.tickUntil(runSeconds-1, func() bool { return ms.Outcome != "" }) >= 0 {
		t.Fatal("ended early")
	}
	x.tickUntil(1.5, func() bool { return ms.Outcome != "" })
	if ms.Outcome != "done" || x.m.active["red"] != nil || countKind(x.w.Events, "run-done") != 1 {
		t.Fatalf("after 180 s: %+v", ms)
	}
	if x.m.lastRun("red") != ms {
		t.Fatal("lastRun lost the ended run")
	}
	next, err := x.m.Start("red", x.w, nil)
	if err != nil || x.m.lastRun("red") != next {
		t.Fatalf("play again: %v", err)
	}
	// Points after the end don't count.
	before := ms.Score
	x.m.score(x.m.player("a", x.w), 5, x.w)
	if ms.Score != before {
		t.Fatal("ended run scored")
	}
}

func TestCityCapSharesAgentsFairly(t *testing.T) {
	w := NewWorld(NewCity(nil))
	f := newFakeOps()
	s := NewScheduler(f, 24, 8)
	t.Cleanup(s.Close)
	m := NewMissions(rand.New(rand.NewPCG(1, 2)))
	var runs []*Mission
	for i := range 20 {
		id := fmt.Sprint("p", i)
		w.Avatars[id] = NewAvatar(id, id, "", "solo-"+id, Vec{1.5, 1.5})
		ms, _ := m.Start("solo-"+id, w, nil)
		runs = append(runs, ms)
	}
	m.Tick(w, s)
	if n := m.live(w); n != maxLiveTargets {
		t.Fatalf("live = %d", n)
	}
	for _, ms := range runs {
		if len(ms.Targets) < 1 || len(ms.Targets) > 2 {
			t.Fatalf("run %s has %d agents", ms.ID, len(ms.Targets))
		}
	}
	m.Tick(w, s)
	if n := m.live(w); n != maxLiveTargets {
		t.Fatalf("live after a second tick = %d", n)
	}
}

func TestLeaderboardRanksBestRun(t *testing.T) {
	x := newMTest(t, 24)
	x.m.players["a"] = &ScoreRow{PlayerID: "a", Nick: "ann", Best: 3, Freezes: 9}
	x.m.players["b"] = &ScoreRow{PlayerID: "b", Nick: "bob", Best: 5, Freezes: 5}
	x.m.players["c"] = &ScoreRow{PlayerID: "c", Nick: "cat", Best: 3, Freezes: 12}
	var got []string
	for _, r := range x.m.Leaderboard() {
		got = append(got, r.PlayerID)
	}
	if fmt.Sprint(got) != "[b c a]" {
		t.Fatalf("order = %v", got)
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

// finalStepUnderFreeze: an agent on its last step has a freeze in flight when its
// final step completes.
func finalStepUnderFreeze(t *testing.T) (*mtest, *Mission, *Target) {
	x := newMTest(t, 24)
	ms := x.start("red")
	tg := x.target(ms, 0)
	tg.Step = 3
	tg.HitBy = map[string]bool{"a": true}
	x.w.start(tg, "freeze", "a")
	x.route(Result{Kind: "step", Target: tg.ID, OK: true, Step: StepResult{Say: "got away"}})
	if tg.State != Working || !tg.escapePending {
		t.Fatalf("while the freeze is pending: state %s pending %v", tg.State, tg.escapePending)
	}
	return x, ms, tg
}

func TestFreezePendingBeatsFinalStep(t *testing.T) {
	x, ms, tg := finalStepUnderFreeze(t)
	x.route(Result{Kind: "freeze", Target: tg.ID, OK: true, Ms: 20})
	if tg.State != Frozen || tg.escapePending || ms.Score != 1 {
		t.Fatalf("freeze won: state %s pending %v score %d", tg.State, tg.escapePending, ms.Score)
	}
	if countKind(x.w.Events, "escaped") != 0 {
		t.Fatalf("escaped event after the freeze won: %+v", x.w.Events)
	}
}

func TestFreezePendingFailsThenEscapes(t *testing.T) {
	x, ms, tg := finalStepUnderFreeze(t)
	x.route(Result{Kind: "freeze", Target: tg.ID, OK: false, Err: "boom"})
	if tg.State != Escaped || ms.Outcome != "" || countKind(x.w.Events, "escaped") != 1 {
		t.Fatalf("freeze failed: state %s outcome %q", tg.State, ms.Outcome)
	}
}

func TestLateFreezeNeverRevives(t *testing.T) {
	for _, st := range []TargetState{Escaped, Booked, Deleted} {
		w := testWorld([]string{"red"}, Vec{1.5, 1.5})
		tg := addTarget(w, "t1", "red", st, Vec{3, 3})
		w.start(tg, "freeze", "a")
		w.Resolve("t1", "freeze", true, 10)
		if tg.State != st || tg.Busy {
			t.Fatalf("%s: late freeze left state %s busy %v", st, tg.State, tg.Busy)
		}
		for _, e := range w.Events {
			if e.Kind == "frozen" {
				t.Fatalf("%s: frozen event for a gone target", st)
			}
		}
	}
}
