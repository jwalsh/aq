/-
  aq — Lean 4 formalization of conflict detection properties.

  This file proves key properties of aq's conflict severity computation,
  validating conjecture C-4: "CPRR phase modulates conflict severity."

  The formalization covers:
    1. The type system (Phase, Severity, Status, Broadcast)
    2. The severity computation function
    3. Six core theorems about conflict detection correctness
    4. Four additional strengthening theorems

  Each theorem is motivated by empirical observations from docs/DOGFOODING.md
  and validates a specific aspect of aq's design.

  Reference: CLAUDE.md broadcast payload schema, conflict.py, main.go:checkConflicts
-/

-- ============================================================================
-- Types
-- ============================================================================

/-- CPRR phases from Lakatos's Proofs and Refutations methodology.
    These represent the epistemic state of an agent's work. -/
inductive Phase where
  | conjecture
  | proof
  | refutation
  | refinement
  deriving Repr, DecidableEq

/-- Conflict severity levels. The core of C-4: phase modulates severity.
    - low: neither agent is in proof phase (exploratory overlap)
    - medium: exactly one agent is in proof phase (asymmetric risk)
    - high: both agents are in proof phase (active merge conflict risk) -/
inductive Severity where
  | low
  | medium
  | high
  deriving Repr, DecidableEq

/-- Agent work status.
    - prosecuting: actively working
    - done: finished (broadcast should be cleared from active set)
    - blocked: waiting on external dependency -/
inductive Status where
  | prosecuting
  | done
  | blocked
  deriving Repr, DecidableEq

/-- A broadcast payload representing an agent's ambient presence.
    Maps directly to the CLAUDE.md broadcast payload schema.
    Uses Nat for ts/ttl since Lean's Nat is cleaner for proofs than Float. -/
structure Broadcast where
  agent : String
  conjecture_id : String
  phase : Phase
  status : Status
  files : List String
  ts : Nat
  ttl : Nat
  deriving Repr, DecidableEq

-- ============================================================================
-- Helper: shared files computation
-- ============================================================================

/-- Compute the list of files that appear in both broadcasts' file lists.
    This mirrors the set intersection in conflict.py and main.go:checkConflicts. -/
def sharedFiles (a b : Broadcast) : List String :=
  a.files.filter (fun f => b.files.contains f)

-- ============================================================================
-- Core helper: is this phase proof?
-- ============================================================================

/-- Predicate: is a phase the proof phase? Used in severity computation. -/
def Phase.isProof : Phase → Bool
  | .proof => true
  | _      => false

-- ============================================================================
-- Core function: conflict severity computation
-- ============================================================================

/-- Compute the conflict severity between two broadcasts given their shared files.

    This is the direct formalization of the three-line severity computation from
    main.go (lines 927-934) and conflict.py (lines 34-36):

      bothProof := me.Phase == "proof" && other.Phase == "proof"
      oneProof  := me.Phase == "proof" || other.Phase == "proof"
      severity  := "high" if bothProof else ("medium" if oneProof else "low")

    Returns `none` when there are no shared files (no conflict exists).

    Validates C-4: CPRR phase modulates conflict severity. The severity
    is not flat -- it depends on the epistemic phase of both agents. -/
def severity (a b : Broadcast) (shared : List String) : Option Severity :=
  if shared.isEmpty then none
  else match a.phase.isProof, b.phase.isProof with
    | true,  true  => some .high
    | true,  false => some .medium
    | false, true  => some .medium
    | false, false => some .low

-- ============================================================================
-- Active set predicate
-- ============================================================================

/-- A broadcast is active if it has not expired and its status is not done.

    The expiry check mirrors protocol.py's `is_expired`:
      return time.time() > self.ts + self.ttl

    The status check formalizes the exit condition from README.org:
    an agent announces status=done to signal completion, at which point
    its broadcast should no longer contribute to conflict detection. -/
def isActive (b : Broadcast) (now : Nat) : Prop :=
  now ≤ b.ts + b.ttl ∧ b.status ≠ Status.done

