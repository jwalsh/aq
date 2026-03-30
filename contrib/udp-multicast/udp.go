//go:build ignore

// udp.go -- UDP multicast transport plugin for aq
//
// True LAN gossip: send aq broadcast payloads as UDP datagrams on a
// link-local multicast group. No broker, no discovery, no accounts,
// no files. sendto(sock, payload, (group, port)) is the entire TX path.
//
// Tier 0.5 in the transport hierarchy -- between filesystem and mDNS.
// Sub-millisecond latency, zero dependencies, zero configuration beyond
// group address and port.
//
// Wire format: 4-byte frame header + JSON payload.
//   Bytes 0-1: Magic "AQ" (0x41 0x51)
//   Byte  2:   Version (0x01)
//   Byte  3:   Format  (0x01 = JSON)
//   Byte  4+:  JSON-encoded Broadcast
//
// Two modes:
//   -publish    TX: send broadcast JSON to multicast group
//   -subscribe  RX: listen on multicast group, write aq-*.json locally
//
// Usage:
//   go run udp.go -publish -agent jwalsh/main -conjecture C-1 \
//       -phase proof -files "cli.py,protocol.py"
//   go run udp.go -subscribe
//   go run udp.go -subscribe -group 239.42.0.1 -port 4271 -iface en0

package main

import (
	"crypto/rand"
	"encoding/hex"
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

// ---------- Broadcast payload (matches main.go) ----------

// Broadcast is the aq broadcast payload. Same schema as main.go.
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

// generateULID produces a 22-character hex ID matching the main aq binary.
// 12 hex chars of millisecond timestamp + 10 hex chars of randomness.
func generateULID() string {
	ms := time.Now().UnixMilli()
	ts := fmt.Sprintf("%012x", ms)

	randomBytes := make([]byte, 5)
	_, _ = rand.Read(randomBytes)
	randomHex := hex.EncodeToString(randomBytes)
	return ts + randomHex
}

// ---------- Frame header ----------
//
// 4-byte header per udp-multicast-transport.org section 3.2:
//   [0x41 0x51] "AQ" magic
//   [0x01]      version
//   [0x01]      format: JSON

var (
	frameMagic   = []byte{0x41, 0x51} // "AQ"
	frameVersion = byte(0x01)
	frameJSON    = byte(0x01)
)

// frameEncode wraps a JSON payload in the 4-byte multicast frame header.
func frameEncode(jsonPayload []byte) []byte {
	frame := make([]byte, 4+len(jsonPayload))
	copy(frame[0:2], frameMagic)
	frame[2] = frameVersion
	frame[3] = frameJSON
	copy(frame[4:], jsonPayload)
	return frame
}

// frameDecode strips the 4-byte header and returns the JSON payload.
// Returns the payload and true on success, nil and false on failure.
func frameDecode(datagram []byte) ([]byte, bool) {
	if len(datagram) < 5 {
		return nil, false
	}
	// Check magic bytes
	if datagram[0] != frameMagic[0] || datagram[1] != frameMagic[1] {
		return nil, false
	}
	// Check version
	if datagram[2] != frameVersion {
		log.Printf("[udp] RX: unknown frame version: 0x%02x", datagram[2])
		return nil, false
	}
	// Check format (only JSON supported)
	if datagram[3] != frameJSON {
		log.Printf("[udp] RX: unsupported frame format: 0x%02x", datagram[3])
		return nil, false
	}
	return datagram[4:], true
}

// ---------- Filesystem materialization ----------

// broadcastDir returns ~/.aq/channels/broadcast/requests/, creating it
// if it does not exist.
func broadcastDir() (string, error) {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("home dir: %w", err)
	}
	requestsDir := filepath.Join(homeDir, ".aq", "channels", "broadcast", "requests")
	if mkdirErr := os.MkdirAll(requestsDir, 0o755); mkdirErr != nil {
		return "", fmt.Errorf("mkdir %s: %w", requestsDir, mkdirErr)
	}
	return requestsDir, nil
}

