package tui

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestTailFile_BasicSmallFile: a file smaller than the tail window returns
// all its non-empty lines (in order).
func TestTailFile_BasicSmallFile(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "small.log")
	body := "a\nb\nc\n"
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	got := tailFile(p, 10)
	want := []string{"a", "b", "c"}
	if len(got) != len(want) {
		t.Fatalf("len=%d, want %d; got=%v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("line %d: got %q, want %q", i, got[i], want[i])
		}
	}
}

// TestTailFile_ExactlyN: returns the last N lines when the file has more.
func TestTailFile_ExactlyN(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "medium.log")
	var buf bytes.Buffer
	for i := 0; i < 1000; i++ {
		fmt.Fprintf(&buf, "line-%04d\n", i)
	}
	if err := os.WriteFile(p, buf.Bytes(), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	got := tailFile(p, 5)
	want := []string{"line-0995", "line-0996", "line-0997", "line-0998", "line-0999"}
	if len(got) != len(want) {
		t.Fatalf("len=%d, want %d; got=%v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("line %d: got %q, want %q", i, got[i], want[i])
		}
	}
}

// TestTailFile_MissingFile: tolerates missing paths and returns nil.
func TestTailFile_MissingFile(t *testing.T) {
	got := tailFile(filepath.Join(t.TempDir(), "nope.log"), 20)
	if got != nil {
		t.Fatalf("missing file: want nil, got %v", got)
	}
}

// TestTailFile_LargeFileStreams feeds a 10MB log file to tailFile and
// verifies:
//
//	(a) the returned tail has exactly n lines
//	(b) content matches the last n lines
//
// This is the streaming-read test: before the fix, tailFile called
// os.ReadFile which loaded all 10MB into memory. After the fix, tailFile
// reads chunks from the end until it finds n newlines.
func TestTailFile_LargeFileStreams(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "big.log")

	// Build a line format that is ~100 bytes each so 100k lines ≈ 10MB.
	// Use a predictable content so we can validate the last N lines.
	const total = 100_000
	const pad = "........................................................................." // 73 dots
	f, err := os.Create(p)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	for i := 0; i < total; i++ {
		fmt.Fprintf(f, "line-%07d %s\n", i, pad)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	// Sanity-check the file is actually large (> 5MB).
	fi, err := os.Stat(p)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if fi.Size() < 5*1024*1024 {
		t.Fatalf("fixture too small: %d bytes", fi.Size())
	}

	n := 20
	got := tailFile(p, n)
	if len(got) != n {
		t.Fatalf("len=%d want %d", len(got), n)
	}
	for i, line := range got {
		wantPrefix := fmt.Sprintf("line-%07d ", total-n+i)
		if !strings.HasPrefix(line, wantPrefix) {
			t.Fatalf("line %d: prefix=%q want %q", i, line[:min(20, len(line))], wantPrefix)
		}
	}
}

// TestTailFile_FileEndingWithoutNewline: the last line (no trailing \n)
// must still be included.
func TestTailFile_FileEndingWithoutNewline(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "noendnl.log")
	// no trailing newline
	if err := os.WriteFile(p, []byte("alpha\nbeta\ngamma"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	got := tailFile(p, 2)
	want := []string{"beta", "gamma"}
	if len(got) != len(want) {
		t.Fatalf("len=%d want %d; got=%v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("line %d: got %q want %q", i, got[i], want[i])
		}
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
