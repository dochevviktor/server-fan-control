package control

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"path/filepath"
	"reflect"
	"sort"
	"sync"
	"time"

	"github.com/dochevviktor/nvidia-fan-control/internal/config"
	"github.com/dochevviktor/nvidia-fan-control/internal/curve"
	"github.com/dochevviktor/nvidia-fan-control/internal/gpu"
)

type configStore interface {
	Current() config.Config
	Update(config.Config, uint64) (config.Config, error)
	Path() string
}
type deviceState struct {
	snapshot       GPU
	demand         curve.State
	failures       int
	commanded      map[int]int
	fanErrors      map[int]string
	critical       bool
	coolingSince   time.Time
	present        bool
	observedManual map[int]bool
}
type command struct {
	ctx      context.Context
	kind     string
	cfg      config.Config
	expected uint64
	uuid     string
	reply    chan response
}
type response struct {
	cfg       config.Config
	automatic AutomaticResult
	err       error
}

type Service struct {
	adapter    gpu.Adapter
	store      configStore
	commands   chan command
	done       chan struct{}
	runOnce    sync.Once
	snapshotMu sync.RWMutex
	status     Status
	snapshots  []GPU
	histories  map[string][]HistoryPoint
	// The following fields belong exclusively to Run's worker.
	cfg          config.Config
	devices      map[string]*deviceState
	owned        map[fanKey]bool
	recovering   map[string]bool
	latched      map[string]string
	journalPath  string
	saveJournal  func(string, map[fanKey]bool) error
	journalError string
	adapterError string
	initialized  bool
	clock        func() time.Time
	stopContext  context.Context
}

