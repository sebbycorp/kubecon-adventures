package podpacker

import "math"

// Rules are the well and the scoring, shared by the phone (it fetches them from
// /pod-packer/api/rules) and the server, which checks every reported score against them.
type Rules struct {
	Cols          int       `json:"cols"`          // the well's width: a worker node's slots
	Rows          int       `json:"rows"`          // the well's height: the cluster's nodes
	Gravity       []float64 `json:"gravity"`       // seconds per row by level (1-based; the last repeats)
	SoftDrop      float64   `json:"softDrop"`      // seconds per row while soft dropping
	LockDelay     float64   `json:"lockDelay"`     // seconds a landed piece may still slide
	LinesPerLevel int       `json:"linesPerLevel"` // lines to go up a level
	Points        []int     `json:"points"`        // points for 0-4 lines at once, times the level
	BackToBack    float64   `json:"backToBack"`    // multiplier for a tetris right after a tetris
	Combo         int       `json:"combo"`         // points per combo step, times the level
	SoftPoint     int       `json:"softPoint"`     // points per row soft dropped
	HardPoint     int       `json:"hardPoint"`     // points per row hard dropped
	MaxPPS        float64   `json:"maxPps"`        // fastest plausible pieces a second
}

// Default is the game every run plays: guideline Tetris scoring and a gravity curve that
// gets fast by level 10.
var Default = Rules{
	Cols: 10, Rows: 20,
	Gravity:       []float64{0.8, 0.72, 0.63, 0.55, 0.47, 0.38, 0.3, 0.22, 0.13, 0.1, 0.083, 0.07, 0.058, 0.05, 0.042},
	SoftDrop:      0.03,
	LockDelay:     0.5,
	LinesPerLevel: 10,
	Points:        []int{0, 100, 300, 500, 800},
	BackToBack:    1.5,
	Combo:         50,
	SoftPoint:     1,
	HardPoint:     2,
	MaxPPS:        5,
}

// Level is the level after lines cleared (it starts at 1).
func (r Rules) Level(lines int) int { return 1 + max(0, lines)/r.LinesPerLevel }

// MaxScore is the most a run could score with lines cleared and pieces placed: every line
// at the back-to-back tetris rate, the longest possible combo, and every piece hard
// dropped the whole well, all at the highest level reached.
func (r Rules) MaxScore(lines, pieces int) int {
	l := float64(r.Level(lines))
	perLine := r.BackToBack * float64(r.Points[4]) / 4
	combo := float64(r.Combo) * float64(lines) * float64(lines+1) / 2
	drops := float64(pieces * r.Rows * r.HardPoint)
	return int(math.Ceil(l*(perLine*float64(lines)+combo) + drops))
}

// Plausible reports whether a run that lasted elapsed seconds could have scored score with
// lines (tetrises of them four at once) from pieces, when the server saw serverLines
// cleared through its clear calls.
func (r Rules) Plausible(score, lines, tetrises, pieces, serverLines int, elapsed float64) bool {
	switch {
	case score < 0 || lines < 0 || tetrises < 0 || pieces < 0:
		return false
	case tetrises*4 > lines:
		return false
	case lines*r.Cols > pieces*4: // every cleared cell came from a piece
		return false
	case float64(pieces) > elapsed*r.MaxPPS+3:
		return false
	case lines > serverLines+4: // the phone reports clears as they happen
		return false
	}
	return score <= r.MaxScore(lines, pieces)
}
