//go:build ignore

// irc.go — IRC transport plugin for aq
//
// Raw RFC 1459 IRC client over TCP. No external dependencies.
// Bridges aq broadcasts to/from an IRC channel using the AMTP compact
// wire format defined in irc-transport.org §2.1:
//
//   aq:{agent} {conjecture_id} [{phase}] {files}
//
// Two modes:
//   -publish    TX: serialize broadcast, send as PRIVMSG, disconnect
//   -subscribe  RX: join channel, parse incoming PRIVMSGs, write JSON
//               to ~/.aq/channels/broadcast/requests/
//
// Usage:
//   go run irc.go -publish -server localhost:6999 -agent jwalsh/main \
//       -conjecture C-42 -phase proof -files "cli.py,protocol.py"
//   go run irc.go -subscribe -server localhost:6999

package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

// Broadcast is the aq broadcast payload. Matches main.go.
type Broadcast struct {
	ID              string   `json:"id"`
	Agent           string   `json:"agent"`
	Worktree        string   `json:"worktree"`
	ConjectureID    string   `json:"conjecture_id"`
	ConjectureClaim string   `json:"conjecture_claim,omitempty"`
	Phase           string   `json:"phase"`
	Status          string   `json:"status"`
	Files           []string `json:"files,omitempty"`
	Timestamp       float64  `json:"ts"`
	TTL             int      `json:"ttl"`
}

// ircConn wraps a TCP connection with line-oriented read/write for IRC.
type ircConn struct {
	conn    net.Conn
	scanner *bufio.Scanner
}

// dial opens a TCP connection to the IRC server with a 5-second timeout.
func dial(address string) (*ircConn, error) {
	conn, err := net.DialTimeout("tcp", address, 5*time.Second)
	if err != nil {
		return nil, fmt.Errorf("dial %s: %w", address, err)
	}
	scanner := bufio.NewScanner(conn)
	return &ircConn{conn: conn, scanner: scanner}, nil
}

// send writes a raw IRC protocol line (appends \r\n).
func (c *ircConn) send(format string, args ...interface{}) error {
	line := fmt.Sprintf(format, args...)
	_, err := fmt.Fprintf(c.conn, "%s\r\n", line)
	if err != nil {
		return fmt.Errorf("send: %w", err)
	}
	return nil
}

// readLine reads the next line from the server. Blocks until data arrives.
func (c *ircConn) readLine() (string, error) {
	if c.scanner.Scan() {
		return strings.TrimRight(c.scanner.Text(), "\r\n"), nil
	}
	if err := c.scanner.Err(); err != nil {
		return "", err
	}
	return "", fmt.Errorf("connection closed")
}

// close shuts down the connection.
func (c *ircConn) close() {
	c.conn.Close()
}

// register sends NICK and USER commands to identify with the server.
func (c *ircConn) register(nick string) error {
	if err := c.send("NICK %s", nick); err != nil {
		return err
	}
	return c.send("USER %s 0 * :aq presence bot", nick)
}

// join sends a JOIN command for the given channel.
func (c *ircConn) join(channel string) error {
	return c.send("JOIN %s", channel)
}

// privmsg sends a PRIVMSG to the target (channel or nick).
func (c *ircConn) privmsg(target, message string) error {
	return c.send("PRIVMSG %s :%s", target, message)
}

// quit sends a QUIT command with the given reason.
func (c *ircConn) quit(reason string) error {
	return c.send("QUIT :%s", reason)
}

// --- AMTP compact format ---
//
// TX:  aq:{agent} {conjecture_id} [{phase}] {files}
// The format is human-readable and fits within IRC's line limit.

// encodeCompact serializes a Broadcast to the AMTP compact wire format.
func encodeCompact(b Broadcast) string {
	var sb strings.Builder
	sb.WriteString("aq:")
	sb.WriteString(b.Agent)
	sb.WriteString(" ")
	sb.WriteString(b.ConjectureID)
	sb.WriteString(" [")
	sb.WriteString(b.Phase)
	sb.WriteString("]")
	if len(b.Files) > 0 {
		sb.WriteString(" ")
		sb.WriteString(strings.Join(b.Files, ","))
	}
	return sb.String()
}

