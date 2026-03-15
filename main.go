// aq — ambient agent queue
//
// Gossip layer (L1.5) for multi-agent development. Agents broadcast
// presence via filesystem-backed channels so peers detect semantic
// conflicts before they become merge conflicts.
//
// See spec.org for the full specification.
// See CLAUDE.md for agent instructions.
package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

var (
	Version   = "dev"
	GitCommit = "none"
	BuildDate = "unknown"
)

// Default configuration constants.
//
// DefaultTTL is 3600s (1 hour) to match real agent session length.
// The original 300s (5min) caused "gossip with amnesia" — broadcasts
// expired while agents were still working. Observed 5 times during
// dogfooding (DOGFOODING.md §4, §8). Whisper remains 60s for
// transient read-level presence signals.
const (
	DefaultTTL     = 3600
	WhisperTTL     = 60
	DefaultChannel = "broadcast"
)

// Global flags parsed before subcommand dispatch.
var (
	jsonOutput  bool
	channelName string
)

// ---------- ULID generation ----------

// generateULID produces a 22-character ID: 12 hex chars of millisecond
// timestamp + 10 random lowercase-alphanumeric chars. This matches the
// Python prototype's ulid() function.
func generateULID() string {
	ms := time.Now().UnixMilli()
	ts := fmt.Sprintf("%012x", ms)

	b := make([]byte, 5)
	_, _ = rand.Read(b)
	r := hex.EncodeToString(b) // 10 hex chars, all [0-9a-f]
	return ts + r
}

// ---------- Broadcast payload ----------

// Phase represents a CPRR epistemic phase.
type Phase string

const (
	PhaseConjecture Phase = "conjecture"
	PhaseProof      Phase = "proof"
	PhaseRefutation Phase = "refutation"
	PhaseRefinement Phase = "refinement"
)

// Valid returns true if p is one of the four CPRR phases.
func (p Phase) Valid() bool {
	switch p {
	case PhaseConjecture, PhaseProof, PhaseRefutation, PhaseRefinement:
		return true
	}
	return false
}

// Status represents a broadcast's work status.
type Status string

const (
	StatusProsecuting Status = "prosecuting"
	StatusDone        Status = "done"
	StatusBlocked     Status = "blocked"
)

// Severity represents a conflict severity level.
type Severity string

const (
	SeverityHigh   Severity = "high"
	SeverityMedium Severity = "medium"
	SeverityLow    Severity = "low"
)

// Broadcast is the ambient presence payload. Lifecycle:
//  1. announce() before touching files
//  2. re-announce every TTL/2 while working
//  3. announce(status="done") when finished
type Broadcast struct {
	Agent           string   `json:"agent"`
	Worktree        string   `json:"worktree"`
	ConjectureID    string   `json:"conjecture_id"`
	ConjectureClaim string   `json:"conjecture_claim"`
	Phase           Phase    `json:"phase"`
	Status          Status   `json:"status"`
	Files           []string `json:"files"`
	Ts              float64  `json:"ts"`
	TTL             int      `json:"ttl"`
	ID              string   `json:"id"`
}

// NewBroadcast creates a Broadcast with sensible defaults filled in.
func NewBroadcast() Broadcast {
	return Broadcast{
		Ts:  float64(time.Now().Unix()),
		TTL: DefaultTTL,
		ID:  generateULID(),
	}
}

// IsExpired returns true if the broadcast has outlived its TTL.
func (b *Broadcast) IsExpired() bool {
	return float64(time.Now().Unix()) > b.Ts+float64(b.TTL)
}

// Overlaps returns true if the two broadcasts share at least one file.
func (b *Broadcast) Overlaps(other *Broadcast) bool {
	set := make(map[string]struct{}, len(b.Files))
	for _, f := range b.Files {
		set[f] = struct{}{}
	}
	for _, f := range other.Files {
		if _, ok := set[f]; ok {
			return true
		}
	}
	return false
}

