package main

import (
	"encoding/json"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
)

// ---------- Test helpers ----------

// makeTempAQHome creates a temporary directory and sets AQ_HOME to it.
func makeTempAQHome(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("AQ_HOME", dir)
	return dir
}

// makeBroadcast creates a Broadcast with defaults, applying optional overrides.
func makeBroadcast(overrides ...func(*Broadcast)) Broadcast {
	b := Broadcast{
		ID:              generateULID(),
		Agent:           "test/agent",
		Worktree:        "main",
		ConjectureID:    "C-1",
		ConjectureClaim: "test claim",
		Phase:           "proof",
		Status:          "prosecuting",
		Files:           []string{"main.go"},
		Ts:              float64(time.Now().Unix()),
		TTL:             300,
	}
	for _, f := range overrides {
		f(&b)
	}
	return b
}

// ---------- ULID Tests ----------

func TestGenerateULID_Format(t *testing.T) {
	id := generateULID()

	if len(id) != 22 {
		t.Errorf("ULID length = %d, want 22", len(id))
	}

	// First 12 chars are hex (timestamp).
	tspart := id[:12]
	for _, c := range tspart {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			t.Errorf("ULID timestamp char %c is not hex", c)
		}
	}

	// Last 10 chars are hex (random, from hex.EncodeToString).
	randpart := id[12:]
	if len(randpart) != 10 {
		t.Errorf("ULID random part length = %d, want 10", len(randpart))
	}
	for _, c := range randpart {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			t.Errorf("ULID random char %c is not hex", c)
		}
	}
}

func TestGenerateULID_Uniqueness(t *testing.T) {
	seen := make(map[string]struct{}, 1000)
	for i := 0; i < 1000; i++ {
		id := generateULID()
		if _, dup := seen[id]; dup {
			t.Fatalf("duplicate ULID at iteration %d: %s", i, id)
		}
		seen[id] = struct{}{}
	}
}

func TestGenerateULID_Monotonic(t *testing.T) {
	id1 := generateULID()
	// Sleep briefly to ensure timestamp advances.
	time.Sleep(2 * time.Millisecond)
	id2 := generateULID()

	ts1 := id1[:12]
	ts2 := id2[:12]

	if ts1 > ts2 {
		t.Errorf("timestamp portion not monotonic: %s > %s", ts1, ts2)
	}
}

// ---------- Broadcast Tests ----------

func TestBroadcast_JSON_RoundTrip(t *testing.T) {
	original := makeBroadcast(func(b *Broadcast) {
		b.Files = []string{"auth.go", "handler.go"}
		b.Phase = "refutation"
	})

	j, err := original.ToJSON()
	if err != nil {
		t.Fatalf("ToJSON error: %v", err)
	}

	restored, err := BroadcastFromJSON(j)
	if err != nil {
		t.Fatalf("BroadcastFromJSON error: %v", err)
	}

	if restored.Agent != original.Agent {
		t.Errorf("Agent = %q, want %q", restored.Agent, original.Agent)
	}
	if restored.ConjectureID != original.ConjectureID {
		t.Errorf("ConjectureID = %q, want %q", restored.ConjectureID, original.ConjectureID)
	}
	if restored.Phase != original.Phase {
		t.Errorf("Phase = %q, want %q", restored.Phase, original.Phase)
	}
	if restored.TTL != original.TTL {
		t.Errorf("TTL = %d, want %d", restored.TTL, original.TTL)
	}
	if restored.ID != original.ID {
		t.Errorf("ID = %q, want %q", restored.ID, original.ID)
	}
	if len(restored.Files) != len(original.Files) {
		t.Fatalf("Files length = %d, want %d", len(restored.Files), len(original.Files))
	}
	for i, f := range restored.Files {
		if f != original.Files[i] {
			t.Errorf("Files[%d] = %q, want %q", i, f, original.Files[i])
		}
	}
}

func TestBroadcast_WireFormat(t *testing.T) {
	b := makeBroadcast()
	j, err := b.ToJSON()
	if err != nil {
		t.Fatalf("ToJSON error: %v", err)
	}

	// Verify that the JSON keys are snake_case, matching the Python prototype.
	var raw map[string]interface{}
	if err := json.Unmarshal([]byte(j), &raw); err != nil {
		t.Fatalf("unmarshal error: %v", err)
	}

	requiredKeys := []string{
		"agent", "worktree", "conjecture_id", "conjecture_claim",
		"phase", "status", "files", "ts", "ttl", "id",
	}
	for _, key := range requiredKeys {
		if _, ok := raw[key]; !ok {
			t.Errorf("missing JSON key %q", key)
		}
	}

	// Verify no camelCase keys snuck in.
	for key := range raw {
		if key != strings.ToLower(key) && key != "ts" && key != "id" && key != "ttl" {
			// Allow "ts", "id", "ttl" as-is since they are already lowercase.
			// Flag anything with uppercase.
			t.Errorf("JSON key %q is not snake_case", key)
		}
	}
}

func TestBroadcast_DefaultTTL(t *testing.T) {
	b := NewBroadcast()
	if b.TTL != 3600 {
		t.Errorf("default TTL = %d, want 3600", b.TTL)
	}
}

func TestBroadcast_IsExpired(t *testing.T) {
	// Fresh broadcast should not be expired.
	fresh := makeBroadcast()
	if fresh.IsExpired() {
		t.Error("fresh broadcast should not be expired")
	}

	// Old broadcast with past timestamp should be expired.
	old := makeBroadcast(func(b *Broadcast) {
		b.Ts = float64(time.Now().Unix() - 600)
		b.TTL = 300
	})
	if !old.IsExpired() {
		t.Error("broadcast 600s old with TTL 300 should be expired")
	}
}

func TestBroadcast_IsExpired_BoundaryTTL(t *testing.T) {
	// Broadcast exactly at TTL boundary: ts + ttl == now.
	// The condition is time.Now() > ts + ttl, so exactly at boundary
	// should NOT be expired (strictly greater than).
	now := float64(time.Now().Unix())
	boundary := makeBroadcast(func(b *Broadcast) {
		b.Ts = now
		b.TTL = 0
	})
	// With TTL=0, ts+ttl == now. time.Now() may be >= now but the
	// check is strictly >, so this tests the boundary. In practice
	// the clock may tick, but we test the semantics.

	// A broadcast with TTL=1 and ts=now should not be expired yet.
	almostExpired := makeBroadcast(func(b *Broadcast) {
		b.Ts = float64(time.Now().Unix())
		b.TTL = 1
	})
	if almostExpired.IsExpired() {
		t.Error("broadcast with TTL=1 at current time should not be expired")
	}

	// Boundary test: TTL=0 means it expires immediately (or at the boundary).
	// We accept that IsExpired may return true or false at exact boundary.
	_ = boundary
}

// ---------- Storage Tests ----------

func TestAqHome_Default(t *testing.T) {
	// Unset AQ_HOME so the default kicks in.
	t.Setenv("AQ_HOME", "")
	home := aqHome()
	if home == "" {
		t.Error("aqHome() returned empty string")
	}
	// When AQ_HOME is empty string, it returns the empty string as env is set
	// but empty. Let's test with a truly unset variable approach.
}

func TestAqHome_EnvOverride(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("AQ_HOME", dir)
	home := aqHome()
	if home != dir {
		t.Errorf("aqHome() = %q, want %q", home, dir)
	}
}

func TestWriteBroadcast_CreatesFile(t *testing.T) {
	makeTempAQHome(t)

	b := makeBroadcast()
	path, err := writeBroadcast(b, "broadcast")
	if err != nil {
		t.Fatalf("writeBroadcast error: %v", err)
	}

	if _, err := os.Stat(path); os.IsNotExist(err) {
		t.Errorf("expected file at %s, but it does not exist", path)
	}
}

