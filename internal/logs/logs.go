// Package logs provides a per-run logging directory that mirrors the behavior
// of the original gh-crfix bash tool: a timestamped master log, per-PR logs,
// and small status/started sidecar files used by the rest of the pipeline.
package logs

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// tsFormat matches bash's `date '+%Y-%m-%d %H:%M:%S'` output.
const tsFormat = "2006-01-02 15:04:05"

// Run owns the on-disk artifacts of a single gh-crfix run.
// All methods on *Run are safe for concurrent use.
type Run struct {
	dir       string
	masterLog string

	mu     sync.Mutex
	master *os.File
	closed bool
}

// NewRun creates a fresh temp dir under /tmp, opens the master log, and best-
// effort updates $HOME/.gh-crfix/last-run to point at it. A failure to
// create the symlink is logged to the master log and swallowed — the run
// proceeds regardless.
func NewRun() (*Run, error) {
	dir, err := os.MkdirTemp("", "gh-crfix-*")
	if err != nil {
		return nil, fmt.Errorf("create log dir: %w", err)
	}

	masterPath := filepath.Join(dir, "run.log")
	f, err := os.OpenFile(masterPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		// Best-effort cleanup; ignore removal error.
		_ = os.RemoveAll(dir)
		return nil, fmt.Errorf("open master log: %w", err)
	}

	r := &Run{dir: dir, masterLog: masterPath, master: f}
	r.updateLastRunSymlink()
	return r, nil
}

// updateLastRunSymlink mirrors `ln -sfn "$LOG_DIR" "$HOME/.gh-crfix/last-run"`.
// Any failure is recorded in the master log and otherwise ignored.
func (r *Run) updateLastRunSymlink() {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		r.Mlog("warn: could not resolve HOME for last-run symlink: %v", err)
		return
	}
	parent := filepath.Join(home, ".gh-crfix")
	if err := os.MkdirAll(parent, 0o755); err != nil {
		r.Mlog("warn: could not create %s: %v", parent, err)
		return
	}
	link := filepath.Join(parent, "last-run")
	// `-sfn` semantics: replace an existing symlink without following it.
	// os.Remove on a broken or valid symlink removes the link itself.
	if _, err := os.Lstat(link); err == nil {
		if err := os.Remove(link); err != nil {
			r.Mlog("warn: could not remove existing symlink %s: %v", link, err)
			return
		}
	}
	if err := os.Symlink(r.dir, link); err != nil {
		r.Mlog("warn: could not create last-run symlink at %s: %v", link, err)
	}
}

// Dir returns the absolute path to this run's log directory.
func (r *Run) Dir() string { return r.dir }

// MasterLog returns the absolute path to run.log.
func (r *Run) MasterLog() string { return r.masterLog }

// PRLog returns the path where pr-$N.log would live. It does not create
// the file.
func (r *Run) PRLog(prNum int) string {
	return filepath.Join(r.dir, fmt.Sprintf("pr-%d.log", prNum))
}

// Mlog appends one timestamped line to the master log. Formatting follows
// fmt.Sprintf. Writes are serialized across goroutines.
func (r *Run) Mlog(format string, a ...any) {
	line := r.formatLine(format, a...)
	r.writeMaster(line)
}

// MlogTo appends one timestamped line to the master log AND to the file at
// `path`. If `path` can't be opened, the error is recorded in the master
// log — MlogTo never panics.
func (r *Run) MlogTo(path, format string, a ...any) {
	line := r.formatLine(format, a...)
	r.writeMaster(line)

	// Opening the secondary file doesn't share the master mutex — a slow
	// per-PR file should not block master log writes from other PRs.
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		r.Mlog("warn: could not open %s for MlogTo: %v", path, err)
		return
	}
	defer f.Close()
	if _, err := f.WriteString(line); err != nil {
		r.Mlog("warn: write to %s failed: %v", path, err)
	}
}

