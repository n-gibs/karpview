# Disruption Budgets (C6) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Prerequisite:** The C4/C5 plan (`docs/superpowers/plans/2026-03-27-nodepool-signals-c4-c5.md`) must be fully executed before starting this plan. C6 builds on `NodePoolInfo`, `buildNodePoolInfoMap`, `NodeResult.NodePoolPolicy`, and the POLICY column added in C4/C5.

**Goal:** Add a BUDGET column to KarpView output showing each node's NodePool disruption budget headroom per disruption reason, with `[BLOCKED]` when the pool is rate-limited and schedule window state for time-gated budgets.

**Architecture:** `nodepool.go` gains a `DisruptionBudget` struct and `NodePoolInfo.Budgets` field parsed from `spec.disruption.budgets`. A new pure `budget.go` provides `evaluateBudgets()` — which computes per-reason headroom using pool node counts derived from existing `data.Nodes` — and returns a pre-formatted display string. `Analyze()` in `blocker.go` builds a `poolStats` map from `data.Nodes`, calls `evaluateBudgets()` per node, and stamps `BudgetDisplay`/`BudgetBlocked` onto `NodeResult`. The printer gains a BUDGET column writing `r.BudgetDisplay` directly.

**Tech Stack:** Go 1.25+, `github.com/robfig/cron/v3` for schedule window evaluation, existing `k8s.io/apimachinery/pkg/apis/meta/v1/unstructured`.

---

## File Map

| File | Change |
|------|--------|
| `go.mod` / `go.sum` | Add `github.com/robfig/cron/v3` |
| `internal/analyzer/nodepool.go` | Add `DisruptionBudget` struct; extend `NodePoolInfo.Budgets`; extend `buildNodePoolInfoMap` to parse `spec.disruption.budgets` |
| `internal/analyzer/nodepool_test.go` | Add 4 budget-parsing tests |
| `internal/analyzer/budget.go` | New — `poolStats`, `poolHeadroom`, `budgetAppliesToReason`, `scheduleWindowActive`, `evaluateBudgets`, `formatBudgetDisplay` |
| `internal/analyzer/budget_test.go` | New — 12 unit tests |
| `internal/analyzer/blocker.go` | Add `BudgetDisplay string`, `BudgetBlocked bool` to `NodeResult`; build `poolStats` map in `Analyze()`; call `evaluateBudgets()` per node |
| `internal/analyzer/blocker_test.go` | Add 3 integration tests |
| `internal/printer/printer.go` | Add BUDGET column — max-width calc, column in row format |
| `internal/printer/printer_test.go` | Add 1 integration test |

---

## Task 1: Add robfig/cron/v3 dependency

**Files:**
- Modify: `go.mod`, `go.sum`

- [ ] **Step 1: Add the dependency**

```bash
cd /Users/nikgibson/karpview
go get github.com/robfig/cron/v3@v3.0.1
go mod tidy
```

Expected: `go.mod` now contains `github.com/robfig/cron/v3 v3.0.1` in the `require` block.

- [ ] **Step 2: Verify it compiles**

```bash
go build ./...
```

Expected: no output (clean build).

- [ ] **Step 3: Commit**

```bash
git add go.mod go.sum
git commit -m "chore: add robfig/cron/v3 for disruption budget schedule evaluation"
```

---

## Task 2: Add DisruptionBudget to NodePoolInfo and parse from NodePool spec

**Files:**
- Modify: `internal/analyzer/nodepool.go`
- Modify: `internal/analyzer/nodepool_test.go`

- [ ] **Step 1: Write failing tests for budget parsing**

Open `internal/analyzer/nodepool_test.go` and add after the existing tests:

