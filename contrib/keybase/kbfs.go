//go:build ignore

// kbfs.go -- Keybase/KBFS transport plugin for aq
//
// Standalone binary (not compiled into the main aq binary).
// Shells out to the `keybase` CLI for all KBFS operations.
// No external Go dependencies -- stdlib only.
//
// Two modes:
//   -publish   TX: read broadcast JSON from stdin, write to KBFS team dir
//   -subscribe RX: poll KBFS team dir, write new broadcasts locally
//
// Usage:
//   go run contrib/keybase/kbfs.go -publish -team myteam \
//       -agent origin/feat-auth -conjecture C-1 -phase proof -files "auth.py"
//   go run contrib/keybase/kbfs.go -subscribe -team myteam
//   go run contrib/keybase/kbfs.go -subscribe -path /keybase/private/alice,bob/aq

package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

// Broadcast is the aq broadcast payload. Matches main.go schema exactly.
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

// kbfsBasePath returns the KBFS directory for aq broadcasts.
// If pathOverride is set, use it directly. Otherwise, derive from team name.
func kbfsBasePath(team, pathOverride string) string {
	if pathOverride != "" {
		return pathOverride
	}
	return fmt.Sprintf("/keybase/team/%s/aq", team)
}

// localIngestDir returns the local directory where RX writes incoming broadcasts.
func localIngestDir() string {
	aqDir := os.Getenv("AQ_DIR")
	if aqDir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			log.Fatalf("cannot determine home directory: %v", err)
		}
		aqDir = filepath.Join(home, ".aq")
	}
	return filepath.Join(aqDir, "channels", "broadcast", "requests")
}

// generateBroadcastID produces a timestamp-based ID.
// Not a proper ULID but sufficient for dedup and ordering.
func generateBroadcastID() string {
	return fmt.Sprintf("%d-%04x", time.Now().UnixMilli(), os.Getpid()&0xffff)
}

// broadcastFilename returns the filename for a broadcast on KBFS.
func broadcastFilename(broadcast Broadcast) string {
	return fmt.Sprintf("aq-%d-%s.json", int64(broadcast.Ts), broadcast.ID)
}

