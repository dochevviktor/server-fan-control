package control

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dochevviktor/server-fan-control/internal/config"
	"github.com/dochevviktor/server-fan-control/internal/curve"
	"github.com/dochevviktor/server-fan-control/internal/gpu"
)

const testUUID = "GPU-TEST"

type fakeStore struct {
	mu      sync.Mutex
	cfg     config.Config
	path    string
	failure bool
}

func (f *fakeStore) Current() config.Config {
	f.mu.Lock()
	defer f.mu.Unlock()
	return config.Clone(f.cfg)
}
func (f *fakeStore) Path() string { return f.path }
func (f *fakeStore) Update(c config.Config, rev uint64) (config.Config, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if rev != f.cfg.Revision || c.Revision != rev {
		return config.Clone(f.cfg), config.ErrConflict
	}
	if f.failure {
		return config.Clone(f.cfg), errors.New("read-only configuration filesystem")
	}
	if err := config.Validate(c); err != nil {
		return config.Clone(f.cfg), err
	}
	c.Revision++
	f.cfg = config.Clone(c)
	return config.Clone(c), nil
}

type fakeAdapter struct {
	now            time.Time
	devices        []gpu.Device
	temperature    float64
	badTemperature bool
	stale          bool
	readError      bool
	readAdvance    time.Duration
	initError      bool
	setError       map[int]bool
	resetError     map[int]bool
	duties         map[int]int
	events         []string
}