// decodeCompact parses the AMTP compact wire format back into a Broadcast.
// Returns the broadcast and true on success, zero value and false on failure.
func decodeCompact(line string) (Broadcast, bool) {
	// Expected: aq:{agent} {conjecture_id} [{phase}] {files}
	if !strings.HasPrefix(line, "aq:") {
		return Broadcast{}, false
	}
	rest := line[3:] // strip "aq:"

	// Split into tokens. We need at least: agent, conjecture_id, [phase]
	parts := strings.Fields(rest)
	if len(parts) < 3 {
		return Broadcast{}, false
	}

	agent := parts[0]
	conjectureID := parts[1]
	phaseToken := parts[2] // e.g. "[proof]"

	// Extract phase from brackets
	if !strings.HasPrefix(phaseToken, "[") || !strings.HasSuffix(phaseToken, "]") {
		return Broadcast{}, false
	}
	phase := phaseToken[1 : len(phaseToken)-1]

	// Optional: files (comma-separated)
	var files []string
	if len(parts) > 3 {
		files = strings.Split(parts[3], ",")
	}

	// Derive worktree from agent (last component after /)
	worktree := agent
	if idx := strings.LastIndex(agent, "/"); idx >= 0 {
		worktree = agent[idx+1:]
	}

	now := float64(time.Now().Unix())
	return Broadcast{
		ID:              fmt.Sprintf("%d", time.Now().UnixMilli()),
		Agent:           agent,
		Worktree:        worktree,
		ConjectureID:    conjectureID,
		ConjectureClaim: "(received via irc)",
		Phase:           phase,
		Status:          "prosecuting",
		Files:           files,
		Timestamp:       now,
		TTL:             300, // 5 minutes for IRC-received broadcasts
	}, true
}

// --- Filesystem materialization ---

// broadcastDir returns the directory where received broadcasts are written.
func broadcastDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		home = os.TempDir()
	}
	return filepath.Join(home, ".aq", "channels", "broadcast", "requests")
}

// writeBroadcast serializes a Broadcast to JSON and writes it to the
// requests directory as aq-{ts}-{id}.json.
func writeBroadcast(b Broadcast) error {
	dir := broadcastDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("mkdir %s: %w", dir, err)
	}

	filename := fmt.Sprintf("aq-%d-%s.json", time.Now().UnixMilli(), b.ID)
	path := filepath.Join(dir, filename)

	data, err := json.MarshalIndent(b, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal: %w", err)
	}

	if err := os.WriteFile(path, data, 0o644); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}

	log.Printf("wrote %s", path)
	return nil
}

// --- Deduplication ---

// dedupKey returns a string key for deduplication: agent+conjecture+phase.
func dedupKey(b Broadcast) string {
	return b.Agent + "|" + b.ConjectureID + "|" + b.Phase
}

// isDuplicate checks the requests directory for an existing non-expired
// broadcast with the same agent, conjecture, and phase.
func isDuplicate(b Broadcast) bool {
	dir := broadcastDir()
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false
	}

	key := dedupKey(b)
	now := float64(time.Now().Unix())

	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, entry.Name()))
		if err != nil {
			continue
		}
		var existing Broadcast
		if err := json.Unmarshal(data, &existing); err != nil {
			continue
		}
		// Skip expired broadcasts
		if now > existing.Timestamp+float64(existing.TTL) {
			continue
		}
		if dedupKey(existing) == key {
			return true
		}
	}
	return false
}

// --- PRIVMSG parsing ---

