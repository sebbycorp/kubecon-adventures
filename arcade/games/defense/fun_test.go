package defense

import (
	"math"
	"testing"
)

// live is a world with one online ship in a live round, the wave queue emptied so tests
// control every spawn.
func live(t *testing.T) (*World, *Ship) {
	t.Helper()
	w := testWorld()
	s := w.Join("p", "Pat", "blue")
	if err := w.Play(); err != nil {
		t.Fatal(err)
	}
	w.Round.queue = nil
	w.Round.queuedAt = 1e9 // no next wave on its own
	w.Tick(0)
	return w, s
}

// confirm suspends a by player and confirms it.
func confirm(w *World, a *Agent, player string) {
	a.Hits = a.Need
	w.suspend(a, player)
	w.Resolve(Result{Kind: "suspend", ID: a.ID, OK: true, Ms: 30})
}

func lastEvent(w *World, kind string) *Event {
	for i := len(w.Events) - 1; i >= 0; i-- {
		if w.Events[i].Kind == kind {
			return &w.Events[i]
		}
	}
	return nil
}

func TestComboBuildsAndDecays(t *testing.T) {
	w, s := live(t)
	for i, want := range []int{1, 2, 3, 4, 5, 5} {
		a := addAgent(w, string(rune('a'+i)), Vec{10, 10})
		confirm(w, a, "p")
		if s.Combo != want {
			t.Fatalf("suspend %d: combo %d, want %d", i+1, s.Combo, want)
		}
		w.Tick(1.5)
	}
	if s.Score != 1+2+3+4+5+5 {
		t.Fatalf("score = %d", s.Score)
	}
	if e := lastEvent(w, "suspended"); e == nil || e.Combo != 5 || e.Points != 5 {
		t.Fatalf("event = %+v", e)
	}
	w.Tick(comboWindow + 0.1)
	if s.Combo != 1 {
		t.Fatalf("combo did not decay: %d", s.Combo)
	}
}

func TestKindsNeedTheirHits(t *testing.T) {
	w, _ := live(t)
	for kind, k := range kinds {
		a := addAgent(w, kind, Vec{10, 10})
		a.Kind, a.Need = kind, k.Hits
		for range k.Hits - 1 {
			w.hit(a, "p", 1)
		}
		if a.State != Running {
			t.Fatalf("%s suspended after %d hits", kind, k.Hits-1)
		}
		w.hit(a, "p", 1)
		if a.State != Suspending {
			t.Fatalf("%s not suspending after %d hits", kind, k.Hits)
		}
	}
}

func TestOrchestratorSpawnsKidsAndEarlyBonus(t *testing.T) {
	w, s := live(t)
	o := addAgent(w, "o", Vec{10, 10})
	o.Kind, o.Need, o.Arrived = "orchestrator", 6, true
	w.bucket = spawnBurst
	got := countKinds(w.Tick(0.01))
	if got["spawn"] < 2 || got["spawn"] > 3 || !o.KidsDone {
		t.Fatalf("orchestrator kids: %v, done %v", got, o.KidsDone)
	}
	if w.Spawning() != got["spawn"] {
		t.Fatalf("kids not counted as spawning: %d", w.Spawning())
	}
	// One caught before its kids is worth the early bonus.
	e := addAgent(w, "e", Vec{20, 10})
	e.Kind, e.Need = "orchestrator", 6
	confirm(w, e, "p")
	if s.Score != kinds["orchestrator"].Points+earlyBonus {
		t.Fatalf("early orchestrator scored %d", s.Score)
	}
	if ev := lastEvent(w, "suspended"); ev.Text != "early" {
		t.Fatalf("event text %q", ev.Text)
	}
}

func TestKidsRespectRoom(t *testing.T) {
	w, _ := live(t)
	w.Slots = 3
	o := addAgent(w, "o", Vec{10, 10})
	o.Kind, o.Need, o.Arrived = "orchestrator", 6, true
	addAgent(w, "x", Vec{20, 20})
	w.bucket = spawnBurst
	if got := countKinds(w.Tick(0.01)); got["spawn"] != 1 {
		t.Fatalf("spawns with 1 free worker: %v", got)
	}
	// And the spawn budget: an empty bucket spawns nothing.
	w2, _ := live(t)
	b := addAgent(w2, "b", Vec{10, 10})
	b.Kind, b.Need, b.NextKids = "boss", 24, 0
	w2.bucket = 0
	if got := countKinds(w2.Tick(0.01)); got["spawn"] != 0 {
		t.Fatalf("spawned with no budget: %v", got)
	}
}