// ToJSON marshals the broadcast to a JSON string.
func (b *Broadcast) ToJSON() (string, error) {
	data, err := json.Marshal(b)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

// BroadcastFromJSON unmarshals a JSON string into a Broadcast.
func BroadcastFromJSON(s string) (Broadcast, error) {
	var b Broadcast
	err := json.Unmarshal([]byte(s), &b)
	return b, err
}

// ---------- ConflictSignal ----------

// ConflictSignal records a detected file overlap between two broadcasts,
// with severity determined by CPRR phase.
type ConflictSignal struct {
	A           Broadcast `json:"a"`
	B           Broadcast `json:"b"`
	SharedFiles []string  `json:"shared_files"`
	Severity    Severity  `json:"severity"`
}

// Summary returns a human-readable one-liner for the conflict.
func (c *ConflictSignal) Summary() string {
	files := strings.Join(c.SharedFiles, ", ")
	return fmt.Sprintf("[%s] %s (%s) <-> %s (%s) -- shared: %s",
		strings.ToUpper(string(c.Severity)),
		c.A.Agent, c.A.ConjectureID,
		c.B.Agent, c.B.ConjectureID,
		files)
}

// ---------- Invariants ----------
//
// Invariants are advisory assertions about broadcasts, the world, or the
// protocol. They warn but never block. Gossip without invariants is rumor;
// gossip with invariants is intelligence.

// InvariantResult captures the outcome of a single invariant check.
type InvariantResult struct {
	Name     string `json:"name"`
	Passed   bool   `json:"passed"`
	Message  string `json:"message"`
	Category string `json:"category"` // "self", "world", "protocol"
	Severity string `json:"severity"` // "error", "warning", "info"
}

// Invariant represents a verifiable assertion. The Check function is not
// serialized -- it is the executable check itself.
type Invariant struct {
	Name        string                 `json:"name"`
	Category    string                 `json:"category"`
	Description string                 `json:"description"`
	Severity    string                 `json:"severity"`
	Check       func() InvariantResult `json:"-"`
}

// --- Self-check invariants (Layer A) ---
// These verify a broadcast's claims against reality before writing.

// checkFilesExist verifies that all files in the broadcast actually exist
// on the filesystem relative to the current working directory.
func checkFilesExist(files []string) InvariantResult {
	var missing []string
	for _, f := range files {
		if _, err := os.Stat(f); os.IsNotExist(err) {
			missing = append(missing, f)
		}
	}
	if len(missing) > 0 {
		return InvariantResult{
			Name:     "files_exist",
			Passed:   false,
			Message:  fmt.Sprintf("files not found: %s", strings.Join(missing, ", ")),
			Category: "self",
			Severity: "warning",
		}
	}
	return InvariantResult{
		Name:     "files_exist",
		Passed:   true,
		Message:  fmt.Sprintf("all %d files exist", len(files)),
		Category: "self",
		Severity: "info",
	}
}

// checkGitBranchMatches verifies that the current git branch matches the
// worktree field that will be broadcast.
func checkGitBranchMatches(worktree string) InvariantResult {
	cmd := exec.Command("git", "rev-parse", "--abbrev-ref", "HEAD")
	out, err := cmd.Output()
	if err != nil {
		return InvariantResult{
			Name:     "git_branch_matches",
			Passed:   false,
			Message:  "cannot determine git branch",
			Category: "self",
			Severity: "warning",
		}
	}
	branch := strings.TrimSpace(string(out))
	if branch != worktree {
		return InvariantResult{
			Name:     "git_branch_matches",
			Passed:   false,
			Message:  fmt.Sprintf("git branch is %q but announcing worktree %q", branch, worktree),
			Category: "self",
			Severity: "warning",
		}
	}
	return InvariantResult{
		Name:     "git_branch_matches",
		Passed:   true,
		Message:  fmt.Sprintf("branch matches: %s", branch),
		Category: "self",
		Severity: "info",
	}
}

// checkPhaseValid verifies that the phase is one of the four valid CPRR phases.
func checkPhaseValid(phase Phase) InvariantResult {
	if !phase.Valid() {
		return InvariantResult{
			Name:     "phase_valid",
			Passed:   false,
			Message:  fmt.Sprintf("invalid phase %q; must be conjecture|proof|refutation|refinement", phase),
			Category: "self",
			Severity: "error",
		}
	}
	return InvariantResult{
		Name:     "phase_valid",
		Passed:   true,
		Message:  fmt.Sprintf("phase %q is valid", phase),
		Category: "self",
		Severity: "info",
	}
}

// checkTTLReasonable verifies that the TTL is within a sane range.
// TTL below 10s is probably a mistake; above 86400s (24h) is stale gossip.
func checkTTLReasonable(ttl int) InvariantResult {
	if ttl < 10 {
		return InvariantResult{
			Name:     "ttl_reasonable",
			Passed:   false,
			Message:  fmt.Sprintf("TTL %d is too short (minimum 10s)", ttl),
			Category: "self",
			Severity: "warning",
		}
	}
	if ttl > 86400 {
		return InvariantResult{
			Name:     "ttl_reasonable",
			Passed:   false,
			Message:  fmt.Sprintf("TTL %d exceeds 24h — gossip should not persist this long", ttl),
			Category: "self",
			Severity: "warning",
		}
	}
	return InvariantResult{
		Name:     "ttl_reasonable",
		Passed:   true,
		Message:  fmt.Sprintf("TTL %ds is reasonable", ttl),
		Category: "self",
		Severity: "info",
	}
}

// checkPathsRelative verifies that no file paths are absolute.
// Absolute paths leak filesystem structure and break portability.
func checkPathsRelative(files []string) InvariantResult {
	var absolute []string
	for _, f := range files {
		if filepath.IsAbs(f) {
			absolute = append(absolute, f)
		}
	}
	if len(absolute) > 0 {
		return InvariantResult{
			Name:     "paths_relative",
			Passed:   false,
			Message:  fmt.Sprintf("absolute paths found: %s", strings.Join(absolute, ", ")),
			Category: "self",
			Severity: "error",
		}
	}
	return InvariantResult{
		Name:     "paths_relative",
		Passed:   true,
		Message:  "all paths are relative",
		Category: "self",
		Severity: "info",
	}
}

// --- World-check invariants (Layer B) ---
// These verify that the world hasn't changed since the agent last looked.

// checkBranchNotDiverged checks if origin/main has moved significantly
// since the current branch was created. "Significantly" = more than 50 commits.
func checkBranchNotDiverged() InvariantResult {
	cmd := exec.Command("git", "rev-list", "--count", "HEAD..origin/main")
	out, err := cmd.Output()
	if err != nil {
		// origin/main may not exist or git not available -- not an error.
		return InvariantResult{
			Name:     "branch_not_diverged",
			Passed:   true,
			Message:  "cannot check divergence (no origin/main or no git)",
			Category: "world",
			Severity: "info",
		}
	}
	countStr := strings.TrimSpace(string(out))
	var count int
	fmt.Sscanf(countStr, "%d", &count)
	if count > 50 {
		return InvariantResult{
			Name:     "branch_not_diverged",
			Passed:   false,
			Message:  fmt.Sprintf("origin/main is %d commits ahead — consider rebasing", count),
			Category: "world",
			Severity: "warning",
		}
	}
	return InvariantResult{
		Name:     "branch_not_diverged",
		Passed:   true,
		Message:  fmt.Sprintf("origin/main is %d commits ahead", count),
		Category: "world",
		Severity: "info",
	}
}

// checkNoGhostBroadcasts checks if the current agent has any active
// broadcasts that it may have forgotten about (TTL still valid but old).
func checkNoGhostBroadcasts(agentAddress string, channel string) InvariantResult {
	active, err := readActive(channel)
	if err != nil {
		return InvariantResult{
			Name:     "no_ghost_broadcasts",
			Passed:   true,
			Message:  "cannot read active broadcasts",
			Category: "world",
			Severity: "info",
		}
	}
	var ghosts []string
	now := float64(time.Now().Unix())
	for _, b := range active {
		if b.Agent == agentAddress {
			age := now - b.Ts
			remaining := float64(b.TTL) - age
			// Ghost: more than 80% of TTL has elapsed.
			if remaining < float64(b.TTL)*0.2 {
				ghosts = append(ghosts, fmt.Sprintf("%s (%.0fs remaining)", b.ConjectureID, remaining))
			}
		}
	}
	if len(ghosts) > 0 {
		return InvariantResult{
			Name:     "no_ghost_broadcasts",
			Passed:   false,
			Message:  fmt.Sprintf("near-expiry broadcasts: %s — consider re-announcing", strings.Join(ghosts, "; ")),
			Category: "world",
			Severity: "warning",
		}
	}
	return InvariantResult{
		Name:     "no_ghost_broadcasts",
		Passed:   true,
		Message:  "no ghost broadcasts",
		Category: "world",
		Severity: "info",
	}
}

// checkDiskSpaceOK checks that AQ_HOME is not consuming excessive disk space.
// Threshold: 100MB.
func checkDiskSpaceOK() InvariantResult {
	home := aqHome()
	var totalSize int64
	_ = filepath.Walk(home, func(_ string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if !info.IsDir() {
			totalSize += info.Size()
		}
		return nil
	})
	sizeMB := float64(totalSize) / (1024 * 1024)
	if sizeMB > 100 {
		return InvariantResult{
			Name:     "disk_space_ok",
			Passed:   false,
			Message:  fmt.Sprintf("AQ_HOME is %.1fMB (threshold: 100MB) — consider running gc", sizeMB),
			Category: "world",
			Severity: "warning",
		}
	}
	return InvariantResult{
		Name:     "disk_space_ok",
		Passed:   true,
		Message:  fmt.Sprintf("AQ_HOME is %.1fMB", sizeMB),
		Category: "world",
		Severity: "info",
	}
}

// --- Protocol-check invariants (Layer C) ---
// These verify that the gossip protocol's structural properties hold.

// checkULIDUnique scans all broadcasts (active + archive) and checks for
// duplicate ULIDs.
func checkULIDUnique(channel string) InvariantResult {
	seen := make(map[string]string) // ULID -> filename
	var duplicates []string

	scanDir := func(dir string) {
		entries, err := os.ReadDir(dir)
		if err != nil {
			return
		}
		for _, entry := range entries {
			if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
				continue
			}
			path := filepath.Join(dir, entry.Name())
			data, err := os.ReadFile(path)
			if err != nil {
				continue
			}
			var b Broadcast
			if err := json.Unmarshal([]byte(strings.TrimSpace(string(data))), &b); err != nil {
				continue
			}
			if prev, ok := seen[b.ID]; ok {
				duplicates = append(duplicates, fmt.Sprintf("%s (in %s and %s)", b.ID, prev, entry.Name()))
			} else {
				seen[b.ID] = entry.Name()
			}
		}
	}

	scanDir(requestsPath(channel))
	scanDir(archivePath(channel))

	if len(duplicates) > 0 {
		return InvariantResult{
			Name:     "ulid_unique",
			Passed:   false,
			Message:  fmt.Sprintf("duplicate ULIDs: %s", strings.Join(duplicates, "; ")),
			Category: "protocol",
			Severity: "error",
		}
	}
	return InvariantResult{
		Name:     "ulid_unique",
		Passed:   true,
		Message:  fmt.Sprintf("all %d ULIDs are unique", len(seen)),
		Category: "protocol",
		Severity: "info",
	}
}

