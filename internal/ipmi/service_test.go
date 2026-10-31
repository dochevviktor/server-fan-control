package ipmi

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dochevviktor/server-fan-control/internal/config"
)

type serviceFake struct {
	mu                        sync.Mutex
	reading                   Reading
	events                    []struct{ fan, duty int }
	readErr, setErr, resetErr bool
	block                     <-chan struct{}
}

func newServiceFake() *serviceFake {
	rpm := 900.0
	return &serviceFake{reading: Reading{Identity: Identity{Board: "E3C246D2I", Manufacturer: "ASRock Rack", Firmware: "1.00"}, Duties: []int{0, 77, 0, 0, 0, 0, 0, 0}, Fans: []Fan{{Name: "FAN1", RPM: &rpm, Status: "ok"}, {Name: "FAN2", RPM: &rpm, Status: "ok"}, {Name: "FAN3", RPM: &rpm, Status: "ok"}}, CollectedAt: time.Now().UTC()}}
}
func (f *serviceFake) Mock() bool { return true }
func (f *serviceFake) Read(ctx context.Context) (Reading, error) {
	if f.block != nil {
		select {
		case <-f.block:
		case <-ctx.Done():
			return Reading{}, ctx.Err()
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	r := cloneValue(f.reading)
	r.CollectedAt = time.Now().UTC()
	if f.readErr {
		return r, errors.New("BMC unavailable")
	}
	return r, nil
}
func (f *serviceFake) Set(ctx context.Context, id Identity, fan, duty int) (Reading, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return Reading{}, err
	}
	if id != f.reading.Identity {
		return f.reading, errors.New("identity mismatch")
	}
	f.events = append(f.events, struct{ fan, duty int }{fan, duty})
	if duty == 0 && f.resetErr {
		return f.reading, errors.New("restore failed")
	}
	f.reading.Duties[fan-1] = duty
	if duty > 0 && f.setErr {
		return f.reading, errors.New("partial write failure")
	}
	return cloneValue(f.reading), nil
}
func readyService(t *testing.T) (*Service, *serviceFake) {
	t.Helper()
	f := newServiceFake()
	s, err := NewService(f, filepath.Join(t.TempDir(), "ipmi.json"))
	if err != nil {
		t.Fatal(err)
	}
	c := s.Config()
	c.Profile = ProfileID
	c.ExpectedIdentity = f.reading.Identity
	c.MinDuty = 20
	c.AutomaticVerified = true
	c.MappingVerified = true
	c.ThermalVerified = true
	if _, err = s.update(c, c.Revision); err != nil {
		t.Fatal(err)
	}
	return s, f
}

func TestServiceMonitorOnlyAndReadinessGates(t *testing.T) {
	f := newServiceFake()
	s, err := NewService(f, filepath.Join(t.TempDir(), "ipmi.json"))
	if err != nil {
		t.Fatal(err)
	}
	s.poll(context.Background())
	if s.Snapshot().State != "monitoring" || len(f.events) != 0 {
		t.Fatal("default service must only monitor")
	}
	if _, err = s.set(context.Background(), 3, 35); !errors.Is(err, ErrInvalid) {
		t.Fatal("unverified manual target accepted")
	}
	s, f = readyService(t)
	for _, pair := range [][2]int{{0, 35}, {4, 35}, {3, 0}, {3, 19}, {3, 101}} {
		if _, err = s.set(context.Background(), pair[0], pair[1]); !errors.Is(err, ErrInvalid) {
			t.Fatalf("accepted invalid target %v", pair)
		}
	}
	f.reading.Identity.Firmware = "different"
	r, err := s.set(context.Background(), 3, 35)
	if err != nil || r.Applied || r.HardwareError == "" || len(f.events) != 0 {
		t.Fatalf("identity mismatch actuated: %+v %v", r, err)
	}
}

func TestServiceSetPersistsOwnershipPreservesOtherFansAndRestores(t *testing.T) {
	s, f := readyService(t)
	r, err := s.set(context.Background(), 3, 35)
	if err != nil || !r.Applied {
		t.Fatalf("set: %+v %v", r, err)
	}
	owned, err := readOwnership(s.journal)
	if err != nil || owned[3] != f.reading.Identity {
		t.Fatalf("journal: %v %v", owned, err)
	}
	if f.reading.Duties[1] != 77 || s.Snapshot().State != "manual" {
		t.Fatal("unselected fan changed or manual state missing")
	}
	c := s.Config()
	c.MinDuty++
	if _, err = s.update(c, c.Revision); !errors.Is(err, ErrInvalid) {
		t.Fatal("settings changed during ownership")
	}
	r = s.restoreBounded(context.Background())
	if !r.Restored || !r.Persisted || len(s.owned) != 0 || f.reading.Duties[2] != 0 || f.reading.Duties[1] != 77 {
		t.Fatalf("restore: %+v", r)
	}
}

func TestServiceOwnershipFailureRefusesManualAndPartialWriteRestores(t *testing.T) {
	s, f := readyService(t)
	s.write = func(path string, v any) error {
		if path == s.journal {
			return errors.New("disk full")
		}
		return writeDocument(path, v)
	}
	r, _ := s.set(context.Background(), 3, 35)
	if r.Applied || r.StorageError == "" || len(f.events) != 0 {
		t.Fatalf("actuated without journal %+v", r)
	}
	s, f = readyService(t)
	f.setErr = true
	r, _ = s.set(context.Background(), 3, 35)
	if r.Applied || r.HardwareError == "" || !r.Restored || f.reading.Duties[2] != 0 || s.Snapshot().State != "fallback" {
		t.Fatalf("failed partial-write rollback %+v", r)
	}
	if _, err := s.set(context.Background(), 3, 45); !errors.Is(err, ErrInvalid) {
		t.Fatal("fallback did not latch")
	}
}

func TestServiceRestoreRunsDespiteStorageFailureAndRetries(t *testing.T) {
	s, f := readyService(t)
	if r, _ := s.set(context.Background(), 3, 35); !r.Applied {
		t.Fatal(r)
	}
	s.write = func(string, any) error { return errors.New("read only") }
	r := s.restoreBounded(context.Background())
	if !r.Restored || r.Persisted || r.StorageError == "" || f.reading.Duties[2] != 0 || len(s.owned) != 1 {
		t.Fatalf("restore lost pending durable record: %+v", r)
	}
	s.write = writeDocument
	r = s.restoreBounded(context.Background())
	if !r.Restored || !r.Persisted || len(s.owned) != 0 {
		t.Fatal(r)
	}
}

func TestServicePollingLossRestoresAndFailedResetRetries(t *testing.T) {
	s, f := readyService(t)
	if r, _ := s.set(context.Background(), 3, 35); !r.Applied {
		t.Fatal(r)
	}
	f.resetErr = true
	f.reading.Fans[2].RPM = nil
	s.poll(context.Background())
	if s.Snapshot().State != "degraded" || len(s.owned) != 1 {
		t.Fatal("missing RPM failed to latch pending reset")
	}
	f.resetErr = false
	s.poll(context.Background())
	if s.Snapshot().State != "fallback" || len(s.owned) != 0 || f.reading.Duties[2] != 0 {
		t.Fatal("pending reset not retried")
	}
}

func TestServiceRestartRecoversWithoutResumingManual(t *testing.T) {
	s, f := readyService(t)
	if r, _ := s.set(context.Background(), 3, 35); !r.Applied {
		t.Fatal(r)
	}
	restarted, err := NewService(f, s.path)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- restarted.Run(ctx) }()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) && restarted.Snapshot().State != "monitoring" {
		time.Sleep(time.Millisecond)
	}
	if snap := restarted.Snapshot(); snap.State != "monitoring" || len(snap.OwnedFans) != 0 {
		t.Fatalf("restart: %+v", snap)
	}
	cancel()
	if err = <-done; err != nil {
		t.Fatal(err)
	}
	if f.reading.Duties[2] != 0 {
		t.Fatal("manual resumed on restart")
	}
}

