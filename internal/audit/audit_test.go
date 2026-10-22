// SPDX-License-Identifier: Apache-2.0

package audit

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"
)

type testRecord struct {
	RequestID string `json:"request_id"`
	Attempt   int    `json:"attempt"`
	Provider  string `json:"provider"`
	Text      string `json:"text"`
}

// captureHandler collects log messages so a test can see what was reported.
type captureHandler struct {
	mu   sync.Mutex
	msgs []string
}

func (h *captureHandler) Enabled(context.Context, slog.Level) bool { return true }
func (h *captureHandler) Handle(_ context.Context, r slog.Record) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.msgs = append(h.msgs, r.Level.String()+" "+r.Message)
	return nil
}
func (h *captureHandler) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h *captureHandler) WithGroup(string) slog.Handler      { return h }
func (h *captureHandler) joined() string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return strings.Join(h.msgs, "\n")
}

func newTestLog(t *testing.T, now time.Time) (*Log, *time.Time, *captureHandler) {
	t.Helper()
	clock := now
	logs := &captureHandler{}
	l := New(filepath.Join(t.TempDir(), "logs"), slog.New(logs))
	l.now = func() time.Time { return clock }
	return l, &clock, logs
}

func readLines(t *testing.T, path string) []string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, line := range strings.Split(string(data), "\n") {
		if line != "" {
			out = append(out, line)
		}
	}
	return out
}

var t0 = time.Date(2026, 10, 22, 12, 0, 0, 123456000, time.UTC)

// doc §6 Audit log: "Record shape test; file mode test."
func TestWriteRecordShape(t *testing.T) {
	l, _, _ := newTestLog(t, t0)
	rec := testRecord{RequestID: "abc", Attempt: 2, Provider: "openrouter", Text: "<b>Привет</b> & \"q\""}
	if err := l.Write(rec); err != nil {
		t.Fatal(err)
	}
	lines := readLines(t, l.Path())
	if len(lines) != 1 {
		t.Fatalf("got %d lines, want 1", len(lines))
	}
	want := `{"created_at":"2026-10-22T12:00:00.123456+00:00","request_id":"abc","attempt":2,"provider":"openrouter","text":"<b>Привет</b> & \"q\""}`
	if lines[0] != want {
		t.Fatalf("line =\n%s\nwant\n%s", lines[0], want)
	}
	if !regexp.MustCompile(`^\{"created_at":"\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}\.\d{6}\+00:00",`).MatchString(lines[0]) {
		t.Fatalf("created_at is not Python isoformat: %s", lines[0])
	}
}

func TestWriteEmptyObject(t *testing.T) {
	l, _, _ := newTestLog(t, t0)
	if err := l.Write(struct{}{}); err != nil {
		t.Fatal(err)
	}
	if got := readLines(t, l.Path())[0]; got != `{"created_at":"2026-10-22T12:00:00.123456+00:00"}` {
		t.Fatalf("line = %s", got)
	}
}

func TestWriteRejectsNonObject(t *testing.T) {
	l, _, _ := newTestLog(t, t0)
	if err := l.Write([]int{1}); err == nil {
		t.Fatal("a JSON array must be refused")
	}
	if _, err := os.Stat(l.Path()); err == nil {
		t.Fatal("nothing may be written for a refused record")
	}
}

// doc §6 Audit log: "operator only" (AGENTS.md: the log holds real chat text).
func TestFileModeIs0600(t *testing.T) {
	l, _, _ := newTestLog(t, t0)
	if err := l.Write(testRecord{}); err != nil {
		t.Fatal(err)
	}
	assertMode(t, l.Path(), 0o600)
	info, err := os.Stat(filepath.Dir(l.Path()))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o700 {
		t.Fatalf("created directory mode = %v, want 0700", info.Mode().Perm())
	}
}

