// Package heist is Substrate Siege: a multiplayer top-down city game. This file is the
// pure-logic map: tiles, districts per cluster node, and collision-aware movement.
package heist

import (
	"maps"
	"math"
	"math/rand/v2"
	"slices"
	"sync"
)

// Vec is a position or direction in tile units.
type Vec struct{ X, Y float64 }

// Rect is an axis-aligned rectangle in tile units.
type Rect struct{ X, Y, W, H float64 }

// Contains reports whether p lies inside r.
func (r Rect) Contains(p Vec) bool {
	return p.X >= r.X && p.X < r.X+r.W && p.Y >= r.Y && p.Y < r.Y+r.H
}

// Tile bytes. Buildings, water, trees, containers, fountains and planters, and overpass
// pillars block walking; all of those except water also block shots (see blocksShot).
const (
	Road         = '.'
	Building     = '#'
	VaultTile    = 'V'
	DispatchTile = 'D'
	Water        = '~'
	Bridge       = '='
	Plaza        = 'p' // paving: the central plaza and the river promenades
	Grass        = 'g'
	Tree         = 't'
	Container    = 'c' // a shipping container on the docks
	Feature      = 'f' // a fountain or a planter on the plaza
	Alley        = 'a' // a one-tile lane between the alley quarter's buildings
	Under        = 'h' // ground under the elevated highway
	Pillar       = 'o' // an overpass pillar
)

// blocksWalk and blocksShot classify tiles. Water stops feet, not bullets.
func blocksWalk(t byte) bool {
	switch t {
	case Building, Water, Tree, Container, Feature, Pillar:
		return true
	}
	return false
}

func blocksShot(t byte) bool { return t != Water && blocksWalk(t) }

// open reports ground where an agent or a respawning player may be placed.
func open(t byte) bool {
	switch t {
	case Road, Plaza, Grass, Alley, Under:
		return true
	}
	return false
}

// playerRadius is the collision radius in tiles.
const playerRadius = 0.3

// The map is 88x60 tiles: a 3-tile ring road around six districts, with a river across
// the middle. North of it are downtown (3-tile roads between 5x5 blocks on an 8-tile
// pitch, as the first city was), the central plaza around Dispatch, and the alley
// quarter; the elevated highway crosses the plaza and the alleys. South of it are the
// riverside park (grass, trees and a pond) and the docks (shipping containers,
// warehouses and the Vault). Three bridges and the ring road cross the river.
const (
	mapW, mapH = 88, 60
	pitch      = 8 // downtown's road pitch
	riverY0    = 28
	riverY1    = 31 // inclusive
	overY0     = 20 // the highway's band (inclusive)
	overY1     = 22
)

// Region is a named district of the map, for landmarks and the minimap.
type Region struct {
	Name, Kind string
	R          Rect
}

var regions = []Region{
	{"DOWNTOWN", "downtown", Rect{3, 3, 32, 25}},
	{"CENTRAL PLAZA", "plaza", Rect{35, 3, 21, 25}},
	{"THE ALLEYS", "alleys", Rect{56, 3, 29, 25}},
	{"HIGHWAY 8", "overpass", Rect{35, overY0, 50, overY1 - overY0 + 1}},
	{"THE RIVER", "river", Rect{3, riverY0, 82, riverY1 - riverY0 + 1}},
	{"RIVERSIDE PARK", "park", Rect{3, 33, 37, 24}},
	{"THE DOCKS", "docks", Rect{40, 33, 45, 24}},
}

// bridges are the columns (inclusive) that cross the river inside the ring road: in
// line with a downtown road, the plaza's north-south axis and the alleys' main road.
var bridges = [][2]int{{16, 18}, {43, 45}, {68, 70}}

// layout is the fixed map.
var layout = buildLayout()