// parsePrivmsg extracts the sender nick and message body from a raw
// IRC PRIVMSG line. Returns nick, message, true on success.
//
// Format: :nick!user@host PRIVMSG #channel :message body
func parsePrivmsg(line string) (nick string, message string, ok bool) {
	if !strings.Contains(line, "PRIVMSG") {
		return "", "", false
	}

	// Split into prefix and rest
	if !strings.HasPrefix(line, ":") {
		return "", "", false
	}

	// Find the sender (before first space)
	spaceIdx := strings.Index(line, " ")
	if spaceIdx < 0 {
		return "", "", false
	}
	prefix := line[1:spaceIdx] // strip leading ':'

	// Extract nick from nick!user@host
	if bangIdx := strings.Index(prefix, "!"); bangIdx > 0 {
		nick = prefix[:bangIdx]
	} else {
		nick = prefix
	}

	// Find "PRIVMSG"
	rest := line[spaceIdx+1:]
	if !strings.HasPrefix(rest, "PRIVMSG ") {
		return "", "", false
	}
	rest = rest[8:] // skip "PRIVMSG "

	// Find the message body (after " :")
	colonIdx := strings.Index(rest, " :")
	if colonIdx < 0 {
		return "", "", false
	}
	message = rest[colonIdx+2:]

	return nick, message, true
}

// --- Publish mode ---

// publish connects to IRC, sends a single PRIVMSG with the broadcast
// in AMTP compact format, then disconnects. Fire-and-forget.
func publish(address, channel, nick string, broadcast Broadcast) error {
	conn, err := dial(address)
	if err != nil {
		return err
	}
	defer conn.close()

	if err := conn.register(nick); err != nil {
		return fmt.Errorf("register: %w", err)
	}

	// Brief delay for server to process NICK/USER (see spec §2.3)
	time.Sleep(100 * time.Millisecond)

	if err := conn.join(channel); err != nil {
		return fmt.Errorf("join: %w", err)
	}

	// Brief delay for JOIN to be processed
	time.Sleep(100 * time.Millisecond)

	payload := encodeCompact(broadcast)
	if err := conn.privmsg(channel, payload); err != nil {
		return fmt.Errorf("privmsg: %w", err)
	}

	log.Printf("sent to %s: %s", channel, payload)

	if err := conn.quit("aq broadcast complete"); err != nil {
		return fmt.Errorf("quit: %w", err)
	}

	return nil
}

// --- Subscribe mode ---

// subscribe connects to IRC and listens for PRIVMSGs indefinitely.
// Parsed aq broadcasts are written to the filesystem. Handles PING/PONG.
// Returns on error or when the shutdown channel is closed.
func subscribe(address, channel, nick string, shutdown <-chan struct{}) error {
	conn, err := dial(address)
	if err != nil {
		return err
	}
	defer conn.close()

	if err := conn.register(nick); err != nil {
		return fmt.Errorf("register: %w", err)
	}

	// Wait for welcome (consume server messages until 001 or timeout)
	welcomeDeadline := time.Now().Add(10 * time.Second)
	conn.conn.SetReadDeadline(welcomeDeadline)
	for {
		line, err := conn.readLine()
		if err != nil {
			// Timeout or error: proceed anyway, JOIN might still work
			break
		}
		// Handle PING during registration
		if strings.HasPrefix(line, "PING") {
			pongPayload := strings.TrimPrefix(line, "PING ")
			conn.send("PONG %s", pongPayload)
		}
		// 001 = RPL_WELCOME, registration complete
		if strings.Contains(line, " 001 ") {
			break
		}
	}

	// Clear the deadline for the main loop
	conn.conn.SetReadDeadline(time.Time{})

	if err := conn.join(channel); err != nil {
		return fmt.Errorf("join: %w", err)
	}

	log.Printf("joined %s on %s — listening for broadcasts", channel, address)

	// Main read loop with shutdown awareness
	errChan := make(chan error, 1)
	lineChan := make(chan string, 16)

	go func() {
		for {
			line, err := conn.readLine()
			if err != nil {
				errChan <- err
				return
			}
			lineChan <- line
		}
	}()

	for {
		select {
		case <-shutdown:
			log.Println("shutting down subscriber")
			conn.quit("aq subscriber shutdown")
			return nil

		case err := <-errChan:
			return fmt.Errorf("read: %w", err)

		case line := <-lineChan:
			// PING/PONG keepalive
			if strings.HasPrefix(line, "PING") {
				pongPayload := strings.TrimPrefix(line, "PING ")
				if err := conn.send("PONG %s", pongPayload); err != nil {
					log.Printf("pong failed: %v", err)
				}
				continue
			}

			// Parse PRIVMSG
			senderNick, message, ok := parsePrivmsg(line)
			if !ok {
				continue
			}

			// Skip our own messages
			if senderNick == nick {
				continue
			}

			// Try to decode AMTP compact format
			broadcast, ok := decodeCompact(message)
			if !ok {
				continue
			}

			log.Printf("received from %s: %s", senderNick, message)

			// Dedup check
			if isDuplicate(broadcast) {
				log.Printf("skipping duplicate: %s", dedupKey(broadcast))
				continue
			}

			// Materialize to filesystem
			if err := writeBroadcast(broadcast); err != nil {
				log.Printf("write failed: %v", err)
			}
		}
	}
}