func newFakeAdapter() *fakeAdapter {
	return &fakeAdapter{now: time.Now().UTC(), devices: []gpu.Device{{UUID: testUUID, Name: "Test GPU", Fans: []int{0, 1}, Controllable: true, MinDuty: 20, MaxDuty: 100, BoundsKnown: true}}, temperature: 50, setError: map[int]bool{}, resetError: map[int]bool{}, duties: map[int]int{}}
}
func (f *fakeAdapter) Mock() bool { return true }
func (f *fakeAdapter) Init() error {
	f.events = append(f.events, "init")
	if f.initError {
		return errors.New("library missing")
	}
	return nil
}
func (f *fakeAdapter) Close() error                    { f.events = append(f.events, "close"); return nil }
func (f *fakeAdapter) Discover() ([]gpu.Device, error) { return f.devices, nil }
func (f *fakeAdapter) Read(uuid string) (gpu.Telemetry, error) {
	if f.readError {
		return gpu.Telemetry{}, errors.New("GPU lost")
	}
	at := f.now
	if f.stale {
		at = at.Add(-time.Minute)
	}
	temp := gpu.Reading(f.temperature, "°C", at)
	if f.badTemperature {
		temp.Value = nil
		temp.Status = "error"
		temp.Reason = "sensor unavailable"
	}
	fans := []gpu.FanReading{}
	for _, fan := range []int{0, 1} {
		duty, manual := f.duties[fan]
		fans = append(fans, gpu.FanReading{Index: fan, Reported: gpu.Reading(float64(duty), "%", at), Manual: &manual})
	}
	f.now = f.now.Add(f.readAdvance)
	return gpu.Telemetry{Metrics: map[string]gpu.Metric{"temperature_c": temp}, Fans: fans}, nil
}
func (f *fakeAdapter) Set(uuid string, fan, duty int) error {
	f.events = append(f.events, fmt.Sprintf("set:%d:%d", fan, duty))
	if f.setError[fan] {
		return errors.New("set denied")
	}
	f.duties[fan] = duty
	return nil
}
func (f *fakeAdapter) Reset(uuid string, fan int) error {
	f.events = append(f.events, fmt.Sprintf("reset:%d", fan))
	if f.resetError[fan] {
		return errors.New("reset denied")
	}
	delete(f.duties, fan)
	return nil
}
func policy() config.Policy {
	return config.Policy{Mode: "curve", Curve: "test", MinDuty: 20, CriticalTemp: 90, Hysteresis: 3, CoolingHoldSeconds: 2, LimitsConfirmed: true}
}
func newTestService(t *testing.T, active bool) (*Service, *fakeAdapter, *fakeStore) {
	t.Helper()
	a := newFakeAdapter()
	cfg := config.Default()
	cfg.Curves = map[string]curve.Curve{"test": {Type: "step", Points: []curve.Point{{Temperature: 30, Duty: 30}, {Temperature: 60, Duty: 60}, {Temperature: 80, Duty: 100}}}}
	if active {
		cfg.GPUs[testUUID] = policy()
	}
	store := &fakeStore{cfg: cfg, path: filepath.Join(t.TempDir(), "config.json")}
	s, err := newService(a, store)
	if err != nil {
		t.Fatal(err)
	}
	return s, a, store
}
func countEvents(events []string, prefix string) int {
	count := 0
	for _, e := range events {
		if strings.HasPrefix(e, prefix) {
			count++
		}
	}
	return count
}
func tick(s *Service, a *fakeAdapter) {
	s.clock = func() time.Time { return a.now }
	s.tick(a.now)
	a.now = a.now.Add(time.Second)
}
func TestAutomaticNeverTakesOverAndMonitoringDevicesRemainVisible(t *testing.T) {
	s, a, _ := newTestService(t, false)
	a.devices = append(a.devices, gpu.Device{UUID: "GPU-PASSIVE", Name: "Passive", Fans: []int{}, ControlReason: "no fans", MaxDuty: 100})
	tick(s, a)
	if countEvents(a.events, "set:") != 0 || countEvents(a.events, "reset:") != 0 {
		t.Fatal(a.events)
	}
	if len(s.GPUs()) != 2 || s.GPUs()[0].State != "monitoring" {
		t.Fatal(s.GPUs())
	}
	if s.GPUs()[1].LastSuccessAt == nil {
		t.Fatal("monitoring temperature needs a success timestamp")
	}
}
func TestJournalPrecedesEveryTakeoverAndCoalesces(t *testing.T) {
	s, a, _ := newTestService(t, true)
	s.saveJournal = func(path string, owned map[fanKey]bool) error {
		a.events = append(a.events, "journal")
		return writeJournal(path, owned)
	}
	tick(s, a)
	tick(s, a)
	if !reflect.DeepEqual(a.events, []string{"init", "journal", "set:0:30", "set:1:30"}) {
		t.Fatal(a.events)
	}
	if s.Snapshot().AppliedRevision != 1 {
		t.Fatal(s.Snapshot())
	}
	// A driver-reported return to automatic defeats command coalescing.
	delete(a.duties, 1)
	tick(s, a)
	if countEvents(a.events, "set:1:") != 2 {
		t.Fatal(a.events)
	}
}
func TestRefuseTakeoverIfInitialJournalWriteFails(t *testing.T) {
	s, a, _ := newTestService(t, true)
	s.saveJournal = func(string, map[fanKey]bool) error { return errors.New("full filesystem") }
	tick(s, a)
	if countEvents(a.events, "set:") != 0 || countEvents(a.events, "reset:") != 0 {
		t.Fatal(a.events)
	}
	if s.GPUs()[0].State != "degraded" || s.Snapshot().AppliedRevision != 0 {
		t.Fatal(s.GPUs())
	}
}
func TestPartialWriteRestoresAllAttemptedFansAndRetainsFailedReset(t *testing.T) {
	s, a, _ := newTestService(t, true)
	a.setError[1] = true
	a.resetError[1] = true
	tick(s, a)
	expected := []string{"init", "set:0:30", "set:1:30", "reset:0", "reset:1"}
	if !reflect.DeepEqual(a.events, expected) {
		t.Fatal(a.events)
	}
	owned, err := readJournal(s.journalPath)
	if err != nil || len(owned) != 1 || !owned[fanKey{testUUID, 1}] {
		t.Fatal(owned, err)
	}
	if s.GPUs()[0].State != "degraded" || s.GPUs()[0].Fans[1].State != "failed" {
		t.Fatal(s.GPUs())
	}
	a.resetError[1] = false
	tick(s, a)
	if len(s.owned) != 0 || countEvents(a.events, "set:") != 2 {
		t.Fatal(s.owned, a.events)
	}
	// Failed transition stays latched after restoration succeeds.
	if _, err := s.update(s.Config(), s.cfg.Revision); !errors.Is(err, ErrInvalidPolicy) {
		t.Fatal(err)
	}
}
func TestInvalidTemperatureHoldsThenFallsBackAndNeverCommandsZero(t *testing.T) {
	s, a, _ := newTestService(t, true)
	tick(s, a)
	a.badTemperature = true
	tick(s, a)
	if countEvents(a.events, "reset:") != 0 {
		t.Fatal("fallback before configured failure limit", a.events)
	}
	tick(s, a)
	tick(s, a)
	if countEvents(a.events, "set:") != 2 || countEvents(a.events, "reset:") != 2 {
		t.Fatal(a.events)
	}
	if s.GPUs()[0].Metrics["temperature_c"].Value != nil {
		t.Fatal("invalid temperature became a value")
	}
}
func TestStaleSampleImmediatelyFallsBack(t *testing.T) {
	s, a, _ := newTestService(t, true)
	tick(s, a)
	a.stale = true
	a.now = a.now.Add(20 * time.Second)
	tick(s, a)
	if countEvents(a.events, "reset:") != 2 || countEvents(a.events, "set:") != 2 {
		t.Fatal(a.events)
	}
}
func TestCriticalDemandBypassesHoldThenRequiresCoolingHold(t *testing.T) {
	s, a, _ := newTestService(t, true)
	tick(s, a)
	a.temperature = 95
	tick(s, a)
	if a.duties[0] != 100 || a.duties[1] != 100 {
		t.Fatal(a.duties)
	}
	a.temperature = 50
	tick(s, a)
	if a.duties[0] != 100 {
		t.Fatal("reduced immediately after critical override")
	}
	tick(s, a)
	tick(s, a)
	if a.duties[0] != 30 {
		t.Fatal(a.duties)
	}
}
func TestLostGPUAttemptsRestoreAndStaysVisibleOffline(t *testing.T) {
	s, a, _ := newTestService(t, true)
	tick(s, a)
	a.devices = nil
	a.resetError[0] = true
	tick(s, a)
	if len(s.GPUs()) != 1 || s.GPUs()[0].State != "offline" || len(s.owned) != 1 {
		t.Fatal(s.GPUs(), s.owned)
	}
	if s.GPUs()[0].Metrics["temperature_c"].Value != nil {
		t.Fatal("offline telemetry must be null")
	}
}
func TestReturnAutomaticWorksWhenConfigAndJournalCannotBeWritten(t *testing.T) {
	s, a, store := newTestService(t, true)
	tick(s, a)
	store.failure = true
	s.saveJournal = func(string, map[fanKey]bool) error { return errors.New("journal filesystem read-only") }
	result, err := s.automatic(testUUID)
	if err != nil || !result.Restored || result.Persisted || result.StorageError == "" {
		t.Fatal(result, err)
	}
	if len(a.duties) != 0 || len(s.owned) != 2 {
		t.Fatal(a.duties, s.owned)
	}
	tick(s, a)
	if countEvents(a.events, "set:") != 2 {
		t.Fatal("old curve reapplied", a.events)
	}
	store.failure = false
	s.saveJournal = writeJournal
	if _, err := s.update(s.Config(), s.cfg.Revision); !errors.Is(err, ErrInvalidPolicy) {
		t.Fatal("unrelated save must not rearm old persisted curve", err)
	}
	result, err = s.automatic(testUUID)
	if err != nil || !result.Restored || !result.Persisted {
		t.Fatal(result, err)
	}
	next := s.Config()
	next.GPUs[testUUID] = policy()
	if _, err = s.update(next, next.Revision); err != nil {
		t.Fatal(err)
	}
	if countEvents(a.events, "set:") != 4 {
		t.Fatal("explicit activation did not rearm", a.events)
	}
}
func TestRestartRestoresJournalForRemovedConfigurationBeforeNewWrites(t *testing.T) {
	s, a, store := newTestService(t, true)
	tick(s, a)
	// Simulate the crash window: removal saved but the device was not reset.
	next := s.Config()
	delete(next.GPUs, testUUID)
	if _, err := store.Update(next, next.Revision); err != nil {
		t.Fatal(err)
	}
	restarted, err := newService(a, store)
	if err != nil {
		t.Fatal(err)
	}
	a.events = nil
	tick(restarted, a)
	if !reflect.DeepEqual(a.events, []string{"init", "reset:0", "reset:1"}) || len(restarted.owned) != 0 {
		t.Fatal(a.events, restarted.owned)
	}
}
func TestRemovingSelectedFanRestoresBeforeReapplyingRemainingFan(t *testing.T) {
	s, a, _ := newTestService(t, true)
	tick(s, a)
	a.events = nil
	next := s.Config()
	p := next.GPUs[testUUID]
	p.Fans = []int{0}
	next.GPUs[testUUID] = p
	if _, err := s.update(next, next.Revision); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(a.events, []string{"reset:0", "reset:1", "set:0:30"}) {
		t.Fatal(a.events)
	}
}
func TestSnapshotCopiesAndBoundedHistory(t *testing.T) {
	s, a, _ := newTestService(t, false)
	tick(s, a)
	snapshot := s.GPUs()
	*snapshot[0].Metrics["temperature_c"].Value = 999
	if *s.GPUs()[0].Metrics["temperature_c"].Value == 999 {
		t.Fatal("snapshot is not isolated")
	}
	a.now = a.now.Add(31 * time.Minute)
	tick(s, a)
	if len(s.History(testUUID)) != 1 {
		t.Fatal(len(s.History(testUUID)))
	}
}
func TestShutdownRestoresBeforeClosingAdapter(t *testing.T) {
	s, a, _ := newTestService(t, true)
	ctx, cancel := context.WithCancel(context.Background())
	finished := make(chan error, 1)
	go func() { finished <- s.Run(ctx) }()
	deadline := time.Now().Add(time.Second)
	for s.Snapshot().AppliedRevision != 1 {
		if time.Now().After(deadline) {
			t.Fatal("worker did not start")
		}
		time.Sleep(time.Millisecond)
	}
	cancel()
	if err := <-finished; err != nil {
		t.Fatal(err)
	}
	n := len(a.events)
	if n < 3 || !reflect.DeepEqual(a.events[n-3:], []string{"reset:0", "reset:1", "close"}) {
		t.Fatal(a.events)
	}
	requestCtx, stop := context.WithTimeout(context.Background(), time.Second)
	defer stop()
	if _, err := s.Automatic(requestCtx, testUUID); !errors.Is(err, ErrUnavailable) {
		t.Fatal(err)
	}
}