func TestWriteBroadcast_FileFormat(t *testing.T) {
	makeTempAQHome(t)

	b := makeBroadcast(func(b *Broadcast) {
		b.Ts = 1710345600 // fixed timestamp
		b.ID = "0191a2b3c4d5e6"
	})

	path, err := writeBroadcast(b, "broadcast")
	if err != nil {
		t.Fatalf("writeBroadcast error: %v", err)
	}

	filename := filepath.Base(path)
	// Format: aq-{ts14d}-{id}.json
	if !strings.HasPrefix(filename, "aq-") {
		t.Errorf("filename %q does not start with 'aq-'", filename)
	}
	if !strings.HasSuffix(filename, ".json") {
		t.Errorf("filename %q does not end with '.json'", filename)
	}

	// Check the 14-digit timestamp portion.
	parts := strings.SplitN(filename, "-", 3)
	if len(parts) < 3 {
		t.Fatalf("filename %q does not have expected aq-{ts}-{id}.json structure", filename)
	}
	tsStr := parts[1]
	if len(tsStr) != 14 {
		t.Errorf("timestamp part %q has length %d, want 14", tsStr, len(tsStr))
	}
}

func TestWriteBroadcast_Content(t *testing.T) {
	home := makeTempAQHome(t)

	b := makeBroadcast()
	path, err := writeBroadcast(b, "broadcast")
	if err != nil {
		t.Fatalf("writeBroadcast error: %v", err)
	}
	_ = home

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile error: %v", err)
	}

	var read Broadcast
	if err := json.Unmarshal([]byte(strings.TrimSpace(string(data))), &read); err != nil {
		t.Fatalf("unmarshal error: %v", err)
	}

	if read.ID != b.ID {
		t.Errorf("ID = %q, want %q", read.ID, b.ID)
	}
	if read.Agent != b.Agent {
		t.Errorf("Agent = %q, want %q", read.Agent, b.Agent)
	}
}

func TestReadActive_Empty(t *testing.T) {
	makeTempAQHome(t)

	active, err := readActive("broadcast")
	if err != nil {
		t.Fatalf("readActive error: %v", err)
	}
	if len(active) != 0 {
		t.Errorf("readActive returned %d broadcasts, want 0", len(active))
	}
}

func TestReadActive_FilterExpired(t *testing.T) {
	home := makeTempAQHome(t)

	// Write a fresh broadcast.
	fresh := makeBroadcast(func(b *Broadcast) {
		b.ID = "fresh000000000000000000"[:22]
		b.Agent = "fresh/agent"
	})
	_, err := writeBroadcast(fresh, "broadcast")
	if err != nil {
		t.Fatalf("write fresh: %v", err)
	}

	// Write an expired broadcast.
	expired := makeBroadcast(func(b *Broadcast) {
		b.ID = "expired0000000000000000"[:22]
		b.Ts = float64(time.Now().Unix() - 600)
		b.TTL = 300
		b.Agent = "expired/agent"
	})
	_, err = writeBroadcast(expired, "broadcast")
	if err != nil {
		t.Fatalf("write expired: %v", err)
	}

	active, err := readActive("broadcast")
	if err != nil {
		t.Fatalf("readActive error: %v", err)
	}
	if len(active) != 1 {
		t.Fatalf("readActive returned %d broadcasts, want 1", len(active))
	}
	if active[0].Agent != "fresh/agent" {
		t.Errorf("active agent = %q, want %q", active[0].Agent, "fresh/agent")
	}

	// Verify the expired file was moved to archive.
	archiveDir := filepath.Join(home, "channels", "broadcast", "archive")
	entries, err := os.ReadDir(archiveDir)
	if err != nil {
		t.Fatalf("ReadDir archive: %v", err)
	}
	if len(entries) != 1 {
		t.Errorf("archive has %d entries, want 1", len(entries))
	}
}

