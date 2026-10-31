package ipmi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"syscall"
	"time"

	"github.com/dochevviktor/server-fan-control/internal/config"
)

type ownership struct {
	SchemaVersion int              `json:"schema_version"`
	Profile       string           `json:"profile"`
	Fans          map[int]Identity `json:"fans"`
}

func decodeFile(path string, out any) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, 1024*1024+1))
	if err != nil {
		return err
	}
	if len(data) > 1024*1024 {
		return errors.New("IPMI file exceeds 1 MiB")
	}
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if err = d.Decode(out); err != nil {
		return err
	}
	var extra any
	if d.Decode(&extra) != io.EOF {
		return errors.New("expected one JSON document")
	}
	return nil
}

func writeDocument(path string, value any) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	return config.AtomicWrite(path, append(data, '\n'))
}

func removeSettingsPending(path string) error {
	if err := os.Remove(path); err != nil {
		return err
	}
	dir, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer dir.Close()
	err = dir.Sync()
	if errors.Is(err, syscall.EINVAL) || errors.Is(err, syscall.ENOTSUP) {
		return nil
	}
	return err
}

func recoverSettings(path string) error {
	var previous Settings
	if err := decodeFile(path+".pending", &previous); errors.Is(err, os.ErrNotExist) {
		return nil
	} else if err != nil {
		return fmt.Errorf("pending IPMI settings: %w", err)
	}
	if err := ValidateSettings(previous); err != nil {
		return err
	}
	if err := writeDocument(path, previous); err != nil {
		return err
	}
	return removeSettingsPending(path + ".pending")
}

func journalPath(path string, mock bool) string {
	if mock {
		return path + ".ownership.mock.json"
	}
	return path + ".ownership.json"
}

func readOwnership(path string) (map[int]Identity, error) {
	d := ownership{}
	err := decodeFile(path, &d)
	if errors.Is(err, os.ErrNotExist) {
		return map[int]Identity{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("IPMI ownership: %w", err)
	}
	if d.SchemaVersion != 1 || d.Profile != ProfileID || d.Fans == nil {
		return nil, errors.New("invalid IPMI ownership schema")
	}
	for fan, id := range d.Fans {
		if fan < 1 || fan > 3 || id.Board != "E3C246D2I" || id.Manufacturer == "" || id.Firmware == "" {
			return nil, errors.New("invalid IPMI ownership identity or fan")
		}
	}
	return d.Fans, nil
}

func saveOwnership(path string, fans map[int]Identity) error {
	return writeDocument(path, ownership{SchemaVersion: 1, Profile: ProfileID, Fans: fans})
}

func copyOwned(in map[int]Identity) map[int]Identity {
	out := map[int]Identity{}
	for k, v := range in {
		out[k] = v
	}
	return out
}

func sortedFans(in map[int]Identity) []int {
	out := make([]int, 0, len(in))
	for k := range in {
		out = append(out, k)
	}
	sort.Ints(out)
	return out
}

// RestorePending performs journal-only recovery under the instance lock. It
// does not need valid desired settings and never enters manual mode.
func RestorePending(adapter Adapter, path string) (err error) {
	if _, err = os.Stat(journalPath(path, adapter.Mock())); errors.Is(err, os.ErrNotExist) {
		return nil
	}
	release, err := config.LockDirectory(path)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, release()) }()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	return RestorePendingLocked(ctx, adapter, path)
}

// RestorePendingLocked requires the caller to hold the data directory lock.
func RestorePendingLocked(ctx context.Context, adapter Adapter, path string) error {
	p := journalPath(path, adapter.Mock())
	owned, err := readOwnership(p)
	if err != nil {
		return err
	}
	if len(owned) == 0 {
		return nil
	}
	remaining := copyOwned(owned)
	for _, fan := range sortedFans(owned) {
		if _, e := adapter.Set(ctx, owned[fan], fan, 0); e != nil {
			err = errors.Join(err, fmt.Errorf("restore FAN%d: %w", fan, e))
		} else {
			delete(remaining, fan)
		}
	}
	return errors.Join(err, saveOwnership(p, remaining))
}
