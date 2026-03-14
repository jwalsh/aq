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
const (
	DefaultTTL     = 300
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

// Broadcast is the ambient presence payload. Lifecycle:
//  1. announce() before touching files
//  2. re-announce every TTL/2 while working
//  3. announce(status="done") when finished
type Broadcast struct {
	Agent           string   `json:"agent"`
	Worktree        string   `json:"worktree"`
	ConjectureID    string   `json:"conjecture_id"`
	ConjectureClaim string   `json:"conjecture_claim"`
	Phase           string   `json:"phase"`
	Status          string   `json:"status"`
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
	Severity    string    `json:"severity"` // low | medium | high
}

// Summary returns a human-readable one-liner for the conflict.
func (c *ConflictSignal) Summary() string {
	files := strings.Join(c.SharedFiles, ", ")
	return fmt.Sprintf("[%s] %s (%s) <-> %s (%s) -- shared: %s",
		strings.ToUpper(c.Severity),
		c.A.Agent, c.A.ConjectureID,
		c.B.Agent, c.B.ConjectureID,
		files)
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
			// Move to archive.
			archDir := archivePath(channel)
			_ = os.MkdirAll(archDir, 0o755)
			_ = os.Rename(path, filepath.Join(archDir, entry.Name()))
		} else {
			active = append(active, b)
		}
	}
	return active, nil
}

// ---------- Conflict detection ----------

// severityRank maps severity strings to sort order (lower = more severe).
func severityRank(s string) int {
	switch s {
	case "high":
		return 0
	case "medium":
		return 1
	case "low":
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
		bothProof := me.Phase == "proof" && other.Phase == "proof"
		oneProof := me.Phase == "proof" || other.Phase == "proof"
		severity := "low"
		if bothProof {
			severity = "high"
		} else if oneProof {
			severity = "medium"
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

// cmdAnnounce broadcasts presence with the given parameters.
func cmdAnnounce(args []string) int {
	var (
		conjecture string
		files      string
		claim      string
		phase      = "proof"
		status     = "prosecuting"
		ttl        = DefaultTTL
	)

	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "-c", "--conjecture":
			if i+1 < len(args) {
				conjecture = args[i+1]
				i++
			}
		case "-f", "--files":
			if i+1 < len(args) {
				files = args[i+1]
				i++
			}
		case "--claim":
			if i+1 < len(args) {
				claim = args[i+1]
				i++
			}
		case "--phase":
			if i+1 < len(args) {
				phase = args[i+1]
				i++
			}
		case "--status":
			if i+1 < len(args) {
				status = args[i+1]
				i++
			}
		case "--ttl":
			if i+1 < len(args) {
				fmt.Sscanf(args[i+1], "%d", &ttl)
				i++
			}
		case "-h", "--help":
			fmt.Print(`aq announce — broadcast presence

Usage: aq announce -c <conjecture> [options]

Options:
  -c, --conjecture <id>    Conjecture ID (required)
  -f, --files <list>       Comma-separated file list
  --claim <text>           Human-readable claim
  --phase <phase>          conjecture|proof|refutation|refinement (default: proof)
  --status <status>        prosecuting|done|blocked (default: prosecuting)
  --ttl <seconds>          Time to live (default: 300)
  -h, --help               Show this help
`)
			return 0
		}
	}

	if conjecture == "" {
		fmt.Fprintln(os.Stderr, "error: --conjecture (-c) is required")
		return 1
	}

	if claim == "" {
		claim = "working on " + conjecture
	}

	var fileList []string
	if files != "" {
		for _, f := range strings.Split(files, ",") {
			f = strings.TrimSpace(f)
			if f != "" {
				fileList = append(fileList, f)
			}
		}
	}

	sb := detectSandbox()

	b := NewBroadcast()
	b.Agent = sb.AgentAddress
	b.Worktree = sb.Branch
	b.ConjectureID = conjecture
	b.ConjectureClaim = claim
	b.Phase = phase
	b.Status = status
	b.Files = fileList
	b.TTL = ttl

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

// cmdCheck checks for conflicts against active broadcasts.
func cmdCheck(args []string) int {
	var (
		conjecture string
		files      string
		phase      = "proof"
	)

	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "-c", "--conjecture":
			if i+1 < len(args) {
				conjecture = args[i+1]
				i++
			}
		case "-f", "--files":
			if i+1 < len(args) {
				files = args[i+1]
				i++
			}
		case "--phase":
			if i+1 < len(args) {
				phase = args[i+1]
				i++
			}
		case "-h", "--help":
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
	}

	if conjecture == "" {
		conjecture = "C-?"
	}

	var fileList []string
	if files != "" {
		for _, f := range strings.Split(files, ",") {
			f = strings.TrimSpace(f)
			if f != "" {
				fileList = append(fileList, f)
			}
		}
	}

	sb := detectSandbox()

	me := NewBroadcast()
	me.Agent = sb.AgentAddress
	me.Worktree = sb.Branch
	me.ConjectureID = conjecture
	me.Phase = phase
	me.Status = "prosecuting"
	me.Files = fileList

	signals, err := checkConflicts(me, channelName)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}

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
		if s.Severity == "high" {
			return 1
		}
	}
	return 0
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
	return 0
}