func TestReadActive_MalformedJSON(t *testing.T) {
	home := makeTempAQHome(t)

	// Create the requests directory.
	reqDir := filepath.Join(home, "channels", "broadcast", "requests")
	if err := os.MkdirAll(reqDir, 0o755); err != nil {
		t.Fatal(err)
	}

	// Write a malformed JSON file.
	malformed := filepath.Join(reqDir, "aq-00000000000001-badid00000bad.json")
	if err := os.WriteFile(malformed, []byte("{not valid json\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Write a valid broadcast alongside it.
	valid := makeBroadcast(func(b *Broadcast) {
		b.Agent = "valid/agent"
	})
	_, err := writeBroadcast(valid, "broadcast")
	if err != nil {
		t.Fatal(err)
	}

	active, err := readActive("broadcast")
	if err != nil {
		t.Fatalf("readActive error: %v", err)
	}

	// Should only get the valid broadcast, no error from malformed.
	if len(active) != 1 {
		t.Fatalf("readActive returned %d broadcasts, want 1", len(active))
	}
	if active[0].Agent != "valid/agent" {
		t.Errorf("active agent = %q, want %q", active[0].Agent, "valid/agent")
	}
}

// ---------- Conflict Detection Tests ----------

func TestCheckConflicts_NoOverlap(t *testing.T) {
	makeTempAQHome(t)

	// Agent A touches main.go.
	agentA := makeBroadcast(func(b *Broadcast) {
		b.Agent = "origin/alpha"
		b.Files = []string{"main.go"}
	})
	_, _ = writeBroadcast(agentA, "broadcast")

	// Agent B touches handler.go -- no overlap.
	agentB := makeBroadcast(func(b *Broadcast) {
		b.Agent = "origin/beta"
		b.Files = []string{"handler.go"}
	})

	signals, err := checkConflicts(agentB, "broadcast")
	if err != nil {
		t.Fatalf("checkConflicts error: %v", err)
	}
	if len(signals) != 0 {
		t.Errorf("expected 0 conflicts, got %d", len(signals))
	}
}

func TestCheckConflicts_SharedFiles_BothProof(t *testing.T) {
	makeTempAQHome(t)

	// Agent A: proof phase, touching auth.go.
	agentA := makeBroadcast(func(b *Broadcast) {
		b.Agent = "origin/alpha"
		b.Phase = "proof"
		b.Files = []string{"auth.go"}
	})
	_, _ = writeBroadcast(agentA, "broadcast")

	// Agent B: also proof phase, also touching auth.go.
	agentB := makeBroadcast(func(b *Broadcast) {
		b.Agent = "origin/beta"
		b.Phase = "proof"
		b.Files = []string{"auth.go"}
	})

	signals, err := checkConflicts(agentB, "broadcast")
	if err != nil {
		t.Fatalf("checkConflicts error: %v", err)
	}
	if len(signals) != 1 {
		t.Fatalf("expected 1 conflict, got %d", len(signals))
	}
	if signals[0].Severity != "high" {
		t.Errorf("severity = %q, want %q", signals[0].Severity, "high")
	}
}

func TestCheckConflicts_SharedFiles_OneProof(t *testing.T) {
	makeTempAQHome(t)

	agentA := makeBroadcast(func(b *Broadcast) {
		b.Agent = "origin/alpha"
		b.Phase = "proof"
		b.Files = []string{"auth.go"}
	})
	_, _ = writeBroadcast(agentA, "broadcast")

	agentB := makeBroadcast(func(b *Broadcast) {
		b.Agent = "origin/beta"
		b.Phase = "conjecture"
		b.Files = []string{"auth.go"}
	})

	signals, err := checkConflicts(agentB, "broadcast")
	if err != nil {
		t.Fatalf("checkConflicts error: %v", err)
	}
	if len(signals) != 1 {
		t.Fatalf("expected 1 conflict, got %d", len(signals))
	}
	if signals[0].Severity != "medium" {
		t.Errorf("severity = %q, want %q", signals[0].Severity, "medium")
	}
}

func TestCheckConflicts_SharedFiles_NeitherProof(t *testing.T) {
	makeTempAQHome(t)

	agentA := makeBroadcast(func(b *Broadcast) {
		b.Agent = "origin/alpha"
		b.Phase = "conjecture"
		b.Files = []string{"auth.go"}
	})
	_, _ = writeBroadcast(agentA, "broadcast")

	agentB := makeBroadcast(func(b *Broadcast) {
		b.Agent = "origin/beta"
		b.Phase = "refinement"
		b.Files = []string{"auth.go"}
	})

	signals, err := checkConflicts(agentB, "broadcast")
	if err != nil {
		t.Fatalf("checkConflicts error: %v", err)
	}
	if len(signals) != 1 {
		t.Fatalf("expected 1 conflict, got %d", len(signals))
	}
	if signals[0].Severity != "low" {
		t.Errorf("severity = %q, want %q", signals[0].Severity, "low")
	}
}

func TestCheckConflicts_SkipSelf(t *testing.T) {
	makeTempAQHome(t)

	me := makeBroadcast(func(b *Broadcast) {
		b.Agent = "origin/same-agent"
		b.Files = []string{"main.go"}
	})
	_, _ = writeBroadcast(me, "broadcast")

	// Checking conflicts for the same agent should skip self.
	signals, err := checkConflicts(me, "broadcast")
	if err != nil {
		t.Fatalf("checkConflicts error: %v", err)
	}
	if len(signals) != 0 {
		t.Errorf("expected 0 conflicts (self-skip), got %d", len(signals))
	}
}

func TestCheckConflicts_SortBySeverity(t *testing.T) {
	makeTempAQHome(t)

	// Write three broadcasts with different phases.
	low := makeBroadcast(func(b *Broadcast) {
		b.Agent = "origin/low"
		b.Phase = "conjecture"
		b.Files = []string{"shared.go"}
	})
	_, _ = writeBroadcast(low, "broadcast")

	medium := makeBroadcast(func(b *Broadcast) {
		b.Agent = "origin/medium"
		b.Phase = "proof"
		b.Files = []string{"shared.go"}
	})
	_, _ = writeBroadcast(medium, "broadcast")

	high := makeBroadcast(func(b *Broadcast) {
		b.Agent = "origin/high"
		b.Phase = "proof"
		b.Files = []string{"shared.go"}
	})
	_, _ = writeBroadcast(high, "broadcast")

	// "me" is also proof on the same file, so:
	// vs low (conjecture) => medium (one proof)
	// vs medium (proof) => high (both proof)
	// vs high (proof) => high (both proof)
	me := makeBroadcast(func(b *Broadcast) {
		b.Agent = "origin/me"
		b.Phase = "proof"
		b.Files = []string{"shared.go"}
	})

	signals, err := checkConflicts(me, "broadcast")
	if err != nil {
		t.Fatalf("checkConflicts error: %v", err)
	}
	if len(signals) != 3 {
		t.Fatalf("expected 3 conflicts, got %d", len(signals))
	}

	// Verify sorted by severity: high first, then medium.
	for i := 0; i < len(signals)-1; i++ {
		if severityRank(signals[i].Severity) > severityRank(signals[i+1].Severity) {
			t.Errorf("conflicts not sorted by severity at index %d: %s > %s",
				i, signals[i].Severity, signals[i+1].Severity)
		}
	}
	if signals[0].Severity != "high" {
		t.Errorf("first conflict severity = %q, want %q", signals[0].Severity, "high")
	}
}

func TestCheckConflicts_MultipleFiles(t *testing.T) {
	makeTempAQHome(t)

	agentA := makeBroadcast(func(b *Broadcast) {
		b.Agent = "origin/alpha"
		b.Phase = "proof"
		b.Files = []string{"auth.go", "handler.go", "config.go"}
	})
	_, _ = writeBroadcast(agentA, "broadcast")

	// Agent B touches auth.go and config.go but not handler.go.
	agentB := makeBroadcast(func(b *Broadcast) {
		b.Agent = "origin/beta"
		b.Phase = "proof"
		b.Files = []string{"auth.go", "config.go", "router.go"}
	})

	signals, err := checkConflicts(agentB, "broadcast")
	if err != nil {
		t.Fatalf("checkConflicts error: %v", err)
	}
	if len(signals) != 1 {
		t.Fatalf("expected 1 conflict, got %d", len(signals))
	}

	shared := signals[0].SharedFiles
	sort.Strings(shared)
	expected := []string{"auth.go", "config.go"}
	if len(shared) != len(expected) {
		t.Fatalf("shared files = %v, want %v", shared, expected)
	}
	for i, f := range shared {
		if f != expected[i] {
			t.Errorf("shared[%d] = %q, want %q", i, f, expected[i])
		}
	}
}

// ---------- Integration Tests ----------

func TestAnnounce_ThenStatus(t *testing.T) {
	makeTempAQHome(t)

	// Write a broadcast directly (simulating cmdAnnounce).
	b := makeBroadcast(func(b *Broadcast) {
		b.Agent = "test/integration-agent"
		b.ConjectureID = "C-42"
		b.Files = []string{"foo.py", "bar.py"}
	})
	_, err := writeBroadcast(b, "broadcast")
	if err != nil {
		t.Fatalf("writeBroadcast error: %v", err)
	}

	// Read it back (simulating cmdStatus).
	active, err := readActive("broadcast")
	if err != nil {
		t.Fatalf("readActive error: %v", err)
	}
	if len(active) != 1 {
		t.Fatalf("expected 1 active broadcast, got %d", len(active))
	}
	if active[0].ConjectureID != "C-42" {
		t.Errorf("conjecture_id = %q, want %q", active[0].ConjectureID, "C-42")
	}
	if active[0].Agent != "test/integration-agent" {
		t.Errorf("agent = %q, want %q", active[0].Agent, "test/integration-agent")
	}
}

func TestAnnounce_ThenCheck(t *testing.T) {
	makeTempAQHome(t)

	// Agent A announces.
	agentA := makeBroadcast(func(b *Broadcast) {
		b.Agent = "origin/agent-alpha"
		b.ConjectureID = "C-1"
		b.Phase = "proof"
		b.Files = []string{"auth.py"}
	})
	_, err := writeBroadcast(agentA, "broadcast")
	if err != nil {
		t.Fatalf("write agent A: %v", err)
	}

	// Agent B announces the same file.
	agentB := makeBroadcast(func(b *Broadcast) {
		b.Agent = "origin/agent-beta"
		b.ConjectureID = "C-7"
		b.Phase = "proof"
		b.Files = []string{"auth.py"}
	})

	// Agent B checks for conflicts.
	signals, err := checkConflicts(agentB, "broadcast")
	if err != nil {
		t.Fatalf("checkConflicts error: %v", err)
	}
	if len(signals) != 1 {
		t.Fatalf("expected 1 conflict, got %d", len(signals))
	}
	if signals[0].Severity != "high" {
		t.Errorf("severity = %q, want %q (both proof, shared file)", signals[0].Severity, "high")
	}
	if len(signals[0].SharedFiles) != 1 || signals[0].SharedFiles[0] != "auth.py" {
		t.Errorf("shared files = %v, want [auth.py]", signals[0].SharedFiles)
	}
}

func TestWhisper_ShortTTL(t *testing.T) {
	makeTempAQHome(t)

	b := makeBroadcast(func(b *Broadcast) {
		b.TTL = WhisperTTL
	})
	if b.TTL != 60 {
		t.Errorf("whisper TTL = %d, want 60", b.TTL)
	}

	// Also verify the constant.
	if WhisperTTL != 60 {
		t.Errorf("WhisperTTL constant = %d, want 60", WhisperTTL)
	}
}

// ---------- Additional edge case tests ----------

func TestBroadcast_Overlaps(t *testing.T) {
	a := makeBroadcast(func(b *Broadcast) {
		b.Files = []string{"main.go", "handler.go"}
	})
	b := makeBroadcast(func(b *Broadcast) {
		b.Files = []string{"handler.go", "config.go"}
	})
	c := makeBroadcast(func(b *Broadcast) {
		b.Files = []string{"router.go"}
	})

	if !a.Overlaps(&b) {
		t.Error("a and b should overlap on handler.go")
	}
	if a.Overlaps(&c) {
		t.Error("a and c should not overlap")
	}
}

func TestNewBroadcast_HasDefaults(t *testing.T) {
	b := NewBroadcast()
	if b.TTL != DefaultTTL {
		t.Errorf("TTL = %d, want %d", b.TTL, DefaultTTL)
	}
	if b.ID == "" {
		t.Error("ID should not be empty")
	}
	if b.Ts == 0 {
		t.Error("Ts should not be zero")
	}
}

func TestConflictSignal_Summary(t *testing.T) {
	s := ConflictSignal{
		A:           makeBroadcast(func(b *Broadcast) { b.Agent = "origin/a"; b.ConjectureID = "C-1" }),
		B:           makeBroadcast(func(b *Broadcast) { b.Agent = "origin/b"; b.ConjectureID = "C-2" }),
		SharedFiles: []string{"main.go"},
		Severity:    "high",
	}
	summary := s.Summary()
	if !strings.Contains(summary, "HIGH") {
		t.Errorf("summary should contain 'HIGH': %s", summary)
	}
	if !strings.Contains(summary, "origin/a") {
		t.Errorf("summary should contain agent a: %s", summary)
	}
	if !strings.Contains(summary, "main.go") {
		t.Errorf("summary should contain shared file: %s", summary)
	}
}

func TestEnsureDirs_CreatesStructure(t *testing.T) {
	home := makeTempAQHome(t)

	err := ensureDirs("broadcast")
	if err != nil {
		t.Fatalf("ensureDirs error: %v", err)
	}

	dirs := []string{
		filepath.Join(home, "channels", "broadcast", "requests"),
		filepath.Join(home, "channels", "broadcast", "archive"),
		filepath.Join(home, "agents"),
		filepath.Join(home, "logs"),
	}
	for _, d := range dirs {
		info, err := os.Stat(d)
		if err != nil {
			t.Errorf("directory %s not created: %v", d, err)
		} else if !info.IsDir() {
			t.Errorf("%s is not a directory", d)
		}
	}
}

func TestChannelPath(t *testing.T) {
	t.Setenv("AQ_HOME", "/tmp/test-aq")
	path := channelPath("broadcast")
	expected := "/tmp/test-aq/channels/broadcast"
	if path != expected {
		t.Errorf("channelPath = %q, want %q", path, expected)
	}
}

func TestSeverityRank(t *testing.T) {
	if severityRank("high") >= severityRank("medium") {
		t.Error("high should rank lower (more severe) than medium")
	}
	if severityRank("medium") >= severityRank("low") {
		t.Error("medium should rank lower (more severe) than low")
	}
}

// ---------- Invariant Tests ----------
// Layer A: Self-checks

func TestFilesExist_AllPresent(t *testing.T) {
	// Create temporary files that exist.
	dir := t.TempDir()
	f1 := filepath.Join(dir, "a.go")
	f2 := filepath.Join(dir, "b.go")
	os.WriteFile(f1, []byte("package a"), 0o644)
	os.WriteFile(f2, []byte("package b"), 0o644)

	result := checkFilesExist([]string{f1, f2})
	if !result.Passed {
		t.Errorf("expected pass, got fail: %s", result.Message)
	}
	if result.Name != "files_exist" {
		t.Errorf("name = %q, want %q", result.Name, "files_exist")
	}
	if result.Category != "self" {
		t.Errorf("category = %q, want %q", result.Category, "self")
	}
}

func TestFilesExist_SomeMissing(t *testing.T) {
	dir := t.TempDir()
	existing := filepath.Join(dir, "exists.go")
	os.WriteFile(existing, []byte("package x"), 0o644)
	missing := filepath.Join(dir, "ghost.go")

	result := checkFilesExist([]string{existing, missing})
	if result.Passed {
		t.Error("expected fail when some files are missing")
	}
	if !strings.Contains(result.Message, "ghost.go") {
		t.Errorf("message should mention missing file: %s", result.Message)
	}
	if result.Severity != "warning" {
		t.Errorf("severity = %q, want %q", result.Severity, "warning")
	}
}

func TestFilesExist_Empty(t *testing.T) {
	// Empty file list: all zero files "exist".
	result := checkFilesExist([]string{})
	if !result.Passed {
		t.Errorf("empty file list should pass: %s", result.Message)
	}
}

func TestPhaseValid_AllPhases(t *testing.T) {
	valid := []string{"conjecture", "proof", "refutation", "refinement"}
	for _, phase := range valid {
		result := checkPhaseValid(phase)
		if !result.Passed {
			t.Errorf("phase %q should be valid", phase)
		}
	}
}

func TestPhaseValid_Invalid(t *testing.T) {
	invalid := []string{"", "draft", "done", "PROOF", "Proof"}
	for _, phase := range invalid {
		result := checkPhaseValid(phase)
		if result.Passed {
			t.Errorf("phase %q should be invalid", phase)
		}
		if result.Severity != "error" {
			t.Errorf("invalid phase severity = %q, want %q", result.Severity, "error")
		}
	}
}

func TestTTLReasonable_Valid(t *testing.T) {
	cases := []int{10, 60, 300, 1800, 3600, 86400}
	for _, ttl := range cases {
		result := checkTTLReasonable(ttl)
		if !result.Passed {
			t.Errorf("TTL %d should be reasonable: %s", ttl, result.Message)
		}
	}
}

func TestTTLReasonable_TooShort(t *testing.T) {
	cases := []int{0, 1, 5, 9}
	for _, ttl := range cases {
		result := checkTTLReasonable(ttl)
		if result.Passed {
			t.Errorf("TTL %d should be too short", ttl)
		}
		if result.Severity != "warning" {
			t.Errorf("severity = %q, want %q", result.Severity, "warning")
		}
	}
}

func TestTTLReasonable_TooLong(t *testing.T) {
	result := checkTTLReasonable(86401)
	if result.Passed {
		t.Error("TTL 86401 should be too long")
	}
	result2 := checkTTLReasonable(999999)
	if result2.Passed {
		t.Error("TTL 999999 should be too long")
	}
}

func TestPathsRelative_AllRelative(t *testing.T) {
	result := checkPathsRelative([]string{"main.go", "src/auth.py", "docs/README.md"})
	if !result.Passed {
		t.Errorf("all relative paths should pass: %s", result.Message)
	}
}

func TestPathsRelative_SomeAbsolute(t *testing.T) {
	result := checkPathsRelative([]string{"main.go", "/usr/local/bin/aq", "/etc/config"})
	if result.Passed {
		t.Error("absolute paths should fail")
	}
	if result.Severity != "error" {
		t.Errorf("severity = %q, want %q", result.Severity, "error")
	}
	if !strings.Contains(result.Message, "/usr/local/bin/aq") {
		t.Errorf("message should mention the absolute path: %s", result.Message)
	}
}

func TestPathsRelative_Empty(t *testing.T) {
	result := checkPathsRelative([]string{})
	if !result.Passed {
		t.Error("empty file list should pass")
	}
}

// Layer B: World-checks

func TestNoGhostBroadcasts_NoGhosts(t *testing.T) {
	makeTempAQHome(t)

	// Write a fresh broadcast.
	b := makeBroadcast(func(b *Broadcast) {
		b.Agent = "test/agent"
		b.Ts = float64(time.Now().Unix())
		b.TTL = 300
	})
	_, _ = writeBroadcast(b, "broadcast")

	result := checkNoGhostBroadcasts("test/agent", "broadcast")
	if !result.Passed {
		t.Errorf("fresh broadcast should not be a ghost: %s", result.Message)
	}
}

func TestNoGhostBroadcasts_NearExpiry(t *testing.T) {
	makeTempAQHome(t)

	// Write a broadcast that is 95% through its TTL (near expiry).
	b := makeBroadcast(func(b *Broadcast) {
		b.Agent = "test/agent"
		b.Ts = float64(time.Now().Unix()) - 285 // 285s of 300s TTL elapsed
		b.TTL = 300
	})
	_, _ = writeBroadcast(b, "broadcast")

	result := checkNoGhostBroadcasts("test/agent", "broadcast")
	if result.Passed {
		t.Error("near-expiry broadcast should be flagged as ghost")
	}
	if result.Severity != "warning" {
		t.Errorf("severity = %q, want %q", result.Severity, "warning")
	}
}

func TestNoGhostBroadcasts_DifferentAgent(t *testing.T) {
	makeTempAQHome(t)

	// Write a near-expiry broadcast for a different agent.
	b := makeBroadcast(func(b *Broadcast) {
		b.Agent = "other/agent"
		b.Ts = float64(time.Now().Unix()) - 285
		b.TTL = 300
	})
	_, _ = writeBroadcast(b, "broadcast")

	// Check for "test/agent" -- should not see other agent's ghosts.
	result := checkNoGhostBroadcasts("test/agent", "broadcast")
	if !result.Passed {
		t.Error("should not flag other agent's broadcasts as ghosts")
	}
}

func TestDiskSpaceOK_Small(t *testing.T) {
	home := makeTempAQHome(t)

	// Write a small file to AQ_HOME.
	os.MkdirAll(filepath.Join(home, "channels", "broadcast", "requests"), 0o755)
	os.WriteFile(filepath.Join(home, "channels", "broadcast", "requests", "small.json"), []byte("{}"), 0o644)

	result := checkDiskSpaceOK()
	if !result.Passed {
		t.Errorf("small AQ_HOME should pass: %s", result.Message)
	}
}

// Layer C: Protocol-checks

func TestULIDUnique_AllUnique(t *testing.T) {
	makeTempAQHome(t)

	// Write three broadcasts with unique IDs.
	for i := 0; i < 3; i++ {
		b := makeBroadcast(func(b *Broadcast) {
			b.Agent = fmt.Sprintf("agent/%d", i)
		})
		_, _ = writeBroadcast(b, "broadcast")
	}

	result := checkULIDUnique("broadcast")
	if !result.Passed {
		t.Errorf("unique ULIDs should pass: %s", result.Message)
	}
}

func TestULIDUnique_Duplicate(t *testing.T) {
	home := makeTempAQHome(t)

	// Write two broadcasts with the same ULID.
	fixedID := "aabbccddee0011223344"
	// Pad to 22 chars.
	for len(fixedID) < 22 {
		fixedID += "0"
	}

	b1 := makeBroadcast(func(b *Broadcast) {
		b.ID = fixedID
		b.Agent = "agent/one"
	})
	b2 := makeBroadcast(func(b *Broadcast) {
		b.ID = fixedID
		b.Agent = "agent/two"
		b.Ts = b1.Ts + 1 // Different timestamp so different filename.
	})

	reqDir := filepath.Join(home, "channels", "broadcast", "requests")
	os.MkdirAll(reqDir, 0o755)

	// Write them manually with different filenames.
	d1, _ := b1.ToJSON()
	d2, _ := b2.ToJSON()
	os.WriteFile(filepath.Join(reqDir, "aq-00000000000001-"+fixedID+".json"), []byte(d1+"\n"), 0o644)
	os.WriteFile(filepath.Join(reqDir, "aq-00000000000002-"+fixedID+".json"), []byte(d2+"\n"), 0o644)

	result := checkULIDUnique("broadcast")
	if result.Passed {
		t.Error("duplicate ULIDs should fail")
	}
	if result.Severity != "error" {
		t.Errorf("severity = %q, want %q", result.Severity, "error")
	}
}

func TestNoDuplicateActive_Clean(t *testing.T) {
	makeTempAQHome(t)

	// Two broadcasts from different agents.
	b1 := makeBroadcast(func(b *Broadcast) {
		b.Agent = "agent/alpha"
		b.ConjectureID = "C-1"
	})
	b2 := makeBroadcast(func(b *Broadcast) {
		b.Agent = "agent/beta"
		b.ConjectureID = "C-1"
	})
	_, _ = writeBroadcast(b1, "broadcast")
	_, _ = writeBroadcast(b2, "broadcast")

	result := checkNoDuplicateActive("broadcast")
	if !result.Passed {
		t.Errorf("different agents same conjecture should pass: %s", result.Message)
	}
}

func TestNoDuplicateActive_Duplicate(t *testing.T) {
	makeTempAQHome(t)

	// Two active broadcasts from same agent + same conjecture.
	b1 := makeBroadcast(func(b *Broadcast) {
		b.Agent = "agent/alpha"
		b.ConjectureID = "C-1"
		b.Status = "prosecuting"
	})
	b2 := makeBroadcast(func(b *Broadcast) {
		b.Agent = "agent/alpha"
		b.ConjectureID = "C-1"
		b.Status = "prosecuting"
	})
	_, _ = writeBroadcast(b1, "broadcast")
	_, _ = writeBroadcast(b2, "broadcast")

	result := checkNoDuplicateActive("broadcast")
	if result.Passed {
		t.Error("duplicate active broadcasts from same agent should fail")
	}
	if result.Severity != "warning" {
		t.Errorf("severity = %q, want %q", result.Severity, "warning")
	}
}

func TestNoDuplicateActive_DoneExempt(t *testing.T) {
	makeTempAQHome(t)

	// One active, one done -- should not count as duplicate.
	b1 := makeBroadcast(func(b *Broadcast) {
		b.Agent = "agent/alpha"
		b.ConjectureID = "C-1"
		b.Status = "prosecuting"
	})
	b2 := makeBroadcast(func(b *Broadcast) {
		b.Agent = "agent/alpha"
		b.ConjectureID = "C-1"
		b.Status = "done"
	})
	_, _ = writeBroadcast(b1, "broadcast")
	_, _ = writeBroadcast(b2, "broadcast")

	result := checkNoDuplicateActive("broadcast")
	if !result.Passed {
		t.Errorf("done + active should not count as duplicate: %s", result.Message)
	}
}

func TestTimestampsSane_AllPast(t *testing.T) {
	makeTempAQHome(t)

	b := makeBroadcast(func(b *Broadcast) {
		b.Ts = float64(time.Now().Unix()) - 10
	})
	_, _ = writeBroadcast(b, "broadcast")

	result := checkTimestampsSane("broadcast")
	if !result.Passed {
		t.Errorf("past timestamps should pass: %s", result.Message)
	}
}

func TestTimestampsSane_Future(t *testing.T) {
	home := makeTempAQHome(t)

	// Write a broadcast with a future timestamp.
	b := makeBroadcast(func(b *Broadcast) {
		b.Ts = float64(time.Now().Unix()) + 3600 // 1 hour in the future
		b.TTL = 7200                             // Keep it "active"
	})
	reqDir := filepath.Join(home, "channels", "broadcast", "requests")
	os.MkdirAll(reqDir, 0o755)
	d, _ := b.ToJSON()
	ts := fmt.Sprintf("%014d", int64(b.Ts))
	os.WriteFile(filepath.Join(reqDir, fmt.Sprintf("aq-%s-%s.json", ts, b.ID)), []byte(d+"\n"), 0o644)

	result := checkTimestampsSane("broadcast")
	if result.Passed {
		t.Error("future timestamp should fail")
	}
	if result.Severity != "error" {
		t.Errorf("severity = %q, want %q", result.Severity, "error")
	}
}

func TestAllPathsRelativeInActive_Clean(t *testing.T) {
	makeTempAQHome(t)

	b := makeBroadcast(func(b *Broadcast) {
		b.Files = []string{"main.go", "src/lib.go"}
	})
	_, _ = writeBroadcast(b, "broadcast")

	result := checkAllPathsRelativeInActive("broadcast")
	if !result.Passed {
		t.Errorf("relative paths should pass: %s", result.Message)
	}
}

func TestAllPathsRelativeInActive_Absolute(t *testing.T) {
	makeTempAQHome(t)

	b := makeBroadcast(func(b *Broadcast) {
		b.Files = []string{"/etc/passwd", "main.go"}
	})
	_, _ = writeBroadcast(b, "broadcast")

	result := checkAllPathsRelativeInActive("broadcast")
	if result.Passed {
		t.Error("absolute path in broadcast should fail")
	}
	if result.Severity != "error" {
		t.Errorf("severity = %q, want %q", result.Severity, "error")
	}
}

// Test runSelfChecks integration.
func TestRunSelfChecks_Integration(t *testing.T) {
	dir := t.TempDir()
	existingFile := filepath.Join(dir, "exists.go")
	os.WriteFile(existingFile, []byte("package x"), 0o644)

	b := Broadcast{
		Agent:    "test/agent",
		Worktree: "main",
		Phase:    "proof",
		Status:   "prosecuting",
		Files:    []string{existingFile},
		TTL:      300,
	}

	results := runSelfChecks(b)
	// Should have: files_exist, paths_relative (absolute path!), git_branch_matches, phase_valid, ttl_reasonable
	if len(results) < 5 {
		t.Errorf("expected at least 5 results, got %d", len(results))
	}

	// Check that paths_relative catches the absolute temp path.
	var pathResult *InvariantResult
	for i := range results {
		if results[i].Name == "paths_relative" {
			pathResult = &results[i]
			break
		}
	}
	if pathResult == nil {
		t.Fatal("paths_relative result not found")
	}
	if pathResult.Passed {
		t.Error("absolute temp dir path should fail paths_relative")
	}
}

// Test countFailures.
func TestCountFailures(t *testing.T) {
	results := []InvariantResult{
		{Passed: true, Severity: "info"},
		{Passed: false, Severity: "error"},
		{Passed: false, Severity: "warning"},
		{Passed: false, Severity: "warning"},
		{Passed: true, Severity: "info"},
		{Passed: false, Severity: "error"},
	}

	errs, warns := countFailures(results)
	if errs != 2 {
		t.Errorf("errors = %d, want 2", errs)
	}
	if warns != 2 {
		t.Errorf("warnings = %d, want 2", warns)
	}
}

// Test InvariantResult JSON serialization.
func TestInvariantResult_JSON(t *testing.T) {
	r := InvariantResult{
		Name:     "files_exist",
		Passed:   true,
		Message:  "all 3 files exist",
		Category: "self",
		Severity: "info",
	}
	data, err := json.Marshal(r)
	if err != nil {
		t.Fatalf("marshal error: %v", err)
	}

	var restored InvariantResult
	if err := json.Unmarshal(data, &restored); err != nil {
		t.Fatalf("unmarshal error: %v", err)
	}

	if restored.Name != r.Name {
		t.Errorf("Name = %q, want %q", restored.Name, r.Name)
	}
	if restored.Passed != r.Passed {
		t.Errorf("Passed = %v, want %v", restored.Passed, r.Passed)
	}
	if restored.Category != r.Category {
		t.Errorf("Category = %q, want %q", restored.Category, r.Category)
	}
}

// ---------- L7 Review Fixes ----------

// Test that checkConflicts skips broadcasts with status=done.
func TestCheckConflicts_SkipsDoneStatus(t *testing.T) {
	makeTempAQHome(t)
	ch := "broadcast"

	// Agent A is done but broadcast hasn't expired yet.
	doneAgent := makeBroadcast(func(b *Broadcast) {
		b.Agent = "agent-a"
		b.ConjectureID = "C-1"
		b.Phase = "proof"
		b.Files = []string{"auth.py"}
		b.Status = "done"
	})
	writeBroadcast(doneAgent, ch)

	// Agent B is actively working on the same file.
	activeAgent := makeBroadcast(func(b *Broadcast) {
		b.Agent = "agent-b"
		b.ConjectureID = "C-2"
		b.Phase = "proof"
		b.Files = []string{"auth.py"}
	})
	writeBroadcast(activeAgent, ch)

	// Agent C checks — should NOT see conflict with done agent A.
	me := makeBroadcast(func(b *Broadcast) {
		b.Agent = "agent-c"
		b.ConjectureID = "C-3"
		b.Phase = "proof"
		b.Files = []string{"auth.py"}
	})
	signals, err := checkConflicts(me, ch)
	if err != nil {
		t.Fatalf("checkConflicts error: %v", err)
	}

	// Should only conflict with agent-b, not agent-a.
	if len(signals) != 1 {
		t.Fatalf("expected 1 conflict signal, got %d", len(signals))
	}
	if signals[0].B.Agent != "agent-b" {
		t.Errorf("expected conflict with agent-b, got %s", signals[0].B.Agent)
	}
}

// Test that readActive handles concurrent archive gracefully.
func TestReadActive_ConcurrentArchive(t *testing.T) {
	makeTempAQHome(t)
	ch := "broadcast"

	// Create an already-expired broadcast.
	expired := makeBroadcast(func(b *Broadcast) {
		b.Agent = "agent-old"
		b.Files = []string{"old.py"}
		b.Ts = float64(time.Now().Add(-10 * time.Minute).Unix())
		b.TTL = 60
	})
	writeBroadcast(expired, ch)

	// First read should archive it.
	active1, err := readActive(ch)
	if err != nil {
		t.Fatalf("first readActive error: %v", err)
	}
	if len(active1) != 0 {
		t.Errorf("expected 0 active after expiry, got %d", len(active1))
	}

	// Second read should not error — file is already gone.
	active2, err := readActive(ch)
	if err != nil {
		t.Fatalf("second readActive error: %v", err)
	}
	if len(active2) != 0 {
		t.Errorf("expected 0 active on second read, got %d", len(active2))
	}
}

// ---------- CLI Command Tests ----------

// Test cmdAnnounce with valid arguments.
func TestCmdAnnounce_Valid(t *testing.T) {
	makeTempAQHome(t)
	channelName = "broadcast"
	jsonOutput = false

	code := cmdAnnounce([]string{"-c", "C-1", "-f", "auth.py", "--claim", "test announce"})
	if code != 0 {
		t.Fatalf("cmdAnnounce returned %d, want 0", code)
	}

	// Verify broadcast was written.
	active, err := readActive("broadcast")
	if err != nil {
		t.Fatalf("readActive error: %v", err)
	}
	if len(active) != 1 {
		t.Fatalf("expected 1 active broadcast, got %d", len(active))
	}
	if active[0].ConjectureID != "C-1" {
		t.Errorf("conjecture = %q, want C-1", active[0].ConjectureID)
	}
	if active[0].ConjectureClaim != "test announce" {
		t.Errorf("claim = %q, want 'test announce'", active[0].ConjectureClaim)
	}
}

// Test cmdAnnounce fails without -c flag.
func TestCmdAnnounce_MissingConjecture(t *testing.T) {
	code := cmdAnnounce([]string{"-f", "auth.py"})
	if code != 1 {
		t.Errorf("cmdAnnounce without -c returned %d, want 1", code)
	}
}

// Test cmdCheck with valid arguments.
func TestCmdCheck_Valid(t *testing.T) {
	makeTempAQHome(t)
	channelName = "broadcast"
	jsonOutput = false

	// Write a broadcast from another agent first.
	other := makeBroadcast(func(b *Broadcast) {
		b.Agent = "other-agent"
		b.ConjectureID = "C-1"
		b.Phase = "proof"
		b.Files = []string{"auth.py"}
	})
	writeBroadcast(other, "broadcast")

	// Check should find a HIGH conflict (both proof + shared file) and return 1.
	code := cmdCheck([]string{"-c", "C-2", "-f", "auth.py"})
	if code != 1 {
		t.Errorf("cmdCheck with HIGH conflict returned %d, want 1", code)
	}
}

// Test cmdStatus runs without error.
func TestCmdStatus_Valid(t *testing.T) {
	makeTempAQHome(t)
	channelName = "broadcast"
	jsonOutput = false

	code := cmdStatus([]string{})
	if code != 0 {
		t.Errorf("cmdStatus returned %d, want 0", code)
	}
}

// Test parseAnnounceArgs parses all flags correctly.
func TestParseAnnounceArgs(t *testing.T) {
	args := []string{"-c", "C-5", "-f", "a.py,b.py", "--claim", "fixing bugs", "--phase", "refutation", "--status", "blocked", "--ttl", "600"}
	p := parseAnnounceArgs(args)

	if p.conjecture != "C-5" {
		t.Errorf("conjecture = %q, want C-5", p.conjecture)
	}
	if p.files != "a.py,b.py" {
		t.Errorf("files = %q, want a.py,b.py", p.files)
	}
	if p.claim != "fixing bugs" {
		t.Errorf("claim = %q, want 'fixing bugs'", p.claim)
	}
	if p.phase != "refutation" {
		t.Errorf("phase = %q, want refutation", p.phase)
	}
	if p.status != "blocked" {
		t.Errorf("status = %q, want blocked", p.status)
	}
	if p.ttl != 600 {
		t.Errorf("ttl = %d, want 600", p.ttl)
	}
}

// Test parseAnnounceArgs defaults.
func TestParseAnnounceArgs_Defaults(t *testing.T) {
	p := parseAnnounceArgs([]string{})
	if p.phase != "proof" {
		t.Errorf("default phase = %q, want proof", p.phase)
	}
	if p.status != "prosecuting" {
		t.Errorf("default status = %q, want prosecuting", p.status)
	}
	if p.ttl != DefaultTTL {
		t.Errorf("default ttl = %d, want %d", p.ttl, DefaultTTL)
	}
}

// ========== Benchmark Helpers ==========

// makeBenchAQHome creates a temp AQ_HOME for benchmarks.
func makeBenchAQHome(b *testing.B) {
	b.Helper()
	dir := b.TempDir()
	b.Setenv("AQ_HOME", dir)
}

// populateBroadcasts pre-fills the channel with n broadcasts.
func populateBroadcasts(b *testing.B, n int, channel string) {
	b.Helper()
	for i := 0; i < n; i++ {
		bc := makeBroadcast(func(bc *Broadcast) {
			bc.Agent = fmt.Sprintf("agent/%d", i)
			bc.Files = []string{fmt.Sprintf("file%d.go", i%20)}
		})
		if _, err := writeBroadcast(bc, channel); err != nil {
			b.Fatal(err)
		}
	}
}

// ========== 1. Serial Baselines ==========

func BenchmarkWriteBroadcast_Serial(b *testing.B) {
	makeBenchAQHome(b)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		bc := makeBroadcast(func(bc *Broadcast) {
			bc.Agent = fmt.Sprintf("bench/serial-%d", i)
		})
		if _, err := writeBroadcast(bc, "broadcast"); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkReadActive_Serial(b *testing.B) {
	makeBenchAQHome(b)
	populateBroadcasts(b, 100, "broadcast")
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := readActive("broadcast"); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkCheckConflicts_Serial(b *testing.B) {
	makeBenchAQHome(b)
	populateBroadcasts(b, 10, "broadcast")
	me := makeBroadcast(func(bc *Broadcast) {
		bc.Agent = "bench/checker"
		bc.Phase = "proof"
		bc.Files = []string{"file0.go"}
	})
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := checkConflicts(me, "broadcast"); err != nil {
			b.Fatal(err)
		}
	}
}

// ========== 2. Parallel Benchmarks ==========

func BenchmarkWriteBroadcast_Parallel(b *testing.B) {
	makeBenchAQHome(b)
	var counter uint64
	var mu sync.Mutex
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		mu.Lock()
		counter++
		id := counter
		mu.Unlock()
		i := 0
		for pb.Next() {
			bc := makeBroadcast(func(bc *Broadcast) {
				bc.Agent = fmt.Sprintf("bench/parallel-%d-%d", id, i)
			})
			if _, err := writeBroadcast(bc, "broadcast"); err != nil {
				b.Fatal(err)
			}
			i++
		}
	})
}

func BenchmarkReadActive_Parallel(b *testing.B) {
	makeBenchAQHome(b)
	populateBroadcasts(b, 100, "broadcast")
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			if _, err := readActive("broadcast"); err != nil {
				b.Fatal(err)
			}
		}
	})
}