```go
func TestBuildNodePoolInfoMap_Budgets_SimplePercentage(t *testing.T) {
	np := unstructured.Unstructured{Object: map[string]any{
		"metadata": map[string]any{"name": "default"},
		"spec": map[string]any{
			"disruption": map[string]any{
				"consolidationPolicy": "WhenEmptyOrUnderutilized",
				"budgets": []any{
					map[string]any{"nodes": "20%"},
				},
			},
		},
	}}
	m := buildNodePoolInfoMap([]unstructured.Unstructured{np})
	info, ok := m["default"]
	if !ok {
		t.Fatal("expected pool 'default' in map")
	}
	if len(info.Budgets) != 1 {
		t.Fatalf("expected 1 budget, got %d", len(info.Budgets))
	}
	if info.Budgets[0].Nodes != "20%" {
		t.Errorf("expected nodes=20%%, got %q", info.Budgets[0].Nodes)
	}
}

func TestBuildNodePoolInfoMap_Budgets_WithReasonsAndSchedule(t *testing.T) {
	np := unstructured.Unstructured{Object: map[string]any{
		"metadata": map[string]any{"name": "batch"},
		"spec": map[string]any{
			"disruption": map[string]any{
				"budgets": []any{
					map[string]any{
						"nodes":    "0",
						"reasons":  []any{"Underutilized"},
						"schedule": "@daily",
						"duration": "10m",
					},
				},
			},
		},
	}}
	m := buildNodePoolInfoMap([]unstructured.Unstructured{np})
	b := m["batch"].Budgets[0]
	if b.Nodes != "0" {
		t.Errorf("nodes: want 0 got %q", b.Nodes)
	}
	if len(b.Reasons) != 1 || b.Reasons[0] != "Underutilized" {
		t.Errorf("reasons: want [Underutilized] got %v", b.Reasons)
	}
	if b.Schedule != "@daily" {
		t.Errorf("schedule: want @daily got %q", b.Schedule)
	}
	if b.Duration != "10m" {
		t.Errorf("duration: want 10m got %q", b.Duration)
	}
}

func TestBuildNodePoolInfoMap_Budgets_NoBudgets(t *testing.T) {
	np := unstructured.Unstructured{Object: map[string]any{
		"metadata": map[string]any{"name": "default"},
		"spec": map[string]any{
			"disruption": map[string]any{
				"consolidationPolicy": "WhenEmpty",
			},
		},
	}}
	m := buildNodePoolInfoMap([]unstructured.Unstructured{np})
	if len(m["default"].Budgets) != 0 {
		t.Errorf("expected 0 budgets, got %d", len(m["default"].Budgets))
	}
}

func TestBuildNodePoolInfoMap_Budgets_MultipleBudgets(t *testing.T) {
	np := unstructured.Unstructured{Object: map[string]any{
		"metadata": map[string]any{"name": "default"},
		"spec": map[string]any{
			"disruption": map[string]any{
				"budgets": []any{
					map[string]any{"nodes": "20%", "reasons": []any{"Empty", "Drifted"}},
					map[string]any{"nodes": "5"},
				},
			},
		},
	}}
	m := buildNodePoolInfoMap([]unstructured.Unstructured{np})
	if len(m["default"].Budgets) != 2 {
		t.Fatalf("expected 2 budgets, got %d", len(m["default"].Budgets))
	}
	if len(m["default"].Budgets[0].Reasons) != 2 {
		t.Errorf("expected 2 reasons on first budget, got %v", m["default"].Budgets[0].Reasons)
	}
	if m["default"].Budgets[1].Nodes != "5" {
		t.Errorf("second budget nodes: want 5 got %q", m["default"].Budgets[1].Nodes)
	}
}
```

- [ ] **Step 2: Run tests to confirm they fail**

```bash
go test ./internal/analyzer/ -run TestBuildNodePoolInfoMap_Budgets -v
```

Expected: FAIL — `DisruptionBudget` undefined / `Budgets` field not on `NodePoolInfo`.

- [ ] **Step 3: Add DisruptionBudget struct and Budgets field**

Open `internal/analyzer/nodepool.go`. Add `DisruptionBudget` struct and extend `NodePoolInfo`:

```go
// DisruptionBudget represents one entry from spec.disruption.budgets on a NodePool.
type DisruptionBudget struct {
	Nodes    string   // "20%" or "5" or "0"
	Reasons  []string // nil means applies to all reasons
	Schedule string   // "" | "@daily" | "0 9 * * 1-5"
	Duration string   // "" | "10m" | "1h30m"
}

type NodePoolInfo struct {
	ConsolidationPolicy string
	ConsolidateAfter    string
	Budgets             []DisruptionBudget // NEW
}
```

- [ ] **Step 4: Extend buildNodePoolInfoMap to parse budgets**

In `buildNodePoolInfoMap`, after extracting `ConsolidationPolicy` and `ConsolidateAfter`, add budget parsing. The full updated function body (replace the existing implementation, keeping the function signature unchanged):