func buildLayout() []string {
	g := make([][]byte, mapH)
	for y := range mapH {
		g[y] = make([]byte, mapW)
		for x := range mapW {
			g[y][x] = baseTile(x, y)
		}
	}
	// Plaza: Dispatch in the middle, fountains (2x2) and planters (3x1) as cover.
	fill(g, 43, 11, 5, 5, DispatchTile)
	for _, f := range [][2]int{{38, 6}, {51, 6}, {38, 16}, {51, 16}} {
		fill(g, f[0], f[1], 2, 2, Feature)
	}
	for _, f := range [][2]int{{40, 9}, {48, 9}, {40, 18}, {48, 18}} {
		fill(g, f[0], f[1], 3, 1, Feature)
	}
	// The park's pond.
	for y := 33; y < 57; y++ {
		for x := 3; x < 40; x++ {
			if dx, dy := float64(x)+0.5-28, float64(y)+0.5-38; dx*dx/30+dy*dy/10 <= 1 {
				g[y][x] = Water
			}
		}
	}
	// Docks: five rows of five containers (5x2), two warehouses, and the Vault.
	for _, y0 := range []int{36, 40, 47} {
		for _, x0 := range []int{43, 50, 57, 64, 71} {
			fill(g, x0, y0, 5, 2, Container)
		}
	}
	fill(g, 43, 52, 8, 4, Building)
	fill(g, 54, 52, 8, 4, Building)
	fill(g, 78, 50, 5, 5, VaultTile)
	// The highway: open ground underneath, with pillars along both edges.
	for y := overY0; y <= overY1; y++ {
		for x := 35; x < mapW-3; x++ {
			g[y][x] = Under
			if (y == overY0 || y == overY1) && (x-35)%6 == 3 {
				g[y][x] = Pillar
			}
		}
	}
	enclose(g)
	rows := make([]string, mapH)
	for y := range g {
		rows[y] = string(g[y])
	}
	return rows
}

// baseTile is a tile before the landmarks are placed.
func baseTile(x, y int) byte {
	ring := x < 3 || y < 3 || x >= mapW-3 || y >= mapH-3
	river := y >= riverY0 && y <= riverY1
	switch {
	case river && (ring || slices.ContainsFunc(bridges, func(b [2]int) bool { return x >= b[0] && x <= b[1] })):
		return Bridge
	case ring:
		return Road
	case river:
		return Water
	case y == riverY0-1 || y == riverY1+1:
		return Plaza // the promenades along both banks
	case y < riverY0 && x < 35: // downtown
		if x%pitch < 3 || y%pitch < 3 {
			return Road
		}
		return Building
	case y < riverY0 && x < 56: // the plaza
		return Plaza
	case y < riverY0: // the alleys: 2x2 buildings, one-tile lanes, a main road each way
		if (y >= 12 && y <= 14) || (x >= 68 && x <= 70) {
			return Road
		}
		if (x-56)%3 == 2 || (y-3)%3 == 2 {
			return Alley
		}
		return Building
	case x < 40: // the park: grass, two paths, trees kept off the paths and the edges
		if y == 44 || x == 17 {
			return Plaza
		}
		if x > 4 && x < 38 && y > 34 && y < 55 && abs(y-44) > 1 && abs(x-17) > 1 && hash(x, y)%100 < 12 {
			return Tree
		}
		return Grass
	default: // the docks' yard
		return Road
	}
}

// enclose turns walkable ground that can't be reached from Dispatch into trees, so
// nothing ever spawns in a pocket nobody can walk to.
func enclose(g [][]byte) {
	var start [2]int
	for y := range g {
		for x := range g[y] {
			if g[y][x] == DispatchTile {
				start = [2]int{x, y}
			}
		}
	}
	seen := map[[2]int]bool{start: true}
	q := [][2]int{start}
	for len(q) > 0 {
		p := q[0]
		q = q[1:]
		for _, d := range [4][2]int{{1, 0}, {-1, 0}, {0, 1}, {0, -1}} {
			n := [2]int{p[0] + d[0], p[1] + d[1]}
			if n[0] >= 0 && n[1] >= 0 && n[0] < mapW && n[1] < mapH && !seen[n] && !blocksWalk(g[n[1]][n[0]]) {
				seen[n] = true
				q = append(q, n)
			}
		}
	}
	for y := range g {
		for x := range g[y] {
			if !blocksWalk(g[y][x]) && !seen[[2]int{x, y}] {
				g[y][x] = Tree
			}
		}
	}
}

func fill(g [][]byte, x0, y0, w, h int, t byte) {
	for y := y0; y < y0+h; y++ {
		for x := x0; x < x0+w; x++ {
			g[y][x] = t
		}
	}
}

func abs(v int) int { return max(v, -v) }

// hash is a fixed per-tile pseudo-random number (the park's trees).
func hash(x, y int) uint32 {
	h := uint32(x)*73856093 ^ uint32(y)*19349663
	h ^= h >> 13
	h *= 0x5bd1e995
	return h ^ h>>15
}

// City is the tile map plus the named regions on it.
type City struct {
	W, H      int
	Tiles     []byte // row-major, W*H
	Districts map[string]Rect
	Regions   []Region
	Vault     Rect
	Dispatch  Rect

	openOnce sync.Once
	spots    []Vec // centres of every open tile (openTiles)
}

// NewCity builds the map and splits its width evenly into one district per node,
// ordered by node name.
func NewCity(nodes []string) *City {
	c := &City{W: len(layout[0]), H: len(layout), Districts: map[string]Rect{}, Regions: regions}
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
	return !blocksWalk(c.Tiles[y*c.W+x])
}

