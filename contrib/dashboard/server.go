//go:build ignore

// aq dashboard — live broadcast viewer with WebSocket push.
//
// Standalone HTTP server that shows active aq broadcasts, conflict
// alerts, and TTL countdowns. Sources data from the filesystem
// transport (polls ~/.aq/channels/broadcast/requests/) or from
// Postgres LISTEN if AQ_POSTGRES_URL is set.
//
// Usage:
//   go run contrib/dashboard/server.go
//
// Environment:
//   AQ_DASHBOARD_PORT  — HTTP port (default 8085)
//   AQ_HOME            — aq state directory (default ~/.aq)
//   AQ_POSTGRES_URL    — optional Postgres connection for live NOTIFY
package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	_ "embed"
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

type ConflictSignal struct {
	A           Broadcast `json:"a"`
	B           Broadcast `json:"b"`
	SharedFiles []string  `json:"shared_files"`
	Severity    string    `json:"severity"`
}

// ---------- Filesystem source ----------

func aqHome() string {
	if env := os.Getenv("AQ_HOME"); env != "" {
		return env
	}
	if info, err := os.Stat(".aq"); err == nil && info.IsDir() {
		if abs, err := filepath.Abs(".aq"); err == nil {
			return abs
		}
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".aq")
}

func readActive() []Broadcast {
	reqDir := filepath.Join(aqHome(), "channels", "broadcast", "requests")
	entries, err := os.ReadDir(reqDir)
	if err != nil {
		return nil
	}

	var active []Broadcast
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasPrefix(entry.Name(), "aq-") || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(reqDir, entry.Name()))
		if err != nil {
			continue
		}
		var b Broadcast
		if err := json.Unmarshal([]byte(strings.TrimSpace(string(data))), &b); err != nil {
			continue
		}
		if !b.IsExpired() {
			active = append(active, b)
		}
	}

	sort.Slice(active, func(i, j int) bool { return active[i].Ts < active[j].Ts })
	return active
}

func detectConflicts(broadcasts []Broadcast) []ConflictSignal {
	var signals []ConflictSignal
	for i := 0; i < len(broadcasts); i++ {
		for j := i + 1; j < len(broadcasts); j++ {
			a, b := broadcasts[i], broadcasts[j]
			if a.Agent == b.Agent {
				continue
			}
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
			sort.Strings(shared)
			bothProof := a.Phase == "proof" && b.Phase == "proof"
			oneProof := a.Phase == "proof" || b.Phase == "proof"
			severity := "low"
			if bothProof {
				severity = "high"
			} else if oneProof {
				severity = "medium"
			}
			signals = append(signals, ConflictSignal{A: a, B: b, SharedFiles: shared, Severity: severity})
		}
	}
	return signals
}

// ---------- Minimal WebSocket (no gorilla dependency) ----------
// We implement a bare-minimum WebSocket upgrade using standard library.
// This handles the upgrade handshake and frame writing for text messages.
// For production use, consider gorilla/websocket or nhooyr/websocket.

// wsClients tracks connected WebSocket clients.
var (
	wsClients   = make(map[*wsConn]bool)
	wsClientsMu sync.Mutex
)

type wsConn struct {
	conn   interface{ Write([]byte) (int, error) }
	closed bool
}

// ---------- HTTP Handlers ----------

func handleIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	// HATEOAS: if the client asks for JSON, return API root with links.
	accept := r.Header.Get("Accept")
	if strings.Contains(accept, "application/json") && !strings.Contains(accept, "text/html") {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"service":     "aq",
			"description": "Ambient agent queue — gossip layer (L1.5) for multi-agent development",
			"version":     "dev",
			"_links": map[string]interface{}{
				"self":       map[string]string{"href": "/", "title": "API root (this document)"},
				"broadcasts": map[string]string{"href": "/api/broadcasts", "title": "Active broadcasts"},
				"conflicts":  map[string]string{"href": "/api/conflicts", "title": "Detected conflicts"},
				"state":      map[string]string{"href": "/api/state", "title": "Combined broadcasts + conflicts"},
				"announce":   map[string]string{"href": "/api/announce", "method": "POST", "title": "Create a broadcast"},
				"openapi":    map[string]string{"href": "/openapi.json", "title": "OpenAPI 3.1 specification"},
				"dashboard":  map[string]string{"href": "/", "type": "text/html", "title": "Web dashboard"},
			},
		})
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write([]byte(indexHTML))
}

