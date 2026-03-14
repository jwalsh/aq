# Agent Instructions

This project uses **bd** (beads) for issue tracking. Run `bd onboard` to get started.

## What This Is

`aq` is a gossip layer (L1.5) for multi-agent development. Agents broadcast
presence via filesystem-backed channels so peers detect semantic conflicts
before they become merge conflicts. Not an orchestrator, not a broker — just gossip.

## Current State

- Python prototype exists in `src/aq/` (working, wire format is canonical)
- Go port planned in `main.go` (bead `aq-os0`, P1)
- 6 conjectures registered in `cprr`
- 7+ beads in `bd` with dependency chain

## Priority Work

```bash
bd ready              # See what's unblocked
cprr list             # See open conjectures
```

The Go port (`aq-os0`) is the primary deliverable. Pattern reference:
- `../sb/main.go` — single-file Go CLI, stdlib only, manual dispatch
- `../cprr/main.go` — same pattern, with JSON persistence

## Build & Test

```bash
# Go (target state)
make build            # Build aq binary
make test             # Run tests
make install          # Install to ~/.local/bin

# Python (prototype, for reference)
PYTHONPATH=src python -c "from aq.protocol import Broadcast"
```

## Quick Reference

```bash
bd ready              # Find available work
bd show <id>          # View issue details
bd update <id> --status in_progress  # Claim work
bd close <id>         # Complete work
bd sync               # Sync with git
```

## Security Practices

- **No secrets in repo.** `.gitignore` covers `.env`, `*.pem`, `*.key`,
  `credentials.json`, `secrets.json`, `.secrets/`, `.tokens/`
- **No API keys needed.** `aq` is filesystem-only, zero network dependencies
- **AQ_HOME env var** controls runtime data location (default `~/.aq/`)
- If secrets are ever needed (e.g., future network transport), use env vars
  or `~/.config/aq/` outside the repo. Never commit secrets.
- **Review before push.** Run `git diff --cached` before committing.
  If a file looks like it contains credentials, stop.

## Commit Conventions

- Conventional commits (`feat:`, `fix:`, `docs:`, `refactor:`, `test:`)
- Stacked diff style — small, focused commits
- Git notes on every commit with agent metadata:
  ```
  X-Agent-Role: builder | reviewer | ...
  X-Agent-Runner: Claude Code <version>
  X-Agent-Model: <model>
  X-Deviations: <any deviations from plan>
  X-Testing: <what was tested>
  X-Notes: <rebuild context>
  ```
- Co-author trailer: `Co-Authored-By: <name> <email>`

## Landing the Plane (Session Completion)

**When ending a work session**, complete ALL steps. Work is NOT complete until `git push` succeeds.

1. **File issues for remaining work** — `bd create "..." -t feature`
2. **Run quality gates** — `make test && make lint` (if code changed)
3. **Update issue status** — `bd close <id>` for finished work
4. **Push to remote** — MANDATORY:
   ```bash
   git pull --rebase
   bd sync
   git push
   git push origin refs/notes/commits
   git status  # MUST show "up to date with origin"
   ```
5. **Verify** — all changes committed AND pushed, notes pushed

**CRITICAL:** Work is NOT complete until `git push` succeeds. NEVER stop before pushing.
