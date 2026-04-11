//go:build ignore

// mesh.go — Meshtastic/LoRa transport plugin for aq
//
// Bridges aq broadcasts to Meshtastic mesh radio via the `meshtastic` CLI
// (serial) or `mosquitto_pub`/`mosquitto_sub` (MQTT bridge). Standalone
// binary, not compiled into the main aq binary.
//
// Physical proximity is a proxy for relevance. If your radio hears it,
// it's probably your concern. Walk into range and you're subscribed;
// walk away and you're not.
//
// Compact wire format (<=80 bytes, pipe-delimited):
//   1|<phase_code>|<agent_short>|<ts_delta>|<status_code>[|<payload>]
//
// Dependencies: stdlib only. Shells out to `meshtastic` or `mosquitto_pub`/
// `mosquitto_sub` for actual radio/MQTT I/O.
//
// Usage:
//   go run mesh.go -publish -agent jwalsh/feat-auth -conjecture C-42 \
//       -phase proof -files "api.py,auth.py"
//   go run mesh.go -subscribe -mqtt-host mqtt.meshtastic.org
//   go run mesh.go -publish -via serial -port /dev/cu.usbmodem1101 \
//       -agent jwalsh/fix-mqtt -conjecture C-7 -phase refutation -files monitor.py

package main

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// subprocessTimeout is the default timeout for meshtastic/mosquitto CLI calls.
const subprocessTimeout = 30 * time.Second

// ---------- Broadcast payload (matches main.go) ----------

// Broadcast is the aq broadcast payload. Simplified copy of main.go struct
// for the standalone mesh binary. Uses plain strings for phase/status
// since this is a bridge, not the canonical implementation.
type Broadcast struct {
	V               int      `json:"v,omitempty"`
	Agent           string   `json:"agent"`
	Host            string   `json:"host,omitempty"`
	User            string   `json:"user,omitempty"`
	Worktree        string   `json:"worktree"`
	ConjectureID    string   `json:"cid"`
	ConjectureClaim string   `json:"claim,omitempty"`
	Phase           string   `json:"phase"`
	Status          string   `json:"status"`
	Files           []string `json:"files,omitempty"`
	Ts              float64  `json:"ts"`
	TTL             int      `json:"ttl"`
	ID              string   `json:"id"`
}

// generateULID produces a 22-character ID matching the main aq binary.
func generateULID() string {
	ms := time.Now().UnixMilli()
	ts := fmt.Sprintf("%012x", ms)
	randomBytes := make([]byte, 5)
	_, _ = rand.Read(randomBytes)
	randomHex := hex.EncodeToString(randomBytes)
	return ts + randomHex
}

// ---------- Compact wire format ----------
//
// Meshtastic text payloads are limited to ~200 bytes after encryption.
// Full JSON broadcasts are 300-500 bytes. The compact format compresses
// to <=80 bytes using pipe-delimited fields with abbreviated values.
//
// v3 format: 3|<host>|<user>|<phase>|<status>|<cid>|<agent_short>|<ts_delta>[|<files>]
// v1 format: 1|<phase>|<agent_short>|<ts_delta>|<status>|<cid>[|<files>]
//
// v3 adds host + user (identity is mandatory), uses ts_delta from epoch
// base 2026-01-01 to save bytes. Receivers accept both v1 and v3.
//
// Field encoding:
//   phase_code:  c=conjecture, p=proof, r=refutation, n=refinement
//   status_code: a=prosecuting, d=done, b=blocked
//   ts_delta:    v3: seconds since 2026-01-01; v1: raw unix seconds
//   files:       comma-separated basenames (optional)

// epochBase is 2026-01-01T00:00:00Z for ts_delta encoding.
const epochBase int64 = 1767225600

var phaseToCode = map[string]string{
	"conjecture": "c",
	"proof":      "p",
	"refutation": "r",
	"refinement": "n",
}

var codeToPhase = map[string]string{
	"c": "conjecture",
	"p": "proof",
	"r": "refutation",
	"n": "refinement",
}

var statusToCode = map[string]string{
	"prosecuting": "a",
	"done":        "d",
	"blocked":     "b",
}

var codeToStatus = map[string]string{
	"a": "prosecuting",
	"d": "done",
	"b": "blocked",
}