instance : Decidable (isActive b now) :=
  if h1 : now ≤ b.ts + b.ttl then
    if h2 : b.status ≠ Status.done then
      isTrue ⟨h1, h2⟩
    else
      isFalse (fun ⟨_, h2'⟩ => h2 h2')
  else
    isFalse (fun ⟨h1', _⟩ => h1 h1')

-- ============================================================================
-- Severity ordering (for phase_ordering theorem)
-- ============================================================================

/-- Numeric rank for severity, matching main.go's severityRank function.
    Lower rank = more severe. Used to prove ordering properties. -/
def severityRank : Severity → Nat
  | .high   => 0
  | .medium => 1
  | .low    => 2

/-- Severity s1 is strictly more severe than s2. -/
def moreSevereThan (s1 s2 : Severity) : Prop :=
  severityRank s1 < severityRank s2

-- ============================================================================
-- Lemmas about Phase.isProof
-- ============================================================================

theorem Phase.isProof_proof : Phase.isProof .proof = true := rfl

theorem Phase.isProof_conjecture : Phase.isProof .conjecture = false := rfl

theorem Phase.isProof_refutation : Phase.isProof .refutation = false := rfl

theorem Phase.isProof_refinement : Phase.isProof .refinement = false := rfl

theorem Phase.isProof_iff (p : Phase) : p.isProof = true ↔ p = .proof := by
  cases p <;> simp [Phase.isProof]

theorem Phase.not_isProof_iff (p : Phase) : p.isProof = false ↔ p ≠ .proof := by
  cases p <;> simp [Phase.isProof]

-- ============================================================================
-- Theorem 1: both_proof_shared_is_high
-- ============================================================================

/-- C-4 validation, core case.

    When two agents are both in the proof phase and share at least one file,
    the conflict severity is always HIGH.

    This is the critical property for C-4: the phase *modulates* severity.
    Both-proof is the highest-risk scenario because both agents are actively
    writing code that will need to merge.

    Motivated by DOGFOODING.md Section 2 (The Single-File Paradox): when all
    four agents were in proof phase on main.go, every pair produced HIGH.
    The theorem confirms this is by design, not a bug -- the heuristic is
    correct for the multi-file case even though it saturates in the
    single-file degenerate case. -/
theorem both_proof_shared_is_high
    (a b : Broadcast)
    (shared : List String)
    (hne : shared.isEmpty = false)
    (ha : a.phase = .proof)
    (hb : b.phase = .proof)
    : severity a b shared = some .high := by
  unfold severity
  simp [hne, ha, hb, Phase.isProof]

-- ============================================================================
-- Theorem 2: no_shared_files_no_conflict
-- ============================================================================

/-- C-2 validation (conjecture identity prevents semantic conflicts).

    When two agents have completely disjoint file lists, there is no conflict
    regardless of their phases.

    This formalizes the base case: no file overlap means no conflict signal.
    The severity function returns `none`, not `some .low`. This distinction
    matters -- LOW means "we detected overlap but it's low risk," while
    NONE means "there is nothing to report."

    Motivated by DOGFOODING.md Section 5: "The tool needs to exist before
    it can be evaluated fairly." Disjoint files is the common case in a
    healthy multi-agent setup -- the case aq is designed for. -/
theorem no_shared_files_no_conflict
    (a b : Broadcast)
    (shared : List String)
    (hempty : shared.isEmpty = true)
    : severity a b shared = none := by
  unfold severity
  simp [hempty]

-- ============================================================================
-- Theorem 3: severity_symmetric
-- ============================================================================

/-- C-4 validation, symmetry property.

    The conflict severity between agent A and agent B is the same as
    between agent B and agent A, given the same shared file list.

    This is a sanity property: conflict detection should not depend on
    which agent "checks" and which is "checked against." The Python
    implementation in conflict.py iterates over all other broadcasts
    from the perspective of `me`, but the severity computation itself
    is symmetric in the phase arguments.

    The proof proceeds by case analysis on both phases. The key insight
    is that the match on (isProof, isProof) is symmetric because the
    match arms for (true, false) and (false, true) produce the same
    result (medium). -/
