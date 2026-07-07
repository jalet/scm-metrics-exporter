// Command operator runs the scm-metrics-exporter Kubernetes controller-manager.
// It reconciles GitHubMetricsExporter and GitLabMetricsExporter custom resources
// into exporter Deployments (plus Service and optional ServiceMonitor).
//
// This is a scaffolding stub. The manager wiring -- scheme registration, the
// controllers, leader election, and health probes -- lands in Epic 07 (see
// tasks/epic-07-operator-scaffolding.md).
package main

import (
	"flag"
	"fmt"
	"os"
)

// Build metadata, populated via -ldflags "-X main.version=...".
var (
	version = "dev"
	commit  = "none"
	date    = "unknown"
)

func main() {
	showVersion := flag.Bool("version", false, "print version and exit")
	flag.Parse()

	if *showVersion {
		fmt.Printf("scm-metrics-operator %s (commit %s, built %s)\n", version, commit, date)
		return
	}

	fmt.Fprintln(os.Stderr, "operator: not yet implemented (see tasks/epic-07-operator-scaffolding.md)")
	os.Exit(1)
}
