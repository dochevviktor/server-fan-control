// Package disks reads only explicitly selected ATA/SAT HDD temperature telemetry.
// It never discovers devices, changes SMART settings or runs self-tests.
package disks

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"time"
	"unicode"
)

type Drive struct {
	ID     string `json:"id"`
	Path   string `json:"path"`
	Type   string `json:"type"`
	Serial string `json:"serial"`
}

type Sample struct {
	ID              string    `json:"id"`
	Path            string    `json:"path"`
	Serial          string    `json:"serial"`
	TemperatureC    *float64  `json:"temperature_c"`
	Status          string    `json:"status"`
	Reason          string    `json:"reason,omitempty"`
	HealthFlags     int       `json:"health_flags,omitempty"`
	AttemptedAt     time.Time `json:"attempted_at"`
	ReadCompletedAt time.Time `json:"read_completed_at"`
	LastValidAt     time.Time `json:"last_valid_at"`
}

type Snapshot struct {
	Enabled   bool      `json:"enabled"`
	Mock      bool      `json:"mock"`
	Samples   []Sample  `json:"samples"`
	UpdatedAt time.Time `json:"updated_at"`
}

type Config struct {
	Drives          []Drive `json:"drives"`
	SamplingSeconds int     `json:"sampling_seconds"`
}

type Provider interface {
	Read(context.Context, Drive) Sample
	Mock() bool
}

func ValidateDrive(d Drive) error {
	for name, value := range map[string]string{"id": d.ID, "serial": d.Serial} {
		if value == "" || len(value) > 128 || strings.TrimSpace(value) != value || strings.IndexFunc(value, unicode.IsControl) >= 0 {
			return fmt.Errorf("drive %s must be a nonempty identifier of at most 128 characters", name)
		}
	}
	if d.Type != "ata" && d.Type != "sat" {
		return fmt.Errorf("drive type must be explicitly verified ata or sat; autodetection and SAS are unsupported")
	}
	if len(d.Path) > 256 || !strings.HasPrefix(d.Path, "/dev/") || d.Path == "/dev/" || filepath.Clean(d.Path) != d.Path || strings.IndexFunc(d.Path, func(r rune) bool { return unicode.IsControl(r) || unicode.IsSpace(r) }) >= 0 {
		return fmt.Errorf("drive path must be an absolute, clean device path beneath /dev")
	}
	return nil
}