// writeBroadcast serializes a Broadcast to JSON and writes it to the
// requests directory as aq-{ts}-{id}.json. Uses atomic write (temp + rename)
// to prevent readers from seeing partial files.
func writeBroadcast(broadcast Broadcast) error {
	requestsDir, err := broadcastDir()
	if err != nil {
		return err
	}

	data, err := json.MarshalIndent(broadcast, "", "  ")
	if err != nil {
		return fmt.Errorf("[udp] RX: marshal broadcast: %w", err)
	}

	filename := fmt.Sprintf("aq-%d-%s.json", time.Now().UnixMilli(), broadcast.ID)
	finalPath := filepath.Join(requestsDir, filename)

	// Atomic write: temp file + rename.
	tmpFile, tmpErr := os.CreateTemp(requestsDir, ".aq-tmp-*.json")
	if tmpErr != nil {
		return fmt.Errorf("[udp] RX: create temp file: %w", tmpErr)
	}
	tmpPath := tmpFile.Name()

	if _, writeErr := tmpFile.Write(data); writeErr != nil {
		tmpFile.Close()
		os.Remove(tmpPath)
		return fmt.Errorf("[udp] RX: write temp %s: %w", tmpPath, writeErr)
	}
	if err := tmpFile.Close(); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("[udp] RX: close temp %s: %w", tmpPath, err)
	}
	if err := os.Rename(tmpPath, finalPath); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("[udp] RX: rename %s -> %s: %w", tmpPath, finalPath, err)
	}

	log.Printf("[udp] RX: wrote %s", finalPath)
	return nil
}

// ---------- Deduplication ----------
//
// In-memory dedup by broadcast ID. Bounded to prevent unbounded growth.
// The dedup window is keyed on the (agent, id) tuple per the spec.

const maxDedupEntries = 1024

type dedupCache struct {
	seen map[string]time.Time
}

func newDedupCache() *dedupCache {
	return &dedupCache{seen: make(map[string]time.Time)}
}

// dedupKey returns the transport-agnostic dedup key: agent + id.
func dedupKey(broadcast Broadcast) string {
	return broadcast.Agent + "|" + broadcast.ID
}

// isDuplicate returns true if this broadcast has been seen before.
// Also prunes entries older than 5 minutes to bound memory.
func (dc *dedupCache) isDuplicate(broadcast Broadcast) bool {
	key := dedupKey(broadcast)

	// Prune stale entries if cache is getting large
	if len(dc.seen) > maxDedupEntries/2 {
		cutoff := time.Now().Add(-5 * time.Minute)
		for k, t := range dc.seen {
			if t.Before(cutoff) {
				delete(dc.seen, k)
			}
		}
	}

	if _, exists := dc.seen[key]; exists {
		return true
	}

	dc.seen[key] = time.Now()
	return false
}

// ---------- TX: publish ----------

// publish sends a single broadcast as a framed JSON datagram to the
// multicast group. Fire and forget -- no ACK, no retry.
func publish(group string, port int, ttlHops int, ifaceName string, broadcast Broadcast) error {
	jsonPayload, err := json.Marshal(broadcast)
	if err != nil {
		return fmt.Errorf("marshal: %w", err)
	}

	datagram := frameEncode(jsonPayload)

	if len(datagram) > 1472 {
		log.Printf("[udp] TX: warning: datagram is %d bytes, may fragment on Ethernet (MTU 1500)", len(datagram))
	}

	groupAddr := fmt.Sprintf("%s:%d", group, port)
	destAddr, err := net.ResolveUDPAddr("udp4", groupAddr)
	if err != nil {
		return fmt.Errorf("resolve %s: %w", groupAddr, err)
	}

	conn, err := net.DialUDP("udp4", nil, destAddr)
	if err != nil {
		return fmt.Errorf("dial %s: %w", groupAddr, err)
	}
	defer conn.Close()

	// Set multicast TTL (hop limit)
	rawConn, err := conn.SyscallConn()
	if err == nil {
		rawConn.Control(func(fd uintptr) {
			syscall.SetsockoptInt(int(fd), syscall.IPPROTO_IP, syscall.IP_MULTICAST_TTL, ttlHops)
		})
	}

	// Bind to specific interface if requested
	if ifaceName != "" {
		iface, ifErr := net.InterfaceByName(ifaceName)
		if ifErr != nil {
			return fmt.Errorf("interface %s: %w", ifaceName, ifErr)
		}
		rawConn, err := conn.SyscallConn()
		if err == nil {
			rawConn.Control(func(fd uintptr) {
				addrs, _ := iface.Addrs()
				for _, addr := range addrs {
					if ipNet, ok := addr.(*net.IPNet); ok && ipNet.IP.To4() != nil {
						copy([]byte{0, 0, 0, 0}, ipNet.IP.To4())
						syscall.SetsockoptInet4Addr(int(fd), syscall.IPPROTO_IP, syscall.IP_MULTICAST_IF, [4]byte(ipNet.IP.To4()))
						break
					}
				}
			})
		}
	}

	n, err := conn.Write(datagram)
	if err != nil {
		return fmt.Errorf("write: %w", err)
	}

	log.Printf("[udp] TX: %d bytes to %s (id=%s agent=%s conjecture=%s phase=%s)",
		n, groupAddr, broadcast.ID, broadcast.Agent, broadcast.ConjectureID, broadcast.Phase)
	return nil
}