// compactEncode serializes a Broadcast into the v3 compact wire format.
// Format: 3|<host>|<user>|<phase>|<status>|<cid>|<agent_short>|<ts_delta>[|<files>]
// Returns error if the result exceeds 200 bytes (Meshtastic frame limit).
func compactEncode(broadcast Broadcast) (string, error) {
	phaseCode, ok := phaseToCode[broadcast.Phase]
	if !ok {
		phaseCode = "c"
	}

	statusCode, ok := statusToCode[broadcast.Status]
	if !ok {
		statusCode = "a"
	}

	// Truncate agent to last 20 chars for wire efficiency.
	agentShort := broadcast.Agent
	if len(agentShort) > 20 {
		agentShort = agentShort[len(agentShort)-20:]
	}

	// Host and user: truncate to 8 chars, fall back to "?" if empty.
	host := broadcast.Host
	if host == "" {
		host = "?"
	}
	if len(host) > 8 {
		host = host[:8]
	}
	user := broadcast.User
	if user == "" {
		user = "?"
	}
	if len(user) > 8 {
		user = user[:8]
	}

	// ts_delta: seconds since 2026-01-01 epoch base.
	tsDelta := strconv.FormatInt(int64(broadcast.Ts)-epochBase, 10)

	cid := broadcast.ConjectureID

	parts := []string{
		"3",
		host,
		user,
		phaseCode,
		statusCode,
		cid,
		agentShort,
		tsDelta,
	}

	// Append basenames of files if present.
	if len(broadcast.Files) > 0 {
		basenames := make([]string, len(broadcast.Files))
		for i, filePath := range broadcast.Files {
			basenames[i] = filepath.Base(filePath)
		}
		parts = append(parts, strings.Join(basenames, ","))
	}

	encoded := strings.Join(parts, "|")

	if len(encoded) > 200 {
		// Drop files to fit.
		parts = parts[:8]
		encoded = strings.Join(parts, "|")
		if len(encoded) > 200 {
			return "", fmt.Errorf("compact payload exceeds 200 bytes even without files: %d bytes", len(encoded))
		}
	}

	return encoded, nil
}

// compactDecode parses a compact wire payload back into a Broadcast.
// Accepts both v1 and v3 compact formats — gossip in any accent.
//   v1: 1|<phase>|<agent_short>|<ts_raw>|<status>|<cid>[|<files>]
//   v3: 3|<host>|<user>|<phase>|<status>|<cid>|<agent_short>|<ts_delta>[|<files>]
func compactDecode(payload string) (Broadcast, error) {
	parts := strings.Split(payload, "|")
	if len(parts) < 6 {
		return Broadcast{}, fmt.Errorf("compact payload needs >=6 fields, got %d: %q", len(parts), payload)
	}

	version := parts[0]

	switch version {
	case "3":
		return compactDecodeV3(parts)
	case "1":
		return compactDecodeV1(parts)
	default:
		return Broadcast{}, fmt.Errorf("unknown compact version %q — maybe newer gossip?", version)
	}
}

// compactDecodeV3 decodes the v3 compact format with identity fields.
func compactDecodeV3(parts []string) (Broadcast, error) {
	if len(parts) < 8 {
		return Broadcast{}, fmt.Errorf("v3 compact needs >=8 fields, got %d", len(parts))
	}

	host := parts[1]
	user := parts[2]

	phase, ok := codeToPhase[parts[3]]
	if !ok {
		return Broadcast{}, fmt.Errorf("unknown phase code: %s", parts[3])
	}

	status, ok := codeToStatus[parts[4]]
	if !ok {
		return Broadcast{}, fmt.Errorf("unknown status code: %s", parts[4])
	}

	cid := parts[5]
	agentShort := parts[6]

	tsDelta, err := strconv.ParseInt(parts[7], 10, 64)
	if err != nil {
		return Broadcast{}, fmt.Errorf("invalid ts_delta: %w", err)
	}
	tsSeconds := tsDelta + epochBase

	var files []string
	if len(parts) > 8 && parts[8] != "" {
		files = strings.Split(parts[8], ",")
	}

	worktree := agentShort
	if idx := strings.LastIndex(agentShort, "/"); idx >= 0 {
		worktree = agentShort[idx+1:]
	}

	return Broadcast{
		V:            3,
		ID:           generateULID(),
		Host:         host,
		User:         user,
		Agent:        agentShort,
		Worktree:     worktree,
		ConjectureID: cid,
		Phase:        phase,
		Status:       status,
		Files:        files,
		Ts:           float64(tsSeconds),
		TTL:          3600,
	}, nil
}

