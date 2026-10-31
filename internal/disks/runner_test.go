package disks

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func scriptRunner(t *testing.T, body string, timeout time.Duration) localRunner {
	t.Helper()
	path := filepath.Join(t.TempDir(), "fake-smartctl")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body), 0700); err != nil {
		t.Fatal(err)
	}
	return localRunner{executable: path, timeout: timeout}
}

func TestRunnerKeepsOutputOnHealthExitAndForcesLocale(t *testing.T) {
	r := scriptRunner(t, "printf '%s\\n' \"$LC_ALL\" \"$@\"\nexit 8", time.Second)
	got := r.run(context.Background(), "-j", "/dev/test")
	if got.err != nil || got.exitCode != 8 || got.stdout != "C\n-j\n/dev/test\n" {
		t.Fatalf("%+v", got)
	}
}

func TestRunnerBoundedCancellationAndOutput(t *testing.T) {
	for _, stream := range []string{"", " >&2"} {
		r := scriptRunner(t, "while :; do printf '"+strings.Repeat("x", 128)+"'"+stream+"; done", time.Second)
		got := r.run(context.Background())
		if !errors.Is(got.err, errOutputLimit) || len(got.stdout) > outputLimit || len(got.stderr) > outputLimit {
			t.Fatalf("unbounded stream: %v", got.err)
		}
	}
	r := scriptRunner(t, "exec sleep 10", 25*time.Millisecond)
	start := time.Now()
	got := r.run(context.Background())
	if !errors.Is(got.err, context.DeadlineExceeded) || time.Since(start) > time.Second {
		t.Fatalf("timeout not bounded: %+v", got)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if got = r.run(ctx); !errors.Is(got.err, context.Canceled) {
		t.Fatalf("cancel not honored: %+v", got)
	}
}
