// Package ipmi provides a bounded local OpenIPMI adapter. OEM writes are limited
// to the explicitly selected ASRock Rack E3C246D2I profile.
package ipmi

import (
	"context"
	"time"
)

const ProfileID = "asrock-e3c246d2i"

type Identity struct {
	Board        string `json:"board"`
	Manufacturer string `json:"manufacturer"`
	Firmware     string `json:"firmware"`
}

type Fan struct {
	Name   string   `json:"name"`
	RPM    *float64 `json:"rpm"`
	Status string   `json:"status"`
	Reason string   `json:"reason,omitempty"`
}

type Reading struct {
	Identity Identity `json:"identity"`
	Fans     []Fan    `json:"fans"`
	// Duties contains the eight raw profile bytes, in payload order. Mapping a
	// slot to a physical header and the zero/percentage encoding needs host validation.
	Duties      []int     `json:"duties"`
	DutyError   string    `json:"duty_error,omitempty"`
	CollectedAt time.Time `json:"collected_at"`
}

type Adapter interface {
	Read(context.Context) (Reading, error)
	// Set changes one 1-based payload slot, preserving all other bytes. Zero
	// requests the profile's automatic encoding; 1..100 requests a duty percent.
	// The service must verify the physical mapping and automatic restoration first.
	Set(context.Context, Identity, int, int) (Reading, error)
	Mock() bool
}