func New(adapter gpu.Adapter, store *config.Store) (*Service, error) {
	return newService(adapter, store)
}
func newService(adapter gpu.Adapter, store configStore) (*Service, error) {
	name := "ownership.json"
	if adapter.Mock() {
		name = "ownership.mock.json"
	}
	path := filepath.Join(filepath.Dir(store.Path()), name)
	owned, err := readJournal(path)
	if err != nil {
		return nil, err
	}
	s := &Service{adapter: adapter, store: store, cfg: store.Current(), commands: make(chan command), done: make(chan struct{}), devices: map[string]*deviceState{}, owned: owned, recovering: map[string]bool{}, latched: map[string]string{}, journalPath: path, saveJournal: writeJournal, histories: map[string][]HistoryPoint{}, snapshots: []GPU{}}
	for key := range owned {
		s.recovering[key.UUID] = true
	}
	for uuid := range s.cfg.GPUs {
		s.addMissingDevice(uuid)
	}
	for key := range owned {
		s.addMissingDevice(key.UUID)
	}
	s.clock = func() time.Time { return time.Now().UTC() }
	s.status = Status{Mock: adapter.Mock(), DesiredRevision: s.cfg.Revision, Version: "gpu-mvp", UpdatedAt: time.Now().UTC()}
	return s, nil
}
func (s *Service) addMissingDevice(uuid string) {
	if s.devices[uuid] != nil {
		return
	}
	s.devices[uuid] = &deviceState{snapshot: GPU{Device: gpu.Device{UUID: uuid, Name: uuid, Fans: []int{}, MaxDuty: 100}, Metrics: map[string]gpu.Metric{}, Fans: []Fan{}, State: "offline"}, commanded: map[int]int{}, fanErrors: map[int]string{}, observedManual: map[int]bool{}}
}
func (s *Service) Config() config.Config { return s.store.Current() }
func (s *Service) Snapshot() Status {
	s.snapshotMu.RLock()
	defer s.snapshotMu.RUnlock()
	return s.status
}
func clone[T any](value T) T {
	data, _ := json.Marshal(value)
	var out T
	_ = json.Unmarshal(data, &out)
	return out
}
func (s *Service) GPUs() []GPU {
	s.snapshotMu.RLock()
	defer s.snapshotMu.RUnlock()
	return clone(s.snapshots)
}
func (s *Service) History(uuid string) []HistoryPoint {
	s.snapshotMu.RLock()
	defer s.snapshotMu.RUnlock()
	if h := s.histories[uuid]; h != nil {
		return clone(h)
	}
	return []HistoryPoint{}
}
func (s *Service) request(ctx context.Context, c command) (response, error) {
	c.ctx = ctx
	c.reply = make(chan response, 1)
	select {
	case s.commands <- c:
	case <-ctx.Done():
		return response{}, ctx.Err()
	case <-s.done:
		return response{}, ErrUnavailable
	}
	select {
	case r := <-c.reply:
		return r, r.err
	case <-ctx.Done():
		return response{}, ctx.Err()
	case <-s.done:
		return response{}, ErrUnavailable
	}
}
func (s *Service) Update(ctx context.Context, cfg config.Config, expected uint64) (config.Config, error) {
	r, err := s.request(ctx, command{kind: "update", cfg: config.Clone(cfg), expected: expected})
	return r.cfg, err
}
func (s *Service) Automatic(ctx context.Context, uuid string) (AutomaticResult, error) {
	r, err := s.request(ctx, command{kind: "automatic", uuid: uuid})
	return r.automatic, err
}
func (s *Service) Run(ctx context.Context) (err error) {
	started := false
	s.runOnce.Do(func() { started = true })
	if !started {
		return ErrUnavailable
	}
	defer close(s.done)
	defer func() {
		// Restoration is attempted even after config/journal failures, before Close.
		for _, uuid := range s.ownedUUIDs() {
			hardware, storage := s.restore(uuid)
			err = errors.Join(err, hardware, storage)
		}
		err = errors.Join(err, s.adapter.Close())
		s.initialized = false
		s.adapterError = "controller stopped"
		s.publish(time.Now().UTC(), false)
	}()
	s.stopContext = ctx
	if ctx.Err() != nil {
		return nil
	}
	s.tick(s.clock())
	timer := time.NewTimer(time.Duration(s.cfg.SamplingSeconds) * time.Second)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case c := <-s.commands:
			if ctx.Err() != nil {
				c.reply <- response{err: ErrUnavailable}
				return nil
			}
			if c.ctx.Err() != nil {
				c.reply <- response{err: c.ctx.Err()}
				continue
			}
			r := response{}
			switch c.kind {
			case "update":
				r.cfg, r.err = s.update(c.cfg, c.expected)
				if r.err == nil {
					if !timer.Stop() {
						select {
						case <-timer.C:
						default:
						}
					}
					timer.Reset(time.Duration(s.cfg.SamplingSeconds) * time.Second)
				}
			case "automatic":
				r.automatic, r.err = s.automatic(c.uuid)
			}
			s.publish(time.Now().UTC(), false)
			c.reply <- r
		case now := <-timer.C:
			s.tick(now.UTC())
			timer.Reset(time.Duration(s.cfg.SamplingSeconds) * time.Second)
		}
	}
}
func (s *Service) ownedUUIDs() []string {
	seen := map[string]bool{}
	for key := range s.owned {
		seen[key.UUID] = true
	}
	out := []string{}
	for uuid := range seen {
		out = append(out, uuid)
	}
	sort.Strings(out)
	return out
}
func (s *Service) selected(d gpu.Device, p config.Policy) []int {
	if len(p.Fans) > 0 {
		return p.Fans
	}
	return d.Fans
}
func (s *Service) ownedFor(uuid string) bool {
	for key := range s.owned {
		if key.UUID == uuid {
			return true
		}
	}
	return false
}
func (s *Service) restore(uuid string) (hardwareErr, storageErr error) {
	next := copyOwnership(s.owned)
	keys := []fanKey{}
	for key := range s.owned {
		if key.UUID == uuid {
			keys = append(keys, key)
		}
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i].Index < keys[j].Index })
	if len(keys) == 0 {
		return nil, nil
	}
	for _, key := range keys {
		var err error
		if !s.initialized {
			err = errors.New("NVML unavailable; restoration pending")
		} else {
			err = s.adapter.Reset(key.UUID, key.Index)
		}
		d := s.devices[uuid]
		if err != nil {
			hardwareErr = errors.Join(hardwareErr, fmt.Errorf("GPU %s fan %d: %w", uuid, key.Index, err))
			if d != nil {
				d.fanErrors[key.Index] = err.Error()
			}
			continue
		}
		delete(next, key)
		if d != nil {
			if d.observedManual == nil {
				d.observedManual = map[int]bool{}
			}
			d.observedManual[key.Index] = false
			delete(d.commanded, key.Index)
			delete(d.fanErrors, key.Index)
		}
	}
	if err := s.saveJournal(s.journalPath, next); err != nil {
		storageErr = fmt.Errorf("persist restoration journal: %w", err)
		s.journalError = storageErr.Error()
	} else {
		s.owned = next
		s.journalError = ""
	}
	if !s.ownedFor(uuid) {
		delete(s.recovering, uuid)
	}
	return hardwareErr, storageErr
}
func (s *Service) latch(uuid, reason string) {
	s.latched[uuid] = reason
	if d := s.devices[uuid]; d != nil {
		d.snapshot.State = "degraded"
		d.snapshot.Reason = reason
		d.snapshot.AppliedRevision = 0
		d.snapshot.DesiredDuty = nil
		d.demand.Reset()
	}
	hardware, storage := s.restore(uuid)
	if d := s.devices[uuid]; d != nil {
		if e := errors.Join(hardware, storage); e != nil {
			d.snapshot.Reason = reason + "; " + e.Error()
		}
	}
}
func (s *Service) automatic(uuid string) (AutomaticResult, error) {
	if s.devices[uuid] == nil && !s.ownedFor(uuid) {
		if _, exists := s.cfg.GPUs[uuid]; !exists {
			return AutomaticResult{}, ErrNotFound
		}
	}
	hadOwnership := s.ownedFor(uuid)
	wasOffline := s.devices[uuid] == nil || !s.devices[uuid].present || s.devices[uuid].snapshot.State == "offline"
	s.latched[uuid] = "automatic mode requested; explicit activation required to resume"
	if d := s.devices[uuid]; d != nil {
		d.snapshot.DesiredDuty = nil
		d.demand.Reset()
		d.critical = false
	}
	hardware, journal := s.restore(uuid)
	if !hadOwnership {
		if wasOffline {
			hardware = errors.New("GPU offline; no managed fan restoration could be verified")
		} else if d := s.devices[uuid]; d != nil {
			if !d.snapshot.Controllable {
				hardware = errors.New("monitoring-only GPU; no managed fans to restore")
			}
			for _, manual := range d.observedManual {
				if manual {
					hardware = errors.New("unowned manual fan policy observed; another controller must release it")
				}
			}
		}
	}
	externalManual := false
	if d := s.devices[uuid]; d != nil {
		for index, manual := range d.observedManual {
			if manual && !s.owned[fanKey{uuid, index}] {
				externalManual = true
			}
		}
	}
	if externalManual {
		hardware = errors.Join(hardware, errors.New("unowned manual fan policy remains; another controller must release it"))
	}
	next := config.Clone(s.cfg)
	p := next.GPUs[uuid]
	p.Mode = "automatic"
	next.GPUs[uuid] = p
	saved, storage := s.store.Update(next, s.cfg.Revision)
	if storage == nil {
		s.cfg = saved
	}
	result := AutomaticResult{UUID: uuid, Restored: hardware == nil, Persisted: storage == nil && journal == nil, Revision: s.cfg.Revision}
	if hardware != nil {
		result.HardwareError = hardware.Error()
	}
	if e := errors.Join(storage, journal); e != nil {
		result.StorageError = e.Error()
	}
	if d := s.devices[uuid]; d != nil {
		d.snapshot.DesiredDuty = nil
		if hardware == nil && storage == nil && journal == nil {
			d.snapshot.State = "automatic"
			d.snapshot.Reason = ""
			d.snapshot.AppliedRevision = s.cfg.Revision
		} else {
			d.snapshot.State = "degraded"
			d.snapshot.Reason = "automatic requested; " + result.HardwareError + " " + result.StorageError
			d.snapshot.AppliedRevision = 0
		}
		if wasOffline {
			d.snapshot.State = "offline"
		} else if !d.snapshot.Controllable {
			d.snapshot.State = "monitoring"
			d.snapshot.Reason = d.snapshot.ControlReason
		} else if (!hadOwnership || externalManual) && hardware != nil && !s.ownedFor(uuid) {
			d.snapshot.State = "monitoring"
			d.snapshot.Reason = hardware.Error()
		}
		s.syncFans(d)
	}
	return result, nil
}
func (s *Service) update(next config.Config, expected uint64) (config.Config, error) {
	if expected != s.cfg.Revision || next.Revision != expected {
		return config.Clone(s.cfg), config.ErrConflict
	}
	if err := config.Validate(next); err != nil {
		return config.Clone(s.cfg), err
	}
	for uuid, p := range next.GPUs {
		if p.Mode != "curve" {
			continue
		}
		old := s.cfg.GPUs[uuid]
		if s.latched[uuid] != "" && old.Mode == "curve" {
			return config.Clone(s.cfg), fmt.Errorf("%w: GPU %s is in fallback: return to automatic and persist it before applying a curve", ErrInvalidPolicy, uuid)
		}
		d := s.devices[uuid]
		if d == nil || !d.present || !d.snapshot.Controllable {
			return config.Clone(s.cfg), fmt.Errorf("%w: GPU %s is unavailable or monitoring only", ErrInvalidPolicy, uuid)
		}
		allowed := map[int]bool{}
		for _, fan := range d.snapshot.Device.Fans {
			allowed[fan] = true
		}
		for _, fan := range s.selected(d.snapshot.Device, p) {
			if !allowed[fan] {
				return config.Clone(s.cfg), fmt.Errorf("%w: GPU %s fan %d does not exist", ErrInvalidPolicy, uuid, fan)
			}
		}
		if d.snapshot.BoundsKnown && (p.MinDuty < d.snapshot.MinDuty || d.snapshot.MaxDuty < 100) {
			return config.Clone(s.cfg), fmt.Errorf("%w: GPU %s policy does not satisfy reported fan duty bounds", ErrInvalidPolicy, uuid)
		}
	}
	saved, err := s.store.Update(next, expected)
	if err != nil {
		return saved, err
	}
	old := s.cfg
	s.cfg = saved
	for uuid := range saved.GPUs {
		s.addMissingDevice(uuid)
	}
	for uuid, d := range s.devices {
		before := old.GPUs[uuid]
		after := saved.GPUs[uuid]
		changed := !reflect.DeepEqual(before, after) || !reflect.DeepEqual(old.Curves[before.Curve], saved.Curves[after.Curve])
		if changed {
			d.demand.Reset()
			d.critical = false
			d.coolingSince = time.Time{}
			d.snapshot.AppliedRevision = 0
		}
		if after.Mode == "curve" && before.Mode != "curve" {
			delete(s.latched, uuid)
		}
	}
	// Reconcile immediately; config persistence is distinct from hardware success.
	s.tick(s.clock())
	return saved, nil
}
func (s *Service) tick(now time.Time) {
	if !s.initialized {
		if err := s.adapter.Init(); err != nil {
			s.adapterError = err.Error()
			s.publish(now, true)
			return
		}
		s.initialized = true
	}
	devices, discoveryErr := s.adapter.Discover()
	s.adapterError = ""
	if len(devices) == 0 && discoveryErr == nil {
		s.adapterError = "no GPUs visible to NVML"
	}
	if discoveryErr != nil {
		s.adapterError = discoveryErr.Error()
	}
	for _, d := range s.devices {
		d.present = false
	}
	for _, device := range devices {
		d := s.devices[device.UUID]
		if d == nil {
			d = &deviceState{snapshot: GPU{Device: device, Metrics: map[string]gpu.Metric{}, Fans: []Fan{}, State: "automatic"}, commanded: map[int]int{}, fanErrors: map[int]string{}}
			s.devices[device.UUID] = d
		}
		d.snapshot.Device = device
		d.present = true
	}
	// Recover all journaled ownership, including no-longer-selected devices.
	for uuid := range s.recovering {
		hardware, storage := s.restore(uuid)
		if e := errors.Join(hardware, storage); e != nil {
			s.adapterError = errors.Join(errors.New("startup restoration pending"), e).Error()
		}
	}
	for uuid, d := range s.devices {
		d.snapshot.UpdatedAt = now
		var sample gpu.Telemetry
		var err error
		if d.present {
			sample, err = s.adapter.Read(uuid)
		} else {
			err = errors.New("GPU no longer visible")
		}
		// Assess age after vendor calls: a slow read must not use a stale tick time.
		now = s.clock()
		d.snapshot.UpdatedAt = now
		if err != nil {
			d.failures++
			for name, m := range d.snapshot.Metrics {
				m.Value = nil
				m.Status = "offline"
				m.Reason = err.Error()
				m.CollectedAt = now
				d.snapshot.Metrics[name] = m
			}
			d.snapshot.State = "offline"
			d.snapshot.Reason = err.Error()
			d.snapshot.AppliedRevision = 0
			for i := range d.snapshot.Fans {
				m := d.snapshot.Fans[i].Reported
				m.Value = nil
				m.Status = "offline"
				m.Reason = err.Error()
				m.CollectedAt = now
				d.snapshot.Fans[i].Reported = m
			}
			if s.ownedFor(uuid) {
				s.latch(uuid, "GPU unavailable; restoration pending: "+err.Error())
				d.snapshot.State = "offline"
			}
			s.syncFans(d)
			continue
		}
		d.snapshot.Metrics = sample.Metrics
		temperature := sample.Metrics["temperature_c"]
		valid := temperature.Value != nil && temperature.Status == "ok" && !math.IsNaN(*temperature.Value) && !math.IsInf(*temperature.Value, 0) && *temperature.Value >= 0 && *temperature.Value <= 200 && now.Sub(temperature.CollectedAt) <= time.Duration(s.cfg.FreshnessSeconds)*time.Second && !temperature.CollectedAt.After(now.Add(time.Second))
		if valid {
			d.snapshot.LastSuccessAt = temperature.LastSuccessAt
			d.failures = 0
		} else if temperature.Value != nil {
			temperature.Value = nil
			temperature.Status = "error"
			temperature.Reason = "invalid or stale temperature"
			temperature.LastSuccessAt = d.snapshot.LastSuccessAt
			d.snapshot.Metrics["temperature_c"] = temperature
		}
		d.observedManual = map[int]bool{}
		oldFans := d.snapshot.Fans
		d.snapshot.Fans = []Fan{}
		for _, f := range sample.Fans {
			if f.Manual != nil {
				d.observedManual[f.Index] = *f.Manual
			}
			d.snapshot.Fans = append(d.snapshot.Fans, Fan{Index: f.Index, Reported: f.Reported})
		}
		// Preserve disappeared fans until their tracked ownership is restored.
		for _, old := range oldFans {
			found := false
			for _, f := range d.snapshot.Fans {
				if f.Index == old.Index {
					found = true
				}
			}
			if !found && s.owned[fanKey{uuid, old.Index}] {
				old.Reported.Value = nil
				old.Reported.Status = "offline"
				old.Reported.CollectedAt = now
				d.snapshot.Fans = append(d.snapshot.Fans, old)
			}
		}
		p, managed := s.cfg.GPUs[uuid]
		// Removed GPUs/fans always restore before any further commands.
		remove := false
		allowed := map[int]bool{}
		if managed && p.Mode == "curve" {
			for _, fan := range s.selected(d.snapshot.Device, p) {
				allowed[fan] = true
			}
		}
		for key := range s.owned {
			if key.UUID == uuid && !allowed[key.Index] {
				remove = true
			}
		}
		if remove {
			hardware, storage := s.restore(uuid)
			if e := errors.Join(hardware, storage); e != nil {
				s.latch(uuid, "restore removed policy: "+e.Error())
			}
		}
		if !managed || p.Mode != "curve" {
			d.snapshot.DesiredDuty = nil
			d.snapshot.State = "automatic"
			d.snapshot.Reason = ""
			d.snapshot.AppliedRevision = s.cfg.Revision
			if !d.snapshot.Controllable {
				d.snapshot.State = "monitoring"
				d.snapshot.Reason = d.snapshot.ControlReason
			}
			for _, manual := range d.observedManual {
				if manual {
					d.snapshot.State = "monitoring"
					d.snapshot.Reason = "manual fan policy observed outside this controller"
				}
			}
			if s.ownedFor(uuid) || s.journalError != "" {
				d.snapshot.State = "degraded"
				d.snapshot.Reason = "automatic restoration or journal update pending"
				d.snapshot.AppliedRevision = 0
			}
			s.syncFans(d)
			continue
		}
		if reason := s.latched[uuid]; reason != "" {
			s.latch(uuid, reason)
			s.syncFans(d)
			continue
		}
		if len(s.recovering) > 0 {
			d.snapshot.State = "degraded"
			d.snapshot.Reason = "startup restoration pending"
			d.snapshot.AppliedRevision = 0
			s.syncFans(d)
			continue
		}
		if !d.snapshot.Controllable {
			s.latch(uuid, "GPU is monitoring only: "+d.snapshot.ControlReason)
			s.syncFans(d)
			continue
		}
		if !valid {
			d.failures++
			d.snapshot.AppliedRevision = 0
			stale := d.snapshot.LastSuccessAt == nil || now.Sub(*d.snapshot.LastSuccessAt) >= time.Duration(s.cfg.FreshnessSeconds)*time.Second
			if stale || d.failures >= s.cfg.FailureLimit {
				s.latch(uuid, "temperature invalid, stale or repeatedly unavailable")
			} else {
				d.snapshot.State = "degraded"
				d.snapshot.Reason = "temperature unavailable; holding the last target"
			}
			s.syncFans(d)
			continue
		}
		d.failures = 0
		d.snapshot.LastSuccessAt = temperature.LastSuccessAt
		duty := d.demand.Next(s.cfg.Curves[p.Curve], *temperature.Value, p.Hysteresis, time.Duration(p.CoolingHoldSeconds)*time.Second, now)
		if duty < p.MinDuty {
			duty = p.MinDuty
		}
		if duty < d.snapshot.MinDuty {
			duty = d.snapshot.MinDuty
		}
		if *temperature.Value >= p.CriticalTemp {
			d.critical = true
			d.coolingSince = time.Time{}
		}
		if d.critical {
			if *temperature.Value < p.CriticalTemp-p.Hysteresis {
				if d.coolingSince.IsZero() {
					d.coolingSince = now
				}
				if now.Sub(d.coolingSince) >= time.Duration(p.CoolingHoldSeconds)*time.Second {
					d.critical = false
				}
			} else {
				d.coolingSince = time.Time{}
			}
			if d.critical {
				duty = 100
			}
		}
		d.snapshot.DesiredDuty = &duty
		d.snapshot.State = "curve"
		d.snapshot.Reason = ""
		if d.critical {
			d.snapshot.Reason = "critical temperature: full cooling override"
		}
		selected := s.selected(d.snapshot.Device, p)
		availableFans := map[int]bool{}
		for _, fan := range d.snapshot.Device.Fans {
			availableFans[fan] = true
		}
		invalidSelection := false
		for _, fan := range selected {
			if !availableFans[fan] {
				invalidSelection = true
			}
		}
		if invalidSelection || (d.snapshot.BoundsKnown && (p.MinDuty < d.snapshot.MinDuty || d.snapshot.MaxDuty < 100)) {
			s.latch(uuid, "configured fans or duty floor do not match current hardware")
			s.syncFans(d)
			continue
		}
		if len(selected) == 0 {
			s.latch(uuid, "no fans available")
			s.syncFans(d)
			continue
		}
		if s.stopContext != nil && s.stopContext.Err() != nil {
			s.publish(now, false)
			return
		}
		// Journal every selected fan before the first possible manual write.
		next := copyOwnership(s.owned)
		needsJournal := false
		for _, fan := range selected {
			key := fanKey{uuid, fan}
			if !next[key] {
				next[key] = true
				needsJournal = true
			}
		}
		if needsJournal {
			if err := s.saveJournal(s.journalPath, next); err != nil {
				s.journalError = err.Error()
				s.latch(uuid, "refusing manual takeover: ownership journal write failed: "+err.Error())
				s.syncFans(d)
				continue
			}
			s.owned = next
			s.journalError = ""
		}
		failed := false
		for _, fan := range selected {
			if s.stopContext != nil && s.stopContext.Err() != nil {
				s.publish(now, false)
				return
			}
			known, exists := d.commanded[fan]
			manualLost := false
			for _, reading := range sample.Fans {
				if reading.Index == fan && reading.Manual != nil && !*reading.Manual {
					manualLost = true
				}
			}
			if exists && known == duty && !manualLost {
				continue
			}
			if err := s.adapter.Set(uuid, fan, duty); err != nil {
				d.fanErrors[fan] = err.Error()
				s.latch(uuid, fmt.Sprintf("fan %d write failed: %v", fan, err))
				failed = true
				break
			}
			d.commanded[fan] = duty
			delete(d.fanErrors, fan)
		}
		if !failed {
			d.snapshot.AppliedRevision = s.cfg.Revision
		}
		s.syncFans(d)
	}
	s.publish(now, true)
}
func (s *Service) syncFans(d *deviceState) {
	for i := range d.snapshot.Fans {
		fan := &d.snapshot.Fans[i]
		fan.DesiredDuty = nil
		fan.CommandedDuty = nil
		fan.State = "automatic"
		if d.observedManual[fan.Index] {
			fan.State = "external_manual"
		}
		fan.Error = d.fanErrors[fan.Index]
		if duty, exists := d.commanded[fan.Index]; exists {
			v := duty
			fan.CommandedDuty = &v
			fan.State = "manual"
		}
		if s.owned[fanKey{d.snapshot.UUID, fan.Index}] {
			fan.DesiredDuty = d.snapshot.DesiredDuty
			if fan.CommandedDuty == nil {
				fan.State = "uncertain"
			}
		}
		if fan.Error != "" {
			fan.State = "failed"
		}
	}
}
func (s *Service) publish(now time.Time, history bool) {
	snapshots := make([]GPU, 0, len(s.devices))
	applied := s.cfg.Revision
	for _, d := range s.devices {
		snapshots = append(snapshots, clone(d.snapshot))
		if d.snapshot.AppliedRevision != s.cfg.Revision {
			applied = 0
		}
	}
	sort.Slice(snapshots, func(i, j int) bool { return snapshots[i].UUID < snapshots[j].UUID })
	if !s.initialized || len(s.recovering) > 0 || s.adapterError != "" || s.journalError != "" {
		applied = 0
	}
	status := Status{Mock: s.adapter.Mock(), Available: s.initialized && s.adapterError == "", DesiredRevision: s.cfg.Revision, AppliedRevision: applied, UpdatedAt: now, Version: "gpu-mvp"}
	if s.adapterError != "" {
		status.Error = s.adapterError
	}
	if s.journalError != "" {
		status.Error += "; " + s.journalError
	}
	s.snapshotMu.Lock()
	defer s.snapshotMu.Unlock()
	s.status = status
	s.snapshots = snapshots
	if history {
		for _, d := range snapshots {
			points := append(s.histories[d.UUID], HistoryPoint{At: now, TemperatureC: d.Metrics["temperature_c"].Value, DesiredDuty: d.DesiredDuty})
			first := 0
			for first < len(points) && now.Sub(points[first].At) > 30*time.Minute {
				first++
			}
			points = points[first:]
			if len(points) > 1800 {
				points = points[len(points)-1800:]
			}
			s.histories[d.UUID] = points
		}
	}
}