// compactDecodeV1 decodes the legacy v1 compact format (no identity).
func compactDecodeV1(parts []string) (Broadcast, error) {
	phase, ok := codeToPhase[parts[1]]
	if !ok {
		return Broadcast{}, fmt.Errorf("unknown phase code: %s", parts[1])
	}

	agentShort := parts[2]

	tsSeconds, err := strconv.ParseInt(parts[3], 10, 64)
	if err != nil {
		return Broadcast{}, fmt.Errorf("invalid timestamp: %w", err)
	}

	status, ok := codeToStatus[parts[4]]
	if !ok {
		return Broadcast{}, fmt.Errorf("unknown status code: %s", parts[4])
	}

	conjectureID := parts[5]

	var files []string
	if len(parts) > 6 && parts[6] != "" {
		files = strings.Split(parts[6], ",")
	}

	worktree := agentShort
	if idx := strings.LastIndex(agentShort, "/"); idx >= 0 {
		worktree = agentShort[idx+1:]
	}

	return Broadcast{
		V:            1,
		ID:           generateULID(),
		Agent:        agentShort,
		Worktree:     worktree,
		ConjectureID: conjectureID,
		Phase:        phase,
		Status:       status,
		Files:        files,
		Ts:           float64(tsSeconds),
		TTL:          3600,
	}, nil
}

// ---------- Transport: serial via meshtastic CLI ----------

// checkBinary verifies a CLI binary is installed, returning a descriptive error if not.
func checkBinary(name, installHint string) error {
	_, err := exec.LookPath(name)
	if err != nil {
		return fmt.Errorf("[mesh] %s binary not found in PATH: %s", name, installHint)
	}
	return nil
}

// publishSerial sends a compact payload via the meshtastic CLI over serial.
// Shells out to: meshtastic --port <port> --sendtext '<payload>' --ch-index <ch>
func publishSerial(payload string, serialPort string, channelIndex int) error {
	ctx, cancel := context.WithTimeout(context.Background(), subprocessTimeout)
	defer cancel()

	channelStr := strconv.Itoa(channelIndex)
	cmd := exec.CommandContext(ctx, "meshtastic",
		"--port", serialPort,
		"--sendtext", payload,
		"--ch-index", channelStr,
	)
	cmd.Stderr = os.Stderr

	log.Printf("[mesh] TX: meshtastic --port %s --sendtext '%s' --ch-index %s",
		serialPort, payload, channelStr)

	if err := cmd.Run(); err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return fmt.Errorf("[mesh] TX: meshtastic send timed out after %v", subprocessTimeout)
		}
		return fmt.Errorf("[mesh] TX: meshtastic send failed: %w", err)
	}
	return nil
}

// ---------- Transport: MQTT via mosquitto CLI ----------

// meshMQTTTopic returns the Meshtastic MQTT topic for broadcasting.
// Standard Meshtastic topic hierarchy: msh/<region>/2/e/<channel_name>/<gateway_id>
func meshMQTTTopic(channelIndex int) string {
	return fmt.Sprintf("msh/US/2/e/LongFast/!aq%04d", channelIndex)
}

// meshMQTTWildcard returns the wildcard topic for subscribing.
func meshMQTTWildcard() string {
	return "msh/US/2/+/+"
}

// publishMQTT sends a compact payload via mosquitto_pub to the MQTT bridge.
func publishMQTT(payload string, mqttHost string, channelIndex int) error {
	ctx, cancel := context.WithTimeout(context.Background(), subprocessTimeout)
	defer cancel()

	topic := meshMQTTTopic(channelIndex)
	cmd := exec.CommandContext(ctx, "mosquitto_pub",
		"-h", mqttHost,
		"-t", topic,
		"-m", payload,
	)
	cmd.Stderr = os.Stderr

	log.Printf("[mesh] TX: mosquitto_pub -h %s -t '%s' -m '%s'",
		mqttHost, topic, payload)

	if err := cmd.Run(); err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return fmt.Errorf("[mesh] TX: mosquitto_pub timed out after %v", subprocessTimeout)
		}
		return fmt.Errorf("[mesh] TX: mosquitto_pub failed: %w", err)
	}
	return nil
}