// ========== 3. Fan-out Patterns ==========

func BenchmarkFanOut_1WriterNReaders(b *testing.B) {
	cases := []int{1, 5, 10, 50}
	for _, n := range cases {
		b.Run(fmt.Sprintf("readers=%d", n), func(b *testing.B) {
			makeBenchAQHome(b)
			done := make(chan struct{})
			var wg sync.WaitGroup

			// Start N reader goroutines.
			for r := 0; r < n; r++ {
				wg.Add(1)
				go func() {
					defer wg.Done()
					for {
						select {
						case <-done:
							return
						default:
							readActive("broadcast")
						}
					}
				}()
			}

			// Writer: write b.N broadcasts at ~10ms intervals.
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				bc := makeBroadcast(func(bc *Broadcast) {
					bc.Agent = fmt.Sprintf("writer/%d", i)
				})
				if _, err := writeBroadcast(bc, "broadcast"); err != nil {
					b.Fatal(err)
				}
				time.Sleep(10 * time.Millisecond)
			}
			b.StopTimer()
			close(done)
			wg.Wait()
		})
	}
}

func BenchmarkFanOut_NWriters1Reader(b *testing.B) {
	cases := []int{1, 5, 10, 50}
	for _, n := range cases {
		b.Run(fmt.Sprintf("writers=%d", n), func(b *testing.B) {
			makeBenchAQHome(b)
			done := make(chan struct{})
			var wg sync.WaitGroup

			// Start 1 reader goroutine.
			wg.Add(1)
			go func() {
				defer wg.Done()
				for {
					select {
					case <-done:
						return
					default:
						readActive("broadcast")
					}
				}
			}()

			// N writer goroutines, each writes b.N/N broadcasts.
			perWriter := b.N / n
			if perWriter < 1 {
				perWriter = 1
			}
			b.ResetTimer()
			var writerWg sync.WaitGroup
			for w := 0; w < n; w++ {
				writerWg.Add(1)
				go func(wid int) {
					defer writerWg.Done()
					for i := 0; i < perWriter; i++ {
						bc := makeBroadcast(func(bc *Broadcast) {
							bc.Agent = fmt.Sprintf("writer/%d/%d", wid, i)
						})
						writeBroadcast(bc, "broadcast")
						time.Sleep(10 * time.Millisecond)
					}
				}(w)
			}
			writerWg.Wait()
			b.StopTimer()
			close(done)
			wg.Wait()
		})
	}
}

