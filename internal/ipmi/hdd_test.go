package ipmi

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/dochevviktor/server-fan-control/internal/curve"
	"github.com/dochevviktor/server-fan-control/internal/disks"
)

func hddPolicy() *HDDPolicy {
	return &HDDPolicy{Mode: "curve", Fan: 3, Drives: []disks.Drive{{ID: "disk-a", Path: "/dev/sda", Type: "sat", Serial: "SERIAL-A"}, {ID: "disk-b", Path: "/dev/sdb", Type: "sat", Serial: "SERIAL-B"}}, Curve: curve.Curve{Type: "linear", Points: []curve.Point{{Temperature: 25, Duty: 30}, {Temperature: 35, Duty: 50}, {Temperature: 45, Duty: 100}}}, SamplingSeconds: 60, FreshnessSeconds: 180, MissingGraceSeconds: 60, CriticalTemp: 55, Hysteresis: 2, CoolingHoldSeconds: 120, SharedMinDuty: 30, LimitsConfirmed: true, StandbyVerified: true, SharedCoolingVerified: true}
}
func hddSamples(p *HDDPolicy, temperatures ...float64) disks.Snapshot {
	now := time.Now().UTC()
	result := disks.Snapshot{Enabled: true, Mock: true, UpdatedAt: now, Samples: []disks.Sample{}}
	for i, temp := range temperatures {
		d := p.Drives[i]
		v := temp
		result.Samples = append(result.Samples, disks.Sample{ID: d.ID, Path: d.Path, Serial: d.Serial, TemperatureC: &v, Status: "ok", AttemptedAt: now, ReadCompletedAt: now, LastValidAt: now})
	}
	return result
}
func hddService(t *testing.T) (*Service, *serviceFake, *disks.Snapshot) {
	t.Helper()
	s, f := readyService(t)
	c := s.Config()
	c.HDD = hddPolicy()
	source := hddSamples(c.HDD, 35, 39)
	s.SetDiskSource(func() disks.Snapshot { return source })
	if _, err := s.update(c, c.Revision); err != nil {
		t.Fatal(err)
	}
	return s, f, &source
}

func TestHDDAggregationUsesMeanMaximumAndVerifiedFreshCoverage(t *testing.T) {
	p := hddPolicy()
	p.Drives = append(p.Drives, disks.Drive{ID: "disk-c", Path: "/dev/sdc", Type: "ata", Serial: "C"}, disks.Drive{ID: "disk-d", Path: "/dev/sdd", Type: "ata", Serial: "D"})
	source := hddSamples(p, 30, 30, 30, 54)
	now := time.Now().UTC()
	a := aggregateHDD(p, source, now)
	if a.Valid != 4 || a.Expected != 4 || *a.MeanC != 36 || *a.MaxC != 54 {
		t.Fatalf("incorrect average/max: %+v", a)
	}
	source.Samples[3].LastValidAt = now.Add(-181 * time.Second)
	a = aggregateHDD(p, source, now)
	if a.Valid != 3 || *a.MeanC != 30 {
		t.Fatalf("stale hottest disk counted: %+v", a)
	}
	source.Samples[0].Path = "/dev/replacement"
	source.Samples[1].Serial = "replacement"
	source.Samples[2].Status = "invalid_data"
	a = aggregateHDD(p, source, now)
	if a.Valid != 0 || a.MeanC != nil || a.MaxC != nil {
		t.Fatalf("bad identity/data became a temperature: %+v", a)
	}
	source = hddSamples(p, 30, 30, 30, 54)
	source.Samples = append(source.Samples, source.Samples[0])
	if a = aggregateHDD(p, source, now.Add(time.Second)); a.Valid != 3 {
		t.Fatal("ambiguous duplicate identity counted")
	}
}