```go
func buildNodePoolInfoMap(nodePools []unstructured.Unstructured) map[string]NodePoolInfo {
	m := make(map[string]NodePoolInfo, len(nodePools))
	for i := range nodePools {
		np := &nodePools[i]
		name := np.GetName()
		if name == "" {
			continue
		}
		var info NodePoolInfo
		spec, _ := np.Object["spec"].(map[string]any)
		if spec == nil {
			m[name] = info
			continue
		}
		disruption, _ := spec["disruption"].(map[string]any)
		if disruption == nil {
			m[name] = info
			continue
		}
		info.ConsolidationPolicy, _ = disruption["consolidationPolicy"].(string)
		info.ConsolidateAfter, _ = disruption["consolidateAfter"].(string)

		if rawBudgets, ok := disruption["budgets"].([]any); ok {
			for _, rb := range rawBudgets {
				bMap, ok := rb.(map[string]any)
				if !ok {
					continue
				}
				var b DisruptionBudget
				b.Nodes, _ = bMap["nodes"].(string)
				b.Schedule, _ = bMap["schedule"].(string)
				b.Duration, _ = bMap["duration"].(string)
				if rawReasons, ok := bMap["reasons"].([]any); ok {
					for _, rr := range rawReasons {
						if s, ok := rr.(string); ok {
							b.Reasons = append(b.Reasons, s)
						}
					}
				}
				info.Budgets = append(info.Budgets, b)
			}
		}
		m[name] = info
	}
	return m
}
```

- [ ] **Step 5: Run tests to confirm they pass**

```bash
go test ./internal/analyzer/ -run TestBuildNodePoolInfoMap_Budgets -v
```

Expected: all 4 PASS.

- [ ] **Step 6: Run full test suite to confirm no regressions**

```bash
go test ./...
```

Expected: all existing tests PASS.

- [ ] **Step 7: Commit**

```bash
git add internal/analyzer/nodepool.go internal/analyzer/nodepool_test.go
git commit -m "feat(c6): add DisruptionBudget to NodePoolInfo and parse from NodePool spec"
```

---

## Task 3: Create budget.go — helpers and always-active evaluation

**Files:**
- Create: `internal/analyzer/budget.go`
- Create: `internal/analyzer/budget_test.go` (partial — helpers only)

- [ ] **Step 1: Write failing tests for poolHeadroom and budgetAppliesToReason**

Create `internal/analyzer/budget_test.go`:

```go
package analyzer

import (
	"strings"
	"testing"
	"time"
)

func TestPoolHeadroom_Percentage(t *testing.T) {
	// roundup(10 * 0.20) = 2; 2 - 0 - 0 = 2
	if got := poolHeadroom("20%", 10, 0, 0); got != 2 {
		t.Errorf("want 2 got %d", got)
	}
}

func TestPoolHeadroom_Percentage_Ceil(t *testing.T) {
	// roundup(19 * 0.20) = roundup(3.8) = 4; 4 - 0 - 0 = 4
	if got := poolHeadroom("20%", 19, 0, 0); got != 4 {
		t.Errorf("want 4 got %d", got)
	}
}

func TestPoolHeadroom_Static(t *testing.T) {
	if got := poolHeadroom("5", 20, 1, 0); got != 4 {
		t.Errorf("want 4 got %d", got)
	}
}

func TestPoolHeadroom_Zero(t *testing.T) {
	if got := poolHeadroom("0", 10, 0, 0); got != 0 {
		t.Errorf("want 0 got %d", got)
	}
}

func TestPoolHeadroom_ClampNegative(t *testing.T) {
	// 1 - 3 = -2, clamped to 0
	if got := poolHeadroom("1", 10, 3, 0); got != 0 {
		t.Errorf("want 0 got %d", got)
	}
}

func TestBudgetAppliesToReason_NilReasons(t *testing.T) {
	b := DisruptionBudget{Nodes: "20%"}
	if !budgetAppliesToReason(b, "Empty") {
		t.Error("nil reasons should apply to all")
	}
}

func TestBudgetAppliesToReason_Match(t *testing.T) {
	b := DisruptionBudget{Nodes: "5", Reasons: []string{"Empty", "Drifted"}}
	if !budgetAppliesToReason(b, "Empty") {
		t.Error("Empty should match")
	}
}

func TestBudgetAppliesToReason_NoMatch(t *testing.T) {
	b := DisruptionBudget{Nodes: "5", Reasons: []string{"Drifted"}}
	if budgetAppliesToReason(b, "Underutilized") {
		t.Error("Underutilized should not match Drifted-only budget")
	}
}

// scheduleWindowActive tests are in Task 4
// evaluateBudgets tests are in Task 5
```

- [ ] **Step 2: Run tests to confirm they fail**

```bash
go test ./internal/analyzer/ -run "TestPoolHeadroom|TestBudgetApplies" -v
```

Expected: FAIL — `poolHeadroom` and `budgetAppliesToReason` undefined.

- [ ] **Step 3: Create budget.go with helpers**

