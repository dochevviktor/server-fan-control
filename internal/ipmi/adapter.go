package ipmi

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"sync"
	"time"
)

type localAdapter struct {
	mu     sync.Mutex
	runner commandRunner
}

func NewLocal() Adapter {
	return &localAdapter{runner: localRunner{executable: "ipmitool", timeout: commandTimeout}}
}

func (*localAdapter) Mock() bool { return false }

// SupportedIdentity recognizes only the supplied board profile. A firmware match
// is still required against the owner's verification record before every write.
func SupportedIdentity(id Identity) bool {
	m := strings.ToUpper(strings.Join(strings.Fields(id.Manufacturer), ""))
	return id.Board == "E3C246D2I" && (m == "ASROCKRACK" || m == "ASROCK") && id.Firmware != ""
}

func (a *localAdapter) identity(ctx context.Context) (Identity, error) {
	mc, mcErr := a.runner.run(ctx, "mc", "info")
	fru, fruErr := a.runner.run(ctx, "fru", "print")
	id, parseErr := parseIdentity(mc, fru)
	return id, errors.Join(mcErr, fruErr, parseErr)
}

func (a *localAdapter) duties(ctx context.Context) ([]int, error) {
	text, err := a.runner.run(ctx, "raw", "0x3a", "0x02")
	if err != nil {
		return nil, err
	}
	return parseDuties(text)
}

func (a *localAdapter) fans(ctx context.Context) ([]Fan, error) {
	text, err := a.runner.run(ctx, "sdr", "type", "Fan")
	if err != nil {
		return []Fan{}, err
	}
	return parseFans(text)
}

func (a *localAdapter) Read(ctx context.Context) (Reading, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	r := Reading{Fans: []Fan{}}
	var idErr, fanErr, dutyErr error
	r.Identity, idErr = a.identity(ctx)
	r.Fans, fanErr = a.fans(ctx)
	if idErr == nil && SupportedIdentity(r.Identity) {
		r.Duties, dutyErr = a.duties(ctx)
		if dutyErr != nil {
			r.DutyError = dutyErr.Error()
		}
	}
	r.CollectedAt = time.Now().UTC()
	return r, errors.Join(idErr, fanErr, dutyErr)
}

func (a *localAdapter) Set(ctx context.Context, expected Identity, slot, duty int) (Reading, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	r := Reading{Fans: []Fan{}}
	if slot < 1 || slot > 8 || duty < 0 || duty > 100 {
		return r, errors.New("profile payload slot must be 1..8 and duty must be 0..100")
	}
	if !SupportedIdentity(expected) {
		return r, errors.New("unsupported or incomplete verified board identity")
	}
	id, err := a.identity(ctx)
	r.Identity = id
	if err != nil {
		return r, fmt.Errorf("verify identity before write: %w", err)
	}
	if id != expected {
		return r, errors.New("board identity or BMC firmware differs from verified identity")
	}
	before, err := a.duties(ctx)
	if err != nil {
		return r, fmt.Errorf("read profile before write: %w", err)
	}
	want := append([]int(nil), before...)
	want[slot-1] = duty
	args := []string{"raw", "0x3a", "0x01"}
	for _, value := range want {
		// Explicit hexadecimal avoids ipmitool's base-0 parsing (35 decimal is 0x23).
		args = append(args, fmt.Sprintf("0x%02x", value))
	}
	_, writeErr := a.runner.run(ctx, args...)
	// A failed command can still have reached the BMC. Always try readback, and
	// report the write error even when the target happens to match afterwards.
	r.Duties, err = a.duties(ctx)
	if err != nil {
		r.DutyError = err.Error()
	}
	var verifyErr error
	if err == nil {
		for i, value := range want {
			if r.Duties[i] != value {
				verifyErr = fmt.Errorf("profile readback mismatch at payload slot %d: got %d, expected %d", i+1, r.Duties[i], value)
				break
			}
		}
	}
	var fanErr error
	r.Fans, fanErr = a.fans(ctx)
	r.CollectedAt = time.Now().UTC()
	return r, errors.Join(writeErr, err, verifyErr, fanErr)
}

func parseIdentity(mc, fru string) (Identity, error) {
	var id Identity
	for _, line := range strings.Split(mc, "\n") {
		key, value, ok := strings.Cut(line, ":")
		if ok && strings.TrimSpace(key) == "Firmware Revision" {
			if id.Firmware != "" {
				return id, errors.New("ambiguous BMC firmware identity")
			}
			id.Firmware = strings.TrimSpace(value)
		}
	}
	// Only use the built-in FRU (ID 0), never a replaceable expansion board.
	include := !strings.Contains(fru, "FRU Device Description")
	for _, line := range strings.Split(fru, "\n") {
		key, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		key, value = strings.TrimSpace(key), strings.TrimSpace(value)
		if key == "FRU Device Description" {
			include = strings.HasSuffix(value, "(ID 0)")
			continue
		}
		if !include {
			continue
		}
		switch key {
		case "Board Product":
			if id.Board != "" {
				return id, errors.New("ambiguous built-in board product")
			}
			id.Board = value
		case "Board Mfg":
			if id.Manufacturer != "" {
				return id, errors.New("ambiguous built-in board manufacturer")
			}
			id.Manufacturer = value
		}
	}
	if id.Board == "" || id.Manufacturer == "" || id.Firmware == "" {
		return id, errors.New("BMC firmware or built-in FRU board identity unavailable")
	}
	return id, nil
}

func parseDuties(text string) ([]int, error) {
	fields := strings.Fields(text)
	if len(fields) != 8 {
		return nil, fmt.Errorf("profile read must return exactly eight bytes, got %d", len(fields))
	}
	result := make([]int, 8)
	for i, field := range fields {
		if len(field) != 2 {
			return nil, errors.New("profile read contains a malformed hexadecimal byte")
		}
		n, err := strconv.ParseUint(field, 16, 8)
		if err != nil {
			return nil, fmt.Errorf("profile read byte %d: %w", i+1, err)
		}
		result[i] = int(n)
	}
	return result, nil
}

func parseFans(text string) ([]Fan, error) {
	fans := []Fan{}
	seen := map[string]bool{}
	for _, line := range strings.Split(text, "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		fields := strings.Split(line, "|")
		// sdr type Fan normally returns name | sensor ID | status | entity | reading.
		// Also accept the compact name | reading | status form used by sdr list.
		if len(fields) != 5 && len(fields) != 3 {
			return fans, errors.New("malformed IPMI fan sensor row")
		}
		name := strings.TrimSpace(fields[0])
		if name == "" || seen[name] {
			return fans, errors.New("empty or duplicate IPMI fan sensor name")
		}
		seen[name] = true
		value, status := strings.TrimSpace(fields[1]), strings.TrimSpace(fields[2])
		if len(fields) == 5 {
			value = strings.TrimSpace(fields[4])
		}
		fan := Fan{Name: name, Status: "unavailable", Reason: value}
		parts := strings.Fields(value)
		if len(parts) == 2 && strings.EqualFold(parts[1], "RPM") {
			rpm, err := strconv.ParseFloat(parts[0], 64)
			if err != nil || math.IsNaN(rpm) || math.IsInf(rpm, 0) || rpm < 0 {
				fan.Status = "invalid"
			} else {
				fan.RPM = &rpm
				if status == "ok" {
					fan.Status, fan.Reason = "ok", ""
				} else {
					fan.Status, fan.Reason = "alarm", status
				}
			}
		}
		fans = append(fans, fan)
	}
	if len(fans) == 0 {
		return fans, errors.New("no IPMI fan sensors found")
	}
	return fans, nil
}
