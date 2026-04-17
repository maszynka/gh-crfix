# gh-crfix

> https://github.com/maszynka/gh-crfix

Hybrid GitHub PR review fixer — automatically addresses unresolved review comments using deterministic triage + AI.

## How it works

```
Parse PR → Create worktree → Merge base branch → Fetch review threads
  → Deterministic triage (skip / auto / already-fixed / needs-LLM)
  → Deterministic validation/tests + score threshold
  → Small-model gate (only when score >= 1)
  → Advanced-model fix (only for residual hard cases)
  → Reply & resolve threads
  → Post fix summary → Request Copilot re-review
  → Post-fix cycle (wait → check new comments → merge base)
```

## Install

```bash
# Via gh extension install (recommended)
gh extension install maszynka/gh-crfix

# Or clone and symlink manually
git clone https://github.com/maszynka/gh-crfix && cd gh-crfix && bash install.sh
```

Then run:

```bash
gh crfix
```

## Usage

```bash
# Interactive launcher (TTY)
gh crfix

# Single PR (inside a git repo)
gh crfix 123

# PR range
gh crfix 123-126

# PR list
gh crfix [123,125,130]

# Full URL (works from anywhere)
gh crfix https://github.com/owner/repo/pull/123

# Parallel batch run (plain output by default)
gh crfix 100-110 -c 5

# Parallel with TUI dashboard
gh crfix 100-110 -c 5 --tui

# Dry run (no mutations)
gh crfix 123 --dry-run

# Force Codex backend
gh crfix 123 --ai-backend codex --gate-model gpt-5.4-mini --fix-model gpt-5.4

# Tune gate score threshold inputs
gh crfix 123 --score-needs-llm .2 --score-pr-comment .4 --score-test-failure 1
```

## Flags

| Flag | Default | Description |
|------|---------|-------------|
| `-c N, --concurrency N` | 3 | Max parallel PR workers |
| `--ai-backend BACKEND` | auto | AI backend: `auto`, `claude`, `codex` |
| `--seq` | | Sequential mode (same as `-c 1`) |
| `--tui` | | Enable the fullscreen TUI dashboard for batch runs |
| `--no-tui` | | Force plain output; this is the default for CLI batch runs |
| `--no-post-fix` | | Skip post-fix review cycle |
| `--setup-only` | | Only setup worktrees + triage |
| `--no-resolve` | | Do not resolve GitHub threads |
| `--exclude-outdated` | | Skip outdated unresolved threads (opt-out of the default behaviour) |
| `--include-outdated` | | _(deprecated, now the default)_ Include outdated threads; kept for backward compatibility |
| `--gate-model MODEL` | sonnet | Small model for gate decision |
| `--fix-model MODEL` | sonnet | Advanced model for fixing |
| `--validate-hook PATH` | | Deterministic validation script |
| `--no-validate` | | Skip validation hook and built-in test detection |
| `--score-needs-llm N` | 1 | Gate score contribution for residual semantic review |
| `--score-pr-comment N` | 0.4 | Gate score contribution for PR-level comments |
| `--score-test-failure N` | 1 | Gate score contribution for failed validation/tests |
| `--max-threads N` | 100 | Max threads fetched per PR |
| `--autofix-hook PATH` | | Repo-local deterministic autofix script |
| `--no-autofix` | | Skip autofix hook |
| `--resolve-skipped` | | Also resolve skipped threads |
| `--dry-run` | | No writes to GitHub, no AI fix run |

## Prerequisites

