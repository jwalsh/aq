#!/usr/bin/env bash
set -euo pipefail

STATUS="ok"
HARD_PASS=0
HARD_FAIL=0
SOFT_PASS=0
SOFT_FAIL=0

check_hard() {
    local name="$1" cmd="$2"
    if eval "$cmd" >/dev/null 2>&1; then
        HARD_PASS=$((HARD_PASS + 1))
        echo "  PASS  [hard]  $name"
    else
        HARD_FAIL=$((HARD_FAIL + 1))
        STATUS="broken"
        echo "  FAIL  [hard]  $name"
    fi
}

check_soft() {
    local name="$1" cmd="$2"
    if eval "$cmd" >/dev/null 2>&1; then
        SOFT_PASS=$((SOFT_PASS + 1))
        echo "  PASS  [soft]  $name"
    else
        SOFT_FAIL=$((SOFT_FAIL + 1))
        [ "$STATUS" = "ok" ] && STATUS="degraded"
        echo "  FAIL  [soft]  $name"
    fi
}

echo "aq health check"
echo "==============="

check_hard "git repo"      "git rev-parse --git-dir"
check_hard "spec.org"      "test -f spec.org"
check_hard "CLAUDE.md"     "test -f CLAUDE.md"
check_hard "AGENTS.md"     "test -f AGENTS.md"
check_hard "cprr store"    "command -v cprr"
check_hard "remote"        "git remote get-url origin"
check_hard "python import" "python -c 'from aq.protocol import Broadcast'"

check_soft "bd server"     "bd list"
check_soft "bd ready"      "bd ready"
check_soft "sb doctor"     "sb doctor"

echo ""
echo "hard: $HARD_PASS pass, $HARD_FAIL fail"
echo "soft: $SOFT_PASS pass, $SOFT_FAIL fail"
echo "status: $STATUS"

cat <<EOF

{"status":"$STATUS","hard_pass":$HARD_PASS,"hard_fail":$HARD_FAIL,"soft_pass":$SOFT_PASS,"soft_fail":$SOFT_FAIL}
EOF

case "$STATUS" in
    ok)       exit 0 ;;
    degraded) exit 1 ;;
    broken)   exit 2 ;;
esac
