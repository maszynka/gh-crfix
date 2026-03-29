# gh-crfix

> https://github.com/maszynka/gh-crfix

Hybrid GitHub PR review fixer — automatically addresses unresolved review comments using deterministic triage + AI.

## How it works

```
Parse PR → Create worktree → Merge base branch → Fetch review threads
  → Deterministic triage (skip / auto / already-fixed / needs-LLM)
  → Small-model gate (is advanced model needed?)
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
gh crfix 123
```

## Usage

```bash
# Single PR (inside a git repo)
gh crfix 123

# PR range
gh crfix 123-126

# PR list
gh crfix [123,125,130]

# Full URL (works from anywhere)
gh crfix https://github.com/owner/repo/pull/123

# Parallel with TUI dashboard
gh crfix 100-110 -c 5

# Dry run (no mutations)
gh crfix 123 --dry-run

# Force Codex backend
gh crfix 123 --ai-backend codex --gate-model gpt-5.4-mini --fix-model gpt-5.4
```

## Flags

| Flag | Default | Description |
|------|---------|-------------|
| `-c N, --concurrency N` | 3 | Max parallel PR workers |
| `--ai-backend BACKEND` | auto | AI backend: `auto`, `claude`, `codex` |
| `--seq` | | Sequential mode (same as `-c 1`) |
| `--no-tui` | | Disable TUI dashboard |
| `--no-post-fix` | | Skip post-fix review cycle |
| `--setup-only` | | Only setup worktrees + triage |
| `--no-resolve` | | Do not resolve GitHub threads |
| `--include-outdated` | | Include outdated unresolved threads |
| `--gate-model MODEL` | sonnet | Small model for gate decision |
| `--fix-model MODEL` | sonnet | Advanced model for fixing |
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

## Environment variables

| Variable | Description |
|----------|-------------|
| `GH_CRFIX_DIR` | Force local repo path (auto-detected otherwise) |
| `GH_CRFIX_AI_BACKEND` | AI backend override: `auto`, `claude`, `codex` |
| `GH_CRFIX_REVIEW_WAIT` | Seconds to wait for re-review (default: 90) |
| `GH_CRFIX_GATE_MODEL` | Gate model override |
| `GH_CRFIX_FIX_MODEL` | Fix model override |

## Tests

```bash
# Install test helpers first (bats-support, bats-assert)
bash test/install-test-helpers.sh

# Run all tests
bats test/

# Run v2-specific tests
bats test/v2/
```

## File structure

```
gh-crfix/
├── gh-crfix              # Main script (gh extension binary)
├── install.sh            # Symlink as gh extension
├── uninstall.sh          # Remove symlink
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
