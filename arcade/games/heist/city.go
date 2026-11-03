// Package heist is Grand Theft Agent: a multiplayer top-down city game. This file is the
// pure-logic map: tiles, districts per cluster node, and collision-aware movement.
package heist

import (
	"math"
	"math/rand/v2"
	"slices"
)

// Vec is a position or direction in tile units.
type Vec struct{ X, Y float64 }

// Rect is an axis-aligned rectangle in tile units.
type Rect struct{ X, Y, W, H float64 }

// Contains reports whether p lies inside r.
func (r Rect) Contains(p Vec) bool {
	return p.X >= r.X && p.X < r.X+r.W && p.Y >= r.Y && p.Y < r.Y+r.H
}

// Tile bytes.
const (
	Road         = '.'
	Building     = '#'
	VaultTile    = 'V'
	DispatchTile = 'D'
)

// playerRadius is the collision radius in tiles.
const playerRadius = 0.3

// layout is the fixed 48x32 map: a road grid on a 6-tile pitch, the Vault in the
// bottom-right block and Dispatch in the centre.
var layout = []string{
	"................................................",
	".#####.#####.#####.#####.#####.#####.#####.####.",
	".#####.#####.#####.#####.#####.#####.#####.####.",
	".#####.#####.#####.#####.#####.#####.#####.####.",
	".#####.#####.#####.#####.#####.#####.#####.####.",
	".#####.#####.#####.#####.#####.#####.#####.####.",
	"................................................",
	".#####.#####.#####.#####.#####.#####.#####.####.",
	".#####.#####.#####.#####.#####.#####.#####.####.",
	".#####.#####.#####.#####.#####.#####.#####.####.",
	".#####.#####.#####.#####.#####.#####.#####.####.",
	".#####.#####.#####.#####.#####.#####.#####.####.",
	"................................................",
	".#####.#####.#####.#####.DDDDD.#####.#####.####.",
	".#####.#####.#####.#####.DDDDD.#####.#####.####.",
	".#####.#####.#####.#####.DDDDD.#####.#####.####.",
	".#####.#####.#####.#####.DDDDD.#####.#####.####.",
	".#####.#####.#####.#####.DDDDD.#####.#####.####.",
	"................................................",
	".#####.#####.#####.#####.#####.#####.#####.####.",
	".#####.#####.#####.#####.#####.#####.#####.####.",
	".#####.#####.#####.#####.#####.#####.#####.####.",
	".#####.#####.#####.#####.#####.#####.#####.####.",
	".#####.#####.#####.#####.#####.#####.#####.####.",
	"................................................",
	".#####.#####.#####.#####.#####.#####.#####.VVVV.",
	".#####.#####.#####.#####.#####.#####.#####.VVVV.",
	".#####.#####.#####.#####.#####.#####.#####.VVVV.",
	".#####.#####.#####.#####.#####.#####.#####.VVVV.",
	".#####.#####.#####.#####.#####.#####.#####.VVVV.",
	"................................................",
	"................................................",
}

// City is the tile map plus the named regions on it.
type City struct {
	W, H      int
	Tiles     []byte // row-major, W*H
	Districts map[string]Rect
	Vault     Rect
	Dispatch  Rect
}

// NewCity builds the map and splits its width evenly into one district per node,
// ordered by node name.
func NewCity(nodes []string) *City {
	c := &City{W: len(layout[0]), H: len(layout), Districts: map[string]Rect{}}
	for _, row := range layout {
		c.Tiles = append(c.Tiles, row...)
	}
	c.Vault = c.bounds(VaultTile)
	c.Dispatch = c.bounds(DispatchTile)
	names := slices.Sorted(slices.Values(nodes))
	names = slices.Compact(names)
	for i, n := range names {
		x0 := c.W * i / len(names)
		x1 := c.W * (i + 1) / len(names)
		c.Districts[n] = Rect{float64(x0), 0, float64(x1 - x0), float64(c.H)}
	}
	return c
}

// bounds is the bounding rectangle of all tiles equal to t.
func (c *City) bounds(t byte) Rect {
	x0, y0, x1, y1 := c.W, c.H, -1, -1
	for i, b := range c.Tiles {
		if b != t {
			continue
		}
		x, y := i%c.W, i/c.W
		x0, y0, x1, y1 = min(x0, x), min(y0, y), max(x1, x), max(y1, y)
	}
	return Rect{float64(x0), float64(y0), float64(x1 - x0 + 1), float64(y1 - y0 + 1)}
}

// Walkable reports whether the tile under p is open ground.
func (c *City) Walkable(p Vec) bool {
	x, y := int(math.Floor(p.X)), int(math.Floor(p.Y))
	if p.X < 0 || p.Y < 0 || x >= c.W || y >= c.H {
		return false
	}
	return c.Tiles[y*c.W+x] != Building
}

