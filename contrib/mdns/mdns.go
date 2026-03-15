//go:build ignore

// mdns.go — mDNS/DNS-SD transport example for aq
//
// This is a standalone demonstration of how aq broadcasts would work
// over mDNS using DNS-SD service discovery. It is not part of the main
// aq binary. To run it:
//
//   go run mdns.go -register -agent "origin/feat-auth" -conjecture C-1 -phase proof -files "auth.py,session.py"
//   go run mdns.go -browse
//
// Dependencies:
//   go get github.com/hashicorp/mdns
//
// This file is not compiled by default (//go:build ignore) and is not
// referenced by go.mod. It exists as a concrete, runnable example of
// the mDNS transport described in docs/TRANSPORT-RESEARCH.md §3.7.
//
// The hashicorp/mdns library provides a pure-Go mDNS implementation
// that works on macOS, Linux, and Windows without requiring Bonjour
// or Avahi to be installed.

package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/hashicorp/mdns"
)

const (
	// ServiceType is the DNS-SD service type for aq broadcasts.
	// Follows RFC 6763 naming: _<application>._<transport>.
	// The _tcp suffix is conventional even though aq does not use
	// TCP connections -- DNS-SD requires either _tcp or _udp.
	ServiceType = "_aq._tcp"

	// DefaultDomain is the mDNS domain for local network discovery.
	DefaultDomain = "local."

	// DefaultTTL matches aq's default broadcast TTL of 300 seconds.
	DefaultTTL = 300

	// WhisperTTL matches aq's whisper TTL of 60 seconds.
	WhisperTTL = 60
)

// broadcastTXT builds DNS TXT records from aq broadcast fields.
// Each key=value pair becomes a separate TXT string per RFC 6763 §6.
//
// DNS TXT records have a 255-byte-per-string limit. The aq broadcast
// payload fits comfortably: even with long file lists, the total is
// well under the ~1300-byte aggregate limit.
func broadcastTXT(agent, conjecture, claim, phase, status, files, worktree string) []string {
	txt := []string{
		fmt.Sprintf("conjecture=%s", conjecture),
		fmt.Sprintf("phase=%s", phase),
		fmt.Sprintf("status=%s", status),
		fmt.Sprintf("files=%s", files),
		fmt.Sprintf("worktree=%s", worktree),
	}
	if claim != "" {
		txt = append(txt, fmt.Sprintf("claim=%s", claim))
	}
	return txt
}

// parseTXT extracts aq broadcast fields from DNS TXT records.
func parseTXT(records []string) map[string]string {
	fields := make(map[string]string)
	for _, record := range records {
		parts := strings.SplitN(record, "=", 2)
		if len(parts) == 2 {
			fields[parts[0]] = parts[1]
		}
	}
	return fields
}

// severityLabel returns the conflict severity when two agents share
// files, based on CPRR phase. This is the same logic as
// conflict.py / main.go but extracted here for the mDNS example.
func severityLabel(phaseA, phaseB string) string {
	proofPhases := map[string]bool{"proof": true}

	if proofPhases[phaseA] && proofPhases[phaseB] {
		return "HIGH"
	}
	if proofPhases[phaseA] || proofPhases[phaseB] {
		return "MEDIUM"
	}
	return "LOW"
}

// filesOverlap checks whether two comma-separated file lists share
// any entries.
func filesOverlap(a, b string) []string {
	setA := make(map[string]bool)
	for _, f := range strings.Split(a, ",") {
		f = strings.TrimSpace(f)
		if f != "" {
			setA[f] = true
		}
	}

	var overlap []string
	for _, f := range strings.Split(b, ",") {
		f = strings.TrimSpace(f)
		if f != "" && setA[f] {
			overlap = append(overlap, f)
		}
	}
	return overlap
}

