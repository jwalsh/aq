# C-7: Heartbeat Options — Keeping Broadcasts Alive

## The Problem

TTL is 300s (5min). Agent sessions last 30-60min. Without re-announcement,
broadcasts expire after 5min and the gossip layer forgets the agent exists.
This was observed 5 times during dogfooding (DOGFOODING.md §4, §8).

"Manual gossip is an oxymoron." Every approach to C-7 must make
re-announcement automatic.

## Option A: Claude Code PostToolUse Hook (bead aq-14i)

### How it works
A Claude Code user hook fires after every Edit or Write tool call.
The hook runs `aq announce` with the files just touched.

### Setup

```json
// ~/.claude/hooks.json (or .claude/hooks.json in the project)
{
  "hooks": [
    {
      "event": "PostToolUse",
      "matcher": {
        "tool_name": "Edit|Write"
      },
      "command": "aq announce -c \"${AQ_CONJECTURE:-C-0}\" --claim \"editing\" --phase proof -f \"$(echo $CLAUDE_TOOL_PARAMS | jq -r '.file_path // .file_name' 2>/dev/null || echo 'unknown')\" --status prosecuting 2>/dev/null || true"
    }
  ]
}
```

### Alternatively, as a shell script hook:

```bash
#!/bin/sh
# .claude/hooks/post-tool-use.sh
# Fires after Edit/Write tool calls in Claude Code

# Only fire for file-modifying tools
case "$CLAUDE_TOOL_NAME" in
  Edit|Write) ;;
  *) exit 0 ;;
esac

# Extract file path from tool params
FILE=$(echo "$CLAUDE_TOOL_PARAMS" | jq -r '.file_path // .file_name // empty' 2>/dev/null)
[ -z "$FILE" ] && exit 0

# Make path relative
FILE=$(realpath --relative-to=. "$FILE" 2>/dev/null || echo "$FILE")

# Announce — advisory, never block
aq announce \
  -c "${AQ_CONJECTURE:-C-0}" \
  --claim "editing" \
  --phase proof \
  -f "$FILE" \
  --status prosecuting \
  2>/dev/null || true

exit 0
```

### Properties
- **Trigger**: Every file edit (natural work rhythm)
- **Daemon**: None
- **Dependencies**: Claude Code hooks, jq
- **Coverage**: High during active editing, zero during thinking/reading
- **Failure mode**: Hook fails silently, commit proceeds
- **Gossip axiom**: Preserved — advisory, no blocking

### Conjecture
C-7a: PostToolUse hooks provide sufficient re-announcement coverage.
Refutation: significant work periods (>TTL) with no Edit/Write calls
leave the agent invisible.

## Option B: aq watch — Filesystem Watcher (bead aq-bqr)

### How it works
A daemon process watches the working tree for file changes using
fswatch (macOS), inotify (Linux), or kqueue (FreeBSD). On change,
re-announces with the modified files.

### Usage (proposed)
```bash
aq watch -c C-1 --phase proof
# Background: runs until killed, re-announces on file changes
# Filters: only git-tracked files, debounce 2s
```

### Properties
- **Trigger**: Any file modification in the working tree
- **Daemon**: Yes — separate process
- **Dependencies**: fswatch/inotify/kqueue
- **Coverage**: Complete — any file change triggers re-announcement
- **Failure mode**: Daemon crashes = silent, back to TTL cliff
- **Gossip axiom**: Tension — a daemon is infrastructure, gossip should be lightweight

### Conjecture
C-7b: Filesystem watcher daemon provides reliable re-announcement.
Refutation: daemon adds coupling/complexity that exceeds the value,
or daemon crashes go undetected (gossip about gossip about gossip).

## Option C: Cron/Loop Heartbeat (bead aq-tit)

### How it works
Simple loop: re-announce every TTL/2 (150s) with the same conjecture
and files. Can be backgrounded or run via cron.

### Usage (proposed)
```bash
# Background heartbeat
aq heartbeat -c C-1 -f "main.go" &

# Or via cron (every 2 min)
*/2 * * * * aq announce -c C-1 -f "main.go" --phase proof 2>/dev/null
```

### Properties
- **Trigger**: Timer (fixed interval)
- **Daemon**: Minimal — backgrounded process or cron
- **Dependencies**: None
- **Coverage**: Fixed — broadcasts are always alive, regardless of work
- **Failure mode**: Announces stale state if agent has moved to different files
- **Gossip axiom**: Tension — broadcasting when nothing changed is noise

### Conjecture
C-7c: Fixed-interval heartbeat keeps broadcasts alive.
Refutation: heartbeat announces stale state (wrong files, wrong phase)
more often than current state, producing misleading gossip.

## Comparison