// keybaseFSWrite writes data to a KBFS path via `keybase fs write`.
func keybaseFSWrite(kbfsPath string, data []byte) error {
	cmd := exec.Command("keybase", "fs", "write", kbfsPath)
	cmd.Stdin = strings.NewReader(string(data))
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// keybaseFSRead reads a file from KBFS via `keybase fs read`.
func keybaseFSRead(kbfsPath string) ([]byte, error) {
	cmd := exec.Command("keybase", "fs", "read", kbfsPath)
	cmd.Stderr = os.Stderr
	return cmd.Output()
}

// keybaseFSList lists files in a KBFS directory via `keybase fs ls`.
// Returns a slice of filenames (not full paths).
func keybaseFSList(kbfsDir string) ([]string, error) {
	cmd := exec.Command("keybase", "fs", "ls", kbfsDir)
	cmd.Stderr = os.Stderr
	output, err := cmd.Output()
	if err != nil {
		return nil, err
	}

	var entries []string
	for _, line := range strings.Split(strings.TrimSpace(string(output)), "\n") {
		name := strings.TrimSpace(line)
		if name != "" {
			entries = append(entries, name)
		}
	}
	return entries, nil
}

// keybaseFSMkdir creates a directory on KBFS via `keybase fs mkdir`.
func keybaseFSMkdir(kbfsDir string) error {
	cmd := exec.Command("keybase", "fs", "mkdir", kbfsDir)
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// publish writes a broadcast JSON file to the KBFS team directory.
func publish(broadcast Broadcast, team, pathOverride string) error {
	baseDir := kbfsBasePath(team, pathOverride)

	// Ensure the target directory exists. Ignore errors -- the dir may
	// already exist and `keybase fs mkdir` has no -p equivalent.
	_ = keybaseFSMkdir(baseDir)

	data, err := json.MarshalIndent(broadcast, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal broadcast: %w", err)
	}

	filename := broadcastFilename(broadcast)
	remotePath := baseDir + "/" + filename

	if err := keybaseFSWrite(remotePath, data); err != nil {
		return fmt.Errorf("kbfs write %s: %w", remotePath, err)
	}

	log.Printf("published %s to %s", filename, baseDir)
	return nil
}

// subscribe polls the KBFS team directory and writes new broadcasts locally.
func subscribe(team, pathOverride string, pollInterval time.Duration) error {
	baseDir := kbfsBasePath(team, pathOverride)
	ingestDir := localIngestDir()

	if err := os.MkdirAll(ingestDir, 0o755); err != nil {
		return fmt.Errorf("create ingest dir %s: %w", ingestDir, err)
	}

	// Track which broadcast IDs we have already ingested to avoid duplicates.
	seenIDs := make(map[string]bool)

	// Pre-populate seen set from existing local files so we don't
	// re-ingest broadcasts that were already written in a prior run.
	existingFiles, _ := filepath.Glob(filepath.Join(ingestDir, "aq-*.json"))
	for _, existingFile := range existingFiles {
		data, err := os.ReadFile(existingFile)
		if err != nil {
			continue
		}
		var existingBroadcast Broadcast
		if err := json.Unmarshal(data, &existingBroadcast); err != nil {
			continue
		}
		if existingBroadcast.ID != "" {
			seenIDs[existingBroadcast.ID] = true
		}
	}

	log.Printf("subscribing to %s (poll every %v)", baseDir, pollInterval)
	log.Printf("writing to %s", ingestDir)
	log.Printf("pre-populated %d seen broadcast IDs", len(seenIDs))

	// Set up signal handling for graceful shutdown.
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()

	// Run one poll immediately before entering the ticker loop.
	pollOnce(baseDir, ingestDir, seenIDs)

	for {
		select {
		case <-ticker.C:
			pollOnce(baseDir, ingestDir, seenIDs)
		case sig := <-sigChan:
			log.Printf("received %v, shutting down", sig)
			return nil
		}
	}
}

// pollOnce lists the KBFS directory, reads any new aq-*.json files,
// deduplicates by broadcast ID, and writes them to the local ingest dir.
func pollOnce(kbfsDir, ingestDir string, seenIDs map[string]bool) {
	entries, err := keybaseFSList(kbfsDir)
	if err != nil {
		log.Printf("poll error listing %s: %v", kbfsDir, err)
		return
	}

	for _, entry := range entries {
		if !strings.HasPrefix(entry, "aq-") || !strings.HasSuffix(entry, ".json") {
			continue
		}

		remotePath := kbfsDir + "/" + entry
		data, err := keybaseFSRead(remotePath)
		if err != nil {
			log.Printf("poll error reading %s: %v", remotePath, err)
			continue
		}

		var broadcast Broadcast
		if err := json.Unmarshal(data, &broadcast); err != nil {
			log.Printf("poll error parsing %s: %v", remotePath, err)
			continue
		}

		// Dedup by broadcast ID.
		if broadcast.ID == "" {
			// Fall back to filename as dedup key if ID is missing.
			broadcast.ID = entry
		}
		if seenIDs[broadcast.ID] {
			continue
		}
		seenIDs[broadcast.ID] = true

		// Check TTL expiry. A broadcast with ts + ttl < now is stale.
		if broadcast.TTL > 0 {
			expiresAt := broadcast.Ts + float64(broadcast.TTL)
			if float64(time.Now().Unix()) > expiresAt {
				log.Printf("skipping expired broadcast %s (expired %.0fs ago)",
					broadcast.ID, float64(time.Now().Unix())-expiresAt)
				continue
			}
		}

		// Write to local ingest directory.
		localPath := filepath.Join(ingestDir, entry)
		if err := os.WriteFile(localPath, data, 0o644); err != nil {
			log.Printf("poll error writing %s: %v", localPath, err)
			continue
		}

		log.Printf("ingested %s from %s (agent=%s conjecture=%s phase=%s)",
			entry, kbfsDir, broadcast.Agent, broadcast.ConjectureID, broadcast.Phase)
	}
}

func main() {
	doPublish := flag.Bool("publish", false, "TX: publish a broadcast to KBFS")
	doSubscribe := flag.Bool("subscribe", false, "RX: poll KBFS and ingest broadcasts locally")

	team := flag.String("team", "", "Keybase team name (e.g. amigosmalla)")
	pathOverride := flag.String("path", "", "KBFS path override (e.g. /keybase/private/alice,bob/aq)")
	pollIntervalSec := flag.Int("poll-interval", 5, "RX poll interval in seconds")

	// Publish-mode flags.
	agent := flag.String("agent", "", "Agent address (e.g. origin/feat-auth)")
	conjecture := flag.String("conjecture", "C-0", "Conjecture ID")
	claim := flag.String("claim", "", "Conjecture claim (plain language intent)")
	phase := flag.String("phase", "conjecture", "CPRR phase: conjecture|proof|refutation|refinement")
	status := flag.String("status", "prosecuting", "Broadcast status: prosecuting|done|blocked")
	files := flag.String("files", "", "Comma-separated list of files being touched")
	ttl := flag.Int("ttl", 3600, "Broadcast TTL in seconds")

	flag.Parse()

	if *team == "" && *pathOverride == "" {
		fmt.Fprintf(os.Stderr, "error: specify -team or -path\n")
		flag.Usage()
		os.Exit(1)
	}

	switch {
	case *doPublish:
		if *agent == "" {
			log.Fatal("-agent required for publish mode")
		}

		worktree := "main"
		if parts := strings.Split(*agent, "/"); len(parts) > 1 {
			worktree = parts[len(parts)-1]
		}

		var fileList []string
		if *files != "" {
			fileList = strings.Split(*files, ",")
		}

		broadcastID := generateBroadcastID()
		broadcast := Broadcast{
			Agent:           *agent,
			Worktree:        worktree,
			ConjectureID:    *conjecture,
			ConjectureClaim: *claim,
			Phase:           *phase,
			Status:          *status,
			Files:           fileList,
			Ts:              float64(time.Now().Unix()),
			TTL:             *ttl,
			ID:              broadcastID,
		}

		if err := publish(broadcast, *team, *pathOverride); err != nil {
			log.Fatalf("publish failed: %v", err)
		}

	case *doSubscribe:
		pollInterval := time.Duration(*pollIntervalSec) * time.Second
		if err := subscribe(*team, *pathOverride, pollInterval); err != nil {
			log.Fatalf("subscribe failed: %v", err)
		}

	default:
		fmt.Fprintf(os.Stderr, "specify -publish or -subscribe\n")
		flag.Usage()
		os.Exit(1)
	}
}
