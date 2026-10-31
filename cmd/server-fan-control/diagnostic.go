package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/NVIDIA/go-nvml/pkg/nvml"
)

// The diagnostic deliberately has no access to fan policy or speed setters.
type diagnosticNVML interface {
	Init() nvml.Return
	Shutdown() nvml.Return
	DeviceGetCount() (int, nvml.Return)
	DeviceGetHandleByIndex(int) (nvml.Device, nvml.Return)
	DeviceGetUUID(nvml.Device) (string, nvml.Return)
	DeviceGetName(nvml.Device) (string, nvml.Return)
	DeviceGetTemperature(nvml.Device, nvml.TemperatureSensors) (uint32, nvml.Return)
	ErrorString(nvml.Return) string
}

type diagnosticGPU struct {
	Index        int    `json:"index"`
	UUID         string `json:"uuid"`
	Name         string `json:"name"`
	TemperatureC uint32 `json:"temperature_c"`
}

func runDiagnostic(library diagnosticNVML, output io.Writer) (err error) {
	if ret := library.Init(); ret != nvml.SUCCESS {
		return fmt.Errorf("initialize NVML: %s", library.ErrorString(ret))
	}
	defer func() {
		if ret := library.Shutdown(); ret != nvml.SUCCESS {
			err = errors.Join(err, fmt.Errorf("shutdown NVML: %s", library.ErrorString(ret)))
		}
	}()

	count, ret := library.DeviceGetCount()
	if ret != nvml.SUCCESS {
		return fmt.Errorf("enumerate GPUs: %s", library.ErrorString(ret))
	}
	if count == 0 {
		return errors.New("no GPUs visible to NVML; check the container GPU reservation")
	}
	result := struct {
		GPUs []diagnosticGPU `json:"gpus"`
	}{GPUs: make([]diagnosticGPU, 0, count)}
	for i := 0; i < count; i++ {
		device, ret := library.DeviceGetHandleByIndex(i)
		if ret != nvml.SUCCESS {
			return fmt.Errorf("get GPU %d: %s", i, library.ErrorString(ret))
		}
		uuid, ret := library.DeviceGetUUID(device)
		if ret != nvml.SUCCESS {
			return fmt.Errorf("read GPU %d UUID: %s", i, library.ErrorString(ret))
		}
		name, ret := library.DeviceGetName(device)
		if ret != nvml.SUCCESS {
			return fmt.Errorf("read GPU %s name: %s", uuid, library.ErrorString(ret))
		}
		temperature, ret := library.DeviceGetTemperature(device, nvml.TEMPERATURE_GPU)
		if ret != nvml.SUCCESS {
			return fmt.Errorf("read GPU %s temperature: %s", uuid, library.ErrorString(ret))
		}
		result.GPUs = append(result.GPUs, diagnosticGPU{
			Index: i, UUID: uuid, Name: name, TemperatureC: temperature,
		})
	}
	if err := json.NewEncoder(output).Encode(result); err != nil {
		return fmt.Errorf("write diagnostic output: %w", err)
	}
	return nil
}
