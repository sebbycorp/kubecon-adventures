package heist

import (
	"math"
	"math/rand/v2"
	"testing"
)

func TestCityDistrictsSplitByNode(t *testing.T) {
	c := NewCity([]string{"kagent-worker2", "kagent-worker"})
	a, b := c.DistrictOf("kagent-worker"), c.DistrictOf("kagent-worker2")
	if a.X != 0 || a.X+a.W != b.X || b.X+b.W != float64(c.W) {
		t.Fatalf("districts do not tile the width: %+v %+v", a, b)
	}
	if c.DistrictOf("unknown") != c.Dispatch {
		t.Fatalf("unknown node should map to Dispatch")
	}
	rnd := rand.New(rand.NewPCG(1, 2))
	for i := 0; i < 50; i++ {
		if p := c.RandomStreet(a, rnd); !a.Contains(p) || !c.Walkable(p) {
			t.Fatalf("bad street point %+v", p)
		}
	}
}

func TestMoveStopsAtBuildings(t *testing.T) {
	c := NewCity(nil)
	// Road column band x=0..2; building tiles start at x=3 on row 5 (the middle of its block).
	got := c.Move(Vec{2.5, 5.5}, Vec{1, 0}, 4, 1)
	if got.X >= 3-0.3+1e-9 {
		t.Fatalf("walked into building: %+v", got)
	}
	// Slide along the wall: east blocked, south continues.
	got = c.Move(Vec{2.5, 5.5}, Vec{1, 1}, 4, 0.25)
	if got.Y <= 5.5 || got.X > 2.7+1e-9 {
		t.Fatalf("no slide: %+v", got)
	}
}

func TestMoveNormalizesDiagonal(t *testing.T) {
	c := NewCity(nil)
	from := Vec{25.5, 9.5} // the middle of a junction
	got := c.Move(from, Vec{1, 1}, 4, 0.25)
	if d := math.Hypot(got.X-from.X, got.Y-from.Y); math.Abs(d-1) > 1e-9 {
		t.Fatalf("diagonal moved %v tiles, want 1 (4 tiles/s for 0.25s)", d)
	}
}

func TestVaultAndDispatchReachable(t *testing.T) {
	c := NewCity(nil)
	start := Vec{c.Dispatch.X + 1, c.Dispatch.Y + 1}
	seen := map[[2]int]bool{{int(start.X), int(start.Y)}: true}
	q := [][2]int{{int(start.X), int(start.Y)}}
	for len(q) > 0 {
		p := q[0]
		q = q[1:]
		for _, d := range [][2]int{{1, 0}, {-1, 0}, {0, 1}, {0, -1}} {
			n := [2]int{p[0] + d[0], p[1] + d[1]}
			if !seen[n] && c.Walkable(Vec{float64(n[0]) + .5, float64(n[1]) + .5}) {
				seen[n] = true
				q = append(q, n)
			}
		}
	}
	if !seen[[2]int{int(c.Vault.X) + 1, int(c.Vault.Y) + 1}] {
		t.Fatal("vault unreachable from dispatch")
	}
}

func TestMoveTurnsCorner(t *testing.T) {
	c := NewCity(nil)
	// Road band y=0..2 crosses the side street x=8..10 (edge lane centre 10.5). Start
	// 0.3 off that lane's centre line, 0.5 tile before the street begins at y=3, and
	// push south into it.
	p := Vec{10.8, 2.5}
	for i := 0; i < 30; i++ {
		p = c.Move(p, Vec{0, 1}, 5, 1.0/30)
	}
	if p.Y < 3.5 || !c.free(p) {
		t.Fatalf("did not turn into the side street within 1s: %+v", p)
	}
}

func TestMoveReachesWall(t *testing.T) {
	c := NewCity(nil)
	// Building face at x=3; a 0.3-radius body should stop at 2.7, give or take 0.05.
	got := c.Move(Vec{2.5, 5.5}, Vec{1, 0}, 5, 1)
	if got.X < 3-0.35 || got.X > 3-0.3+1e-9 {
		t.Fatalf("stopped at %+v, want x in [2.65, 2.7]", got)
	}
	// A large dt must not tunnel through the block to the next street (x=8).
	if got := c.Move(Vec{2.5, 5.5}, Vec{1, 0}, 5, 1.2); got.X > 3-0.3+1e-9 {
		t.Fatalf("tunnelled: %+v", got)
	}
	from := Vec{25.5, 9.5}
	for _, bad := range []struct {
		dir       Vec
		speed, dt float64
	}{
		{Vec{math.NaN(), 0}, 5, 0.1},
		{Vec{1, math.Inf(1)}, 5, 0.1},
		{Vec{1, 0}, math.NaN(), 0.1},
		{Vec{1, 0}, 5, math.Inf(1)},
	} {
		if got := c.Move(from, bad.dir, bad.speed, bad.dt); got != from {
			t.Fatalf("non-finite input %+v moved to %+v", bad, got)
		}
	}
}

