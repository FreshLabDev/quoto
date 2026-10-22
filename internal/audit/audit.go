// SPDX-License-Identifier: Apache-2.0

// Package audit keeps ai_audit.jsonl: one JSON line per model call, with the
// request body and the raw answer. It holds real chat text, so the file is
// readable by its owner only and lines expire after seven days.
//
// The Python writer rewrote the whole file on every record whenever anything
// had expired, in place, and swallowed every error. Here a record is a single
// append; expired lines are dropped at most once an hour by writing a temporary
// file and renaming it over the log; failures are returned or logged.
package audit

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"
)

const (
	// FileName is the log's name inside the audit directory.
	FileName = "ai_audit.jsonl"
	// Retention is how long a line is kept.
	Retention = 7 * 24 * time.Hour
	// PruneInterval is the shortest time between two cleanups.
	PruneInterval = time.Hour

	fileMode = 0o600
	dirMode  = 0o700
	// maxLine bounds what pruning holds in memory for one line. A record with
	// 1500 messages and the answer is a few megabytes; anything past this is
	// not a record.
	maxLine = 64 << 20
)

// Log appends records to the audit file. Safe for concurrent use.
type Log struct {
	dir  string
	path string
	log  *slog.Logger
	now  func() time.Time

	mu        sync.Mutex
	lastPrune time.Time
}

// New returns a Log writing to dir/ai_audit.jsonl. The directory is created on
// the first write.
func New(dir string, log *slog.Logger) *Log {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &Log{dir: dir, path: filepath.Join(dir, FileName), log: log, now: time.Now}
}

// Prune drops expired lines now. The log does it on its first write and then at
// most once an hour while it is written to; the process calls this at start, so
// a bot that has not called a model for a week still keeps its promise.
func (l *Log) Prune() {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now().UTC()
	l.lastPrune = now
	if err := l.prune(now); err != nil {
		l.log.Error("audit log cleanup failed", "path", l.path, "err", err)
	}
}

// Path is the file the log writes to.
func (l *Log) Path() string { return l.path }

