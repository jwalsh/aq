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

## Recommendation

Start with **A** (PostToolUse) for Claude Code agents — it's the lightest
weight and fires on actual work. Fall back to **C** (heartbeat) for
non-Claude agents or when A's coverage gaps matter. Build **B** (watch)
only if A and C prove insufficient.

The git hooks (already implemented) handle commit-time presence. PostToolUse
handles edit-time presence. Together they cover the full work lifecycle
without a daemon.

## Test Plan

For each option, measure:
1. **Coverage**: % of work time with active broadcast (target: >90%)
2. **Staleness**: % of broadcasts that reference wrong files (target: <10%)
3. **Overhead**: ms added to each edit/commit (target: <100ms)
4. **Reliability**: % of sessions where presence was maintained (target: >95%)

Run a 30-minute coding session with each option. Compare to the
baseline (no heartbeat, git hooks only) from DOGFOODING.md §4
where coverage was ~16%.
