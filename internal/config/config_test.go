package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDefaults(t *testing.T) {
	c := Defaults()

	t.Run("Concurrency is 3", func(t *testing.T) {
		if c.Concurrency != 3 {
			t.Errorf("Defaults().Concurrency = %d, want 3", c.Concurrency)
		}
	})
	t.Run("AIBackend is auto", func(t *testing.T) {
		if c.AIBackend != "auto" {
			t.Errorf("Defaults().AIBackend = %q, want %q", c.AIBackend, "auto")
		}
	})
	t.Run("GateModel is sonnet", func(t *testing.T) {
		if c.GateModel != "sonnet" {
			t.Errorf("Defaults().GateModel = %q, want %q", c.GateModel, "sonnet")
		}
	})
	t.Run("FixModel is sonnet", func(t *testing.T) {
		if c.FixModel != "sonnet" {
			t.Errorf("Defaults().FixModel = %q, want %q", c.FixModel, "sonnet")
		}
	})
	t.Run("ScoreNeedsLLM is 1.0", func(t *testing.T) {
		if c.ScoreNeedsLLM != 1.0 {
			t.Errorf("Defaults().ScoreNeedsLLM = %v, want 1.0", c.ScoreNeedsLLM)
		}
	})
	t.Run("ScorePRComment is 0.4", func(t *testing.T) {
		if c.ScorePRComment != 0.4 {
			t.Errorf("Defaults().ScorePRComment = %v, want 0.4", c.ScorePRComment)
		}
	})
	t.Run("ScoreTestFailure is 1.0", func(t *testing.T) {
		if c.ScoreTestFailure != 1.0 {
			t.Errorf("Defaults().ScoreTestFailure = %v, want 1.0", c.ScoreTestFailure)
		}
	})
}

func TestLoad(t *testing.T) {
	t.Run("all keys set", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "defaults")
		content := `AI_BACKEND=claude
GATE_MODEL=opus
FIX_MODEL=haiku
CONCURRENCY=5
SCORE_NEEDS_LLM=0.800
SCORE_PR_COMMENT=0.300
SCORE_TEST_FAILURE=0.900
`
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		c, err := Load(path)
		if err != nil {
			t.Fatalf("Load error: %v", err)
		}
		if c.AIBackend != "claude" {
			t.Errorf("AIBackend = %q, want %q", c.AIBackend, "claude")
		}
		if c.GateModel != "opus" {
			t.Errorf("GateModel = %q, want %q", c.GateModel, "opus")
		}
		if c.FixModel != "haiku" {
			t.Errorf("FixModel = %q, want %q", c.FixModel, "haiku")
		}
		if c.Concurrency != 5 {
			t.Errorf("Concurrency = %d, want 5", c.Concurrency)
		}
		if c.ScoreNeedsLLM != 0.800 {
			t.Errorf("ScoreNeedsLLM = %v, want 0.800", c.ScoreNeedsLLM)
		}
		if c.ScorePRComment != 0.300 {
			t.Errorf("ScorePRComment = %v, want 0.300", c.ScorePRComment)
		}
		if c.ScoreTestFailure != 0.900 {
			t.Errorf("ScoreTestFailure = %v, want 0.900", c.ScoreTestFailure)
		}
	})

	t.Run("empty file returns defaults", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "defaults")
		if err := os.WriteFile(path, []byte(""), 0o644); err != nil {
			t.Fatal(err)
		}
		c, err := Load(path)
		if err != nil {
			t.Fatalf("Load error: %v", err)
		}
		def := Defaults()
		if c.Concurrency != def.Concurrency {
			t.Errorf("empty file: Concurrency = %d, want %d", c.Concurrency, def.Concurrency)
		}
		if c.AIBackend != def.AIBackend {
			t.Errorf("empty file: AIBackend = %q, want %q", c.AIBackend, def.AIBackend)
		}
		if c.GateModel != def.GateModel {
			t.Errorf("empty file: GateModel = %q, want %q", c.GateModel, def.GateModel)
		}
	})

	t.Run("unknown keys are ignored", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "defaults")
		content := `UNKNOWN_KEY=whatever
CONCURRENCY=7
`
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		c, err := Load(path)
		if err != nil {
			t.Fatalf("Load error: %v", err)
		}
		if c.Concurrency != 7 {
			t.Errorf("Concurrency = %d, want 7", c.Concurrency)
		}
	})

	t.Run("invalid CONCURRENCY keeps default", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "defaults")
		content := "CONCURRENCY=notanumber\n"
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		c, err := Load(path)
		if err != nil {
			t.Fatalf("Load error: %v", err)
		}
		def := Defaults()
		if c.Concurrency != def.Concurrency {
			t.Errorf("invalid CONCURRENCY: got %d, want default %d", c.Concurrency, def.Concurrency)
		}
	})

	t.Run("comments and blank lines ignored", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "defaults")
		content := `# this is a comment

CONCURRENCY=4

# another comment
`
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		c, err := Load(path)
		if err != nil {
			t.Fatalf("Load error: %v", err)
		}
		if c.Concurrency != 4 {
			t.Errorf("Concurrency = %d, want 4", c.Concurrency)
		}
	})

	t.Run("inline # comment in value is stripped", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "defaults")
		content := "AI_BACKEND=claude # set by launcher\nCONCURRENCY=6 # max workers\n"
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		c, err := Load(path)
		if err != nil {
			t.Fatalf("Load error: %v", err)
		}
		if c.AIBackend != "claude" {
			t.Errorf("AIBackend = %q, want %q", c.AIBackend, "claude")
		}
		if c.Concurrency != 6 {
			t.Errorf("Concurrency = %d, want 6", c.Concurrency)
		}
	})

	t.Run("score out of bounds keeps default", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "defaults")
		content := "SCORE_NEEDS_LLM=5\nSCORE_PR_COMMENT=-1\n"
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		c, err := Load(path)
		if err != nil {
			t.Fatalf("Load error: %v", err)
		}
		def := Defaults()
		if c.ScoreNeedsLLM != def.ScoreNeedsLLM {
			t.Errorf("ScoreNeedsLLM = %v after out-of-bounds value, want default %v", c.ScoreNeedsLLM, def.ScoreNeedsLLM)
		}
		if c.ScorePRComment != def.ScorePRComment {
			t.Errorf("ScorePRComment = %v after out-of-bounds value, want default %v", c.ScorePRComment, def.ScorePRComment)
		}
	})

	t.Run("whitespace around key and value trimmed", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "defaults")
		content := " CONCURRENCY = 9 \n"
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		c, err := Load(path)
		if err != nil {
			t.Fatalf("Load error: %v", err)
		}
		if c.Concurrency != 9 {
			t.Errorf("Concurrency = %d, want 9 (whitespace should be trimmed)", c.Concurrency)
		}
	})
}

