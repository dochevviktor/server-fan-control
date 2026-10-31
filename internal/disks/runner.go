package disks

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"time"
)

const commandTimeout = 10 * time.Second
const outputLimit = 128 * 1024

var errOutputLimit = errors.New("smartctl output exceeds 128 KiB")

type commandResult struct {
	stdout, stderr string
	exitCode       int
	err            error
}
type commandRunner interface {
	run(context.Context, ...string) commandResult
}
type localRunner struct {
	executable string
	timeout    time.Duration
}
type limitedOutput struct {
	data     bytes.Buffer
	exceeded bool
	cancel   context.CancelFunc
}

func (b *limitedOutput) Write(p []byte) (int, error) {
	remaining := outputLimit - b.data.Len()
	if len(p) > remaining {
		_, _ = b.data.Write(p[:remaining])
		b.exceeded = true
		b.cancel()
		return len(p), nil
	}
	return b.data.Write(p)
}

func (r localRunner) run(parent context.Context, args ...string) commandResult {
	ctx, cancel := context.WithTimeout(parent, r.timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, r.executable, args...)
	cmd.Env = append(os.Environ(), "LC_ALL=C", "LANG=C")
	cmd.WaitDelay = 100 * time.Millisecond
	stdout, stderr := &limitedOutput{cancel: cancel}, &limitedOutput{cancel: cancel}
	cmd.Stdout, cmd.Stderr = stdout, stderr
	err := cmd.Run()
	result := commandResult{stdout: stdout.data.String(), stderr: stderr.data.String(), exitCode: -1, err: err}
	if cmd.ProcessState != nil {
		result.exitCode = cmd.ProcessState.ExitCode()
	}
	if stdout.exceeded || stderr.exceeded {
		result.err = errOutputLimit
	} else if ctx.Err() != nil {
		result.err = ctx.Err()
	} else {
		var exit *exec.ExitError
		if errors.As(err, &exit) && exit.ExitCode() >= 0 {
			// The caller interprets smartctl's exit bitmask alongside its JSON.
			result.err = nil
		}
	}
	return result
}
