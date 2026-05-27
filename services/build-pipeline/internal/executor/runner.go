package executor

import (
	"bytes"
	"context"
	"os/exec"
	"time"
)

// buildTimeout is the maximum time a build job is allowed to run before it is
// forcibly terminated (Requirement 2.3).
const buildTimeout = 10 * time.Minute

// RunResult holds the outcome of a single build execution.
type RunResult struct {
	// Log is the combined stdout+stderr of the build, potentially truncated
	// using middle-truncation if it exceeds MaxLogBytes.
	Log []byte

	// ExitCode is the process exit code.  0 indicates success.  For timed-out
	// builds this value is meaningless (TimedOut will be true).
	ExitCode int

	// TimedOut is true when the build did not complete within the 10-minute
	// deadline (Requirement 2.3).
	TimedOut bool
}

// RunBuild executes cmd inside workDir, enforcing a 10-minute timeout.
// Stdout and stderr are captured together and truncated to MaxLogBytes using
// middle-truncation if necessary.
//
// The caller-supplied ctx is respected: if it is cancelled before the timeout
// fires, the build is also terminated.
//
// The function never returns an error; all failure information is encoded in
// the returned RunResult so that callers can apply the correct status
// transition without extra error handling.
func RunBuild(ctx context.Context, workDir string, cmd BuildCommand) RunResult {
	return RunBuildWithTimeout(ctx, workDir, cmd, buildTimeout)
}

// RunBuildWithTimeout is the same as RunBuild but lets callers override the
// timeout.  It is intended for use in tests (e.g. passing 1 ms to verify
// timeout handling) and for the production path via RunBuild.
func RunBuildWithTimeout(ctx context.Context, workDir string, cmd BuildCommand, timeout time.Duration) RunResult {
	// Derive a child context with the caller-supplied timeout.
	buildCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	//nolint:gosec // workDir and cmd originate from controlled internal logic.
	c := exec.CommandContext(buildCtx, cmd.Binary, cmd.Args...)
	c.Dir = workDir
	// Inherit the process environment and append any extra vars from the
	// BuildCommand so that PATH, HOME, GOPATH, CARGO_HOME, etc. are available.
	// c.Env == nil means "inherit everything"; we only set it when extras exist.
	if len(cmd.Env) > 0 {
		c.Env = append(c.Environ(), cmd.Env...)
	}

	var buf bytes.Buffer
	c.Stdout = &buf
	c.Stderr = &buf

	err := c.Run()

	log := TruncateMiddle(buf.Bytes())

	// Check whether the deadline was exceeded.
	if buildCtx.Err() == context.DeadlineExceeded {
		return RunResult{
			Log:      log,
			ExitCode: -1,
			TimedOut: true,
		}
	}

	exitCode := 0
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		} else {
			// The command could not be started (e.g. binary not found).
			// Treat this as a non-zero exit with the error in the log.
			exitCode = 1
			log = TruncateMiddle(append(buf.Bytes(), []byte("\n"+err.Error())...))
		}
	}

	return RunResult{
		Log:      log,
		ExitCode: exitCode,
		TimedOut: false,
	}
}
