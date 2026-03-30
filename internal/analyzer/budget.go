package analyzer

import (
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/robfig/cron/v3"
)

// poolStats holds counts of nodes in a NodePool needed for budget headroom calculation.
type poolStats struct {
	Total    int // all nodes in pool
	Deleting int // DeletionTimestamp != nil
	NotReady int // Ready condition = False or Unknown
}

// poolHeadroom computes how many additional node disruptions the budget allows.
// nodes is "20%" or a static integer string like "5". Negative results are clamped to 0.
func poolHeadroom(nodes string, total, deleting, notready int) int {
	var allowed int
	if strings.HasSuffix(nodes, "%") {
		pct, err := strconv.ParseFloat(strings.TrimSuffix(nodes, "%"), 64)
		if err != nil || pct < 0 {
			return 0
		}
		allowed = int(math.Ceil(float64(total) * pct / 100.0))
	} else {
		n, err := strconv.Atoi(nodes)
		if err != nil || n < 0 {
			return 0
		}
		allowed = n
	}
	h := allowed - deleting - notready
	if h < 0 {
		h = 0
	}
	return h
}

// budgetAppliesToReason returns true if the budget applies to the given disruption reason.
// A budget with nil/empty Reasons applies to all reasons.
func budgetAppliesToReason(b DisruptionBudget, reason string) bool {
	if len(b.Reasons) == 0 {
		return true
	}
	for _, r := range b.Reasons {
		if r == reason {
			return true
		}
	}
	return false
}

// scheduleWindowActive returns true if the cron schedule window is currently active.
// schedule supports standard 5-field cron and macros (@daily, @weekly, etc.).
// The window is active if the most recent fire time is within duration of now.
func scheduleWindowActive(schedule, duration string, now time.Time) bool {
	if schedule == "" {
		return false
	}
	dur, err := parseDuration(duration)
	if err != nil || dur <= 0 {
		return false
	}
	parser := cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow | cron.Descriptor)
	sched, err := parser.Parse(schedule)
	if err != nil {
		return false
	}
	// Find the most recent fire time: last Next() call before (now - dur + epsilon)
	prev := sched.Next(now.Add(-dur))
	return !prev.After(now)
}

// parseDuration parses compound durations like "10m", "1h30m", "30s".
func parseDuration(s string) (time.Duration, error) {
	if s == "" {
		return 0, fmt.Errorf("empty duration")
	}
	return time.ParseDuration(s)
}

// reasonLabel returns the single-letter label for a disruption reason.
func reasonLabel(reason string) string {
	switch reason {
	case "Empty":
		return "E"
	case "Underutilized":
		return "U"
	case "Drifted":
		return "D"
	default:
		return reason
	}
}

// consolidationClassToReason maps a node's ConsolidationClass to a Karpenter disruption reason.
func consolidationClassToReason(class string) string {
	switch class {
	case ConsolidationEmpty, ConsolidationDaemonOnly:
		return "Empty"
	default:
		return "Underutilized"
	}
}

type reasonHeadroom struct {
	label    string
	headroom int
	blocked  bool
	schedStr string // non-empty when schedule budget and window inactive
}

// evaluateBudgets computes budget headroom for the node's disruption reason.
// reason is the Karpenter disruption reason for this node ("Empty" or "Underutilized").
// policy omits "Underutilized" from display when "WhenEmpty".
// now is injected for deterministic testing.
func evaluateBudgets(budgets []DisruptionBudget, reason string, policy string, stats poolStats, now time.Time) (headroom int, blocked bool, display string) {
	if len(budgets) == 0 {
		h := poolHeadroom("10%", stats.Total, stats.Deleting, stats.NotReady)
		return h, h == 0, fmt.Sprintf("default 10%% (%d/%d avail)", h, stats.Total)
	}

	// Determine which reasons to evaluate for display
	evalReasons := []string{"Empty", "Underutilized", "Drifted"}
	if policy == "WhenEmpty" {
		evalReasons = []string{"Empty", "Drifted"}
	}

	// Check if all budgets are uniform (no reasons specified)
	allUniform := true
	for _, b := range budgets {
		if len(b.Reasons) > 0 {
			allUniform = false
			break
		}
	}

	if allUniform {
		return evaluateUniformBudgets(budgets, stats, now)
	}

	// Per-reason evaluation
	var results []reasonHeadroom
	for _, er := range evalReasons {
		applicable := filterBudgets(budgets, er)
		if len(applicable) == 0 {
			continue
		}
		h, bl, schedStr := evaluateApplicableBudgets(applicable, stats, now)
		results = append(results, reasonHeadroom{
			label:    reasonLabel(er),
			headroom: h,
			blocked:  bl,
			schedStr: schedStr,
		})
	}

	if len(results) == 0 {
		return 0, false, "—"
	}

	// Find overall minimum headroom
	minH := results[0].headroom
	for _, r := range results[1:] {
		if r.headroom < minH {
			minH = r.headroom
		}
	}

	leadLabel := reasonLabel(reason)
	return minH, minH == 0, formatPerReasonDisplay(results, leadLabel, stats.Total)
}

