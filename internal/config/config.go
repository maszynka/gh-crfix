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
		// skip blank lines and comments
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		idx := strings.IndexByte(line, '=')
		if idx < 0 {
			continue
		}
		key := line[:idx]
		value := line[idx+1:]
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
			if err == nil {
				c.ScoreNeedsLLM = v
			}
		case "SCORE_PR_COMMENT":
			v, err := strconv.ParseFloat(value, 64)
			if err == nil {
				c.ScorePRComment = v
			}
		case "SCORE_TEST_FAILURE":
			v, err := strconv.ParseFloat(value, 64)
			if err == nil {
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
	tmp := path + ".tmp"
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}
	// Clean up the tmp file if anything goes wrong after this point.
	ok := false
	defer func() {
		if !ok {
			os.Remove(tmp) //nolint:errcheck
		}
	}()

	fmt.Fprintf(f, "# gh-crfix persisted defaults\n")
	fmt.Fprintf(f, "AI_BACKEND=%s\n", c.AIBackend)
	fmt.Fprintf(f, "GATE_MODEL=%s\n", c.GateModel)
	fmt.Fprintf(f, "FIX_MODEL=%s\n", c.FixModel)
	fmt.Fprintf(f, "CONCURRENCY=%d\n", c.Concurrency)
	fmt.Fprintf(f, "SCORE_NEEDS_LLM=%.3f\n", c.ScoreNeedsLLM)
	fmt.Fprintf(f, "SCORE_PR_COMMENT=%.3f\n", c.ScorePRComment)
	fmt.Fprintf(f, "SCORE_TEST_FAILURE=%.3f\n", c.ScoreTestFailure)
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		return err
	}
	ok = true
	return nil
}