Create `internal/analyzer/budget.go`:

```go
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
```

- [ ] **Step 4: Run helper tests**

```bash
go test ./internal/analyzer/ -run "TestPoolHeadroom|TestBudgetApplies" -v
```

Expected: all PASS.

- [ ] **Step 5: Run full suite**

```bash
go test ./...
```

Expected: all PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/analyzer/budget.go internal/analyzer/budget_test.go
git commit -m "feat(c6): add budget helpers — poolHeadroom, budgetAppliesToReason, scheduleWindowActive"
```

---

## Task 4: Add schedule window tests

**Files:**
- Modify: `internal/analyzer/budget_test.go`

- [ ] **Step 1: Add schedule window tests**

Append to `internal/analyzer/budget_test.go`:

```go
func TestScheduleWindowActive_Daily_Inside(t *testing.T) {
	// @daily fires at 00:00 UTC. Window: 00:00–00:10 UTC.
	// now = 00:05 UTC → inside window.
	now := time.Date(2026, 3, 27, 0, 5, 0, 0, time.UTC)
	if !scheduleWindowActive("@daily", "10m", now) {
		t.Error("expected window active at 00:05 UTC")
	}
}

func TestScheduleWindowActive_Daily_Outside(t *testing.T) {
	// now = 01:00 UTC → outside window (fired at 00:00, window ended at 00:10).
	now := time.Date(2026, 3, 27, 1, 0, 0, 0, time.UTC)
	if scheduleWindowActive("@daily", "10m", now) {
		t.Error("expected window inactive at 01:00 UTC")
	}
}

func TestScheduleWindowActive_Weekly_Outside(t *testing.T) {
	// @weekly fires Sunday 00:00 UTC. Test on a Thursday.
	now := time.Date(2026, 3, 26, 12, 0, 0, 0, time.UTC) // Thursday
	if scheduleWindowActive("@weekly", "1h", now) {
		t.Error("expected window inactive on Thursday")
	}
}

func TestScheduleWindowActive_EmptySchedule(t *testing.T) {
	now := time.Now()
	if scheduleWindowActive("", "10m", now) {
		t.Error("empty schedule should return false")
	}
}
```

- [ ] **Step 2: Run schedule tests**

```bash
go test ./internal/analyzer/ -run TestScheduleWindowActive -v
```

Expected: all 4 PASS.

- [ ] **Step 3: Commit**

```bash
git add internal/analyzer/budget_test.go
git commit -m "test(c6): add schedule window evaluation tests"
```

---

## Task 5: Add evaluateBudgets and formatBudgetDisplay

**Files:**
- Modify: `internal/analyzer/budget.go`
- Modify: `internal/analyzer/budget_test.go`

- [ ] **Step 1: Write failing tests for evaluateBudgets**

Append to `internal/analyzer/budget_test.go`:

```go
var fixedNow = time.Date(2026, 3, 27, 12, 0, 0, 0, time.UTC) // noon UTC, no schedules active

func TestEvaluateBudgets_Default(t *testing.T) {
	// No budgets → Karpenter default 10%
	stats := poolStats{Total: 10, Deleting: 0, NotReady: 0}
	h, blocked, display := evaluateBudgets(nil, "Empty", "", stats, fixedNow)
	if h != 1 {
		t.Errorf("headroom: want 1 got %d", h)
	}
	if blocked {
		t.Error("should not be blocked")
	}
	if display != "default 10% (1/10 avail)" {
		t.Errorf("display: got %q", display)
	}
}

func TestEvaluateBudgets_SimplePercentage_NotBlocked(t *testing.T) {
	budgets := []DisruptionBudget{{Nodes: "20%"}}
	stats := poolStats{Total: 10, Deleting: 0, NotReady: 0}
	h, blocked, display := evaluateBudgets(budgets, "Empty", "", stats, fixedNow)
	if h != 2 {
		t.Errorf("headroom: want 2 got %d", h)
	}
	if blocked {
		t.Error("should not be blocked")
	}
	if display != "20% (2/10 avail)" {
		t.Errorf("display: got %q", display)
	}
}

func TestEvaluateBudgets_SimplePercentage_Blocked(t *testing.T) {
	budgets := []DisruptionBudget{{Nodes: "20%"}}
	stats := poolStats{Total: 10, Deleting: 2, NotReady: 0}
	h, blocked, display := evaluateBudgets(budgets, "Underutilized", "", stats, fixedNow)
	if h != 0 {
		t.Errorf("headroom: want 0 got %d", h)
	}
	if !blocked {
		t.Error("should be blocked")
	}
	if display != "20% [BLOCKED]" {
		t.Errorf("display: got %q", display)
	}
}

