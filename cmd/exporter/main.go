// Command exporter runs the scm-metrics-exporter: it polls a source-control
// provider (GitHub, GitLab) for open review items and security findings and
// exposes them as OpenTelemetry metrics, with the exporter (Prometheus or OTLP)
// selected at runtime.
//
// This is a scaffolding stub. The full wiring -- config loading, provider
// construction, the poller/collector, and the metrics pipeline -- lands in
// Epic 05 (see tasks/epic-05-exporter-binary.md).
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
	provider := flag.String("provider", "github", "source-control provider to poll (github|gitlab)")
	showVersion := flag.Bool("version", false, "print version and exit")
	flag.Parse()

	if *showVersion {
		fmt.Printf("scm-metrics-exporter %s (commit %s, built %s)\n", version, commit, date)
		return
	}

	fmt.Fprintf(os.Stderr, "exporter: provider %q not yet implemented (see tasks/epic-05-exporter-binary.md)\n", *provider)
	os.Exit(1)
}
