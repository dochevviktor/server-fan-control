package ipmi

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

var ErrInvalid = errors.New("invalid system-fan request")
var ErrUnavailable = errors.New("system-fan controller unavailable")

// Settings records the owner's hardware acceptance. It never persists a manual
// target: each process starts in monitoring after recovering prior ownership.
type Settings struct {
	SchemaVersion     int            `json:"schema_version"`
	Revision          uint64         `json:"revision"`
	Profile           string         `json:"profile"`
	ExpectedIdentity  Identity       `json:"expected_identity"`
	MinDuty           int            `json:"min_duty"`
	AutomaticVerified bool           `json:"automatic_verified"`
	MappingVerified   bool           `json:"mapping_verified"`
	ThermalVerified   bool           `json:"thermal_verified"`
	FanSensors        map[int]string `json:"fan_sensors"`
}

func DefaultSettings() Settings {
	return Settings{SchemaVersion: 1, Revision: 1, FanSensors: map[int]string{1: "FAN1", 2: "FAN2", 3: "FAN3"}}
}

func ValidateSettings(c Settings) error {
	if c.SchemaVersion != 1 || c.Revision == 0 {
		return fmt.Errorf("%w: invalid settings schema or revision", ErrInvalid)
	}
	if c.Profile != "" && c.Profile != ProfileID {
		return fmt.Errorf("%w: unsupported board profile", ErrInvalid)
	}
	if c.MinDuty < 0 || c.MinDuty > 100 {
		return fmt.Errorf("%w: minimum duty must be 0..100 (zero is an unconfigured draft)", ErrInvalid)
	}
	for _, value := range []string{c.ExpectedIdentity.Board, c.ExpectedIdentity.Manufacturer, c.ExpectedIdentity.Firmware} {
		if len(value) > 128 || strings.ContainsAny(value, "\r\n\x00") {
			return fmt.Errorf("%w: invalid expected identity", ErrInvalid)
		}
	}
	seen := map[string]bool{}
	for fan, sensor := range c.FanSensors {
		if fan < 1 || fan > 3 || strings.TrimSpace(sensor) != sensor || sensor == "" || len(sensor) > 64 || strings.ContainsAny(sensor, "\r\n\x00") || seen[sensor] {
			return fmt.Errorf("%w: fan sensor mappings must use distinct names and slots 1..3", ErrInvalid)
		}
		seen[sensor] = true
	}
	if c.AutomaticVerified || c.MappingVerified || c.ThermalVerified {
		if c.Profile != ProfileID || c.ExpectedIdentity.Board != "E3C246D2I" || c.ExpectedIdentity.Manufacturer == "" || c.ExpectedIdentity.Firmware == "" || c.MinDuty == 0 {
			return fmt.Errorf("%w: hardware verification requires the board profile, complete observed identity and a nonzero minimum duty", ErrInvalid)
		}
	}
	if c.MappingVerified && len(c.FanSensors) == 0 {
		return fmt.Errorf("%w: verified mapping must select at least one sensor", ErrInvalid)
	}
	return nil
}

func (c Settings) ready() bool {
	return c.Profile == ProfileID && c.AutomaticVerified && c.MappingVerified && c.ThermalVerified && c.MinDuty > 0
}

type Snapshot struct {
	Enabled   bool      `json:"enabled"`
	Mock      bool      `json:"mock"`
	State     string    `json:"state"`
	Reason    string    `json:"reason,omitempty"`
	Reading   Reading   `json:"reading"`
	OwnedFans []int     `json:"owned_fans"`
	UpdatedAt time.Time `json:"updated_at"`
}

type Result struct {
	Applied       bool   `json:"applied"`
	Restored      bool   `json:"restored"`
	Persisted     bool   `json:"persisted"`
	HardwareError string `json:"hardware_error,omitempty"`
	StorageError  string `json:"storage_error,omitempty"`
}