theorem severity_symmetric
    (a b : Broadcast)
    (shared : List String)
    : severity a b shared = severity b a shared := by
  unfold severity
  cases a.phase <;> cases b.phase <;> simp [Phase.isProof]

-- ============================================================================
-- Theorem 4: expired_not_active
-- ============================================================================

/-- C-1 validation (filesystem-first transport, TTL-based expiry).

    A broadcast whose timestamp plus TTL is strictly less than the current
    time is not in the active set. This formalizes protocol.py's `is_expired`:

      def is_expired(self) -> bool:
          return time.time() > self.ts + self.ttl

    Motivated by DOGFOODING.md Section 4 (The TTL Cliff): the original
    300-second default TTL caused broadcasts to expire mid-session. The
    default was corrected to 3600 seconds (1 hour) in main.go. This theorem
    confirms the expiry mechanism itself is correct -- the problem documented
    in DOGFOODING.md was with the *default value*, not the *mechanism*.

    Note: we use `b.ts + b.ttl < now` (strict less-than) matching the
    Python implementation's strict `>` comparison. -/
theorem expired_not_active
    (b : Broadcast)
    (now : Nat)
    (hexp : b.ts + b.ttl < now)
    : ¬ isActive b now := by
  intro ⟨h1, _⟩
  exact absurd (Nat.lt_of_lt_of_le hexp (Nat.le_refl now)) (Nat.not_lt.mpr h1)

-- ============================================================================
-- Theorem 5: done_status_clears
-- ============================================================================

/-- C-4 validation, exit condition.

    A broadcast with status=done is not in the active set, regardless
    of its TTL.

    This formalizes the end-to-end acceptance test from CLAUDE.md:
    "Agent A finishes and announces status=done. Watcher confirms
    conflict cleared."

    The done-status exit is critical for the gossip model: without it,
    an agent that finishes early would continue to generate false-positive
    conflict signals until its TTL expires. The `status=done` announcement
    is the agent's way of saying "I'm no longer touching these files."

    Motivated by DOGFOODING.md Section 8: "Retroactive announcements were
    added with status=done, which is technically correct but defeats the
    entire purpose of ambient presence." The theorem confirms that done
    *does* clear -- the problem was that agents announced done *after*
    the fact, not that the mechanism is broken. -/
theorem done_status_clears
    (b : Broadcast)
    (now : Nat)
    (hdone : b.status = .done)
    : ¬ isActive b now := by
  intro ⟨_, h2⟩
  exact h2 hdone

-- ============================================================================
-- Theorem 6: phase_ordering
-- ============================================================================

/-- C-4 validation, ordering property.

    For any non-empty shared file list, when both agents are in the proof
    phase, the severity is strictly higher (more severe) than when both
    agents are in the conjecture phase.

    This is the core claim of C-4: phase *modulates* severity. It's not
    just that proof-vs-proof produces a different label than
    conjecture-vs-conjecture -- it produces a *more severe* one according
    to the severity ordering.

    Motivated by DOGFOODING.md Section 5: "Severity modulated by phase:
    The C-4 conjecture is sound in principle. Both-proof-on-same-file is
    genuinely riskier than conjecture-on-same-file." This theorem
    formalizes that empirical observation. -/
theorem phase_ordering
    (a_proof b_proof a_conj b_conj : Broadcast)
    (shared : List String)
    (hne : shared.isEmpty = false)
    (hap : a_proof.phase = .proof)
    (hbp : b_proof.phase = .proof)
    (hac : a_conj.phase = .conjecture)
    (hbc : b_conj.phase = .conjecture)
    : ∃ s1 s2 : Severity,
        severity a_proof b_proof shared = some s1 ∧
        severity a_conj b_conj shared = some s2 ∧
        moreSevereThan s1 s2 := by
  refine ⟨Severity.high, Severity.low, ?_, ?_, ?_⟩
  · -- severity of proof+proof = high
    unfold severity
    simp [hne, hap, hbp, Phase.isProof]
  · -- severity of conjecture+conjecture = low
    unfold severity
    simp [hne, hac, hbc, Phase.isProof]
  · -- high is more severe than low
    unfold moreSevereThan severityRank
    decide

