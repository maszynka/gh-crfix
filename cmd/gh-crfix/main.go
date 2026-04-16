package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/maszynka/gh-crfix/internal/ai"
	"github.com/maszynka/gh-crfix/internal/config"
	"github.com/maszynka/gh-crfix/internal/input"
	"github.com/maszynka/gh-crfix/internal/model"
	"github.com/maszynka/gh-crfix/internal/workflow"
)

const version = "0.1.0-go"

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
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

	prSpec, flags := splitArgsAndFlags(args)

	for _, f := range flags {
		if f == "--version" || f == "-v" {
			fmt.Printf("gh-crfix %s (Go port)\n", version)
			return 0
		}
	}

	cfgPath := defaultConfigPath()
	cfg, err := config.Load(cfgPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: could not load config: %v\n", err)
		cfg = config.Defaults()
	}

	applyFlags(flags, &cfg)

	_ = model.Family(cfg.GateModel)
	_ = model.Family(cfg.FixModel)

	// Parse PR spec.
	var ownerRepo string
	var prNums []int

	if strings.Contains(prSpec, "github.com/") {
		ownerRepo, prNums, err = input.ParseURL(prSpec)
	} else {
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

	// Build base options from config.
	baseOpts := workflow.OptionsFromConfig(cfg, ownerRepo, 0)
	baseOpts.RepoRoot = os.Getenv("GH_CRFIX_DIR")
	applyWorkflowFlags(flags, &baseOpts)

	// Detect backend if auto.
	if baseOpts.AIBackend == ai.BackendAuto {
		baseOpts.AIBackend = ai.Detect()
		switch baseOpts.AIBackend {
		case ai.BackendClaude:
			fmt.Println("  backend auto-detected: claude")
		case ai.BackendCodex:
			fmt.Println("  backend auto-detected: codex")
		default:
			fmt.Fprintln(os.Stderr, "warning: no AI backend found (install claude or codex)")
		}
	}
	fmt.Println()

	results := workflow.ProcessBatch(workflow.BatchOptions{
		PRNums:      prNums,
		Concurrency: cfg.Concurrency,
		Base:        baseOpts,
		Out:         os.Stdout,
	})

	workflow.PrintResults(os.Stdout, results)

	exit := 0
	for _, r := range results {
		if r.Status == "failed" {
			exit = 1
		}
	}
	return exit
}

// splitArgsAndFlags separates the first positional argument from flags.
func splitArgsAndFlags(args []string) (prSpec string, flags []string) {
	for i := 0; i < len(args); i++ {
		a := args[i]
		if strings.HasPrefix(a, "-") {
			switch a {
			case "-c", "--concurrency", "--ai-backend",
				"--gate-model", "--fix-model",
				"--score-needs-llm", "--score-pr-comment", "--score-test-failure",
				"--max-threads", "--autofix-hook", "--validate-hook",
				"--review-wait":
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

// applyWorkflowFlags overlays CLI flags on workflow options.
func applyWorkflowFlags(flags []string, opts *workflow.Options) {
	for i := 0; i < len(flags); i++ {
		switch flags[i] {
		case "--ai-backend":
			if i+1 < len(flags) {
				i++
				opts.AIBackend = ai.ParseBackend(flags[i])
			}
		case "--gate-model":
			if i+1 < len(flags) {
				i++
				opts.GateModel = flags[i]
			}
		case "--fix-model":
			if i+1 < len(flags) {
				i++
				opts.FixModel = flags[i]
			}
		case "--max-threads":
			if i+1 < len(flags) {
				i++
				var n int
				fmt.Sscanf(flags[i], "%d", &n)
				if n > 0 {
					opts.MaxThreads = n
				}
			}
		case "--autofix-hook":
			if i+1 < len(flags) {
				i++
				opts.AutofixHook = flags[i]
			}
		case "--validate-hook":
			if i+1 < len(flags) {
				i++
				opts.ValidateHook = flags[i]
			}
		case "--review-wait":
			if i+1 < len(flags) {
				i++
				var n int
				fmt.Sscanf(flags[i], "%d", &n)
				if n >= 0 {
					opts.ReviewWaitSecs = n
				}
			}
		case "--dry-run":
			opts.DryRun = true
		case "--no-resolve":
			opts.NoResolve = true
		case "--resolve-skipped":
			opts.ResolveSkipped = true
		case "--no-post-fix":
			opts.NoPostFix = true
		case "--no-autofix":
			opts.NoAutofix = true
		case "--setup-only":
			opts.SetupOnly = true
		case "--exclude-outdated":
			opts.IncludeOutdated = false
		case "--include-outdated":
			opts.IncludeOutdated = true
		case "--verbose":
			opts.Verbose = true
		}
	}
}

func currentRepo() (string, error) {
	out, err := exec.Command("gh", "repo", "view", "--json", "nameWithOwner", "-q", ".nameWithOwner").Output()
	if err != nil {
		return "", fmt.Errorf("gh repo view: %w", err)
	}
	return strings.TrimSpace(string(out)), nil
}

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
  --max-threads N          max threads fetched per PR (default: 100)
  --validate-hook PATH     repo-local validation script
  --autofix-hook PATH      repo-local autofix script
  --no-autofix             skip autofix hook
  --dry-run                no GitHub writes, no AI run
  --exclude-outdated       skip outdated threads
  --include-outdated       include outdated threads (default)
  --resolve-skipped        resolve skipped threads too
  --no-resolve             do not reply or resolve
  --no-post-fix            skip post-fix review cycle
  --review-wait SECS       post-fix wait before re-check (default: 180)
  --setup-only             set up worktrees and exit
  --score-needs-llm N      gate score weight [0,1]
  --score-pr-comment N     gate score weight [0,1]
  --score-test-failure N   gate score weight [0,1]
  --verbose                verbose output
  --version, -v            show version
  --help, -h               show this help

Env:
  GH_CRFIX_DIR             local repo path (defaults to current git root)

Config: %s
`, version, defaultConfigPath())
}