func TestServiceWorkerSnapshotResponsiveAndShutdownRestores(t *testing.T) {
	s, f := readyService(t)
	if r, _ := s.set(context.Background(), 3, 35); !r.Applied {
		t.Fatal(r)
	}
	// Begin with recovered ownership, then exercise shutdown after an explicit target.
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.Run(ctx) }()
	opctx, stop := context.WithTimeout(context.Background(), time.Second)
	defer stop()
	if _, err := s.Automatic(opctx); err != nil {
		t.Fatal(err)
	}
	if r, err := s.Set(opctx, 3, 40); err != nil || !r.Applied {
		t.Fatalf("set %+v %v", r, err)
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if f.reading.Duties[2] != 0 {
		t.Fatal("shutdown did not reset")
	}
	if _, err := s.Set(opctx, 3, 40); !errors.Is(err, ErrUnavailable) {
		t.Fatal("stopped service accepted work")
	}
	// A BMC read waits in its own worker; snapshots remain independently readable.
	s, f = readyService(t)
	f.block = make(chan struct{})
	s.operationTimeout = 20 * time.Millisecond
	ctx, cancel = context.WithCancel(context.Background())
	done = make(chan error, 1)
	go func() { done <- s.Run(ctx) }()
	start := time.Now()
	_ = s.Snapshot()
	_ = s.Config()
	if time.Since(start) > 10*time.Millisecond {
		t.Fatal("snapshot waited for hardware")
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestServiceSettingsRevisionAndJournalOnlyRecovery(t *testing.T) {
	s, f := readyService(t)
	c := s.Config()
	if _, err := s.update(c, c.Revision-1); !errors.Is(err, config.ErrConflict) {
		t.Fatal("stale revision accepted")
	}
	if r, _ := s.set(context.Background(), 3, 35); !r.Applied {
		t.Fatal(r)
	}
	if err := os.WriteFile(s.path, []byte("broken"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := NewService(f, s.path); err == nil {
		t.Fatal("bad settings accepted")
	}
	if err := RestorePending(f, s.path); err != nil {
		t.Fatal(err)
	}
	if f.reading.Duties[2] != 0 {
		t.Fatal("bad settings prevented journal recovery")
	}
	if data, _ := os.ReadFile(s.path); string(data) != "broken" {
		t.Fatal("recovery rewrote invalid settings")
	}
	if err := os.WriteFile(s.journal, []byte(`{"schema_version":1,"profile":"asrock-e3c246d2i","fans":{"8":{"board":"E3C246D2I","manufacturer":"ASRock Rack","firmware":"1"}}}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := RestorePending(f, s.path); err == nil || !strings.Contains(err.Error(), "invalid") {
		t.Fatal("invalid recovery fan accepted")
	}
}

func TestServiceSettingsUncertainCommitRollsBackOnRestart(t *testing.T) {
	s, f := readyService(t)
	before := s.Config()
	changed := cloneValue(before)
	changed.MinDuty = 45
	s.write = func(path string, value any) error {
		if path == s.path {
			// Simulate a successful rename followed by a failed directory sync,
			// and an unavailable rollback write until the next process starts.
			if settings, ok := value.(Settings); ok && settings.Revision > before.Revision {
				if err := writeDocument(path, value); err != nil {
					return err
				}
			}
			return errors.New("directory sync failed")
		}
		return writeDocument(path, value)
	}
	if _, err := s.update(changed, before.Revision); err == nil {
		t.Fatal("uncertain commit reported success")
	}
	if s.Config().MinDuty != before.MinDuty {
		t.Fatal("failed settings commit changed live verification")
	}
	restarted, err := NewService(f, s.path)
	if err != nil {
		t.Fatal(err)
	}
	if restarted.Config().MinDuty != before.MinDuty || restarted.Config().Revision != before.Revision {
		t.Fatal("restart did not roll back pending settings")
	}
	if _, err = os.Stat(s.path + ".pending"); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("pending rollback marker retained")
	}
	if err = os.WriteFile(s.path, []byte(`{}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = NewService(f, s.path); !errors.Is(err, ErrInvalid) {
		t.Fatal("settings without version/revision accepted")
	}
}