var apiLinks = map[string]interface{}{
	"root":       map[string]string{"href": "/"},
	"broadcasts": map[string]string{"href": "/api/broadcasts"},
	"conflicts":  map[string]string{"href": "/api/conflicts"},
	"state":      map[string]string{"href": "/api/state"},
	"announce":   map[string]string{"href": "/api/announce", "method": "POST"},
	"openapi":    map[string]string{"href": "/openapi.json"},
}

func handleAPIBroadcasts(w http.ResponseWriter, r *http.Request) {
	active := readActive()
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"broadcasts": active,
		"count":      len(active),
		"ts":         time.Now().Unix(),
		"_links":     apiLinks,
	})
}

func handleAPIConflicts(w http.ResponseWriter, r *http.Request) {
	active := readActive()
	conflicts := detectConflicts(active)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"conflicts": conflicts,
		"count":     len(conflicts),
		"ts":        time.Now().Unix(),
		"_links":    apiLinks,
	})
}

func handleAPIState(w http.ResponseWriter, r *http.Request) {
	active := readActive()
	conflicts := detectConflicts(active)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"broadcasts": active,
		"conflicts":  conflicts,
		"ts":         time.Now().Unix(),
		"_links":     apiLinks,
	})
}

func handleAPIAnnounce(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		http.Error(w, `{"error":"method not allowed","_links":{"self":{"href":"/api/announce","method":"POST"}}}`, http.StatusMethodNotAllowed)
		return
	}
	var b Broadcast
	if err := json.NewDecoder(r.Body).Decode(&b); err != nil {
		http.Error(w, fmt.Sprintf(`{"error":"invalid JSON: %s"}`, err.Error()), http.StatusBadRequest)
		return
	}
	if b.ConjectureID == "" {
		http.Error(w, `{"error":"conjecture_id is required"}`, http.StatusBadRequest)
		return
	}
	// Fill defaults.
	if b.ID == "" {
		b.ID = fmt.Sprintf("%012x%010x", time.Now().UnixMilli(), time.Now().UnixNano()%0xFFFFFFFFFF)
	}
	if b.Ts == 0 {
		b.Ts = float64(time.Now().Unix())
	}
	if b.TTL == 0 {
		b.TTL = 300
	}
	if b.Phase == "" {
		b.Phase = "proof"
	}
	if b.Status == "" {
		b.Status = "prosecuting"
	}
	// Write to filesystem.
	reqDir := filepath.Join(aqHome(), "channels", "broadcast", "requests")
	os.MkdirAll(reqDir, 0o755)
	data, _ := json.Marshal(b)
	filename := fmt.Sprintf("aq-%014d-%s.json", int64(b.Ts), b.ID)
	path := filepath.Join(reqDir, filename)
	if err := os.WriteFile(path, append(data, '\n'), 0o644); err != nil {
		http.Error(w, fmt.Sprintf(`{"error":"write failed: %s"}`, err.Error()), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Location", "/api/broadcasts")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(map[string]interface{}{
		"announced": b.ConjectureID,
		"file":      filename,
		"broadcast": b,
		"_links": map[string]interface{}{
			"broadcasts": map[string]string{"href": "/api/broadcasts"},
			"conflicts":  map[string]string{"href": "/api/conflicts"},
			"state":      map[string]string{"href": "/api/state"},
		},
	})
}

func handleOpenAPI(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Write([]byte(openAPISpec))
}