func TestHDDActivationRequiresEveryAcceptanceAndSeparateLimits(t *testing.T) {
	s, _ := readyService(t)
	c := s.Config()
	c.HDD = hddPolicy()
	for name, change := range map[string]func(*Settings){
		"profile":           func(c *Settings) { c.AutomaticVerified = false },
		"limits":            func(c *Settings) { c.HDD.LimitsConfirmed = false },
		"transport/standby": func(c *Settings) { c.HDD.StandbyVerified = false },
		"shared cooling":    func(c *Settings) { c.HDD.SharedCoolingVerified = false },
		"floor":             func(c *Settings) { c.HDD.SharedMinDuty = 0 },
		"critical":          func(c *Settings) { c.HDD.CriticalTemp = 0 },
		"endpoint":          func(c *Settings) { c.HDD.Curve.Points[2].Duty = 90 },
		"late endpoint":     func(c *Settings) { c.HDD.CriticalTemp = 40 },
		"empty":             func(c *Settings) { c.HDD.Drives = nil },
		"duplicate":         func(c *Settings) { c.HDD.Drives = append(c.HDD.Drives, c.HDD.Drives[0]) },
		"SSD type":          func(c *Settings) { c.HDD.Drives[0].Type = "nvme" },
	} {
		t.Run(name, func(t *testing.T) {
			bad := cloneValue(c)
			change(&bad)
			if !errors.Is(ValidateSettings(bad), ErrInvalid) {
				t.Fatal("unsafe curve accepted")
			}
		})
	}
	c.HDD.Mode = "automatic"
	c.HDD.CriticalTemp = 0
	c.HDD.SharedMinDuty = 0
	c.HDD.LimitsConfirmed = false
	c.HDD.StandbyVerified = false
	c.HDD.SharedCoolingVerified = false
	c.HDD.Drives = nil
	if err := ValidateSettings(c); err != nil {
		t.Fatalf("unconfigured draft rejected: %v", err)
	}
}

func TestHDDCurveControlsSelectedFanCoalescesAndHonorsFloor(t *testing.T) {
	s, f, source := hddService(t)
	s.poll(context.Background())
	a := s.Snapshot().HDD
	if a.State != "curve" || a.Target == nil || *a.Target != 60 || f.reading.Duties[2] != 60 || f.reading.Duties[1] != 77 {
		t.Fatalf("mean37 should command60 on selected fan: %+v", a)
	}
	count := len(f.events)
	s.poll(context.Background())
	if len(f.events) != count {
		t.Fatal("identical target not coalesced")
	}
	if _, err := s.set(context.Background(), 1, 40); !errors.Is(err, ErrInvalid) {
		t.Fatal("manual request overlapped armed curve")
	}
	*source = hddSamples(s.Config().HDD, 41, 41)
	s.poll(context.Background())
	if f.reading.Duties[2] != 80 {
		t.Fatal("rising demand delayed")
	}
	*source = hddSamples(s.Config().HDD, 25, 25)
	s.poll(context.Background())
	if f.reading.Duties[2] != 80 {
		t.Fatal("cooling hold not honored")
	}
	c := s.Config()
	c.HDD.CoolingHoldSeconds = 0
	c.HDD.SharedMinDuty = 45
	if _, err := s.update(c, c.Revision); err != nil {
		t.Fatal(err)
	}
	s.poll(context.Background())
	if f.reading.Duties[2] != 45 {
		t.Fatal("verified shared-component floor not applied")
	}
}

func TestHDDMissingHottestHoldsThenRestoresAndLatches(t *testing.T) {
	s, f, source := hddService(t)
	s.poll(context.Background())
	source.Samples = source.Samples[:1]
	s.poll(context.Background())
	a := s.Snapshot().HDD
	if a.State != "waiting" || a.Valid != 1 || f.reading.Duties[2] != 60 {
		t.Fatalf("missing hottest disk reduced cooling: %+v", a)
	}
	s.hddMissingSince = time.Now().Add(-61 * time.Second)
	s.poll(context.Background())
	if s.Snapshot().State != "fallback" || f.reading.Duties[2] != 0 || len(s.owned) != 0 {
		t.Fatal("missing coverage did not restore")
	}
	*source = hddSamples(s.Config().HDD, 35, 39)
	s.poll(context.Background())
	if f.reading.Duties[2] != 0 || s.Snapshot().HDD.State != "fallback" {
		t.Fatal("fallback silently resumed")
	}
}

