//go:build ignore

// aq-pg implements the Channel interface for Postgres transport.
//
// This is a standalone sketch -- it is NOT imported by main.go.
// Postgres is a Tier 2 transport. Filesystem (Tier 0) is always required.
//
// Dependencies (not in go.mod -- this file is standalone):
//   go get github.com/lib/pq
//
// Usage:
//   export AQ_POSTGRES_URL="postgres://localhost:5432/aq?sslmode=disable"
//   go run contrib/postgres/aq-pg.go
package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	// Postgres driver with LISTEN/NOTIFY support.
	// Install: go get github.com/lib/pq
	"github.com/lib/pq"
)

// ---------- Broadcast (matches main.go exactly) ----------

// Broadcast is the ambient presence payload. This definition mirrors
// the struct in main.go so this file can be compiled independently.
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

// IsExpired returns true if the broadcast has outlived its TTL.
func (b *Broadcast) IsExpired() bool {
	return float64(time.Now().Unix()) > b.Ts+float64(b.TTL)
}

// ---------- Channel interface ----------

// Channel is the transport abstraction from TRANSPORT-RESEARCH.md section 8.
type Channel interface {
	Publish(broadcast Broadcast) error
	Subscribe(ctx context.Context) <-chan Broadcast
	Active() ([]Broadcast, error)
}

// ---------- PostgresChannel ----------

// PostgresChannel implements Channel using Postgres as the backing store.
// Publish inserts rows; the trg_notify_aq_broadcast trigger fires NOTIFY.
// Subscribe uses LISTEN to receive notifications in real time.
// Active queries the active_broadcasts view (filters by TTL expiry).
type PostgresChannel struct {
	connStr string
	db      *sql.DB
}

// NewPostgresChannel creates a new PostgresChannel. The connection string
// is read from AQ_POSTGRES_URL or the provided fallback.
func NewPostgresChannel(connStr string) (*PostgresChannel, error) {
	if connStr == "" {
		connStr = os.Getenv("AQ_POSTGRES_URL")
	}
	if connStr == "" {
		return nil, fmt.Errorf("no connection string: set AQ_POSTGRES_URL or pass connStr")
	}

	db, err := sql.Open("postgres", connStr)
	if err != nil {
		return nil, fmt.Errorf("failed to open database: %w", err)
	}

	// Verify connectivity.
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, fmt.Errorf("failed to ping database: %w", err)
	}

	return &PostgresChannel{
		connStr: connStr,
		db:      db,
	}, nil
}

// Close releases the database connection.
func (c *PostgresChannel) Close() error {
	if c.db != nil {
		return c.db.Close()
	}
	return nil
}

// Publish inserts a broadcast into the broadcasts table. The database
// trigger fires pg_notify('aq_broadcast', payload) automatically.
// This is fire-and-forget from the caller's perspective: the error
// return is for local failures (connection down, constraint violation),
// not delivery confirmation.
func (c *PostgresChannel) Publish(b Broadcast) error {
	payload, err := json.Marshal(b)
	if err != nil {
		return fmt.Errorf("failed to marshal broadcast: %w", err)
	}

	// Use the insert_broadcast convenience function from schema.sql.
	_, err = c.db.Exec("SELECT insert_broadcast($1::jsonb)", string(payload))
	if err != nil {
		return fmt.Errorf("failed to insert broadcast: %w", err)
	}

	return nil
}

// Subscribe listens for new broadcasts via Postgres LISTEN/NOTIFY.
// Each NOTIFY payload contains the full JSON broadcast. The returned
// channel emits broadcasts as they arrive. Cancel the context to stop.
func (c *PostgresChannel) Subscribe(ctx context.Context) <-chan Broadcast {
	ch := make(chan Broadcast, 64)

	go func() {
		defer close(ch)

		// Create a dedicated listener connection using lib/pq.
		listener := pq.NewListener(c.connStr, 10*time.Second, time.Minute, func(ev pq.ListenerEventType, err error) {
			if err != nil {
				log.Printf("aq-pg: listener event error: %v", err)
			}
		})
		defer listener.Close()

		if err := listener.Listen("aq_broadcast"); err != nil {
			log.Printf("aq-pg: failed to LISTEN: %v", err)
			return
		}

		log.Println("aq-pg: listening on channel aq_broadcast")

		for {
			select {
			case <-ctx.Done():
				return

			case notification := <-listener.Notify:
				if notification == nil {
					// Reconnection notification -- ignore.
					continue
				}

				var b Broadcast
				if err := json.Unmarshal([]byte(notification.Extra), &b); err != nil {
					log.Printf("aq-pg: failed to unmarshal notification: %v", err)
					continue
				}

				if b.IsExpired() {
					continue
				}

				select {
				case ch <- b:
				case <-ctx.Done():
					return
				}
			}
		}
	}()

	return ch
}

