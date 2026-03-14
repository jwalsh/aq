# CTO Strategic Review -- aq

Date: 2026-03-13
Reviewer: CTO (strategic assessment, build/buy/partner)

## One-Line Assessment

aq is a thesis disguised as a tool -- a research artifact with a legitimate gap observation that has not yet proven product-market fit, but whose dogfooding data is more valuable than its code.

## Problem Validation

### Is the problem real?

Yes, but it is narrower than the framing suggests.

Multi-agent development teams do suffer from merge conflicts caused by lack of awareness. The JOURNEY.md data confirms this: four agents, one file, six HIGH conflict pairs, zero useful signal. The problem is real in the same way that merge conflicts in human teams are real -- git solved it with branches, GitHub solved it with pull requests, and most teams solve it with Slack messages ("heads up, I'm touching auth.py").

The question is whether the problem scales with agent count in a way that justifies dedicated tooling. At 2-3 agents, a shared document or Slack channel is sufficient (the DOGFOODING.md itself admits this). At 10-50 agents, file-level overlap detection saturates -- the single-file paradox documented in the dogfooding session is not an edge case but the dominant pattern for Go, Rust, and many modern codebases that use large files.

The real problem aq identifies is not "agents don't know about each other" but rather "there is no standard presence primitive in the multi-agent stack." MCP handles tool invocation. A2A handles agent-to-agent messaging. Neither handles ambient awareness. That gap is genuine. Whether it is a product-sized gap or a feature-sized gap is the open question.

**Verdict**: Real gap, unclear if it is product-scale or feature-scale.

### Is the timing right?

Early 2026 is the moment when multi-agent coding moved from demos to production. Claude Code, Devin, Cursor agents, Codex agents, and internal tooling at mid-tier companies are running multiple agents in parallel on real codebases. The pain point aq addresses -- "Agent A and Agent B both touched auth.py and now the merge is a disaster" -- is being felt right now by exactly the teams pushing hardest on agent-driven development.

However, the market is in a "tools for one agent" phase, not a "tools for fleets of agents" phase. Most teams are still figuring out how to get one agent to be reliable. Fleet coordination is a concern for the top 5% of adopters. aq is building for the early majority's problem before the early adopters have finished standardizing on single-agent workflows.

The GEACL papers (arXiv 2508.01531, 2512.03285) from 2025 validate the academic framing: gossip is the missing substrate for multi-agent coordination. Neither paper has a working implementation. aq has one. Being the first concrete implementation of an academically-validated gap is a strong timing signal -- if the market arrives.

**Verdict**: 12-18 months early. The gap is real but the customer base is not yet large enough. This is a research bet, not a product launch.

### Who else is solving this?

**Direct competitors**: None. Nobody is building a dedicated presence/gossip layer for multi-agent development. This is both an opportunity signal and a warning signal -- it might mean nobody else thinks this is a product.

**Adjacent solutions that could absorb this**:

- **MCP (Model Context Protocol)**: Could add a presence extension. MCP already defines tool invocation; adding "agent X is currently using tool Y on file Z" is a natural extension. If Anthropic ships this, aq is immediately redundant.
- **A2A (Agent-to-Agent Protocol, Google)**: Explicitly designed for agent communication. A presence/awareness mode would be a small addition to the spec. Same risk as MCP.
- **CrewAI / AutoGen / LangGraph**: Orchestration frameworks that already coordinate agent work. They solve the awareness problem through explicit task assignment -- "Agent A owns auth.py" -- rather than gossip. Different philosophy but same outcome for the user.
- **Temporal**: Workflow-level coordination with built-in visibility. If your agents run as Temporal workflows, you already know who is doing what.
- **IDE-native solutions**: Cursor, Windsurf, and Copilot Workspace could add multi-agent awareness as a product feature. The IDE already knows which files are open and which agents are running.

**The moat question**: aq's moat is philosophical, not technical. The filesystem-first, zero-dependency, gossip-not-coordination stance is a design choice that the orchestration frameworks will never make because they are fundamentally coordination tools. If the market wants gossip (ambient, lossy, zero-obligation), aq is the only option. If the market wants coordination (reliable, assigned, guaranteed), aq is irrelevant.

The WS-*/SOA deja vu documented in TRANSPORTS.org is instructive: every problem aq faces has been solved before with registries (UDDI), notifications (WS-Notification), and governance specs. Those solutions failed not because they were wrong but because they were too heavy. aq bets on being light enough to succeed where they failed. But "light" also means "limited."

