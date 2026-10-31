package ipmi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"reflect"
	"sync"
	"time"

	"github.com/dochevviktor/server-fan-control/internal/config"
)

type serviceCommand struct {
	ctx       context.Context
	kind      string
	settings  Settings
	expected  uint64
	fan, duty int
	reply     chan serviceResponse
}
type serviceResponse struct {
	settings Settings
	result   Result
	err      error
}

type Service struct {
	adapter       Adapter
	path, journal string
	commands      chan serviceCommand
	done          chan struct{}
	once          sync.Once
	mu            sync.RWMutex
	settings      Settings
	snapshot      Snapshot
	// Remaining state belongs exclusively to Run.
	owned            map[int]Identity
	targets          map[int]int
	latched          string
	storageError     string
	write            func(string, any) error
	pollInterval     time.Duration
	operationTimeout time.Duration
}

// NewService requires the caller to hold the shared configuration directory lock.
func NewService(adapter Adapter, path string) (*Service, error) {
	if err := recoverSettings(path); err != nil {
		return nil, err
	}
	var c Settings
	if err := decodeFile(path, &c); errors.Is(err, os.ErrNotExist) {
		c = DefaultSettings()
		if err = writeDocument(path, c); err != nil {
			return nil, err
		}
	} else if err != nil {
		return nil, fmt.Errorf("IPMI settings: %w", err)
	}
	if err := ValidateSettings(c); err != nil {
		return nil, err
	}
	p := journalPath(path, adapter.Mock())
	owned, err := readOwnership(p)
	if err != nil {
		return nil, err
	}
	s := &Service{adapter: adapter, path: path, journal: p, settings: c, owned: owned, targets: map[int]int{}, commands: make(chan serviceCommand), done: make(chan struct{}), write: writeDocument, pollInterval: 15 * time.Second, operationTimeout: 8 * time.Second}
	s.snapshot = Snapshot{Enabled: true, Mock: adapter.Mock(), State: "starting", OwnedFans: sortedFans(owned), UpdatedAt: time.Now().UTC(), Reading: Reading{Fans: []Fan{}}}
	return s, nil
}

func cloneValue[T any](in T) T {
	b, _ := json.Marshal(in)
	var out T
	_ = json.Unmarshal(b, &out)
	return out
}
func (s *Service) Config() Settings {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return cloneValue(s.settings)
}
func (s *Service) Snapshot() Snapshot {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return cloneValue(s.snapshot)
}
func (s *Service) publish(state, reason string, r *Reading) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.snapshot.State = state
	s.snapshot.Reason = reason
	s.snapshot.OwnedFans = sortedFans(s.owned)
	s.snapshot.UpdatedAt = time.Now().UTC()
	if r != nil {
		s.snapshot.Reading = cloneValue(*r)
	}
}
func (s *Service) request(ctx context.Context, c serviceCommand) (serviceResponse, error) {
	c.ctx = ctx
	c.reply = make(chan serviceResponse, 1)
	select {
	case s.commands <- c:
	case <-ctx.Done():
		return serviceResponse{}, ctx.Err()
	case <-s.done:
		return serviceResponse{}, ErrUnavailable
	}
	select {
	case r := <-c.reply:
		return r, r.err
	case <-ctx.Done():
		return serviceResponse{}, ctx.Err()
	case <-s.done:
		return serviceResponse{}, ErrUnavailable
	}
}
func (s *Service) Update(ctx context.Context, c Settings, expected uint64) (Settings, error) {
	r, e := s.request(ctx, serviceCommand{kind: "update", settings: cloneValue(c), expected: expected})
	return r.settings, e
}
func (s *Service) Set(ctx context.Context, fan, duty int) (Result, error) {
	r, e := s.request(ctx, serviceCommand{kind: "set", fan: fan, duty: duty})
	return r.result, e
}
func (s *Service) Automatic(ctx context.Context) (Result, error) {
	r, e := s.request(ctx, serviceCommand{kind: "automatic"})
	return r.result, e
}

