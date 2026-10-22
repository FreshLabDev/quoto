// SPDX-License-Identifier: Apache-2.0

package media

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

// Runner runs an external program and returns what it printed to stdout. The
// pipeline talks to ffmpeg and ffprobe only through it, so the unit tests
// replace it.
type Runner interface {
	Run(ctx context.Context, name string, args ...string) (stdout []byte, err error)
}

// execRunner is the Runner that starts real processes. They get a small
// environment of their own; extraEnv is added to it, for tests.
type execRunner struct{ extraEnv []string }

// Output limits. ffmpeg is run with -loglevel error, so its stderr is short; the
// tail is kept for the message of a failure. Python kept the last 1000
// characters and so does this.
const (
	stderrTail = 1000
	stdoutCap  = 64 << 10
)

func (r execRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	bin, err := exec.LookPath(name)
	if err != nil {
		return nil, fmt.Errorf("%s is not installed", name)
	}
	cmdCtx, cancel := context.WithTimeout(ctx, commandTimeout)
	defer cancel()

	stdout := cappedBuffer{limit: stdoutCap}
	stderr := tailBuffer{limit: stderrTail}
	cmd := exec.CommandContext(cmdCtx, bin, args...)
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	// ffmpeg parses files that any member of a group can send. It gets what it
	// needs to run and none of the bot's secrets.
	cmd.Env = append([]string{"PATH=" + os.Getenv("PATH"), "TMPDIR=" + os.TempDir(), "LC_ALL=C"}, r.extraEnv...)
	// A killed ffmpeg must not keep the job waiting on its pipes.
	cmd.WaitDelay = 5 * time.Second

	err = cmd.Run()
	if err == nil {
		return stdout.Bytes(), nil
	}
	// The job ending (shutdown, its own timeout) is not the command's fault;
	// the command timing out is.
	if ctx.Err() != nil {
		return nil, fmt.Errorf("command stopped: %s: %w", name, ctx.Err())
	}
	if errors.Is(cmdCtx.Err(), context.DeadlineExceeded) {
		return nil, fmt.Errorf("command timed out after %s: %s", commandTimeout, name)
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		detail := ""
		if tail := strings.TrimSpace(stderr.String()); tail != "" {
			detail = ": " + tail
		}
		return nil, fmt.Errorf("command failed with exit status %d: %s%s", exit.ExitCode(), name, detail)
	}
	return nil, fmt.Errorf("command failed: %s: %w", name, err)
}

// cappedBuffer keeps the first limit bytes and drops the rest. It does not
// embed bytes.Buffer: its ReadFrom would be promoted, and io.Copy would use it
// and skip the limit.
type cappedBuffer struct {
	buf   bytes.Buffer
	limit int
}

func (b *cappedBuffer) Write(p []byte) (int, error) {
	if room := b.limit - b.buf.Len(); room > 0 {
		b.buf.Write(p[:min(room, len(p))])
	}
	return len(p), nil
}

func (b *cappedBuffer) Bytes() []byte { return b.buf.Bytes() }

// tailBuffer keeps the last limit bytes.
type tailBuffer struct {
	buf   []byte
	limit int
}

func (b *tailBuffer) Write(p []byte) (int, error) {
	b.buf = append(b.buf, p...)
	if len(b.buf) > 2*b.limit {
		b.buf = append(b.buf[:0], b.buf[len(b.buf)-b.limit:]...)
	}
	return len(p), nil
}

func (b *tailBuffer) String() string {
	if len(b.buf) > b.limit {
		return string(b.buf[len(b.buf)-b.limit:])
	}
	return string(b.buf)
}