// register publishes an aq broadcast as an mDNS service.
// The service stays registered until the process receives SIGINT/SIGTERM,
// at which point it deregisters -- matching aq's lifecycle semantics.
func register(agent, conjecture, claim, phase, status, files, worktree string, ttl int) {
	txt := broadcastTXT(agent, conjecture, claim, phase, status, files, worktree)

	// The hashicorp/mdns library expects a ServiceEntry-compatible config.
	// Port 0: aq does not listen on a TCP port. The service registration
	// is purely for presence broadcasting via TXT records.
	service, err := mdns.NewMDNSService(
		agent,       // Instance name: the agent address
		ServiceType, // Service type: _aq._tcp
		"",          // Domain: empty = default (local.)
		"",          // Host: empty = auto-detect
		0,           // Port: not used by aq
		nil,         // IPs: nil = auto-detect
		txt,         // TXT records: the broadcast payload
	)
	if err != nil {
		log.Fatalf("Failed to create mDNS service: %v", err)
	}

	// Create the mDNS server. This starts responding to queries
	// and proactively announces the service on the network.
	server, err := mdns.NewServer(&mdns.Config{Zone: service})
	if err != nil {
		log.Fatalf("Failed to start mDNS server: %v", err)
	}

	fmt.Printf("aq broadcast registered via mDNS:\n")
	fmt.Printf("  agent:       %s\n", agent)
	fmt.Printf("  conjecture:  %s\n", conjecture)
	fmt.Printf("  phase:       %s\n", phase)
	fmt.Printf("  status:      %s\n", status)
	fmt.Printf("  files:       %s\n", files)
	fmt.Printf("  worktree:    %s\n", worktree)
	fmt.Printf("  ttl:         %d seconds\n", ttl)
	fmt.Printf("\nBroadcasting on %s. Ctrl-C to deregister and exit.\n", ServiceType)

	// Wait for interrupt signal. When the process exits, the mDNS
	// server shuts down and the service is deregistered from the
	// network. This is exactly aq's lifecycle: broadcast lives as
	// long as the agent is working.
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	<-sig

	fmt.Printf("\nDeregistering broadcast for %s...\n", agent)
	server.Shutdown()
	fmt.Println("Done. Broadcast removed from network.")
}

// browse discovers aq broadcasts on the local network via mDNS.
// It performs a one-shot query and prints all discovered agents,
// then checks for conflicts between them.
func browse(myFiles, myPhase string) {
	fmt.Printf("Browsing for aq agents on %s...\n\n", ServiceType)

	// Channel to receive discovered services.
	entries := make(chan *mdns.ServiceEntry, 16)

	// Collect results in a slice for conflict analysis.
	type agent struct {
		name   string
		fields map[string]string
	}
	var agents []agent

	// Start a goroutine to collect results.
	done := make(chan struct{})
	go func() {
		for entry := range entries {
			fields := parseTXT(entry.InfoFields)
			a := agent{name: entry.Name, fields: fields}
			agents = append(agents, a)

			fmt.Printf("Discovered agent: %s\n", entry.Name)
			fmt.Printf("  host:        %s\n", entry.Host)
			fmt.Printf("  conjecture:  %s\n", fields["conjecture"])
			fmt.Printf("  claim:       %s\n", fields["claim"])
			fmt.Printf("  phase:       %s\n", fields["phase"])
			fmt.Printf("  status:      %s\n", fields["status"])
			fmt.Printf("  files:       %s\n", fields["files"])
			fmt.Printf("  worktree:    %s\n", fields["worktree"])
			fmt.Println()
		}
		close(done)
	}()

	// Perform the mDNS lookup. This sends a multicast query to
	// 224.0.0.251:5353 and waits for responses.
	params := mdns.DefaultParams(ServiceType)
	params.Entries = entries
	params.Timeout = 3 * time.Second

	err := mdns.Query(params)
	if err != nil {
		log.Fatalf("mDNS query failed: %v", err)
	}
	close(entries)
	<-done

	if len(agents) == 0 {
		fmt.Println("No aq agents found on the network.")
		return
	}

	// Conflict detection: if the caller specified files and phase,
	// check for conflicts with discovered agents.
	if myFiles != "" {
		fmt.Println("--- Conflict Check ---")
		for _, a := range agents {
			theirFiles := a.fields["files"]
			theirPhase := a.fields["phase"]
			overlap := filesOverlap(myFiles, theirFiles)
			if len(overlap) > 0 {
				severity := severityLabel(myPhase, theirPhase)
				fmt.Printf("CONFLICT [%s] with %s\n", severity, a.name)
				fmt.Printf("  shared files:  %s\n", strings.Join(overlap, ", "))
				fmt.Printf("  their phase:   %s\n", theirPhase)
				fmt.Printf("  your phase:    %s\n", myPhase)
				fmt.Println()
			}
		}
	}

	// Pairwise conflict detection among all discovered agents.
	if len(agents) > 1 {
		fmt.Println("--- Pairwise Conflicts ---")
		conflicts := 0
		for i := 0; i < len(agents); i++ {
			for j := i + 1; j < len(agents); j++ {
				a, b := agents[i], agents[j]
				overlap := filesOverlap(a.fields["files"], b.fields["files"])
				if len(overlap) > 0 {
					severity := severityLabel(a.fields["phase"], b.fields["phase"])
					fmt.Printf("CONFLICT [%s]: %s <-> %s\n", severity, a.name, b.name)
					fmt.Printf("  shared files: %s\n", strings.Join(overlap, ", "))
					fmt.Println()
					conflicts++
				}
			}
		}
		if conflicts == 0 {
			fmt.Println("No conflicts detected between discovered agents.")
		}
	}
}

