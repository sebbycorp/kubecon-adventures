package main

import (
	"cmp"
	"fmt"
	"math"
	"slices"
	"strings"

	"github.com/sebbycorp/kubecon-adventures/arcade/internal/platform"
)

// The arcade-wide leaderboard. Every game of ours scores on its own scale, so each best
// score is converted to arcade points, where par (a great run in that game) is worth
// 1000, and a player's arcade points are the sum over every game they played. The
// external AI agentgateway missions game is not counted.

// arcadePar is the score that is worth 1000 arcade points, by game path.
var arcadePar = map[string]float64{
	"/substrate-siege": 40,   // freezes +1, bookings +2, player freezes +2 in a 3-minute run
	"/cluster-defense": 300,  // a round's suspends with combos
	"/gateway-defense": 4000, // a run's points, about wave 20
	"/agent-flow":      5,    // tasks finished (and jailbreaks the guard blocked)
	"/duck-hunt":       6000, // a 75-second hunt with combos
	"/flappy":          40,   // gaps passed plus bubbles
}

// arcadePoints is score in arcade points for the game at path.
func arcadePoints(path string, score int) int {
	par := arcadePar[path]
	if par <= 0 || score <= 0 {
		return 0
	}
	return int(math.Round(1000 * float64(score) / par))
}

// overallBoard ranks players by their arcade points across games. Rows from different
// games are the same player when nick and colour match (case-insensitive nick). The
// detail names how many games they played and their best one.
func overallBoard(n int, games []GameBoard) []platform.BoardRow {
	type tally struct {
		row        platform.BoardRow
		games      int
		best       string
		bestPoints int
	}
	by := map[string]*tally{}
	for _, g := range games {
		if g.External {
			continue
		}
		seen := map[string]bool{} // a player counts once per game (their best row)
		for _, r := range g.Rows {
			key := strings.ToLower(strings.TrimSpace(r.Nick)) + "|" + r.Color
			if r.Nick == "" || seen[key] {
				continue
			}
			seen[key] = true
			pts := arcadePoints(g.Path, r.Score)
			t := by[key]
			if t == nil {
				t = &tally{row: platform.BoardRow{Nick: r.Nick, Color: r.Color}}
				by[key] = t
			}
			t.row.Score += pts
			t.games++
			if pts > t.bestPoints {
				t.best, t.bestPoints = g.Game, pts
			}
		}
	}
	rows := make([]platform.BoardRow, 0, len(by))
	for _, t := range by {
		if t.row.Score <= 0 {
			continue
		}
		plural := "s"
		if t.games == 1 {
			plural = ""
		}
		t.row.Detail = fmt.Sprintf("%d game%s · best: %s", t.games, plural, t.best)
		rows = append(rows, t.row)
	}
	slices.SortFunc(rows, func(a, b platform.BoardRow) int {
		return cmp.Or(cmp.Compare(b.Score, a.Score), strings.Compare(strings.ToLower(a.Nick), strings.ToLower(b.Nick)))
	})
	if len(rows) > n {
		rows = rows[:n]
	}
	return rows
}
