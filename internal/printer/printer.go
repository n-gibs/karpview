package printer

import (
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/nikgibson/karpview/internal/analyzer"
	"golang.org/x/term"
)

const (
	colorRed    = "\033[31m"
	colorGreen  = "\033[32m"
	colorYellow = "\033[33m"
	colorReset  = "\033[0m"

	emDash               = "—"
	policyWhenEmpty      = "WhenEmpty"
	policyWhenUnderutilized = "WhenUnderutilized"
)

// isColorEnabled returns true when the output stream supports color.
// It respects the NO_COLOR convention (https://no-color.org) and checks
// whether w is a terminal via golang.org/x/term.
func isColorEnabled(w io.Writer) bool {
	if os.Getenv("NO_COLOR") != "" {
		return false
	}
	if os.Getenv("TERM") == "dumb" {
		return false
	}
	if f, ok := w.(*os.File); ok {
		return term.IsTerminal(int(f.Fd()))
	}
	return false
}

// Print writes the analysis results to w with colored, aligned columns.
func Print(w io.Writer, clusterName string, results []analyzer.NodeResult) {
	color := isColorEnabled(w)

	fmt.Fprintf(w, "\nCluster: %s\n\n", sanitize(clusterName))

	if len(results) == 0 {
		fmt.Fprintln(w, "No nodes found.")
		return
	}

	// Sort: BLOCKED nodes first, DRAINING second, then others.
	sort.SliceStable(results, func(i, j int) bool {
		ri, rj := statusRank(results[i].Status), statusRank(results[j].Status)
		if ri != rj {
			return ri < rj
		}
		return results[i].NodeName < results[j].NodeName
	})

	// Calculate column widths for alignment.
	maxName := 0
	maxPool := 0
	maxConsolidation := len("daemon-only")    // minimum — longest static value
	maxPolicy := len("WhenUnderutilized")     // minimum — longest static abbreviation
	maxBudget := len("BUDGET")               // minimum — header width
	maxDisruption := len("DISRUPTION")       // minimum — header width
	for i := range results {
		r := &results[i]
		if len(r.NodeName) > maxName {
			maxName = len(r.NodeName)
		}
		if len(r.NodePool) > maxPool {
			maxPool = len(r.NodePool)
		}
		if n := len(formatConsolidation(*r)); n > maxConsolidation {
			maxConsolidation = n
		}
		if n := len(formatPolicy(*r)); n > maxPolicy {
			maxPolicy = n
		}
		if n := len(r.BudgetDisplay); n > maxBudget {
			maxBudget = n
		}
		if n := len(r.DisruptionDisplay); n > maxDisruption {
			maxDisruption = n
		}
	}

	blockedCount := 0
	drainingCount := 0
	for i := range results {
		r := &results[i]
		status := formatStatus(r.Status, color)
		reason := formatReason(*r)

		fmt.Fprintf(w, "%s  %-*s   NodePool: %-*s   %-*s   %-*s   %-*s   %s   %s\n",
			status,
			maxName, sanitize(r.NodeName),
			maxPool, sanitize(r.NodePool),
			maxConsolidation, formatConsolidation(*r),
			maxPolicy, formatPolicy(*r),
			maxBudget, sanitize(r.BudgetDisplay),
			formatDisruptionCol(*r, maxDisruption, color),
			reason,
		)
		switch r.Status {
		case analyzer.StatusBlocked:
			blockedCount++
		case analyzer.StatusDraining:
			drainingCount++
		default:
		}
	}
	fmt.Fprintf(w, "\n%d node(s) blocked, %d draining / %d total\n\n",
		blockedCount, drainingCount, len(results))
}

// statusRank controls print order: lower rank prints first.
// BLOCKED=0, DRAINING=1, READY/UNKNOWN=2.
func statusRank(s analyzer.NodeStatus) int {
	switch s {
	case analyzer.StatusBlocked:
		return 0
	case analyzer.StatusDraining:
		return 1
	default:
		return 2
	}
}

