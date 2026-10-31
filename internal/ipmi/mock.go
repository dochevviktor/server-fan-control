package ipmi

import (
	"context"
	"errors"
	"sync"
	"time"
)

type mockAdapter struct {
	mu     sync.Mutex
	duties [8]int
}

func NewMock() Adapter          { return &mockAdapter{} }
func (*mockAdapter) Mock() bool { return true }

func (a *mockAdapter) reading() Reading {
	r := Reading{Identity: Identity{Board: "E3C246D2I", Manufacturer: "ASRock Rack", Firmware: "mock-1.0"}, Duties: append([]int(nil), a.duties[:]...), Fans: []Fan{}, CollectedAt: time.Now().UTC()}
	for i := 1; i <= 3; i++ {
		duty := a.duties[i-1]
		if duty == 0 {
			duty = 45
		}
		rpm := float64(duty * 30)
		r.Fans = append(r.Fans, Fan{Name: "FAN" + string(rune('0'+i)), RPM: &rpm, Status: "ok"})
	}
	return r
}

func (a *mockAdapter) Read(ctx context.Context) (Reading, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return Reading{}, err
	}
	return a.reading(), nil
}

func (a *mockAdapter) Set(ctx context.Context, expected Identity, slot, duty int) (Reading, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return Reading{}, err
	}
	if expected != a.reading().Identity {
		return a.reading(), errors.New("mock identity mismatch")
	}
	if slot < 1 || slot > 8 || duty < 0 || duty > 100 {
		return a.reading(), errors.New("invalid mock slot or duty")
	}
	a.duties[slot-1] = duty
	return a.reading(), nil
}