| Property | A: PostToolUse | B: aq watch | C: Heartbeat |
|----------|---------------|-------------|-------------|
| Daemon | None | Yes | Minimal |
| Deps | Claude Code | fswatch | None |
| Trigger | Agent edits | File changes | Timer |
| Coverage | Work-driven | Complete | Always-on |
| Staleness risk | Low | Low | High |
| Complexity | Low | High | Low |
| Gossip axiom | Clean | Tension | Tension |
| Agent-agnostic | No (Claude only) | Yes | Yes |

## Option D: Shell Hook (cd wrapper)

### How it works
A shell function wrapping `cd` that announces when you enter a git
repo with `.aq/` initialized. Presence triggered by *navigation*,
not editing.

```bash
# ~/.zshrc or ~/.bashrc
aq_cd() {
  builtin cd "$@" || return
  [ -d .aq ] || return
  command -v aq >/dev/null 2>&1 || return
  BRANCH=$(git rev-parse --abbrev-ref HEAD 2>/dev/null || echo "unknown")
  CONJECTURE=$(echo "$BRANCH" | grep -oE 'C-[0-9]+' || echo "C-0")
  aq announce -c "$CONJECTURE" --claim "entered worktree" \
    --phase conjecture --status prosecuting 2>/dev/null &
}
alias cd=aq_cd
```

### Properties
- **Trigger**: Directory change
- **Daemon**: None
- **Coverage**: One-shot per cd — no re-announcement during work
- **Best for**: Human developers navigating between worktrees
- **Gossip axiom**: Clean — presence on entry, nothing more

## Option E: Git Hooks (already implemented)

Pre-commit and post-commit hooks auto-announce on every commit.
See `.githooks/pre-commit` and `.githooks/post-commit`. Installed
by `aq init`.

Coverage: commit-time only. The gap is the 20 minutes *between*
commits. Combined with Option A (PostToolUse), covers the full
edit-commit lifecycle.

## Option F: Claude Code Read Hook

### How it works
A `PostToolUse` hook on `Read` calls — announces which files the
agent is *reading*, not just editing. This is the lightest possible
signal: "I'm looking at this file" is presence at the attention
level, not the modification level.

```json
{
  "hooks": [
    {
      "event": "PostToolUse",
      "matcher": {
        "tool_name": "Read"
      },
      "command": "aq whisper -c \"${AQ_CONJECTURE:-C-0}\" --claim \"reading\" -f \"$(echo $CLAUDE_TOOL_PARAMS | jq -r '.file_path' 2>/dev/null | xargs realpath --relative-to=. 2>/dev/null)\" 2>/dev/null || true"
    }
  ]
}
```

Note: uses `aq whisper` (TTL 60s) not `aq announce` (TTL 300s).
Reading is lower-signal than editing — it should expire faster.

### Properties
- **Trigger**: Every file read
- **Daemon**: None
- **Coverage**: Very high — agents read before they edit
- **Staleness risk**: Low — reads are current attention
- **Noise risk**: High — agents read many files they don't modify
- **Gossip axiom**: Clean — whisper semantics match read intent

### The layered approach
Reads = whisper (TTL 60s, low priority).
Edits = announce (TTL 300s, via PostToolUse hook).
Commits = announce (TTL 300s, via git hooks).

Three layers, increasing commitment. A whisper says "I'm looking."
An edit-announce says "I'm changing." A commit-announce says "I changed."
Each layer is independently useful. Combined, they cover the full
attention-edit-commit lifecycle without a daemon.

## Updated Comparison

| Property | A: PostToolUse | B: watch | C: Heartbeat | D: cd | E: git hooks | F: Read hook |
|----------|---------------|----------|-------------|-------|-------------|-------------|
| Trigger | Edit/Write | File Δ | Timer | cd | Commit | Read |
| TTL | 300s | 300s | 300s | 300s | 300s | 60s (whisper) |
| Daemon | None | Yes | Minimal | None | None | None |
| Coverage | Edit-time | Complete | Always | Entry | Commit-time | Read-time |
| Signal | "I'm changing" | "files changed" | "I'm here" | "I arrived" | "I committed" | "I'm looking" |

## Recommendation

Layer the hooks: **E** (git hooks, already done) + **A** (PostToolUse edits)
+ **F** (Read whispers). Three layers, zero daemons, full lifecycle:

- Read a file → whisper (60s TTL)
- Edit a file → announce (300s TTL)
- Commit → announce done (300s TTL)

Fall back to **C** (heartbeat) for non-Claude agents. Build **B** (watch)
only if the hook-based approach proves insufficient.

## Test Plan

For each option, measure:
1. **Coverage**: % of work time with active broadcast (target: >90%)
2. **Staleness**: % of broadcasts that reference wrong files (target: <10%)
3. **Overhead**: ms added to each edit/commit (target: <100ms)
4. **Reliability**: % of sessions where presence was maintained (target: >95%)

Run a 30-minute coding session with each option. Compare to the
baseline (no heartbeat, git hooks only) from DOGFOODING.md §4
where coverage was ~16%.
