package main

import (
	"bytes"
	"errors"
	"flag"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFlagEnvironmentPrecedence(t *testing.T) {
	for _, tc := range []struct {
		name, path, enabled string
		args                []string
		wantPath            string
		wantEnabled         bool
		invalid             bool
	}{
		{name: "empty defaults", wantPath: "default.json"},
		{name: "environment", path: "/data/env.json", enabled: "true", wantPath: "/data/env.json", wantEnabled: true},
		{name: "explicit overrides", path: "/data/env.json", enabled: "true", args: []string{"--config", "flag.json", "--secure-cookies=false"}, wantPath: "flag.json"},
		{name: "explicit empty overrides", path: "/data/env.json", enabled: "invalid", args: []string{"--config=", "--secure-cookies=false"}},
		{name: "invalid boolean", enabled: "not-a-bool", wantPath: "default.json", invalid: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("FAN_CONTROL_CONFIG", tc.path)
			t.Setenv("FAN_CONTROL_SECURE_COOKIES", tc.enabled)
			f := flag.NewFlagSet("test", flag.ContinueOnError)
			path := f.String("config", "default.json", "")
			enabled := f.Bool("secure-cookies", false, "")
			if err := f.Parse(tc.args); err != nil {
				t.Fatal(err)
			}
			err := applyFlagEnvironment(f, "config", "secure-cookies")
			if (err != nil) != tc.invalid || *path != tc.wantPath || *enabled != tc.wantEnabled {
				t.Fatalf("got %q, %t, %v", *path, *enabled, err)
			}
			if err != nil && (!strings.Contains(err.Error(), "FAN_CONTROL_SECURE_COOKIES") || strings.Contains(err.Error(), tc.enabled)) {
				t.Fatalf("error should name the variable without its contents: %v", err)
			}
		})
	}
}

func TestEnvironmentDiagnosticAndModeOverrides(t *testing.T) {
	for _, args := range [][]string{nil, {"--diagnose", "--mock=false"}} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			t.Setenv("FAN_CONTROL_DIAGNOSE", "true")
			t.Setenv("FAN_CONTROL_MOCK", "false")
			if len(args) != 0 {
				t.Setenv("FAN_CONTROL_DIAGNOSE", "invalid")
				t.Setenv("FAN_CONTROL_MOCK", "invalid")
			}
			path := filepath.Join(t.TempDir(), "config.json")
			t.Setenv("FAN_CONTROL_CONFIG", path)
			library := &fakeDiagnosticNVML{count: 1}
			if err := runCLI(args, io.Discard, io.Discard, library); err != nil {
				t.Fatal(err)
			}
			if library.shutdowns != 1 {
				t.Fatal("diagnostic was not executed")
			}
			if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("diagnostic accessed config: %v", err)
			}
		})
	}
}

func TestInvalidEnvironmentModesDoNotAccessHardware(t *testing.T) {
	for _, name := range []string{"FAN_CONTROL_MOCK", "FAN_CONTROL_DIAGNOSE", "FAN_CONTROL_DIAGNOSE_IPMI"} {
		t.Run(name, func(t *testing.T) {
			t.Setenv(name, "invalid")
			err := runCLI(nil, io.Discard, io.Discard, nil)
			if err == nil || !strings.Contains(err.Error(), name) {
				t.Fatalf("invalid mode accepted: %v", err)
			}
		})
	}
	t.Run("conflicting IPMI diagnostic", func(t *testing.T) {
		t.Setenv("FAN_CONTROL_DIAGNOSE_IPMI", "true")
		t.Setenv("FAN_CONTROL_MOCK", "true")
		err := runCLI(nil, io.Discard, io.Discard, nil)
		if err == nil || !strings.Contains(err.Error(), "cannot be combined") {
			t.Fatalf("conflicting diagnostic accepted: %v", err)
		}
	})
}

func TestEnvironmentServiceConfiguration(t *testing.T) {
	// Reserve a listener so startup stops before running workers. Reaching this
	// address proves environment-only config, secret, mock and HTTPS settings
	// passed their validation without starting a signal-driven server in the test.
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	for _, secure := range []string{"false", "true"} {
		t.Run("secure cookies="+secure, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "config.json")
			token := filepath.Join(dir, "token")
			if err := os.WriteFile(token, []byte(strings.Repeat("s", 32)), 0600); err != nil {
				t.Fatal(err)
			}
			t.Setenv("FAN_CONTROL_CONFIG", path)
			t.Setenv("FAN_CONTROL_LISTEN", listener.Addr().String())
			t.Setenv("FAN_CONTROL_ADMIN_TOKEN_FILE", token)
			t.Setenv("FAN_CONTROL_MOCK", "true")
			t.Setenv("FAN_CONTROL_PUBLIC_ORIGIN", "https://fans.example.test")
			t.Setenv("FAN_CONTROL_SECURE_COOKIES", secure)
			t.Setenv(ipmiEnabledEnv, "false")
			t.Setenv(smartEnabledEnv, "false")
			err := runCLI(nil, io.Discard, io.Discard, nil)
			want := "HTTPS public origin requires secure cookies"
			if secure == "true" {
				want = "address already in use"
			}
			if err == nil || !strings.Contains(err.Error(), want) {
				t.Fatalf("got %v, want %q", err, want)
			}
			if _, err := os.Stat(path); err != nil {
				t.Fatalf("environment config path was not used: %v", err)
			}
		})
	}
}

func TestEnvironmentAuthenticationBypassRestrictions(t *testing.T) {
	for _, tc := range []struct{ mock, listen string }{{"false", "127.0.0.1:8080"}, {"true", "0.0.0.0:8080"}} {
		t.Run(tc.mock+" "+tc.listen, func(t *testing.T) {
			t.Setenv("FAN_CONTROL_CONFIG", filepath.Join(t.TempDir(), "config.json"))
			t.Setenv("FAN_CONTROL_DEV_NO_AUTH", "true")
			t.Setenv("FAN_CONTROL_MOCK", tc.mock)
			t.Setenv("FAN_CONTROL_LISTEN", tc.listen)
			err := runCLI(nil, io.Discard, io.Discard, nil)
			if err == nil || !strings.Contains(err.Error(), "loopback") {
				t.Fatalf("unsafe environment auth bypass accepted: %v", err)
			}
		})
	}
}

func TestCLIHelpListsEnvironmentOptionsWithoutParsingThem(t *testing.T) {
	t.Setenv("FAN_CONTROL_MOCK", "invalid")
	var output bytes.Buffer
	if err := runCLI([]string{"--help"}, io.Discard, &output, nil); !errors.Is(err, flag.ErrHelp) {
		t.Fatal(err)
	}
	for _, name := range []string{"CONFIG", "LISTEN", "ADMIN_TOKEN_FILE", "PUBLIC_ORIGIN", "SECURE_COOKIES", "MOCK", "DEV_NO_AUTH", "DIAGNOSE", "DIAGNOSE_IPMI"} {
		if !strings.Contains(output.String(), "FAN_CONTROL_"+name) {
			t.Errorf("help omitted %s", name)
		}
	}
}