// checkNoDuplicateActive verifies that no agent has two active broadcasts
// for the same conjecture ID (unless one is "done").
func checkNoDuplicateActive(channel string) InvariantResult {
	active, err := readActive(channel)
	if err != nil {
		return InvariantResult{
			Name:     "no_duplicate_active",
			Passed:   true,
			Message:  "cannot read active broadcasts",
			Category: "protocol",
			Severity: "info",
		}
	}

	type key struct {
		agent, conjecture string
	}
	seen := make(map[key]int)
	var duplicates []string

	for _, b := range active {
		if b.Status == StatusDone {
			continue
		}
		k := key{b.Agent, b.ConjectureID}
		seen[k]++
		if seen[k] == 2 {
			duplicates = append(duplicates, fmt.Sprintf("%s+%s", b.Agent, b.ConjectureID))
		}
	}

	if len(duplicates) > 0 {
		return InvariantResult{
			Name:     "no_duplicate_active",
			Passed:   false,
			Message:  fmt.Sprintf("duplicate active broadcasts: %s", strings.Join(duplicates, "; ")),
			Category: "protocol",
			Severity: "warning",
		}
	}
	return InvariantResult{
		Name:     "no_duplicate_active",
		Passed:   true,
		Message:  "no duplicate active broadcasts",
		Category: "protocol",
		Severity: "info",
	}
}

// checkTimestampsSane verifies that no active broadcasts have future timestamps.
func checkTimestampsSane(channel string) InvariantResult {
	active, err := readActive(channel)
	if err != nil {
		return InvariantResult{
			Name:     "timestamps_sane",
			Passed:   true,
			Message:  "cannot read active broadcasts",
			Category: "protocol",
			Severity: "info",
		}
	}

	now := float64(time.Now().Unix())
	var future []string
	for _, b := range active {
		if b.Ts > now+60 { // allow 60s clock skew
			future = append(future, fmt.Sprintf("%s (%.0fs in future)", b.ID, b.Ts-now))
		}
	}

	if len(future) > 0 {
		return InvariantResult{
			Name:     "timestamps_sane",
			Passed:   false,
			Message:  fmt.Sprintf("future timestamps: %s", strings.Join(future, "; ")),
			Category: "protocol",
			Severity: "error",
		}
	}
	return InvariantResult{
		Name:     "timestamps_sane",
		Passed:   true,
		Message:  "all timestamps are sane",
		Category: "protocol",
		Severity: "info",
	}
}

// checkAllPathsRelativeInActive scans all active broadcasts for absolute paths.
func checkAllPathsRelativeInActive(channel string) InvariantResult {
	active, err := readActive(channel)
	if err != nil {
		return InvariantResult{
			Name:     "all_paths_relative",
			Passed:   true,
			Message:  "cannot read active broadcasts",
			Category: "protocol",
			Severity: "info",
		}
	}

	var violations []string
	for _, b := range active {
		for _, f := range b.Files {
			if filepath.IsAbs(f) {
				violations = append(violations, fmt.Sprintf("%s in broadcast %s", f, b.ID))
			}
		}
	}

	if len(violations) > 0 {
		return InvariantResult{
			Name:     "all_paths_relative",
			Passed:   false,
			Message:  fmt.Sprintf("absolute paths in broadcasts: %s", strings.Join(violations, "; ")),
			Category: "protocol",
			Severity: "error",
		}
	}
	return InvariantResult{
		Name:     "all_paths_relative",
		Passed:   true,
		Message:  "all broadcast paths are relative",
		Category: "protocol",
		Severity: "info",
	}
}

