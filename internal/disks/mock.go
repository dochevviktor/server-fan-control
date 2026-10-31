package disks

import (
	"context"
	"hash/fnv"
	"time"
)

type mockProvider struct{}

func NewMock() Provider         { return mockProvider{} }
func (mockProvider) Mock() bool { return true }
func (mockProvider) Read(ctx context.Context, d Drive) Sample {
	now := time.Now().UTC()
	s := Sample{ID: d.ID, Path: d.Path, Serial: d.Serial, AttemptedAt: now, ReadCompletedAt: now, Status: "invalid_data"}
	if err := ValidateDrive(d); err != nil {
		s.Reason = err.Error()
		return s
	}
	if ctx.Err() != nil {
		s.Status, s.Reason = "timeout", ctx.Err().Error()
		return s
	}
	h := fnv.New32a()
	_, _ = h.Write([]byte(d.ID))
	t := float64(32 + h.Sum32()%9)
	s.TemperatureC, s.LastValidAt, s.Status = &t, now, "ok"
	return s
}
