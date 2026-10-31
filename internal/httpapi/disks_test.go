package httpapi

import (
	"bytes"
	"encoding/json"
	"testing"
	"time"

	"github.com/dochevviktor/server-fan-control/internal/disks"
)

type diskSnapshot struct{ value disks.Snapshot }

func (d diskSnapshot) Snapshot() disks.Snapshot { return d.value }

func TestDiskSnapshotRequiresSessionAndPreservesSampleAge(t *testing.T) {
	s, _ := testServer(t)
	if w := request(s, "GET", "/api/v1/disks", nil, credentials{}, ""); w.Code != 401 {
		t.Fatalf("unauthenticated disk identity exposed: %d", w.Code)
	}
	auth := login(t, s)
	w := request(s, "GET", "/api/v1/disks", nil, auth, "")
	if w.Code != 200 || !bytes.Contains(w.Body.Bytes(), []byte(`"enabled":false`)) {
		t.Fatalf("disabled SMART snapshot: %d %s", w.Code, w.Body)
	}
	old := time.Now().UTC().Add(-10 * time.Minute)
	temperature := 45.0
	s.options.Disks = diskSnapshot{disks.Snapshot{Enabled: true, Mock: true, UpdatedAt: old, Samples: []disks.Sample{{ID: "hdd-1", Serial: "test-serial", TemperatureC: &temperature, Status: "standby", LastValidAt: old}}}}
	for i := 0; i < 2; i++ {
		w = request(s, "GET", "/api/v1/disks", nil, auth, "")
		var got disks.Snapshot
		if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil || len(got.Samples) != 1 {
			t.Fatalf("snapshot: %s", w.Body)
		}
		if !got.Samples[0].LastValidAt.Equal(old) || got.Samples[0].Status != "standby" || !got.UpdatedAt.Equal(old) {
			t.Fatalf("HTTP polling manufactured sample freshness: %+v", got)
		}
	}
	if w := request(s, "POST", "/api/v1/disks", []byte(`{"command":"--scan"}`), auth, "http://example.com"); w.Code != 405 {
		t.Fatalf("disk route accepted mutation: %d", w.Code)
	}
}
