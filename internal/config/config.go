// Package config manages persisted user defaults (~/.config/gh-crfix/defaults).
package config

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Config holds all configurable defaults for gh-crfix.
type Config struct {
	AIBackend        string
	GateModel        string
	FixModel         string
	Concurrency      int
	ScoreNeedsLLM    float64
	ScorePRComment   float64
	ScoreTestFailure float64
}

// Defaults returns a Config populated with built-in default values.
func Defaults() Config {
	return Config{
		AIBackend:        "auto",
		GateModel:        "sonnet",
		FixModel:         "sonnet",
		Concurrency:      3,
		ScoreNeedsLLM:    1.0,
		ScorePRComment:   0.4,
		ScoreTestFailure: 1.0,
	}
}

// isValidScore reports whether v is in the range [0, 1].
func isValidScore(v float64) bool { return v >= 0 && v <= 1 }

// Load reads a config file at path. Unknown keys are ignored. If the file does
// not exist or a key is missing, the default value is used.
func Load(path string) (Config, error) {
	c := Defaults()

	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return c, nil
		}
		return c, err
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Text()
		// skip blank lines and full-line comments
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		idx := strings.IndexByte(line, '=')
		if idx < 0 {
			continue
		}
		key := strings.TrimSpace(line[:idx])
		value := strings.TrimSpace(line[idx+1:])
		// strip inline comments
		if ci := strings.IndexByte(value, '#'); ci >= 0 {
			value = strings.TrimSpace(value[:ci])
		}
		switch key {
		case "AI_BACKEND":
			switch value {
			case "auto", "claude", "codex":
				c.AIBackend = value
			}
		case "GATE_MODEL":
			if value != "" {
				c.GateModel = value
			}
		case "FIX_MODEL":
			if value != "" {
				c.FixModel = value
			}
		case "CONCURRENCY":
			n, err := strconv.Atoi(value)
			if err == nil && n > 0 {
				c.Concurrency = n
			}
		case "SCORE_NEEDS_LLM":
			v, err := strconv.ParseFloat(value, 64)
			if err == nil && isValidScore(v) {
				c.ScoreNeedsLLM = v
			}
		case "SCORE_PR_COMMENT":
			v, err := strconv.ParseFloat(value, 64)
			if err == nil && isValidScore(v) {
				c.ScorePRComment = v
			}
		case "SCORE_TEST_FAILURE":
			v, err := strconv.ParseFloat(value, 64)
			if err == nil && isValidScore(v) {
				c.ScoreTestFailure = v
			}
		// unknown keys: silently ignore
		}
	}
	return c, scanner.Err()
}

// Save writes c to path atomically, creating parent directories as needed.
func Save(path string, c Config) error {
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	// Use a unique temp file to avoid collisions with concurrent saves.
	f, err := os.CreateTemp(filepath.Dir(path), ".gh-crfix-defaults-*.tmp")
	if err != nil {
		return err
	}
	tmp := f.Name()
	// Clean up the temp file if anything goes wrong after this point.
	ok := false
	defer func() {
		if !ok {
			os.Remove(tmp) //nolint:errcheck
		}
	}()

	// Track write errors via a closure so each fmt.Fprintf is checked.
	var writeErr error
	write := func(format string, a ...interface{}) {
		if writeErr != nil {
			return
		}
		_, writeErr = fmt.Fprintf(f, format, a...)
	}
	write("# gh-crfix persisted defaults\n")
	write("AI_BACKEND=%s\n", c.AIBackend)
	write("GATE_MODEL=%s\n", c.GateModel)
	write("FIX_MODEL=%s\n", c.FixModel)
	write("CONCURRENCY=%d\n", c.Concurrency)
	write("SCORE_NEEDS_LLM=%.3f\n", c.ScoreNeedsLLM)
	write("SCORE_PR_COMMENT=%.3f\n", c.ScorePRComment)
	write("SCORE_TEST_FAILURE=%.3f\n", c.ScoreTestFailure)
	if writeErr != nil {
		f.Close()
		return writeErr
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		return err
	}
	ok = true
	return nil
}