**Verdict**: No direct competitors, but the gap could be absorbed by MCP/A2A with a single spec extension. The moat is design philosophy, which is defensible only if the philosophy turns out to be correct.

## Build Assessment

### What was built

In a single session (approximately 2 hours wall clock, T+20:31 to T+22:19):

- **Go binary**: 1,601 lines of `main.go`, single-file, stdlib-only, zero dependencies. Commands: `init`, `announce`, `whisper`, `check`, `status`, `doctor`, `quickstart`, `validate`. Functional and tested.
- **59 tests**: 1,309 lines of `main_test.go`. 32 core tests + 26 invariant tests + 1 integration. All passing with race detector.
- **CI pipeline**: GitHub Actions with go vet, gofmt, race-enabled tests, and build verification.
- **7 documentation files**: README.org, TRANSPORTS.org, DOGFOODING.md, JOURNEY.md, INVARIANTS.md, WAVE-PROTOCOL.md, UX-REVIEW.md. Totaling perhaps 2,500+ lines of analysis.
- **Python prototype**: Pre-existing reference implementation in `src/aq/`, used as spec for the Go port.
- **Invariant system**: Three-layer advisory assertion framework (self-checks, world-checks, protocol-checks) with JSON output for CI integration.

What is notably missing: no daemon/watch mode, no heartbeat/auto-renewal, no function-level granularity, no transport abstraction beyond filesystem, no LICENSE file.

### Team velocity

Four agents, four worktrees, ~58 minutes of parallel implementation time, followed by merge and documentation work. The output is roughly equivalent to what a senior engineer could produce in 2-3 days of focused work.

Key velocity observations:

1. **3.5x duplication**: Each agent independently reimplemented the full `main.go` because the single-file convention forced each to write the whole binary. ~3,154 lines of parallel `main.go` were produced to yield ~1,601 lines of merged output. This is the cost of isolation without composability.

2. **Documentation quality exceeds code quality**: The DOGFOODING.md, JOURNEY.md, and INVARIANTS.md are exceptional -- self-aware, unflinching, and analytically rigorous. The code is competent but straightforward Go. The documentation is the real intellectual output.