func TestEvaluateBudgets_NodesZero_Blocked(t *testing.T) {
	budgets := []DisruptionBudget{{Nodes: "0"}}
	stats := poolStats{Total: 10}
	_, blocked, display := evaluateBudgets(budgets, "Empty", "", stats, fixedNow)
	if !blocked {
		t.Error("nodes=0 should always be blocked")
	}
	if display != "0 [BLOCKED]" {
		t.Errorf("display: got %q", display)
	}
}

func TestEvaluateBudgets_PerReason_LeadFirst(t *testing.T) {
	// U budget: 5 nodes, 3 deleting → blocked
	// E budget: 20%, 10 total → 2 avail
	budgets := []DisruptionBudget{
		{Nodes: "5", Reasons: []string{"Underutilized"}},
		{Nodes: "20%", Reasons: []string{"Empty"}},
	}
	stats := poolStats{Total: 10, Deleting: 3}
	_, _, display := evaluateBudgets(budgets, "Underutilized", "", stats, fixedNow)
	if !strings.HasPrefix(display, "U:") {
		t.Errorf("lead reason should be U:, got %q", display)
	}
	if !strings.Contains(display, "U:[BLOCKED]") {
		t.Errorf("U should be blocked, got %q", display)
	}
	if !strings.Contains(display, "E:2/10") {
		t.Errorf("E headroom should be 2/10, got %q", display)
	}
}

func TestEvaluateBudgets_WhenEmptyPolicy_OmitsU(t *testing.T) {
	budgets := []DisruptionBudget{
		{Nodes: "5", Reasons: []string{"Underutilized"}},
		{Nodes: "20%", Reasons: []string{"Empty"}},
	}
	stats := poolStats{Total: 10}
	_, _, display := evaluateBudgets(budgets, "Underutilized", "WhenEmpty", stats, fixedNow)
	if strings.Contains(display, "U:") {
		t.Errorf("WhenEmpty policy should omit U:, got %q", display)
	}
}

func TestEvaluateBudgets_ScheduleInactive(t *testing.T) {
	// noon UTC — @daily window (00:00–00:10) is inactive
	budgets := []DisruptionBudget{{Nodes: "0", Schedule: "@daily", Duration: "10m"}}
	stats := poolStats{Total: 10}
	_, blocked, display := evaluateBudgets(budgets, "Empty", "", stats, fixedNow)
	if blocked {
		t.Error("inactive schedule should not block")
	}
	if display != "@daily/10m [inactive]" {
		t.Errorf("display: got %q", display)
	}
}

func TestEvaluateBudgets_ScheduleActive_Blocked(t *testing.T) {
	// 00:05 UTC — @daily/10m window is active; nodes=0 → blocked
	now := time.Date(2026, 3, 27, 0, 5, 0, 0, time.UTC)
	budgets := []DisruptionBudget{{Nodes: "0", Schedule: "@daily", Duration: "10m"}}
	stats := poolStats{Total: 10}
	_, blocked, display := evaluateBudgets(budgets, "Empty", "", stats, now)
	if !blocked {
		t.Error("active window with nodes=0 should block")
	}
	if display != "@daily/10m [BLOCKED]" {
		t.Errorf("display: got %q", display)
	}
}

func TestEvaluateBudgets_MultipleUniform_MostRestrictiveWins(t *testing.T) {
	// Two budgets with no reasons: 5 nodes and 2 nodes. Most restrictive = 2.
	budgets := []DisruptionBudget{
		{Nodes: "5"},
		{Nodes: "2"},
	}
	stats := poolStats{Total: 10, Deleting: 2}
	h, _, _ := evaluateBudgets(budgets, "Empty", "", stats, fixedNow)
	// min(5-2, 2-2) = min(3, 0) = 0
	if h != 0 {
		t.Errorf("headroom: want 0 got %d", h)
	}
}
```

- [ ] **Step 2: Run tests to confirm they fail**

```bash
go test ./internal/analyzer/ -run TestEvaluateBudgets -v
```

Expected: FAIL — `evaluateBudgets` undefined.

- [ ] **Step 3: Add evaluateBudgets and formatBudgetDisplay to budget.go**

Append to `internal/analyzer/budget.go`:

```go
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
			return 0, false, schedDisplay + " [inactive]"
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
```

- [ ] **Step 4: Run evaluateBudgets tests**

```bash
go test ./internal/analyzer/ -run TestEvaluateBudgets -v
```

Expected: all PASS.

- [ ] **Step 5: Run full suite**

```bash
go test ./...
```

Expected: all PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/analyzer/budget.go internal/analyzer/budget_test.go
git commit -m "feat(c6): add evaluateBudgets and formatBudgetDisplay"
```