// ---------- RX: subscribe ----------

// subscribe joins the multicast group and listens for datagrams indefinitely.
// Received broadcasts are deduped and written to the filesystem.
// Returns when the shutdown channel is closed or on unrecoverable error.
func subscribe(group string, port int, ifaceName string, selfAgent string, shutdown <-chan struct{}) error {
	groupAddr := fmt.Sprintf("%s:%d", group, port)
	gaddr, err := net.ResolveUDPAddr("udp4", groupAddr)
	if err != nil {
		return fmt.Errorf("resolve %s: %w", groupAddr, err)
	}

	// Resolve network interface (nil = all interfaces)
	var iface *net.Interface
	if ifaceName != "" {
		iface, err = net.InterfaceByName(ifaceName)
		if err != nil {
			return fmt.Errorf("interface %s: %w", ifaceName, err)
		}
		log.Printf("[udp] RX: binding to interface %s", ifaceName)
	}

	conn, err := net.ListenMulticastUDP("udp4", iface, gaddr)
	if err != nil {
		return fmt.Errorf("listen multicast %s: %w", groupAddr, err)
	}
	defer conn.Close()

	conn.SetReadBuffer(65536)

	log.Printf("[udp] RX: joined multicast group %s -- listening for broadcasts", groupAddr)
	if selfAgent != "" {
		log.Printf("[udp] RX: self-exclusion: filtering broadcasts from agent %q", selfAgent)
	}

	dedup := newDedupCache()
	buf := make([]byte, 65536)

	// Read loop in a goroutine so we can select on shutdown
	type datagramResult struct {
		data []byte
		src  *net.UDPAddr
		err  error
	}
	results := make(chan datagramResult, 16)

	go func() {
		for {
			n, src, readErr := conn.ReadFromUDP(buf)
			if readErr != nil {
				results <- datagramResult{err: readErr}
				return
			}
			// Copy the data since buf is reused
			dataCopy := make([]byte, n)
			copy(dataCopy, buf[:n])
			results <- datagramResult{data: dataCopy, src: src}
		}
	}()

	for {
		select {
		case <-shutdown:
			log.Println("[udp] RX: shutting down subscriber")
			return nil

		case result := <-results:
			if result.err != nil {
				return fmt.Errorf("[udp] RX: read: %w", result.err)
			}

			// Decode the frame header
			jsonPayload, ok := frameDecode(result.data)
			if !ok {
				// Not an aq frame, silently discard
				continue
			}

			// Parse the broadcast JSON
			var broadcast Broadcast
			if jsonErr := json.Unmarshal(jsonPayload, &broadcast); jsonErr != nil {
				log.Printf("[udp] RX: invalid JSON from %s: %v", result.src, jsonErr)
				continue
			}

			// Self-exclusion: skip our own broadcasts
			if selfAgent != "" && broadcast.Agent == selfAgent {
				continue
			}

			// Check TTL expiry
			now := float64(time.Now().Unix())
			if broadcast.TTL > 0 && now > broadcast.Ts+float64(broadcast.TTL) {
				log.Printf("[udp] RX: expired broadcast from %s (age=%.0fs, ttl=%d)",
					broadcast.Agent, now-broadcast.Ts, broadcast.TTL)
				continue
			}

			// Dedup
			if dedup.isDuplicate(broadcast) {
				continue
			}

			log.Printf("[udp] RX: from %s: agent=%s conjecture=%s phase=%s files=%v",
				result.src, broadcast.Agent, broadcast.ConjectureID, broadcast.Phase, broadcast.Files)

			// Materialize to filesystem
			if writeErr := writeBroadcast(broadcast); writeErr != nil {
				log.Printf("[udp] RX: write failed: %v", writeErr)
			}
		}
	}
}

