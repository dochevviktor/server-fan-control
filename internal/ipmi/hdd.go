package ipmi

import (
	"context"
	"errors"
	"fmt"
	"math"
	"reflect"
	"time"

	"github.com/dochevviktor/server-fan-control/internal/curve"
	"github.com/dochevviktor/server-fan-control/internal/disks"
)

// HDDPolicy is a saved draft until explicitly applied in this process. Restart
// always restores journaled ownership before requiring another explicit apply.
type HDDPolicy struct {
	Mode                  string        `json:"mode"`
	Fan                   int           `json:"fan"`
	Drives                []disks.Drive `json:"drives"`
	Curve                 curve.Curve   `json:"curve"`
	SamplingSeconds       int           `json:"sampling_seconds"`
	FreshnessSeconds      int           `json:"freshness_seconds"`
	MissingGraceSeconds   int           `json:"missing_grace_seconds"`
	CriticalTemp          float64       `json:"critical_temp"`
	Hysteresis            float64       `json:"hysteresis"`
	CoolingHoldSeconds    int           `json:"cooling_hold_seconds"`
	SharedMinDuty         int           `json:"shared_min_duty"`
	LimitsConfirmed       bool          `json:"limits_confirmed"`
	StandbyVerified       bool          `json:"standby_verified"`
	SharedCoolingVerified bool          `json:"shared_cooling_verified"`
}

type HDDStatus struct {
	State     string         `json:"state"`
	Reason    string         `json:"reason,omitempty"`
	MeanC     *float64       `json:"mean_c"`
	MaxC      *float64       `json:"max_c"`
	Valid     int            `json:"valid"`
	Expected  int            `json:"expected"`
	Asleep    int            `json:"asleep"`
	Target    *int           `json:"target"`
	Armed     bool           `json:"armed"`
	Samples   []disks.Sample `json:"samples"`
	UpdatedAt time.Time      `json:"updated_at"`
}

func validateHDD(c Settings) error {
	p := c.HDD
	if p == nil {
		return nil
	}
	invalid := func(message string) error { return fmt.Errorf("%w: HDD %s", ErrInvalid, message) }
	if p.Mode != "automatic" && p.Mode != "curve" {
		return invalid("mode must be automatic or curve")
	}
	if p.Fan < 1 || p.Fan > 3 {
		return invalid("fan must be a verified slot 1..3")
	}
	if p.SamplingSeconds < 10 || p.SamplingSeconds > 3600 {
		return invalid("sampling_seconds must be 10..3600")
	}
	if p.FreshnessSeconds < p.SamplingSeconds || p.FreshnessSeconds > 7200 {
		return invalid("freshness_seconds must be between sampling_seconds and 7200")
	}
	if p.MissingGraceSeconds < 0 || p.MissingGraceSeconds > 300 {
		return invalid("missing_grace_seconds must be 0..300")
	}
	if !hddFinite(p.Hysteresis) || p.Hysteresis < 0 || p.Hysteresis > 20 || p.CoolingHoldSeconds < 0 || p.CoolingHoldSeconds > 3600 {
		return invalid("invalid hysteresis or cooling hold")
	}
	if !hddFinite(p.CriticalTemp) || p.CriticalTemp < 0 || p.CriticalTemp > 100 || p.SharedMinDuty < 0 || p.SharedMinDuty > 100 {
		return invalid("critical temperature must be 0..100 and shared minimum 0..100; zero is an unconfigured draft")
	}
	if len(p.Drives) > 32 {
		return invalid("at most 32 selected drives are allowed")
	}
	ids, paths, serials := map[string]bool{}, map[string]bool{}, map[string]bool{}
	for _, d := range p.Drives {
		if err := disks.ValidateDrive(d); err != nil {
			return invalid(err.Error())
		}
		if ids[d.ID] || paths[d.Path] || serials[d.Serial] {
			return invalid("selected drive identities, paths and serials must be distinct")
		}
		ids[d.ID], paths[d.Path], serials[d.Serial] = true, true, true
	}
	if err := curve.Validate(p.Curve); err != nil {
		return invalid(err.Error())
	}
	if len(p.Curve.Ranges) > 0 {
		return invalid("use HDD curve points rather than legacy GPU ranges")
	}
	for _, point := range p.Curve.Points {
		if point.Temperature > 100 {
			return invalid("curve temperatures must be 0..100 Celsius")
		}
	}
	if p.Mode == "curve" {
		if !c.ready() || c.FanSensors[p.Fan] == "" || !p.LimitsConfirmed || !p.StandbyVerified || !p.SharedCoolingVerified {
			return invalid("activation requires verified hardware, thermal limits, drive identity/transport and standby behavior, and shared-component cooling")
		}
		if len(p.Drives) == 0 || p.CriticalTemp <= 0 || p.SharedMinDuty < c.MinDuty || curve.MaxDuty(p.Curve) != 100 {
			return invalid("activation requires selected drives, a critical limit, a verified shared floor at least the hardware minimum and a 100% upper endpoint")
		}
		if p.Curve.Points[len(p.Curve.Points)-1].Temperature > p.CriticalTemp {
			return invalid("full-cooling endpoint must not exceed the hottest-drive limit")
		}
	}
	return nil
}
func hddFinite(v float64) bool { return !math.IsNaN(v) && !math.IsInf(v, 0) }

