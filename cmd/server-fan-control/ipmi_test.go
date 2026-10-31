package main

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dochevviktor/server-fan-control/internal/config"
)

func TestStartupFailureRecoversIPMIEvenWhenDisabled(t *testing.T) {
	for _, cause := range []string{"missing token", "invalid GPU config", "invalid IPMI config", "invalid IPMI environment", "invalid SMART environment", "invalid cookies environment", "invalid auth environment", "invalid listen"} {
		t.Run(cause, func(t *testing.T) {
			t.Setenv(ipmiEnabledEnv, "false")
			t.Setenv(smartEnabledEnv, "false")
			dir := t.TempDir()
			cfg := filepath.Join(dir, "config.json")
			journal := filepath.Join(dir, "ipmi.mock.json.ownership.mock.json")
			data := `{"schema_version":1,"profile":"asrock-e3c246d2i","fans":{"3":{"board":"E3C246D2I","manufacturer":"ASRock Rack","firmware":"mock-1.0"}}}`
			if err := os.WriteFile(journal, []byte(data), 0600); err != nil {
				t.Fatal(err)
			}
			args := []string{"--mock", "--config", cfg}
			switch cause {
			case "invalid GPU config":
				args = append(args, "--dev-no-auth")
				if err := os.WriteFile(cfg, []byte("invalid"), 0600); err != nil {
					t.Fatal(err)
				}
			case "invalid IPMI config":
				t.Setenv(ipmiEnabledEnv, "true")
				args = append(args, "--dev-no-auth")
				if err := os.WriteFile(filepath.Join(dir, "ipmi.mock.json"), []byte("invalid"), 0600); err != nil {
					t.Fatal(err)
				}
			case "invalid IPMI environment":
				t.Setenv(ipmiEnabledEnv, "invalid")
			case "invalid SMART environment":
				t.Setenv(smartEnabledEnv, "invalid")
			case "invalid cookies environment":
				t.Setenv("FAN_CONTROL_SECURE_COOKIES", "invalid")
			case "invalid auth environment":
				t.Setenv("FAN_CONTROL_DEV_NO_AUTH", "invalid")
			case "invalid listen":
				t.Setenv("FAN_CONTROL_LISTEN", "invalid")
			}
			err := runCLI(args, io.Discard, io.Discard, &fakeDiagnosticNVML{})
			if err == nil {
				t.Fatal("startup failure expected")
			}
			if cause == "invalid IPMI config" && !strings.Contains(err.Error(), "open IPMI settings") {
				t.Fatalf("IPMI environment did not enable settings loading: %v", err)
			}
			if strings.Contains(cause, "environment") && !strings.Contains(err.Error(), "must be a boolean") {
				t.Fatalf("invalid environment was not reported: %v", err)
			}
			saved, err := os.ReadFile(journal)
			if err != nil {
				t.Fatal(err)
			}
			var record struct {
				Fans map[string]any `json:"fans"`
			}
			if err := json.Unmarshal(saved, &record); err != nil {
				t.Fatal(err)
			}
			if len(record.Fans) != 0 {
				t.Fatalf("ownership not recovered: %s", saved)
			}
		})
	}
}
func TestStartupFailureDoesNotRestoreAnotherInstanceIPMI(t *testing.T) {
	t.Setenv(ipmiEnabledEnv, "false")
	t.Setenv(smartEnabledEnv, "false")
	dir := t.TempDir()
	cfg := filepath.Join(dir, "config.json")
	store, err := config.Open(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	journal := filepath.Join(dir, "ipmi.mock.json.ownership.mock.json")
	data := `{"schema_version":1,"profile":"asrock-e3c246d2i","fans":{"3":{"board":"E3C246D2I","manufacturer":"ASRock Rack","firmware":"mock-1.0"}}}`
	if err := os.WriteFile(journal, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	err = runCLI([]string{"--mock", "--config", cfg}, io.Discard, io.Discard, &fakeDiagnosticNVML{})
	if err == nil || !strings.Contains(err.Error(), "another instance") {
		t.Fatalf("missing lock error: %v", err)
	}
	saved, err := os.ReadFile(journal)
	if err != nil {
		t.Fatal(err)
	}
	if string(saved) != data {
		t.Fatal("recovered fans belonging to running instance")
	}
}
func TestIPMIDiagnosticConflictingModes(t *testing.T) {
	for _, args := range [][]string{{"--diagnose-ipmi", "--mock"}, {"--diagnose-ipmi", "--diagnose"}} {
		if err := runCLI(args, io.Discard, io.Discard, &fakeDiagnosticNVML{}); err == nil {
			t.Fatal("conflicting diagnostic mode accepted")
		}
	}
}

func TestSMARTRequiresIPMISettings(t *testing.T) {
	t.Setenv(smartEnabledEnv, "true")
	t.Setenv(ipmiEnabledEnv, "false")
	err := runCLI([]string{"--mock", "--dev-no-auth", "--config", filepath.Join(t.TempDir(), "config.json")}, io.Discard, io.Discard, &fakeDiagnosticNVML{})
	if err == nil || !strings.Contains(err.Error(), "FAN_CONTROL_SMART_ENABLED=true requires FAN_CONTROL_IPMI_ENABLED=true") {
		t.Fatalf("missing SMART configuration prerequisite: %v", err)
	}
}

func TestFeatureEnvironmentDefaultsAndValidation(t *testing.T) {
	for _, name := range []string{ipmiEnabledEnv, smartEnabledEnv} {
		for _, tc := range []struct {
			value         string
			want, invalid bool
		}{{"", false, false}, {"false", false, false}, {"true", true, false}, {"invalid", false, true}} {
			t.Run(name+"="+tc.value, func(t *testing.T) {
				t.Setenv(name, tc.value)
				got, err := environmentBool(name)
				if got != tc.want || (err != nil) != tc.invalid {
					t.Fatalf("got %t, %v", got, err)
				}
			})
		}
		t.Run(name+" unset", func(t *testing.T) {
			t.Setenv(name, "")
			if err := os.Unsetenv(name); err != nil {
				t.Fatal(err)
			}
			if enabled, err := environmentBool(name); err != nil || enabled {
				t.Fatalf("unset feature did not default to false: %t, %v", enabled, err)
			}
		})
	}
}
