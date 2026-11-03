package flappy

import "math"

// Rules are the swim's physics and course, in world units (the world is 640 tall) and
// seconds. The phone plays by them (it fetches them from /flappy/api/rules) and the
// server checks reported scores against them, so both always agree.
type Rules struct {
	Height      float64 `json:"height"`      // world height
	Surface     float64 `json:"surface"`     // the water's surface: touching it ends the run
	Floor       float64 `json:"floor"`       // the sea floor: touching it ends the run
	Gravity     float64 `json:"gravity"`     // units/s²
	Flap        float64 `json:"flap"`        // upward speed a tap sets
	MaxFall     float64 `json:"maxFall"`     // terminal sinking speed
	Speed0      float64 `json:"speed0"`      // starting swim speed, units/s
	Accel       float64 `json:"accel"`       // swim speed gained per second
	MaxSpeed    float64 `json:"maxSpeed"`    // swim speed cap
	FirstGap    float64 `json:"firstGap"`    // distance to the first pillar pair
	Spacing     float64 `json:"spacing"`     // distance between pillar pairs
	PillarW     float64 `json:"pillarW"`     // pillar width
	Gap0        float64 `json:"gap0"`        // the first gaps' height
	GapMin      float64 `json:"gapMin"`      // the narrowest gap
	GapShrink   float64 `json:"gapShrink"`   // gap height lost per pillar passed
	Radius      float64 `json:"radius"`      // the octopus's hit circle
	Countdown   float64 `json:"countdown"`   // seconds between waking and swimming
	BubbleEvery int     `json:"bubbleEvery"` // a bonus bubble floats in about one gap in this many
}

// Default is the course every run swims.
var Default = Rules{
	Height: 640, Surface: 34, Floor: 590,
	Gravity: 1500, Flap: 430, MaxFall: 620,
	Speed0: 165, Accel: 2.4, MaxSpeed: 300,
	FirstGap: 420, Spacing: 250, PillarW: 72,
	Gap0: 205, GapMin: 148, GapShrink: 1.2,
	Radius: 21, Countdown: 3, BubbleEvery: 2,
}

// Distance is how far the octopus has swum t seconds after the countdown.
func (r Rules) Distance(t float64) float64 {
	if t <= 0 {
		return 0
	}
	tc := (r.MaxSpeed - r.Speed0) / r.Accel // when the speed caps
	if t <= tc {
		return r.Speed0*t + r.Accel*t*t/2
	}
	return r.Speed0*tc + r.Accel*tc*tc/2 + r.MaxSpeed*(t-tc)
}

// MaxGaps is the most pillar pairs a run that lasted elapsed seconds (from its start,
// countdown included) could have passed, with a little slack for clocks and lag.
func (r Rules) MaxGaps(elapsed float64) int {
	d := r.Distance(elapsed - r.Countdown + 1.5)
	if d < r.FirstGap {
		return 0
	}
	return int(math.Floor((d-r.FirstGap)/r.Spacing)) + 2
}

// Plausible reports whether gaps and bubbles could have been scored in elapsed seconds:
// every gap must have been reachable, and there is at most one bubble per gap (plus one).
func (r Rules) Plausible(gaps, bubbles int, elapsed float64) bool {
	return gaps >= 0 && bubbles >= 0 && gaps <= r.MaxGaps(elapsed) && bubbles <= gaps+1
}

// Score is a run's points: one per gap and five per bonus bubble.
func Score(gaps, bubbles int) int { return gaps + 5*bubbles }
