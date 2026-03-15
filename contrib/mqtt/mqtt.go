//go:build ignore

// mqtt.go — MQTT transport sketch for aq
//
// Implements the Channel interface over MQTT using QoS 0 (fire-and-forget).
// This is a design sketch, not compiled into the main binary.
//
// Key mapping:
//   aq announce  → MQTT PUBLISH (QoS 0, Retain=true)
//   aq status    → MQTT SUBSCRIBE + retained message cache
//   aq disconnect → MQTT Will message (auto status=done)
//
// Dependencies:
//   go get github.com/eclipse/paho.mqtt.golang
//
// Usage:
//   go run mqtt.go -broker tcp://localhost:1883 -publish -agent origin/feat-auth \
//       -conjecture C-1 -phase proof -files "auth.py"
//   go run mqtt.go -broker tcp://localhost:1883 -subscribe

package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"
)

// Broadcast is the aq broadcast payload. Same schema as main.go.
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

// MQTTChannel implements the Channel interface over MQTT.
//
// Publish → mqtt.Publish with QoS 0, Retain true
// Subscribe → mqtt.Subscribe with message handler
// Active → local cache populated by retained messages + live subscriptions
type MQTTChannel struct {
	client   mqtt.Client
	repo     string
	mu       sync.RWMutex
	active   map[string]Broadcast // keyed by agent address
	handlers []func(Broadcast)
}

// topicForBranch returns the MQTT topic for a given branch.
// Topic hierarchy: aq/{repo}/{branch}/presence
func topicForBranch(repo, branch string) string {
	return fmt.Sprintf("aq/%s/%s/presence", repo, branch)
}

// topicWildcard returns the MQTT wildcard topic to see all branches.
// Uses + (single-level wildcard) per MQTT spec.
func topicWildcard(repo string) string {
	return fmt.Sprintf("aq/%s/+/presence", repo)
}

// topicGlobal returns the wildcard topic to see all repos and branches.
func topicGlobal() string {
	return "aq/+/+/presence"
}

// NewMQTTChannel creates a channel connected to the given broker.
// The Will message ensures that if this client disconnects unexpectedly,
// the broker publishes a status=done broadcast on its behalf. This
// solves C-7 (heartbeat/TTL-cliff) without a daemon.
func NewMQTTChannel(brokerURL, repo, agent, branch string) (*MQTTChannel, error) {
	ch := &MQTTChannel{
		repo:   repo,
		active: make(map[string]Broadcast),
	}

	// Will message: auto-announce done on unexpected disconnect.
	willPayload := Broadcast{
		Agent:     agent,
		Worktree:  branch,
		Status:    "done",
		Timestamp: float64(time.Now().Unix()),
	}
	willBytes, _ := json.Marshal(willPayload)
	willTopic := topicForBranch(repo, branch)

	opts := mqtt.NewClientOptions().
		AddBroker(brokerURL).
		SetClientID(fmt.Sprintf("aq-%s-%d", agent, time.Now().UnixMilli())).
		SetWill(willTopic, string(willBytes), 0 /* QoS 0 */, true /* retain */).
		SetAutoReconnect(true).
		SetOnConnectHandler(func(c mqtt.Client) {
			log.Printf("connected to %s", brokerURL)
		}).
		SetConnectionLostHandler(func(c mqtt.Client, err error) {
			log.Printf("connection lost: %v (will message sent by broker)", err)
		})

	ch.client = mqtt.NewClient(opts)
	if token := ch.client.Connect(); token.Wait() && token.Error() != nil {
		return nil, fmt.Errorf("mqtt connect: %w", token.Error())
	}

	return ch, nil
}

// Publish sends a broadcast to the MQTT topic for this agent's branch.
// QoS 0 (fire-and-forget), Retain true (so latecomers see current state).
func (ch *MQTTChannel) Publish(b Broadcast) error {
	data, err := json.Marshal(b)
	if err != nil {
		return fmt.Errorf("marshal broadcast: %w", err)
	}

	topic := topicForBranch(ch.repo, b.Worktree)
	token := ch.client.Publish(
		topic,
		0,    // QoS 0: fire-and-forget, gossip semantics
		true, // Retain: last known state for new subscribers
		data,
	)
	// Even though QoS 0 doesn't guarantee delivery, the Paho client
	// uses token.Wait() to confirm the message was handed to the
	// network stack. This is local confirmation, not delivery receipt.
	token.Wait()
	return token.Error()
}

