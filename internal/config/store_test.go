package config

import (
	"encoding/json"
	"errors"
	"github.com/dochevviktor/server-fan-control/internal/curve"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func openTest(t *testing.T) *Store {
	t.Helper()
	s, e := Open(filepath.Join(t.TempDir(), "config.json"))
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { s.Close() })
	return s
}
func TestMigrationPreservesLegacyAndDoesNotManageDevices(t *testing.T) {
	p := filepath.Join(t.TempDir(), "config.json")
	b := []byte(`{"time_to_update":5,"temperature_ranges":[{"min_temperature":0,"max_temperature":40,"fan_speed":30,"hysteresis":3},{"min_temperature":40,"max_temperature":200,"fan_speed":100,"hysteresis":3}]}`)
	os.WriteFile(p, b, 0600)
	s, e := Open(p)
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	c := s.Current()
	if len(c.GPUs) != 0 || c.SamplingSeconds != 5 || curve.Evaluate(c.Curves["Imported legacy"], 40) != 30 || curve.Evaluate(c.Curves["Imported legacy"], 41) != 100 {
		t.Fatalf("invalid migration %+v", c)
	}
	backup, e := os.ReadFile(p + ".legacy.bak")
	if e != nil || string(backup) != string(b) {
		t.Fatal("legacy backup missing")
	}
}
func TestConcurrentUpdatesOnlyOneWinsAndBackupRetained(t *testing.T) {
	s := openTest(t)
	initial := s.Current()
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); _, e := s.Update(initial, 1); results <- e }()
	}
	wg.Wait()
	close(results)
	ok, conflicts := 0, 0
	for e := range results {
		if e == nil {
			ok++
		} else if errors.Is(e, ErrConflict) {
			conflicts++
		} else {
			t.Fatal(e)
		}
	}
	if ok != 1 || conflicts != 1 || s.Current().Revision != 2 {
		t.Fatalf("ok %d conflicts %d", ok, conflicts)
	}
	if _, e := os.Stat(s.Path() + ".bak"); e != nil {
		t.Fatal(e)
	}
}
func TestFailedPersistencePreservesState(t *testing.T) {
	s := openTest(t)
	before := s.Current()
	if e := os.Mkdir(s.Path()+".bak", 0700); e != nil {
		t.Fatal(e)
	}
	next := Clone(before)
	next.SamplingSeconds = 10
	if _, e := s.Update(next, next.Revision); e == nil {
		t.Fatal("expected backup write failure")
	}
	if s.Current().Revision != before.Revision || s.Current().SamplingSeconds != before.SamplingSeconds {
		t.Fatal("failed save changed live config")
	}
}
func TestDirectoryLockAndMalformedConfig(t *testing.T) {
	s := openTest(t)
	if second, e := Open(s.Path()); e == nil {
		second.Close()
		t.Fatal("duplicate instance accepted")
	}
	p := filepath.Join(t.TempDir(), "config.json")
	b := []byte(`{"schema_version":9}`)
	os.WriteFile(p, b, 0600)
	if _, e := Open(p); e == nil {
		t.Fatal("unknown schema accepted")
	}
	after, _ := os.ReadFile(p)
	if string(after) != string(b) {
		t.Fatal("malformed config overwritten")
	}
}
func TestConfigSnapshotsDoNotAlias(t *testing.T) {
	s := openTest(t)
	c := s.Current()
	delete(c.Curves, "Balanced")
	if len(s.Current().Curves) != 1 {
		t.Fatal("snapshot mutated config")
	}
}
func TestActivePolicyRequiresConfirmedLimitsAndFullCooling(t *testing.T) {
	c := Default()
	p := Policy{Mode: "curve", Curve: "Balanced", CriticalTemp: 90}
	c.GPUs["GPU-test"] = p
	if Validate(c) == nil {
		t.Fatal("unconfirmed accepted")
	}
	p.LimitsConfirmed = true
	c.GPUs["GPU-test"] = p
	if e := Validate(c); e != nil {
		t.Fatal(e)
	}
	cv := c.Curves["Balanced"]
	cv.Points[2].Duty = 90
	c.Curves["Balanced"] = cv
	if Validate(c) == nil {
		t.Fatal("no full cooling accepted")
	}
}

func TestInterruptedMigrationCanRetry(t *testing.T) {
	p := filepath.Join(t.TempDir(), "config.json")
	data := []byte(`{"time_to_update":5,"temperature_ranges":[{"min_temperature":0,"max_temperature":40,"fan_speed":30,"hysteresis":3},{"min_temperature":40,"max_temperature":200,"fan_speed":100,"hysteresis":3}]}`)
	os.WriteFile(p, data, 0600)
	os.WriteFile(p+".legacy.bak", data, 0600)
	s, err := Open(p)
	if err != nil {
		t.Fatal(err)
	}
	s.Close()
}
func TestPendingTransactionRollsBackBeforeStartup(t *testing.T) {
	p := filepath.Join(t.TempDir(), "config.json")
	old := Default()
	pending, _ := json.Marshal(old)
	newConfig := Clone(old)
	newConfig.Revision = 2
	newConfig.SamplingSeconds = 10
	next, _ := json.Marshal(newConfig)
	os.WriteFile(p, next, 0600)
	os.WriteFile(p+".pending", pending, 0600)
	s, err := Open(p)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if s.Current().Revision != 1 || s.Current().SamplingSeconds != 5 {
		t.Fatal("incomplete transaction became active")
	}
	if _, err := os.Stat(p + ".pending"); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("pending transaction not cleared")
	}
}

func TestRecoveryLockCanOpenExistingReadOnlyLockfile(t *testing.T) {
	dir := t.TempDir()
	lockPath := filepath.Join(dir, ".fan-control.lock")
	if err := os.WriteFile(lockPath, nil, 0400); err != nil {
		t.Fatal(err)
	}
	release, err := LockDirectory(filepath.Join(dir, "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	if second, err := LockDirectory(filepath.Join(dir, "config.json")); !errors.Is(err, ErrLocked) {
		if second != nil {
			second()
		}
		t.Fatalf("lock exclusivity lost: %v", err)
	}
}
