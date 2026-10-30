// Package gpu is the hardware boundary. Only the controller calls its mutators.
package gpu

import "time"

type Metric struct {
	Value         *float64   `json:"value"`
	Unit          string     `json:"unit"`
	CollectedAt   time.Time  `json:"collected_at"`
	LastSuccessAt *time.Time `json:"last_success_at"`
	Status        string     `json:"status"`
	Reason        string     `json:"reason,omitempty"`
}

type Device struct {
	UUID          string `json:"uuid"`
	Name          string `json:"name"`
	Index         int    `json:"index"`
	Fans          []int  `json:"-"`
	Controllable  bool   `json:"controllable"`
	ControlReason string `json:"control_reason,omitempty"`
	MinDuty       int    `json:"min_duty"`
	MaxDuty       int    `json:"max_duty"`
	BoundsKnown   bool   `json:"bounds_known"`
}

type FanReading struct {
	Index    int
	Reported Metric
	Manual   *bool
}

type Telemetry struct {
	Metrics map[string]Metric
	Fans    []FanReading
}

// Adapter methods are called serially. Close follows all restoration attempts.
// A context timeout cannot interrupt an in-process vendor C function.
type Adapter interface {
	Init() error
	Discover() ([]Device, error)
	Read(uuid string) (Telemetry, error)
	Set(uuid string, fan, duty int) error
	Reset(uuid string, fan int) error
	Close() error
	Mock() bool
}

func Reading(value float64, unit string, now time.Time) Metric {
	return Metric{Value: &value, Unit: unit, CollectedAt: now, LastSuccessAt: &now, Status: "ok"}
}