func TestReadLatencyIsCountedAgainstFreshnessBudget(t *testing.T) {
	s, a, _ := newTestService(t, true)
	tick(s, a)
	a.readAdvance = 20 * time.Second
	tick(s, a)
	if countEvents(a.events, "reset:") != 2 {
		t.Fatal("slow read concealed stale temperature", a.events)
	}
	if s.GPUs()[0].Metrics["temperature_c"].Value != nil {
		t.Fatal("stale read retained a usable value")
	}
}
func TestUnownedExternalManualFanIsNotResetOrClaimedAutomatic(t *testing.T) {
	s, a, _ := newTestService(t, false)
	a.duties[0] = 55
	tick(s, a)
	if s.GPUs()[0].State != "monitoring" || s.GPUs()[0].Fans[0].State != "external_manual" {
		t.Fatal(s.GPUs())
	}
	result, err := s.automatic(testUUID)
	if err != nil || result.Restored || result.HardwareError == "" {
		t.Fatal(result, err)
	}
	if countEvents(a.events, "reset:") != 0 || a.duties[0] != 55 {
		t.Fatal(a.events, a.duties)
	}
}
func TestPersistedInvalidHardwareSelectionNeverTakesOver(t *testing.T) {
	s, a, _ := newTestService(t, true)
	p := s.cfg.GPUs[testUUID]
	p.Fans = []int{12}
	s.cfg.GPUs[testUUID] = p
	tick(s, a)
	if countEvents(a.events, "set:") != 0 || len(s.owned) != 0 {
		t.Fatal(a.events, s.owned)
	}
	if s.GPUs()[0].State != "degraded" {
		t.Fatal(s.GPUs())
	}
}

