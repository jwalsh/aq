---
name: compliance
description: Check codebase compliance with CLAUDE.md mandates — network leaks, filesystem-first, anti-goals, git hygiene
user-invocable: true
---

# /compliance — CLAUDE.md mandate verification

Run this before any push or release to verify the codebase hasn't
drifted from its own rules.

## Checks to run (in order)

### 1. Network info leak scan (CRITICAL — blocks push)

```bash
# Real IPs in tracked files (only placeholder x.x patterns allowed)
git ls-files | xargs grep -nE '192\.168\.[0-9]+\.[0-9]+|10\.[0-9]+\.[0-9]+\.[0-9]+' 2>/dev/null | grep -v '\.x\.' | grep -v 'config\.sample' | grep -v 'transports\.sample'

# LAN hostnames
git ls-files | xargs grep -nE 'nexus\.lan|hydra\.lan|mini\.lan' 2>/dev/null

# Credentials
git ls-files | xargs grep -nE 'password\s*=|api[_-]key\s*=|secret\s*=' 2>/dev/null | grep -v 'test' | grep -v '#'
```

**Zero hits = PASS.** Any hit = FAIL. Do not push.

### 2. Filesystem-first (CRITICAL)

Verify `aq announce` works without any network:

```bash
# Should succeed even with no UDP/MQTT/mDNS running
AQ_HOME=$(mktemp -d) aq announce -c C-compliance --claim "fs-first test" --json 2>/dev/null
```

**Produces valid JSON with all v3 fields = PASS.**

### 3. No bytecode/cache in git

```bash
git ls-files | grep -E '__pycache__|\.pyc|\.pyo|\.egg-info|\.DS_Store|node_modules'
```

**Zero hits = PASS.**

### 4. Wire format v3 compliance

```bash
# Announce must emit v=3, cid (not conjecture_id), single-char phase/status, mandatory host/user
aq announce -c C-v3check --claim "wire test" --json 2>/dev/null | python3 -c "
import json, sys
b = json.load(sys.stdin)
checks = [
    ('v', b.get('v') == 3),
    ('cid', 'cid' in b),
    ('no conjecture_id', 'conjecture_id' not in b),
    ('claim', 'claim' in b),
    ('no conjecture_claim', 'conjecture_claim' not in b),
    ('phase single-char', len(b.get('phase','xx')) == 1),
    ('status single-char', len(b.get('status','xx')) == 1),
    ('host non-empty', bool(b.get('host'))),
    ('user non-empty', bool(b.get('user'))),
]
for name, ok in checks:
    print(f'  {\"PASS\" if ok else \"FAIL\"}: {name}')
if not all(ok for _, ok in checks):
    sys.exit(1)
"
```

**All 9 checks PASS = compliant.**

### 5. Anti-goals scan

Verify no coordination, locking, or task-assignment patterns crept in:

```bash
# These should return zero hits in main.go
grep -cE 'sync\.Mutex|sync\.RWMutex|sync\.WaitGroup' main.go
grep -cE 'chan.*request|chan.*response|leader|elect|consensus' main.go
grep -cE 'Enqueue|Dequeue|TaskAssign|Lock\(\)|Unlock\(\)' main.go
```

**All zero = PASS.** Any hit needs review against the anti-goals in CLAUDE.md.

### 6. Build and test

```bash
go build ./... && go test ./... -count=1 -timeout 60s
go test -C contrib/codecs ./... -count=1 -timeout 60s
go test -C contrib/harness ./... -count=1 -timeout 120s
go test -C contrib/otel-bridge ./... -count=1 -timeout 60s
```

**All pass = PASS.**

### 7. CI status

```bash
gh run list --limit 1 --json conclusion -q '.[0].conclusion'
```

**"success" = PASS.**

## Output format

```
=== COMPLIANCE CHECK ===
Date: $(date -u +%Y-%m-%dT%H:%M:%SZ)
Version: $(aq version)

1. Network leaks:     PASS/FAIL (N hits)
2. Filesystem-first:  PASS/FAIL
3. No bytecode:       PASS/FAIL
4. Wire format v3:    PASS/FAIL (N/9 checks)
5. Anti-goals:        PASS/FAIL
6. Build & test:      PASS/FAIL (N modules)
7. CI:                PASS/FAIL

OVERALL: N/7 PASS
```

If any CRITICAL check fails (1, 2), do NOT push. Fix first.
