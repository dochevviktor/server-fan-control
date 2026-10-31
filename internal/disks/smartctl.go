package disks

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"strings"
	"time"
)

type localProvider struct{ runner commandRunner }

func NewLocal() Provider {
	return &localProvider{runner: localRunner{executable: "smartctl", timeout: commandTimeout}}
}
func (*localProvider) Mock() bool { return false }

// Only normalized temperature.current is accepted. Raw ATA attribute 194 is
// vendor-specific and must not be interpreted as a plain Celsius integer.
type document struct {
	Smartctl struct {
		ExitStatus *int `json:"exit_status"`
		Messages   []struct {
			String string `json:"string"`
		} `json:"messages"`
	} `json:"smartctl"`
	Device struct {
		Protocol string `json:"protocol"`
	} `json:"device"`
	Serial       string `json:"serial_number"`
	RotationRate *int   `json:"rotation_rate"`
	Temperature  struct {
		Current *float64 `json:"current"`
	} `json:"temperature"`
}

func (p *localProvider) Read(ctx context.Context, d Drive) Sample {
	s := Sample{ID: d.ID, Path: d.Path, Status: "invalid_data", AttemptedAt: time.Now().UTC()}
	if err := ValidateDrive(d); err != nil {
		s.Reason = err.Error()
		s.ReadCompletedAt = time.Now().UTC()
		return s
	}
	result := p.runner.run(ctx, "-j", "-i", "-A", "-n", "standby,3,5", "-d", d.Type, d.Path)
	s.ReadCompletedAt = time.Now().UTC()
	if result.err != nil {
		s.Reason = result.err.Error()
		switch {
		case errors.Is(result.err, context.DeadlineExceeded), errors.Is(result.err, context.Canceled):
			s.Status = "timeout"
		case errors.Is(result.err, os.ErrPermission):
			s.Status = "permission_denied"
		case errors.Is(result.err, os.ErrNotExist):
			s.Status = "missing"
		}
		return s
	}
	var doc document
	if err := json.Unmarshal([]byte(result.stdout), &doc); err != nil {
		s.Reason = "invalid smartctl JSON"
		return s
	}
	if doc.Smartctl.ExitStatus == nil || *doc.Smartctl.ExitStatus != result.exitCode || result.exitCode < 0 || result.exitCode > 255 {
		s.Reason = "missing or inconsistent smartctl exit status"
		return s
	}
	var messages []string
	for _, m := range doc.Smartctl.Messages {
		messages = append(messages, strings.TrimSpace(m.String))
	}
	message := strings.Join(messages, "; ")
	if len(message) > 2048 {
		message = message[:2048]
	}
	lower := strings.ToLower(message)
	// These unique -n exit codes are also possible combinations of error bits.
	// Require the matching power-check message, never classify by code alone.
	if result.exitCode == 3 && (strings.Contains(lower, "device is in standby mode") || strings.Contains(lower, "device is in standby_y mode") || strings.Contains(lower, "device is in sleep mode")) && strings.Contains(lower, "exit(3)") {
		s.Status, s.Reason = "standby", "drive sleeping; temperature and identity were not queried"
		return s
	}
	if result.exitCode == 5 && strings.Contains(lower, "check power mode") && strings.Contains(lower, "exit(5)") {
		s.Status, s.Reason = "unsupported", "power check unsupported; temperature query skipped"
		return s
	}
	if result.exitCode&7 != 0 {
		s.Status, s.Reason = "invalid_data", fmt.Sprintf("smartctl transport/command error (exit %d): %s", result.exitCode, message)
		switch {
		case strings.Contains(lower, "permission denied"), strings.Contains(lower, "operation not permitted"):
			s.Status = "permission_denied"
		case strings.Contains(lower, "no such device"), strings.Contains(lower, "no such file"):
			s.Status = "missing"
		case result.exitCode&1 != 0:
			s.Status = "unsupported"
		}
		return s
	}
	s.HealthFlags = result.exitCode & 0xf8
	s.Serial = doc.Serial
	if doc.Serial == "" || doc.Serial != d.Serial {
		s.Status, s.Reason = "identity_mismatch", "observed serial does not match the selected HDD"
		return s
	}
	if doc.Device.Protocol != "ATA" || doc.RotationRate == nil || *doc.RotationRate <= 0 {
		s.Status, s.Reason = "unsupported", "an identified rotating ATA HDD is required; SSD, NVMe and SAS are excluded"
		return s
	}
	t := doc.Temperature.Current
	if t == nil || math.IsNaN(*t) || math.IsInf(*t, 0) || *t < -20 || *t > 100 {
		s.Reason = "normalized temperature.current is missing or outside -20..100 Celsius"
		return s
	}
	s.TemperatureC, s.LastValidAt, s.Status = t, s.ReadCompletedAt, "ok"
	if s.HealthFlags != 0 {
		s.Reason = fmt.Sprintf("SMART health flags 0x%02x; temperature read succeeded", s.HealthFlags)
	}
	return s
}