func TestPowerups(t *testing.T) {
	w, s := live(t)
	s.Pos = Vec{50, 50}
	w.Powerups["r"] = &Powerup{ID: "r", Kind: "rapid", Pos: s.Pos, Expires: w.Now + 5}
	w.Powerups["sp"] = &Powerup{ID: "sp", Kind: "spread", Pos: s.Pos, Expires: w.Now + 5}
	w.Tick(0.01)
	if s.RapidUntil <= w.Now || s.SpreadUntil <= w.Now || len(w.Powerups) != 0 {
		t.Fatalf("pickup: %+v left %d", s, len(w.Powerups))
	}
	s.FireCD, s.Heading = 0, 0
	w.Input("p", Input{Fire: true})
	w.Tick(0.01)
	if len(w.Bullets) != 3 || s.FireCD > rapidCD+1e-9 {
		t.Fatalf("spread+rapid: %d bullets, cd %v", len(w.Bullets), s.FireCD)
	}
	// Snapshot Bomb: every running agent in range suspends at once; a boss takes 4 hits.
	w.Bullets = nil
	near1, near2, far := addAgent(w, "n1", Vec{60, 50}), addAgent(w, "n2", Vec{50, 60}), addAgent(w, "f", Vec{150, 90})
	boss := addAgent(w, "b", Vec{55, 55})
	boss.Kind, boss.Need = "boss", 24
	w.Powerups["bo"] = &Powerup{ID: "bo", Kind: "bomb", Pos: s.Pos, Expires: w.Now + 5}
	got := countKinds(w.Tick(0.01))
	if got["suspend"] != 2 || near1.State != Suspending || near2.State != Suspending || far.State != Running || boss.Hits != 4 {
		t.Fatalf("bomb: %v boss hits %d far %s", got, boss.Hits, far.State)
	}
	// Power-ups expire.
	w.Powerups["x"] = &Powerup{ID: "x", Kind: "shield", Pos: Vec{1, 1}, Expires: w.Now + 1}
	w.Tick(1.1)
	if len(w.Powerups) != 0 {
		t.Fatal("power-up never expired")
	}
}

func TestShieldRamsAndBumpStuns(t *testing.T) {
	w, s := live(t)
	a := addAgent(w, "a", Vec{50, 50})
	a.Arrived, a.orbitR, a.orbitA = true, 30, 3.14159 // ~ (50, 50), barely moving
	s.Pos, s.Vel = a.Pos.add(Vec{1, 0}), Vec{}
	w.Tick(0.01)
	if s.StunUntil <= w.Now || s.Vel.len() < 1 {
		t.Fatalf("no bump: %+v", s)
	}
	w.Input("p", Input{Fire: true})
	n := len(w.Bullets)
	w.Tick(0.01)
	if len(w.Bullets) != n {
		t.Fatal("a stunned ship fired")
	}
	s.ShieldUntil = w.Now + 5
	s.StunUntil = 0
	s.Pos, s.Vel = a.Pos, Vec{}
	w.Input("p", Input{})
	w.Tick(0.01)
	if a.Hits != 1 || s.StunUntil > w.Now {
		t.Fatalf("shield ram: hits %d stun %v", a.Hits, s.StunUntil)
	}
}

func TestSquadSaveAndTeamStreak(t *testing.T) {
	w, _ := live(t)
	for _, id := range []string{"q", "r"} {
		w.Join(id, id, "red")
	}
	for i, p := range []string{"p", "q", "r"} {
		confirm(w, addAgent(w, p+"x", Vec{10, float64(i)}), p)
		w.Tick(0.5)
	}
	if e := lastEvent(w, "squad"); e == nil || e.Text != "3" {
		t.Fatalf("no SQUAD SAVE: %+v", w.Events)
	}
	if w.TeamStreak != 3 {
		t.Fatalf("team streak %d", w.TeamStreak)
	}
	if w.Ships["q"].Score != 1+squadBonus {
		t.Fatalf("squad bonus: %d", w.Ships["q"].Score)
	}
	w.Tick(teamGap + 0.1)
	confirm(w, addAgent(w, "late", Vec{1, 1}), "p")
	if w.TeamStreak != 1 || w.BestStreak != 3 {
		t.Fatalf("streak %d best %d", w.TeamStreak, w.BestStreak)
	}
}

