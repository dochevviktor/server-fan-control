package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"syscall"

	"github.com/dochevviktor/nvidia-fan-control/internal/curve"
)

var ErrConflict = errors.New("configuration revision conflict")
var ErrLocked = errors.New("another instance owns this data directory")

type Store struct {
	mu      sync.Mutex
	path    string
	current Config
	lock    *os.File
}

func decode(data []byte, v any) error {
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if err := d.Decode(v); err != nil {
		return err
	}
	var extra any
	if err := d.Decode(&extra); err != io.EOF {
		return fmt.Errorf("expected a single JSON document")
	}
	return nil
}
func Open(path string) (store *Store, err error) {
	path, err = filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	if err = os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, err
	}
	lock, err := openLock(filepath.Join(filepath.Dir(path), ".fan-control.lock"))
	if err != nil {
		return nil, err
	}
	if err = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		lock.Close()
		return nil, fmt.Errorf("%w: %v", ErrLocked, err)
	}
	defer func() {
		if err != nil {
			_ = syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
			_ = lock.Close()
		}
	}()
	s := &Store{path: path, lock: lock}
	// An interrupted/failed save must not activate a policy on restart.
	if pending, readErr := os.ReadFile(path + ".pending"); readErr == nil {
		var previous Config
		if err = decode(pending, &previous); err != nil {
			return nil, fmt.Errorf("pending config recovery: %w", err)
		}
		if err = Validate(previous); err != nil {
			return nil, err
		}
		if err = AtomicWrite(path, pending); err != nil {
			return nil, err
		}
		if err = removeSynced(path + ".pending"); err != nil {
			return nil, err
		}
	} else if !errors.Is(readErr, os.ErrNotExist) {
		return nil, readErr
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		s.current = Default()
		err = s.write(s.current)
		return s, err
	}
	if err != nil {
		return nil, err
	}
	if len(data) > 1024*1024 {
		return nil, fmt.Errorf("configuration exceeds 1 MiB")
	}
	var header struct {
		SchemaVersion int `json:"schema_version"`
	}
	if err = json.Unmarshal(data, &header); err != nil {
		return nil, err
	}
	if header.SchemaVersion == 0 {
		var legacy struct {
			TimeToUpdate      int           `json:"time_to_update"`
			TemperatureRanges []curve.Range `json:"temperature_ranges"`
		}
		if err = decode(data, &legacy); err != nil {
			return nil, fmt.Errorf("legacy configuration: %w", err)
		}
		s.current = Default()
		s.current.SamplingSeconds = legacy.TimeToUpdate
		s.current.FreshnessSeconds = max(15, legacy.TimeToUpdate*3)
		s.current.Curves = map[string]curve.Curve{"Imported legacy": {Type: "step", Ranges: legacy.TemperatureRanges}}
		if err = Validate(s.current); err != nil {
			return nil, fmt.Errorf("legacy migration: %w", err)
		}
		backup := path + ".legacy.bak"
		if existing, readErr := os.ReadFile(backup); readErr == nil {
			if !bytes.Equal(existing, data) {
				return nil, fmt.Errorf("legacy backup differs from source; preserve and resolve it before migration")
			}
		} else if errors.Is(readErr, os.ErrNotExist) {
			if err = AtomicWrite(backup, data); err != nil {
				return nil, fmt.Errorf("preserve legacy backup: %w", err)
			}
		} else {
			return nil, readErr
		}
		err = s.write(s.current)
		return s, err
	}
	if err = decode(data, &s.current); err != nil {
		return nil, err
	}
	if err = Validate(s.current); err != nil {
		return nil, err
	}
	if s.current.Revision == 0 {
		return nil, fmt.Errorf("configuration revision must be positive")
	}
	return s, nil
}
func (s *Store) Path() string    { return s.path }
func (s *Store) Current() Config { s.mu.Lock(); defer s.mu.Unlock(); return Clone(s.current) }
func (s *Store) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.lock == nil {
		return nil
	}
	err := errors.Join(syscall.Flock(int(s.lock.Fd()), syscall.LOCK_UN), s.lock.Close())
	s.lock = nil
	return err
}
func (s *Store) Update(next Config, expected uint64) (Config, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if expected != s.current.Revision || next.Revision != expected {
		return Clone(s.current), ErrConflict
	}
	if err := Validate(next); err != nil {
		return Clone(s.current), err
	}
	if expected == ^uint64(0) {
		return Clone(s.current), fmt.Errorf("revision exhausted")
	}
	next = Clone(next)
	next.Revision = expected + 1
	old, _ := json.MarshalIndent(s.current, "", "  ")
	if err := AtomicWrite(s.path+".bak", append(old, '\n')); err != nil {
		return Clone(s.current), fmt.Errorf("save last-good backup: %w", err)
	}
	if err := AtomicWrite(s.path+".pending", append(old, '\n')); err != nil {
		return Clone(s.current), fmt.Errorf("prepare configuration transaction: %w", err)
	}
	if err := s.write(next); err != nil {
		return Clone(s.current), errors.Join(fmt.Errorf("save configuration: %w", err), AtomicWrite(s.path, append(old, '\n')))
	}
	if err := removeSynced(s.path + ".pending"); err != nil {
		rollbackErr := AtomicWrite(s.path, append(old, '\n'))
		markerErr := AtomicWrite(s.path+".pending", append(old, '\n'))
		return Clone(s.current), errors.Join(fmt.Errorf("complete configuration transaction: %w", err), rollbackErr, markerErr)
	}
	s.current = next
	return Clone(next), nil
}
func (s *Store) write(c Config) error {
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	return AtomicWrite(s.path, append(data, '\n'))
}

// AtomicWrite keeps temporary files on the same filesystem and syncs both the
// file and containing directory. A sync failure is reported to the caller.
func AtomicWrite(path string, data []byte) error {
	dir := filepath.Dir(path)
	f, err := os.CreateTemp(dir, ".fan-control-*")
	if err != nil {
		return err
	}
	name := f.Name()
	defer os.Remove(name)
	if err = f.Chmod(0600); err == nil {
		_, err = f.Write(data)
	}
	if err == nil {
		err = f.Sync()
	}
	err = errors.Join(err, f.Close())
	if err != nil {
		return err
	}
	if err = os.Rename(name, path); err != nil {
		return err
	}
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer d.Close()
	err = d.Sync()
	if errors.Is(err, syscall.EINVAL) || errors.Is(err, syscall.ENOTSUP) {
		return nil
	}
	return err
}
func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// LockDirectory is used for journal-only recovery when configuration is invalid.
// It takes the same instance lock as Open without interpreting desired policies.
func LockDirectory(configPath string) (func() error, error) {
	path, err := filepath.Abs(configPath)
	if err != nil {
		return nil, err
	}
	f, err := openLock(filepath.Join(filepath.Dir(path), ".fan-control.lock"))
	if err != nil {
		return nil, err
	}
	if err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		return nil, fmt.Errorf("%w: %v", ErrLocked, err)
	}
	return func() error { return errors.Join(syscall.Flock(int(f.Fd()), syscall.LOCK_UN), f.Close()) }, nil
}

func removeSynced(path string) error {
	if err := os.Remove(path); err != nil {
		return err
	}
	d, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer d.Close()
	err = d.Sync()
	if errors.Is(err, syscall.EINVAL) || errors.Is(err, syscall.ENOTSUP) {
		return nil
	}
	return err
}

func openLock(path string) (*os.File, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err == nil {
		return f, nil
	}
	// Linux permits flock on a read-only descriptor. Existing ownership must
	// still be restorable after a data filesystem is remounted read-only.
	return os.Open(path)
}
