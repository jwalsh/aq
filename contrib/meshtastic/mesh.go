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
// Format: 1|<phase_code>|<agent_short>|<ts_delta>|<status_code>[|<files>]
//
// Field encoding:
//   version:     always "1"
//   phase_code:  c=conjecture, p=proof, r=refutation, n=refinement
//   agent_short: last 20 chars of agent address
//   ts_delta:    seconds since epoch as compact decimal
//   status_code: a=prosecuting, d=done, b=blocked
//   files:       comma-separated basenames (optional)

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

// compactEncode serializes a Broadcast into the compact wire format.
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

	// Timestamp as compact integer (seconds since epoch).
	tsDelta := strconv.FormatInt(int64(broadcast.Ts), 10)

	// Build conjecture tag: "C-42" stays as-is.
	conjectureTag := broadcast.ConjectureID

	parts := []string{
		"1",
		phaseCode,
		agentShort,
		tsDelta,
		statusCode,
		conjectureTag,
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
		// Truncate files to fit within the Meshtastic frame.
		// Drop files and retry.
		parts = parts[:6]
		encoded = strings.Join(parts, "|")
		if len(encoded) > 200 {
			return "", fmt.Errorf("compact payload exceeds 200 bytes even without files: %d bytes", len(encoded))
		}
	}

	return encoded, nil
}

// compactDecode parses a compact wire payload back into a Broadcast.
// Reconstructs the full JSON-serializable struct for local storage.
func compactDecode(payload string) (Broadcast, error) {
	parts := strings.Split(payload, "|")
	if len(parts) < 6 {
		return Broadcast{}, fmt.Errorf("compact payload needs >=6 fields, got %d: %q", len(parts), payload)
	}

	version := parts[0]
	if version != "1" {
		return Broadcast{}, fmt.Errorf("unsupported compact version: %s", version)
	}

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

	// Derive worktree from agent address (last path component).
	worktree := agentShort
	if idx := strings.LastIndex(agentShort, "/"); idx >= 0 {
		worktree = agentShort[idx+1:]
	}

	broadcast := Broadcast{
		ID:           generateULID(),
		Agent:        agentShort,
		Worktree:     worktree,
		ConjectureID: conjectureID,
		Phase:        phase,
		Status:       status,
		Files:        files,
		Ts:           float64(tsSeconds),
		TTL:          3600,
	}

	return broadcast, nil
}

// ---------- Transport: serial via meshtastic CLI ----------

// publishSerial sends a compact payload via the meshtastic CLI over serial.
// Shells out to: meshtastic --port <port> --sendtext '<payload>' --ch-index <ch>
func publishSerial(payload string, serialPort string, channelIndex int) error {
	channelStr := strconv.Itoa(channelIndex)
	cmd := exec.Command("meshtastic",
		"--port", serialPort,
		"--sendtext", payload,
		"--ch-index", channelStr,
	)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	log.Printf("TX serial: meshtastic --port %s --sendtext '%s' --ch-index %s",
		serialPort, payload, channelStr)

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("meshtastic send failed: %w", err)
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
	topic := meshMQTTTopic(channelIndex)
	cmd := exec.Command("mosquitto_pub",
		"-h", mqttHost,
		"-t", topic,
		"-m", payload,
	)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	log.Printf("TX mqtt: mosquitto_pub -h %s -t '%s' -m '%s'",
		mqttHost, topic, payload)

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("mosquitto_pub failed: %w", err)
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
		return fmt.Errorf("pipe stdout: %w", err)
	}
	cmd.Stderr = os.Stderr

	log.Printf("RX mqtt: mosquitto_sub -h %s -t '%s'", mqttHost, topic)

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("mosquitto_sub start: %w", err)
	}

	// Ensure subprocess is cleaned up.
	go func() {
		<-shutdown
		log.Println("shutting down mosquitto_sub...")
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

		// Skip non-aq payloads: aq compact format starts with "1|"
		if !strings.HasPrefix(messagePayload, "1|") {
			continue
		}

		broadcast, decodeErr := compactDecode(messagePayload)
		if decodeErr != nil {
			log.Printf("decode error: %v (payload: %q)", decodeErr, messagePayload)
			continue
		}

		if writeErr := writeBroadcast(broadcast); writeErr != nil {
			log.Printf("write error: %v", writeErr)
			continue
		}

		broadcastJSON, _ := json.Marshal(broadcast)
		fmt.Printf("[%s] RX %s\n", time.Now().Format("15:04:05"), string(broadcastJSON))
	}

	if scanErr := scanner.Err(); scanErr != nil && scanErr != io.EOF {
		log.Printf("scanner error: %v", scanErr)
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
// the local requests directory.
func writeBroadcast(broadcast Broadcast) error {
	requestsDir, err := aqRequestsDir()
	if err != nil {
		return err
	}

	data, err := json.MarshalIndent(broadcast, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal broadcast: %w", err)
	}

	filename := fmt.Sprintf("aq-%s.json", broadcast.ID)
	outputPath := filepath.Join(requestsDir, filename)

	if writeErr := os.WriteFile(outputPath, data, 0644); writeErr != nil {
		return fmt.Errorf("write %s: %w", outputPath, writeErr)
	}

	log.Printf("wrote %s", outputPath)
	return nil
}

// ---------- main ----------

func main() {
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

	switch {
	case *doPublish:
		if *agent == "" {
			log.Fatal("-agent required for publish mode")
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
			TTL:             3600,
		}

		payload, err := compactEncode(broadcast)
		if err != nil {
			log.Fatalf("compact encode: %v", err)
		}

		fmt.Printf("compact (%d bytes): %s\n", len(payload), payload)

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

		fmt.Println("TX ok")

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
			log.Printf("received %v, shutting down...", sig)
			close(shutdownChan)
		}()

		fmt.Printf("listening for mesh broadcasts via MQTT (%s)...\n", *mqttHost)
		fmt.Println("broadcasts will be written to ~/.aq/channels/broadcast/requests/")
		fmt.Println("press Ctrl-C to stop")

		if err := subscribeMQTT(*mqttHost, shutdownChan); err != nil {
			log.Printf("subscribe ended: %v", err)
		}

		fmt.Println("shutdown complete")
	}
}