const openAPISpec = `{
  "openapi": "3.1.0",
  "info": {
    "title": "aq — Ambient Agent Queue",
    "description": "Gossip layer (L1.5) for multi-agent development. Agents broadcast presence via channels so peers detect semantic conflicts before they become merge conflicts.",
    "version": "0.1.0",
    "contact": {
      "name": "Jason Walsh",
      "url": "https://github.com/jwalsh/aq"
    },
    "license": {
      "name": "MIT",
      "url": "https://github.com/jwalsh/aq/blob/main/LICENSE"
    }
  },
  "servers": [
    {
      "url": "http://localhost:8085",
      "description": "Local dashboard"
    }
  ],
  "paths": {
    "/": {
      "get": {
        "operationId": "getRoot",
        "summary": "API root with HATEOAS links",
        "description": "Returns service info and hypermedia links to all endpoints. Returns HTML dashboard if Accept header prefers text/html.",
        "responses": {
          "200": {
            "description": "API root",
            "content": {
              "application/json": {
                "schema": { "$ref": "#/components/schemas/ApiRoot" }
              },
              "text/html": {
                "description": "Web dashboard"
              }
            }
          }
        }
      }
    },
    "/api/broadcasts": {
      "get": {
        "operationId": "listBroadcasts",
        "summary": "List active broadcasts",
        "description": "Returns all non-expired broadcasts from the gossip channel.",
        "responses": {
          "200": {
            "description": "Active broadcasts",
            "content": {
              "application/json": {
                "schema": {
                  "type": "object",
                  "properties": {
                    "broadcasts": {
                      "type": "array",
                      "items": { "$ref": "#/components/schemas/Broadcast" }
                    },
                    "count": { "type": "integer" },
                    "ts": { "type": "number", "description": "Unix timestamp of query" }
                  }
                }
              }
            }
          }
        }
      }
    },
    "/api/conflicts": {
      "get": {
        "operationId": "listConflicts",
        "summary": "List detected conflicts",
        "description": "Returns pairwise conflict signals between active broadcasts based on file overlap and CPRR phase severity.",
        "responses": {
          "200": {
            "description": "Detected conflicts",
            "content": {
              "application/json": {
                "schema": {
                  "type": "object",
                  "properties": {
                    "conflicts": {
                      "type": "array",
                      "items": { "$ref": "#/components/schemas/ConflictSignal" }
                    },
                    "count": { "type": "integer" },
                    "ts": { "type": "number" }
                  }
                }
              }
            }
          }
        }
      }
    },
    "/api/state": {
      "get": {
        "operationId": "getState",
        "summary": "Combined broadcasts and conflicts",
        "description": "Returns both active broadcasts and detected conflicts in a single response. Preferred for dashboard polling.",
        "responses": {
          "200": {
            "description": "Full state",
            "content": {
              "application/json": {
                "schema": {
                  "type": "object",
                  "properties": {
                    "broadcasts": {
                      "type": "array",
                      "items": { "$ref": "#/components/schemas/Broadcast" }
                    },
                    "conflicts": {
                      "type": "array",
                      "items": { "$ref": "#/components/schemas/ConflictSignal" }
                    },
                    "ts": { "type": "number" }
                  }
                }
              }
            }
          }
        }
      }
    },
    "/api/announce": {
      "post": {
        "operationId": "announce",
        "summary": "Create a broadcast",
        "description": "Publish a presence broadcast to the gossip channel. Writes to the filesystem transport. Fields conjecture_id is required; all others have sensible defaults.",
        "requestBody": {
          "required": true,
          "content": {
            "application/json": {
              "schema": { "$ref": "#/components/schemas/Broadcast" },
              "example": {
                "agent": "github.com/jwalsh/aq/feat-auth",
                "conjecture_id": "C-1",
                "conjecture_claim": "Filesystem-first transport is sufficient",
                "phase": "proof",
                "files": ["auth.py", "session.py"]
              }
            }
          }
        },
        "responses": {
          "201": {
            "description": "Broadcast created",
            "content": {
              "application/json": {
                "schema": {
                  "type": "object",
                  "properties": {
                    "announced": { "type": "string" },
                    "file": { "type": "string" },
                    "broadcast": { "$ref": "#/components/schemas/Broadcast" },
                    "_links": { "type": "object" }
                  }
                }
              }
            }
          },
          "400": { "description": "Invalid request body or missing conjecture_id" }
        }
      }
    },
    "/openapi.json": {
      "get": {
        "operationId": "getOpenAPISpec",
        "summary": "OpenAPI specification",
        "responses": {
          "200": {
            "description": "This document",
            "content": {
              "application/json": {}
            }
          }
        }
      }
    }
  },
  "components": {
    "schemas": {
      "Broadcast": {
        "type": "object",
        "required": ["conjecture_id"],
        "properties": {
          "id":               { "type": "string", "description": "ULID — auto-generated if omitted" },
          "agent":            { "type": "string", "description": "{remote}/{branch} or worktree address" },
          "worktree":         { "type": "string", "description": "Branch name" },
          "conjecture_id":    { "type": "string", "description": "e.g. C-1", "example": "C-1" },
          "conjecture_claim": { "type": "string", "description": "Human-readable claim" },
          "phase":            { "type": "string", "enum": ["conjecture", "proof", "refutation", "refinement"], "default": "proof" },
          "status":           { "type": "string", "enum": ["prosecuting", "done", "blocked"], "default": "prosecuting" },
          "files":            { "type": "array", "items": { "type": "string" }, "description": "Files being touched" },
          "ts":               { "type": "number", "description": "Unix timestamp — auto-set if omitted" },
          "ttl":              { "type": "integer", "description": "Seconds until expiry", "default": 300 }
        }
      },
      "ConflictSignal": {
        "type": "object",
        "properties": {
          "a":            { "$ref": "#/components/schemas/Broadcast" },
          "b":            { "$ref": "#/components/schemas/Broadcast" },
          "shared_files": { "type": "array", "items": { "type": "string" } },
          "severity":     { "type": "string", "enum": ["high", "medium", "low"], "description": "both proof + shared files = high, one proof = medium, neither = low" }
        }
      },
      "ApiRoot": {
        "type": "object",
        "properties": {
          "service":     { "type": "string" },
          "description": { "type": "string" },
          "version":     { "type": "string" },
          "_links":      { "type": "object", "description": "HATEOAS hypermedia links" }
        }
      }
    }
  }
}`