func TestCityLayout(t *testing.T) {
	c := NewCity(nil)
	if c.W != 88 || c.H != 60 || len(c.Tiles) != c.W*c.H {
		t.Fatalf("city is %dx%d (%d tiles), want 88x60", c.W, c.H, len(c.Tiles))
	}
	if c.Dispatch != (Rect{43, 11, 5, 5}) || c.Vault != (Rect{78, 50, 5, 5}) {
		t.Fatalf("dispatch %+v vault %+v", c.Dispatch, c.Vault)
	}
	// Downtown keeps the first city's grid: 3-tile roads between 5x5 blocks.
	for y := 3; y < riverY0-1; y++ {
		for x := 3; x < 35; x++ {
			want := byte(Building)
			if x%8 < 3 || y%8 < 3 {
				want = Road
			}
			if got := c.Tiles[y*c.W+x]; got != want {
				t.Fatalf("downtown tile (%d,%d) = %q, want %q", x, y, got, want)
			}
		}
	}
	// Every district has its terrain.
	count := map[byte]int{}
	for _, b := range c.Tiles {
		count[b]++
	}
	for _, t2 := range []byte{Road, Building, Water, Bridge, Plaza, Grass, Tree, Container, Feature, Alley, Under, Pillar} {
		if count[t2] == 0 {
			t.Fatalf("no %q tiles", t2)
		}
	}
	// The river is only crossed on bridges (three, plus the ring road on both sides).
	crossings := 0
	for x := 0; x < c.W; x++ {
		if c.Tiles[riverY0*c.W+x] == Bridge && (x == 0 || c.Tiles[riverY0*c.W+x-1] != Bridge) {
			crossings++
		}
		for y := riverY0; y <= riverY1; y++ {
			if b := c.Tiles[y*c.W+x]; b != Water && b != Bridge {
				t.Fatalf("river tile (%d,%d) = %q", x, y, b)
			}
		}
	}
	if crossings != 5 {
		t.Fatalf("river crossings = %d, want 5", crossings)
	}
}

// TestCityAllReachable: every walkable tile can be reached from Dispatch, so no agent
// or respawn lands in a pocket nobody can walk to.
func TestCityAllReachable(t *testing.T) {
	c := NewCity(nil)
	start := [2]int{int(c.Dispatch.X), int(c.Dispatch.Y)}
	seen := map[[2]int]bool{start: true}
	q := [][2]int{start}
	for len(q) > 0 {
		p := q[0]
		q = q[1:]
		for _, d := range [][2]int{{1, 0}, {-1, 0}, {0, 1}, {0, -1}} {
			n := [2]int{p[0] + d[0], p[1] + d[1]}
			if !seen[n] && c.Walkable(Vec{float64(n[0]) + .5, float64(n[1]) + .5}) {
				seen[n] = true
				q = append(q, n)
			}
		}
	}
	for i, b := range c.Tiles {
		if !blocksWalk(b) && !seen[[2]int{i % c.W, i / c.W}] {
			t.Fatalf("tile (%d,%d) %q is walkable but unreachable", i%c.W, i/c.W, b)
		}
	}
	for _, p := range c.openTiles() {
		if !c.Walkable(p) || !open(c.tile(p)) {
			t.Fatalf("open spot %+v is not open ground", p)
		}
	}
}

// TestTerrainBlocks: water stops feet but not shots; buildings, trees, containers,
// fountains and pillars stop both; bridges carry players over the river.
func TestTerrainBlocks(t *testing.T) {
	c := NewCity(nil)
	if c.Walkable(Vec{10.5, 29.5}) || !c.Walkable(Vec{17.5, 29.5}) {
		t.Fatal("river water walkable or bridge blocked")
	}
	// Across the river (water only) the shot is clear.
	if !c.LineOfSight(Vec{10.5, 27.5}, Vec{10.5, 32.5}) {
		t.Fatal("water blocked a shot")
	}
	// Through a downtown block the shot is blocked; along the road it is clear.
	if c.LineOfSight(Vec{1.5, 5.5}, Vec{9.5, 5.5}) || !c.LineOfSight(Vec{1.5, 9.5}, Vec{20.5, 9.5}) {
		t.Fatal("building did not block, or an open road did")
	}
	// Containers on the docks block shots across them.
	if c.LineOfSight(Vec{45.5, 34.5}, Vec{45.5, 38.5}) {
		t.Fatal("container row did not block")
	}
	// A pillar under the highway blocks; the open lane between the pillars does not.
	if c.LineOfSight(Vec{37.5, 20.5}, Vec{39.5, 20.5}) || !c.LineOfSight(Vec{36.5, 21.5}, Vec{60.5, 21.5}) {
		t.Fatal("pillar or highway lane wrong")
	}
	// A shooter standing at a doorway's edge (own tile) can still fire.
	if !c.LineOfSight(Vec{2.9, 9.5}, Vec{2.9, 9.5}) {
		t.Fatal("zero-length shot blocked")
	}
}