// MlogFile dumps the contents of filePath into the master log, surrounded
// by banner lines containing `title`. Missing/unreadable files produce a
// warning entry but never an error.
func (r *Run) MlogFile(title, filePath string) {
	banner := strings.Repeat("-", 8)
	r.Mlog("%s BEGIN %s (%s) %s", banner, title, filePath, banner)

	f, err := os.Open(filePath)
	if err != nil {
		r.Mlog("warn: could not open %s: %v", filePath, err)
		r.Mlog("%s END   %s (%s) %s", banner, title, filePath, banner)
		return
	}
	defer f.Close()

	r.mu.Lock()
	if !r.closed && r.master != nil {
		// Stream the file under the lock so its contents stay contiguous
		// relative to surrounding timestamped banners.
		if _, err := io.Copy(r.master, f); err != nil {
			// Release before re-entering Mlog, which also takes the lock.
			r.mu.Unlock()
			r.Mlog("warn: copy %s into master failed: %v", filePath, err)
			r.Mlog("%s END   %s (%s) %s", banner, title, filePath, banner)
			return
		}
		// Ensure the dump ends with a newline before the END banner.
		_, _ = r.master.WriteString("\n")
	}
	r.mu.Unlock()

	r.Mlog("%s END   %s (%s) %s", banner, title, filePath, banner)
}

// MarkStarted records `time.Now().Unix()` into pr-$N.started atomically.
func (r *Run) MarkStarted(prNum int) error {
	path := filepath.Join(r.dir, fmt.Sprintf("pr-%d.started", prNum))
	return atomicWrite(path, []byte(strconv.FormatInt(time.Now().Unix(), 10)+"\n"))
}

// MarkStatus writes "OK" or "FAIL" into pr-$N.status atomically.
func (r *Run) MarkStatus(prNum int, ok bool) error {
	path := filepath.Join(r.dir, fmt.Sprintf("pr-%d.status", prNum))
	body := "FAIL\n"
	if ok {
		body = "OK\n"
	}
	return atomicWrite(path, []byte(body))
}

// ReadStatus returns ("OK"|"FAIL"|"", started). `started` is true whenever
// pr-$N.started exists, regardless of whether .status has been written.
func (r *Run) ReadStatus(prNum int) (string, bool) {
	startedPath := filepath.Join(r.dir, fmt.Sprintf("pr-%d.started", prNum))
	started := false
	if _, err := os.Stat(startedPath); err == nil {
		started = true
	}

	statusPath := filepath.Join(r.dir, fmt.Sprintf("pr-%d.status", prNum))
	data, err := os.ReadFile(statusPath)
	if err != nil {
		return "", started
	}
	s := strings.TrimSpace(string(data))
	switch s {
	case "OK", "FAIL":
		return s, started
	default:
		// Unknown contents — surface the raw value so callers can decide,
		// but trim to keep things tidy.
		return s, started
	}
}

// Elapsed returns time since MarkStarted, or (0, false) if no .started file.
func (r *Run) Elapsed(prNum int) (time.Duration, bool) {
	path := filepath.Join(r.dir, fmt.Sprintf("pr-%d.started", prNum))
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, false
	}
	s := strings.TrimSpace(string(data))
	secs, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return 0, false
	}
	return time.Since(time.Unix(secs, 0)), true
}

// Close releases the master log file handle. Reserved for future buffering.
// Safe to call multiple times.
func (r *Run) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return nil
	}
	r.closed = true
	if r.master != nil {
		err := r.master.Close()
		r.master = nil
		return err
	}
	return nil
}

// --- internals -------------------------------------------------------------

func (r *Run) formatLine(format string, a ...any) string {
	msg := fmt.Sprintf(format, a...)
	// Strip a trailing newline so the timestamp prefix + caller's message
	// always render as exactly one line.
	msg = strings.TrimRight(msg, "\n")
	return fmt.Sprintf("%s  %s\n", time.Now().Format(tsFormat), msg)
}

func (r *Run) writeMaster(line string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed || r.master == nil {
		return
	}
	// Best-effort: a write failure here is noted to stderr since the
	// master log itself is the thing that failed.
	if _, err := r.master.WriteString(line); err != nil {
		fmt.Fprintf(os.Stderr, "gh-crfix: master log write failed: %v\n", err)
	}
}

// atomicWrite writes data to path via a sibling .tmp file + rename, so
// readers never observe a partial file.
func atomicWrite(path string, data []byte) error {
	tmp := path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("create %s: %w", tmp, err)
	}
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return fmt.Errorf("write %s: %w", tmp, err)
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("close %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("rename %s -> %s: %w", tmp, path, err)
	}
	return nil
}
