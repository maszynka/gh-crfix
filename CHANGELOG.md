# gh crfix — Changelog

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
