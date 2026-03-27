# gh crfix — Changelog

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