func (s *Service) Run(ctx context.Context) (err error) {
	started := false
	s.once.Do(func() { started = true })
	if !started {
		return ErrUnavailable
	}
	defer close(s.done)
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		r := s.restore(cleanup)
		if r.HardwareError != "" {
			err = errors.Join(err, errors.New(r.HardwareError))
		}
		if r.StorageError != "" {
			err = errors.Join(err, errors.New(r.StorageError))
		}
		state, reason := "stopped", "controller stopped; owned fans restored"
		if !r.Restored || !r.Persisted {
			state = "degraded"
			reason = "shutdown restoration pending: " + r.HardwareError + " " + r.StorageError
		}
		s.publish(state, reason, nil)
	}()
	if len(s.owned) > 0 {
		r := s.restoreBounded(ctx)
		if !r.Restored || !r.Persisted {
			s.latched = "startup automatic restoration pending"
		}
	}
	s.poll(ctx)
	ticker := time.NewTicker(s.pollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			s.poll(ctx)
		case c := <-s.commands:
			if c.ctx.Err() != nil {
				c.reply <- serviceResponse{err: c.ctx.Err()}
				continue
			}
			if ctx.Err() != nil {
				c.reply <- serviceResponse{err: ErrUnavailable}
				continue
			}
			r := serviceResponse{}
			switch c.kind {
			case "update":
				r.settings, r.err = s.update(c.settings, c.expected)
			case "set":
				r.result, r.err = s.set(ctx, c.fan, c.duty)
			case "automatic":
				r.result = s.restoreBounded(ctx)
				if r.result.Restored && r.result.Persisted {
					s.latched = ""
					s.publish("automatic", "owned fans restored; other payload slots unchanged", nil)
				} else {
					s.latched = "automatic restoration pending"
					s.publish("degraded", r.result.HardwareError+" "+r.result.StorageError, nil)
				}
			}
			c.reply <- r
		}
	}
}

func (s *Service) update(next Settings, expected uint64) (Settings, error) {
	current := s.Config()
	if next.Revision != expected || current.Revision != expected {
		return current, config.ErrConflict
	}
	if err := ValidateSettings(next); err != nil {
		return current, err
	}
	if len(s.owned) > 0 && !reflect.DeepEqual(next, current) {
		return current, fmt.Errorf("%w: return owned fans to automatic before changing settings", ErrInvalid)
	}
	if expected == ^uint64(0) {
		return current, fmt.Errorf("%w: revision exhausted", ErrInvalid)
	}
	next.Revision++
	if err := s.write(s.path+".bak", current); err != nil {
		return current, err
	}
	if err := s.write(s.path+".pending", current); err != nil {
		return current, err
	}
	if err := s.write(s.path, next); err != nil {
		return current, errors.Join(err, s.write(s.path, current))
	}
	if err := removeSettingsPending(s.path + ".pending"); err != nil {
		return current, errors.Join(err, s.write(s.path, current), s.write(s.path+".pending", current))
	}
	s.mu.Lock()
	s.settings = cloneValue(next)
	s.mu.Unlock()
	return next, nil
}

func healthySensor(r Reading, name string) bool {
	if name == "" {
		return false
	}
	for _, f := range r.Fans {
		if f.Name == name {
			return f.Status == "ok" && f.RPM != nil && !math.IsNaN(*f.RPM) && !math.IsInf(*f.RPM, 0) && *f.RPM > 0
		}
	}
	return false
}

func (s *Service) set(ctx context.Context, fan, duty int) (Result, error) {
	c := s.Config()
	if fan < 1 || fan > 3 || duty < 1 || duty > 100 || duty < c.MinDuty {
		return Result{}, fmt.Errorf("%w: choose fan 1..3 and duty within the verified minimum..100; use automatic for zero", ErrInvalid)
	}
	if !c.ready() || c.FanSensors[fan] == "" {
		return Result{}, fmt.Errorf("%w: profile, identity, automatic restoration, fan mapping and thermal limits must be verified first", ErrInvalid)
	}
	if s.latched != "" || s.storageError != "" {
		return Result{}, fmt.Errorf("%w: return to automatic to clear fallback before manual control", ErrInvalid)
	}
	readCtx, cancel := context.WithTimeout(ctx, s.operationTimeout)
	r, err := s.adapter.Read(readCtx)
	cancel()
	if err != nil || r.Identity != c.ExpectedIdentity || !SupportedIdentity(r.Identity) || len(r.Duties) != 8 || !healthySensor(r, c.FanSensors[fan]) {
		if err == nil {
			err = errors.New("observed identity, duty readback or selected fan RPM does not match verified settings")
		}
		s.publish("degraded", err.Error(), &r)
		if len(s.owned) > 0 {
			s.fail(ctx, "pre-write telemetry failed: "+err.Error())
		}
		return Result{HardwareError: err.Error()}, nil
	}
	if _, exists := s.owned[fan]; !exists {
		next := copyOwned(s.owned)
		next[fan] = c.ExpectedIdentity
		if err := s.write(s.journal, ownership{SchemaVersion: 1, Profile: ProfileID, Fans: next}); err != nil {
			s.storageError = err.Error()
			s.latched = "ownership persistence failed; manual takeover refused"
			// Existing owned fans still need restoration even if this new takeover was refused.
			fallback := s.restoreBounded(ctx)
			s.publish("degraded", s.latched+": "+err.Error(), nil)
			return Result{Restored: fallback.Restored, StorageError: errors.Join(err, errorOrNil(fallback.StorageError)).Error(), HardwareError: fallback.HardwareError}, nil
		}
		s.owned = next
	}
	writeCtx, done := context.WithTimeout(ctx, s.operationTimeout)
	r, err = s.adapter.Set(writeCtx, c.ExpectedIdentity, fan, duty)
	done()
	if err != nil || !healthySensor(r, c.FanSensors[fan]) {
		if err == nil {
			err = errors.New("selected fan RPM is unavailable or zero after target write")
		}
		s.publish("degraded", err.Error(), &r)
		fallback := s.fail(ctx, "target verification failed: "+err.Error())
		fallback.HardwareError = errors.Join(err, errorOrNil(fallback.HardwareError)).Error()
		return fallback, nil
	}
	s.targets[fan] = duty
	s.publish("manual", "target verified by BMC readback; RPM is reported separately", &r)
	return Result{Applied: true, Persisted: true}, nil
}

