// Package control owns all device operations and serializes policy transitions.
package control

import (
	"errors"
	"github.com/dochevviktor/server-fan-control/internal/gpu"
	"time"
)

var ErrUnavailable = errors.New("controller unavailable")
var ErrNotFound = errors.New("GPU not found")
var ErrInvalidPolicy = errors.New("invalid GPU policy")

type Status struct {
	Mock            bool      `json:"mock"`
	Available       bool      `json:"available"`
	Error           string    `json:"error,omitempty"`
	DesiredRevision uint64    `json:"desired_revision"`
	AppliedRevision uint64    `json:"applied_revision"`
	UpdatedAt       time.Time `json:"updated_at"`
	Version         string    `json:"version"`
}

type Fan struct {
	Index         int        `json:"index"`
	Reported      gpu.Metric `json:"reported"`
	DesiredDuty   *int       `json:"desired_duty"`
	CommandedDuty *int       `json:"commanded_duty"`
	State         string     `json:"state"`
	Error         string     `json:"error,omitempty"`
}

type GPU struct {
	gpu.Device
	Metrics         map[string]gpu.Metric `json:"metrics"`
	Fans            []Fan                 `json:"fans"`
	State           string                `json:"state"`
	Reason          string                `json:"reason,omitempty"`
	DesiredDuty     *int                  `json:"desired_duty"`
	AppliedRevision uint64                `json:"applied_revision"`
	UpdatedAt       time.Time             `json:"updated_at"`
	LastSuccessAt   *time.Time            `json:"last_success_at"`
}

type HistoryPoint struct {
	At           time.Time `json:"at"`
	TemperatureC *float64  `json:"temperature_c"`
	DesiredDuty  *int      `json:"desired_duty"`
}

type AutomaticResult struct {
	UUID          string `json:"uuid"`
	Restored      bool   `json:"restored"`
	Persisted     bool   `json:"persisted"`
	HardwareError string `json:"hardware_error,omitempty"`
	StorageError  string `json:"storage_error,omitempty"`
	Revision      uint64 `json:"revision"`
}
