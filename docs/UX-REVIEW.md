# aq UX Review

Agent: Gamma (feat/aq-gamma)
Date: 2026-03-13
Scope: Operational commands (init, doctor, quickstart) + overall CLI UX

## 1. Command Naming

`announce`, `whisper`, `check`, `status` are good verbs. They map to the
gossip metaphor well: you announce loudly (300s TTL), whisper quietly (60s
TTL), check if anyone is talking about the same thing, and ask for status.

One friction point: `announce` implies a one-time action, but the intended
lifecycle is repeated re-announcement every TTL/2. The verb doesn't suggest
ongoing behavior. Consider whether `heartbeat` or `pulse` should exist as
a daemon-mode alias that auto-re-announces.

The aliases (`ann`, `a`, `ls`, `prime`) are helpful for interactive use.
For agent/script consumption, the full names are better.

## 2. Flag Ergonomics

`-c C-1 -f "main.go"` is acceptable but has friction:

- **Files should accept multiple -f flags**, not just comma-separated. When
  shells quote filenames with spaces or commas, `-f "auth.py" -f "login.py"`
  is less error-prone than `-f "auth.py,login.py"`. The comma convention
  works for simple cases but breaks with paths like `src/auth,v2/handler.py`.

- **Conjecture ID is always required for announce** but rarely changes within
  a session. An `AQ_CONJECTURE` environment variable would reduce repetition:
  `export AQ_CONJECTURE=C-1` then `aq announce -f main.go`.

- **Phase default of "proof" is almost always right** for active development.
  Good default.

- **--claim is rarely used** because the auto-generated "working on C-1" is
  usually sufficient. Consider removing it from the required interface and
  keeping it as a hidden option.

## 3. Output Formatting

The status table is readable at small scale (< 10 broadcasts). At larger
scale, the agent address column dominates. Current behavior truncates long
agent addresses with "..." prefix, which is good.

Column widths are hardcoded. For terminal use, detecting terminal width
and adjusting would be better, but for agent consumption (the primary user),
fixed widths are fine.

The `+`/`-`/`!` doctor indicators follow the sb/cprr pattern and are easy
to parse both visually and programmatically.

## 4. Error Messages

Error handling is adequate:
- Missing required flags produce clear messages with the flag name.
- Invalid enum values (phase, status) list valid options.
- Unknown flags point to `--help`.
- File I/O errors include the path that failed.

Missing: no error when `aq announce` is called without `aq init` having
been run first. The announce command creates directories on demand (via
`writeBroadcast` -> `os.MkdirAll`), which is the right behavior (zero
setup needed), but doctor should note if init was never explicitly run.

## 5. Missing Commands

Commands that would be useful but don't exist yet:

- **`aq watch`**: Daemon mode that monitors the channel directory for new
  broadcasts and prints conflict alerts in real-time. This is build step 5.
  Without it, conflict detection is pull-only (you have to ask).
  *Update (v0.5.2)*: `aq listen` now provides RX capability (subscribes to
  UDP and MQTT, materializes incoming broadcasts to disk). A full `aq watch`
  with FSEvents/inotify-driven conflict alerting is still pending.

- **`aq clear`**: Remove your own broadcasts. Currently there's no way to
  clean up without manually deleting files. `aq announce --status done`
  is the intended pattern, but it creates a new broadcast rather than
  removing the old one.

- **`aq expire`**: Force-expire and archive all stale broadcasts. Currently
  `readActive()` does this as a side effect, but an explicit command would
  be useful for maintenance.

- **`aq log`**: Show archived broadcasts (historical view). The archive
  directory exists but there's no command to read it.

- **`aq gc`**: Garbage-collect the archive directory. Broadcasts accumulate
  forever in archive/ with no cleanup policy.

## 6. Integration Gaps

- **Git hooks**: A `post-checkout` hook could auto-announce when switching
  branches. A `pre-push` hook could check for HIGH conflicts and warn.
  Neither is implemented.

- **CI/CD**: No CI integration. A GitHub Action that runs `aq check` and
  posts conflict warnings as PR comments would be valuable.

- **Editor plugins**: No editor integration. An LSP-adjacent service that
  shows active broadcasts for the current file would make the gossip
  visible without leaving the editor.

- **sb integration**: `detectSandbox()` reimplements sandbox detection
  instead of calling `sb detect --json`. This creates drift risk between
  the two implementations. Should shell out to `sb` if available and
  fall back to built-in detection.

## 7. The Single-File Problem

All four agents in this experiment touch `main.go`. File-overlap conflict
detection always fires HIGH for all pairs. This is a false positive rate
problem that directly tests conjecture C-4.

The current heuristic is: shared files + both in proof phase = HIGH. But
file granularity is too coarse. Possible improvements:

- **Function-level overlap**: Parse the file and track which functions each
  agent is modifying. Two agents touching `main.go` but different functions
  is LOW, not HIGH. This requires language-aware parsing, which violates
  the simplicity constraint.

- **Section-level overlap**: Agents could declare file regions instead of
  whole files: `-f "main.go:100-200"`. Crude but effective.

- **Conjecture-aware dedup**: If two agents are working on the same
  conjecture (same conjecture_id), overlap is expected and should be LOW
  regardless of phase.

- **Accept it**: File overlap is a useful signal even when noisy. The cost
  of a false positive (agent checks the diff, sees no real conflict) is low.
  The cost of a false negative (merge conflict at push time) is high.
  Err on the side of alerting.

Recommendation: Accept the noise for now but add conjecture-aware dedup
(same conjecture_id = LOW) as a quick win.

## 8. The TTL Problem

300 seconds (5 minutes) is too short for real work. Typical development
sessions last 30-90 minutes. A broadcast expires long before the work is
done, creating a gap where no conflict detection occurs.

Options:

- **Increase default TTL**: 1800s (30 min) is more realistic. But then stale
  broadcasts linger when agents crash without announcing `status=done`.

- **Auto-renewal**: A daemon (`aq watch --renew`) could re-announce every
  TTL/2 automatically. This is the intended design per the Broadcast
  docstring but isn't implemented.

- **Adaptive TTL**: Start at 300s, double on each re-announce, cap at 3600s.
  This handles both short tasks and long sessions.

- **Heartbeat model**: Instead of TTL-based expiry, use a heartbeat where
  agents must ping every N seconds or their broadcast is considered stale.
  This is functionally equivalent to auto-renewal but makes the intent
  explicit.

Recommendation: Keep 300s default for the broadcast payload, but implement
auto-renewal in the daemon. The short TTL is correct for the protocol (fast
expiry on crash). The problem is the absence of the renewal mechanism, not
the TTL value itself.

## Summary

The CLI is functional and follows the patterns established by sb and cprr.
The biggest gaps are:
1. No full daemon/watch mode (partially addressed: `aq listen` provides RX; full FSEvents watcher still pending)
2. No way to clear/expire broadcasts explicitly
3. File-level conflict granularity is too coarse for multi-agent-on-single-file
4. TTL needs auto-renewal mechanism

None of these are blockers for the current build step. They are refinement
candidates for future conjectures.
