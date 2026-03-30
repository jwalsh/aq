//go:build ignore

// ghissue.go — GitHub Issues transport for aq
//
// Tier 4: the "when everything else is down" transport. Human-readable
// on GitHub. Rate-limited. POC quality.
//
// Uses the `gh` CLI (GitHub CLI) — no Go dependencies beyond stdlib.
//
// TX: posts a broadcast as an issue comment via `gh issue comment`.
// RX: polls issue comments via `gh api`, parses broadcast JSON,
// deduplicates by ID, writes new broadcasts to the local aq channel.
//
// Usage:
//   go run ghissue.go -repo jwalsh/aq -issue 42 -publish \
//       -agent origin/feat-auth -conjecture C-1 -phase proof -files "auth.py"
//   go run ghissue.go -repo jwalsh/aq -issue 42 -subscribe

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

// Broadcast is the aq broadcast payload. Same schema as main.go.
type Broadcast struct {
	Agent           string   `json:"agent"`
	Worktree        string   `json:"worktree"`
	ConjectureID    string   `json:"conjecture_id"`
	ConjectureClaim string   `json:"conjecture_claim"`
	Phase           string   `json:"phase"`
	Status          string   `json:"status"`
	Files           []string `json:"files"`
	Timestamp       float64  `json:"ts"`
	TTL             int      `json:"ttl"`
	ID              string   `json:"id"`
}

// broadcastDir returns the path to the aq broadcast requests directory,
// creating it if it does not exist.
func broadcastDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home: %w", err)
	}
	dir := filepath.Join(home, ".aq", "channels", "broadcast", "requests")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("create broadcast dir: %w", err)
	}
	return dir, nil
}

// writeBroadcast writes a broadcast to the local aq channel as aq-<id>.json.
func writeBroadcast(b Broadcast) error {
	dir, err := broadcastDir()
	if err != nil {
		return err
	}

	data, err := json.MarshalIndent(b, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal broadcast: %w", err)
	}

	filename := filepath.Join(dir, fmt.Sprintf("aq-%s.json", b.ID))
	if err := os.WriteFile(filename, data, 0o644); err != nil {
		return fmt.Errorf("write %s: %w", filename, err)
	}

	log.Printf("wrote %s", filename)
	return nil
}

// ghComment posts a comment on the given issue via `gh issue comment`.
func ghComment(repo string, issue int, body string) error {
	cmd := exec.Command("gh", "issue", "comment",
		fmt.Sprintf("%d", issue),
		"-R", repo,
		"--body", body,
	)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// ghFetchComments retrieves all comments from the issue via `gh api`.
// Returns the raw JSON array of comment objects.
func ghFetchComments(repo string, issue int) ([]json.RawMessage, error) {
	// Use pagination — gh api handles it with --paginate.
	// Each comment object has a "body" field containing the comment text.
	cmd := exec.Command("gh", "api",
		fmt.Sprintf("repos/%s/issues/%d/comments", repo, issue),
		"--paginate",
	)
	out, err := cmd.Output()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			return nil, fmt.Errorf("gh api failed: %s", string(exitErr.Stderr))
		}
		return nil, fmt.Errorf("gh api: %w", err)
	}

	// gh --paginate for JSON arrays concatenates multiple arrays.
	// We need to handle both single array and concatenated arrays.
	// Try single array first.
	var comments []json.RawMessage
	if err := json.Unmarshal(out, &comments); err != nil {
		// Paginated output may be multiple JSON arrays concatenated.
		// Wrap in a single array by treating as NDJSON-ish.
		// Fallback: try to split on "][\n" boundaries.
		fixed := strings.ReplaceAll(string(out), "]\n[", ",")
		if err2 := json.Unmarshal([]byte(fixed), &comments); err2 != nil {
			return nil, fmt.Errorf("parse comments: %w (original: %v)", err2, err)
		}
	}

	return comments, nil
}

// extractBody pulls the "body" field from a GitHub comment JSON object.
func extractBody(raw json.RawMessage) (string, error) {
	var comment struct {
		Body string `json:"body"`
	}
	if err := json.Unmarshal(raw, &comment); err != nil {
		return "", err
	}
	return comment.Body, nil
}

// parseBroadcastsFromComments extracts Broadcast payloads from comment bodies.
// Only comments whose body parses as valid Broadcast JSON (with a non-empty ID)
// are returned. Everything else is silently ignored — the issue thread may
// contain human discussion alongside machine broadcasts.
func parseBroadcastsFromComments(comments []json.RawMessage) []Broadcast {
	var broadcasts []Broadcast
	for _, raw := range comments {
		body, err := extractBody(raw)
		if err != nil {
			continue
		}

		// The body might contain the JSON directly, or it might be
		// wrapped in a markdown code fence. Try both.
		body = strings.TrimSpace(body)
		if strings.HasPrefix(body, "```") {
			// Strip code fence markers
			lines := strings.Split(body, "\n")
			if len(lines) >= 3 {
				// Remove first and last lines (``` markers)
				body = strings.Join(lines[1:len(lines)-1], "\n")
			}
		}

		var broadcast Broadcast
		if err := json.Unmarshal([]byte(body), &broadcast); err != nil {
			continue
		}

		// Must have an ID to be a valid broadcast
		if broadcast.ID == "" {
			continue
		}

		broadcasts = append(broadcasts, broadcast)
	}
	return broadcasts
}