func TestHDDCriticalMaximumOverridesSafeMeanAndFailedRestoreIsDegraded(t *testing.T) {
	s, f, source := hddService(t)
	s.poll(context.Background())
	*source = hddSamples(s.Config().HDD, 20, 55)
	f.resetErr = true
	s.poll(context.Background())
	if s.Snapshot().State != "degraded" || s.Snapshot().HDD.State != "degraded" || len(s.owned) == 0 {
		t.Fatal("failed restoration incorrectly reported safe")
	}
	f.resetErr = false
	s.poll(context.Background())
	if f.reading.Duties[2] != 0 || len(s.owned) != 0 {
		t.Fatal("pending critical fallback not retried")
	}
}

func TestHDDStandbyExcludesSamplesWithoutReducingCooling(t *testing.T) {
	s, f, source := hddService(t)
	s.poll(context.Background())
	source.Samples[1].Status = "standby"
	source.Samples[1].Serial = ""
	source.Samples[1].TemperatureC = nil
	s.poll(context.Background())
	a := s.Snapshot().HDD
	if a.Asleep != 1 || a.Valid != 1 || *a.MeanC != 35 || f.reading.Duties[2] != 60 {
		t.Fatalf("partial sleep reduced demand: %+v", a)
	}
	source.Samples[0].Status = "standby"
	source.Samples[0].Serial = ""
	source.Samples[0].TemperatureC = nil
	s.poll(context.Background())
	a = s.Snapshot().HDD
	if a.State != "sleeping" || a.MeanC != nil || a.MaxC != nil || f.reading.Duties[2] != 0 || s.Snapshot().Reading.Duties[2] != 0 {
		t.Fatalf("all asleep average/automatic wrong: %+v", a)
	}
	*source = hddSamples(s.Config().HDD, 35, 39)
	s.poll(context.Background())
	if f.reading.Duties[2] != 60 {
		t.Fatal("wake did not resume with verified fresh coverage")
	}
	s, f, source = hddService(t)
	source.Samples[1].Status = "standby"
	s.poll(context.Background())
	if f.reading.Duties[2] != 100 {
		t.Fatal("initial partial sleep must use conservative full duty")
	}
}

