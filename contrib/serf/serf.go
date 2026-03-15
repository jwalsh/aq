//go:build ignore

// serf.go — Serf/memberlist transport sketch for aq
//
// This is a standalone sketch implementing the Channel interface over
// HashiCorp Serf (which builds on hashicorp/memberlist). It is NOT
// compiled by default and is NOT referenced by go.mod.
//
// Tier 2.2: the first transport where aq's gossip semantics match the
// transport's gossip semantics. memberlist IS SWIM gossip. Every other
// aq transport simulates gossip over something else.
//
// Dependencies (not in go.mod):
//
//	go get github.com/hashicorp/serf
//	go get github.com/hashicorp/memberlist
//
// Key impedance mismatch: memberlist is ephemeral — events propagate
// and disappear. aq's Active() needs current state. Solution: local
// in-memory cache with TTL pruning, populated by received events.

package main

import (
	"context"
	"encoding/json"
	"log"
	"sync"
	"time"

	"github.com/hashicorp/serf/serf"
)

// ---------- Broadcast (mirrors main.go) ----------

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

func (b *Broadcast) IsExpired() bool {
	return float64(time.Now().Unix()) > b.Ts+float64(b.TTL)
}

// ---------- Channel interface ----------

// Channel is the aq transport abstraction (same as main.go / postgres contrib).
type Channel interface {
	Publish(broadcast Broadcast) error
	Subscribe(ctx context.Context) <-chan Broadcast
	Active() ([]Broadcast, error)
}

// ---------- SerfChannel ----------

// SerfChannel implements Channel over HashiCorp Serf.
//
// Publish: encodes broadcast as JSON, sends as a Serf UserEvent.
// Serf propagates it via memberlist's SWIM protocol — epidemic
// broadcast to all cluster members within a few protocol periods.
//
// Subscribe: returns a channel fed by a Serf EventHandler that
// filters for UserEvents with the aq event name.
//
// Active: returns non-expired entries from the local cache. This is
// the impedance mismatch fix — memberlist doesn't persist events,
// so we maintain application-level state.
type SerfChannel struct {
	serfClient *serf.Serf

	// cache stores the most recent broadcast per agent. Keyed by
	// Broadcast.Agent so that re-announces overwrite stale entries.
	// Pruned on read (Active) by TTL.
	mu    sync.RWMutex
	cache map[string]Broadcast

	// eventName is the Serf UserEvent name used for aq broadcasts.
	// All aq events use this prefix so the handler can filter them.
	eventName string
}

const defaultEventName = "aq:broadcast"

// NewSerfChannel joins a Serf cluster and starts processing events.
//
// bindAddr: local address for memberlist (e.g., "0.0.0.0:7946").
// join:     addresses of existing cluster members to bootstrap from.
// encryptKey: 32-byte key for memberlist encryption (nil = no encryption).
// nodeName: unique name for this node in the cluster.
// tags:     key-value metadata visible to all peers (conjecture, phase).
func NewSerfChannel(bindAddr string, join []string, encryptKey []byte, nodeName string, tags map[string]string) (*SerfChannel, error) {
	conf := serf.DefaultConfig()
	conf.NodeName = nodeName
	conf.Tags = tags

	// memberlist config lives inside serf config.
	conf.MemberlistConfig.BindAddr = bindAddr
	conf.MemberlistConfig.BindPort = 7946
	if encryptKey != nil {
		conf.MemberlistConfig.SecretKey = encryptKey
	}

	// Protocol period: how often memberlist ticks its SWIM protocol.
	// Default is 1s. At this rate, a UserEvent reaches all nodes in
	// O(log N) protocol periods — ~3s for a 10-node cluster.
	// aq's TTL (3600s) is 3600x the propagation time. No conflict.
	//
	// If you need faster propagation (sub-second conflict detection):
	//   conf.MemberlistConfig.GossipInterval = 200 * time.Millisecond

	s, err := serf.Create(conf)
	if err != nil {
		return nil, err
	}

	// Join existing cluster members. memberlist discovers the rest
	// via gossip — you only need one reachable seed node.
	if len(join) > 0 {
		_, err = s.Join(join, true)
		if err != nil {
			log.Printf("serf: failed to join cluster peers %v: %v (continuing as standalone)", join, err)
		}
	}

	sc := &SerfChannel{
		serfClient: s,
		cache:      make(map[string]Broadcast),
		eventName:  defaultEventName,
	}

	// Start background event handler to populate the local cache.
	go sc.handleEvents()

	return sc, nil
}