// subscribeMQTT listens for compact payloads via mosquitto_sub and writes
// reconstructed Broadcast JSON to the aq requests directory.
func subscribeMQTT(mqttHost string, shutdown <-chan struct{}) error {
	topic := meshMQTTWildcard()
	cmd := exec.Command("mosquitto_sub",
		"-h", mqttHost,
		"-t", topic,
		"-v",
	)

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("[mesh] RX: pipe stdout: %w", err)
	}
	cmd.Stderr = os.Stderr

	log.Printf("[mesh] RX: mosquitto_sub -h %s -t '%s'", mqttHost, topic)

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("[mesh] RX: mosquitto_sub start: %w", err)
	}

	// Ensure subprocess is cleaned up.
	go func() {
		<-shutdown
		log.Println("[mesh] RX: shutting down mosquitto_sub...")
		_ = cmd.Process.Signal(syscall.SIGTERM)
	}()

	scanner := bufio.NewScanner(stdout)
	for scanner.Scan() {
		line := scanner.Text()
		// mosquitto_sub -v output: "<topic> <message>"
		spaceIdx := strings.Index(line, " ")
		if spaceIdx < 0 {
			continue
		}
		messagePayload := line[spaceIdx+1:]

		// Skip non-aq payloads: aq compact format starts with "1|" (v1) or "3|" (v3)
		if !strings.HasPrefix(messagePayload, "1|") && !strings.HasPrefix(messagePayload, "3|") {
			continue
		}

		broadcast, decodeErr := compactDecode(messagePayload)
		if decodeErr != nil {
			log.Printf("[mesh] RX: decode error: %v (payload: %q)", decodeErr, messagePayload)
			continue
		}

		if writeErr := writeBroadcast(broadcast); writeErr != nil {
			log.Printf("[mesh] RX: write error: %v", writeErr)
			continue
		}

		broadcastJSON, _ := json.Marshal(broadcast)
		log.Printf("[mesh] RX: [%s] %s", time.Now().Format("15:04:05"), string(broadcastJSON))
	}

	if scanErr := scanner.Err(); scanErr != nil && scanErr != io.EOF {
		log.Printf("[mesh] RX: scanner error: %v", scanErr)
	}

	return cmd.Wait()
}

// ---------- Local aq file I/O ----------

// aqRequestsDir returns the path to ~/.aq/channels/broadcast/requests/,
// creating it if it does not exist.
func aqRequestsDir() (string, error) {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("home dir: %w", err)
	}
	requestsDir := filepath.Join(homeDir, ".aq", "channels", "broadcast", "requests")
	if mkdirErr := os.MkdirAll(requestsDir, 0755); mkdirErr != nil {
		return "", fmt.Errorf("mkdir %s: %w", requestsDir, mkdirErr)
	}
	return requestsDir, nil
}

// writeBroadcast writes a reconstructed Broadcast as aq-<id>.json to
// the local requests directory. Uses atomic write (temp + rename)
// to prevent readers from seeing partial files.
func writeBroadcast(broadcast Broadcast) error {
	requestsDir, err := aqRequestsDir()
	if err != nil {
		return err
	}

	data, err := json.MarshalIndent(broadcast, "", "  ")
	if err != nil {
		return fmt.Errorf("[mesh] RX: marshal broadcast: %w", err)
	}

	filename := fmt.Sprintf("aq-%s.json", broadcast.ID)
	finalPath := filepath.Join(requestsDir, filename)

	// Atomic write: temp file + rename.
	tmpFile, tmpErr := os.CreateTemp(requestsDir, ".aq-tmp-*.json")
	if tmpErr != nil {
		return fmt.Errorf("[mesh] RX: create temp file: %w", tmpErr)
	}
	tmpPath := tmpFile.Name()

	if _, writeErr := tmpFile.Write(data); writeErr != nil {
		tmpFile.Close()
		os.Remove(tmpPath)
		return fmt.Errorf("[mesh] RX: write temp %s: %w", tmpPath, writeErr)
	}
	if err := tmpFile.Close(); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("[mesh] RX: close temp %s: %w", tmpPath, err)
	}
	if err := os.Rename(tmpPath, finalPath); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("[mesh] RX: rename %s -> %s: %w", tmpPath, finalPath, err)
	}

	log.Printf("[mesh] RX: wrote %s", finalPath)
	return nil
}

// ---------- main ----------