func TestExistingLooseFileIsTightened(t *testing.T) {
	l, _, _ := newTestLog(t, t0)
	if err := os.MkdirAll(filepath.Dir(l.Path()), 0o755); err != nil {
		t.Fatal(err)
	}
	// What the Python version left behind: created with the default umask.
	if err := os.WriteFile(l.Path(), []byte(`{"created_at": "2026-10-22T11:00:00+00:00"}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(l.Path(), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := l.Write(testRecord{}); err != nil {
		t.Fatal(err)
	}
	assertMode(t, l.Path(), 0o600)
	if n := len(readLines(t, l.Path())); n != 2 {
		t.Fatalf("lines = %d, want the old one and the new one", n)
	}
}

func assertMode(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != want {
		t.Fatalf("%s mode = %v, want %v", filepath.Base(path), info.Mode().Perm(), want)
	}
}

func seed(t *testing.T, l *Log, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(l.Path()), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(l.Path(), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func day(n int) string {
	return t0.Add(-time.Duration(n)*24*time.Hour).Format("2006-01-02T15:04:05.000000") + "+00:00"
}

// tests/test_ai.py::test_evaluate_messages_prunes_ai_audit_entries_older_than_seven_days
func TestPruneDropsOnlyExpiredLines(t *testing.T) {
	l, _, _ := newTestLog(t, t0)
	seed(t, l, strings.Join([]string{
		// Python's json.dumps default separators, as the Python test wrote them.
		`{"created_at": "` + day(8) + `", "request_id": "old"}`,
		`{"created_at": "` + day(1) + `", "request_id": "fresh"}`,
		// Go's own layout.
		`{"created_at":"` + day(9) + `","request_id":"old-go"}`,
		`{"created_at":"` + day(2) + `","request_id":"fresh-go"}`,
		// A naive timestamp is UTC.
		`{"created_at":"` + strings.TrimSuffix(day(10), "+00:00") + `","request_id":"old-naive"}`,
		// Not ours to judge: kept forever.
		`{"request_id":"no-created-at"}`,
		`{"created_at":12345,"request_id":"numeric-created-at"}`,
		`{"created_at":"yesterday-ish","request_id":"unparsable"}`,
		`not json at all`,
		`123`,
		`[]`,
		// Key elsewhere: needs the full parse.
		`{"request_id":"late-key-old","created_at":"` + day(30) + `"}`,
		// Blank lines go.
		``,
		`   `,
	}, "\n")+"\n")

	if err := l.Write(testRecord{RequestID: "new"}); err != nil {
		t.Fatal(err)
	}
	got := strings.Join(readLines(t, l.Path()), "\n")
	for _, id := range []string{"fresh", "fresh-go", "no-created-at", "numeric-created-at", "unparsable", "not json at all", "new"} {
		if !strings.Contains(got, id) {
			t.Errorf("%q must be kept:\n%s", id, got)
		}
	}
	for _, id := range []string{`"old"`, "old-go", "old-naive", "late-key-old"} {
		if strings.Contains(got, id) {
			t.Errorf("%q must be dropped:\n%s", id, got)
		}
	}
	for _, line := range strings.Split(got, "\n") {
		if strings.TrimSpace(line) == "" {
			t.Errorf("blank line survived")
		}
	}
	assertMode(t, l.Path(), 0o600)
	if _, err := os.Stat(l.Path() + ".tmp"); err == nil {
		t.Error("temporary file left behind")
	}
}

// doc §7.1: "pruned at most once an hour via temp file and rename instead of
// being rewritten on every write."
func TestPruneRunsAtMostOncePerHour(t *testing.T) {
	l, clock, _ := newTestLog(t, t0)
	if err := l.Write(testRecord{RequestID: "first"}); err != nil {
		t.Fatal(err)
	}

	appendRaw := func(s string) {
		f, err := os.OpenFile(l.Path(), os.O_APPEND|os.O_WRONLY, 0o600)
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		if _, err := f.WriteString(s + "\n"); err != nil {
			t.Fatal(err)
		}
	}
	appendRaw(`{"created_at":"` + day(8) + `","request_id":"expired"}`)

	*clock = t0.Add(30 * time.Minute)
	if err := l.Write(testRecord{RequestID: "second"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(readLines(t, l.Path()), "\n"), "expired") {
		t.Fatal("the cleanup ran again after 30 minutes")
	}

	*clock = t0.Add(61 * time.Minute)
	if err := l.Write(testRecord{RequestID: "third"}); err != nil {
		t.Fatal(err)
	}
	all := strings.Join(readLines(t, l.Path()), "\n")
	if strings.Contains(all, "expired") {
		t.Fatalf("the cleanup did not run after an hour:\n%s", all)
	}
	for _, id := range []string{"first", "second", "third"} {
		if !strings.Contains(all, id) {
			t.Fatalf("%s lost:\n%s", id, all)
		}
	}
}

// A cleanup that finds nothing to drop must leave the file alone; one that does
// replaces it through a rename, so a reader never sees a half-written file.
func TestPruneReplacesFileOnlyWhenSomethingGoes(t *testing.T) {
	l, clock, _ := newTestLog(t, t0)
	if err := l.Write(testRecord{RequestID: "a"}); err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(l.Path())
	if err != nil {
		t.Fatal(err)
	}

	*clock = t0.Add(2 * time.Hour)
	if err := l.Write(testRecord{RequestID: "b"}); err != nil {
		t.Fatal(err)
	}
	after, err := os.Stat(l.Path())
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(before, after) {
		t.Fatal("a cleanup with nothing to drop replaced the file")
	}

	// Now something expires: eight days later both earlier records are old.
	*clock = t0.Add(8*24*time.Hour + 3*time.Hour)
	if err := l.Write(testRecord{RequestID: "c"}); err != nil {
		t.Fatal(err)
	}
	replaced, err := os.Stat(l.Path())
	if err != nil {
		t.Fatal(err)
	}
	if os.SameFile(after, replaced) {
		t.Fatal("expired lines were dropped by rewriting the file in place")
	}
	lines := readLines(t, l.Path())
	if len(lines) != 1 || !strings.Contains(lines[0], `"request_id":"c"`) {
		t.Fatalf("lines = %v, want only c", lines)
	}
	entries, _ := os.ReadDir(filepath.Dir(l.Path()))
	if len(entries) != 1 {
		t.Fatalf("directory holds %d entries, want only the log", len(entries))
	}
}

// A record is a few megabytes; the cleanup must neither lose nor parse it whole.
func TestPruneKeepsHugeLinesIntact(t *testing.T) {
	l, _, _ := newTestLog(t, t0)
	huge := `{"created_at":"` + day(1) + `","request_id":"huge","pad":"` + strings.Repeat("я", 3<<20) + `"}`
	seed(t, l, `{"created_at":"`+day(8)+`","request_id":"old"}`+"\n"+huge+"\n")

	if err := l.Write(testRecord{RequestID: "new"}); err != nil {
		t.Fatal(err)
	}
	lines := readLines(t, l.Path())
	if len(lines) != 2 || lines[0] != huge {
		t.Fatalf("got %d lines, first intact: %v", len(lines), len(lines) > 0 && lines[0] == huge)
	}
}

func TestPruneKeepsCRLFLinesAsLines(t *testing.T) {
	l, _, _ := newTestLog(t, t0)
	seed(t, l, `{"created_at":"`+day(8)+`","request_id":"old"}`+"\r\n"+`{"created_at":"`+day(1)+`","request_id":"fresh"}`+"\r\n")
	if err := l.Write(testRecord{RequestID: "new"}); err != nil {
		t.Fatal(err)
	}
	lines := readLines(t, l.Path())
	if len(lines) != 2 || strings.Contains(lines[0], "\r") || !strings.Contains(lines[0], "fresh") {
		t.Fatalf("lines = %q", lines)
	}
}

// doc §7.1: "errors are logged instead of swallowed."
func TestWriteReturnsErrors(t *testing.T) {
	dir := t.TempDir()
	notADir := filepath.Join(dir, "file")
	if err := os.WriteFile(notADir, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	l := New(filepath.Join(notADir, "logs"), nil)
	if err := l.Write(testRecord{}); err == nil {
		t.Fatal("writing under a regular file must fail")
	}

	// The log path itself is a directory.
	l2 := New(dir, nil)
	if err := os.Mkdir(l2.Path(), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := l2.Write(testRecord{}); err == nil {
		t.Fatal("a directory in place of the log must fail")
	}
}

func TestPruneFailureIsLoggedAndTheRecordStillWritten(t *testing.T) {
	l, _, logs := newTestLog(t, t0)
	seed(t, l, `{"created_at":"`+day(8)+`","request_id":"old"}`+"\n")
	// The temporary file cannot be created: a directory sits where it goes.
	if err := os.Mkdir(l.Path()+".tmp", 0o700); err != nil {
		t.Fatal(err)
	}
	if err := l.Write(testRecord{RequestID: "new"}); err != nil {
		t.Fatalf("the record must be written despite the failed cleanup: %v", err)
	}
	if !strings.Contains(logs.joined(), "ERROR audit log cleanup failed") {
		t.Fatalf("cleanup failure not logged: %q", logs.joined())
	}
	if all := strings.Join(readLines(t, l.Path()), "\n"); !strings.Contains(all, `"request_id":"new"`) || !strings.Contains(all, "old") {
		t.Fatalf("log after failed cleanup:\n%s", all)
	}
}

func TestConcurrentWritesStayWholeLines(t *testing.T) {
	l, _, _ := newTestLog(t, t0)
	var wg sync.WaitGroup
	for g := 0; g < 16; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 20; i++ {
				if err := l.Write(testRecord{RequestID: "r", Attempt: i, Text: strings.Repeat("x", 5000)}); err != nil {
					t.Error(err)
				}
			}
		}()
	}
	wg.Wait()
	lines := readLines(t, l.Path())
	if len(lines) != 16*20 {
		t.Fatalf("lines = %d, want %d", len(lines), 16*20)
	}
	for _, line := range lines {
		var m map[string]any
		if err := json.NewDecoder(bytes.NewReader([]byte(line))).Decode(&m); err != nil {
			t.Fatalf("torn line: %v", err)
		}
	}
}