// tile is the tile under p, or a building off the map.
func (c *City) tile(p Vec) byte {
	x, y := int(math.Floor(p.X)), int(math.Floor(p.Y))
	if p.X < 0 || p.Y < 0 || x >= c.W || y >= c.H {
		return Building
	}
	return c.Tiles[y*c.W+x]
}

// LineOfSight reports whether a shot from a to b clears every building, tree,
// container, fountain, planter and pillar (water doesn't stop it). The tiles a and b
// stand on don't count, so a shooter in a doorway can still fire out.
func (c *City) LineOfSight(a, b Vec) bool {
	d := dist(a, b)
	if d < eps {
		return true
	}
	at, bt := [2]int{int(math.Floor(a.X)), int(math.Floor(a.Y))}, [2]int{int(math.Floor(b.X)), int(math.Floor(b.Y))}
	n := int(math.Ceil(d / 0.2))
	for i := 1; i < n; i++ {
		f := float64(i) / float64(n)
		p := Vec{a.X + (b.X-a.X)*f, a.Y + (b.Y-a.Y)*f}
		if t := ([2]int{int(math.Floor(p.X)), int(math.Floor(p.Y))}); t == at || t == bt {
			continue
		}
		if blocksShot(c.tile(p)) {
			return false
		}
	}
	return true
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
			if open(c.Tiles[y*c.W+x]) {
				roads = append(roads, Vec{float64(x) + 0.5, float64(y) + 0.5})
			}
		}
	}
	if len(roads) == 0 {
		return Vec{c.Dispatch.X + c.Dispatch.W/2, c.Dispatch.Y + c.Dispatch.H/2}
	}
	return roads[rnd.IntN(len(roads))]
}

// hideoutMin is how far (tiles) from the Dispatch centre a target may spawn.
const hideoutMin = 8

// Hideout is a target's spawn point: a random street in the node's district, or in a
// random district when the node is unknown (a new kagent session reports none), never
// in the Dispatch block or within hideoutMin tiles of its centre, where players spawn.
func (c *City) Hideout(node string, rnd *rand.Rand) Vec {
	area, ok := c.Districts[node]
	if !ok {
		area = Rect{0, 0, float64(c.W), float64(c.H)}
		if names := slices.Sorted(maps.Keys(c.Districts)); len(names) > 0 {
			area = c.Districts[names[rnd.IntN(len(names))]]
		}
	}
	centre := Vec{c.Dispatch.X + c.Dispatch.W/2, c.Dispatch.Y + c.Dispatch.H/2}
	pick := func(r Rect) (Vec, bool) {
		var roads []Vec
		for y := max(int(r.Y), 0); y < int(r.Y+r.H) && y < c.H; y++ {
			for x := max(int(r.X), 0); x < int(r.X+r.W) && x < c.W; x++ {
				p := Vec{float64(x) + 0.5, float64(y) + 0.5}
				if open(c.Tiles[y*c.W+x]) && !c.Dispatch.Contains(p) && math.Hypot(p.X-centre.X, p.Y-centre.Y) >= hideoutMin {
					roads = append(roads, p)
				}
			}
		}
		if len(roads) == 0 {
			return Vec{}, false
		}
		return roads[rnd.IntN(len(roads))], true
	}
	if p, ok := pick(area); ok {
		return p
	}
	if p, ok := pick(Rect{0, 0, float64(c.W), float64(c.H)}); ok {
		return p
	}
	return c.RandomStreet(c.Dispatch, rnd)
}

// Spawn distances (tiles) from a player for Near.
const (
	nearMin = 6.0
	nearMax = 14.0
)

// Near is a target's spawn point close to a player at from: a random street between
// nearMin and nearMax tiles away, outside the Dispatch block. With no such street it
// falls back to Hideout.
func (c *City) Near(from Vec, node string, rnd *rand.Rand) Vec {
	var roads []Vec
	for y := max(int(from.Y-nearMax), 0); y <= int(from.Y+nearMax) && y < c.H; y++ {
		for x := max(int(from.X-nearMax), 0); x <= int(from.X+nearMax) && x < c.W; x++ {
			p := Vec{float64(x) + 0.5, float64(y) + 0.5}
			if d := dist(p, from); open(c.Tiles[y*c.W+x]) && !c.Dispatch.Contains(p) && d >= nearMin && d <= nearMax {
				roads = append(roads, p)
			}
		}
	}
	if len(roads) == 0 {
		return c.Hideout(node, rnd)
	}
	return roads[rnd.IntN(len(roads))]
}