// formatStatus returns the status string, optionally with ANSI color codes.
func formatStatus(s analyzer.NodeStatus, color bool) string {
	switch s {
	case analyzer.StatusBlocked:
		if color {
			return fmt.Sprintf("%s%-8s%s", colorRed, s, colorReset)
		}
		return fmt.Sprintf("%-8s", s)
	case analyzer.StatusDraining:
		if color {
			return fmt.Sprintf("%s%-8s%s", colorYellow, s, colorReset)
		}
		return fmt.Sprintf("%-8s", s)
	case analyzer.StatusConsolidatable:
		if color {
			return fmt.Sprintf("%s%-8s%s", colorGreen, s, colorReset)
		}
		return fmt.Sprintf("%-8s", s)
	default:
		return fmt.Sprintf("%-8s", s)
	}
}

// sanitize removes ANSI CSI escape sequences and non-printable runes from s
// in a single pass, allocating one strings.Builder instead of two.
func sanitize(s string) string {
	var out strings.Builder
	out.Grow(len(s))
	i := 0
	for i < len(s) {
		// Strip ANSI CSI: ESC [ <params> <final-byte 0x40-0x7e>
		if s[i] == '\x1b' && i+1 < len(s) && s[i+1] == '[' {
			i += 2
			for i < len(s) && (s[i] < 0x40 || s[i] > 0x7e) {
				i++
			}
			if i < len(s) {
				i++ // consume final byte
			}
			continue
		}

		// Decode one UTF-8 rune and filter non-printable.
		r, size := rune(s[i]), 1
		if s[i] >= 0x80 {
			r, size = utf8.DecodeRuneInString(s[i:])
		}
		if unicode.IsPrint(r) {
			out.WriteString(s[i : i+size])
		}
		i += size
	}
	return out.String()
}

// formatConsolidation returns the consolidation classification string for display.
func formatConsolidation(r analyzer.NodeResult) string {
	switch r.ConsolidationClass {
	case analyzer.ConsolidationEmpty:
		return "empty"
	case analyzer.ConsolidationDaemonOnly:
		return "daemon-only"
	default:
		return fmt.Sprintf("%d%% cpu / %d%% mem",
			int(r.CPURequestFraction*100),
			int(r.MemRequestFraction*100),
		)
	}
}

// formatPolicy returns the POLICY column value for a node result.
// WhenEmptyOrUnderutilized is abbreviated to WhenUnderutilized for column width.
// A [skip] suffix is added when the policy is WhenEmpty but the node has
// non-daemon workload pods — Karpenter will never target this node for
// consolidation regardless of utilization, so the CONSOLIDATION column alone
// is misleading without this flag.
func formatPolicy(r analyzer.NodeResult) string {
	if r.NodePoolPolicy == "" {
		return emDash
	}
	var policy string
	switch r.NodePoolPolicy {
	case policyWhenEmpty:
		policy = policyWhenEmpty
	case "WhenEmptyOrUnderutilized":
		policy = policyWhenUnderutilized
	default:
		policy = r.NodePoolPolicy
	}
	if r.ConsolidateAfter != "" {
		policy = fmt.Sprintf("%s (%s)", policy, r.ConsolidateAfter)
	}
	if r.NodePoolPolicy == policyWhenEmpty && r.ConsolidationClass == analyzer.ConsolidationNormal {
		policy += " [skip]"
	}
	return policy
}

