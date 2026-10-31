// Package curve contains deterministic, hardware-independent fan demand rules.
package curve

import (
	"fmt"
	"math"
	"time"
)

type Point struct {
	Temperature float64 `json:"temperature"`
	Duty        int     `json:"duty"`
}
type Range struct {
	MinTemperature float64 `json:"min_temperature"`
	MaxTemperature float64 `json:"max_temperature"`
	FanSpeed       int     `json:"fan_speed"`
	Hysteresis     float64 `json:"hysteresis"`
}
type Curve struct {
	Type   string  `json:"type"`
	Points []Point `json:"points,omitempty"`
	Ranges []Range `json:"ranges,omitempty"`
}

func finite(v float64) bool { return !math.IsNaN(v) && !math.IsInf(v, 0) }
func Validate(c Curve) error {
	if c.Type != "step" && c.Type != "linear" {
		return fmt.Errorf("curve type must be step or linear")
	}
	if len(c.Ranges) > 0 {
		if c.Type != "step" || len(c.Points) > 0 || len(c.Ranges) > 64 {
			return fmt.Errorf("legacy ranges require step type, no points, and at most 64 ranges")
		}
		for i, r := range c.Ranges {
			if !finite(r.MinTemperature) || !finite(r.MaxTemperature) || r.MinTemperature < 0 || r.MaxTemperature > 200 || r.MinTemperature >= r.MaxTemperature || r.FanSpeed < 0 || r.FanSpeed > 100 || !finite(r.Hysteresis) || r.Hysteresis < 0 || r.Hysteresis > 20 {
				return fmt.Errorf("invalid range %d", i+1)
			}
			if i > 0 && (r.MinTemperature != c.Ranges[i-1].MaxTemperature || r.FanSpeed < c.Ranges[i-1].FanSpeed) {
				return fmt.Errorf("ranges must be contiguous, ordered and have nondecreasing duties")
			}
		}
		return nil
	}
	if len(c.Points) < 2 || len(c.Points) > 64 {
		return fmt.Errorf("a curve needs 2 to 64 points")
	}
	for i, p := range c.Points {
		if !finite(p.Temperature) || p.Temperature < 0 || p.Temperature > 200 || p.Duty < 0 || p.Duty > 100 {
			return fmt.Errorf("invalid point %d", i+1)
		}
		if i > 0 && (p.Temperature <= c.Points[i-1].Temperature || p.Duty < c.Points[i-1].Duty) {
			return fmt.Errorf("temperatures must increase and duties must not decrease")
		}
	}
	return nil
}
func MaxDuty(c Curve) int {
	if len(c.Ranges) > 0 {
		return c.Ranges[len(c.Ranges)-1].FanSpeed
	}
	if len(c.Points) > 0 {
		return c.Points[len(c.Points)-1].Duty
	}
	return 0
}

// Evaluate saturates endpoints. Step points use inclusive lower thresholds;
// imported ranges retain their original min < temperature <= max mapping.
func Evaluate(c Curve, t float64) int { d, _ := evaluate(c, t); return d }
func evaluate(c Curve, t float64) (int, int) {
	if len(c.Ranges) > 0 {
		for i, r := range c.Ranges {
			if t <= r.MaxTemperature {
				return r.FanSpeed, i
			}
		}
		i := len(c.Ranges) - 1
		return c.Ranges[i].FanSpeed, i
	}
	if len(c.Points) == 0 {
		return 0, 0
	}
	if t < c.Points[0].Temperature {
		return c.Points[0].Duty, 0
	}
	for i := 1; i < len(c.Points); i++ {
		if t < c.Points[i].Temperature {
			a, b := c.Points[i-1], c.Points[i]
			if c.Type == "linear" {
				return int(math.Ceil(float64(a.Duty) + (t-a.Temperature)/(b.Temperature-a.Temperature)*float64(b.Duty-a.Duty))), i - 1
			}
			return a.Duty, i - 1
		}
	}
	i := len(c.Points) - 1
	return c.Points[i].Duty, i
}

type State struct {
	initialized  bool
	duty, index  int
	coolingSince time.Time
}

func (s *State) Reset() { *s = State{} }

// Next increases immediately. Cooling requires continuous eligibility for hold.
// Step cooling crosses the active lower threshold minus hysteresis; linear
// cooling is eligible only if F(T+hysteresis) is below the commanded duty.
func (s *State) Next(c Curve, t, hysteresis float64, hold time.Duration, now time.Time) int {
	if !finite(t) {
		return s.duty
	} // Caller must apply its invalid-sensor fallback.
	demand, index := evaluate(c, t)
	if !s.initialized || demand > s.duty {
		s.initialized = true
		s.duty = demand
		s.index = index
		s.coolingSince = time.Time{}
		return demand
	}
	if demand == s.duty {
		s.index = index
		s.coolingSince = time.Time{}
		return s.duty
	}
	eligible := false
	if c.Type == "linear" {
		eligible = Evaluate(c, t+hysteresis) < s.duty
	} else if len(c.Ranges) > 0 {
		r := c.Ranges[s.index]
		h := math.Max(hysteresis, r.Hysteresis)
		eligible = t <= r.MinTemperature-h
	} else {
		eligible = t < c.Points[s.index].Temperature-hysteresis
	}
	if !eligible {
		s.coolingSince = time.Time{}
		return s.duty
	}
	if s.coolingSince.IsZero() {
		s.coolingSince = now
	}
	if now.Sub(s.coolingSince) >= hold {
		s.duty = demand
		s.index = index
		s.coolingSince = time.Time{}
	}
	return s.duty
}

// HoldAt reconciles an externally imposed demand (for example a temporary
// sensor-coverage guard) with the smoothing state and restarts the cooling hold.
// Step hysteresis follows the highest threshold whose demand is no greater
// than the imposed duty, so clearing a guard cannot immediately lower cooling.
func (s *State) HoldAt(c Curve, duty int) {
	s.initialized = true
	s.duty = duty
	s.index = 0
	s.coolingSince = time.Time{}
	if len(c.Ranges) > 0 {
		for i, p := range c.Ranges {
			if p.FanSpeed <= duty {
				s.index = i
			}
		}
	} else {
		for i, p := range c.Points {
			if p.Duty <= duty {
				s.index = i
			}
		}
	}
}
