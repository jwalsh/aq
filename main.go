// aq — ambient agent queue
//
// Gossip layer (L1.5) for multi-agent development. Agents broadcast
// presence via filesystem-backed channels so peers detect semantic
// conflicts before they become merge conflicts.
//
// See spec.org for the full specification.
// See CLAUDE.md for agent instructions.
package main

import (
	"fmt"
	"os"
)

var (
	Version   = "dev"
	GitCommit = "none"
	BuildDate = "unknown"
)

func main() {
	args := os.Args[1:]

	if len(args) == 0 {
		printUsage()
		os.Exit(0)
	}

	switch args[0] {
	case "version", "--version", "-v":
		fmt.Printf("aq %s (commit: %s, built: %s)\n", Version, GitCommit, BuildDate)
	case "help", "--help", "-h":
		printUsage()
	default:
		fmt.Fprintf(os.Stderr, "aq: %s not yet implemented\n", args[0])
		fmt.Fprintf(os.Stderr, "see bead aq-os0 for Go port status\n")
		os.Exit(1)
	}
}

func printUsage() {
	fmt.Print(`aq — ambient agent queue (gossip layer for multi-agent development)

Usage: aq <command> [options]

Commands:
  announce, ann, a   Broadcast presence (high priority)
  whisper            Broadcast presence (low priority, short TTL)
  check              Check for conflicts with active broadcasts
  status, ls         List active broadcasts
  init               Create ~/.aq directory structure
  doctor             Health check
  quickstart, prime  Agent-consumable context dump
  version            Show version info
  help               Show this help

Global flags:
  --json             Machine-readable JSON output
  --channel <name>   Channel name (default: broadcast)

Status: stub — run 'bd show aq-os0' for implementation progress.
`)
}