// --- Running invariants ---

// runSelfChecks runs Layer A invariants for a broadcast about to be written.
func runSelfChecks(b Broadcast) []InvariantResult {
	var results []InvariantResult
	if len(b.Files) > 0 {
		results = append(results, checkFilesExist(b.Files))
		results = append(results, checkPathsRelative(b.Files))
	}
	results = append(results, checkGitBranchMatches(b.Worktree))
	results = append(results, checkPhaseValid(b.Phase))
	results = append(results, checkTTLReasonable(b.TTL))
	return results
}

// runWorldChecks runs Layer B invariants about the environment.
func runWorldChecks(agentAddress string, channel string) []InvariantResult {
	var results []InvariantResult
	results = append(results, checkBranchNotDiverged())
	results = append(results, checkNoGhostBroadcasts(agentAddress, channel))
	results = append(results, checkDiskSpaceOK())
	return results
}

// runProtocolChecks runs Layer C invariants about the protocol.
func runProtocolChecks(channel string) []InvariantResult {
	var results []InvariantResult
	results = append(results, checkULIDUnique(channel))
	results = append(results, checkNoDuplicateActive(channel))
	results = append(results, checkTimestampsSane(channel))
	results = append(results, checkAllPathsRelativeInActive(channel))
	return results
}

// runAllChecks runs all invariant layers.
func runAllChecks(agentAddress string, channel string) []InvariantResult {
	// For "all", we create a minimal broadcast to run self-checks against
	// the current state. Self-checks also run contextually via --validate.
	var results []InvariantResult
	results = append(results, runWorldChecks(agentAddress, channel)...)
	results = append(results, runProtocolChecks(channel)...)
	return results
}

// printInvariantResults prints results in human-readable or JSON format.
func printInvariantResults(results []InvariantResult, asJSON bool) {
	if asJSON {
		data, _ := json.MarshalIndent(results, "", "  ")
		fmt.Println(string(data))
		return
	}

	for _, r := range results {
		icon := "+"
		if !r.Passed {
			switch r.Severity {
			case "error":
				icon = "x"
			case "warning":
				icon = "!"
			default:
				icon = "?"
			}
		}
		fmt.Printf("  %s [%s/%s] %s: %s\n", icon, r.Category, r.Severity, r.Name, r.Message)
	}
}

// countFailures returns the number of failed invariants by severity.
func countFailures(results []InvariantResult) (errors, warnings int) {
	for _, r := range results {
		if !r.Passed {
			switch r.Severity {
			case "error":
				errors++
			default:
				warnings++
			}
		}
	}
	return
}

// ---------- Sandbox detection ----------

// Sandbox holds worktree context detected from the current git state.
type Sandbox struct {
	Branch           string
	Remote           string
	WorktreePath     string
	IsLinkedWorktree bool
	AgentAddress     string
}

// detectSandbox inspects the current git repository to determine the
// agent's spatial identity (sb primitive).
func detectSandbox() Sandbox {
	sb := Sandbox{Branch: "unknown", Remote: "local"}

	git := func(args ...string) string {
		cmd := exec.Command("git", args...)
		out, err := cmd.Output()
		if err != nil {
			return ""
		}
		return strings.TrimSpace(string(out))
	}

	if branch := git("rev-parse", "--abbrev-ref", "HEAD"); branch != "" {
		sb.Branch = branch
	}

	remoteURL := git("remote", "get-url", "origin")
	commonDir := git("rev-parse", "--git-common-dir")
	gitDir := git("rev-parse", "--git-dir")

	sb.IsLinkedWorktree = commonDir != "" && gitDir != "" && commonDir != gitDir

	if remoteURL != "" {
		remote := remoteURL
		remote = strings.Replace(remote, "git@github.com:", "github.com/", 1)
		remote = strings.Replace(remote, "https://github.com/", "github.com/", 1)
		remote = strings.TrimSuffix(remote, ".git")
		sb.Remote = remote
	}

	if sb.IsLinkedWorktree {
		sb.AgentAddress = sb.Remote + "/worktrees/" + sb.Branch
	} else {
		sb.AgentAddress = sb.Remote + "/" + sb.Branch
	}

	if cwd, err := os.Getwd(); err == nil {
		sb.WorktreePath = cwd
	}

	return sb
}

// ---------- Storage ----------

// aqHome returns the root directory for aq state.
// Priority: AQ_HOME env > .aq/ in cwd (local-first, like cprr) > ~/.aq/
func aqHome() string {
	if env := os.Getenv("AQ_HOME"); env != "" {
		return env
	}
	// Local-first: check .aq/ in current working directory.
	// This enables per-repo isolation (same pattern as cprr).
	if info, err := os.Stat(".aq"); err == nil && info.IsDir() {
		if abs, err := filepath.Abs(".aq"); err == nil {
			return abs
		}
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(".", ".aq")
	}
	return filepath.Join(home, ".aq")
}

// channelPath returns the directory for the named channel.
func channelPath(channel string) string {
	return filepath.Join(aqHome(), "channels", channel)
}

// requestsPath returns the requests subdirectory for a channel.
func requestsPath(channel string) string {
	return filepath.Join(channelPath(channel), "requests")
}

// archivePath returns the archive subdirectory for a channel.
func archivePath(channel string) string {
	return filepath.Join(channelPath(channel), "archive")
}

// ensureDirs creates the directory structure under AQ_HOME.
func ensureDirs(channel string) error {
	dirs := []string{
		requestsPath(channel),
		archivePath(channel),
		filepath.Join(aqHome(), "agents"),
		filepath.Join(aqHome(), "logs"),
	}
	for _, d := range dirs {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return fmt.Errorf("failed to create %s: %w", d, err)
		}
	}
	return nil
}

// ---------- Broadcast I/O ----------

