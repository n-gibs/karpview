package main

import (
	cryptorand "crypto/rand"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/nikgibson/karpview/internal/analyzer"
	"github.com/nikgibson/karpview/internal/cluster"
	"github.com/nikgibson/karpview/internal/printer"
)

// version is set at build time via -ldflags "-X main.version=<tag>".
var version = "dev"

// Exit codes:
//
//	0 = all nodes consolidatable
//	1 = one or more nodes blocked (business result)
//	2 = runtime error (API failure, kubeconfig error, timeout)
const (
	exitOK      = 0
	exitBlocked = 1
	exitError   = 2
)

// logRecord is the NDJSON schema emitted to stderr when KARPVIEW_LOG_FORMAT=json.
type logRecord struct {
	Ts                  string `json:"ts"`
	Level               string `json:"level"`
	Msg                 string `json:"msg"`
	Version             string `json:"version"`
	Cluster             string `json:"cluster,omitempty"`
	PhaseFetchMs        int64  `json:"phase_fetch_ms"`
	PhaseAnalyzeMs      int64  `json:"phase_analyze_ms"`
	NodesTotal          int    `json:"nodes_total"`
	NodesBlocked        int    `json:"nodes_blocked"`
	NodesDraining       int    `json:"nodes_draining"`
	NodesConsolidatable int    `json:"nodes_consolidatable"`
	ExitCode            int    `json:"exit_code"`
	RunID               string `json:"run_id"`
	Error               string `json:"error,omitempty"`
}

// writeLog emits rec as NDJSON when jsonLog is true, otherwise writes text.
func writeLog(w io.Writer, jsonLog bool, rec logRecord, text string) {
	if jsonLog {
		_ = json.NewEncoder(w).Encode(rec)
		return
	}
	fmt.Fprint(w, text)
}

// newRunID returns a short random hex string for log correlation.
func newRunID() string {
	var b [8]byte
	if _, err := cryptorand.Read(b[:]); err != nil {
		return fmt.Sprintf("%x", time.Now().UnixNano())
	}
	return fmt.Sprintf("%x", b)
}

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr, nil))
}

// run is the testable entry point. fetcher may be nil, in which case a real
// cluster client is created from the flags.
func run(args []string, stdout, stderr io.Writer, fetcher cluster.Fetcher) int {
	if len(args) > 0 && args[0] == "budgets" {
		return runBudgets(args[1:], stdout, stderr, fetcher)
	}
	rid := newRunID()
	jsonLog := os.Getenv("KARPVIEW_LOG_FORMAT") == "json"

	fs := flag.NewFlagSet("karpview", flag.ContinueOnError)
	fs.SetOutput(stderr)
	kubeContext := fs.String("context", "", "kubeconfig context to use (default: current context)")
	timeout := fs.Duration("timeout", 30*time.Second, "timeout for Kubernetes API calls")
	showVersion := fs.Bool("version", false, "print version and exit")
	outputFormat := fs.String("o", "text", "output format: text or json")
	fs.StringVar(outputFormat, "output", "text", "output format: text or json")

	fs.Usage = func() {
		fmt.Fprintf(stderr, "Usage: karpview [flags]\n\nFlags:\n")
		fs.PrintDefaults()
		fmt.Fprintf(stderr, "\nExit codes:\n")
		fmt.Fprintf(stderr, "  0  all nodes are consolidatable\n")
		fmt.Fprintf(stderr, "  1  one or more nodes are blocked\n")
		fmt.Fprintf(stderr, "  2  runtime or configuration error\n")
	}

	if err := fs.Parse(args); err != nil {
		return exitError
	}

	if *showVersion {
		fmt.Fprintln(stdout, version)
		return exitOK
	}

	if *outputFormat != "text" && *outputFormat != "json" {
		fmt.Fprintf(stderr, "error: unsupported output format %q (use \"text\" or \"json\")\n", *outputFormat)
		return exitError
	}

	if fetcher == nil {
		clients, err := cluster.New(*kubeContext)
		if err != nil {
			writeLog(stderr, jsonLog, logRecord{
				Ts:       time.Now().UTC().Format(time.RFC3339Nano),
				Level:    "error",
				Msg:      "client init failed",
				Version:  version,
				ExitCode: exitError,
				RunID:    rid,
				Error:    err.Error(),
			}, fmt.Sprintf("error: %v\n", err))
			return exitError
		}
		fetcher = clients
	}

	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()

	fetchStart := time.Now()
	data, err := fetcher.Fetch(ctx)
	fetchDur := time.Since(fetchStart)
	if err != nil {
		writeLog(stderr, jsonLog, logRecord{
			Ts:           time.Now().UTC().Format(time.RFC3339Nano),
			Level:        "error",
			Msg:          "fetch failed",
			Version:      version,
			PhaseFetchMs: fetchDur.Milliseconds(),
			ExitCode:     exitError,
			RunID:        rid,
			Error:        err.Error(),
		}, fmt.Sprintf("error: %v\n", err))
		return exitError
	}

	analyzeStart := time.Now()
	results := analyzer.Analyze(data)
	analyzeDur := time.Since(analyzeStart)

	switch *outputFormat {
	case "json":
		if err := printJSON(stdout, results); err != nil {
			fmt.Fprintf(stderr, "error: marshaling json: %v\n", err)
			return exitError
		}
	default:
		printer.Print(stdout, data.ClusterName, results)
	}

	blocked := 0
	draining := 0
	for _, r := range results {
		switch r.Status {
		case analyzer.StatusBlocked:
			blocked++
		case analyzer.StatusDraining:
			draining++
		}
	}
	finalCode := exitOK
	if blocked > 0 {
		finalCode = exitBlocked
	}
	writeLog(stderr, jsonLog, logRecord{
		Ts:                  time.Now().UTC().Format(time.RFC3339Nano),
		Level:               "info",
		Msg:                 "run complete",
		Version:             version,
		Cluster:             data.ClusterName,
		PhaseFetchMs:        fetchDur.Milliseconds(),
		PhaseAnalyzeMs:      analyzeDur.Milliseconds(),
		NodesTotal:          len(data.Nodes),
		NodesBlocked:        blocked,
		NodesDraining:       draining,
		NodesConsolidatable: len(data.Nodes) - blocked - draining,
		ExitCode:            finalCode,
		RunID:               rid,
	}, fmt.Sprintf("fetch=%s analyze=%s nodes=%d blocked=%d draining=%d\n",
		fetchDur.Round(time.Millisecond),
		analyzeDur.Round(time.Millisecond),
		len(data.Nodes),
		blocked,
		draining,
	))
	return finalCode
}

