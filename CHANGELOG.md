# gh crfix — Changelog

## 1.4.0 — 2026-03-30

### Added
- **Model registry**: public JSON endpoint at `registry/models.json` serving available model names for both Anthropic and OpenAI — no API keys needed on the client side
- `registry/update.sh` script to refresh the model list from live APIs
- GitHub Actions workflow (`update-models.yml`) that auto-updates the registry every hour
- `fetch_model_registry`, `get_claude_models`, `get_openai_models` helpers in the main script with local caching (~/.cache/gh-crfix/models.json, 1h TTL)
- `GH_CRFIX_MODEL_REGISTRY` env var to override the registry URL
- Tests for registry functions (`test/model_registry.bats`)

## 1.3.0 — 2026-03-27

### Fixed
- **Worktree reuse**: existing worktrees from previous runs could have dirty state (pending merge, uncommitted files). `checkout`/`pull --rebase` failed silently (`2>/dev/null || true`), leaving a stale worktree. Now: abort any pending merge/rebase, clean untracked files, `reset --hard origin/$branch` to guarantee a fresh state.
- **Base branch fetch**: `git fetch origin master` relied on default refspec to update `origin/master`; silenced ALL errors. Now uses explicit refspec (`+refs/heads/$base:refs/remotes/origin/$base`) and logs fetch failures instead of hiding them.

## 1.2.0 — 2026-03-27

### Fixed
- **Critical:** `run_gate_model` used `--output-format json` which wraps the response in a `{"type":"result","result":"..."}` envelope; `jq .needs_advanced_model` returned null on the wrapper → always evaluated as gate=NO → advanced model never ran. Fixed by switching to `--output-format text` so the output is directly parseable JSON.
- Gate prompt bias flipped: was "conservative — don't request advanced model unless necessary" (biases toward false/skip); now "default true unless all threads are clearly non-actionable" — prevents silently skipping real bugs.
- Default gate model changed from `haiku` to `sonnet` for better reasoning on complex/security threads. Can be overridden with `--gate-model haiku` or `GH_CRFIX_GATE_MODEL=haiku`.

## 1.1.0 — 2026-03-27

### Added
- `gate_skipped` responses: when the Haiku gate says "no advanced model needed", `needs_llm` threads now get an `already_fixed` response and are resolved instead of silently dropped
- Merge-failure PR comment: `post_fix_review_cycle` now posts a ⚠️ comment on the PR when `merge_base_branch` fails (instead of silently ignoring)
- Auto-resolve `.github/.auto-fix-iterations` and `thread-responses.json` merge conflicts (checkout `--ours`)
- Cleanup step: after replies are posted, `thread-responses.json` is removed from git in the worktree to prevent future merge conflicts

## 1.0.0 — 2026-03-26

### Added
- Initial open-source release extracted from private monorepo
- Hybrid PR review fixer: deterministic triage + Haiku gate + Sonnet fix
- TUI dashboard for parallel mode with live progress
- Conflict resolution: deterministic rules + LLM fallback
- Post-fix review cycle (wait for re-review, merge base)
- Repo-local autofix hook discovery (`.gh-crfix/autofix.sh`, `scripts/gh-crfix-autofix.sh`)
- `--dry-run`, `--resolve-skipped`, `--include-outdated` flags
- FIFO semaphore for controlled concurrency
- macOS notification on completion
- bats test suite
- `install.sh` / `uninstall.sh` for gh extension setup
