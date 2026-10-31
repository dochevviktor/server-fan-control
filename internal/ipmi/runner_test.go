package ipmi

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func testRunner(t *testing.T, body string, timeout time.Duration) localRunner {
	t.Helper()
	path := filepath.Join(t.TempDir(), "fake-ipmitool")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body), 0700); err != nil {
		t.Fatal(err)
	}
	return localRunner{executable: path, timeout: timeout}
}

func TestRunnerFixedLocalInterfaceAndLocale(t *testing.T) {
	r := testRunner(t, "printf '%s\\n' \"$LC_ALL\" \"$@\"", time.Second)
	out, err := r.run(context.Background(), "raw", "0x3a", "0x02")
	if err != nil || out != "C\n-I\nopen\nraw\n0x3a\n0x02\n" {
		t.Fatalf("%q %v", out, err)
	}
}

func TestRunnerBoundsTimeOutputAndExitErrors(t *testing.T) {
	t.Run("timeout", func(t *testing.T) {
		r := testRunner(t, "exec sleep 10", 30*time.Millisecond)
		start := time.Now()
		_, err := r.run(context.Background(), "mc", "info")
		if !errors.Is(err, context.DeadlineExceeded) || time.Since(start) > time.Second {
			t.Fatalf("%v elapsed %v", err, time.Since(start))
		}
	})
	t.Run("cancelled", func(t *testing.T) {
		r := testRunner(t, "exec sleep 10", time.Second)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		_, err := r.run(ctx, "mc", "info")
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	})
	t.Run("stdout overflow", func(t *testing.T) {
		r := testRunner(t, "while :; do printf '1234567890123456789012345678901234567890123456789012345678901234567890'; done", time.Second)
		_, err := r.run(context.Background(), "mc", "info")
		if !errors.Is(err, errOutputLimit) {
			t.Fatal(err)
		}
	})
	t.Run("stderr overflow", func(t *testing.T) {
		r := testRunner(t, "while :; do printf '1234567890123456789012345678901234567890123456789012345678901234567890' >&2; done", time.Second)
		_, err := r.run(context.Background(), "mc", "info")
		if !errors.Is(err, errOutputLimit) {
			t.Fatal(err)
		}
	})
	t.Run("exit status", func(t *testing.T) {
		r := testRunner(t, "printf 'permission denied' >&2\nexit 1", time.Second)
		_, err := r.run(context.Background(), "mc", "info")
		if err == nil || !strings.Contains(err.Error(), "permission denied") {
			t.Fatal(err)
		}
	})
}
