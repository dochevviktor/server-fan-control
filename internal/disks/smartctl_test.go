package disks

import (
	"context"
	"errors"
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"
)

type fakeRunner struct {
	result commandResult
	args   []string
}

func (r *fakeRunner) run(_ context.Context, args ...string) commandResult {
	r.args = append([]string(nil), args...)
	return r.result
}

func testDrive() Drive { return Drive{ID: "disk-a", Path: "/dev/sda", Type: "sat", Serial: "SERIAL-A"} }

// These are synthetic protocol cases informed by smartmontools 7.4's JSON
// schema. They are not captured hardware fixtures or transport acceptance.
func validJSON(exit int) string {
	return fmt.Sprintf(`{"smartctl":{"exit_status":%d},"device":{"protocol":"ATA"},"serial_number":"SERIAL-A","rotation_rate":7200,"temperature":{"current":37}}`, exit)
}

func TestSMARTReadAndExitClassification(t *testing.T) {
	cases := []struct {
		name, body, status string
		exit               int
		err                error
		valid              bool
	}{
		{"valid", validJSON(0), "ok", 0, nil, true},
		{"health flags keep temperature", validJSON(0xf8), "ok", 0xf8, nil, true},
		{"checksum failed despite temperature", validJSON(4), "invalid_data", 4, nil, false},
		{"identity changed", strings.Replace(validJSON(0), "SERIAL-A", "REPLACEMENT", 1), "identity_mismatch", 0, nil, false},
		{"missing serial", strings.Replace(validJSON(0), "SERIAL-A", "", 1), "identity_mismatch", 0, nil, false},
		{"SSD", strings.Replace(validJSON(0), "7200", "0", 1), "unsupported", 0, nil, false},
		{"SAS", strings.Replace(validJSON(0), `"ATA"`, `"SCSI"`, 1), "unsupported", 0, nil, false},
		{"missing rotation", strings.Replace(validJSON(0), `"rotation_rate":7200,`, "", 1), "unsupported", 0, nil, false},
		{"missing normalized temp", strings.Replace(validJSON(0), `"temperature":{"current":37}`, `"ata_smart_attributes":{"table":[{"id":194,"raw":{"value":37}}]}`, 1), "invalid_data", 0, nil, false},
		{"temp sentinel", strings.Replace(validJSON(0), `"current":37`, `"current":255`, 1), "invalid_data", 0, nil, false},
		{"null temp", strings.Replace(validJSON(0), `"current":37`, `"current":null`, 1), "invalid_data", 0, nil, false},
		{"bad JSON", `broken`, "invalid_data", 0, nil, false},
		{"missing exit", `{}`, "invalid_data", 0, nil, false},
		{"inconsistent exit", validJSON(0), "invalid_data", 8, nil, false},
		{"standby", `{"smartctl":{"exit_status":3,"messages":[{"string":"Device is in STANDBY mode, exit(3)"}]}}`, "standby", 3, nil, false},
		{"sleep", `{"smartctl":{"exit_status":3,"messages":[{"string":"Device is in SLEEP mode, exit(3)"}]}}`, "standby", 3, nil, false},
		{"exit3 not standby", `{"smartctl":{"exit_status":3,"messages":[{"string":"Command line did not parse"}]}}`, "unsupported", 3, nil, false},
		{"unsupported power check", `{"smartctl":{"exit_status":5,"messages":[{"string":"CHECK POWER MODE not implemented, exit(5)"}]}}`, "unsupported", 5, nil, false},
		{"permission", `{"smartctl":{"exit_status":2,"messages":[{"string":"Permission denied"}]}}`, "permission_denied", 2, nil, false},
		{"missing", `{"smartctl":{"exit_status":2,"messages":[{"string":"No such device"}]}}`, "missing", 2, nil, false},
		{"timeout", validJSON(0), "timeout", 0, context.DeadlineExceeded, false},
		{"binary absent", "", "missing", -1, os.ErrNotExist, false},
		{"output cap", validJSON(0), "invalid_data", 0, errOutputLimit, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := &fakeRunner{result: commandResult{stdout: tc.body, exitCode: tc.exit, err: tc.err}}
			p := &localProvider{runner: r}
			s := p.Read(context.Background(), testDrive())
			if s.Status != tc.status || (s.TemperatureC != nil) != tc.valid || (!s.LastValidAt.IsZero()) != tc.valid {
				t.Fatalf("unexpected sample: %+v", s)
			}
			if tc.valid && (s.Serial != "SERIAL-A" || *s.TemperatureC != 37 || s.HealthFlags != tc.exit&0xf8) {
				t.Fatalf("unexpected valid sample: %+v", s)
			}
			if s.AttemptedAt.IsZero() || s.ReadCompletedAt.Before(s.AttemptedAt) {
				t.Fatalf("invalid collection times: %+v", s)
			}
			want := []string{"-j", "-i", "-A", "-n", "standby,3,5", "-d", "sat", "/dev/sda"}
			if !reflect.DeepEqual(r.args, want) {
				t.Fatalf("unsafe/unexpected command: %#v", r.args)
			}
		})
	}
}

