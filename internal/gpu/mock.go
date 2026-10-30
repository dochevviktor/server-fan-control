package gpu

import (
	"fmt"
	"math"
	"time"
)

// MockAdapter is explicit simulation, never a fallback for missing hardware.
type MockAdapter struct {
	start  time.Time
	duties map[string]map[int]int
}

func NewMock() *MockAdapter         { return &MockAdapter{start: time.Now(), duties: map[string]map[int]int{}} }
func (m *MockAdapter) Mock() bool   { return true }
func (m *MockAdapter) Init() error  { return nil }
func (m *MockAdapter) Close() error { return nil }
func (m *MockAdapter) Discover() ([]Device, error) {
	return []Device{
		{UUID: "GPU-MOCK-001", Name: "Simulated GPU · dual fan", Index: 0, Fans: []int{0, 1}, Controllable: true, MinDuty: 20, MaxDuty: 100, BoundsKnown: true},
		{UUID: "GPU-MOCK-002", Name: "Simulated GPU · passive cooling", Index: 1, Fans: []int{}, ControlReason: "passively cooled device", MaxDuty: 100},
	}, nil
}
func (m *MockAdapter) Read(uuid string) (Telemetry, error) {
	if uuid != "GPU-MOCK-001" && uuid != "GPU-MOCK-002" {
		return Telemetry{}, fmt.Errorf("unknown mock GPU %s", uuid)
	}
	now := time.Now().UTC()
	wave := math.Sin(time.Since(m.start).Seconds() / 14)
	metrics := map[string]Metric{
		"temperature_c":              Reading(48+7*wave, "°C", now),
		"memory_used_bytes":          Reading(3.1*1073741824, "bytes", now),
		"memory_total_bytes":         Reading(8*1073741824, "bytes", now),
		"gpu_utilization_percent":    Reading(37+20*wave, "%", now),
		"memory_utilization_percent": Reading(22+10*wave, "%", now),
		"power_watts":                Reading(87+16*wave, "W", now),
	}
	fans := []FanReading{}
	if uuid == "GPU-MOCK-001" {
		for i := 0; i < 2; i++ {
			duty, manual := m.duties[uuid][i]
			if !manual {
				duty = int(35 + 5*wave)
			}
			fans = append(fans, FanReading{Index: i, Reported: Reading(float64(duty), "%", now), Manual: &manual})
		}
	}
	return Telemetry{Metrics: metrics, Fans: fans}, nil
}
func (m *MockAdapter) Set(uuid string, fan, duty int) error {
	if uuid != "GPU-MOCK-001" || fan < 0 || fan > 1 || duty < 0 || duty > 100 {
		return fmt.Errorf("unsupported mock fan target")
	}
	if m.duties[uuid] == nil {
		m.duties[uuid] = map[int]int{}
	}
	m.duties[uuid][fan] = duty
	return nil
}
func (m *MockAdapter) Reset(uuid string, fan int) error {
	if uuid != "GPU-MOCK-001" || fan < 0 || fan > 1 {
		return fmt.Errorf("unsupported mock fan")
	}
	delete(m.duties[uuid], fan)
	return nil
}