// cmdDoctor runs health checks on the aq installation.
func cmdDoctor(args []string) int {
	for _, a := range args {
		if a == "-h" || a == "--help" {
			fmt.Print(`aq doctor — health check

Usage: aq doctor

Checks AQ_HOME, channel directories, config, active broadcasts,
and ecosystem tool availability.
`)
			return 0
		}
	}

	fmt.Println("aq doctor — health check")
	fmt.Println()

	home := aqHome()
	var warnings, errors int

	// AQ_HOME exists.
	if info, err := os.Stat(home); err != nil || !info.IsDir() {
		fmt.Printf("x AQ_HOME: %s does not exist\n", home)
		fmt.Println("  Fix: aq init")
		errors++
	} else {
		fmt.Printf("+ AQ_HOME: %s\n", home)
	}

	// Channel directory.
	ch := channelPath(channelName)
	if info, err := os.Stat(ch); err != nil || !info.IsDir() {
		fmt.Printf("x Channel '%s': not found\n", channelName)
		errors++
	} else {
		fmt.Printf("+ Channel '%s': %s\n", channelName, ch)
	}

	// Config file.
	configPath := filepath.Join(home, "config.json")
	if _, err := os.Stat(configPath); err != nil {
		fmt.Println("! Config: config.json missing")
		warnings++
	} else {
		fmt.Println("+ Config: config.json present")
	}

	// Active broadcasts.
	active, err := readActive(channelName)
	if err != nil {
		fmt.Printf("! Active broadcasts: error reading (%v)\n", err)
		warnings++
	} else {
		fmt.Printf("+ Active broadcasts: %d\n", len(active))
	}

	// Sandbox detection.
	sb := detectSandbox()
	fmt.Printf("+ Agent address: %s\n", sb.AgentAddress)

	// Ecosystem tools.
	for _, tool := range []string{"git", "sb", "cprr"} {
		cmd := exec.Command(tool, "version")
		out, err := cmd.Output()
		if err != nil {
			fmt.Printf("! %s: not found\n", tool)
			if tool == "git" {
				errors++
			} else {
				warnings++
			}
		} else {
			fmt.Printf("+ %s: %s\n", tool, strings.TrimSpace(string(out)))
		}
	}

	fmt.Println()
	fmt.Printf("Overall: %d warning(s), %d error(s)\n", warnings, errors)

	if errors > 0 {
		return 1
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
		me := Broadcast{Agent: sb.AgentAddress, Phase: "proof", Files: b.Files}
		signals, _ := checkConflicts(me, channelName)
		for _, s := range signals {
			if s.Severity == "high" {
				highCount++
			} else if s.Severity == "medium" {
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