// SetDiskSource is called before Run. The source returns cached telemetry and
// must never execute hardware commands on the IPMI worker.
func (s *Service) SetDiskSource(source func() disks.Snapshot) { s.diskSource = source }

func (s *Service) publishHDD(status HDDStatus) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.snapshot.HDD = cloneValue(&status)
}

// aggregateHDD only accepts current identity-matched successful readings. A
// receipt timestamp does not refresh a last-valid sample. Sleeping disks never
// contribute zero or retain stale temperatures in the arithmetic mean.
func aggregateHDD(p *HDDPolicy, source disks.Snapshot, now time.Time) HDDStatus {
	status := HDDStatus{Expected: len(p.Drives), Samples: []disks.Sample{}, UpdatedAt: now}
	samples := map[string]disks.Sample{}
	duplicates := map[string]bool{}
	for _, sample := range source.Samples {
		if _, ok := samples[sample.ID]; ok {
			duplicates[sample.ID] = true
		}
		samples[sample.ID] = sample
	}
	total := 0.0
	maxTemp := -math.MaxFloat64
	for _, d := range p.Drives {
		sample, ok := samples[d.ID]
		if !ok {
			sample = disks.Sample{ID: d.ID, Path: d.Path, Status: "missing", Reason: "waiting for selected drive sample"}
		}
		status.Samples = append(status.Samples, sample)
		if !ok || duplicates[d.ID] || sample.Path != d.Path {
			continue
		}
		// A recent standby result contains no identity read. Its configured path is
		// validated by the collector and its behavior was confirmed before activation.
		if sample.Status == "standby" && !sample.ReadCompletedAt.IsZero() && !sample.ReadCompletedAt.After(now) && now.Sub(sample.ReadCompletedAt) <= time.Duration(p.FreshnessSeconds)*time.Second {
			status.Asleep++
			continue
		}
		if sample.Status != "ok" || sample.Serial != d.Serial || sample.TemperatureC == nil || !hddFinite(*sample.TemperatureC) || *sample.TemperatureC < -20 || *sample.TemperatureC > 100 || sample.LastValidAt.IsZero() || sample.LastValidAt.After(now) || now.Sub(sample.LastValidAt) > time.Duration(p.FreshnessSeconds)*time.Second {
			continue
		}
		status.Valid++
		total += *sample.TemperatureC
		maxTemp = math.Max(maxTemp, *sample.TemperatureC)
	}
	if status.Valid > 0 {
		mean := total / float64(status.Valid)
		status.MeanC = &mean
		status.MaxC = &maxTemp
	}
	return status
}

