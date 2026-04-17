package logs

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"
)

// setFakeHome redirects $HOME to a test-owned temp dir so the last-run
// symlink does not touch the real filesystem.
func setFakeHome(t *testing.T) string {
	t.Helper()
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	return tmp
}

func TestNewRunCreatesDirMasterAndSymlink(t *testing.T) {
	home := setFakeHome(t)

	r, err := NewRun()
	if err != nil {
		t.Fatalf("NewRun: %v", err)
	}
	defer r.Close()

	// Dir exists.
	info, err := os.Stat(r.Dir())
	if err != nil {
		t.Fatalf("stat dir: %v", err)
	}
	if !info.IsDir() {
		t.Fatalf("expected Dir() to be directory, got %v", info.Mode())
	}
	if !strings.HasPrefix(filepath.Base(r.Dir()), "gh-crfix-") {
		t.Errorf("expected dir to start with gh-crfix-, got %q", r.Dir())
	}

	// Master log exists.
	if _, err := os.Stat(r.MasterLog()); err != nil {
		t.Fatalf("master log not created: %v", err)
	}
	if r.MasterLog() != filepath.Join(r.Dir(), "run.log") {
		t.Errorf("master log path unexpected: %q", r.MasterLog())
	}

	// Symlink created.
	linkPath := filepath.Join(home, ".gh-crfix", "last-run")
	target, err := os.Readlink(linkPath)
	if err != nil {
		t.Fatalf("readlink: %v", err)
	}
	if target != r.Dir() {
		t.Errorf("symlink target mismatch: got %q want %q", target, r.Dir())
	}
}

func TestMlogFormatting(t *testing.T) {
	setFakeHome(t)
	r, err := NewRun()
	if err != nil {
		t.Fatalf("NewRun: %v", err)
	}
	defer r.Close()

	r.Mlog("hello %s", "world")

	data, err := os.ReadFile(r.MasterLog())
	if err != nil {
		t.Fatalf("read master: %v", err)
	}
	got := strings.TrimRight(string(data), "\n")
	re := regexp.MustCompile(`^\d{4}-\d{2}-\d{2} \d{2}:\d{2}:\d{2}  hello world$`)
	if !re.MatchString(got) {
		t.Fatalf("master log line does not match format: %q", got)
	}
}

func TestMlogConcurrency(t *testing.T) {
	setFakeHome(t)
	r, err := NewRun()
	if err != nil {
		t.Fatalf("NewRun: %v", err)
	}
	defer r.Close()

	const n = 100
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			r.Mlog("line-%03d", i)
		}(i)
	}
	wg.Wait()

	f, err := os.Open(r.MasterLog())
	if err != nil {
		t.Fatalf("open master: %v", err)
	}
	defer f.Close()

	seen := make(map[string]bool)
	lineRe := regexp.MustCompile(`^\d{4}-\d{2}-\d{2} \d{2}:\d{2}:\d{2}  (line-\d{3})$`)
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		m := lineRe.FindStringSubmatch(scanner.Text())
		if m == nil {
			t.Fatalf("unexpected line format: %q", scanner.Text())
		}
		if seen[m[1]] {
			t.Errorf("duplicate line %q", m[1])
		}
		seen[m[1]] = true
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("scan: %v", err)
	}
	if len(seen) != n {
		t.Fatalf("expected %d distinct lines, got %d", n, len(seen))
	}
}

func TestMlogToWritesBoth(t *testing.T) {
	setFakeHome(t)
	r, err := NewRun()
	if err != nil {
		t.Fatalf("NewRun: %v", err)
	}
	defer r.Close()

	prLog := r.PRLog(42)
	r.MlogTo(prLog, "processing %d", 42)

	master, err := os.ReadFile(r.MasterLog())
	if err != nil {
		t.Fatalf("read master: %v", err)
	}
	pr, err := os.ReadFile(prLog)
	if err != nil {
		t.Fatalf("read pr log: %v", err)
	}

	if !strings.Contains(string(master), "processing 42") {
		t.Errorf("master missing message: %q", string(master))
	}
	if !strings.Contains(string(pr), "processing 42") {
		t.Errorf("pr log missing message: %q", string(pr))
	}
	re := regexp.MustCompile(`^\d{4}-\d{2}-\d{2} \d{2}:\d{2}:\d{2}  processing 42$`)
	if !re.MatchString(strings.TrimRight(string(pr), "\n")) {
		t.Errorf("pr log format wrong: %q", string(pr))
	}
}

func TestMlogFileEmbedsContents(t *testing.T) {
	setFakeHome(t)
	r, err := NewRun()
	if err != nil {
		t.Fatalf("NewRun: %v", err)
	}
	defer r.Close()

	src := filepath.Join(t.TempDir(), "snippet.txt")
	body := "alpha\nbeta\ngamma\n"
	if err := os.WriteFile(src, []byte(body), 0o644); err != nil {
		t.Fatalf("write src: %v", err)
	}

	r.MlogFile("CI LOG", src)

	data, err := os.ReadFile(r.MasterLog())
	if err != nil {
		t.Fatalf("read master: %v", err)
	}
	s := string(data)
	if !strings.Contains(s, "CI LOG") {
		t.Errorf("expected banner CI LOG in master: %q", s)
	}
	if !strings.Contains(s, "alpha") || !strings.Contains(s, "beta") || !strings.Contains(s, "gamma") {
		t.Errorf("expected file contents dumped into master: %q", s)
	}
}