// TestWorktreeMode covers the parsing rules for the WORKTREE_MODE config
// key: defaults to "temp" when missing, accepts the three valid values, and
// silently rejects unknown values (defensive against typos).
func TestWorktreeMode(t *testing.T) {
	t.Run("Defaults to temp", func(t *testing.T) {
		c := Defaults()
		if c.WorktreeMode != "temp" {
			t.Errorf("Defaults().WorktreeMode = %q, want %q", c.WorktreeMode, "temp")
		}
	})

	for _, mode := range []string{"temp", "reuse", "stash"} {
		t.Run("accepts "+mode, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "defaults")
			if err := os.WriteFile(path, []byte("WORKTREE_MODE="+mode+"\n"), 0o644); err != nil {
				t.Fatalf("write: %v", err)
			}
			c, err := Load(path)
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			if c.WorktreeMode != mode {
				t.Errorf("WorktreeMode = %q, want %q", c.WorktreeMode, mode)
			}
		})
	}

	t.Run("rejects unknown value (keeps default)", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "defaults")
		if err := os.WriteFile(path, []byte("WORKTREE_MODE=garbage\n"), 0o644); err != nil {
			t.Fatalf("write: %v", err)
		}
		c, err := Load(path)
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		if c.WorktreeMode != "temp" {
			t.Errorf("WorktreeMode = %q, want default %q after rejecting garbage",
				c.WorktreeMode, "temp")
		}
	})
}

func TestSaveAndLoad(t *testing.T) {
	t.Run("round-trip", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "defaults")

		original := Config{
			AIBackend:        "codex",
			GateModel:        "gpt-5.4-mini",
			FixModel:         "gpt-5.4",
			Concurrency:      8,
			ScoreNeedsLLM:    0.750,
			ScorePRComment:   0.250,
			ScoreTestFailure: 0.500,
			WorktreeMode:     "stash",
		}
		if err := Save(path, original); err != nil {
			t.Fatalf("Save error: %v", err)
		}
		loaded, err := Load(path)
		if err != nil {
			t.Fatalf("Load error: %v", err)
		}
		if loaded.WorktreeMode != original.WorktreeMode {
			t.Errorf("WorktreeMode = %q, want %q", loaded.WorktreeMode, original.WorktreeMode)
		}
		if loaded.AIBackend != original.AIBackend {
			t.Errorf("AIBackend = %q, want %q", loaded.AIBackend, original.AIBackend)
		}
		if loaded.GateModel != original.GateModel {
			t.Errorf("GateModel = %q, want %q", loaded.GateModel, original.GateModel)
		}
		if loaded.FixModel != original.FixModel {
			t.Errorf("FixModel = %q, want %q", loaded.FixModel, original.FixModel)
		}
		if loaded.Concurrency != original.Concurrency {
			t.Errorf("Concurrency = %d, want %d", loaded.Concurrency, original.Concurrency)
		}
		if loaded.ScoreNeedsLLM != original.ScoreNeedsLLM {
			t.Errorf("ScoreNeedsLLM = %v, want %v", loaded.ScoreNeedsLLM, original.ScoreNeedsLLM)
		}
		if loaded.ScorePRComment != original.ScorePRComment {
			t.Errorf("ScorePRComment = %v, want %v", loaded.ScorePRComment, original.ScorePRComment)
		}
		if loaded.ScoreTestFailure != original.ScoreTestFailure {
			t.Errorf("ScoreTestFailure = %v, want %v", loaded.ScoreTestFailure, original.ScoreTestFailure)
		}
	})

	t.Run("Save creates directories if needed", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "subdir", "nested", "defaults")
		c := Defaults()
		if err := Save(path, c); err != nil {
			t.Fatalf("Save error when creating dirs: %v", err)
		}
		if _, err := os.Stat(path); err != nil {
			t.Errorf("file not created: %v", err)
		}
	})
}
