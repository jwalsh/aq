# CLAUDE.md v0 Self-Review

## Critical (must fix before proceeding)

- [x] Agent role is stated explicitly (coding, not planning) — line 1-3
- [x] Build order has failure handler text — present after step 7
- [x] Conjectures have instrumentation requirement — dedicated section present
- [x] Axiom appears before line 10 — line 5

## Substantive (fix now)

- [x] Confirmation gate is present — lines 11-13
- [x] Anti-goals state mechanical failure modes — each has "because X" reasoning
- [x] Architectural constraints are named sections — "Filesystem-First Constraint" and "Three-Primitive Interlock"
- [x] Success criteria are testable assertions — end-to-end test with concrete scenario
- [x] No "low relevance" links included — all links are primary references

## Minor (note but proceed)

- [ ] External URL (waveprotocol.org) may need vendoring — noted, proceeding
- [ ] PYTHONPATH assumption for `bin/aq` — mitigated by pyproject.toml install

## Result

All critical and substantive checks pass. CLAUDE.md v0 promoted to v1 (live)
without changes needed. Pre-review snapshot saved as `meta-prompt-v0.md`.