// Subscribe registers a handler for incoming broadcasts and populates
// the active cache. Subscribes to the wildcard topic for the repo
// so all branches are visible.
func (ch *MQTTChannel) Subscribe(handler func(Broadcast)) error {
	ch.mu.Lock()
	ch.handlers = append(ch.handlers, handler)
	ch.mu.Unlock()

	topic := topicWildcard(ch.repo)
	token := ch.client.Subscribe(topic, 0, func(c mqtt.Client, msg mqtt.Message) {
		var b Broadcast
		if err := json.Unmarshal(msg.Payload(), &b); err != nil {
			log.Printf("ignoring malformed broadcast on %s: %v", msg.Topic(), err)
			return
		}

		ch.mu.Lock()
		if b.Status == "done" {
			delete(ch.active, b.Agent)
		} else {
			ch.active[b.Agent] = b
		}
		handlers := make([]func(Broadcast), len(ch.handlers))
		copy(handlers, ch.handlers)
		ch.mu.Unlock()

		for _, h := range handlers {
			h(b)
		}
	})
	token.Wait()
	return token.Error()
}

// Active returns all currently known broadcasts. The cache is populated
// by retained messages received on subscribe plus any live publishes.
func (ch *MQTTChannel) Active() ([]Broadcast, error) {
	ch.mu.RLock()
	defer ch.mu.RUnlock()

	result := make([]Broadcast, 0, len(ch.active))
	for _, b := range ch.active {
		result = append(result, b)
	}
	return result, nil
}

// Close disconnects from the broker. The Will message is NOT sent on
// clean disconnect — only on unexpected disconnect. For clean shutdown,
// publish a status=done broadcast before calling Close.
func (ch *MQTTChannel) Close() {
	ch.client.Disconnect(250) // 250ms quiesce
}

func main() {
	broker := flag.String("broker", "tcp://localhost:1883", "MQTT broker URL")
	doPublish := flag.Bool("publish", false, "Publish a broadcast")
	doSubscribe := flag.Bool("subscribe", false, "Subscribe to broadcasts")
	agent := flag.String("agent", "", "Agent address")
	repo := flag.String("repo", "github.com/jwalsh/aq", "Repository identifier")
	conjecture := flag.String("conjecture", "C-0", "Conjecture ID")
	claim := flag.String("claim", "", "Conjecture claim")
	phase := flag.String("phase", "conjecture", "CPRR phase")
	files := flag.String("files", "", "Comma-separated files")
	flag.Parse()

	branch := "main"
	if *agent != "" {
		parts := strings.Split(*agent, "/")
		branch = parts[len(parts)-1]
	}

	switch {
	case *doPublish:
		if *agent == "" {
			log.Fatal("-agent required")
		}
		ch, err := NewMQTTChannel(*broker, *repo, *agent, branch)
		if err != nil {
			log.Fatal(err)
		}
		defer ch.Close()

		var fileList []string
		if *files != "" {
			fileList = strings.Split(*files, ",")
		}

		b := Broadcast{
			ID:              fmt.Sprintf("%d", time.Now().UnixMilli()),
			Agent:           *agent,
			Worktree:        branch,
			ConjectureID:    *conjecture,
			ConjectureClaim: *claim,
			Phase:           *phase,
			Status:          "prosecuting",
			Files:           fileList,
			Timestamp:       float64(time.Now().Unix()),
			TTL:             3600,
		}
		if err := ch.Publish(b); err != nil {
			log.Fatalf("publish failed: %v", err)
		}
		fmt.Printf("published to %s\n", topicForBranch(*repo, branch))

	case *doSubscribe:
		ch, err := NewMQTTChannel(*broker, *repo, *agent, branch)
		if err != nil {
			log.Fatal(err)
		}
		defer ch.Close()

		err = ch.Subscribe(func(b Broadcast) {
			fmt.Printf("[%s] %s conjecture=%s phase=%s status=%s files=%v\n",
				time.Now().Format("15:04:05"), b.Agent, b.ConjectureID, b.Phase, b.Status, b.Files)
		})
		if err != nil {
			log.Fatalf("subscribe failed: %v", err)
		}

		fmt.Printf("subscribed to %s — waiting for broadcasts (Ctrl-C to quit)\n", topicWildcard(*repo))
		sig := make(chan os.Signal, 1)
		signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
		<-sig
		fmt.Println("\nshutting down")

	default:
		fmt.Fprintf(os.Stderr, "specify -publish or -subscribe\n")
		os.Exit(1)
	}
}