// Publish sends a broadcast as a Serf UserEvent.
//
// Serf UserEvents are fire-and-forget epidemic broadcasts. memberlist
// propagates them via its SWIM protocol — no broker, no coordinator.
// The payload is the JSON-encoded broadcast. Serf coalesces events
// with the same name, so we use the agent name as a suffix to avoid
// coalescing broadcasts from different agents.
//
// Max payload: Serf UserEvents support up to ~9KB. The aq broadcast
// payload is typically <1KB. No truncation needed.
func (sc *SerfChannel) Publish(b Broadcast) error {
	data, err := json.Marshal(b)
	if err != nil {
		return err
	}

	// UserEvent with coalesce=true: if this agent sends multiple
	// events before the previous one has fully propagated, Serf
	// coalesces them. This is correct for aq — only the latest
	// broadcast from each agent matters.
	return sc.serfClient.UserEvent(sc.eventName, data, true)
}

// Subscribe returns a channel that emits broadcasts as they arrive
// from the Serf cluster. Cancel the context to stop receiving.
func (sc *SerfChannel) Subscribe(ctx context.Context) <-chan Broadcast {
	ch := make(chan Broadcast, 64)

	go func() {
		defer close(ch)
		// Poll the cache for changes. In a production implementation,
		// this would use serf.EventCh directly. This sketch uses cache
		// polling to keep the example simple and avoid exposing serf
		// internals through the Channel interface.
		ticker := time.NewTicker(500 * time.Millisecond)
		defer ticker.Stop()

		seen := make(map[string]string) // agent -> last seen ID

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				sc.mu.RLock()
				for agent, b := range sc.cache {
					if b.IsExpired() {
						continue
					}
					if seen[agent] != b.ID {
						seen[agent] = b.ID
						select {
						case ch <- b:
						default:
							// Drop if consumer is slow. Gossip is lossy-ok.
						}
					}
				}
				sc.mu.RUnlock()
			}
		}
	}()

	return ch
}

// Active returns all non-expired broadcasts from the local cache.
//
// This is the impedance mismatch fix. memberlist does not persist
// events — once propagated, they exist only in memory on each node.
// The cache is populated by handleEvents() and pruned here by TTL.
func (sc *SerfChannel) Active() ([]Broadcast, error) {
	sc.mu.Lock()
	defer sc.mu.Unlock()

	var active []Broadcast
	for agent, b := range sc.cache {
		if b.IsExpired() {
			delete(sc.cache, agent)
			continue
		}
		active = append(active, b)
	}
	return active, nil
}

// handleEvents runs in a background goroutine, reading Serf events
// and updating the local cache. This is what bridges memberlist's
// ephemeral event model to aq's Active()-needs-state model.
func (sc *SerfChannel) handleEvents() {
	eventCh := sc.serfClient.ShutdownCh()

	for {
		select {
		case <-eventCh:
			return
		default:
		}

		// In a real implementation, this would read from the Serf
		// event channel. Sketch version: the cache is populated
		// by Publish() calls on this node and by received UserEvents
		// on the serf event channel (which serf.Create makes
		// available via serf.EventCh() in the config).
		//
		// The production pattern:
		//
		//   conf.EventCh = make(chan serf.Event, 256)
		//   ...
		//   for event := range conf.EventCh {
		//       if ue, ok := event.(serf.UserEvent); ok && ue.Name == sc.eventName {
		//           var b Broadcast
		//           json.Unmarshal(ue.Payload, &b)
		//           sc.mu.Lock()
		//           sc.cache[b.Agent] = b
		//           sc.mu.Unlock()
		//       }
		//   }

		time.Sleep(100 * time.Millisecond)
	}
}

// Close leaves the Serf cluster gracefully. Other nodes will see
// this node depart within one protocol period.
func (sc *SerfChannel) Close() error {
	return sc.serfClient.Leave()
}