- [`gh`](https://cli.github.com/) (authenticated)
- `jq`
- One AI CLI:
  - [`claude`](https://docs.anthropic.com/en/docs/claude-code)
  - [`codex`](https://developers.openai.com/codex/cli/)
- `bash` 4+
- `bats` (for tests only)

By default `gh crfix` uses `--ai-backend auto`, which prefers `claude` if installed and otherwise falls back to `codex`.

## Launcher

Running plain `gh crfix` in a TTY opens a full-screen launcher where you can:

- enter a PR number, range, list, or full GitHub PR URL
- choose `auto`, `claude`, or `codex` with arrow keys
- choose gate and fix models with arrow keys
- set concurrency with arrow keys
- tune the gate score inputs with arrow keys

After you launch from this screen, `gh crfix` persists those defaults to:

```bash
${XDG_CONFIG_HOME:-~/.config}/gh-crfix/defaults
```

The next launcher run will preload them, and CLI runs without explicit flags will also use them unless overridden by env vars or flags.

The launcher keeps the target field as free text, but the other fields use allowed option lists. It starts with static fallback model names and then tries to refresh live model lists in the background when API credentials are available.

When you pass PR numbers or URLs directly on the CLI, `gh crfix` now stays in plain output mode by default. Use `--tui` if you want the live fullscreen dashboard for a batch run.

The gate score uses three weights:

- `needs_llm`
- `pr_comment`
- `test_failure`

Each accepts a value between `0` and `1`, including shorthand like `.2` or `.4`. The weights for the current PR are summed, and the gate model runs only when the total is at least `1`.

## Codex Usage

```bash
# Recommended Codex setup
gh crfix 123 \
  --ai-backend codex \
  --gate-model gpt-5.4-mini \
  --fix-model gpt-5.4

# Works with full URL too
gh crfix https://github.com/owner/repo/pull/123 \
  --ai-backend codex \
  --gate-model gpt-5.4-mini \
  --fix-model gpt-5.4
```

## Security note

When fixing code, `gh crfix` runs the selected AI CLI with full filesystem and shell access **within the worktree**. For `claude` this uses `--dangerously-skip-permissions`; for `codex` it uses `--dangerously-bypass-approvals-and-sandbox`. The model can read, write, commit, and push code autonomously. This is by design — the tool needs to edit files and push fixes without interactive approval.

Use `--dry-run` to preview what would happen without any mutations. Review the generated commits before merging.

## Repo autofix hook

`gh crfix` looks for a deterministic autofix script in the **target repo**:

1. `--autofix-hook PATH` flag
2. `.gh-crfix/autofix.sh` (executable)
3. `scripts/gh-crfix-autofix.sh` (executable)

The hook runs in the worktree before the AI gate/fix phase.

## Deterministic validation

`gh crfix` also looks for deterministic validation before running the gate model:

1. `--validate-hook PATH`
2. `.gh-crfix/validate.sh` (executable)
3. `scripts/gh-crfix-validate.sh` (executable)
4. built-in `package.json` test detection (`npm`, `pnpm`, `yarn`, `bun`)

Validation runs after the deterministic autofix phase and before the gate model. If validation fails, that contributes to the gate score via `--score-test-failure`.

## Environment variables

| Variable | Description |
|----------|-------------|
| `GH_CRFIX_DIR` | Force local repo path (auto-detected otherwise) |
| `GH_CRFIX_AI_BACKEND` | AI backend override: `auto`, `claude`, `codex` |
| `GH_CRFIX_REVIEW_WAIT` | Seconds to wait for re-review (default: 90) |
| `GH_CRFIX_GATE_MODEL` | Gate model override |
| `GH_CRFIX_FIX_MODEL` | Fix model override |
| `GH_CRFIX_MODEL_REGISTRY` | Override model registry URL |
| `GH_CRFIX_SCORE_NEEDS_LLM` | Gate score contribution for residual semantic review |
| `GH_CRFIX_SCORE_PR_COMMENT` | Gate score contribution for PR-level comments |
| `GH_CRFIX_SCORE_TEST_FAILURE` | Gate score contribution for failed validation/tests |

## Model registry

Available model names (for `--gate-model` / `--fix-model` and the launcher TUI) are loaded from a **public JSON endpoint** — no API keys needed on your machine.

```
https://raw.githubusercontent.com/maszynka/gh-crfix/main/registry/models.json
```

The list is **updated automatically every hour** via GitHub Actions, which fetches live model lists from Anthropic and OpenAI APIs. Results are cached locally for 1 hour at `~/.cache/gh-crfix/models.json`.

To update the registry manually:

```bash
ANTHROPIC_API_KEY=sk-... OPENAI_API_KEY=sk-... bash registry/update.sh
```

Override the endpoint:

```bash
export GH_CRFIX_MODEL_REGISTRY="https://your-domain.com/models.json"
```

## Go port

`cmd/gh-crfix` is the Go port of the bash CLI. It is **feature-complete against the bash implementation** except for the CI-hosted registry autoupdate (both ports still consume the live GitHub-hosted `registry/models.json`).

Build and run in place of bash:

```bash
go build -o bin/gh-crfix ./cmd/gh-crfix
./bin/gh-crfix https://github.com/owner/repo/pull/123
```

Flag parity is intentional — the same flags (including `-c`, `--dry-run`, `--no-resolve`, `--no-post-fix`, the gate score weights, etc.) work identically. Two Go-only additions:

- `--no-tui` — disable the Bubble Tea dashboard even on a TTY.
- `--no-notify` — suppress the best-effort completion notification (same behaviour as `GH_CRFIX_NO_NOTIFY=1`).

Model registry override behaviour is documented in [`docs/registry.md`](docs/registry.md) and driven by the same `GH_CRFIX_MODEL_REGISTRY` env var used by bash.

End-to-end tests for the Go port live in:

- `cmd/gh-crfix/main_e2e_test.go` — `go test -tags=e2e ./cmd/gh-crfix/...`
- `test/e2e/go-port.sh` — POSIX-sh mirror of the dry-run happy path, runs the built binary against stubbed `gh`/`claude`.
- `test/e2e/go-port-scenarios.sh` — additional POSIX-sh scenarios (CLOSED PR, zero threads, `--setup-only`, gate-skip below threshold).

## Tests

```bash
# Install test helpers first (bats-support, bats-assert)
bash test/install-test-helpers.sh

# Run all tests
bats test/

# Run v2-specific tests
bats test/v2/

# Go unit tests
go test ./...

# Go end-to-end test (builds binary + stubs gh/claude on PATH)
go test -tags=e2e ./cmd/gh-crfix/...

# POSIX-sh end-to-end for the Go port
sh test/e2e/go-port.sh
```

## File structure

```
gh-crfix/
├── gh-crfix              # Main script (gh extension binary)
├── install.sh            # Symlink as gh extension
├── uninstall.sh          # Remove symlink
├── registry/
│   ├── models.json       # Public model list (auto-updated hourly)
│   └── update.sh         # Script to refresh models from APIs
├── .github/workflows/
│   ├── test.yml
│   ├── e2e.yml
│   └── update-models.yml # Hourly model list update
├── test/
│   ├── install-test-helpers.sh
│   ├── test_helper/
│   │   └── common.bash
│   ├── *.bats
│   └── v2/
│       └── *.bats
├── LICENSE
├── CHANGELOG.md
└── README.md
```

## License

MIT