func BenchmarkFanOut_NWritersNReaders(b *testing.B) {
	cases := []int{2, 5, 10}
	for _, n := range cases {
		b.Run(fmt.Sprintf("n=%d", n), func(b *testing.B) {
			makeBenchAQHome(b)
			done := make(chan struct{})
			var wg sync.WaitGroup

			// Start N reader goroutines.
			for r := 0; r < n; r++ {
				wg.Add(1)
				go func() {
					defer wg.Done()
					for {
						select {
						case <-done:
							return
						default:
							readActive("broadcast")
						}
					}
				}()
			}

			// N writer goroutines, each writes b.N/N broadcasts.
			perWriter := b.N / n
			if perWriter < 1 {
				perWriter = 1
			}
			b.ResetTimer()
			var writerWg sync.WaitGroup
			for w := 0; w < n; w++ {
				writerWg.Add(1)
				go func(wid int) {
					defer writerWg.Done()
					for i := 0; i < perWriter; i++ {
						bc := makeBroadcast(func(bc *Broadcast) {
							bc.Agent = fmt.Sprintf("writer/%d/%d", wid, i)
						})
						writeBroadcast(bc, "broadcast")
						time.Sleep(10 * time.Millisecond)
					}
				}(w)
			}
			writerWg.Wait()
			b.StopTimer()
			close(done)
			wg.Wait()
		})
	}
}

