package main

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/maszynka/gh-crfix/internal/config"
)

// runSetupWizard interactively prompts for the worktree mode and persists it
// to cfgPath. The reader/writer pair lets tests drive it without a real TTY.
// Returns the resolved config so the caller can use it for the rest of the
// process (or pass to config.Save itself, which the wizard already does).
func runSetupWizard(in io.Reader, out io.Writer, cfg config.Config, cfgPath string) (config.Config, error) {
	br := bufio.NewReader(in)

	fmt.Fprintln(out, "gh-crfix setup")
	fmt.Fprintln(out)
	fmt.Fprintln(out, "Worktree mode controls how gh-crfix handles per-PR worktrees")
	fmt.Fprintln(out, "(.gh-crfix/worktrees/pr-<N>) when running on the same PR more than once.")
	fmt.Fprintln(out)
	fmt.Fprintln(out, "  temp   — remove the worktree after each run (default)")
	fmt.Fprintln(out, "           Pros: nothing accumulates on disk.")
	fmt.Fprintln(out, "           Cons: ~2-5s/PR fetch+add overhead on each run.")
	fmt.Fprintln(out, "  reuse  — keep the worktree across runs as-is")
	fmt.Fprintln(out, "           Pros: zero-overhead reruns; your in-progress edits persist.")
	fmt.Fprintln(out, "           Cons: gh-crfix refuses to run if the worktree is dirty.")
	fmt.Fprintln(out, "  stash  — git stash uncommitted changes before reset; pop after run")
	fmt.Fprintln(out, "           Pros: rerun safely with dirty trees; nothing is lost.")
	fmt.Fprintln(out, "           Cons: stash conflicts on pop need manual cleanup.")
	fmt.Fprintln(out)

	cur := cfg.WorktreeMode
	if cur == "" {
		cur = "temp"
	}
	fmt.Fprintf(out, "Worktree mode [%s]: ", cur)
	line, err := br.ReadString('\n')
	if err != nil && err != io.EOF {
		return cfg, fmt.Errorf("read input: %w", err)
	}
	choice := strings.TrimSpace(line)
	if choice == "" {
		choice = cur
	}
	switch choice {
	case "temp", "reuse", "stash":
		cfg.WorktreeMode = choice
	default:
		fmt.Fprintf(out, "  warning: unknown mode %q; using %q\n", choice, cur)
		cfg.WorktreeMode = cur
	}

	if err := config.Save(cfgPath, cfg); err != nil {
		return cfg, fmt.Errorf("save config: %w", err)
	}
	fmt.Fprintf(out, "  saved: WORKTREE_MODE=%s -> %s\n", cfg.WorktreeMode, cfgPath)
	fmt.Fprintln(out)
	return cfg, nil
}

// firstRunNeeded reports whether the config file is missing — meaning this
// is a brand-new install and the wizard should auto-trigger on a TTY.
func firstRunNeeded(cfgPath string) bool {
	_, err := os.Stat(cfgPath)
	return os.IsNotExist(err)
}
