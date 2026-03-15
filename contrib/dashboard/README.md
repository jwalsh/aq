# aq Dashboard

Live broadcast viewer for aq. Shows active broadcasts, conflict alerts,
and TTL countdowns in a dark-themed web UI.

## Quick Start

```bash
go run contrib/dashboard/server.go
```

Open http://localhost:8085 in your browser.

## What It Shows

The dashboard displays:

- **Active broadcasts** as cards showing agent, conjecture, phase, files,
  and a TTL countdown bar that transitions green -> yellow -> red as the
  broadcast approaches expiry.
- **Conflict alerts** highlighted by severity (high = red, medium = yellow,
  low = green) with the shared files and CPRR phases that caused the alert.
- **Filter** input to search by agent name, conjecture ID, or file path.
- **Auto-refresh** every 2 seconds via polling, with per-second TTL
  countdown updates between polls.

## Data Source

By default the dashboard reads from the filesystem transport:

```
~/.aq/channels/broadcast/requests/aq-*.json
```

It polls this directory every 2 seconds. If `AQ_HOME` is set, it reads
from that directory instead.

If `AQ_POSTGRES_URL` is set, the dashboard logs that Postgres is
available (future: direct Postgres LISTEN integration for sub-second
updates).

## API Endpoints

| Endpoint | Method | Description |
|----------|--------|-------------|
| `/` | GET | Dashboard UI |
| `/api/broadcasts` | GET | JSON: all active broadcasts |
| `/api/conflicts` | GET | JSON: detected conflicts |
| `/api/state` | GET | JSON: broadcasts + conflicts combined |

### Example

```bash
# Get active broadcasts as JSON
curl -s http://localhost:8085/api/broadcasts | jq .

# Get conflicts
curl -s http://localhost:8085/api/conflicts | jq .
```

## Configuration

| Environment Variable | Default | Description |
|---------------------|---------|-------------|
| `AQ_DASHBOARD_PORT` | `8085` | HTTP listen port |
| `AQ_HOME` | `~/.aq` | aq state directory |
| `AQ_POSTGRES_URL` | (none) | Optional Postgres connection |

## UI Description

The dashboard uses a dark theme (GitHub-dark inspired) with:

```
+----------------------------------------------------------+
| aq dashboard         Broadcasts: 3  Conflicts: 1  12:34  |
+----------------------------------------------------------+
| [Filter by agent, conjecture, or file...              ]  |
+----------------------------------------------------------+
| CONFLICTS                                                |
| [HIGH] origin/feat-auth / C-1 <-> origin/fix-bug / C-7  |
|        Shared files: auth.py | Phases: proof / proof     |
+----------------------------------------------------------+
| ACTIVE BROADCASTS                                        |
| +------------------------+ +------------------------+    |
| | origin/feat-auth  4m32s| | origin/fix-bug    2m11s|    |
| | C-1 — FS transport     | | C-7 — Heartbeat       |    |
| | [proof] [prosecuting]  | | [proof] [prosecuting]  |    |
| | auth.py, protocol.py   | | auth.py, daemon.py     |    |
| | [=========>          ] | | [=====>              ] |    |
| +------------------------+ +------------------------+    |
| +------------------------+                               |
| | local/experiment  1m05s|                               |
| | C-3 — Wave semantics   |                               |
| | [conjecture] [done]    |                               |
| | wave.py                |                               |
| | [===>                ] |                               |
| +------------------------+                               |
+----------------------------------------------------------+
```

The TTL bar at the bottom of each card shows remaining time:
- Green: > 40% remaining
- Yellow: 15-40% remaining
- Red: < 15% remaining

Conflict cards have a colored left border matching severity.

## Architecture

The dashboard is a single Go file with the HTML embedded as a string
constant. No build step, no npm, no external JS dependencies. The UI
uses vanilla JavaScript with `fetch()` polling.

```
server.go
  |-- HTTP server (net/http)
  |-- /            -> serves embedded HTML
  |-- /api/state   -> reads filesystem, returns JSON
  |-- /api/*       -> individual endpoints
  |
  +-- index.html (standalone copy for development)
```

## macOS Menu Bar Integration

### Quick approach: browser-based

Run the dashboard and open it as a "menu bar app" using macOS features:

```bash
# Start the dashboard
go run contrib/dashboard/server.go &

# Open in a minimal browser window (macOS)
open http://localhost:8085
```

For a more app-like experience, use [Fluid](https://fluidapp.com/) or
[Unite](https://www.bzgapps.com/unite) to wrap the URL as a standalone
macOS app. Or use Chrome's "Create Shortcut" with "Open as window" to
get a frameless window.

### Native menu bar (future sketch)

A true macOS menu bar icon that shows the conflict count badge:

```go
// Sketch using github.com/caseymrm/menuet
//go:build darwin && ignore

package main

import (
    "fmt"
    "github.com/caseymrm/menuet"
)

func main() {
    go func() {
        // Poll aq filesystem every 5s
        for {
            active := readActive()
            conflicts := detectConflicts(active)

            title := fmt.Sprintf("aq:%d", len(conflicts))
            if len(conflicts) > 0 {
                title = fmt.Sprintf("aq:%d!", len(conflicts))
            }
            menuet.App().SetMenuState(&menuet.MenuState{
                Title: title,
            })
            menuet.App().MenuChanged()
            time.Sleep(5 * time.Second)
        }
    }()

    menuet.App().Label = "com.aq.menubar"
    menuet.App().Children = func() []menuet.MenuItem {
        active := readActive()
        conflicts := detectConflicts(active)

        items := []menuet.MenuItem{
            {Text: fmt.Sprintf("%d active broadcasts", len(active))},
            {Text: fmt.Sprintf("%d conflicts", len(conflicts))},
            {Type: menuet.Separator},
        }
        for _, c := range conflicts {
            items = append(items, menuet.MenuItem{
                Text: fmt.Sprintf("[%s] %s <-> %s",
                    c.Severity, c.A.Agent, c.B.Agent),
            })
        }
        items = append(items, menuet.MenuItem{Type: menuet.Separator})
        items = append(items, menuet.MenuItem{
            Text: "Open Dashboard",
            Clicked: func() { exec.Command("open", "http://localhost:8085").Run() },
        })
        return items
    }
    menuet.App().RunApplication()
}
```

Alternative libraries:
- [progrium/macdriver](https://github.com/progrium/macdriver) -- lower level, more control
- [fyne-io/systray](https://github.com/fyne-io/systray) -- cross-platform system tray

The menu bar approach works well because aq is ambient by design: a
small icon showing "aq:0" (no conflicts) or "aq:2!" (two conflicts)
is exactly the level of awareness gossip should provide. You glance
at it. If the number is zero, you forget about it. If it is nonzero,
you click to see details.

## No Dependencies

The dashboard has zero external dependencies. It uses only the Go
standard library (`net/http`, `encoding/json`, `os`). The HTML/CSS/JS
is embedded directly. No npm, no webpack, no node_modules.
