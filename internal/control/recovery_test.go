package control

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/dochevviktor/server-fan-control/internal/config"
)

func TestRecoveryDoesNotNeedValidConfiguration(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	if err := os.WriteFile(path, []byte("invalid JSON"), 0600); err != nil {
		t.Fatal(err)
	}
	journal := filepath.Join(dir, "ownership.mock.json")
	if err := writeJournal(journal, map[fanKey]bool{{testUUID, 0}: true, {testUUID, 1}: true}); err != nil {
		t.Fatal(err)
	}
	a := newFakeAdapter()
	if err := RestorePending(a, path); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(a.events, []string{"init", "reset:0", "reset:1", "close"}) {
		t.Fatal(a.events)
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "invalid JSON" {
		t.Fatal("recovery changed invalid configuration", string(data), err)
	}
	owned, err := readJournal(journal)
	if err != nil || len(owned) != 0 {
		t.Fatal(owned, err)
	}
}
func TestRecoveryCannotResetAnActivePeer(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	store, err := config.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err = writeJournal(filepath.Join(dir, "ownership.mock.json"), map[fanKey]bool{{testUUID, 0}: true}); err != nil {
		t.Fatal(err)
	}
	a := newFakeAdapter()
	if err = RestorePending(a, path); !errors.Is(err, config.ErrLocked) {
		t.Fatal(err)
	}
	if len(a.events) != 0 {
		t.Fatal("recovery touched active peer", a.events)
	}
}
func TestRecoveryRetainsFailedFansAndStillClosesLibrary(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	journal := filepath.Join(dir, "ownership.mock.json")
	if err := writeJournal(journal, map[fanKey]bool{{testUUID, 0}: true, {testUUID, 1}: true}); err != nil {
		t.Fatal(err)
	}
	a := newFakeAdapter()
	a.resetError[0] = true
	if err := RestorePending(a, path); err == nil {
		t.Fatal("failed reset reported success")
	}
	if !reflect.DeepEqual(a.events, []string{"init", "reset:0", "reset:1", "close"}) {
		t.Fatal(a.events)
	}
	owned, err := readJournal(journal)
	if err != nil || len(owned) != 1 || !owned[fanKey{testUUID, 0}] {
		t.Fatal(owned, err)
	}
}
func TestMalformedJournalIsNeverDiscarded(t *testing.T) {
	s, a, _ := newTestService(t, false)
	if err := os.WriteFile(s.journalPath, []byte(`{"schema_version":1,"fans":{"GPU-TEST":[-1]}}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := RestorePending(a, s.store.Path()); err == nil {
		t.Fatal("invalid journal accepted")
	}
	if len(a.events) != 0 {
		t.Fatal(a.events)
	}
}
