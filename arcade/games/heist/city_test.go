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
	// Road column x=6; building tiles start at x=7 on row 3.
	got := c.Move(Vec{6.5, 3.5}, Vec{1, 0}, 4, 1)
	if got.X >= 7-0.3+1e-9 {
		t.Fatalf("walked into building: %+v", got)
	}
	// Slide along the wall: east blocked, south continues.
	got = c.Move(Vec{6.5, 3.5}, Vec{1, 1}, 4, 0.25)
	if got.Y <= 3.5 || got.X > 6.7+1e-9 {
		t.Fatalf("no slide: %+v", got)
	}
}

func TestMoveNormalizesDiagonal(t *testing.T) {
	c := NewCity(nil)
	from := Vec{24.5, 12.5}
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
	// Road row y=6 crosses side street column x=12 (centre 12.5). Start 0.3 off its
	// centre line and push south into it.
	p := Vec{12.8, 6.5}
	for i := 0; i < 30; i++ {
		p = c.Move(p, Vec{0, 1}, 5, 1.0/30)
	}
	if p.Y < 7.5 || !c.free(p) {
		t.Fatalf("did not turn into the side street within 1s: %+v", p)
	}
}

func TestMoveReachesWall(t *testing.T) {
	c := NewCity(nil)
	// Building face at x=7; a 0.3-radius body should stop at 6.7, give or take 0.05.
	got := c.Move(Vec{6.5, 3.5}, Vec{1, 0}, 5, 1)
	if got.X < 7-0.35 || got.X > 7-0.3+1e-9 {
		t.Fatalf("stopped at %+v, want x in [6.65, 6.7]", got)
	}
	// A large dt must not tunnel through the block to the next street (x=12).
	if got := c.Move(Vec{6.5, 3.5}, Vec{1, 0}, 5, 1.2); got.X > 7-0.3+1e-9 {
		t.Fatalf("tunnelled: %+v", got)
	}
	from := Vec{24.5, 12.5}
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