// PrintBudgets writes per-NodePool budget summaries to w.
// One block per NodePool, rules indented underneath.
func PrintBudgets(w io.Writer, clusterName string, summaries []analyzer.NodePoolBudgetSummary) {
	color := isColorEnabled(w)
	fmt.Fprintf(w, "Cluster: %s\n", sanitize(clusterName))

	if len(summaries) == 0 {
		fmt.Fprintln(w, "No NodePools found.")
		return
	}

	blockedPools := 0
	for _, s := range summaries {
		policy := s.Policy
		if policy == "WhenEmptyOrUnderutilized" {
			policy = policyWhenUnderutilized
		}
		policyPart := ""
		if policy != "" {
			policyPart = fmt.Sprintf("  (%s)", policy)
		}
		fmt.Fprintf(w, "\nNodePool: %s%s   %d nodes / %d deleting / %d not-ready\n",
			sanitize(s.PoolName), policyPart,
			s.Stats.Total, s.Stats.Deleting, s.Stats.NotReady)

		poolBlocked := false
		for _, r := range s.Rules {
			reasons := "all"
			if len(r.Reasons) > 0 {
				reasons = strings.Join(r.Reasons, ",")
			}
			window := "always"
			if r.Schedule != "" {
				window = r.Schedule
				if r.Duration != "" {
					window += "/" + r.Duration
				}
			}

			if !r.WindowActive {
				marker := "[inactive]"
				if color {
					marker = colorYellow + marker + colorReset
				}
				fmt.Fprintf(w, "  nodes: %-5s  reasons: %-12s  window: %s  %s\n",
					r.Nodes, reasons, window, marker)
			} else {
				headroom := fmt.Sprintf("headroom: %d/%d", r.Headroom, s.Stats.Total)
				suffix := ""
				if r.Blocked {
					m := "[BLOCKED]"
					if color {
						m = colorRed + m + colorReset
					}
					suffix = "  " + m
					poolBlocked = true
				}
				fmt.Fprintf(w, "  nodes: %-5s  reasons: %-12s  window: %-22s  %s%s\n",
					r.Nodes, reasons, window, headroom, suffix)
			}
		}
		if poolBlocked {
			blockedPools++
		}
	}

	fmt.Fprintf(w, "\n%d NodePool(s) / %d with blocked budgets\n", len(summaries), blockedPools)
}

// formatReason returns a human-readable reason string for a node result.
func formatReason(r analyzer.NodeResult) string {
	switch r.Status {
	case analyzer.StatusConsolidatable:
		return emDash
	case analyzer.StatusDraining:
		return "Disruption in progress"
	case analyzer.StatusUnknown:
		return "Unknown"
	default:
	}

	parts := make([]string, 0, len(r.Blockers))
	for _, b := range r.Blockers {
		switch b.Type {
		case "PDB":
			if b.PodName != "" {
				parts = append(parts, fmt.Sprintf("PDB: %s (%s) via %s",
					sanitize(b.Name), sanitize(b.Namespace), sanitize(b.PodName)))
			} else {
				parts = append(parts, fmt.Sprintf("PDB: %s (%s)",
					sanitize(b.Name), sanitize(b.Namespace)))
			}
		case "Terminating":
			parts = append(parts, fmt.Sprintf("Terminating: %s (%s)",
				sanitize(b.Name), sanitize(b.Namespace)))
		case "Annotation":
			parts = append(parts, fmt.Sprintf("Annotation: %s", sanitize(b.Name)))
		default:
			parts = append(parts, fmt.Sprintf("%s: %s", sanitize(b.Type), sanitize(b.Name)))
		}
	}
	return strings.Join(parts, ", ")
}

// formatDisruptionCol returns the DISRUPTION column value, padded to maxWidth
// and optionally colored. Color is determined from raw NodeResult fields —
// not by parsing DisruptionDisplay — so that mixed signals get the
// highest-severity color (red > yellow).
func formatDisruptionCol(r analyzer.NodeResult, maxWidth int, color bool) string {
	raw := sanitize(r.DisruptionDisplay)
	pad := strings.Repeat(" ", maxWidth-len(raw))
	if !color || raw == emDash {
		return raw + pad
	}
	if r.ExpiryState == "expired" {
		return colorRed + raw + colorReset + pad
	}
	// yellow for: unhealthy, drifted, expiring
	if len(r.HealthIssues) > 0 || r.Drifted || r.ExpiryState == "expiring" {
		return colorYellow + raw + colorReset + pad
	}
	return raw + pad
}
