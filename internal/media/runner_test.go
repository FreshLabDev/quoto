// SPDX-License-Identifier: Apache-2.0

package media

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
)

// TestHelperProcess is not a test: execRunner starts the test binary as the
// external command, so the runner is exercised on a real process without
// ffmpeg. The mode comes from the first argument after "--".
func TestHelperProcess(t *testing.T) {
	if os.Getenv("QUOTO_MEDIA_HELPER") != "1" {
		return
	}
	args := os.Args
	for len(args) > 0 && args[0] != "--" {
		args = args[1:]
	}
	switch args[1] {
	case "print":
		fmt.Print(strings.Join(args[2:], " "))
	case "fail":
		fmt.Fprint(os.Stderr, strings.Repeat("x", 3000)+"the real reason")
		os.Exit(187)
	case "noisy":
		for i := 0; i < 20000; i++ {
			fmt.Println("0123456789012345678901234567890123456789")
			fmt.Fprintln(os.Stderr, "progress line")
		}
	case "env":
		fmt.Print(os.Getenv("BOT_TOKEN") + "|" + os.Getenv("ANTHROPIC_API_KEY") + "|" + os.Getenv("DATABASE_URL"))
	case "sleep":
		time.Sleep(time.Minute)
	}
	os.Exit(0)
}

// helperRunner starts the test binary with the helper switch on. The runner gives
// its children a minimal environment, so the switch has to be passed explicitly.
var helperRunner = execRunner{extraEnv: []string{"QUOTO_MEDIA_HELPER=1"}}

func helper(t *testing.T, mode string, args ...string) (name string, argv []string) {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	return exe, append([]string{"-test.run=^TestHelperProcess$", "--", mode}, args...)
}

func TestExecRunnerOutput(t *testing.T) {
	name, args := helper(t, "print", "12.5")
	out, err := helperRunner.Run(context.Background(), name, args...)
	if err != nil || string(out) != "12.5" {
		t.Fatalf("out %q err %v", out, err)
	}
}

// tests/test_media.py::test_run_includes_stderr_on_command_failure (MED-17): the
// error names the exit status and carries the end of stderr, at most 1000
// characters of it.
func TestExecRunnerFailureCarriesStderr(t *testing.T) {
	name, args := helper(t, "fail")
	_, err := helperRunner.Run(context.Background(), name, args...)
	if err == nil {
		t.Fatal("no error")
	}
	msg := err.Error()
	if !strings.Contains(msg, "exit status 187") || !strings.Contains(msg, "the real reason") {
		t.Fatalf("error %q", msg)
	}
	if len(msg) > 1200 {
		t.Fatalf("the error is %d bytes: the stderr tail is cut to %d", len(msg), stderrTail)
	}
}

// tests/test_media.py::test_run_raises_runtime_error_on_command_timeout (MED-16).
func TestExecRunnerTimeout(t *testing.T) {
	shorten(t, &commandTimeout, 200*time.Millisecond)
	name, args := helper(t, "sleep")
	started := time.Now()
	_, err := helperRunner.Run(context.Background(), name, args...)
	if err == nil || !strings.Contains(err.Error(), "timed out after 200ms") {
		t.Fatalf("err = %v", err)
	}
	if time.Since(started) > 30*time.Second {
		t.Fatal("the process was not killed")
	}
}

// The job ending kills the child at once; ffmpeg must not run on after a
// timeout or a shutdown (Python left it running, phase0/02-media.md 1.8).
func TestExecRunnerStopsWithTheJob(t *testing.T) {
	name, args := helper(t, "sleep")
	ctx, cancel := context.WithCancel(context.Background())
	go cancel()
	_, err := helperRunner.Run(ctx, name, args...)
	if err == nil || !strings.Contains(err.Error(), "stopped") {
		t.Fatalf("err = %v", err)
	}
}

func TestExecRunnerMissingBinary(t *testing.T) {
	_, err := helperRunner.Run(context.Background(), "definitely-not-ffmpeg-binary")
	if err == nil || !strings.Contains(err.Error(), "definitely-not-ffmpeg-binary is not installed") {
		t.Fatalf("err = %v", err)
	}
}

// doc §2: Python buffered the whole of ffmpeg's stderr. Here both pipes are
// bounded, however much the process writes.
func TestExecRunnerBoundsOutput(t *testing.T) {
	name, args := helper(t, "noisy")
	out, err := helperRunner.Run(context.Background(), name, args...)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) > stdoutCap {
		t.Fatalf("stdout kept %d bytes, cap is %d", len(out), stdoutCap)
	}
}

func TestTailBuffer(t *testing.T) {
	b := tailBuffer{limit: 10}
	for _, chunk := range []string{"abcdefgh", "ijklmnop", "qrstuvwx", "yz"} {
		if _, err := b.Write([]byte(chunk)); err != nil {
			t.Fatal(err)
		}
	}
	if got := b.String(); got != "qrstuvwxyz" {
		t.Fatalf("tail %q", got)
	}
	if len(b.buf) > 2*b.limit {
		t.Fatalf("buffer grew to %d", len(b.buf))
	}
}

// ffmpeg parses files from strangers, so it does not see the bot's secrets
// (review SEC-4).
func TestExecRunnerHidesTheEnvironment(t *testing.T) {
	t.Setenv("BOT_TOKEN", "123:secret-token")
	t.Setenv("ANTHROPIC_API_KEY", "sk-ant-secret")
	t.Setenv("DATABASE_URL", "postgres://u:secret@h/db")
	name, args := helper(t, "env")
	out, err := helperRunner.Run(context.Background(), name, args...)
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != "||" {
		t.Fatalf("the child saw %q", out)
	}
}
