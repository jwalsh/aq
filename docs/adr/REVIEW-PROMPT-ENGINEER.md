# Staff Prompt Engineer Review --- aq

Date: 2026-03-13
Reviewer: Staff Prompt Engineer (Anthropic, agent instruction design)

## CLAUDE.md Assessment

### Axiom resilience

"aq is gossip, not coordination" is an excellent axiom. It survives aggressive context compression because it is *counterintuitive* --- you expect a multi-agent tool to coordinate, so the negation carries genuine information. Under 8k compression, a model retaining only one sentence from the Foundational Axiom section will retain this one. The axiom is short (7 words), oppositional (X not Y), and domain-specific enough that a model cannot silently substitute a generic interpretation.

Compare to axioms that compress to nothing: "build high-quality software" or "follow best practices." These are invisible under compression because they match the model's prior. The gossip axiom works because it *contradicts* the prior that multi-agent tools should coordinate.

The three-line reinforcement at lines 16-17 ("Do not make aq authoritative, do not make it a retrieval system, do not make it coordinate. It broadcasts. That's it.") is the strongest single paragraph in the file. Under extreme compression, this is what an agent needs --- three negative constraints and a one-sentence positive. This paragraph should be the *first* thing in the file, not behind a role declaration and a layer-stack explanation.

**Risk**: The L1.5 positioning explanation (lines 10-14) compresses *badly*. Terms like "seven-concern stack", "bd", "JITIR", and "mDNS of multi-agent development" are dense references that require external context. An agent encountering CLAUDE.md cold will not know what L1, L2, bd, or JITIR are. Under compression, these terms become noise tokens. They should be moved below the axiom and anti-goals, not placed above them as framing.

### Confirmation gate effectiveness

The confirmation gate (lines 19-23) is well-structured: four specific items, imperative tone, explicit "wait for confirmation." However, the dogfooding evidence shows it was routinely bypassed:

1. Agents created their own worktrees instead of using the ones set up for them (JOURNEY.md, Humor Log #4). This means the confirmation gate either was not triggered or was answered implicitly. The gate says "wait for confirmation before proceeding" but does not specify *who* confirms or *how*. In a headless agent session, there is no confirmer.

2. Agents forgot to run `aq announce` despite the gate listing "which conjectures are relevant" (DOGFOODING.md, section 8, "We forgot to announce (again)"). The gate asks for conjectures but does not ask "have you announced via aq?" --- the tool's own usage is not a gate item.

**Structural flaw**: The gate is positioned as section 3 (after Role and Axiom), but the Build Order (section 9) contains its own implicit gates ("acceptance: X succeeds"). An agent sees two different gating mechanisms and will follow whichever it encounters first in context. Under compression, the earlier gate disappears and the agent follows only Build Order acceptance criteria, which do not include "announce via aq."

**Recommendation**: Merge the confirmation gate into the build order. Each build step should have: (1) the gate items, (2) the acceptance test, (3) the aq announcement requirement. One gating mechanism, not two.

### Anti-goal framing

The four "Not X" statements (lines 34-42) are effective negative constraints. They work because:

1. Each names a *specific tool category* (Temporal, Redis, Celery) that an agent might drift toward. This is important --- without concrete examples, "not an orchestrator" is ambiguous. With "Temporal, Airflow, CrewAI" as grounding, the constraint is unambiguous.

2. Each includes a *reason* ("orchestrators create coupling and single points of failure"). The reason is as important as the constraint --- it gives the agent a principle to apply when encountering novel situations not covered by the four examples.

3. The framing uses negation correctly: "Not X" is more reliable for LLM agents than "avoid X" or "don't become X". The declarative form ("aq is peer-to-peer broadcast") paired with the negative ("Not an orchestrator") gives agents both a boundary and a direction.

**One weakness**: The anti-goals are duplicated almost verbatim in README.org (lines 29-42). When both files are loaded, this duplication consumes context budget for zero additional signal. Worse, if the two copies drift apart during editing, the agent faces contradictory constraints. CLAUDE.md should own the canonical anti-goals; README.org should reference them.

### Conjecture integration

Conjectures C-1 through C-8 are listed with refutation criteria, which is structurally excellent. However, the dogfooding evidence reveals a fundamental problem: **agents do not know what to DO with a conjecture**.

The Build Order (section 9) maps to tasks: "write this code, run this test." Conjectures map to epistemic state: "we believe X, we would be wrong if Y." These are different categories of instruction. An agent assigned build step 3 (conflict detection tests) knows exactly what to produce. An agent told "C-4: CPRR phase modulates conflict severity" does not know whether to (a) implement phase-based severity, (b) write tests for it, (c) measure false positive rates, or (d) write a document evaluating it.

The Instrumentation Requirement section (lines 118-126) tries to bridge this gap ("add instrumentation that can produce evidence for or against it") but the instruction is too abstract. "Measure p99 latency at load" is not an implementable task without knowing: where is the measurement hook? what format? where does the data go? what constitutes a refutation threshold?

**Evidence from dogfooding**: DOGFOODING.md section 9 notes that "Conjectures treated as backlog items" is an anti-pattern discovered during the ARIA bootstrap review. The agents conflated `cprr add` with `bd create` --- treating epistemic frames as tasks. This is a direct result of CLAUDE.md presenting conjectures alongside build steps without distinguishing the two action types.

**Recommendation**: Separate conjectures from build steps explicitly. Add a "Conjecture Protocol" section that tells agents: "When you encounter a conjecture while working on a build step, (1) check whether your code produces evidence for or against it, (2) if yes, add a measurement hook with this signature: [concrete template], (3) log the measurement location in your commit notes under X-Conjectures." This converts the conjecture from an ambient belief into a concrete side-task.

### Information density

CLAUDE.md is 8,518 bytes / ~1,171 words. At roughly 1.3 tokens per word for English prose with code fragments, this is approximately 1,500-1,700 tokens. That is within a healthy budget for a project instruction file --- under 2% of a 128k context window, under 10% of a compressed 16k working context.

However, **information density is uneven**. The file contains three categories of content:

1. **Load-bearing constraints** (must survive compression): axiom, anti-goals, filesystem-first constraint, build order with acceptance tests. ~600 words.
2. **Reference material** (useful but not critical per-turn): payload schema, three-primitive interlock table, research context. ~350 words.
3. **Process overhead** (git notes template, instrumentation requirement): ~220 words.

Under compression, category 3 disappears first. This is acceptable --- git notes are a post-commit concern, not a per-turn concern. Category 2 disappears next. This is problematic --- the payload schema is needed whenever writing protocol code, but it will be the first structured data to vanish under token pressure.

**What gets lost first**: The tables. Markdown tables compress catastrophically in LLM context because they are high-token, low-semantic-density. The Broadcast Payload Schema table (lines 78-89) is 12 lines and ~120 tokens for information that could be expressed as a type signature in 30 tokens: `Broadcast(agent: str, worktree: str, conjecture_id: str, phase: Phase, status: Status, files: list[str], ts: float, ttl: int = 300, id: str)`. The three-primitive interlock table is similarly compressible.

**What should be promoted**: The three negation sentences at lines 16-17 should be the very first content after the heading, before even the role description. The role description ("You are a coding agent working on aq") is generic and compresses to the model's default behavior. The negation sentences are specific and oppositional --- they are the highest-signal content in the file.

## Agent Instruction Anti-Patterns Found

### 1. Dual gating mechanism

CLAUDE.md has both a Confirmation Gate (section 3) and acceptance tests embedded in Build Order (section 9). Under context pressure, agents follow whichever gate they encounter first. If the Confirmation Gate is compressed away, agents proceed without announcing their plan. If Build Order is compressed away, agents have no acceptance criteria. Neither is a clean failure mode.

### 2. Implicit tool usage

CLAUDE.md never explicitly instructs the agent to run `aq announce` before starting work. The Build Order step 4 tests that `aq announce` *works*, but no step says "before you begin coding, run aq announce." The dogfooding data confirms this: agents forgot to announce repeatedly (DOGFOODING.md section 3, "We forgot to announce (again)"). The instruction to "re-announce every TTL/2" appears in README.org (line 136) but not in CLAUDE.md. The agent-facing file omits the tool's own usage protocol.

### 3. Unreachable references

CLAUDE.md references `sb`, `cprr`, `bd`, `JITIR`, "the seven-concern stack", and "DefRecord ecosystem" without defining them. An agent encountering CLAUDE.md cold --- the standard case for a new session --- has no way to resolve these references. They become noise under compression and confusion when the agent tries to act on them. The practical impact: an agent told "A broadcast payload requires all three: worktree identity (sb)" will try to invoke `sb` and fail if `sb` is not installed.

### 4. Metaphor overload in supporting docs

INVARIANTS.md uses the bus metaphor across 35 lines (lines 169-186). DOGFOODING.md includes a WS-* enterprise architect dialogue across 20 lines (lines 243-266). These are excellent for human readers but actively harmful for agent consumption. Agents do not extract actionable constraints from metaphors --- they extract them from imperative statements and structured data. An agent parsing the bus metaphor will not learn "invariants are advisory" as effectively as from the single sentence "Invariants are ADVISORY, not AUTHORITATIVE" (line 30).

### 5. Git notes as unfollowed mandate

The Git Notes section (lines 142-163) mandates eight trailer fields on every commit. The dogfooding data shows zero evidence that any agent followed this mandate. The mandate is positioned late in the file (section 12 of ~15), making it a prime candidate for compression loss. More fundamentally, the mandate requires the agent to (a) know its own model name, (b) know bead IDs, (c) track conjectures across turns, and (d) assess deviations --- meta-cognitive tasks that agents perform unreliably, especially under context pressure.

### 6. Competing canonical sources

CLAUDE.md line 107-108 says "Conjecture status is tracked by CPRR, not here. CLAUDE.md lists the conjectures for agent context only." This creates a split-authority problem. The agent sees conjectures in CLAUDE.md but is told the authoritative source is elsewhere. Under compression, the disclaimer vanishes and the agent treats CLAUDE.md's conjecture list as authoritative. Under full context, the agent may try to invoke `cprr` to check status, adding latency and tool calls for marginal benefit.

## Context Window Budget

| Source | Bytes | Est. tokens | % of 128k | % of 16k working context |
|--------|-------|-------------|-----------|--------------------------|
| CLAUDE.md | 8,518 | ~1,700 | 1.3% | 10.6% |
| README.org | 15,960 | ~3,000 | 2.3% | 18.8% |
| DOGFOODING.md | 22,001 | ~4,200 | 3.3% | 26.3% |
| INVARIANTS.md | 7,360 | ~1,400 | 1.1% | 8.8% |
| JOURNEY.md | 10,198 | ~1,900 | 1.5% | 11.9% |
| **Total** | **64,037** | **~12,200** | **9.5%** | **76.3%** |

If all five files are loaded into a Claude Code session (as they were for this review), they consume roughly 12,200 tokens --- nearly 10% of a 128k window. That is acceptable for a full-context review session.

However, in a *working* session where the agent also has source code, test output, tool results, and conversation history, the effective working context is far smaller. A realistic working context budget after system prompt, tool definitions, and conversation history is roughly 16k-32k tokens. At 12,200 tokens for documentation alone, the project docs consume 38-76% of the working budget, leaving the agent with limited room for actual code reasoning.

**CLAUDE.md alone** (1,700 tokens, ~10% of a 16k working context) is well-sized. This is the only file that should be auto-loaded. All other docs should be loaded on demand.

## Prompt Resilience Testing

### What survives 8k compression

- The foundational axiom ("gossip, not coordination")
- The four anti-goals (named tool categories provide anchoring)
- Build Order steps 1-4 (numbered lists survive compression well)
- The end-to-end acceptance test (concrete scenario with named agents)
- Filesystem-first constraint (single clear directive)

### What survives 4k compression

- The axiom (7 words, high signal)
- "Not an orchestrator / not a broker" (the first two anti-goals; later ones may truncate)
- Build step 1 acceptance test (`from aq.protocol import Broadcast`)
- "Python 3.11+, no runtime dependencies" (Stack Preferences, short and concrete)
- The role sentence ("You are a coding agent working on aq")

### What gets lost first

1. **Git Notes mandate** --- positioned late, high complexity, no reinforcement. First to vanish, first to be ignored even at full context.
2. **Three-Primitive Interlock table** --- requires understanding of sb/cprr/bd. Becomes noise under compression because the referents are not in scope.
3. **Broadcast Payload Schema table** --- high token count, compressible to a type signature. The table format is wasteful.
4. **Research Context** --- links to waveprotocol.org (which is dead, per DOGFOODING.md) and Lakatos. Zero operational value for an agent writing code.
5. **Open Conjectures C-6, C-7, C-8** --- later list items. An agent under pressure retains C-1 and C-2 (first two) and loses the rest.
6. **Instrumentation Requirement** --- abstract directive without concrete template. Compresses to "add measurement" which an agent interprets as "add a log statement" or ignores entirely.

## Milestones (from prompt engineering perspective)

### M0: Current agent-readiness

**Verdict: Partially ready. An agent can pick up CLAUDE.md cold and build, but will deviate in predictable ways.**

Strengths:
- Clear build order with acceptance gates provides a concrete execution path
- Anti-goals prevent the most common drift pattern (building a task queue or orchestrator)
- Axiom is memorable and oppositional, providing genuine constraint

Weaknesses:
- An agent working cold will not know what `sb`, `cprr`, or `bd` are, and will either hallucinate their behavior or ignore the three-primitive interlock
- The confirmation gate will be skipped in headless sessions (no confirmer) or when context pressure pushes the agent directly to Build Order
- The agent will not run `aq announce` because CLAUDE.md never tells it to as a per-session action (only as a build step to test)
- Conjectures will be ignored or treated as tasks --- the agent has no protocol for "what do I do with a conjecture?"

Evidence from dogfooding that this assessment is correct:
- Agents created wrong worktrees (JOURNEY.md #4) --- spatial context instructions were not actionable
- Agents forgot to announce (DOGFOODING.md section 3) --- tool usage was not mandated in the instruction flow
- Four agents each reimplemented the full main.go (JOURNEY.md, Hour 1) --- the build order did not prevent scope overlap because the single-file convention was not addressed

### M1: Compression-resilient instructions

CLAUDE.md needs restructuring for agents working under 4k-8k effective context. Changes:

1. Move the three negation sentences ("Do not make aq authoritative...") to line 1, before the role description. These are the highest-signal constraint.
2. Collapse the Broadcast Payload Schema from a table to a type signature. Tables are token-expensive and compress badly.
3. Remove Research Context entirely from CLAUDE.md. An agent writing code does not need to know about Lakatos or waveprotocol.org. Move to README.org (which is human-facing).
4. Inline the critical constraint from each conjecture into the relevant build step, rather than listing conjectures in a separate section. C-1 belongs in build step 7 (benchmark). C-4 belongs in build step 3 (conflict detection tests).
5. Replace the Git Notes section with a one-line directive: "After committing, run `make notes` to add agent metadata." Push the template into a Makefile target or script, not into the instruction file.

### M2: Multi-agent prompt coordination

When N agents work in parallel, CLAUDE.md needs to answer: "What am I NOT doing?"

Currently, CLAUDE.md gives every agent the same full instruction set. The dogfooding showed the result: four agents each built the complete main.go because nothing told them their scope boundary. In a multi-agent scenario, CLAUDE.md should support parameterization:

1. Add a `## Your Scope` section that each agent's launcher can populate: "You are Agent Alpha. Your scope is build steps 1-2. You are NOT responsible for build steps 3-7. Other agents are handling those."
2. Add a `## Active Peers` section that can be populated from `aq status` output at session start: "Agent Beta is working on conflict.go (C-4). Agent Gamma is working on cli.go (C-3). Avoid modifying their files."
3. Add an explicit `## Session Protocol` that is distinct from the build order: "At session start: run `aq announce -c [your-conjecture] -f [your-files]`. Every 2 minutes: re-announce. At session end: `aq announce --status done`."

The key insight from the dogfooding: CLAUDE.md is a single-agent document being used in a multi-agent context. It needs a multi-agent protocol layer, and that layer should be generated dynamically from aq's own state, not hand-written.

## Recommendations

### P0: Critical (do immediately)

1. **Add explicit aq usage protocol to CLAUDE.md.** Insert a `## Session Protocol` section after the Confirmation Gate with concrete commands: `aq announce` at start, re-announce cadence, `aq announce --status done` at end. The dogfooding proves that agents will not use aq unless explicitly instructed to in the agent-facing file. The "re-announce every TTL/2" instruction exists only in README.org, not in CLAUDE.md. (Evidence: DOGFOODING.md section 3, "We forgot to announce (again)")

2. **Restructure CLAUDE.md for compression resilience.** Move the three negation sentences ("Do not make aq authoritative...") to the top of the file, before the role description. Move Research Context and Git Notes template to separate files or README.org. Target: CLAUDE.md under 1,000 words / 1,300 tokens. Current is 1,171 words; cutting Research Context (-50 words) and collapsing tables (-100 words) gets close. (Rationale: every token spent on Lakatos references is a token unavailable for code reasoning)

3. **Merge confirmation gate into build order.** Eliminate the dual-gating anti-pattern. Each build step becomes: precondition check, aq announcement, implementation, acceptance test. One flow, not two. (Evidence: the gate was routinely bypassed; JOURNEY.md shows agents proceeding without confirmation)

### P1: Important (do this week)

4. **Replace Broadcast Payload Schema table with a type signature.** `Broadcast(agent: str, worktree: str, conjecture_id: str, conjecture_claim: str, phase: Phase, status: Status, files: list[str], ts: float, ttl: int = 300, id: str)` is 40 tokens vs ~120 for the table. Tables are the first structured data to degrade under compression. (Rationale: agents need the schema when writing protocol code; the compact form survives better)

5. **Add scope parameterization for multi-agent use.** Add a `## Your Scope` section with placeholders that agent launchers can fill: `BUILD_STEPS=`, `CONJECTURE=`, `FILES=`, `PEER_AGENTS=`. Without scope boundaries, N agents each build the full system. (Evidence: JOURNEY.md, "4 complete implementations instead of 4 composable slices")

6. **Remove or relocate unreachable references.** Terms `sb`, `cprr`, `bd`, `JITIR`, "seven-concern stack", and "DefRecord ecosystem" should either be defined inline (one sentence each) or removed from CLAUDE.md. An agent that cannot resolve a reference will either hallucinate its meaning or waste tool calls trying to look it up. If these tools are required, add them to a `## Prerequisites` section with installation/verification commands.

7. **Add a conjecture-action protocol.** Replace the abstract "add instrumentation" directive with a concrete template: "When implementing code related to conjecture C-N, add a function `measure_c_N()` that returns a dict with keys `conjecture_id`, `metric_name`, `value`, `threshold`, `passed`. Call it from the relevant test. Log the result under X-Conjectures in git notes." Without a concrete protocol, agents treat conjectures as documentation, not as testable hypotheses. (Evidence: DOGFOODING.md section 9, "Conjectures treated as backlog items")

### P2: Nice to have (do when convenient)

8. **Deduplicate anti-goals between CLAUDE.md and README.org.** Both files contain nearly identical anti-goal sections. CLAUDE.md should own the canonical version; README.org should use a one-line reference: "See CLAUDE.md for anti-goals." Duplication wastes context budget and creates drift risk.

9. **Convert INVARIANTS.md bus metaphor to imperative statements for agent consumption.** The bus metaphor is 35 lines of prose that compress to one actionable sentence: "Invariants warn but never block; a failing invariant does not prevent a broadcast." Agents need the sentence, not the metaphor. Keep the metaphor in a human-facing README or design doc.

10. **Add a `## Failure Modes` section to CLAUDE.md.** The dogfooding produced six documented protocol gaps (JOURNEY.md table). These are the known failure modes agents will encounter. Listing them in CLAUDE.md with "what to do when X happens" guidance prevents agents from re-discovering (and mishandling) known issues. Example: "If `aq status` shows no active broadcasts but you know agents are working, the TTL has expired. Re-announce."

11. **Reduce DOGFOODING.md to a summary for agent context.** At 22,001 bytes (est. 4,200 tokens), DOGFOODING.md is the largest file in the docs directory and is loaded as project context. Most of its content is retrospective narrative (the WS-* dialogue, the ARIA bootstrap review) that has no per-turn value for an agent writing code. Extract the actionable lessons (the Protocol Gaps table, the "what we wished existed" list) into CLAUDE.md or a concise addendum. Keep the full narrative for human readers.

12. **Gate the Git Notes mandate behind a Makefile target.** Replace the eight-field template in CLAUDE.md with `make notes` that auto-populates what it can (model name, timestamp, branch) and prompts for what it cannot (conjecture IDs, deviations). Agents reliably run Makefile targets; they unreliably compose multi-field metadata strings from memory under context pressure.