-- ============================================================================
-- Additional theorems: strengthening the formalization
-- ============================================================================

/-- Medium severity arises when exactly one agent is in proof phase.
    This completes the case analysis for severity (high, medium, low)
    by covering the asymmetric case.

    Validates C-4: the three-tier severity is not arbitrary. Each tier
    corresponds to a distinct risk profile:
    - Both proof: both are writing code that will merge (HIGH)
    - One proof: one is writing, the other is exploring (MEDIUM)
    - Neither proof: both are exploring (LOW) -/
theorem one_proof_is_medium
    (a b : Broadcast)
    (shared : List String)
    (hne : shared.isEmpty = false)
    (ha : a.phase = .proof)
    (hb : b.phase ≠ .proof)
    : severity a b shared = some .medium := by
  unfold severity
  have hbf : b.phase.isProof = false := (Phase.not_isProof_iff b.phase).mpr hb
  simp [hne, ha, Phase.isProof]

/-- Neither agent in proof phase with shared files produces LOW severity.
    Covers the remaining case in the severity trichotomy. -/
theorem neither_proof_is_low
    (a b : Broadcast)
    (shared : List String)
    (hne : shared.isEmpty = false)
    (ha : a.phase ≠ .proof)
    (hb : b.phase ≠ .proof)
    : severity a b shared = some .low := by
  unfold severity
  have haf : a.phase.isProof = false := (Phase.not_isProof_iff a.phase).mpr ha
  have hbf : b.phase.isProof = false := (Phase.not_isProof_iff b.phase).mpr hb
  simp [hne, haf, hbf]

/-- Active broadcasts must be both unexpired and not done.
    This is the conjunction property -- both conditions are required. -/
theorem active_requires_both
    (b : Broadcast) (now : Nat)
    : isActive b now ↔ (now ≤ b.ts + b.ttl ∧ b.status ≠ Status.done) := by
  unfold isActive
  exact Iff.rfl

/-- Severity is total on non-empty shared file lists: it always returns
    some severity value, never none.

    This guarantees that when agents have overlapping files, a severity
    signal is always produced. There are no "gaps" in the detection. -/
theorem severity_total_on_nonempty
    (a b : Broadcast)
    (shared : List String)
    (hne : shared.isEmpty = false)
    : ∃ s : Severity, severity a b shared = some s := by
  unfold severity
  simp [hne]
  cases a.phase <;> cases b.phase <;> simp [Phase.isProof]

-- ============================================================================
-- Summary
-- ============================================================================

/-
  Theorems proved (no sorry):

  1. both_proof_shared_is_high  — C-4 core: proof+proof+shared → HIGH
  2. no_shared_files_no_conflict — C-2: disjoint files → no conflict
  3. severity_symmetric          — C-4: severity(a,b) = severity(b,a)
  4. expired_not_active          — C-1: expired broadcasts leave active set
  5. done_status_clears          — C-4: status=done clears from active set
  6. phase_ordering              — C-4: proof > conjecture in severity ordering

  Additional:
  7. one_proof_is_medium         — C-4: exactly one proof → MEDIUM
  8. neither_proof_is_low        — C-4: no proof → LOW
  9. active_requires_both        — active ↔ unexpired ∧ not-done
  10. severity_total_on_nonempty — non-empty shared files always produce severity

  All proofs are complete. No sorry used.

  Conjecture validation summary:
  - C-4 (phase modulates severity): 6 theorems directly validate the
    three-tier severity model and its ordering properties.
  - C-2 (conjecture identity prevents conflicts): no_shared_files_no_conflict
    confirms that file-disjoint agents never conflict.
  - C-1 (filesystem-first transport): expired_not_active confirms the TTL
    expiry mechanism is correct (the DOGFOODING.md TTL cliff issue is about
    the default value, not the mechanism).
-/