// writeBroadcast writes a broadcast payload to the channel's requests
// directory. The filename format matches the Python prototype:
// aq-{ts14d}-{id}.json
func writeBroadcast(b Broadcast, channel string) (string, error) {
	if err := ensureDirs(channel); err != nil {
		return "", err
	}
	ts := fmt.Sprintf("%014d", int64(b.Ts))
	filename := fmt.Sprintf("aq-%s-%s.json", ts, b.ID)
	path := filepath.Join(requestsPath(channel), filename)

	data, err := b.ToJSON()
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(path, []byte(data+"\n"), 0o644); err != nil {
		return "", err
	}
	return path, nil
}

// readActive scans the channel's requests directory and returns all
// non-expired broadcasts. Expired broadcasts are moved to archive.
// Malformed files are silently skipped.
func readActive(channel string) ([]Broadcast, error) {
	reqDir := requestsPath(channel)
	if _, err := os.Stat(reqDir); os.IsNotExist(err) {
		return nil, nil
	}

	entries, err := os.ReadDir(reqDir)
	if err != nil {
		return nil, err
	}

	// Sort by name (which embeds timestamp).
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].Name() < entries[j].Name()
	})

	var active []Broadcast
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		if !strings.HasPrefix(entry.Name(), "aq-") || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}

		path := filepath.Join(reqDir, entry.Name())
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}

		var b Broadcast
		if err := json.Unmarshal([]byte(strings.TrimSpace(string(data))), &b); err != nil {
			// Malformed JSON — skip without error.
			continue
		}

		if b.IsExpired() {
			// Move to archive. Use rename-or-skip to handle concurrent
			// readers: if another process already archived this file,
			// the rename returns an error (ENOENT) and we skip it.
			archDir := archivePath(channel)
			_ = os.MkdirAll(archDir, 0o755)
			if err := os.Rename(path, filepath.Join(archDir, entry.Name())); err != nil {
				continue // Already archived by another reader.
			}
		} else {
			active = append(active, b)
		}
	}
	return active, nil
}

// ---------- Conflict detection ----------

// severityRank maps Severity to sort order (lower = more severe).
func severityRank(s Severity) int {
	switch s {
	case SeverityHigh:
		return 0
	case SeverityMedium:
		return 1
	case SeverityLow:
		return 2
	default:
		return 3
	}
}

// checkConflicts compares a broadcast against all active broadcasts on
// the channel, returning conflict signals sorted by severity (HIGH first).
func checkConflicts(me Broadcast, channel string) ([]ConflictSignal, error) {
	active, err := readActive(channel)
	if err != nil {
		return nil, err
	}

	var signals []ConflictSignal
	for _, other := range active {
		if other.Agent == me.Agent {
			continue
		}
		// Skip agents that have announced they're done — their broadcast
		// is still active (not expired) but should not trigger conflicts.
		if other.Status == StatusDone {
			continue
		}

		// Compute shared files.
		myFiles := make(map[string]struct{}, len(me.Files))
		for _, f := range me.Files {
			myFiles[f] = struct{}{}
		}
		var shared []string
		for _, f := range other.Files {
			if _, ok := myFiles[f]; ok {
				shared = append(shared, f)
			}
		}
		if len(shared) == 0 {
			continue
		}

		sort.Strings(shared)

		// Severity: three lines, not a table (per originating agent's note).
		bothProof := me.Phase == PhaseProof && other.Phase == PhaseProof
		oneProof := me.Phase == PhaseProof || other.Phase == PhaseProof
		severity := SeverityLow
		if bothProof {
			severity = SeverityHigh
		} else if oneProof {
			severity = SeverityMedium
		}

		signals = append(signals, ConflictSignal{
			A:           me,
			B:           other,
			SharedFiles: shared,
			Severity:    severity,
		})
	}

	sort.Slice(signals, func(i, j int) bool {
		return severityRank(signals[i].Severity) < severityRank(signals[j].Severity)
	})

	return signals, nil
}

// ---------- CLI commands ----------

// announceParams holds the parsed arguments for the announce command.
type announceParams struct {
	conjecture string
	files      string
	claim      string
	phase      string
	status     string
	ttl        int
	validate   bool
	showHelp   bool
}

// consumeArg returns the next argument value and advances the index, or returns
// the fallback if no next argument exists.
func consumeArg(args []string, i *int, fallback string) string {
	if *i+1 < len(args) {
		*i++
		return args[*i]
	}
	return fallback
}

// parseAnnounceArgs parses the argument list for the announce subcommand.
func parseAnnounceArgs(args []string) announceParams {
	p := announceParams{
		phase:  "proof",
		status: "prosecuting",
		ttl:    DefaultTTL,
	}
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "-c", "--conjecture":
			p.conjecture = consumeArg(args, &i, p.conjecture)
		case "-f", "--files":
			p.files = consumeArg(args, &i, p.files)
		case "--claim":
			p.claim = consumeArg(args, &i, p.claim)
		case "--phase":
			p.phase = consumeArg(args, &i, p.phase)
		case "--status":
			p.status = consumeArg(args, &i, p.status)
		case "--ttl":
			fmt.Sscanf(consumeArg(args, &i, ""), "%d", &p.ttl)
		case "--validate":
			p.validate = true
		case "-h", "--help":
			p.showHelp = true
		}
	}
	return p
}

// parseFileList splits a comma-separated file string into a slice, trimming whitespace.
func parseFileList(files string) []string {
	if files == "" {
		return nil
	}
	var result []string
	for _, f := range strings.Split(files, ",") {
		f = strings.TrimSpace(f)
		if f != "" {
			result = append(result, f)
		}
	}
	return result
}

// buildAnnounceBroadcast constructs a Broadcast from parsed announce parameters.
func buildAnnounceBroadcast(p announceParams) Broadcast {
	sb := detectSandbox()
	claim := p.claim
	if claim == "" {
		claim = "working on " + p.conjecture
	}
	b := NewBroadcast()
	b.Agent = sb.AgentAddress
	b.Worktree = sb.Branch
	b.ConjectureID = p.conjecture
	b.ConjectureClaim = claim
	b.Phase = Phase(p.phase)
	b.Status = Status(p.status)
	b.Files = parseFileList(p.files)
	b.TTL = p.ttl
	return b
}