func main() {
	server := flag.String("server", "localhost:6999", "IRC server address (host:port)")
	channel := flag.String("channel", "#aq-presence", "IRC channel to join")
	nick := flag.String("nick", "", "IRC nickname (default: aq-{pid})")
	doPublish := flag.Bool("publish", false, "TX: send a broadcast as an IRC message")
	doSubscribe := flag.Bool("subscribe", false, "RX: listen for broadcasts and write to filesystem")

	// Broadcast fields (publish mode)
	agent := flag.String("agent", "", "Agent address (e.g. jwalsh/main)")
	conjecture := flag.String("conjecture", "C-0", "Conjecture ID")
	claim := flag.String("claim", "", "Conjecture claim (human-readable intent)")
	phase := flag.String("phase", "conjecture", "CPRR phase: conjecture|proof|refutation|refinement")
	status := flag.String("status", "prosecuting", "Status: prosecuting|done|blocked")
	files := flag.String("files", "", "Comma-separated file list")

	flag.Parse()

	// Default nick includes PID to avoid collisions
	if *nick == "" {
		*nick = fmt.Sprintf("aq-%d", os.Getpid())
	}

	switch {
	case *doPublish:
		if *agent == "" {
			log.Fatal("-agent is required for publish mode")
		}

		worktree := *agent
		if idx := strings.LastIndex(*agent, "/"); idx >= 0 {
			worktree = (*agent)[idx+1:]
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
			TTL:             3600,
		}

		if err := publish(*server, *channel, *nick, broadcast); err != nil {
			log.Fatalf("publish failed: %v", err)
		}

	case *doSubscribe:
		shutdown := make(chan struct{})
		sig := make(chan os.Signal, 1)
		signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)

		go func() {
			<-sig
			fmt.Println("\nreceived signal, shutting down...")
			close(shutdown)
		}()

		// Reconnect loop
		for {
			err := subscribe(*server, *channel, *nick, shutdown)
			if err == nil {
				// Clean shutdown
				break
			}

			// Check if we're shutting down
			select {
			case <-shutdown:
				break
			default:
			}

			log.Printf("disconnected: %v — reconnecting in 5s", err)
			select {
			case <-shutdown:
				return
			case <-time.After(5 * time.Second):
				// Reconnect
			}
		}

	default:
		fmt.Fprintf(os.Stderr, "Usage: go run irc.go -publish|-subscribe [flags]\n\n")
		fmt.Fprintf(os.Stderr, "Modes:\n")
		fmt.Fprintf(os.Stderr, "  -publish     Send a broadcast as an IRC PRIVMSG\n")
		fmt.Fprintf(os.Stderr, "  -subscribe   Listen for broadcasts, write to filesystem\n\n")
		flag.PrintDefaults()
		os.Exit(1)
	}
}
