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

func TestStartupFailureRecoversIPMIEvenWithoutOptInFlag(t *testing.T) {
	for _, cause := range []string{"missing token", "invalid GPU config", "invalid IPMI config"} {
		t.Run(cause, func(t *testing.T) {
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
				args = append(args, "--dev-no-auth", "--ipmi")
				if err := os.WriteFile(filepath.Join(dir, "ipmi.mock.json"), []byte("invalid"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if err := runCLI(args, io.Discard, io.Discard, &fakeDiagnosticNVML{}); err == nil {
				t.Fatal("startup failure expected")
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