func TestAlreadyCanceledWorkerNeverStartsManualControl(t *testing.T) {
	s, a, _ := newTestService(t, true)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := s.Run(ctx); err != nil {
		t.Fatal(err)
	}
	if countEvents(a.events, "init") != 0 || countEvents(a.events, "set:") != 0 {
		t.Fatal(a.events)
	}
}

func TestRestoringSelectedFanDoesNotClaimExternalFanIsAutomatic(t *testing.T) {
	s, a, _ := newTestService(t, true)
	p := s.cfg.GPUs[testUUID]
	p.Fans = []int{0}
	s.cfg.GPUs[testUUID] = p
	a.duties[1] = 66
	tick(s, a)
	result, err := s.automatic(testUUID)
	if err != nil || result.Restored || result.HardwareError == "" {
		t.Fatal(result, err)
	}
	if s.devices[testUUID].snapshot.State != "monitoring" || a.duties[1] != 66 || countEvents(a.events, "reset:1") != 0 {
		t.Fatal(s.devices[testUUID].snapshot, a.events)
	}
}
func TestSamplingIntervalUpdateReschedulesNextRead(t *testing.T) {
	s, a, store := newTestService(t, false)
	store.cfg.SamplingSeconds = 60
	store.cfg.FreshnessSeconds = 180
	s.cfg = store.Current()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	finished := make(chan error, 1)
	go func() { finished <- s.Run(ctx) }()
	deadline := time.Now().Add(time.Second)
	for !s.Snapshot().Available {
		if time.Now().After(deadline) {
			t.Fatal("worker did not start")
		}
		time.Sleep(time.Millisecond)
	}
	next := s.Config()
	next.SamplingSeconds = 1
	requestCtx, stop := context.WithTimeout(context.Background(), time.Second)
	defer stop()
	if _, err := s.Update(requestCtx, next, next.Revision); err != nil {
		t.Fatal(err)
	}
	afterUpdate := s.Snapshot().UpdatedAt
	deadline = time.Now().Add(2500 * time.Millisecond)
	for !s.Snapshot().UpdatedAt.After(afterUpdate) {
		if time.Now().After(deadline) {
			t.Fatal("new interval did not take effect")
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	if err := <-finished; err != nil {
		t.Fatal(err)
	}
	_ = a
}
