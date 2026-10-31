package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/NVIDIA/go-nvml/pkg/nvml"
)

// Any unimplemented NVML operation (including a fan write) panics through the
// nil embedded interface, so the diagnostic cannot silently start controlling.
type fakeDiagnosticNVML struct {
	nvml.Interface
	failures  map[string]nvml.Return
	count     int
	shutdowns int
}

type fakeDiagnosticDevice struct {
	nvml.Device
	index int
}

func (f *fakeDiagnosticNVML) Init() nvml.Return { return f.failures["init"] }
func (f *fakeDiagnosticNVML) Shutdown() nvml.Return {
	f.shutdowns++
	return f.failures["shutdown"]
}
func (f *fakeDiagnosticNVML) ErrorString(ret nvml.Return) string {
	return nvml.ErrorString(ret)
}
func (f *fakeDiagnosticNVML) DeviceGetCount() (int, nvml.Return) {
	return f.count, f.failures["count"]
}
func (f *fakeDiagnosticNVML) DeviceGetHandleByIndex(index int) (nvml.Device, nvml.Return) {
	return fakeDiagnosticDevice{index: index}, f.failures["handle"]
}
func (f *fakeDiagnosticNVML) DeviceGetUUID(device nvml.Device) (string, nvml.Return) {
	return fmt.Sprintf("GPU-%d", device.(fakeDiagnosticDevice).index), f.failures["uuid"]
}
func (f *fakeDiagnosticNVML) DeviceGetName(device nvml.Device) (string, nvml.Return) {
	return fmt.Sprintf("Test GPU %d", device.(fakeDiagnosticDevice).index), f.failures["name"]
}
func (f *fakeDiagnosticNVML) DeviceGetTemperature(device nvml.Device, sensor nvml.TemperatureSensors) (uint32, nvml.Return) {
	if sensor != nvml.TEMPERATURE_GPU {
		panic("unexpected temperature sensor")
	}
	return 42 + uint32(device.(fakeDiagnosticDevice).index), f.failures["temperature"]
}

func TestDiagnosticReadsEveryVisibleGPUWithoutConfigurationOrWrites(t *testing.T) {
	library := &fakeDiagnosticNVML{count: 2}
	var output bytes.Buffer
	missingConfig := filepath.Join(t.TempDir(), "missing.json")
	if err := runCLI([]string{"--diagnose", "--config", missingConfig}, &output, io.Discard, library); err != nil {
		t.Fatal(err)
	}
	var result struct {
		GPUs []diagnosticGPU `json:"gpus"`
	}
	if err := json.Unmarshal(output.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if len(result.GPUs) != 2 {
		t.Fatalf("got %d GPUs, want 2", len(result.GPUs))
	}
	for i, gpu := range result.GPUs {
		if gpu.Index != i || gpu.UUID != fmt.Sprintf("GPU-%d", i) || gpu.Name != fmt.Sprintf("Test GPU %d", i) || gpu.TemperatureC != uint32(42+i) {
			t.Errorf("unexpected GPU %d: %+v", i, gpu)
		}
	}
	if library.shutdowns != 1 {
		t.Errorf("shutdown called %d times, want 1", library.shutdowns)
	}
}

func TestDiagnosticFailuresAndCleanup(t *testing.T) {
	for _, test := range []struct {
		name      string
		operation string
		count     int
		wantError string
		shutdowns int
	}{
		{"initialization", "init", 1, "initialize NVML", 0},
		{"enumeration", "count", 1, "enumerate GPUs", 1},
		{"no devices", "", 0, "no GPUs visible", 1},
		{"device handle", "handle", 1, "get GPU 0", 1},
		{"UUID", "uuid", 1, "UUID", 1},
		{"name", "name", 1, "name", 1},
		{"temperature", "temperature", 1, "temperature", 1},
		{"shutdown", "shutdown", 1, "shutdown NVML", 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			library := &fakeDiagnosticNVML{
				count: test.count, failures: map[string]nvml.Return{test.operation: nvml.ERROR_UNKNOWN},
			}
			err := runDiagnostic(library, io.Discard)
			if err == nil || !strings.Contains(err.Error(), test.wantError) {
				t.Fatalf("got error %v, want %q", err, test.wantError)
			}
			if library.shutdowns != test.shutdowns {
				t.Errorf("shutdown called %d times, want %d", library.shutdowns, test.shutdowns)
			}
		})
	}
}

func TestDiagnosticPreservesReadAndShutdownFailures(t *testing.T) {
	library := &fakeDiagnosticNVML{count: 1, failures: map[string]nvml.Return{
		"temperature": nvml.ERROR_GPU_IS_LOST,
		"shutdown":    nvml.ERROR_UNKNOWN,
	}}
	err := runDiagnostic(library, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "temperature") || !strings.Contains(err.Error(), "shutdown NVML") {
		t.Fatalf("expected both errors, got %v", err)
	}
}

type failedWriter struct{}

func (failedWriter) Write([]byte) (int, error) { return 0, io.ErrClosedPipe }

func TestDiagnosticCleansUpAfterOutputFailure(t *testing.T) {
	library := &fakeDiagnosticNVML{count: 1}
	err := runDiagnostic(library, failedWriter{})
	if !errors.Is(err, io.ErrClosedPipe) || library.shutdowns != 1 {
		t.Fatalf("got error %v and %d shutdowns", err, library.shutdowns)
	}
}

func TestCLIHelpDoesNotInitializeNVML(t *testing.T) {
	if err := runCLI([]string{"--help"}, io.Discard, io.Discard, nil); !errors.Is(err, flag.ErrHelp) {
		t.Fatalf("got %v, want flag.ErrHelp", err)
	}
}

func TestCLIRejectsUnknownArgumentsBeforeNVML(t *testing.T) {
	for _, args := range [][]string{{"--unknown"}, {"config.json"}, {"--diagnose", "extra"}} {
		if err := runCLI(args, io.Discard, io.Discard, nil); err == nil {
			t.Errorf("expected invalid arguments %v to fail", args)
		}
	}
}

func TestCLIRejectsUnsafeAuthenticationBypass(t *testing.T) {
	for _, args := range [][]string{{"--dev-no-auth"}, {"--mock", "--dev-no-auth", "--listen", "0.0.0.0:8080"}} {
		if err := runCLI(args, io.Discard, io.Discard, nil); err == nil || !strings.Contains(err.Error(), "loopback") {
			t.Fatalf("unsafe bypass accepted: %v", err)
		}
	}
}

func TestStartupSecretFailureStillRestoresOwnedMockFans(t *testing.T) {
	dir := t.TempDir()
	journal := filepath.Join(dir, "ownership.mock.json")
	if err := os.WriteFile(journal, []byte(`{"schema_version":1,"fans":{"GPU-MOCK-001":[0,1]}}`), 0600); err != nil {
		t.Fatal(err)
	}
	err := runCLI([]string{"--mock", "--config", filepath.Join(dir, "config.json"), "--admin-token-file", filepath.Join(dir, "missing-token")}, io.Discard, io.Discard, nil)
	if err == nil || !strings.Contains(err.Error(), "admin token") {
		t.Fatalf("expected startup error, got %v", err)
	}
	data, readErr := os.ReadFile(journal)
	if readErr != nil {
		t.Fatal(readErr)
	}
	var document struct {
		Fans map[string][]int `json:"fans"`
	}
	if err := json.Unmarshal(data, &document); err != nil {
		t.Fatal(err)
	}
	if len(document.Fans) != 0 {
		t.Fatalf("startup error left owned fans: %s", data)
	}
}