3. **The meta-circularity produced genuine insight**: Building aq using aq (while aq didn't work) produced eight documented protocol gaps, three new conjectures (C-6, C-7, C-8), and partial refutations of three original conjectures (C-1, C-2, C-4). This is rigorous applied epistemology, not just software development.

4. **Agent discipline failures are load-bearing data**: Agents forgot to announce, created wrong worktrees, let broadcasts expire, and never rebuilt the binary. These failures are not bugs -- they are evidence about the viability of manual gossip. The conclusion ("manual gossip is an oxymoron," requiring automatic heartbeat) could not have been reached without the failures.

**Verdict**: Impressive velocity for code; extraordinary velocity for insight. The 4-agent session produced a working tool AND a falsifiable analysis of its own limitations. Very few human teams achieve this level of self-awareness in a sprint.

### Technical debt

**Debt taken on**:

- **Single-file monolith**: 1,601 lines in one `main.go`. No packages, no interfaces, no transport abstraction. This is intentional (ecosystem convention) but will block any transport extensibility.
- **No daemon mode**: The TTL cliff is documented and understood but not solved. The tool is currently useful for approximately 5 minutes per session.
- **File-level granularity only**: The core conflict heuristic is known to produce 100% false positives in single-file repos. No function-level or AST-based alternative exists.
- **No LICENSE file**: A public repository with no license is legally ambiguous. Trivial to fix but notable by its absence.
- **Python prototype alongside Go binary**: Two implementations of the same protocol. The Python version is the "wire format reference" but has no tests. Maintenance burden will grow.
- **Manual help text**: The `printUsage()` function is hand-maintained and already fell out of sync with the command dispatch table during the session.

**Is it the right debt?**

Mostly yes. The single-file convention is a legitimate design choice for a tool at this stage. The missing daemon is the right thing to defer -- you need the protocol to stabilize before building the watcher. The file-level granularity limit is well-documented as conjecture C-8 with an explicit refutation criterion.

The wrong debt: not having a LICENSE file for a public repository, and the dual Python/Go implementations without a clear deprecation path.

## Strategic Options

### Option A: Internal tool

Use aq internally for agent coordination across the DefRecord ecosystem (sb, cprr, bd, JITIR).

**Pros**: Immediate use case. The dogfooding data shows exactly where aq breaks and what to fix. Internal users are forgiving. The three-primitive interlock (sb + cprr + aq) is a genuine architectural insight that only works if all three tools exist.

**Cons**: Internal tools rarely get the feedback intensity needed to find product-market fit. The DefRecord ecosystem is a one-person shop (Jason Walsh). Internal dogfooding by the creator is not market validation.

**Investment**: 0.5 person-months. Fix the TTL cliff (daemon/heartbeat), add a LICENSE, deprecate the Python prototype.

**Verdict**: Default option. Low risk, low upside, validates the thesis at zero external cost.

### Option B: Open source ecosystem play

Position aq as the presence layer that MCP/A2A don't provide. Target the multi-agent tooling ecosystem.

**Pros**: The GEACL papers provide academic legitimacy. "The concrete implementation of GEACL" is a positioning story. The filesystem-first, zero-dependency design makes integration trivial. The Go binary cross-compiles to every platform.

**Cons**: The market for multi-agent presence is tiny in early 2026. MCP or A2A could absorb this gap with a single spec extension. Open source traction requires marketing effort that competes with engineering effort. The moat (design philosophy) is not defensible against a well-resourced competitor who decides to build it.

**Investment**: 3-5 person-months. Requires: transport abstraction (interface-based backend), function-level conflict detection, daemon with FSEvents/inotify, MCP integration (aq as an MCP resource), proper packaging (Homebrew, apt, nix), documentation site, and sustained community engagement.

**Verdict**: High risk, high potential upside if multi-agent development accelerates on the predicted timeline. The bet is that MCP/A2A will not add presence semantics in the next 12 months. If they do, aq becomes a reference implementation rather than a product.

### Option C: Research artifact

Publish the DOGFOODING.md findings. The journey is the product.

**Pros**: The dogfooding documentation is genuinely novel. "Four AI agents tried to build a coordination tool using that coordination tool, here's what broke" is a compelling paper/blog post. The WS-*/SOA parallel, the bootstrap paradox, the single-file granularity problem, and the "manual gossip is an oxymoron" conclusion are all publishable insights. The CPRR methodology (conjectures with explicit refutation criteria) applied to software development is itself a research contribution.

**Cons**: Research artifacts don't generate revenue. Publishing the journey without continuing the tool makes aq a cautionary tale rather than a solution.

**Investment**: 1 person-month. Clean up the documentation, write a blog post or short paper, present at an agent-tooling meetup. The code stays as-is (working prototype, 59 tests, CI green).

**Verdict**: The highest-ROI option for personal brand and ecosystem influence. The DOGFOODING.md is better than most conference talks on multi-agent coordination. This option is also compatible with Options A and B -- publish the research while continuing to build.

### Option D: Kill it

**When to kill**: If any of the following occur within 6 months:

1. **MCP adds a presence extension**. If Anthropic ships `mcp.presence.broadcast()` and `mcp.presence.active()`, aq's reason to exist evaporates overnight.
2. **A2A adds ambient awareness**. Same logic as MCP.
3. **Multi-agent development does not scale past 2-3 agents per task**. If the industry settles on "one agent per task, human coordinates" as the dominant pattern, fleet-level presence is a solution to a problem nobody has.
4. **The filesystem-first bet fails at scale**. If C-1 is refuted (p99 > 500ms at 10 agents), the core transport choice is wrong and a rewrite is required.
5. **Function-level granularity proves intractable**. If C-8 is refuted (AST parsing complexity exceeds value), the conflict heuristic will never be useful for real codebases.

**Kill signal**: Two or more of the above occurring simultaneously.

## Milestones & Investment

### M0 (Now): Research prototype

**Current state**: Working Go binary, 59 tests, CI pipeline, exhaustive documentation. Core protocol functional. Three documented failure modes (TTL cliff, single-file saturation, bootstrap paradox). Four conjectures partially tested.

**Go/no-go criteria**: M0 is complete. The question is whether to invest in M1.

**Estimated cost**: 0 additional person-months (already spent).

**Key risks**: None. The prototype exists and the documentation is honest about its limitations.

### M1: Internal dogfooding (0-3 months)

**What's needed**:
- Daemon/watch mode with FSEvents/inotify (fixes TTL cliff, validates C-7)
- Auto-announce via git hooks (pre-commit, post-checkout)
- Transport interface abstraction (prep for future backends without shipping them)
- Deprecate Python prototype, Go binary becomes sole implementation
- LICENSE file (Apache 2.0 or MIT)
- Integration with sb (auto-detect worktree) and cprr (auto-populate conjecture context)

**Go/no-go criteria**: Run aq internally for 30 days across 3+ agent sessions. Measure: (a) How many broadcasts expire before being read? (b) How many real conflicts does aq detect that would have been missed? (c) Does any agent voluntarily use aq without being prompted?

If (c) is zero after 30 days, the tool does not solve a felt need. Stop.

**Estimated cost**: 2 person-months.

**Key risks**: The "nobody remembers to announce" problem may not be solvable with git hooks alone. If announcement requires zero human/agent effort and still nobody reads the output, the presence paradigm itself may be wrong.

### M2: External alpha (3-6 months)

**What's needed**:
- Function-level or symbol-level conflict detection (validates C-8)
- MCP resource adapter (expose aq broadcasts as MCP resources)
- Cross-machine transport (NATS or NFS, validates C-1 at scale)
- Homebrew formula, goreleaser for cross-platform binaries
- Benchmark harness: 10 agents, 100 msg/min, p99 latency measurement
- 3-5 external alpha users running aq in real multi-agent workflows

**Go/no-go criteria**: (a) p99 < 500ms at 10 agents (C-1). (b) At least one external user reports aq detected a conflict they would have missed. (c) No MCP/A2A presence spec has been announced.

If (b) is not achieved with 5 alpha users over 60 days, the tool is solving a theoretical problem, not a practical one. Stop.

**Estimated cost**: 4 person-months.

**Key risks**: MCP/A2A presence spec announcement kills the differentiation story. Function-level granularity may require per-language AST parsers, which violates the zero-dependency constraint. External users may not exist yet (market timing risk).

### M3: Production (6-12 months)

**What's needed**:
- Multi-transport backend (filesystem + NATS + S3)
- Web dashboard for fleet visibility
- Integration with major agent frameworks (Claude Code, Cursor, Devin)
- SaaS offering or managed service (if B2B)
- Security audit (broadcast payloads could leak file paths, conjecture details)
- Enterprise features: namespaced channels, RBAC, audit logging

**Go/no-go criteria**: (a) 50+ active external installations. (b) At least one paying customer or one framework integration. (c) Retention: users who install aq still use it 30 days later.

If (c) < 20% retention at 30 days, the tool is a novelty, not a necessity. Kill it or pivot to Option C (research artifact).

**Estimated cost**: 8-12 person-months (requires hiring or contracting).

**Key risks**: The market may consolidate around orchestration (CrewAI, LangGraph) rather than gossip. The "gossip, not coordination" philosophy may be intellectually pure but commercially insufficient -- customers may want aq to also coordinate, at which point it becomes a different product.

## The Real Question

**What would make me bet the company on this?**

Evidence that multi-agent development is moving from "one agent per developer" to "fleet of agents per team" as the default operating model, AND that the awareness/presence gap is causing measurable productivity loss (merge conflicts, duplicated work, wasted compute). If we see teams routinely running 5-10 agents in parallel and losing 20%+ of agent compute to conflicts that could have been detected, aq becomes a productivity tool with clear ROI.

The second signal: if MCP/A2A explicitly decide NOT to add presence semantics -- if the spec authors say "presence is out of scope for MCP" -- then aq has a permanent gap to fill rather than a temporary one.

**What would make me kill it tomorrow?**

An announcement from Anthropic, Google, or OpenAI that their agent protocol includes ambient presence broadcasting. One line in an MCP spec update -- `mcp.presence.announce(files=["auth.py"], phase="proof")` -- and aq's reason to exist is gone. The protocol owners can absorb this feature faster than aq can build a community around it.

The second kill signal: if after three months of internal use, agents consistently ignore aq broadcasts even when they are available. If ambient awareness turns out to be noise that agents (and humans) learn to tune out -- the way people ignore most mDNS traffic -- then the gossip model is philosophically sound but practically useless.

**The honest assessment**: What the team built in 58 minutes is remarkable. The dogfooding documentation is among the best self-critical engineering writing I have seen. The CPRR methodology applied to software development is a genuine intellectual contribution. But the code is a research prototype for a market that does not yet exist at scale. The safest path is Option C (publish the research) combined with Option A (continue internal use), with Option B as an escalation trigger if multi-agent fleet development accelerates faster than expected.

The tool is not the product. The insight is the product. Ship the insight; keep the tool warm.