func (s *Service) hddPoll(ctx context.Context, reading Reading) {
	p := s.Config().HDD
	if p == nil {
		s.mu.Lock()
		s.snapshot.HDD = nil
		s.mu.Unlock()
		return
	}
	source := disks.Snapshot{}
	if s.diskSource != nil {
		source = s.diskSource()
	}
	now := time.Now().UTC()
	status := aggregateHDD(p, source, now)
	status.Armed = s.hddArmed
	defer func() { s.publishHDD(status) }()
	if s.latched != "" {
		status.State = s.Snapshot().State
		status.Reason = s.latched
		return
	}
	if p.Mode != "curve" || !s.hddArmed {
		status.State = "automatic"
		status.Reason = "saved HDD draft; explicitly apply to start curve control"
		if p.Mode == "curve" {
			status.State = "awaiting_activation"
			status.Reason = "saved curve is inactive; apply explicitly after each process restart or restoration"
		}
		return
	}
	fallback := func(reason string) {
		status.State = "fallback"
		status.Reason = reason
		r := s.fail(ctx, reason)
		if !r.Restored || !r.Persisted {
			status.State = "degraded"
			status.Reason += "; automatic restoration pending: " + r.HardwareError + " " + r.StorageError
		}
	}
	if !source.Enabled || source.Mock != s.adapter.Mock() {
		fallback("HDD source is unavailable or its mock mode does not match the actuator")
		return
	}
	if status.MaxC != nil && *status.MaxC >= p.CriticalTemp {
		fallback("hottest-drive temperature reached the verified critical limit")
		return
	}
	if status.Asleep == status.Expected && status.Expected > 0 {
		s.hddMissingSince = time.Time{}
		s.hddCurve.Reset()
		r := s.restoreBounded(ctx)
		if !r.Restored || !r.Persisted {
			fallback("all selected HDDs asleep; automatic restoration failed")
			return
		}
		status.State = "sleeping"
		status.Reason = "all selected HDDs are asleep; owned fans restored to firmware automatic control"
		s.publish("automatic", status.Reason, nil)
		return
	}
	missing := status.Valid+status.Asleep < status.Expected
	if missing {
		// Require complete initial coverage; missing telemetry may hold only an
		// already established duty. It must never authorize initial manual takeover.
		if s.hddMissingSince.IsZero() {
			s.hddMissingSince = now
		}
		if now.Sub(s.hddMissingSince) >= time.Duration(p.MissingGraceSeconds)*time.Second {
			fallback("selected HDD coverage is missing, invalid or stale beyond the grace period")
			return
		}
		status.State = "waiting"
		status.Reason = "selected HDD coverage incomplete; holding existing cooling during grace period"
		if target, ok := s.targets[p.Fan]; ok {
			// Valid remaining samples may still increase demand; incomplete
			// coverage can never lower it or authorize first takeover.
			if status.MeanC != nil {
				demand := curve.Evaluate(p.Curve, *status.MeanC)
				if demand > target {
					result, err := s.setTarget(ctx, p.Fan, demand)
					if err != nil {
						fallback("HDD grace target rejected: " + err.Error())
						return
					}
					if !result.Applied {
						status.State = "degraded"
						status.Reason = "HDD grace target failed: " + result.HardwareError + " " + result.StorageError
						return
					}
					target = demand
				}
			}
			s.hddCurve.HoldAt(p.Curve, target)
			status.Target = &target
		}
		s.publish("curve_waiting", status.Reason, nil)
		return
	}
	s.hddMissingSince = time.Time{}
	if status.MeanC == nil {
		fallback("no eligible HDD temperature samples")
		return
	}
	target := s.hddCurve.Next(p.Curve, *status.MeanC, p.Hysteresis, time.Duration(p.CoolingHoldSeconds)*time.Second, now)
	target = int(math.Max(float64(target), math.Max(float64(s.Config().MinDuty), float64(p.SharedMinDuty))))
	status.State = "curve"
	status.Reason = "average HDD temperature curve"
	if target > curve.Evaluate(p.Curve, *status.MeanC) {
		status.Reason = "verified shared-component floor or cooling hold"
	}
	if status.Asleep > 0 {
		// Excluding an asleep (possibly hottest) member cannot justify a reduction.
		// On initial partial sleep there is no historical target, so choose full
		// duty until every selected disk is awake or the group is fully asleep.
		prior, ok := s.targets[p.Fan]
		if !ok {
			prior = 100
		}
		target = int(math.Max(float64(target), float64(prior)))
		status.Reason = "some selected HDDs are asleep; cooling reductions inhibited"
		s.hddCurve.HoldAt(p.Curve, target)
	}
	status.Target = &target
	if prior, ok := s.targets[p.Fan]; ok && prior == target {
		s.publish("curve", status.Reason, &reading)
		return
	}
	result, err := s.setTarget(ctx, p.Fan, target)
	if err != nil {
		fallback("HDD target rejected: " + err.Error())
		return
	}
	if !result.Applied {
		status.State = "degraded"
		status.Reason = "HDD target not applied: " + result.HardwareError + " " + result.StorageError
		return
	}
	s.publish("curve", status.Reason, nil)
}

func sameHardware(a, b Settings) bool { a.HDD = nil; b.HDD = nil; return reflect.DeepEqual(a, b) }

// automatic inhibits the in-memory curve before any filesystem operation. Even
// failed persistence cannot cause the running worker (or a restart) to reapply.
func (s *Service) automatic(ctx context.Context) Result {
	s.hddArmed = false
	s.hddCurve.Reset()
	s.hddMissingSince = time.Time{}
	current := s.Config()
	var saveErr error
	if current.HDD != nil && current.HDD.Mode == "curve" {
		next := cloneValue(current)
		next.HDD.Mode = "automatic"
		if next.Revision == ^uint64(0) {
			saveErr = fmt.Errorf("revision exhausted")
		} else {
			_, saveErr = s.saveSettings(next, current, false)
		}
	}
	result := s.restoreBounded(ctx)
	if saveErr != nil {
		result.Persisted = false
		result.StorageError = errors.Join(saveErr, errorOrNil(result.StorageError)).Error()
	}
	if result.Restored && result.Persisted {
		s.latched = ""
		s.publish("automatic", "owned fans restored; HDD curve disabled; other payload slots unchanged", nil)
	} else {
		s.latched = "automatic restoration or persistence pending; HDD curve inhibited"
		s.publish("degraded", s.latched+": "+result.HardwareError+" "+result.StorageError, nil)
	}
	s.hddPoll(ctx, s.Snapshot().Reading)
	return result
}