// runPreFlightValidation runs advisory invariant checks and prints results.
func runPreFlightValidation(b Broadcast) {
	results := runSelfChecks(b)
	errs, warns := countFailures(results)
	if !jsonOutput {
		fmt.Println("pre-flight checks:")
		printInvariantResults(results, false)
		if errs > 0 || warns > 0 {
			fmt.Printf("  %d error(s), %d warning(s) — announcing anyway (gossip is advisory)\n", errs, warns)
		}
		fmt.Println()
	}
}

// printAnnounceHelp prints the usage text for the announce subcommand.
func printAnnounceHelp() {
	fmt.Print(`aq announce — broadcast presence

Usage: aq announce -c <conjecture> [options]

Options:
  -c, --conjecture <id>    Conjecture ID (required)
  -f, --files <list>       Comma-separated file list
  --claim <text>           Human-readable claim
  --phase <phase>          conjecture|proof|refutation|refinement (default: proof)
  --status <status>        prosecuting|done|blocked (default: prosecuting)
  --ttl <seconds>          Time to live (default: 3600)
  --validate               Run pre-flight invariant checks (advisory, never blocks)
  -h, --help               Show this help
`)
}

// cmdAnnounce broadcasts presence with the given parameters.
func cmdAnnounce(args []string) int {
	p := parseAnnounceArgs(args)

	if p.showHelp {
		printAnnounceHelp()
		return 0
	}

	if p.conjecture == "" {
		fmt.Fprintln(os.Stderr, "error: --conjecture (-c) is required")
		return 1
	}

	b := buildAnnounceBroadcast(p)

	if p.validate {
		runPreFlightValidation(b)
	}

	path, err := writeBroadcast(b, channelName)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}

	if jsonOutput {
		j, _ := b.ToJSON()
		fmt.Println(j)
	} else {
		fmt.Printf("announced: %s -> %s\n", b.ConjectureID, filepath.Base(path))
	}
	return 0
}

// cmdWhisper is like announce but with a short TTL (60s).
func cmdWhisper(args []string) int {
	// Inject --ttl 60 into args if not already specified.
	hasTTL := false
	for _, a := range args {
		if a == "--ttl" {
			hasTTL = true
			break
		}
	}
	if !hasTTL {
		args = append(args, "--ttl", fmt.Sprintf("%d", WhisperTTL))
	}
	return cmdAnnounce(args)
}

// checkParams holds the parsed arguments for the check command.
type checkParams struct {
	conjecture string
	files      string
	phase      string
	showHelp   bool
}

// parseCheckArgs parses the argument list for the check subcommand.
func parseCheckArgs(args []string) checkParams {
	p := checkParams{
		phase: "proof",
	}
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "-c", "--conjecture":
			p.conjecture = consumeArg(args, &i, p.conjecture)
		case "-f", "--files":
			p.files = consumeArg(args, &i, p.files)
		case "--phase":
			p.phase = consumeArg(args, &i, p.phase)
		case "-h", "--help":
			p.showHelp = true
		}
	}
	return p
}

// buildCheckBroadcast constructs a Broadcast representing the current agent for conflict checking.
func buildCheckBroadcast(p checkParams) Broadcast {
	conjecture := p.conjecture
	if conjecture == "" {
		conjecture = "C-?"
	}
	sb := detectSandbox()
	me := NewBroadcast()
	me.Agent = sb.AgentAddress
	me.Worktree = sb.Branch
	me.ConjectureID = conjecture
	me.Phase = Phase(p.phase)
	me.Status = StatusProsecuting
	me.Files = parseFileList(p.files)
	return me
}

// outputConflictSignals prints conflict signals and returns the appropriate exit code.
func outputConflictSignals(signals []ConflictSignal) int {
	if len(signals) == 0 {
		if jsonOutput {
			fmt.Println("[]")
		} else {
			fmt.Println("no conflicts detected")
		}
		return 0
	}

	if jsonOutput {
		data, _ := json.MarshalIndent(signals, "", "  ")
		fmt.Println(string(data))
	} else {
		for _, s := range signals {
			fmt.Println(s.Summary())
		}
	}

	for _, s := range signals {
		if s.Severity == SeverityHigh {
			return 1
		}
	}
	return 0
}

// cmdCheck checks for conflicts against active broadcasts.
func cmdCheck(args []string) int {
	p := parseCheckArgs(args)

	if p.showHelp {
		fmt.Print(`aq check — check for conflicts with active broadcasts

Usage: aq check [options]

Options:
  -c, --conjecture <id>    Conjecture ID (default: C-?)
  -f, --files <list>       Comma-separated file list
  --phase <phase>          conjecture|proof|refutation|refinement (default: proof)
  -h, --help               Show this help
`)
		return 0
	}

	me := buildCheckBroadcast(p)

	signals, err := checkConflicts(me, channelName)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}

	return outputConflictSignals(signals)
}

// cmdStatus lists active broadcasts.
func cmdStatus(args []string) int {
	for _, a := range args {
		if a == "-h" || a == "--help" {
			fmt.Print(`aq status — list active broadcasts

Usage: aq status [options]

Options:
  --json       JSON output
  -h, --help   Show this help
`)
			return 0
		}
	}

	active, err := readActive(channelName)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}

	if jsonOutput {
		data, _ := json.MarshalIndent(active, "", "  ")
		fmt.Println(string(data))
		return 0
	}

	if len(active) == 0 {
		fmt.Println("no active broadcasts")
		return 0
	}

	for _, b := range active {
		fmt.Printf("  %-50s  %s  [%s]  %s\n",
			b.Agent, b.ConjectureID, b.Phase, strings.Join(b.Files, ", "))
	}
	return 0
}

