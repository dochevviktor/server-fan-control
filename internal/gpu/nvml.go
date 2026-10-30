package gpu

import (
	"errors"
	"fmt"
	"github.com/NVIDIA/go-nvml/pkg/nvml"
	"time"
)

type NVML struct {
	lib         nvml.Interface
	initialized bool
	devices     map[string]Device
	previous    map[string]map[string]Metric
}

func NewNVML() *NVML {
	return &NVML{lib: nvml.New(), devices: map[string]Device{}, previous: map[string]map[string]Metric{}}
}
func (n *NVML) Mock() bool { return false }
func (n *NVML) Init() error {
	if n.initialized {
		return nil
	}
	if r := n.lib.Init(); r != nvml.SUCCESS {
		return fmt.Errorf("initialize NVML: %s", n.lib.ErrorString(r))
	}
	n.initialized = true
	return nil
}
func (n *NVML) Close() error {
	if !n.initialized {
		return nil
	}
	n.initialized = false
	return n.result("shutdown NVML", n.lib.Shutdown())
}
func (n *NVML) result(operation string, r nvml.Return) error {
	if r != nvml.SUCCESS {
		return fmt.Errorf("%s: %s", operation, n.lib.ErrorString(r))
	}
	return nil
}
func (n *NVML) symbol(name string) bool {
	library, ok := n.lib.(interface{ LookupSymbol(string) error })
	return ok && library.LookupSymbol(name) == nil
}
func (n *NVML) Discover() ([]Device, error) {
	count, r := n.lib.DeviceGetCount()
	if r != nvml.SUCCESS {
		return nil, n.result("enumerate GPUs", r)
	}
	devices := make([]Device, 0, count)
	var problems error
	for i := 0; i < count; i++ {
		handle, r := n.lib.DeviceGetHandleByIndex(i)
		if r != nvml.SUCCESS {
			problems = errors.Join(problems, n.result("get device", r))
			continue
		}
		uuid, r := n.lib.DeviceGetUUID(handle)
		if r != nvml.SUCCESS {
			problems = errors.Join(problems, n.result("get UUID", r))
			continue
		}
		name, r := n.lib.DeviceGetName(handle)
		if r != nvml.SUCCESS {
			name = uuid
		}
		d := Device{UUID: uuid, Name: name, Index: i, Fans: []int{}, MaxDuty: 100}
		if !n.symbol("nvmlDeviceGetNumFans") {
			d.ControlReason = "fan enumeration symbol unavailable"
		} else {
			count, r := n.lib.DeviceGetNumFans(handle)
			if r != nvml.SUCCESS {
				d.ControlReason = n.lib.ErrorString(r)
			} else {
				for fan := 0; fan < count; fan++ {
					d.Fans = append(d.Fans, fan)
				}
				d.Controllable = count > 0 && n.symbol("nvmlDeviceSetFanSpeed_v2") && n.symbol("nvmlDeviceSetDefaultFanSpeed_v2")
				if count == 0 {
					d.ControlReason = "device has no controllable fans"
				} else if !d.Controllable {
					d.ControlReason = "fan set/reset symbols unavailable"
				}
			}
		}
		if n.symbol("nvmlDeviceGetMinMaxFanSpeed") {
			min, max, r := n.lib.DeviceGetMinMaxFanSpeed(handle)
			if r == nvml.SUCCESS {
				d.MinDuty = min
				d.MaxDuty = max
				d.BoundsKnown = true
			}
		}
		devices = append(devices, d)
		n.devices[uuid] = d
	}
	return devices, problems
}
func (n *NVML) handle(uuid string) (nvml.Device, error) {
	d, r := n.lib.DeviceGetHandleByUUID(uuid)
	if r != nvml.SUCCESS {
		return nil, n.result("find GPU "+uuid, r)
	}
	return d, nil
}
func (n *NVML) Read(uuid string) (Telemetry, error) {
	handle, err := n.handle(uuid)
	if err != nil {
		return Telemetry{}, err
	}
	previous := n.previous[uuid]
	metrics := map[string]Metric{}
	record := func(key, unit, symbol string, read func() (float64, nvml.Return)) Metric {
		now := time.Now().UTC()
		m := Metric{Unit: unit, CollectedAt: now, LastSuccessAt: previous[key].LastSuccessAt}
		if !n.symbol(symbol) {
			m.Status = "missing_symbol"
			m.Reason = symbol + " unavailable"
		} else {
			value, r := read()
			now = time.Now().UTC()
			m.CollectedAt = now
			if r == nvml.SUCCESS {
				m = Reading(value, unit, now)
			} else {
				m.Status = "error"
				m.Reason = n.lib.ErrorString(r)
				if r == nvml.ERROR_NOT_SUPPORTED {
					m.Status = "unsupported"
				}
				if r == nvml.ERROR_NO_PERMISSION {
					m.Status = "permission"
				}
				if r == nvml.ERROR_GPU_IS_LOST {
					m.Status = "offline"
				}
			}
		}
		metrics[key] = m
		return m
	}
	record("temperature_c", "°C", "nvmlDeviceGetTemperature", func() (float64, nvml.Return) {
		v, r := n.lib.DeviceGetTemperature(handle, nvml.TEMPERATURE_GPU)
		return float64(v), r
	})
	// A single sample supplies both VRAM measures, preserving their common timestamp.
	var memory nvml.Memory
	var memoryResult nvml.Return
	if n.symbol("nvmlDeviceGetMemoryInfo") {
		memory, memoryResult = n.lib.DeviceGetMemoryInfo(handle)
	}
	record("memory_used_bytes", "bytes", "nvmlDeviceGetMemoryInfo", func() (float64, nvml.Return) { return float64(memory.Used), memoryResult })
	record("memory_total_bytes", "bytes", "nvmlDeviceGetMemoryInfo", func() (float64, nvml.Return) { return float64(memory.Total), memoryResult })
	var utilization nvml.Utilization
	var utilizationResult nvml.Return
	if n.symbol("nvmlDeviceGetUtilizationRates") {
		utilization, utilizationResult = n.lib.DeviceGetUtilizationRates(handle)
	}
	record("gpu_utilization_percent", "%", "nvmlDeviceGetUtilizationRates", func() (float64, nvml.Return) { return float64(utilization.Gpu), utilizationResult })
	record("memory_utilization_percent", "%", "nvmlDeviceGetUtilizationRates", func() (float64, nvml.Return) { return float64(utilization.Memory), utilizationResult })
	record("power_watts", "W", "nvmlDeviceGetPowerUsage", func() (float64, nvml.Return) { v, r := n.lib.DeviceGetPowerUsage(handle); return float64(v) / 1000, r })
	fans := []FanReading{}
	for _, fan := range n.devices[uuid].Fans {
		key := fmt.Sprintf("fan_%d_percent", fan)
		reading := FanReading{Index: fan, Reported: record(key, "%", "nvmlDeviceGetFanSpeed_v2", func() (float64, nvml.Return) { v, r := n.lib.DeviceGetFanSpeed_v2(handle, fan); return float64(v), r })}
		if n.symbol("nvmlDeviceGetFanControlPolicy_v2") {
			p, r := n.lib.DeviceGetFanControlPolicy_v2(handle, fan)
			if r == nvml.SUCCESS {
				manual := p == nvml.FAN_POLICY_MANUAL
				reading.Manual = &manual
			}
		}
		fans = append(fans, reading)
	}
	n.previous[uuid] = metrics
	return Telemetry{Metrics: metrics, Fans: fans}, nil
}
func (n *NVML) Set(uuid string, fan, duty int) error {
	if fan < 0 || duty < 0 || duty > 100 {
		return errors.New("invalid fan target")
	}
	if !n.symbol("nvmlDeviceSetFanSpeed_v2") {
		return errors.New("fan setter symbol unavailable")
	}
	handle, err := n.handle(uuid)
	if err != nil {
		return err
	}
	// SetFanSpeed_v2 establishes manual policy itself; no separate policy write.
	return n.result("set fan speed", n.lib.DeviceSetFanSpeed_v2(handle, fan, duty))
}
func (n *NVML) Reset(uuid string, fan int) error {
	if fan < 0 {
		return errors.New("invalid fan index")
	}
	if !n.symbol("nvmlDeviceSetDefaultFanSpeed_v2") {
		return errors.New("fan reset symbol unavailable")
	}
	handle, err := n.handle(uuid)
	if err != nil {
		return err
	}
	return n.result("restore automatic fan control", n.lib.DeviceSetDefaultFanSpeed_v2(handle, fan))
}
