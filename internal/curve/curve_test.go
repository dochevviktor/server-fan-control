package curve

import (
	"testing"
	"time"
)

func TestImportedBoundariesAndSaturation(t *testing.T) {
	c := Curve{Type: "step", Ranges: []Range{{0, 40, 30, 3}, {40, 60, 40, 3}, {60, 80, 70, 3}, {80, 100, 100, 3}}}
	for _, v := range []struct {
		temp float64
		duty int
	}{{-1, 30}, {0, 30}, {40, 30}, {40.1, 40}, {60, 40}, {200, 100}} {
		if got := Evaluate(c, v.temp); got != v.duty {
			t.Errorf("at %v got %d want %d", v.temp, got, v.duty)
		}
	}
	s := State{}
	for i, v := range []struct {
		temp float64
		duty int
	}{{40, 30}, {41, 40}, {40, 40}, {41, 40}, {38, 40}, {37, 30}} {
		if got := s.Next(c, v.temp, 3, 0, time.Unix(int64(i+1), 0)); got != v.duty {
			t.Errorf("sequence %d got %d want %d", i, got, v.duty)
		}
	}
}
func TestLinearCoolingHoldAndImmediateRise(t *testing.T) {
	c := Curve{Type: "linear", Points: []Point{{20, 20}, {100, 100}}}
	s := State{}
	now := time.Unix(1, 0)
	for _, v := range []struct {
		seconds int
		temp    float64
		want    int
	}{{0, 60, 60}, {1, 59, 60}, {2, 50, 60}, {4, 50, 60}, {5, 61, 61}, {6, 50, 61}, {11, 50, 50}, {12, 200, 100}, {13, -10, 100}, {18, -10, 20}} {
		if got := s.Next(c, v.temp, 3, 5*time.Second, now.Add(time.Duration(v.seconds)*time.Second)); got != v.want {
			t.Errorf("at %ds got %d want %d", v.seconds, got, v.want)
		}
	}
}
func TestRepeatedStepDuties(t *testing.T) {
	c := Curve{Type: "step", Points: []Point{{20, 30}, {40, 50}, {60, 50}, {80, 100}}}
	s := State{}
	for i, v := range []struct {
		temp float64
		want int
	}{{65, 50}, {59, 50}, {40, 50}, {38, 50}, {36, 30}, {80, 100}, {79, 100}, {76, 50}} {
		if got := s.Next(c, v.temp, 3, 0, time.Unix(int64(i+1), 0)); got != v.want {
			t.Errorf("%v got %d want %d", v.temp, got, v.want)
		}
	}
}
func TestRejectMalformedCurves(t *testing.T) {
	for _, c := range []Curve{{Type: "linear"}, {Type: "invalid", Points: []Point{{0, 0}, {100, 100}}}, {Type: "step", Points: []Point{{40, 50}, {30, 100}}}, {Type: "linear", Points: []Point{{10, 101}, {50, 100}}}, {Type: "step", Ranges: []Range{{0, 40, 30, 3}, {41, 80, 100, 3}}}, {Type: "step", Ranges: []Range{{0, 40, 30, 3}, {39, 80, 100, 3}}}, {Type: "linear", Points: []Point{{10, 80}, {80, 30}}}} {
		if Validate(c) == nil {
			t.Errorf("accepted %+v", c)
		}
	}
}
