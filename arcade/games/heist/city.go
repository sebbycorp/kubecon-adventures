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

// Move advances from along dir (normalized, so diagonals are no faster) for dt seconds
// at speed tiles/second. Each axis is resolved separately so walls are slid along.
func (c *City) Move(from, dir Vec, speed, dt float64) Vec {
	n := math.Hypot(dir.X, dir.Y)
	if n == 0 {
		return from
	}
	step := speed * dt / n
	p := from
	if q := (Vec{p.X + dir.X*step, p.Y}); c.free(q) {
		p = q
	}
	if q := (Vec{p.X, p.Y + dir.Y*step}); c.free(q) {
		p = q
	}
	return p
}

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