// free reports whether a player-sized box centred on p touches no wall.
func (c *City) free(p Vec) bool {
	for _, dx := range [2]float64{-playerRadius, playerRadius} {
		for _, dy := range [2]float64{-playerRadius, playerRadius} {
			if !c.Walkable(Vec{p.X + dx, p.Y + dy}) {
				return false
			}
		}
	}
	return true
}

// Movement tuning: sub-step length, corner-assist nudge per sub-step, and the longest
// single move (anything past it is a stalled client, not a real step).
const (
	subStep   = 0.1
	cornerAid = 0.15
	maxMove   = 64.0
)

// Move advances from along dir (normalized, so diagonals are no faster) for dt seconds
// at speed tiles/second. It walks in sub-steps of at most 0.1 tile and resolves each
// axis separately, so walls are slid along and reached flush. When one axis is blocked
// and the other is idle, the body is nudged toward the nearest open lane centre so it
// can turn into 1-tile side streets. Non-finite input is treated as no motion.
func (c *City) Move(from, dir Vec, speed, dt float64) Vec {
	n := math.Hypot(dir.X, dir.Y)
	dist := speed * dt
	if n == 0 || !finite(n) || !finite(dist) || dist <= 0 || !finite(from.X) || !finite(from.Y) {
		return from
	}
	dist = min(dist, maxMove)
	steps := int(math.Ceil(dist / subStep))
	sx, sy := dir.X/n*dist/float64(steps), dir.Y/n*dist/float64(steps)
	p := from
	for range steps {
		var bx, by bool
		p, bx = c.slide(p, Vec{sx, 0})
		p, by = c.slide(p, Vec{0, sy})
		if bx && sy == 0 {
			p = c.nudge(p, false, sx)
		}
		if by && sx == 0 {
			p = c.nudge(p, true, sy)
		}
	}
	return p
}

// slide moves p by d, or as far as it can before touching a wall; blocked reports a
// shortened move.
func (c *City) slide(p, d Vec) (Vec, bool) {
	if d == (Vec{}) {
		return p, false
	}
	if q := (Vec{p.X + d.X, p.Y + d.Y}); c.free(q) {
		return q, false
	}
	lo, hi := 0.0, 1.0 // bisect for the furthest free fraction of d
	for range 8 {
		mid := (lo + hi) / 2
		if c.free(Vec{p.X + d.X*mid, p.Y + d.Y*mid}) {
			lo = mid
		} else {
			hi = mid
		}
	}
	return Vec{p.X + d.X*lo, p.Y + d.Y*lo}, true
}

// nudge shifts p on the cross axis (x when vertical is true, else y) toward the nearest
// lane centre whose tile ahead, in the blocked direction ahead, is open.
func (c *City) nudge(p Vec, vertical bool, ahead float64) Vec {
	cross, along := p.Y, p.X
	if vertical {
		cross, along = p.X, p.Y
	}
	look := along + math.Copysign(playerRadius+0.5, ahead)
	best, bestD := 0.0, math.Inf(1)
	for _, ctr := range [3]float64{math.Floor(cross) - 0.5, math.Floor(cross) + 0.5, math.Floor(cross) + 1.5} {
		tile := Vec{look, ctr}
		if vertical {
			tile = Vec{ctr, look}
		}
		if d := math.Abs(ctr - cross); d < bestD && c.Walkable(tile) {
			best, bestD = ctr, d
		}
	}
	if bestD == 0 || bestD > 1 {
		return p
	}
	shift := math.Copysign(min(cornerAid, bestD), best-cross)
	q := Vec{p.X, p.Y + shift}
	if vertical {
		q = Vec{p.X + shift, p.Y}
	}
	if c.free(q) {
		return q
	}
	return p
}

func finite(f float64) bool { return !math.IsNaN(f) && !math.IsInf(f, 0) }

// DistrictOf is the district a node owns; unknown nodes get the Dispatch block.
func (c *City) DistrictOf(node string) Rect {
	if r, ok := c.Districts[node]; ok {
		return r
	}
	return c.Dispatch
}

// RandomStreet picks a random road position inside r (tile centre), falling back to the
// Dispatch block if r has no road.
func (c *City) RandomStreet(r Rect, rnd *rand.Rand) Vec {
	var roads []Vec
	for y := int(r.Y); y < int(r.Y+r.H) && y < c.H; y++ {
		for x := int(r.X); x < int(r.X+r.W) && x < c.W; x++ {
			if c.Tiles[y*c.W+x] == Road {
				roads = append(roads, Vec{float64(x) + 0.5, float64(y) + 0.5})
			}
		}
	}
	if len(roads) == 0 {
		return Vec{c.Dispatch.X + c.Dispatch.W/2, c.Dispatch.Y + c.Dispatch.H/2}
	}
	return roads[rnd.IntN(len(roads))]
}