// cmdInit creates the aq directory structure.
func cmdInit(args []string) int {
	for _, a := range args {
		if a == "-h" || a == "--help" {
			fmt.Print(`aq init — create ~/.aq directory structure

Usage: aq init

Creates the channel directories, agent registry, and logs.
`)
			return 0
		}
	}

	if err := ensureDirs(channelName); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}

	// Write config.json.
	configPath := filepath.Join(aqHome(), "config.json")
	config := map[string]interface{}{
		"version":         "0.1.0",
		"default_channel": DefaultChannel,
		"default_ttl":     DefaultTTL,
	}
	data, _ := json.MarshalIndent(config, "", "  ")
	if err := os.WriteFile(configPath, data, 0o644); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}

	fmt.Printf("aq initialized at %s\n", aqHome())

	// Install git hooks if we're in a git repo.
	if _, err := exec.Command("git", "rev-parse", "--git-dir").Output(); err == nil {
		installGitHooks()
	}

	return 0
}

// installGitHooks writes pre-commit and post-commit hooks for auto-announce.
// Advisory only: hooks exit 0 if aq is not installed.
func installGitHooks() {
	hooksDir := ".githooks"
	if err := os.MkdirAll(hooksDir, 0o755); err != nil {
		return
	}

	preCommit := "#!/bin/sh\n" +
		"# aq pre-commit hook — auto-announce on commit\n" +
		"AQ=$(command -v aq 2>/dev/null || { [ -x \"./aq\" ] && echo \"./aq\"; } || exit 0)\n" +
		"FILES=$(git diff --cached --name-only --diff-filter=ACMR | paste -sd, -)\n" +
		"[ -z \"$FILES\" ] && exit 0\n" +
		"BRANCH=$(git rev-parse --abbrev-ref HEAD 2>/dev/null || echo \"unknown\")\n" +
		"CONJECTURE=$(echo \"$BRANCH\" | grep -oE 'C-[0-9]+' || echo \"C-0\")\n" +
		"$AQ announce -c \"$CONJECTURE\" --claim \"committing\" --phase proof -f \"$FILES\" --status prosecuting 2>/dev/null || true\n" +
		"exit 0\n"

	postCommit := "#!/bin/sh\n" +
		"# aq post-commit hook — announce completion\n" +
		"AQ=$(command -v aq 2>/dev/null || { [ -x \"./aq\" ] && echo \"./aq\"; } || exit 0)\n" +
		"FILES=$(git diff-tree --no-commit-id --name-only -r HEAD | paste -sd, -)\n" +
		"[ -z \"$FILES\" ] && exit 0\n" +
		"BRANCH=$(git rev-parse --abbrev-ref HEAD 2>/dev/null || echo \"unknown\")\n" +
		"CONJECTURE=$(echo \"$BRANCH\" | grep -oE 'C-[0-9]+' || echo \"C-0\")\n" +
		"MSG=$(git log --format=%s -1 HEAD)\n" +
		"$AQ announce -c \"$CONJECTURE\" --claim \"$MSG\" --phase proof -f \"$FILES\" --status done 2>/dev/null || true\n" +
		"exit 0\n"

	hooks := map[string]string{"pre-commit": preCommit, "post-commit": postCommit}
	wrote := false
	for name, content := range hooks {
		path := filepath.Join(hooksDir, name)
		if _, err := os.Stat(path); err == nil {
			continue // don't overwrite existing hooks
		}
		if err := os.WriteFile(path, []byte(content), 0o755); err == nil {
			wrote = true
		}
	}

	if wrote {
		exec.Command("git", "config", "core.hooksPath", hooksDir).Run()
		fmt.Println("git hooks installed at .githooks/ (auto-announce on commit)")
	}
}

// cmdDoctor runs health checks on the aq installation.
func cmdDoctor(args []string) int {
	for _, a := range args {
		if a == "-h" || a == "--help" {
			fmt.Print(`aq doctor — health check

Usage: aq doctor

Verifies AQ_HOME, channels, config, broadcasts, and tools.
`)
			return 0
		}
	}

	home := aqHome()
	var errs, warns int

	// Core paths.
	if info, err := os.Stat(home); err != nil || !info.IsDir() {
		fmt.Printf("FAIL  home       %s (run: aq init)\n", home)
		errs++
	} else {
		fmt.Printf("ok    home       %s\n", home)
	}

	ch := channelPath(channelName)
	if info, err := os.Stat(ch); err != nil || !info.IsDir() {
		fmt.Printf("FAIL  channel    %s not found\n", channelName)
		errs++
	} else {
		fmt.Printf("ok    channel    %s\n", channelName)
	}

	configPath := filepath.Join(home, "config.json")
	if _, err := os.Stat(configPath); err != nil {
		fmt.Printf("warn  config     missing\n")
		warns++
	} else {
		fmt.Printf("ok    config     present\n")
	}

	// Broadcasts.
	active, err := readActive(channelName)
	if err != nil {
		fmt.Printf("warn  broadcasts read error\n")
		warns++
	} else {
		fmt.Printf("ok    broadcasts %d active\n", len(active))
	}

	// Agent identity.
	sb := detectSandbox()
	fmt.Printf("ok    agent      %s\n", sb.AgentAddress)

	// Ecosystem tools: git is required, others optional.
	for _, tool := range []string{"git", "sb", "cprr"} {
		if _, err := exec.LookPath(tool); err != nil {
			if tool == "git" {
				fmt.Printf("FAIL  tool       %s not found\n", tool)
				errs++
			} else {
				fmt.Printf("warn  tool       %s not found\n", tool)
				warns++
			}
		} else {
			fmt.Printf("ok    tool       %s\n", tool)
		}
	}

	// Summary.
	if errs > 0 {
		fmt.Printf("\n%d error(s), %d warning(s)\n", errs, warns)
		return 1
	}
	if warns > 0 {
		fmt.Printf("\nall ok, %d warning(s)\n", warns)
	} else {
		fmt.Printf("\nall ok\n")
	}
	return 0
}

