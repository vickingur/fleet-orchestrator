// Package execx runs external commands behind an interface so callers can be
// tested without spawning processes, and so every run is bounded by a timeout.
package execx

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"time"
)

// Cmd describes one process invocation.
type Cmd struct {
	Name    string
	Args    []string
	Stdin   io.Reader
	Stdout  io.Writer
	Stderr  io.Writer
	Env     []string
	Timeout time.Duration
}

// Result is the outcome of a finished command.
type Result struct {
	ExitCode int
	Duration time.Duration
}

// Runner executes commands.
type Runner interface {
	Run(ctx context.Context, c Cmd) (Result, error)
}

// ErrTimeout reports that a command exceeded its timeout and was killed.
var ErrTimeout = errors.New("command timed out")

// OS runs commands as real processes.
type OS struct{}

// Run executes c. A non-zero exit is reported through Result.ExitCode, not as
// an error; errors mean the command could not run or was killed by timeout.
func (OS) Run(ctx context.Context, c Cmd) (Result, error) {
	if c.Timeout <= 0 {
		return Result{}, fmt.Errorf("run %s: timeout must be positive", c.Name)
	}
	ctx, cancel := context.WithTimeout(ctx, c.Timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, c.Name, c.Args...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = c.Stdin, c.Stdout, c.Stderr
	if c.Env != nil {
		cmd.Env = c.Env
	}
	cmd.WaitDelay = 2 * time.Second
	start := time.Now()
	err := cmd.Run()
	res := Result{Duration: time.Since(start)}
	if ctx.Err() == context.DeadlineExceeded {
		return res, fmt.Errorf("%s after %s: %w", c.Name, c.Timeout, ErrTimeout)
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		res.ExitCode = exitErr.ExitCode()
		return res, nil
	}
	if err != nil {
		return res, fmt.Errorf("run %s: %w", c.Name, err)
	}
	return res, nil
}

// Output runs c and returns its stdout, failing on a non-zero exit with stderr
// included in the error.
func Output(ctx context.Context, r Runner, c Cmd) ([]byte, error) {
	var stdout, stderr bytes.Buffer
	c.Stdout, c.Stderr = &stdout, &stderr
	res, err := r.Run(ctx, c)
	if err != nil {
		return nil, err
	}
	if res.ExitCode != 0 {
		return nil, fmt.Errorf("%s exited %d: %s", c.Name, res.ExitCode, bytes.TrimSpace(stderr.Bytes()))
	}
	return stdout.Bytes(), nil
}
