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
