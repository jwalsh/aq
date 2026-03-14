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

<!-- BEGIN BEADS INTEGRATION -->
## Issue Tracking with bd (beads)

**IMPORTANT**: This project uses **bd (beads)** for ALL issue tracking. Do NOT use markdown TODOs, task lists, or other tracking methods.

### Why bd?

- Dependency-aware: Track blockers and relationships between issues
- Git-friendly: Dolt-powered version control with native sync
- Agent-optimized: JSON output, ready work detection, discovered-from links
- Prevents duplicate tracking systems and confusion

### Quick Start

**Check for ready work:**

```bash
bd ready --json
```

**Create new issues:**

```bash
bd create "Issue title" --description="Detailed context" -t bug|feature|task -p 0-4 --json
bd create "Issue title" --description="What this issue is about" -p 1 --deps discovered-from:bd-123 --json
```

**Claim and update:**

```bash
bd update <id> --claim --json
bd update bd-42 --priority 1 --json
```

**Complete work:**

```bash
bd close bd-42 --reason "Completed" --json
```

### Issue Types

- `bug` - Something broken
- `feature` - New functionality
- `task` - Work item (tests, docs, refactoring)
- `epic` - Large feature with subtasks
- `chore` - Maintenance (dependencies, tooling)

### Priorities

- `0` - Critical (security, data loss, broken builds)
- `1` - High (major features, important bugs)
- `2` - Medium (default, nice-to-have)
- `3` - Low (polish, optimization)
- `4` - Backlog (future ideas)

### Workflow for AI Agents

1. **Check ready work**: `bd ready` shows unblocked issues
2. **Claim your task atomically**: `bd update <id> --claim`
3. **Work on it**: Implement, test, document
4. **Discover new work?** Create linked issue:
   - `bd create "Found bug" --description="Details about what was found" -p 1 --deps discovered-from:<parent-id>`
5. **Complete**: `bd close <id> --reason "Done"`

### Auto-Sync

bd automatically syncs via Dolt:

- Each write auto-commits to Dolt history
- Use `bd dolt push`/`bd dolt pull` for remote sync
- No manual export/import needed!

### Important Rules

- ✅ Use bd for ALL task tracking
- ✅ Always use `--json` flag for programmatic use
- ✅ Link discovered work with `discovered-from` dependencies
- ✅ Check `bd ready` before asking "what should I work on?"
- ❌ Do NOT create markdown TODO lists
- ❌ Do NOT use external issue trackers
- ❌ Do NOT duplicate tracking systems

For more details, see README.md and docs/QUICKSTART.md.

## Landing the Plane (Session Completion)

**When ending a work session**, you MUST complete ALL steps below. Work is NOT complete until `git push` succeeds.

**MANDATORY WORKFLOW:**

1. **File issues for remaining work** - Create issues for anything that needs follow-up
2. **Run quality gates** (if code changed) - Tests, linters, builds
3. **Update issue status** - Close finished work, update in-progress items
4. **PUSH TO REMOTE** - This is MANDATORY:
   ```bash
   git pull --rebase
   bd sync
   git push
   git status  # MUST show "up to date with origin"
   ```
5. **Clean up** - Clear stashes, prune remote branches
6. **Verify** - All changes committed AND pushed
7. **Hand off** - Provide context for next session

**CRITICAL RULES:**
- Work is NOT complete until `git push` succeeds
- NEVER stop before pushing - that leaves work stranded locally
- NEVER say "ready to push when you are" - YOU must push
- If push fails, resolve and retry until it succeeds

<!-- END BEADS INTEGRATION -->