// Write appends v, which must marshal to a JSON object, as one line. The line
// starts with "created_at" (UTC, ISO 8601 with microseconds and +00:00, as
// Python's isoformat wrote it) and then carries v's keys in their order.
func (l *Log) Write(v any) error {
	now := l.now().UTC()
	line, err := encode(now, v)
	if err != nil {
		return fmt.Errorf("audit: encode record: %w", err)
	}

	l.mu.Lock()
	defer l.mu.Unlock()

	if err := os.MkdirAll(l.dir, dirMode); err != nil {
		return fmt.Errorf("audit: create directory: %w", err)
	}
	if l.lastPrune.IsZero() || now.Sub(l.lastPrune) >= PruneInterval {
		// Stamp first: a cleanup that keeps failing must not run on every write.
		l.lastPrune = now
		if err := l.prune(now); err != nil {
			l.log.Error("audit log cleanup failed", "path", l.path, "err", err)
		}
	}

	f, err := os.OpenFile(l.path, os.O_WRONLY|os.O_APPEND|os.O_CREATE, fileMode)
	if err != nil {
		return fmt.Errorf("audit: open log: %w", err)
	}
	// A file the Python version created is 0644. Whoever owns it may tighten it.
	if info, err := f.Stat(); err == nil && info.Mode().Perm() != fileMode {
		if err := f.Chmod(fileMode); err != nil {
			l.log.Warn("audit log mode not tightened", "path", l.path, "err", err)
		}
	}
	if _, err := f.Write(line); err != nil {
		f.Close()
		return fmt.Errorf("audit: write log: %w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("audit: close log: %w", err)
	}
	return nil
}

// encode renders the line: created_at first, then the keys of v untouched.
// HTML escaping is off, as in Python's json.dumps(ensure_ascii=False).
func encode(now time.Time, v any) ([]byte, error) {
	var body bytes.Buffer
	enc := json.NewEncoder(&body)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	obj := bytes.TrimSpace(body.Bytes())
	if len(obj) < 2 || obj[0] != '{' || obj[len(obj)-1] != '}' {
		return nil, errors.New("record is not a JSON object")
	}
	stamp, _ := json.Marshal(formatTime(now))
	line := make([]byte, 0, len(obj)+len(stamp)+32)
	line = append(line, `{"created_at":`...)
	line = append(line, stamp...)
	if len(obj) > 2 {
		line = append(line, ',')
	}
	line = append(line, obj[1:]...)
	return append(line, '\n'), nil
}

func formatTime(t time.Time) string {
	return t.UTC().Format("2006-01-02T15:04:05.000000") + "+00:00"
}

// prune drops lines older than Retention and blank lines. It does nothing, and
// touches nothing, when there is nothing to drop; otherwise it writes the kept
// lines to a temporary file next to the log and renames it over the log.
//
// A line whose created_at is missing or unreadable is kept: it is not ours to
// judge. A line past maxLine is dropped; it cannot be a record.
func (l *Log) prune(now time.Time) error {
	in, err := os.Open(l.path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer in.Close()

	cutoff := now.Add(-Retention)
	tmpPath := l.path + ".tmp"
	var (
		out      *os.File
		w        *bufio.Writer
		consumed int64 // bytes of the log read so far
		dropped  int
	)
	defer func() {
		if out != nil { // left open only by an error before the rename
			out.Close()
			os.Remove(tmpPath)
		}
	}()

	// start opens the temporary file at the first line that must go. Every line
	// before it was kept as it stands, so they are copied as they stand.
	start := func() error {
		f, err := os.OpenFile(tmpPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, fileMode)
		if err != nil {
			return err
		}
		out, w = f, bufio.NewWriterSize(f, 64<<10)
		_, err = io.Copy(w, io.NewSectionReader(in, 0, consumed))
		return err
	}

	r := bufio.NewReaderSize(in, 64<<10)
	for {
		line, n, tooLong, rerr := readLine(r)
		if n > 0 {
			trimmed := bytes.TrimRight(line, "\r\n")
			if tooLong || len(bytes.TrimSpace(trimmed)) == 0 || expired(trimmed, cutoff) {
				dropped++
				if out == nil {
					if err := start(); err != nil {
						return err
					}
				}
			} else if out != nil {
				if _, err := w.Write(trimmed); err != nil {
					return err
				}
				if err := w.WriteByte('\n'); err != nil {
					return err
				}
			}
			consumed += n
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			return rerr
		}
	}
	if out == nil {
		return nil
	}

	if err := w.Flush(); err != nil {
		return err
	}
	if err := out.Sync(); err != nil {
		return err
	}
	closing := out
	out = nil
	if err := closing.Close(); err != nil {
		os.Remove(tmpPath)
		return err
	}
	if err := os.Rename(tmpPath, l.path); err != nil {
		os.Remove(tmpPath)
		return err
	}
	l.log.Info("audit log cleaned", "dropped_lines", dropped)
	return nil
}

// readLine returns one line with its terminator and the number of bytes it
// occupied. A line longer than maxLine is read through without being kept and
// reported as tooLong.
func readLine(r *bufio.Reader) (line []byte, n int64, tooLong bool, err error) {
	for {
		chunk, cerr := r.ReadSlice('\n')
		n += int64(len(chunk))
		if !tooLong {
			if int64(len(line))+int64(len(chunk)) > maxLine {
				tooLong, line = true, nil
			} else {
				line = append(line, chunk...)
			}
		}
		if cerr == bufio.ErrBufferFull {
			continue
		}
		return line, n, tooLong, cerr
	}
}

// expired reports whether the line's created_at is before cutoff.
func expired(line []byte, cutoff time.Time) bool {
	ts, ok := createdAt(line)
	return ok && ts.Before(cutoff)
}

// createdAt reads the line's created_at. Both writers put it first, so the head
// is scanned by hand and a multi-megabyte line is not parsed; a line laid out
// otherwise (key elsewhere, escapes in the value) falls back to a full parse.
func createdAt(line []byte) (time.Time, bool) {
	if s, ok := scanCreatedAt(line); ok {
		return parseTime(s)
	}
	var probe struct {
		CreatedAt json.RawMessage `json:"created_at"`
	}
	if json.Unmarshal(line, &probe) != nil || len(probe.CreatedAt) == 0 {
		return time.Time{}, false
	}
	var s string
	if json.Unmarshal(probe.CreatedAt, &s) != nil {
		return time.Time{}, false
	}
	return parseTime(s)
}

// scanCreatedAt matches {"created_at": "..." at the start of line, with the
// optional spaces json.dumps puts after the colon.
func scanCreatedAt(line []byte) (string, bool) {
	const key = `"created_at"`
	i := skipSpace(line, 0)
	if i >= len(line) || line[i] != '{' {
		return "", false
	}
	i = skipSpace(line, i+1)
	if !bytes.HasPrefix(line[i:], []byte(key)) {
		return "", false
	}
	i = skipSpace(line, i+len(key))
	if i >= len(line) || line[i] != ':' {
		return "", false
	}
	i = skipSpace(line, i+1)
	if i >= len(line) || line[i] != '"' {
		return "", false
	}
	i++
	end := bytes.IndexByte(line[i:], '"')
	if end < 0 || bytes.IndexByte(line[i:i+end], '\\') >= 0 {
		return "", false
	}
	return string(line[i : i+end]), true
}

func skipSpace(b []byte, i int) int {
	for i < len(b) && (b[i] == ' ' || b[i] == '\t') {
		i++
	}
	return i
}

// parseTime reads what Python's isoformat wrote and what json.dumps of a naive
// datetime would have. A time without a zone is UTC, as the Python writer
// assumed.
func parseTime(s string) (time.Time, bool) {
	for _, layout := range []string{
		time.RFC3339Nano,
		"2006-01-02T15:04:05.999999999",
		"2006-01-02 15:04:05.999999999Z07:00",
		"2006-01-02 15:04:05.999999999",
	} {
		if t, err := time.Parse(layout, s); err == nil {
			return t, true
		}
	}
	return time.Time{}, false
}