---

## Task 6: Wire budget evaluation into Analyze()

**Files:**
- Modify: `internal/analyzer/blocker.go`
- Modify: `internal/analyzer/blocker_test.go`

- [ ] **Step 1: Write failing integration tests**

Append to `internal/analyzer/blocker_test.go`:

```go
// makeNodeClaim builds a NodeClaim unstructured for tests (distinct from the existing
// nodeClaim helper which takes nodeName first; this takes nodePool first).
func makeNodeClaim(nodePool, nodeName string) unstructured.Unstructured {
	return unstructured.Unstructured{Object: map[string]any{
		"metadata": map[string]any{
			"name":   nodeName + "-claim",
			"labels": map[string]any{"karpenter.sh/nodepool": nodePool},
		},
		"status": map[string]any{"nodeName": nodeName},
	}}
}

// makeNodePool builds a NodePool unstructured with budgets for tests (distinct from the
// existing nodePool helper in nodepool_test.go which takes consolidateAfter, not budgets).
func makeNodePool(name, policy string, budgets []map[string]any) unstructured.Unstructured {
	budgetSlice := make([]any, len(budgets))
	for i, b := range budgets {
		budgetSlice[i] = b
	}
	return unstructured.Unstructured{Object: map[string]any{
		"metadata": map[string]any{"name": name},
		"spec": map[string]any{
			"disruption": map[string]any{
				"consolidationPolicy": policy,
				"budgets":             budgetSlice,
			},
		},
	}}
}

func TestAnalyze_BudgetPopulated(t *testing.T) {
	n := node("node-1", nil, map[string]string{"karpenter.sh/nodepool": "default"})
	nc := makeNodeClaim("default", "node-1")
	np := makeNodePool("default", "WhenEmptyOrUnderutilized", []map[string]any{
		{"nodes": "20%"},
	})
	data := &cluster.ClusterData{
		Nodes:      []corev1.Node{n},
		NodeClaims: []unstructured.Unstructured{nc},
		NodePools:  []unstructured.Unstructured{np},
	}
	results := Analyze(data)
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	r := results[0]
	if r.BudgetDisplay == "" {
		t.Error("BudgetDisplay should be set")
	}
	if r.BudgetDisplay == "—" {
		t.Error("BudgetDisplay should not be — for a pool with a budget")
	}
}

func TestAnalyze_BudgetUnknownPool(t *testing.T) {
	// Node with no NodePool resolution → budget display = "—"
	n := node("node-orphan", nil, nil)
	data := &cluster.ClusterData{
		Nodes: []corev1.Node{n},
	}
	results := Analyze(data)
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if results[0].BudgetDisplay != "—" {
		t.Errorf("expected — for unknown pool, got %q", results[0].BudgetDisplay)
	}
}

func TestAnalyze_BudgetPolicyWhenEmpty_OmitsU(t *testing.T) {
	// Pool with WhenEmpty policy and a U-only budget — normal node should not show U:
	n := node("node-1", nil, map[string]string{"karpenter.sh/nodepool": "default"})
	nc := makeNodeClaim("default", "node-1")
	np := makeNodePool("default", "WhenEmpty", []map[string]any{
		{"nodes": "5", "reasons": []any{"Underutilized"}},
		{"nodes": "20%", "reasons": []any{"Empty"}},
	})
	data := &cluster.ClusterData{
		Nodes:      []corev1.Node{n},
		NodeClaims: []unstructured.Unstructured{nc},
		NodePools:  []unstructured.Unstructured{np},
	}
	results := Analyze(data)
	if strings.Contains(results[0].BudgetDisplay, "U:") {
		t.Errorf("WhenEmpty pool should omit U:, got %q", results[0].BudgetDisplay)
	}
}
```

- [ ] **Step 2: Run tests to confirm they fail**

```bash
go test ./internal/analyzer/ -run TestAnalyze_Budget -v
```

Expected: FAIL — `BudgetDisplay` and `BudgetBlocked` undefined on `NodeResult` (C4/C5 already added `NodePools` to `ClusterData`).

- [ ] **Step 3: Add BudgetDisplay and BudgetBlocked to NodeResult**

In `internal/analyzer/blocker.go`, extend `NodeResult`:

```go
type NodeResult struct {
	NodeName            string
	NodePool            string
	Status              NodeStatus
	Blockers            []BlockReason
	ConsolidationClass  string
	CPURequestFraction  float64
	MemRequestFraction  float64
	NodePoolPolicy      string // set in C4/C5
	ConsolidateAfter    string // set in C4/C5
	BudgetDisplay       string // pre-formatted BUDGET column value
	BudgetBlocked       bool   // true if effective headroom <= 0
}
```

- [ ] **Step 4: Add poolStats computation and evaluateBudgets call to Analyze()**

In `internal/analyzer/blocker.go`, update `Analyze()` to build poolStats and call evaluateBudgets. The full updated `Analyze()` function:

```go
func Analyze(data *cluster.ClusterData) []NodeResult {
	if data == nil {
		return nil
	}

	nodePoolMap := buildNodePoolMap(data.NodeClaims)
	nodePoolInfos := buildNodePoolInfoMap(data.NodePools)
	podsByNode := indexPodsByNode(data.Pods)
	pdbEntries := compilePDBSelectors(data.PDBs)
	statsMap := buildPoolStats(data.Nodes, nodePoolMap)

	results := make([]NodeResult, 0, len(data.Nodes))
	for i := range data.Nodes {
		node := &data.Nodes[i]
		c := classifyNode(node, podsByNode[node.Name])
		poolName := resolveNodePool(node, nodePoolMap)
		npInfo := nodePoolInfos[poolName]
		stats := statsMap[poolName]
		reason := consolidationClassToReason(c.Class)

		var budgetBlocked bool
		var budgetDisplay string
		if _, inMap := nodePoolInfos[poolName]; !inMap {
			budgetDisplay = "—"
		} else {
			var budgetHeadroom int
			budgetHeadroom, budgetBlocked, budgetDisplay = evaluateBudgets(
				npInfo.Budgets, reason, npInfo.ConsolidationPolicy, stats, time.Now(),
			)
			_ = budgetHeadroom // headroom exposed via BudgetBlocked; reserved for future use
		}

		if isDraining(node) {
			results = append(results, NodeResult{
				NodeName:            node.Name,
				NodePool:            poolName,
				Status:              StatusDraining,
				ConsolidationClass:  c.Class,
				CPURequestFraction:  c.CPURequestFraction,
				MemRequestFraction:  c.MemRequestFraction,
				NodePoolPolicy:      npInfo.ConsolidationPolicy,
				ConsolidateAfter:    npInfo.ConsolidateAfter,
				BudgetDisplay:       budgetDisplay,
				BudgetBlocked:       budgetBlocked,
			})
			continue
		}
		result := analyzeNode(node, nodePoolMap, podsByNode, pdbEntries)
		result.ConsolidationClass = c.Class
		result.CPURequestFraction = c.CPURequestFraction
		result.MemRequestFraction = c.MemRequestFraction
		result.NodePoolPolicy = npInfo.ConsolidationPolicy
		result.ConsolidateAfter = npInfo.ConsolidateAfter
		result.BudgetDisplay = budgetDisplay
		result.BudgetBlocked = budgetBlocked
		results = append(results, result)
	}
	return results
}
```

- [ ] **Step 5: Add buildPoolStats to blocker.go**

Add this function to `internal/analyzer/blocker.go`:

```go
// buildPoolStats computes node counts per NodePool from the existing Nodes slice.
// keyed by NodePool name. Nodes with pool "unknown" are counted under "unknown".
func buildPoolStats(nodes []corev1.Node, nodePoolMap map[string]string) map[string]poolStats {
	m := make(map[string]poolStats)
	for i := range nodes {
		node := &nodes[i]
		pool := resolveNodePool(node, nodePoolMap)
		s := m[pool]
		s.Total++
		if node.DeletionTimestamp != nil {
			s.Deleting++
		}
		for _, c := range node.Status.Conditions {
			if c.Type == corev1.NodeReady &&
				(c.Status == corev1.ConditionFalse || c.Status == corev1.ConditionUnknown) {
				s.NotReady++
				break
			}
		}
		m[pool] = s
	}
	return m
}
```

Also add `"time"` to the imports in `blocker.go` if not already present.

- [ ] **Step 6: Run integration tests**

```bash
go test ./internal/analyzer/ -run TestAnalyze_Budget -v
```

Expected: all 3 PASS.

- [ ] **Step 7: Run full suite**

```bash
go test ./...
```

Expected: all PASS.

- [ ] **Step 8: Commit**