func TestWavesAndBoss(t *testing.T) {
	w := testWorld()
	w.Join("p", "Pat", "blue")
	_ = w.Play()
	if w.Round.Wave != 1 || w.Round.WaveName != "HELLO WORLD" || len(w.Round.queue) == 0 {
		t.Fatalf("wave 1: %+v", w.Round)
	}
	if e := lastEvent(w, "wave"); e == nil || e.Points != 1 {
		t.Fatalf("no wave banner event")
	}
	for n := 1; n <= 10; n++ {
		ks := w.waveKinds(n, 1)
		hasBoss := len(ks) > 0 && ks[0] == "boss"
		if hasBoss != (n%5 == 0) || (n%5 == 0) != (waveName(n)[:4] == "BOSS") {
			t.Fatalf("wave %d: boss %v name %q", n, hasBoss, waveName(n))
		}
	}
	if len(w.waveKinds(4, 16)) <= len(w.waveKinds(4, 1)) {
		t.Fatal("more players should mean bigger waves")
	}
	// Spawns wait for the banner, then go out within the budget; with the field clear the
	// next wave follows.
	got := countKinds(w.Tick(1))
	if got["spawn"] != 0 {
		t.Fatalf("spawned during the banner: %v", got)
	}
	spawned := 0
	for range 200 {
		for _, in := range w.Tick(0.1) {
			if in.Kind == "spawn" {
				spawned++
				w.Resolve(Result{Kind: "spawn", ID: in.ID, Session: "s" + in.ID, OK: true})
			}
		}
		for _, a := range w.Agents {
			if a.State == Running {
				confirm(w, a, "p")
			}
		}
	}
	if w.Round.Wave < 3 || spawned < 10 {
		t.Fatalf("waves did not advance: wave %d, %d spawned", w.Round.Wave, spawned)
	}
	// The budget: 20 s of play never spawns more than burst + rate x time.
	if limit := int(math.Floor(spawnBurst + spawnRate*21)); spawned > limit {
		t.Fatalf("%d spawns in 21 s, budget %d", spawned, limit)
	}
}

func TestAlarmAndSaved(t *testing.T) {
	w, _ := live(t)
	w.Slots = 10
	var as []*Agent
	for i := range 9 {
		as = append(as, addAgent(w, string(rune('a'+i)), Vec{10, 10}))
	}
	w.Tick(0.01)
	if lastEvent(w, "alarm") == nil {
		t.Fatal("no alarm at 90%")
	}
	for _, a := range as[:3] {
		confirm(w, a, "p")
	}
	w.Tick(0.01)
	if lastEvent(w, "saved") == nil {
		t.Fatal("no SAVED IT at 60%")
	}
}

func TestTitles(t *testing.T) {
	cases := map[int]string{0: "Rookie Admin", 14: "Rookie Admin", 15: "SRE", 50: "Platform Hero", 1000: "Substrate Legend"}
	for n, want := range cases {
		if got := Title(n); got != want {
			t.Errorf("Title(%d) = %q, want %q", n, got, want)
		}
	}
	w, s := live(t)
	s.Total = 14
	confirm(w, addAgent(w, "a", Vec{1, 1}), "p")
	if e := lastEvent(w, "title"); e == nil || e.Text != "SRE" {
		t.Fatal("no title unlock event")
	}
}

func TestRunawayFlees(t *testing.T) {
	w, s := live(t)
	a := addAgent(w, "r", Vec{60, 50})
	a.Kind, a.Role = "runaway", "deployer"
	s.Pos = Vec{55, 50}
	before := dist(a.Pos, s.Pos)
	w.Tick(0.2)
	if dist(a.Pos, s.Pos) <= before {
		t.Fatalf("runaway closed in: %v -> %v", before, dist(a.Pos, s.Pos))
	}
}

func TestArmourScales(t *testing.T) {
	w, _ := live(t)
	w.Round.Wave = 1
	if w.need("worker") != 2 || w.need("scout") != 1 || w.need("boss") != 24 {
		t.Fatalf("wave 1 solo: worker %d scout %d boss %d", w.need("worker"), w.need("scout"), w.need("boss"))
	}
	w.Round.Wave = 7
	for i := range 10 {
		w.Join(string(rune('A'+i)), "x", "red")
	}
	if w.need("worker") != 2+2+2 || w.need("boss") != 24*(1+0.25*10) {
		t.Fatalf("wave 7, 11 pilots: worker %d boss %d", w.need("worker"), w.need("boss"))
	}
}