// Active queries the active_broadcasts view and returns all non-expired
// broadcasts. This is a point-in-time snapshot.
func (c *PostgresChannel) Active() ([]Broadcast, error) {
	rows, err := c.db.Query("SELECT payload FROM active_broadcasts ORDER BY ts")
	if err != nil {
		return nil, fmt.Errorf("failed to query active broadcasts: %w", err)
	}
	defer rows.Close()

	var broadcasts []Broadcast
	for rows.Next() {
		var payload string
		if err := rows.Scan(&payload); err != nil {
			continue
		}
		var b Broadcast
		if err := json.Unmarshal([]byte(payload), &b); err != nil {
			continue
		}
		broadcasts = append(broadcasts, b)
	}

	return broadcasts, rows.Err()
}

// ---------- Conflict detection ----------

// ConflictSignal records a detected file overlap between two broadcasts.
type ConflictSignal struct {
	A           Broadcast `json:"a"`
	B           Broadcast `json:"b"`
	SharedFiles []string  `json:"shared_files"`
	Severity    string    `json:"severity"` // low | medium | high
}

// detectConflicts checks all active broadcasts for file overlaps and
// returns conflict signals with CPRR-phase-modulated severity.
func detectConflicts(broadcasts []Broadcast) []ConflictSignal {
	var signals []ConflictSignal

	for i := 0; i < len(broadcasts); i++ {
		for j := i + 1; j < len(broadcasts); j++ {
			a, b := broadcasts[i], broadcasts[j]
			if a.Agent == b.Agent {
				continue
			}

			// Find shared files.
			aFiles := make(map[string]struct{}, len(a.Files))
			for _, f := range a.Files {
				aFiles[f] = struct{}{}
			}
			var shared []string
			for _, f := range b.Files {
				if _, ok := aFiles[f]; ok {
					shared = append(shared, f)
				}
			}
			if len(shared) == 0 {
				continue
			}

			// Severity: both proof = high, one proof = medium, else low.
			bothProof := a.Phase == "proof" && b.Phase == "proof"
			oneProof := a.Phase == "proof" || b.Phase == "proof"
			severity := "low"
			if bothProof {
				severity = "high"
			} else if oneProof {
				severity = "medium"
			}

			signals = append(signals, ConflictSignal{
				A:           a,
				B:           b,
				SharedFiles: shared,
				Severity:    severity,
			})
		}
	}

	return signals
}

// ---------- Demo / self-test ----------

func main() {
	connStr := os.Getenv("AQ_POSTGRES_URL")
	if connStr == "" {
		fmt.Println("Set AQ_POSTGRES_URL to run the Postgres transport demo.")
		fmt.Println("Example: export AQ_POSTGRES_URL='postgres://localhost:5432/aq?sslmode=disable'")
		fmt.Println()
		fmt.Println("Setup:")
		fmt.Println("  createdb aq")
		fmt.Println("  psql aq < contrib/postgres/schema.sql")
		fmt.Println()
		fmt.Println("Then re-run this program.")
		os.Exit(1)
	}

	ch, err := NewPostgresChannel(connStr)
	if err != nil {
		log.Fatalf("Failed to connect: %v", err)
	}
	defer ch.Close()

	// Subscribe in background.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sub := ch.Subscribe(ctx)

	// Publish a test broadcast.
	b := Broadcast{
		Agent:           "demo/pg-test",
		Worktree:        "pg-test",
		ConjectureID:    "C-1",
		ConjectureClaim: "Postgres transport works",
		Phase:           "proof",
		Status:          "prosecuting",
		Files:           []string{"contrib/postgres/aq-pg.go"},
		Ts:              float64(time.Now().Unix()),
		TTL:             300,
		ID:              fmt.Sprintf("pg-test-%d", time.Now().UnixMilli()),
	}

	fmt.Println("Publishing test broadcast...")
	if err := ch.Publish(b); err != nil {
		log.Fatalf("Publish failed: %v", err)
	}
	fmt.Printf("Published: %s\n", b.ID)

	// Wait for the notification.
	fmt.Println("Waiting for NOTIFY (3s timeout)...")
	select {
	case received := <-sub:
		fmt.Printf("Received via LISTEN: agent=%s conjecture=%s files=%s\n",
			received.Agent, received.ConjectureID, strings.Join(received.Files, ","))
	case <-time.After(3 * time.Second):
		fmt.Println("Timeout waiting for notification (trigger may not be installed).")
	}

	// Query active broadcasts.
	active, err := ch.Active()
	if err != nil {
		log.Fatalf("Active() failed: %v", err)
	}
	fmt.Printf("Active broadcasts: %d\n", len(active))
	for _, ab := range active {
		fmt.Printf("  %s: %s (%s) phase=%s files=%s\n",
			ab.ID, ab.Agent, ab.ConjectureID, ab.Phase,
			strings.Join(ab.Files, ","))
	}

	// Check conflicts.
	conflicts := detectConflicts(active)
	if len(conflicts) > 0 {
		fmt.Printf("Conflicts detected: %d\n", len(conflicts))
		for _, c := range conflicts {
			fmt.Printf("  [%s] %s <-> %s -- shared: %s\n",
				strings.ToUpper(c.Severity),
				c.A.Agent, c.B.Agent,
				strings.Join(c.SharedFiles, ", "))
		}
	} else {
		fmt.Println("No conflicts detected.")
	}
}
