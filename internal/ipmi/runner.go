package ipmi

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"
)

const commandTimeout = 3 * time.Second
const outputLimit = 64 * 1024

var errOutputLimit = errors.New("ipmitool output exceeds 64 KiB")

type commandRunner interface {
	run(context.Context, ...string) (string, error)
}

type localRunner struct {
	// Executable and timeout are internal test seams, never configuration/API fields.
	executable string
	timeout    time.Duration
}

type limitedOutput struct {
	mu       sync.Mutex
	data     bytes.Buffer
	exceeded bool
	cancel   context.CancelFunc
}

func (b *limitedOutput) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	remaining := outputLimit - b.data.Len()
	if len(p) > remaining {
		_, _ = b.data.Write(p[:remaining])
		b.exceeded = true
		b.cancel()
		return len(p), nil // Keep draining until process cancellation completes.
	}
	return b.data.Write(p)
}

func (r localRunner) run(parent context.Context, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(parent, r.timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, r.executable, append([]string{"-I", "open"}, args...)...)
	// ipmitool emits locale-dependent text. Preserve PATH for the installed binary,
	// while forcing the parser's language; no remote address or credentials are used.
	cmd.Env = append(os.Environ(), "LC_ALL=C", "LANG=C")
	cmd.WaitDelay = 100 * time.Millisecond
	stdout := &limitedOutput{cancel: cancel}
	stderr := &limitedOutput{cancel: cancel}
	cmd.Stdout, cmd.Stderr = stdout, stderr
	err := cmd.Run()
	if stdout.exceeded || stderr.exceeded {
		return "", errOutputLimit
	}
	if ctx.Err() != nil {
		return "", fmt.Errorf("ipmitool %s: %w", strings.Join(args, " "), ctx.Err())
	}
	if err != nil {
		message := strings.TrimSpace(stderr.data.String())
		if len(message) > 2048 {
			message = message[:2048]
		}
		return stdout.data.String(), fmt.Errorf("ipmitool %s: %w: %s", strings.Join(args, " "), err, message)
	}
	return stdout.data.String(), nil
}
