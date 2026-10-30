package gpu

import (
	"errors"
	"github.com/NVIDIA/go-nvml/pkg/nvml"
	"testing"
)

type minimalNVML struct {
	nvml.Interface
	device nvml.Device
}

func (f *minimalNVML) Init() nvml.Return                  { return nvml.SUCCESS }
func (f *minimalNVML) Shutdown() nvml.Return              { return nvml.SUCCESS }
func (f *minimalNVML) DeviceGetCount() (int, nvml.Return) { return 1, nvml.SUCCESS }
func (f *minimalNVML) DeviceGetHandleByIndex(int) (nvml.Device, nvml.Return) {
	return f.device, nvml.SUCCESS
}
func (f *minimalNVML) DeviceGetHandleByUUID(string) (nvml.Device, nvml.Return) {
	return f.device, nvml.SUCCESS
}
func (f *minimalNVML) DeviceGetUUID(nvml.Device) (string, nvml.Return) {
	return "GPU-TEST", nvml.SUCCESS
}
func (f *minimalNVML) DeviceGetName(nvml.Device) (string, nvml.Return) {
	return "Old driver GPU", nvml.SUCCESS
}
func (f *minimalNVML) LookupSymbol(string) error { return errors.New("symbol missing") }
func TestMissingOptionalSymbolsStayVisibleAndNeverInvokeMissingFunctions(t *testing.T) {
	// All unimplemented functions panic through the nil embedded interface.
	n := NewNVML()
	n.lib = &minimalNVML{}
	if err := n.Init(); err != nil {
		t.Fatal(err)
	}
	devices, err := n.Discover()
	if err != nil || len(devices) != 1 || devices[0].Controllable {
		t.Fatal(devices, err)
	}
	sample, err := n.Read("GPU-TEST")
	if err != nil {
		t.Fatal(err)
	}
	for name, m := range sample.Metrics {
		if m.Value != nil || m.Status != "missing_symbol" {
			t.Fatal(name, m)
		}
	}
	if err := n.Set("GPU-TEST", 0, 30); err == nil {
		t.Fatal("write did not reject missing symbol")
	}
	if err := n.Reset("GPU-TEST", 0); err == nil {
		t.Fatal("reset did not reject missing symbol")
	}
	if err := n.Close(); err != nil {
		t.Fatal(err)
	}
}
