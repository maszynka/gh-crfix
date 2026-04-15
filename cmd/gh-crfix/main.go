package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/maszynka/gh-crfix/internal/config"
	"github.com/maszynka/gh-crfix/internal/input"
	"github.com/maszynka/gh-crfix/internal/model"
)

const version = "0.1.0-go"

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	// No args → show usage
	if len(args) == 0 {
		usage()
		return 0
	}

	switch args[0] {
	case "--version", "-v":
		fmt.Printf("gh-crfix %s (Go port)\n", version)
		return 0
	case "--help", "-h", "help":
		usage()
		return 0
	}

	// Strip known flags to find the positional PR spec.
	prSpec, flags := splitArgsAndFlags(args)

	// --version embedded anywhere
	for _, f := range flags {
		if f == "--version" || f == "-v" {
			fmt.Printf("gh-crfix %s (Go port)\n", version)
			return 0
		}
	}

	// Load persisted config.
	cfgPath := defaultConfigPath()
	cfg, err := config.Load(cfgPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: could not load config: %v\n", err)
		cfg = config.Defaults()
	}

	// Apply flag overrides to config.
	applyFlags(flags, &cfg)

	// Determine backend family for model validation.
	gateFamily := model.Family(cfg.GateModel)
	fixFamily := model.Family(cfg.FixModel)
	_ = gateFamily
	_ = fixFamily

	// Parse PR spec.
	var ownerRepo string
	var prNums []int

	if strings.Contains(prSpec, "github.com/") {
		ownerRepo, prNums, err = input.ParseURL(prSpec)
	} else {
		// Bare numbers: need the current repo. Ask the gh CLI.
		ownerRepo, err = currentRepo()
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: not in a GitHub repo and no URL given (%v)\n", err)
			return 1
		}
		prNums, err = input.ParseBare(prSpec)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}

	fmt.Printf("gh-crfix %s — Go port\n\n", version)
	fmt.Printf("  repo        : %s\n", ownerRepo)
	fmt.Printf("  PRs         : %v\n", prNums)
	fmt.Printf("  backend     : %s\n", cfg.AIBackend)
	fmt.Printf("  gate model  : %s\n", cfg.GateModel)
	fmt.Printf("  fix model   : %s\n", cfg.FixModel)
	fmt.Printf("  concurrency : %d\n", cfg.Concurrency)
	fmt.Printf("  scores      : needs_llm=%.3f  pr_comment=%.3f  test_failure=%.3f\n",
		cfg.ScoreNeedsLLM, cfg.ScorePRComment, cfg.ScoreTestFailure)
	fmt.Println()
	fmt.Println("  ⚠  Full PR workflow (worktree setup, triage run, gate, AI fix)")
	fmt.Println("     is not yet wired up in the Go port.")
	fmt.Println("     Implemented so far: input parsing, config, model registry,")
	fmt.Println("     triage classifier, gate scoring, KV store.")

	return 0
}

// splitArgsAndFlags separates the first positional argument (PR spec) from flags.
func splitArgsAndFlags(args []string) (prSpec string, flags []string) {
	for i := 0; i < len(args); i++ {
		a := args[i]
		if strings.HasPrefix(a, "-") {
			// Flags with values: consume the next token too.
			switch a {
			case "-c", "--concurrency", "--ai-backend",
				"--gate-model", "--fix-model",
				"--score-needs-llm", "--score-pr-comment", "--score-test-failure",
				"--max-threads", "--autofix-hook", "--validate-hook":
				flags = append(flags, a)
				if i+1 < len(args) {
					i++
					flags = append(flags, args[i])
				}
			default:
				flags = append(flags, a)
			}
		} else if prSpec == "" {
			prSpec = a
		}
	}
	return
}

// applyFlags overlays CLI flags on top of the loaded config.
func applyFlags(flags []string, cfg *config.Config) {
	for i := 0; i < len(flags); i++ {
		switch flags[i] {
		case "--ai-backend":
			if i+1 < len(flags) {
				i++
				cfg.AIBackend = flags[i]
			}
		case "--gate-model":
			if i+1 < len(flags) {
				i++
				cfg.GateModel = flags[i]
			}
		case "--fix-model":
			if i+1 < len(flags) {
				i++
				cfg.FixModel = flags[i]
			}
		case "-c", "--concurrency":
			if i+1 < len(flags) {
				i++
				var n int
				fmt.Sscanf(flags[i], "%d", &n)
				if n > 0 {
					cfg.Concurrency = n
				}
			}
		}
	}
}

// currentRepo calls `gh repo view` to find the owner/repo for the current directory.
func currentRepo() (string, error) {
	out, err := exec.Command("gh", "repo", "view", "--json", "nameWithOwner", "-q", ".nameWithOwner").Output()
	if err != nil {
		return "", fmt.Errorf("gh repo view: %w", err)
	}
	return strings.TrimSpace(string(out)), nil
}

// defaultConfigPath returns the path to the persisted defaults file.
func defaultConfigPath() string {
	base := os.Getenv("XDG_CONFIG_HOME")
	if base == "" {
		home, _ := os.UserHomeDir()
		base = filepath.Join(home, ".config")
	}
	return filepath.Join(base, "gh-crfix", "defaults")
}

func usage() {
	fmt.Printf(`gh-crfix %s (Go port)

Usage:
  gh crfix <url>           single PR URL
  gh crfix <url-range>     range   e.g. .../pull/93-95
  gh crfix <url-list>      list    e.g. .../pull/[93,94,95]
  gh crfix <number>        bare number — uses current repo
  gh crfix <n1>-<n2>       bare range
  gh crfix <n1>,<n2>,...   bare list

Flags:
  -c N, --concurrency N    parallel workers (default: 3)
  --ai-backend BACKEND     auto|claude|codex
  --gate-model MODEL       small gate model (default: sonnet)
  --fix-model  MODEL       advanced fix model (default: sonnet)
  --score-needs-llm N      gate score weight [0,1]
  --score-pr-comment N     gate score weight [0,1]
  --score-test-failure N   gate score weight [0,1]
  --version, -v            show version
  --help, -h               show this help

Config: %s
`, version, defaultConfigPath())
}