// publish posts a broadcast as a comment on the GitHub issue.
func publish(repo string, issue int, broadcast Broadcast) error {
	data, err := json.MarshalIndent(broadcast, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal broadcast: %w", err)
	}

	body := string(data)
	log.Printf("publishing broadcast %s to %s#%d", broadcast.ID, repo, issue)
	return ghComment(repo, issue, body)
}

// subscribe polls the GitHub issue for new broadcasts and writes them locally.
func subscribe(repo string, issue int, pollInterval time.Duration) error {
	seen := make(map[string]bool)

	// Initial fetch to populate the seen set without writing (avoids
	// replaying the entire history on first run).
	log.Printf("fetching existing comments from %s#%d...", repo, issue)
	comments, err := ghFetchComments(repo, issue)
	if err != nil {
		return fmt.Errorf("initial fetch: %w", err)
	}

	existing := parseBroadcastsFromComments(comments)
	for _, broadcast := range existing {
		seen[broadcast.ID] = true
	}
	log.Printf("found %d existing broadcasts, starting poll (interval %s)", len(existing), pollInterval)

	// Set up signal handling for graceful shutdown.
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-sigChan:
			log.Println("shutting down subscriber")
			return nil

		case <-ticker.C:
			comments, err := ghFetchComments(repo, issue)
			if err != nil {
				log.Printf("poll error: %v (will retry)", err)
				continue
			}

			broadcasts := parseBroadcastsFromComments(comments)
			newCount := 0
			for _, broadcast := range broadcasts {
				if seen[broadcast.ID] {
					continue
				}
				seen[broadcast.ID] = true
				newCount++

				fmt.Printf("[%s] %s conjecture=%s phase=%s status=%s files=%v\n",
					time.Now().Format("15:04:05"),
					broadcast.Agent,
					broadcast.ConjectureID,
					broadcast.Phase,
					broadcast.Status,
					broadcast.Files,
				)

				if err := writeBroadcast(broadcast); err != nil {
					log.Printf("write error: %v", err)
				}
			}

			if newCount > 0 {
				log.Printf("received %d new broadcast(s)", newCount)
			}
		}
	}
}

func main() {
	repo := flag.String("repo", "", "GitHub repository (owner/repo)")
	issue := flag.Int("issue", 0, "Issue number for the aq gossip thread")
	doPublish := flag.Bool("publish", false, "Publish a broadcast as an issue comment")
	doSubscribe := flag.Bool("subscribe", false, "Subscribe to broadcasts by polling issue comments")
	pollInterval := flag.Int("poll-interval", 30, "Poll interval in seconds (subscribe mode)")
	agent := flag.String("agent", "", "Agent address (e.g. origin/feat-auth)")
	conjecture := flag.String("conjecture", "C-0", "Conjecture ID")
	claim := flag.String("claim", "", "Conjecture claim")
	phase := flag.String("phase", "conjecture", "CPRR phase")
	status := flag.String("status", "prosecuting", "Broadcast status")
	files := flag.String("files", "", "Comma-separated files")
	ttl := flag.Int("ttl", 3600, "TTL in seconds")
	flag.Parse()

	if *repo == "" {
		fmt.Fprintf(os.Stderr, "error: -repo is required\n")
		os.Exit(1)
	}
	if *issue == 0 {
		fmt.Fprintf(os.Stderr, "error: -issue is required\n")
		os.Exit(1)
	}

	switch {
	case *doPublish:
		if *agent == "" {
			log.Fatal("-agent is required for publish")
		}

		worktree := "main"
		parts := strings.Split(*agent, "/")
		if len(parts) > 1 {
			worktree = parts[len(parts)-1]
		}

		var fileList []string
		if *files != "" {
			fileList = strings.Split(*files, ",")
		}

		broadcast := Broadcast{
			ID:              fmt.Sprintf("%d", time.Now().UnixMilli()),
			Agent:           *agent,
			Worktree:        worktree,
			ConjectureID:    *conjecture,
			ConjectureClaim: *claim,
			Phase:           *phase,
			Status:          *status,
			Files:           fileList,
			Timestamp:       float64(time.Now().Unix()),
			TTL:             *ttl,
		}

		if err := publish(*repo, *issue, broadcast); err != nil {
			log.Fatalf("publish failed: %v", err)
		}
		log.Printf("broadcast %s published to %s#%d", broadcast.ID, *repo, *issue)

	case *doSubscribe:
		interval := time.Duration(*pollInterval) * time.Second
		if err := subscribe(*repo, *issue, interval); err != nil {
			log.Fatalf("subscribe failed: %v", err)
		}

	default:
		fmt.Fprintf(os.Stderr, "error: specify -publish or -subscribe\n")
		flag.Usage()
		os.Exit(1)
	}
}