func main() {
	// Subcommands via flag sets.
	if len(os.Args) < 2 {
		fmt.Fprintf(os.Stderr, "Usage: %s <-register|-browse> [options]\n\n", os.Args[0])
		fmt.Fprintf(os.Stderr, "Register an aq broadcast:\n")
		fmt.Fprintf(os.Stderr, "  %s -register -agent NAME -conjecture C-1 -phase proof -files \"a.py,b.py\"\n\n", os.Args[0])
		fmt.Fprintf(os.Stderr, "Browse for aq broadcasts:\n")
		fmt.Fprintf(os.Stderr, "  %s -browse [-files \"a.py\" -phase proof]  (optional: check conflicts)\n", os.Args[0])
		os.Exit(1)
	}

	// Parse flags.
	doRegister := flag.Bool("register", false, "Register an aq broadcast via mDNS")
	doBrowse := flag.Bool("browse", false, "Browse for aq broadcasts via mDNS")
	agent := flag.String("agent", "", "Agent address (e.g., origin/feat-auth)")
	conjecture := flag.String("conjecture", "C-0", "Conjecture ID (e.g., C-1)")
	claim := flag.String("claim", "", "Conjecture claim (human-readable)")
	phase := flag.String("phase", "conjecture", "CPRR phase: conjecture|proof|refutation|refinement")
	status := flag.String("status", "prosecuting", "Status: prosecuting|done|blocked")
	files := flag.String("files", "", "Comma-separated files being touched")
	worktree := flag.String("worktree", "", "Worktree/branch name")
	ttl := flag.Int("ttl", DefaultTTL, "Broadcast TTL in seconds")

	flag.Parse()

	switch {
	case *doRegister:
		if *agent == "" {
			log.Fatal("-agent is required for registration")
		}
		if *files == "" {
			log.Fatal("-files is required for registration")
		}
		wt := *worktree
		if wt == "" {
			// Default worktree from agent address (last path segment).
			parts := strings.Split(*agent, "/")
			wt = parts[len(parts)-1]
		}
		register(*agent, *conjecture, *claim, *phase, *status, *files, wt, *ttl)

	case *doBrowse:
		browse(*files, *phase)

	default:
		fmt.Fprintf(os.Stderr, "Specify either -register or -browse\n")
		os.Exit(1)
	}
}
