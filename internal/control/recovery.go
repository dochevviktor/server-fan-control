package control

import (
	"errors"
	"fmt"
	"path/filepath"
	"sort"

	"github.com/dochevviktor/server-fan-control/internal/config"
	"github.com/dochevviktor/server-fan-control/internal/gpu"
)

// RestorePending is limited to journaled fan ownership. It can recover defaults
// even when desired configuration cannot be decoded, but never takes over a fan.
// The directory lock prevents a second process from resetting a running peer.
func RestorePending(adapter gpu.Adapter, configPath string) (err error) {
	release, err := config.LockDirectory(configPath)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, release()) }()
	name := "ownership.json"
	if adapter.Mock() {
		name = "ownership.mock.json"
	}
	path := filepath.Join(filepath.Dir(configPath), name)
	owned, err := readJournal(path)
	if err != nil {
		return err
	}
	if len(owned) == 0 {
		return nil
	}
	if err = adapter.Init(); err != nil {
		return err
	}
	defer func() { err = errors.Join(err, adapter.Close()) }()
	keys := make([]fanKey, 0, len(owned))
	for key := range owned {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].UUID == keys[j].UUID {
			return keys[i].Index < keys[j].Index
		}
		return keys[i].UUID < keys[j].UUID
	})
	for _, key := range keys {
		if resetErr := adapter.Reset(key.UUID, key.Index); resetErr != nil {
			err = errors.Join(err, fmt.Errorf("restore GPU %s fan %d: %w", key.UUID, key.Index, resetErr))
			continue
		}
		delete(owned, key)
	}
	return errors.Join(err, writeJournal(path, owned))
}