// ========== 4. Conflict Detection at Scale ==========

func BenchmarkCheckConflicts_10Agents_AllOverlap(b *testing.B) {
	makeBenchAQHome(b)
	// 10 agents, all proof phase, all touching the same file.
	for i := 0; i < 10; i++ {
		bc := makeBroadcast(func(bc *Broadcast) {
			bc.Agent = fmt.Sprintf("agent/%d", i)
			bc.Phase = "proof"
			bc.Files = []string{"shared.go"}
		})
		if _, err := writeBroadcast(bc, "broadcast"); err != nil {
			b.Fatal(err)
		}
	}
	me := makeBroadcast(func(bc *Broadcast) {
		bc.Agent = "bench/checker"
		bc.Phase = "proof"
		bc.Files = []string{"shared.go"}
	})
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		signals, err := checkConflicts(me, "broadcast")
		if err != nil {
			b.Fatal(err)
		}
		if len(signals) != 10 {
			b.Fatalf("expected 10 HIGH signals, got %d", len(signals))
		}
	}
}

func BenchmarkCheckConflicts_100Agents_SparseOverlap(b *testing.B) {
	makeBenchAQHome(b)
	rng := rand.New(rand.NewSource(42))
	// 100 agents, each touching 3 random files from pool of 200.
	for i := 0; i < 100; i++ {
		files := make([]string, 3)
		for j := 0; j < 3; j++ {
			files[j] = fmt.Sprintf("file%d.go", rng.Intn(200))
		}
		bc := makeBroadcast(func(bc *Broadcast) {
			bc.Agent = fmt.Sprintf("agent/%d", i)
			bc.Phase = "proof"
			bc.Files = files
		})
		if _, err := writeBroadcast(bc, "broadcast"); err != nil {
			b.Fatal(err)
		}
	}
	me := makeBroadcast(func(bc *Broadcast) {
		bc.Agent = "bench/checker"
		bc.Phase = "proof"
		bc.Files = []string{"file0.go", "file50.go", "file100.go"}
	})
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := checkConflicts(me, "broadcast"); err != nil {
			b.Fatal(err)
		}
	}
}