func TestDriveValidationRefusesArbitraryArguments(t *testing.T) {
	for _, mutate := range []func(*Drive){
		func(d *Drive) { d.Type = "auto" },
		func(d *Drive) { d.Type = "sat --test=long" },
		func(d *Drive) { d.Path = "/etc/passwd" },
		func(d *Drive) { d.Path = "/dev/../etc/passwd" },
		func(d *Drive) { d.Path = "-" },
		func(d *Drive) { d.Path = "/dev/sda\n" },
		func(d *Drive) { d.Serial = "" },
		func(d *Drive) { d.ID = "bad\x00id" },
	} {
		d := testDrive()
		mutate(&d)
		r := &fakeRunner{}
		s := (&localProvider{runner: r}).Read(context.Background(), d)
		if s.Status != "invalid_data" || len(r.args) != 0 {
			t.Fatalf("invalid config reached runner: %+v, %#v", d, r.args)
		}
	}
	if err := ValidateDrive(Drive{ID: "archive", Path: "/dev/disk/by-id/ata-TEST", Serial: "SERIAL-A", Type: "ata"}); err != nil {
		t.Fatal(err)
	}
}

func TestCollectorPreservesOldTimeWithoutTurningFailuresIntoValidSamples(t *testing.T) {
	r := &fakeRunner{result: commandResult{stdout: validJSON(0), exitCode: 0}}
	c := NewCollector(&localProvider{runner: r}, func() Config { return Config{} })
	cfg := Config{Drives: []Drive{testDrive()}}
	c.collect(context.Background(), cfg)
	first := c.Snapshot().Samples[0]
	r.result = commandResult{err: errors.New("broken transport")}
	c.collect(context.Background(), cfg)
	failed := c.Snapshot().Samples[0]
	if failed.Status == "ok" || failed.LastValidAt != first.LastValidAt || failed.TemperatureC == nil || *failed.TemperatureC != 37 {
		t.Fatalf("old sample metadata lost or falsely fresh: %+v", failed)
	}
	*failed.TemperatureC = 0
	if *c.Snapshot().Samples[0].TemperatureC != 37 {
		t.Fatal("snapshot shared mutable temperature pointer")
	}
	// Reusing a label after mapping changes cannot inherit the old temperature.
	cfg.Drives[0].Serial = "REPLACEMENT"
	c.collect(context.Background(), cfg)
	changed := c.Snapshot().Samples[0]
	if changed.TemperatureC != nil || !changed.LastValidAt.IsZero() {
		t.Fatalf("replacement inherited old reading: %+v", changed)
	}
	c.collect(context.Background(), Config{})
	if len(c.Snapshot().Samples) != 0 {
		t.Fatal("removed drive retained in collector")
	}
}