func TestHDDAutomaticStorageFailureInhibitsAndRestartDoesNotRearm(t *testing.T) {
	s, f, _ := hddService(t)
	s.poll(context.Background())
	s.write = func(string, any) error { return errors.New("read-only storage") }
	result := s.automatic(context.Background())
	if !result.Restored || result.Persisted || s.hddArmed || f.reading.Duties[2] != 0 {
		t.Fatalf("failed disable storage blocked restoration: %+v", result)
	}
	s.poll(context.Background())
	if f.reading.Duties[2] != 0 {
		t.Fatal("failed disable rearmed curve")
	}
	restarted, err := NewService(f, s.path)
	if err != nil {
		t.Fatal(err)
	}
	source := hddSamples(restarted.Config().HDD, 35, 39)
	restarted.SetDiskSource(func() disks.Snapshot { return source })
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- restarted.Run(ctx) }()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		snap := restarted.Snapshot()
		if snap.HDD != nil && snap.HDD.State == "awaiting_activation" {
			break
		}
		time.Sleep(time.Millisecond)
	}
	snap := restarted.Snapshot()
	if snap.HDD == nil || snap.HDD.Armed || snap.HDD.State != "awaiting_activation" || len(snap.OwnedFans) != 0 {
		t.Fatalf("restart rearmed or failed recovery: %+v", snap)
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestHDDChangesRestorePreviousZoneAndFailedSaveDoesNotLeaveManual(t *testing.T) {
	s, f, _ := hddService(t)
	s.poll(context.Background())
	c := s.Config()
	c.HDD.Fan = 1
	if _, err := s.update(c, c.Revision); err != nil {
		t.Fatal(err)
	}
	if f.reading.Duties[2] != 0 {
		t.Fatal("zone change did not restore prior owned zone")
	}
	s.poll(context.Background())
	if f.reading.Duties[0] != 60 {
		t.Fatal("new zone not controlled")
	}
	c = s.Config()
	s.write = func(path string, v any) error {
		if path == s.journal {
			return writeDocument(path, v)
		}
		return errors.New("disk full")
	}
	// Even reapplying an unchanged curve must restore before a failing save.
	if _, err := s.update(c, c.Revision); err == nil {
		t.Fatal("save unexpectedly succeeded")
	}
	s.poll(context.Background())
	if s.hddArmed || f.reading.Duties[0] != 0 {
		t.Fatal("failed save retained unattended manual demand")
	}
}

func TestHDDSourceModeMismatchNeverActuates(t *testing.T) {
	s, f, source := hddService(t)
	source.Mock = false
	s.poll(context.Background())
	if len(f.events) != 0 || s.Snapshot().State != "fallback" {
		t.Fatal("mixed real/mock control accepted")
	}
	// Old settings remain backwards compatible and do not require a disk source.
	legacy, err := NewService(newServiceFake(), filepath.Join(t.TempDir(), "ipmi.json"))
	if err != nil {
		t.Fatal(err)
	}
	legacy.poll(context.Background())
	if legacy.Snapshot().HDD != nil {
		t.Fatal("legacy settings unexpectedly enabled HDD control")
	}
}

func TestHDDMissingGraceStillRaisesDemandAndRecoveryRestartsCoolingHold(t *testing.T) {
	s, f, source := hddService(t)
	s.poll(context.Background())
	*source = hddSamples(s.Config().HDD, 43)
	s.poll(context.Background())
	if f.reading.Duties[2] != 90 || s.Snapshot().HDD.State != "waiting" {
		t.Fatal("missing coverage inhibited a valid rise")
	}
	*source = hddSamples(s.Config().HDD, 30, 30)
	s.poll(context.Background())
	if f.reading.Duties[2] != 90 {
		t.Fatal("recovered coverage bypassed cooling hold")
	}
}

func TestHDDPartialSleepWakeDoesNotDropForcedDutyImmediately(t *testing.T) {
	for _, kind := range []string{"linear", "step"} {
		t.Run(kind, func(t *testing.T) {
			s, f, source := hddService(t)
			c := s.Config()
			c.HDD.Curve.Type = kind
			if _, err := s.update(c, c.Revision); err != nil {
				t.Fatal(err)
			}
			source.Samples[1].Status = "standby"
			s.poll(context.Background())
			if f.reading.Duties[2] != 100 {
				t.Fatal("partial sleep did not establish conservative duty")
			}
			*source = hddSamples(s.Config().HDD, 30, 30)
			s.poll(context.Background())
			if f.reading.Duties[2] != 100 {
				t.Fatal("wake bypassed downward hold")
			}
		})
	}
}

func TestHDDRemovalClearsSnapshotAndRestores(t *testing.T) {
	s, f, _ := hddService(t)
	s.poll(context.Background())
	c := s.Config()
	c.HDD = nil
	if _, err := s.update(c, c.Revision); err != nil {
		t.Fatal(err)
	}
	s.poll(context.Background())
	if s.Snapshot().HDD != nil || f.reading.Duties[2] != 0 || s.hddArmed {
		t.Fatal("removed curve remained visible or active")
	}
}

func TestHDDColdValidSensorSaturatesAtCurveFloor(t *testing.T) {
	s, f, source := hddService(t)
	*source = hddSamples(s.Config().HDD, -5, 0)
	s.poll(context.Background())
	a := s.Snapshot().HDD
	if a.Valid != 2 || *a.MeanC != -2.5 || f.reading.Duties[2] != 30 {
		t.Fatalf("cold normalized sample was not valid: %+v", a)
	}
}

func TestHDDReapplySavedCurveCannotInheritManualOwnership(t *testing.T) {
	s, f, _ := hddService(t)
	// A saved curve starts unarmed in a new process.
	restarted, err := NewService(f, s.path)
	if err != nil {
		t.Fatal(err)
	}
	if result, err := restarted.set(context.Background(), 1, 40); err != nil || !result.Applied {
		t.Fatalf("manual target: %+v %v", result, err)
	}
	c := restarted.Config()
	if _, err := restarted.update(c, c.Revision); !errors.Is(err, ErrInvalid) {
		t.Fatal("identical save armed HDD while prior manual fan remained owned")
	}
	if restarted.hddArmed {
		t.Fatal("HDD unexpectedly armed")
	}
}