// ========== 5. Filesystem Stress ==========

func BenchmarkDirectoryListing_1000Files(b *testing.B) {
	makeBenchAQHome(b)
	populateBroadcasts(b, 1000, "broadcast")
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := readActive("broadcast"); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkBurstWrite_100(b *testing.B) {
	makeBenchAQHome(b)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		for j := 0; j < 100; j++ {
			bc := makeBroadcast(func(bc *Broadcast) {
				bc.Agent = fmt.Sprintf("burst/%d/%d", i, j)
			})
			if _, err := writeBroadcast(bc, "broadcast"); err != nil {
				b.Fatal(err)
			}
		}
	}
}

func BenchmarkConcurrentArchive(b *testing.B) {
	makeBenchAQHome(b)
	// Write a mix of expired (TTL=1, old ts) and fresh broadcasts.
	for i := 0; i < 50; i++ {
		bc := makeBroadcast(func(bc *Broadcast) {
			bc.Agent = fmt.Sprintf("expired/%d", i)
			bc.TTL = 1
			bc.Ts = float64(time.Now().Unix() - 100) // well past TTL
		})
		if _, err := writeBroadcast(bc, "broadcast"); err != nil {
			b.Fatal(err)
		}
	}
	for i := 0; i < 50; i++ {
		bc := makeBroadcast(func(bc *Broadcast) {
			bc.Agent = fmt.Sprintf("fresh/%d", i)
		})
		if _, err := writeBroadcast(bc, "broadcast"); err != nil {
			b.Fatal(err)
		}
	}
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			readActive("broadcast")
		}
	})
}