// filterBudgets returns only the budgets that apply to the given reason.
func filterBudgets(budgets []DisruptionBudget, reason string) []DisruptionBudget {
	var out []DisruptionBudget
	for _, b := range budgets {
		if budgetAppliesToReason(b, reason) {
			out = append(out, b)
		}
	}
	return out
}

// evaluateUniformBudgets handles the case where all budgets have no reason filter.
// Takes the minimum headroom across all budgets.
func evaluateUniformBudgets(budgets []DisruptionBudget, stats poolStats, now time.Time) (int, bool, string) {
	minH := math.MaxInt32
	var schedDisplay string

	for _, b := range budgets {
		if b.Schedule != "" {
			active := scheduleWindowActive(b.Schedule, b.Duration, now)
			if !active {
				// Inactive schedule: surface it but don't count toward headroom
				schedDisplay = fmt.Sprintf("%s/%s [inactive]", b.Schedule, b.Duration)
				continue
			}
			schedDisplay = fmt.Sprintf("%s/%s", b.Schedule, b.Duration)
		}
		h := poolHeadroom(b.Nodes, stats.Total, stats.Deleting, stats.NotReady)
		if h < minH {
			minH = h
		}
	}

	if minH == math.MaxInt32 {
		// Only inactive schedule budgets
		if schedDisplay != "" {
			return 0, false, schedDisplay
		}
		return 0, false, "—"
	}

	if minH < 0 {
		minH = 0
	}
	blocked := minH == 0

	// Find the first non-schedule budget for display label
	displayNodes := budgets[0].Nodes
	for _, b := range budgets {
		if b.Schedule == "" {
			displayNodes = b.Nodes
			break
		}
	}

	if schedDisplay != "" {
		if blocked {
			return minH, blocked, schedDisplay + " [BLOCKED]"
		}
		return minH, blocked, schedDisplay + " [inactive]"
	}

	if blocked {
		return minH, blocked, fmt.Sprintf("%s [BLOCKED]", displayNodes)
	}
	return minH, blocked, fmt.Sprintf("%s (%d/%d avail)", displayNodes, minH, stats.Total)
}

// evaluateApplicableBudgets takes the minimum headroom across a pre-filtered set of budgets.
// Returns (headroom, blocked, schedStr) where schedStr is non-empty only when a schedule
// budget is the sole contributor and its window is inactive.
func evaluateApplicableBudgets(budgets []DisruptionBudget, stats poolStats, now time.Time) (int, bool, string) {
	minH := math.MaxInt32
	var schedStr string

	for _, b := range budgets {
		if b.Schedule != "" {
			if !scheduleWindowActive(b.Schedule, b.Duration, now) {
				schedStr = fmt.Sprintf("%s/%s", b.Schedule, b.Duration)
				continue // inactive, skip headroom contribution
			}
		}
		h := poolHeadroom(b.Nodes, stats.Total, stats.Deleting, stats.NotReady)
		if h < minH {
			minH = h
		}
	}

	if minH == math.MaxInt32 {
		// Only inactive schedules
		return stats.Total, false, schedStr
	}
	if minH < 0 {
		minH = 0
	}
	return minH, minH == 0, ""
}

// formatPerReasonDisplay builds the BUDGET column string for per-reason budgets.
// The lead reason appears first; remaining reasons follow.
func formatPerReasonDisplay(results []reasonHeadroom, leadLabel string, total int) string {
	// Reorder: lead reason first
	ordered := make([]reasonHeadroom, 0, len(results))
	var rest []reasonHeadroom
	for _, r := range results {
		if r.label == leadLabel {
			ordered = append(ordered, r)
		} else {
			rest = append(rest, r)
		}
	}
	ordered = append(ordered, rest...)

	parts := make([]string, 0, len(ordered))
	for _, r := range ordered {
		if r.schedStr != "" {
			parts = append(parts, fmt.Sprintf("%s:%s [inactive]", r.label, r.schedStr))
			continue
		}
		if r.blocked {
			parts = append(parts, fmt.Sprintf("%s:[BLOCKED]", r.label))
		} else {
			parts = append(parts, fmt.Sprintf("%s:%d/%d", r.label, r.headroom, total))
		}
	}
	return strings.Join(parts, " ")
}