```bash
git add internal/analyzer/blocker.go internal/analyzer/blocker_test.go
git commit -m "feat(c6): wire budget evaluation into Analyze() — BudgetDisplay, BudgetBlocked on NodeResult"
```

---

## Task 7: Add BUDGET column to printer

**Files:**
- Modify: `internal/printer/printer.go`
- Modify: `internal/printer/printer_test.go`

- [ ] **Step 1: Write failing printer test**

Open `internal/printer/printer_test.go` and add:

```go
func TestPrint_BudgetColumn(t *testing.T) {
	results := []analyzer.NodeResult{
		{
			NodeName:           "node-a",
			NodePool:           "default",
			Status:             analyzer.StatusConsolidatable,
			ConsolidationClass: analyzer.ConsolidationEmpty,
			NodePoolPolicy:     "WhenEmpty",
			BudgetDisplay:      "20% (2/10 avail)",
		},
		{
			NodeName:           "node-b",
			NodePool:           "batch",
			Status:             analyzer.StatusConsolidatable,
			ConsolidationClass: analyzer.ConsolidationNormal,
			NodePoolPolicy:     "WhenEmptyOrUnderutilized",
			BudgetDisplay:      "U:[BLOCKED] E:2/10",
			BudgetBlocked:      true,
		},
		{
			NodeName:           "node-c",
			NodePool:           "spot",
			Status:             analyzer.StatusConsolidatable,
			ConsolidationClass: analyzer.ConsolidationDaemonOnly,
			BudgetDisplay:      "@daily/10m [inactive]",
		},
		{
			NodeName:           "node-orphan",
			NodePool:           "unknown",
			Status:             analyzer.StatusConsolidatable,
			ConsolidationClass: analyzer.ConsolidationNormal,
			BudgetDisplay:      "—",
		},
	}

	var buf strings.Builder
	Print(&buf, "test-cluster", results)
	out := buf.String()

	for _, want := range []string{
		"20% (2/10 avail)",
		"U:[BLOCKED] E:2/10",
		"@daily/10m [inactive]",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q\ngot:\n%s", want, out)
		}
	}
}
```

- [ ] **Step 2: Run test to confirm it fails**

```bash
go test ./internal/printer/ -run TestPrint_BudgetColumn -v
```

Expected: FAIL — output doesn't contain budget strings (no BUDGET column yet).

- [ ] **Step 3: Add BUDGET column to printer**

In `internal/printer/printer.go`, update the column width calculation and the `fmt.Fprintf` row format.

In the width calculation loop (after `maxConsolidation`), add `maxBudget`:

```go
maxBudget := len("BUDGET") // minimum — header width
for _, r := range results {
    if n := len(r.BudgetDisplay); n > maxBudget {
        maxBudget = n
    }
}
```

Update the `fmt.Fprintf` row format to include the BUDGET column after POLICY (which was added in C4/C5). The full updated row format line:

```go
fmt.Fprintf(w, "%s  %-*s   NodePool: %-*s   %-*s   %-*s   %-*s   %s\n",
    status,
    maxName, sanitize(r.NodeName),
    maxPool, sanitize(r.NodePool),
    maxConsolidation, formatConsolidation(r),
    maxPolicy, formatPolicy(r),
    maxBudget, sanitize(r.BudgetDisplay),
    reason,
)
```

Note: `maxPolicy` is added in C4/C5. If the POLICY column isn't present yet (C4/C5 not executed), the format string will be shorter — complete the C4/C5 plan first.

- [ ] **Step 4: Run printer test**

```bash
go test ./internal/printer/ -run TestPrint_BudgetColumn -v
```

Expected: PASS.

- [ ] **Step 5: Run full suite**

```bash
go test ./...
```

Expected: all PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/printer/printer.go internal/printer/printer_test.go
git commit -m "feat(c6): add BUDGET column to printer"
```

---

## Task 8: Smoke test and kanban update

- [ ] **Step 1: Build the binary**

```bash
go build -o karpview ./...
```

Expected: binary produced, no errors.

- [ ] **Step 2: Run the full test suite one final time**

```bash
go test ./... -count=1
```

Expected: all PASS.

- [ ] **Step 3: Mark C6 done in kanban**

In `claude-vault/01 Projects/KarpView/kanban.md`, move the C6 card from Backlog to Done:

```markdown
- [x] **[C6][CLIENT+ANALYZER]** NodePool disruption budgets (`spec.disruption.budgets` — nodes %, schedule windows)
```

- [ ] **Step 4: Final commit**

```bash
git add claude-vault/01\ Projects/KarpView/kanban.md
git commit -m "chore: mark C6 complete in kanban"
```
