package conflict

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func initRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for _, args := range [][]string{
		{"init", "--quiet"},
		{"config", "user.email", "t@example.com"},
		{"config", "user.name", "Test"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	return dir
}

func commitFile(t *testing.T, dir, name, body string) {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"add", name},
		{"commit", "-m", "add " + name, "--quiet"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
}

func TestDetectMarkers_Clean(t *testing.T) {
	dir := initRepo(t)
	commitFile(t, dir, "a.txt", "hello\n")
	files, err := DetectMarkers(dir)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if len(files) != 0 {
		t.Fatalf("expected clean, got %v", files)
	}
}

func TestDetectMarkers_Found(t *testing.T) {
	dir := initRepo(t)
	body := "line1\n<<<<<<< HEAD\nours\n=======\ntheirs\n>>>>>>> main\nline2\n"
	commitFile(t, dir, "conflict.txt", body)
	commitFile(t, dir, "clean.txt", "no markers here\n")
	files, err := DetectMarkers(dir)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if len(files) != 1 || files[0] != "conflict.txt" {
		t.Fatalf("unexpected: %v", files)
	}
}

func TestBuildFixPrompt(t *testing.T) {
	p := BuildFixPrompt([]string{"a.go", "b.md"})
	if !strings.Contains(p, "a.go") || !strings.Contains(p, "b.md") {
		t.Fatalf("missing files in prompt: %s", p)
	}
	if !strings.Contains(p, "conflict marker") {
		t.Fatalf("missing guidance: %s", p)
	}
}