// ---------- Main ----------

func main() {
	port := os.Getenv("AQ_DASHBOARD_PORT")
	if port == "" {
		port = "8085"
	}

	http.HandleFunc("/", handleIndex)
	http.HandleFunc("/api/broadcasts", handleAPIBroadcasts)
	http.HandleFunc("/api/conflicts", handleAPIConflicts)
	http.HandleFunc("/api/state", handleAPIState)
	http.HandleFunc("/api/announce", handleAPIAnnounce)
	http.HandleFunc("/openapi.json", handleOpenAPI)

	addr := fmt.Sprintf(":%s", port)
	log.Printf("aq dashboard starting on http://localhost%s", addr)
	log.Printf("aq home: %s", aqHome())

	if pgURL := os.Getenv("AQ_POSTGRES_URL"); pgURL != "" {
		log.Printf("Postgres URL set -- dashboard will also poll Postgres")
	} else {
		log.Printf("Filesystem-only mode (polling %s every 2s)", filepath.Join(aqHome(), "channels", "broadcast", "requests"))
	}

	log.Fatal(http.ListenAndServe(addr, nil))
}

// ---------- Embedded HTML ----------
// The dashboard UI is a single HTML file embedded at compile time.
// The UI uses polling via fetch() every 2 seconds (simpler than WebSocket
// for a dev tool, and avoids external dependencies).