func TestPRLogPathShape(t *testing.T) {
	setFakeHome(t)
	r, err := NewRun()
	if err != nil {
		t.Fatalf("NewRun: %v", err)
	}
	defer r.Close()

	got := r.PRLog(17)
	want := filepath.Join(r.Dir(), "pr-17.log")
	if got != want {
		t.Errorf("PRLog mismatch: got %q want %q", got, want)
	}
}

func TestMarkStartedAndElapsed(t *testing.T) {
	setFakeHome(t)
	r, err := NewRun()
	if err != nil {
		t.Fatalf("NewRun: %v", err)
	}
	defer r.Close()

	// Before start → not found.
	if _, ok := r.Elapsed(5); ok {
		t.Fatalf("Elapsed should be false before MarkStarted")
	}

	if err := r.MarkStarted(5); err != nil {
		t.Fatalf("MarkStarted: %v", err)
	}
	startedPath := filepath.Join(r.Dir(), "pr-5.started")
	if _, err := os.Stat(startedPath); err != nil {
		t.Fatalf("started file missing: %v", err)
	}

	// Round-trip: file should contain a unix epoch integer.
	raw, err := os.ReadFile(startedPath)
	if err != nil {
		t.Fatalf("read started: %v", err)
	}
	s := strings.TrimSpace(string(raw))
	if len(s) == 0 {
		t.Fatalf("started file empty")
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			t.Fatalf("expected digits only in started file, got %q", s)
		}
	}

	time.Sleep(1100 * time.Millisecond)
	d, ok := r.Elapsed(5)
	if !ok {
		t.Fatalf("Elapsed after MarkStarted should be ok")
	}
	if d < time.Second {
		t.Errorf("expected elapsed >= 1s, got %v", d)
	}
}

func TestMarkStatusAndReadStatusOK(t *testing.T) {
	setFakeHome(t)
	r, err := NewRun()
	if err != nil {
		t.Fatalf("NewRun: %v", err)
	}
	defer r.Close()

	if status, started := r.ReadStatus(1); status != "" || started {
		t.Fatalf("expected empty+!started before writes, got %q started=%v", status, started)
	}

	if err := r.MarkStarted(1); err != nil {
		t.Fatalf("MarkStarted: %v", err)
	}
	if err := r.MarkStatus(1, true); err != nil {
		t.Fatalf("MarkStatus: %v", err)
	}

	status, started := r.ReadStatus(1)
	if status != "OK" {
		t.Errorf("expected OK, got %q", status)
	}
	if !started {
		t.Errorf("expected started true")
	}

	// Verify on-disk contents — "OK" with no trailing junk beyond newline.
	data, err := os.ReadFile(filepath.Join(r.Dir(), "pr-1.status"))
	if err != nil {
		t.Fatalf("read status: %v", err)
	}
	if strings.TrimSpace(string(data)) != "OK" {
		t.Errorf("unexpected on-disk status: %q", string(data))
	}
}

func TestMarkStatusAndReadStatusFAIL(t *testing.T) {
	setFakeHome(t)
	r, err := NewRun()
	if err != nil {
		t.Fatalf("NewRun: %v", err)
	}
	defer r.Close()

	if err := r.MarkStatus(9, false); err != nil {
		t.Fatalf("MarkStatus: %v", err)
	}
	status, _ := r.ReadStatus(9)
	if status != "FAIL" {
		t.Errorf("expected FAIL, got %q", status)
	}
}

func TestReadStatusStartedOnly(t *testing.T) {
	setFakeHome(t)
	r, err := NewRun()
	if err != nil {
		t.Fatalf("NewRun: %v", err)
	}
	defer r.Close()

	if err := r.MarkStarted(77); err != nil {
		t.Fatalf("MarkStarted: %v", err)
	}
	status, started := r.ReadStatus(77)
	if status != "" {
		t.Errorf("expected empty status, got %q", status)
	}
	if !started {
		t.Errorf("expected started=true once .started exists")
	}
}

func TestAtomicStatusWrite(t *testing.T) {
	// Ensure no leftover .tmp files after MarkStatus.
	setFakeHome(t)
	r, err := NewRun()
	if err != nil {
		t.Fatalf("NewRun: %v", err)
	}
	defer r.Close()

	if err := r.MarkStatus(3, true); err != nil {
		t.Fatalf("MarkStatus: %v", err)
	}
	entries, err := os.ReadDir(r.Dir())
	if err != nil {
		t.Fatalf("readdir: %v", err)
	}
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".tmp") {
			t.Errorf("stray .tmp file after atomic write: %s", e.Name())
		}
	}
}

func TestCloseIsNoop(t *testing.T) {
	setFakeHome(t)
	r, err := NewRun()
	if err != nil {
		t.Fatalf("NewRun: %v", err)
	}
	if err := r.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	// Double-close must not panic/error.
	if err := r.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
}

// Compile-time sanity: the public signatures should match what the spec
// promises. This won't compile if the API drifts.
var _ = func(r *Run) {
	var _ string = r.Dir()
	var _ string = r.MasterLog()
	var _ string = r.PRLog(0)
	r.Mlog("%s", "x")
	r.MlogTo("x", "%s", "y")
	r.MlogFile("t", "p")
	_ = r.MarkStarted(0)
	_ = r.MarkStatus(0, true)
	_, _ = r.ReadStatus(0)
	_, _ = r.Elapsed(0)
	_ = r.Close()
	_ = fmt.Sprintf // keep import used
}