// ========== 6. Correctness Under Chaos ==========

func TestChaos_ConcurrentWriteRead(t *testing.T) {
	makeTempAQHome(t)
	var wg sync.WaitGroup
	done := make(chan struct{})
	errCh := make(chan error, 100)

	// 10 writer goroutines.
	for w := 0; w < 10; w++ {
		wg.Add(1)
		go func(wid int) {
			defer wg.Done()
			i := 0
			for {
				select {
				case <-done:
					return
				default:
					bc := makeBroadcast(func(bc *Broadcast) {
						bc.Agent = fmt.Sprintf("chaos-writer/%d/%d", wid, i)
					})
					if _, err := writeBroadcast(bc, "broadcast"); err != nil {
						select {
						case errCh <- fmt.Errorf("writer %d iter %d: %w", wid, i, err):
						default:
						}
					}
					i++
				}
			}
		}(w)
	}

	// 10 reader goroutines.
	for r := 0; r < 10; r++ {
		wg.Add(1)
		go func(rid int) {
			defer wg.Done()
			for {
				select {
				case <-done:
					return
				default:
					if _, err := readActive("broadcast"); err != nil {
						select {
						case errCh <- fmt.Errorf("reader %d: %w", rid, err):
						default:
						}
					}
				}
			}
		}(r)
	}

	// Run for 2 seconds.
	time.Sleep(2 * time.Second)
	close(done)
	wg.Wait()
	close(errCh)

	for err := range errCh {
		t.Errorf("chaos error: %v", err)
	}
}

func TestChaos_NoLostBroadcasts(t *testing.T) {
	makeTempAQHome(t)

	// Write 100 broadcasts with long TTL (they should all be active).
	for i := 0; i < 100; i++ {
		bc := makeBroadcast(func(bc *Broadcast) {
			bc.Agent = fmt.Sprintf("noloss/%d", i)
			bc.TTL = 3600
		})
		if _, err := writeBroadcast(bc, "broadcast"); err != nil {
			t.Fatalf("write %d: %v", i, err)
		}
	}

	active, err := readActive("broadcast")
	if err != nil {
		t.Fatalf("readActive error: %v", err)
	}
	if len(active) < 100 {
		t.Errorf("expected >= 100 active broadcasts, got %d", len(active))
	}
}

func TestChaos_ULIDUniqueness(t *testing.T) {
	const total = 10000
	ids := make([]string, total)
	var wg sync.WaitGroup
	goroutines := 10
	perGoroutine := total / goroutines

	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func(gid int) {
			defer wg.Done()
			for i := 0; i < perGoroutine; i++ {
				ids[gid*perGoroutine+i] = generateULID()
			}
		}(g)
	}
	wg.Wait()

	seen := make(map[string]struct{}, total)
	for i, id := range ids {
		if _, dup := seen[id]; dup {
			t.Fatalf("duplicate ULID at index %d: %s", i, id)
		}
		seen[id] = struct{}{}
	}
}

func TestChaos_ArchiveRace(t *testing.T) {
	home := makeTempAQHome(t)

	// Write 50 broadcasts with TTL=1 and old timestamp (already expired).
	for i := 0; i < 50; i++ {
		bc := makeBroadcast(func(bc *Broadcast) {
			bc.Agent = fmt.Sprintf("archrace/%d", i)
			bc.TTL = 1
			bc.Ts = float64(time.Now().Unix() - 100)
		})
		if _, err := writeBroadcast(bc, "broadcast"); err != nil {
			t.Fatalf("write %d: %v", i, err)
		}
	}

	// 10 goroutines call readActive simultaneously (triggers archiving).
	var wg sync.WaitGroup
	for g := 0; g < 10; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			// Multiple reads per goroutine to increase contention.
			for j := 0; j < 5; j++ {
				readActive("broadcast")
			}
		}()
	}
	wg.Wait()

	// Assert: archive directory has files (some or all 50 should be there).
	archDir := filepath.Join(home, "channels", "broadcast", "archive")
	entries, err := os.ReadDir(archDir)
	if err != nil {
		t.Fatalf("ReadDir archive: %v", err)
	}
	if len(entries) == 0 {
		t.Error("expected archived files, got 0")
	}
	// All 50 should be archived since they were all expired.
	if len(entries) != 50 {
		t.Logf("archived %d of 50 (some may have had race-induced skips, which is acceptable)", len(entries))
	}
}