func errorOrNil(s string) error {
	if s == "" {
		return nil
	}
	return errors.New(s)
}
func (s *Service) restoreBounded(parent context.Context) Result {
	ctx, cancel := context.WithTimeout(parent, s.operationTimeout)
	defer cancel()
	return s.restore(ctx)
}
func (s *Service) restore(ctx context.Context) Result {
	result := Result{Restored: true, Persisted: true}
	if len(s.owned) == 0 {
		// A prior journal commit may have failed after rename. Explicitly clear
		// the durable record before clearing a storage failure latch.
		if s.storageError != "" {
			if err := s.write(s.journal, ownership{SchemaVersion: 1, Profile: ProfileID, Fans: map[int]Identity{}}); err != nil {
				result.Persisted = false
				result.StorageError = err.Error()
				return result
			}
		}
		s.storageError = ""
		return result
	}
	next := copyOwned(s.owned)
	var hardware error
	for _, fan := range sortedFans(s.owned) {
		r, err := s.adapter.Set(ctx, s.owned[fan], fan, 0)
		if err != nil {
			hardware = errors.Join(hardware, fmt.Errorf("restore fan %d: %w", fan, err))
			continue
		}
		delete(next, fan)
		delete(s.targets, fan)
		s.publish("restoring", "verifying automatic restoration", &r)
	}
	if hardware != nil {
		result.Restored = false
		result.HardwareError = hardware.Error()
	}
	if err := s.write(s.journal, ownership{SchemaVersion: 1, Profile: ProfileID, Fans: next}); err != nil {
		s.storageError = err.Error()
		result.Persisted = false
		result.StorageError = err.Error()
	} else {
		s.owned = next
		s.storageError = ""
	}
	return result
}
func (s *Service) fail(ctx context.Context, reason string) Result {
	s.latched = reason
	r := s.restoreBounded(ctx)
	state := "fallback"
	if !r.Restored || !r.Persisted {
		state = "degraded"
	}
	s.publish(state, reason+"; "+r.HardwareError+" "+r.StorageError, nil)
	return r
}
func (s *Service) poll(ctx context.Context) {
	if ctx.Err() != nil {
		return
	}
	if s.latched != "" && len(s.owned) > 0 {
		s.fail(ctx, s.latched)
		return
	}
	readCtx, cancel := context.WithTimeout(ctx, s.operationTimeout)
	r, err := s.adapter.Read(readCtx)
	cancel()
	if err != nil {
		s.publish("offline", err.Error(), &r)
		if len(s.owned) > 0 {
			s.fail(ctx, "IPMI telemetry failed: "+err.Error())
		}
		return
	}
	if s.latched != "" {
		s.publish("fallback", s.latched, &r)
		return
	}
	c := s.Config()
	if len(s.owned) > 0 {
		for fan, id := range s.owned {
			target, ok := s.targets[fan]
			if r.Identity != id || !healthySensor(r, c.FanSensors[fan]) || len(r.Duties) != 8 || !ok || r.Duties[fan-1] != target {
				s.publish("degraded", "owned fan state changed", &r)
				s.fail(ctx, "owned fan identity, target or RPM verification failed")
				return
			}
		}
		s.publish("manual", "target and RPM observation current", &r)
		return
	}
	reason := "monitoring only; configure and verify the board profile before manual control"
	if c.ready() && c.ExpectedIdentity == r.Identity {
		reason = "verified settings loaded; select a fan and explicitly apply a manual target"
	}
	s.publish("monitoring", reason, &r)
}
