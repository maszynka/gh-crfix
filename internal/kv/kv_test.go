package kv

import (
	"testing"
)

func TestNewStore(t *testing.T) {
	dir := t.TempDir()
	s := NewStore(dir)
	if s == nil {
		t.Fatal("NewStore returned nil")
	}
	if s.Dir != dir {
		t.Errorf("Store.Dir = %q, want %q", s.Dir, dir)
	}
}

func TestSetAndGet(t *testing.T) {
	dir := t.TempDir()
	s := NewStore(dir)

	t.Run("set then get retrieves same value", func(t *testing.T) {
		if err := s.Set("ns", "key", "hello"); err != nil {
			t.Fatalf("Set error: %v", err)
		}
		got, err := s.Get("ns", "key")
		if err != nil {
			t.Fatalf("Get error: %v", err)
		}
		if got != "hello" {
			t.Errorf("Get = %q, want %q", got, "hello")
		}
	})

	t.Run("get on missing key returns empty string and no error", func(t *testing.T) {
		got, err := s.Get("ns", "nonexistent")
		if err != nil {
			t.Fatalf("Get on missing key returned error: %v", err)
		}
		if got != "" {
			t.Errorf("Get on missing = %q, want %q", got, "")
		}
	})

	t.Run("set overwrites previous value", func(t *testing.T) {
		if err := s.Set("ns", "overwrite", "first"); err != nil {
			t.Fatalf("Set error: %v", err)
		}
		if err := s.Set("ns", "overwrite", "second"); err != nil {
			t.Fatalf("Set error: %v", err)
		}
		got, err := s.Get("ns", "overwrite")
		if err != nil {
			t.Fatalf("Get error: %v", err)
		}
		if got != "second" {
			t.Errorf("Get after overwrite = %q, want %q", got, "second")
		}
	})
}

func TestAppendAndList(t *testing.T) {
	dir := t.TempDir()
	s := NewStore(dir)

	t.Run("append adds lines, list returns them in order", func(t *testing.T) {
		if err := s.Append("mylist", "line1"); err != nil {
			t.Fatalf("Append error: %v", err)
		}
		if err := s.Append("mylist", "line2"); err != nil {
			t.Fatalf("Append error: %v", err)
		}
		if err := s.Append("mylist", "line3"); err != nil {
			t.Fatalf("Append error: %v", err)
		}

		got, err := s.List("mylist")
		if err != nil {
			t.Fatalf("List error: %v", err)
		}
		want := []string{"line1", "line2", "line3"}
		if len(got) != len(want) {
			t.Fatalf("List length = %d, want %d; got %v", len(got), len(want), got)
		}
		for i := range want {
			if got[i] != want[i] {
				t.Errorf("List[%d] = %q, want %q", i, got[i], want[i])
			}
		}
	})

	t.Run("list on empty list returns empty slice", func(t *testing.T) {
		got, err := s.List("emptylist")
		if err != nil {
			t.Fatalf("List error: %v", err)
		}
		if len(got) != 0 {
			t.Errorf("List on empty = %v, want []", got)
		}
	})

	t.Run("entries have no trailing newline", func(t *testing.T) {
		if err := s.Append("newlines", "value"); err != nil {
			t.Fatalf("Append error: %v", err)
		}
		got, err := s.List("newlines")
		if err != nil {
			t.Fatalf("List error: %v", err)
		}
		if len(got) != 1 {
			t.Fatalf("List length = %d, want 1", len(got))
		}
		if got[0] != "value" {
			t.Errorf("List entry = %q, want %q (no trailing newline)", got[0], "value")
		}
	})
}