// cmdQuickstart dumps agent-consumable context.
func cmdQuickstart(args []string) int {
	for _, a := range args {
		if a == "-h" || a == "--help" {
			fmt.Print(`aq quickstart — agent-consumable context dump

Usage: aq quickstart
`)
			return 0
		}
	}

	sb := detectSandbox()

	active, _ := readActive(channelName)
	var highCount, medCount int
	for _, b := range active {
		me := Broadcast{Agent: sb.AgentAddress, Phase: PhaseProof, Files: b.Files}
		signals, _ := checkConflicts(me, channelName)
		for _, s := range signals {
			if s.Severity == SeverityHigh {
				highCount++
			} else if s.Severity == SeverityMedium {
				medCount++
			}
		}
	}

	fmt.Printf(`aq — ambient agent queue (L1.5 gossip layer)

## Status
AQ_HOME: %s
Active broadcasts: %d
Conflicts detected: %d HIGH, %d MEDIUM

`, aqHome(), len(active), highCount, medCount)

	if len(active) > 0 {
		fmt.Println("## Active Broadcasts")
		for _, b := range active {
			fmt.Printf("  %-40s  %s  [%s]  %s\n",
				b.Agent, b.ConjectureID, b.Phase, strings.Join(b.Files, ", "))
		}
		fmt.Println()
	}

	fmt.Printf(`## Quick Commands
  aq announce -c C-1 -f "main.go"     # Broadcast presence
  aq whisper -c C-1 -f "main.go"      # Low-priority broadcast (60s TTL)
  aq check -c C-1 -f "main.go"        # Check for conflicts
  aq status                            # List active broadcasts
  aq status --json                     # Machine-readable output

## Three-Primitive Interlock
  sb detect    -> where am I?     (worktree identity)
  cprr list    -> why am I here?  (conjecture context)
  aq status    -> who else knows? (peer broadcasts)

## Ecosystem
  aq version:   %s (commit: %s)
  agent:        %s
`, Version, GitCommit, sb.AgentAddress)
	return 0
}

// cmdValidate runs invariant checks across all three layers.
func cmdValidate(args []string) int {
	var category string

	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--category":
			if i+1 < len(args) {
				category = args[i+1]
				i++
			}
		case "-h", "--help":
			fmt.Print(`aq validate — run invariant checks

Usage: aq validate [options]

Runs advisory invariant checks across three layers:
  self      — verify broadcast claims against reality
  world     — check if the environment has changed
  protocol  — verify protocol structural properties

Invariants are advisory. They warn but never block operations.
Gossip without invariants is rumor; gossip with invariants is intelligence.

Options:
  --category <cat>   Run only checks for: self, world, protocol (default: all)
  --json             JSON output
  -h, --help         Show this help
`)
			return 0
		}
	}

	sb := detectSandbox()

	var results []InvariantResult

	switch category {
	case "self":
		if !jsonOutput {
			fmt.Println("aq validate — self-checks (what would a broadcast claim?)")
			fmt.Println()
		}
		b := NewBroadcast()
		b.Agent = sb.AgentAddress
		b.Worktree = sb.Branch
		b.Phase = PhaseProof
		results = runSelfChecks(b)
	case "world":
		if !jsonOutput {
			fmt.Println("aq validate — world-checks (has reality changed?)")
			fmt.Println()
		}
		results = runWorldChecks(sb.AgentAddress, channelName)
	case "protocol":
		if !jsonOutput {
			fmt.Println("aq validate — protocol-checks (is the system healthy?)")
			fmt.Println()
		}
		results = runProtocolChecks(channelName)
	case "":
		if !jsonOutput {
			fmt.Println("aq validate — all invariant checks")
			fmt.Println()
		}
		results = runAllChecks(sb.AgentAddress, channelName)
	default:
		fmt.Fprintf(os.Stderr, "error: unknown category %q (use: self, world, protocol)\n", category)
		return 1
	}

	printInvariantResults(results, jsonOutput)

	errs, warns := countFailures(results)
	if !jsonOutput {
		fmt.Println()
		fmt.Printf("  %d passed, %d warning(s), %d error(s)\n",
			len(results)-errs-warns, warns, errs)
	}

	// Exit 1 only on errors, not warnings. Warnings are expected in gossip.
	if errs > 0 {
		return 1
	}
	return 0
}

// ---------- Global flag parsing ----------

func parseGlobalFlags(args []string) []string {
	channelName = DefaultChannel
	var remaining []string

	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--json":
			jsonOutput = true
		case "--channel":
			if i+1 < len(args) {
				channelName = args[i+1]
				i++
			}
		default:
			remaining = append(remaining, args[i])
		}
	}
	return remaining
}

// ---------- Main ----------

func main() {
	args := parseGlobalFlags(os.Args[1:])

	if len(args) == 0 {
		printUsage()
		os.Exit(0)
	}

	var code int
	switch args[0] {
	case "announce", "ann", "a":
		code = cmdAnnounce(args[1:])
	case "whisper":
		code = cmdWhisper(args[1:])
	case "check":
		code = cmdCheck(args[1:])
	case "status", "ls":
		code = cmdStatus(args[1:])
	case "init":
		code = cmdInit(args[1:])
	case "doctor":
		code = cmdDoctor(args[1:])
	case "quickstart", "prime":
		code = cmdQuickstart(args[1:])
	case "validate":
		code = cmdValidate(args[1:])
	case "version", "--version", "-v":
		fmt.Printf("aq %s (commit: %s, built: %s)\n", Version, GitCommit, BuildDate)
	case "help", "--help", "-h":
		printUsage()
	default:
		fmt.Fprintf(os.Stderr, "aq: unknown command '%s'\n", args[0])
		fmt.Fprintln(os.Stderr, "Run 'aq help' for usage.")
		code = 1
	}

	os.Exit(code)
}

func printUsage() {
	fmt.Print(`aq — ambient agent queue (gossip layer for multi-agent development)

Usage: aq <command> [options]

Broadcast:
  announce, ann, a   Broadcast presence (high priority, TTL 300s)
  whisper            Broadcast presence (low priority, TTL 60s)

Query:
  check              Check for conflicts with active broadcasts
  status, ls         List active broadcasts

Operational:
  init               Create ~/.aq directory structure
  doctor             Health check
  validate           Run invariant checks (advisory, never blocks)
  quickstart, prime  Agent-consumable context dump
  version            Show version info
  help               Show this help

Global flags:
  --json             Machine-readable JSON output
  --channel <name>   Channel name (default: broadcast)

Examples:
  aq announce -c C-1 -f "auth.py,session.py"
  aq whisper -c C-1 -f "readme.md"
  aq check -c C-2 -f "auth.py"
  aq status --json

Gossip, not coordination. Broadcasts expire. Silence is normal.
`)
}