// ---------- main ----------

func main() {
	log.SetOutput(os.Stderr)

	// Mode flags
	doPublish := flag.Bool("publish", false, "TX: send a broadcast to the multicast group")
	doSubscribe := flag.Bool("subscribe", false, "RX: listen for broadcasts, write to filesystem")

	// Multicast flags
	group := flag.String("group", "239.192.65.81", "Multicast group address")
	port := flag.Int("port", 4181, "Multicast port")
	ifaceName := flag.String("iface", "", "Network interface to bind (default: all)")
	ttlHops := flag.Int("ttl", 1, "Multicast TTL hops (1 = LAN only)")

	// Broadcast content flags (publish mode)
	agent := flag.String("agent", "", "Agent address (e.g. jwalsh/main)")
	conjecture := flag.String("conjecture", "C-0", "Conjecture ID")
	claim := flag.String("claim", "", "Conjecture claim (human-readable intent)")
	phase := flag.String("phase", "conjecture", "CPRR phase: conjecture|proof|refutation|refinement")
	status := flag.String("status", "prosecuting", "Status: prosecuting|done|blocked")
	files := flag.String("files", "", "Comma-separated file list")
	broadcastTTL := flag.Int("broadcast-ttl", 3600, "Broadcast TTL in seconds")

	flag.Parse()

	if !*doPublish && !*doSubscribe {
		fmt.Fprintf(os.Stderr, "Usage: go run udp.go -publish|-subscribe [flags]\n\n")
		fmt.Fprintf(os.Stderr, "Modes:\n")
		fmt.Fprintf(os.Stderr, "  -publish     Send a broadcast datagram to the multicast group\n")
		fmt.Fprintf(os.Stderr, "  -subscribe   Listen for broadcasts, write aq-*.json to filesystem\n\n")
		fmt.Fprintf(os.Stderr, "Multicast flags:\n")
		fmt.Fprintf(os.Stderr, "  -group       Multicast address (default 239.192.65.81)\n")
		fmt.Fprintf(os.Stderr, "  -port        Multicast port (default 4181)\n")
		fmt.Fprintf(os.Stderr, "  -iface       Network interface (default: all)\n")
		fmt.Fprintf(os.Stderr, "  -ttl         Multicast TTL hops (default 1 = LAN only)\n\n")
		flag.PrintDefaults()
		os.Exit(1)
	}

	switch {
	case *doPublish:
		if *agent == "" {
			log.Fatal("-agent is required for publish mode")
		}

		// Derive worktree from agent address (last path component)
		worktree := *agent
		if idx := strings.LastIndex(*agent, "/"); idx >= 0 {
			worktree = (*agent)[idx+1:]
		}

		var fileList []string
		if *files != "" {
			fileList = strings.Split(*files, ",")
		}

		broadcast := Broadcast{
			ID:              generateULID(),
			Agent:           *agent,
			Worktree:        worktree,
			ConjectureID:    *conjecture,
			ConjectureClaim: *claim,
			Phase:           *phase,
			Status:          *status,
			Files:           fileList,
			Ts:              float64(time.Now().Unix()),
			TTL:             *broadcastTTL,
		}

		if err := publish(*group, *port, *ttlHops, *ifaceName, broadcast); err != nil {
			log.Fatalf("publish failed: %v", err)
		}

	case *doSubscribe:
		shutdownChan := make(chan struct{})

		signalChan := make(chan os.Signal, 1)
		signal.Notify(signalChan, syscall.SIGINT, syscall.SIGTERM)
		go func() {
			sig := <-signalChan
			log.Printf("[udp] RX: received %v, shutting down...", sig)
			close(shutdownChan)
		}()

		// Use agent flag for self-exclusion if provided
		selfAgent := *agent

		log.Printf("[udp] RX: listening on %s:%d (TTL=%d)", *group, *port, *ttlHops)
		log.Println("[udp] RX: broadcasts will be written to ~/.aq/channels/broadcast/requests/")
		log.Println("[udp] RX: press Ctrl-C to stop")

		if err := subscribe(*group, *port, *ifaceName, selfAgent, shutdownChan); err != nil {
			log.Printf("[udp] RX: subscribe ended: %v", err)
		}

		log.Println("[udp] RX: shutdown complete")
	}
}