// jsonNode is the JSON representation of a node analysis result.
type jsonNode struct {
	NodeName string        `json:"nodeName"`
	NodePool string        `json:"nodePool"`
	Status   string        `json:"status"`
	Blockers []jsonBlocker `json:"blockers"`
}

// jsonBlocker is the JSON representation of a single consolidation blocker.
type jsonBlocker struct {
	Type      string `json:"type"`
	Name      string `json:"name"`
	Namespace string `json:"namespace,omitempty"`
	PodName   string `json:"podName,omitempty"`
}

// printJSON writes analysis results as a JSON array to w.
func printJSON(w io.Writer, results []analyzer.NodeResult) error {
	nodes := make([]jsonNode, len(results))
	for i, r := range results {
		blockers := make([]jsonBlocker, len(r.Blockers))
		for j, b := range r.Blockers {
			blockers[j] = jsonBlocker{
				Type:      b.Type,
				Name:      b.Name,
				Namespace: b.Namespace,
				PodName:   b.PodName,
			}
		}
		nodes[i] = jsonNode{
			NodeName: r.NodeName,
			NodePool: r.NodePool,
			Status:   string(r.Status),
			Blockers: blockers,
		}
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(nodes)
}

// runBudgets implements the "karpview budgets" subcommand.
func runBudgets(args []string, stdout, stderr io.Writer, fetcher cluster.Fetcher) int {
	fs := flag.NewFlagSet("karpview budgets", flag.ContinueOnError)
	fs.SetOutput(stderr)
	kubeContext := fs.String("context", "", "kubeconfig context to use (default: current context)")
	timeout := fs.Duration("timeout", 30*time.Second, "timeout for Kubernetes API calls")
	outputFormat := fs.String("o", "text", "output format: text or json")
	fs.StringVar(outputFormat, "output", "text", "output format: text or json")

	if err := fs.Parse(args); err != nil {
		return exitError
	}

	if *outputFormat != "text" && *outputFormat != "json" {
		fmt.Fprintf(stderr, "error: unsupported output format %q (use \"text\" or \"json\")\n", *outputFormat)
		return exitError
	}

	if fetcher == nil {
		clients, err := cluster.New(*kubeContext)
		if err != nil {
			fmt.Fprintf(stderr, "error: %v\n", err)
			return exitError
		}
		fetcher = clients
	}

	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()

	data, err := fetcher.Fetch(ctx)
	if err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)
		return exitError
	}

	summaries := analyzer.AnalyzeBudgets(data)

	switch *outputFormat {
	case "json":
		printBudgetsJSON(stdout, summaries)
	default:
		printer.PrintBudgets(stdout, data.ClusterName, summaries)
	}

	return exitOK
}

type jsonBudgetStats struct {
	Total    int `json:"total"`
	Deleting int `json:"deleting"`
	NotReady int `json:"notReady"`
}

type jsonBudgetRule struct {
	Nodes        string   `json:"nodes"`
	Reasons      []string `json:"reasons"`
	Schedule     string   `json:"schedule"`
	Duration     string   `json:"duration"`
	WindowActive bool     `json:"windowActive"`
	Headroom     int      `json:"headroom"`
	Blocked      bool     `json:"blocked"`
}

type jsonBudgetSummary struct {
	PoolName string           `json:"poolName"`
	Policy   string           `json:"policy"`
	Stats    jsonBudgetStats  `json:"stats"`
	Rules    []jsonBudgetRule `json:"rules"`
}

func printBudgetsJSON(w io.Writer, summaries []analyzer.NodePoolBudgetSummary) {
	out := make([]jsonBudgetSummary, len(summaries))
	for i, s := range summaries {
		rules := make([]jsonBudgetRule, len(s.Rules))
		for j, r := range s.Rules {
			rules[j] = jsonBudgetRule{
				Nodes:        r.Nodes,
				Reasons:      r.Reasons,
				Schedule:     r.Schedule,
				Duration:     r.Duration,
				WindowActive: r.WindowActive,
				Headroom:     r.Headroom,
				Blocked:      r.Blocked,
			}
		}
		out[i] = jsonBudgetSummary{
			PoolName: s.PoolName,
			Policy:   s.Policy,
			Stats: jsonBudgetStats{
				Total:    s.Stats.Total,
				Deleting: s.Stats.Deleting,
				NotReady: s.Stats.NotReady,
			},
			Rules: rules,
		}
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	_ = enc.Encode(out)
}
