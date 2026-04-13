---
name: doc-check
description: Validate documentation accuracy for agents — staleness, duplication, network leaks, and bloat
user-invocable: true
---

# /doc-check — Validate documentation accuracy for agents

Audit all documentation for accuracy, staleness, duplication, and bloat.
Docs are a lossy projection of the system state — this skill checks
whether the projection is fresh enough for agents to trust.

## What to do

Run the following audit steps and produce a summary table.

### Step 1: Inventory

List all doc files and compute stats:

```bash
find docs papers CHANGELOG.org README.org CLAUDE.md -type f 2>/dev/null | sort
```

Count total files, lines, and bytes. Compare to Go source size:
```bash
wc -l main.go main_test.go
```

### Step 2: Staleness check

For each doc file, extract identifiers (function names, type names, field names, file paths) and verify they exist in the codebase:

```bash
# Extract potential Go identifiers from a doc
grep -oE '[A-Z][a-zA-Z]+\b' <doc> | sort -u | head -20
# Check if they exist in Go source
grep -l '<identifier>' main.go main_test.go contrib/**/*.go
```

Flag a doc as **stale** if it references:
- Version strings older than the current tag (`git describe --tags --abbrev=0`)
- Field names that no longer exist in the Broadcast struct (`conjecture_id` vs `cid`)
- File paths that don't exist (`src/aq/*.py` when Python is deprecated)
- Functions/types not found in the codebase

### Step 3: Duplication check

For each pair of docs in the same domain, check overlap:
- `docs/TRANSPORTS.org` vs `docs/research/TRANSPORT-RESEARCH.md`
- `docs/CHAOS-TESTING.org` vs `docs/research/chaos-test-plan.org`
- `docs/adr/WIRE-FORMAT-V3.1.md` vs `spec-v3-wire.org`

Flag as **duplicate** if >50% of section headings match.

### Step 4: Classification

Classify each doc as one of:
- **reference**: permanent, describes the system as-is (keep in docs/)
- **report**: time-based results, experiments, benchmarks (should be in reports/)
- **derivable**: content available from `aq --help`, `go doc`, `git log`, or `ls` (delete candidate)

### Step 5: Network info audit

Scan for site-specific network info that should not be in public docs:
```bash
grep -rnE '192\.168\.|nexus\.lan|hydra\.lan' docs/ contrib/ CHANGELOG.org CLAUDE.md
```
Flag any hits as **leak**.

### Step 6: Output

Print a table:

```
=== DOC-CHECK RESULTS ===

Current version: <git describe>
Doc files: N  |  Doc lines: N  |  Go lines: N  |  Ratio: X:1

| File | Status | Class | Size | Notes |
|------|--------|-------|------|-------|
| docs/TRANSPORTS.org | stale | reference | 2.1K | references v0.5.0 |
| docs/research/wire-format-stress.org | ok | report | 8.4K | move to reports/ |
...

STALE: N files need updating
DUPLICATE: N pairs should be merged
DERIVABLE: N files are delete candidates
LEAKS: N files have network info
```

Exit with a recommendation: which docs to keep, which to merge, which to move to reports/, which to delete.

### Rules
- Read-only audit. Do NOT edit any files.
- Be opinionated about what to delete.
- The agent-essential minimum is: README.org, CLAUDE.md, spec.org, spec-v3-wire.org, and CHANGELOG.org. Everything else must justify its existence.