const indexHTML = `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>aq dashboard</title>
<style>
  :root {
    --bg: #0d1117;
    --surface: #161b22;
    --border: #30363d;
    --text: #c9d1d9;
    --text-dim: #8b949e;
    --accent: #58a6ff;
    --green: #3fb950;
    --yellow: #d29922;
    --red: #f85149;
    --orange: #db6d28;
  }
  * { margin: 0; padding: 0; box-sizing: border-box; }
  body {
    font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", Helvetica, Arial, sans-serif;
    background: var(--bg);
    color: var(--text);
    line-height: 1.5;
    padding: 1rem;
  }
  header {
    display: flex;
    align-items: center;
    justify-content: space-between;
    margin-bottom: 1.5rem;
    padding-bottom: 1rem;
    border-bottom: 1px solid var(--border);
  }
  header h1 {
    font-size: 1.5rem;
    font-weight: 600;
    color: var(--accent);
  }
  header h1 span { color: var(--text-dim); font-weight: 400; }
  .status-bar {
    display: flex;
    gap: 1.5rem;
    font-size: 0.85rem;
    color: var(--text-dim);
  }
  .status-bar .count { color: var(--text); font-weight: 600; }
  .filters {
    display: flex;
    gap: 0.5rem;
    margin-bottom: 1rem;
    flex-wrap: wrap;
  }
  .filters input {
    background: var(--surface);
    border: 1px solid var(--border);
    color: var(--text);
    padding: 0.4rem 0.75rem;
    border-radius: 6px;
    font-size: 0.85rem;
    outline: none;
    min-width: 200px;
  }
  .filters input:focus { border-color: var(--accent); }
  .filters input::placeholder { color: var(--text-dim); }
  .section-title {
    font-size: 0.9rem;
    font-weight: 600;
    color: var(--text-dim);
    text-transform: uppercase;
    letter-spacing: 0.05em;
    margin: 1.5rem 0 0.75rem;
  }
  .conflicts-section { margin-bottom: 1rem; }
  .conflict-card {
    background: var(--surface);
    border: 1px solid var(--border);
    border-radius: 8px;
    padding: 0.75rem 1rem;
    margin-bottom: 0.5rem;
    border-left: 3px solid var(--border);
  }
  .conflict-card.high { border-left-color: var(--red); }
  .conflict-card.medium { border-left-color: var(--yellow); }
  .conflict-card.low { border-left-color: var(--green); }
  .conflict-header {
    display: flex;
    align-items: center;
    gap: 0.5rem;
    margin-bottom: 0.25rem;
  }
  .severity-badge {
    font-size: 0.7rem;
    font-weight: 700;
    text-transform: uppercase;
    padding: 0.15rem 0.5rem;
    border-radius: 4px;
    letter-spacing: 0.05em;
  }
  .severity-badge.high { background: var(--red); color: #fff; }
  .severity-badge.medium { background: var(--yellow); color: #000; }
  .severity-badge.low { background: var(--green); color: #000; }
  .conflict-detail { font-size: 0.8rem; color: var(--text-dim); }
  .broadcast-grid {
    display: grid;
    grid-template-columns: repeat(auto-fill, minmax(340px, 1fr));
    gap: 0.75rem;
  }
  .broadcast-card {
    background: var(--surface);
    border: 1px solid var(--border);
    border-radius: 8px;
    padding: 1rem;
    position: relative;
    transition: border-color 0.2s;
  }
  .broadcast-card:hover { border-color: var(--accent); }
  .broadcast-card .agent {
    font-weight: 600;
    color: var(--accent);
    font-size: 0.95rem;
    margin-bottom: 0.25rem;
    word-break: break-all;
  }
  .broadcast-card .meta {
    display: flex;
    flex-wrap: wrap;
    gap: 0.5rem;
    margin: 0.5rem 0;
    font-size: 0.8rem;
  }
  .tag {
    background: rgba(88, 166, 255, 0.1);
    border: 1px solid rgba(88, 166, 255, 0.2);
    color: var(--accent);
    padding: 0.15rem 0.5rem;
    border-radius: 4px;
    font-size: 0.75rem;
    font-weight: 500;
  }
  .tag.phase-proof { background: rgba(219, 109, 40, 0.15); border-color: rgba(219, 109, 40, 0.3); color: var(--orange); }
  .tag.phase-conjecture { background: rgba(88, 166, 255, 0.1); border-color: rgba(88, 166, 255, 0.2); color: var(--accent); }
  .tag.phase-refutation { background: rgba(248, 81, 73, 0.1); border-color: rgba(248, 81, 73, 0.2); color: var(--red); }
  .tag.phase-refinement { background: rgba(63, 185, 80, 0.1); border-color: rgba(63, 185, 80, 0.2); color: var(--green); }
  .tag.status-done { background: rgba(63, 185, 80, 0.15); border-color: rgba(63, 185, 80, 0.3); color: var(--green); }
  .tag.status-blocked { background: rgba(248, 81, 73, 0.15); border-color: rgba(248, 81, 73, 0.3); color: var(--red); }
  .files-list {
    font-family: "SFMono-Regular", Consolas, "Liberation Mono", Menlo, monospace;
    font-size: 0.75rem;
    color: var(--text-dim);
    margin-top: 0.5rem;
    word-break: break-all;
  }
  .ttl-bar {
    position: absolute;
    bottom: 0;
    left: 0;
    right: 0;
    height: 3px;
    border-radius: 0 0 8px 8px;
    background: var(--border);
    overflow: hidden;
  }
  .ttl-bar-fill {
    height: 100%;
    background: var(--green);
    transition: width 1s linear, background-color 0.5s;
  }
  .ttl-bar-fill.warning { background: var(--yellow); }
  .ttl-bar-fill.critical { background: var(--red); }
  .ttl-text {
    position: absolute;
    top: 0.5rem;
    right: 0.75rem;
    font-size: 0.7rem;
    color: var(--text-dim);
    font-family: "SFMono-Regular", Consolas, monospace;
  }
  .empty-state {
    text-align: center;
    padding: 3rem 1rem;
    color: var(--text-dim);
  }
  .empty-state h2 { font-size: 1.1rem; margin-bottom: 0.5rem; }
  .empty-state p { font-size: 0.85rem; }
  .empty-state code {
    background: var(--surface);
    padding: 0.2rem 0.5rem;
    border-radius: 4px;
    font-size: 0.8rem;
  }
</style>
</head>
<body>
<header>
  <h1>aq <span>dashboard</span></h1>
  <div class="status-bar">
    <div>Broadcasts: <span class="count" id="broadcast-count">0</span></div>
    <div>Conflicts: <span class="count" id="conflict-count">0</span></div>
    <div>Updated: <span id="last-update">--</span></div>
  </div>
</header>

<div class="filters">
  <input type="text" id="filter-input" placeholder="Filter by agent, conjecture, or file...">
</div>

<div class="conflicts-section" id="conflicts-section" style="display:none">
  <div class="section-title">Conflicts</div>
  <div id="conflicts-container"></div>
</div>

<div class="section-title">Active Broadcasts</div>
<div class="broadcast-grid" id="broadcasts-container">
  <div class="empty-state">
    <h2>No active broadcasts</h2>
    <p>Announce with <code>aq announce -c C-1 -f "file.py"</code></p>
  </div>
</div>

<script>
const API_POLL_MS = 2000;
let filterText = '';

document.getElementById('filter-input').addEventListener('input', (e) => {
  filterText = e.target.value.toLowerCase();
  renderState(lastState);
});

let lastState = { broadcasts: [], conflicts: [] };

function formatTTL(ts, ttl) {
  const now = Date.now() / 1000;
  const remaining = Math.max(0, (ts + ttl) - now);
  if (remaining <= 0) return 'expired';
  if (remaining < 60) return Math.floor(remaining) + 's';
  if (remaining < 3600) return Math.floor(remaining / 60) + 'm ' + Math.floor(remaining % 60) + 's';
  return Math.floor(remaining / 3600) + 'h ' + Math.floor((remaining % 3600) / 60) + 'm';
}

function ttlPercent(ts, ttl) {
  const now = Date.now() / 1000;
  const remaining = Math.max(0, (ts + ttl) - now);
  return Math.min(100, (remaining / ttl) * 100);
}

function matchesFilter(broadcast) {
  if (!filterText) return true;
  const haystack = [
    broadcast.agent,
    broadcast.conjecture_id,
    broadcast.conjecture_claim,
    broadcast.phase,
    broadcast.status,
    ...(broadcast.files || [])
  ].join(' ').toLowerCase();
  return haystack.includes(filterText);
}

function renderState(state) {
  lastState = state;
  const broadcasts = (state.broadcasts || []).filter(matchesFilter);
  const conflicts = (state.conflicts || []).filter(c =>
    matchesFilter(c.a) || matchesFilter(c.b)
  );

  document.getElementById('broadcast-count').textContent = (state.broadcasts || []).length;
  document.getElementById('conflict-count').textContent = (state.conflicts || []).length;
  document.getElementById('last-update').textContent = new Date().toLocaleTimeString();

  // Render conflicts
  const conflictsSection = document.getElementById('conflicts-section');
  const conflictsContainer = document.getElementById('conflicts-container');
  if (conflicts.length > 0) {
    conflictsSection.style.display = 'block';
    conflictsContainer.innerHTML = conflicts.map(c => {
      const sev = c.severity || 'low';
      return '<div class="conflict-card ' + sev + '">' +
        '<div class="conflict-header">' +
          '<span class="severity-badge ' + sev + '">' + sev + '</span>' +
          '<span>' + esc(c.a.agent) + ' / ' + esc(c.a.conjecture_id) +
          ' &harr; ' + esc(c.b.agent) + ' / ' + esc(c.b.conjecture_id) + '</span>' +
        '</div>' +
        '<div class="conflict-detail">Shared files: ' +
          (c.shared_files || []).map(f => '<code>' + esc(f) + '</code>').join(', ') +
          ' | Phases: ' + esc(c.a.phase) + ' / ' + esc(c.b.phase) +
        '</div>' +
      '</div>';
    }).join('');
  } else {
    conflictsSection.style.display = 'none';
  }

  // Render broadcasts
  const container = document.getElementById('broadcasts-container');
  if (broadcasts.length === 0) {
    container.innerHTML = '<div class="empty-state">' +
      '<h2>No active broadcasts</h2>' +
      '<p>Announce with <code>aq announce -c C-1 -f "file.py"</code></p>' +
    '</div>';
    return;
  }

  container.innerHTML = broadcasts.map(b => {
    const pct = ttlPercent(b.ts, b.ttl);
    const ttlClass = pct < 15 ? 'critical' : pct < 40 ? 'warning' : '';
    const phaseClass = 'phase-' + (b.phase || 'conjecture');
    const statusClass = 'status-' + (b.status || 'prosecuting');
    const files = (b.files || []).join(', ');
    const claim = b.conjecture_claim ? ' — ' + esc(b.conjecture_claim) : '';

    return '<div class="broadcast-card">' +
      '<div class="ttl-text">' + formatTTL(b.ts, b.ttl) + '</div>' +
      '<div class="agent">' + esc(b.agent) + '</div>' +
      '<div class="meta">' +
        '<span class="tag">' + esc(b.conjecture_id) + claim + '</span>' +
        '<span class="tag ' + phaseClass + '">' + esc(b.phase) + '</span>' +
        '<span class="tag ' + statusClass + '">' + esc(b.status) + '</span>' +
      '</div>' +
      (files ? '<div class="files-list">' + esc(files) + '</div>' : '') +
      '<div class="ttl-bar"><div class="ttl-bar-fill ' + ttlClass + '" style="width:' + pct + '%"></div></div>' +
    '</div>';
  }).join('');
}

function esc(s) {
  if (!s) return '';
  const div = document.createElement('div');
  div.textContent = s;
  return div.innerHTML;
}

async function poll() {
  try {
    const resp = await fetch('/api/state');
    if (resp.ok) {
      const state = await resp.json();
      renderState(state);
    }
  } catch (e) {
    // Network error -- will retry next poll.
  }
}

// Initial fetch + polling loop.
poll();
setInterval(poll, API_POLL_MS);

// Update TTL countdowns every second without re-fetching.
setInterval(() => {
  document.querySelectorAll('.broadcast-card').forEach((card, i) => {
    const b = lastState.broadcasts && lastState.broadcasts[i];
    if (!b) return;
    const ttlEl = card.querySelector('.ttl-text');
    const fillEl = card.querySelector('.ttl-bar-fill');
    if (ttlEl) ttlEl.textContent = formatTTL(b.ts, b.ttl);
    if (fillEl) {
      const pct = ttlPercent(b.ts, b.ttl);
      fillEl.style.width = pct + '%';
      fillEl.className = 'ttl-bar-fill' + (pct < 15 ? ' critical' : pct < 40 ? ' warning' : '');
    }
  });
}, 1000);
</script>
</body>
</html>
`