func main() {
	log.SetOutput(os.Stderr)

	// Mode flags.
	doPublish := flag.Bool("publish", false, "TX: send compact payload via Meshtastic")
	doSubscribe := flag.Bool("subscribe", false, "RX: listen via MQTT bridge, write aq-*.json locally")

	// Transport flags.
	via := flag.String("via", "mqtt", "Transport mode: serial or mqtt")
	serialPort := flag.String("port", "/dev/ttyUSB0", "Serial port for Meshtastic device")
	mqttHost := flag.String("mqtt-host", "mqtt.meshtastic.org", "MQTT broker host for bridge mode")
	channelIndex := flag.Int("channel", 1, "Meshtastic channel index (default 1; channel 0 is reserved for primary mesh use)")

	// Broadcast content flags.
	agent := flag.String("agent", "", "Agent address (e.g., jwalsh/feat-auth)")
	conjecture := flag.String("conjecture", "C-0", "Conjecture ID")
	claim := flag.String("claim", "", "Conjecture claim (intent)")
	phase := flag.String("phase", "conjecture", "CPRR phase: conjecture|proof|refutation|refinement")
	status := flag.String("status", "prosecuting", "Broadcast status: prosecuting|done|blocked")
	files := flag.String("files", "", "Comma-separated file list")

	flag.Parse()

	if !*doPublish && !*doSubscribe {
		fmt.Fprintf(os.Stderr, "specify -publish or -subscribe\n\n")
		flag.Usage()
		os.Exit(1)
	}

	if *channelIndex == 0 {
		log.Fatal("channel 0 is reserved for primary mesh use; aq uses channel >= 1")
	}

	// Check for required binaries based on transport mode.
	switch *via {
	case "serial":
		if err := checkBinary("meshtastic", "pip install meshtastic"); err != nil {
			log.Printf("%v", err)
			os.Exit(2)
		}
	case "mqtt":
		if *doPublish {
			if err := checkBinary("mosquitto_pub", "install mosquitto-clients"); err != nil {
				log.Printf("%v", err)
				os.Exit(2)
			}
		}
		if *doSubscribe {
			if err := checkBinary("mosquitto_sub", "install mosquitto-clients"); err != nil {
				log.Printf("%v", err)
				os.Exit(2)
			}
		}
	}

	switch {
	case *doPublish:
		if *agent == "" {
			log.Fatal("[mesh] -agent required for publish mode")
		}

		// Derive worktree from agent.
		worktree := *agent
		if idx := strings.LastIndex(*agent, "/"); idx >= 0 {
			worktree = (*agent)[idx+1:]
		}

		var fileList []string
		if *files != "" {
			fileList = strings.Split(*files, ",")
		}

		// Detect identity for v3.
		hostname, _ := os.Hostname()
		if hostname == "" {
			hostname = "unknown"
		}
		if idx := strings.Index(hostname, "."); idx > 0 {
			hostname = hostname[:idx]
		}
		username := os.Getenv("USER")
		if username == "" {
			username = "unknown"
		}

		broadcast := Broadcast{
			V:               3,
			ID:              generateULID(),
			Host:            hostname,
			User:            username,
			Agent:           *agent,
			Worktree:        worktree,
			ConjectureID:    *conjecture,
			ConjectureClaim: *claim,
			Phase:           *phase,
			Status:          *status,
			Files:           fileList,
			Ts:              float64(time.Now().Unix()),
			TTL:             3600,
		}

		payload, err := compactEncode(broadcast)
		if err != nil {
			log.Fatalf("compact encode: %v", err)
		}

		log.Printf("[mesh] TX: compact (%d bytes): %s", len(payload), payload)

		switch *via {
		case "serial":
			if err := publishSerial(payload, *serialPort, *channelIndex); err != nil {
				log.Fatalf("serial TX failed: %v", err)
			}
		case "mqtt":
			if err := publishMQTT(payload, *mqttHost, *channelIndex); err != nil {
				log.Fatalf("mqtt TX failed: %v", err)
			}
		default:
			log.Fatalf("unknown -via mode: %s (use serial or mqtt)", *via)
		}

		log.Println("[mesh] TX: ok")

	case *doSubscribe:
		if *via != "mqtt" {
			log.Fatal("subscribe mode requires -via mqtt (serial RX not implemented)")
		}

		shutdownChan := make(chan struct{})

		// Graceful shutdown on SIGINT/SIGTERM.
		signalChan := make(chan os.Signal, 1)
		signal.Notify(signalChan, syscall.SIGINT, syscall.SIGTERM)
		go func() {
			sig := <-signalChan
			log.Printf("[mesh] RX: received %v, shutting down...", sig)
			close(shutdownChan)
		}()

		log.Printf("[mesh] RX: listening for mesh broadcasts via MQTT (%s)...", *mqttHost)
		log.Println("[mesh] RX: broadcasts will be written to ~/.aq/channels/broadcast/requests/")
		log.Println("[mesh] RX: press Ctrl-C to stop")

		if err := subscribeMQTT(*mqttHost, shutdownChan); err != nil {
			log.Printf("[mesh] RX: subscribe ended: %v", err)
		}

		log.Println("[mesh] RX: shutdown complete")
	}
}
