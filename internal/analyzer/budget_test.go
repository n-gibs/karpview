package analyzer

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/nikgibson/karpview/internal/cluster"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
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

var fixedNow = time.Date(2026, 3, 27, 12, 0, 0, 0, time.UTC) // noon UTC, no schedules active

func TestEvaluateBudgets_Default(t *testing.T) {
	// No budgets → Karpenter default 10%
	stats := PoolStats{Total: 10, Deleting: 0, NotReady: 0}
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
	stats := PoolStats{Total: 10, Deleting: 0, NotReady: 0}
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
	stats := PoolStats{Total: 10, Deleting: 2, NotReady: 0}
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
	stats := PoolStats{Total: 10}
	_, blocked, display := evaluateBudgets(budgets, "Empty", "", stats, fixedNow)
	if !blocked {
		t.Error("nodes=0 should always be blocked")
	}
	if display != "0 [BLOCKED]" {
		t.Errorf("display: got %q", display)
	}
}

func TestEvaluateBudgets_PerReason_LeadFirst(t *testing.T) {
	// U budget: 3 nodes, 3 deleting → 3-3=0, blocked
	// E budget: 50%, 10 total, 3 deleting → ceil(5)-3=2, avail
	budgets := []DisruptionBudget{
		{Nodes: "3", Reasons: []string{"Underutilized"}},
		{Nodes: "50%", Reasons: []string{"Empty"}},
	}
	stats := PoolStats{Total: 10, Deleting: 3}
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
	stats := PoolStats{Total: 10}
	_, _, display := evaluateBudgets(budgets, "Underutilized", "WhenEmpty", stats, fixedNow)
	if strings.Contains(display, "U:") {
		t.Errorf("WhenEmpty policy should omit U:, got %q", display)
	}
}

func TestEvaluateBudgets_ScheduleInactive(t *testing.T) {
	// noon UTC — @daily window (00:00–00:10) is inactive
	budgets := []DisruptionBudget{{Nodes: "0", Schedule: "@daily", Duration: "10m"}}
	stats := PoolStats{Total: 10}
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
	stats := PoolStats{Total: 10}
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
	stats := PoolStats{Total: 10, Deleting: 2}
	h, _, _ := evaluateBudgets(budgets, "Empty", "", stats, fixedNow)
	// min(5-2, 2-2) = min(3, 0) = 0
	if h != 0 {
		t.Errorf("headroom: want 0 got %d", h)
	}
}

// makeNodes creates n nodes labeled with the given pool name.
// The first deleting nodes have DeletionTimestamp set.
func makeNodes(pool string, n, deleting int) []corev1.Node {
	nodes := make([]corev1.Node, n)
	for i := range nodes {
		nodes[i] = corev1.Node{
			ObjectMeta: metav1.ObjectMeta{
				Name:   fmt.Sprintf("%s-node-%d", pool, i),
				Labels: map[string]string{"karpenter.sh/nodepool": pool},
			},
		}
	}
	now := metav1.Now()
	for i := 0; i < deleting && i < n; i++ {
		nodes[i].DeletionTimestamp = &now
	}
	return nodes
}

func TestAnalyzeBudgets_NilData(t *testing.T) {
	if got := AnalyzeBudgets(nil); got != nil {
		t.Errorf("want nil, got %v", got)
	}
}

func TestAnalyzeBudgets_SinglePool_AlwaysActive(t *testing.T) {
	// 20% of 10 = 2 allowed; 2 deleting → headroom = 2-2 = 0 → blocked
	np := unstructured.Unstructured{Object: map[string]any{
		"metadata": map[string]any{"name": "default"},
		"spec": map[string]any{
			"disruption": map[string]any{
				"consolidationPolicy": "WhenEmptyOrUnderutilized",
				"budgets":             []any{map[string]any{"nodes": "20%"}},
			},
		},
	}}
	data := &cluster.ClusterData{
		NodePools: []unstructured.Unstructured{np},
		Nodes:     makeNodes("default", 10, 2),
	}
	summaries := analyzeBudgets(data, fixedNow)
	if len(summaries) != 1 {
		t.Fatalf("want 1 summary, got %d", len(summaries))
	}
	r := summaries[0].Rules[0]
	if !r.WindowActive {
		t.Error("want WindowActive=true for no-schedule budget")
	}
	if r.Headroom != 0 {
		t.Errorf("want Headroom=0, got %d", r.Headroom)
	}
	if !r.Blocked {
		t.Error("want Blocked=true when headroom=0")
	}
}

func TestAnalyzeBudgets_SinglePool_ScheduleInactive(t *testing.T) {
	// fixedNow = noon UTC; @daily/10m window closes at 00:10 → inactive
	np := unstructured.Unstructured{Object: map[string]any{
		"metadata": map[string]any{"name": "default"},
		"spec": map[string]any{
			"disruption": map[string]any{
				"budgets": []any{map[string]any{
					"nodes":    "0",
					"schedule": "@daily",
					"duration": "10m",
				}},
			},
		},
	}}
	data := &cluster.ClusterData{
		NodePools: []unstructured.Unstructured{np},
		Nodes:     makeNodes("default", 5, 0),
	}
	summaries := analyzeBudgets(data, fixedNow)
	if len(summaries) != 1 {
		t.Fatalf("want 1 summary, got %d", len(summaries))
	}
	r := summaries[0].Rules[0]
	if r.WindowActive {
		t.Error("want WindowActive=false at noon UTC for @daily/10m")
	}
	if r.Blocked {
		t.Error("want Blocked=false when window inactive")
	}
}

func TestAnalyzeBudgets_MultiplePools_SortedByName(t *testing.T) {
	npZ := unstructured.Unstructured{Object: map[string]any{
		"metadata": map[string]any{"name": "z-pool"},
		"spec": map[string]any{"disruption": map[string]any{
			"budgets": []any{map[string]any{"nodes": "10%"}},
		}},
	}}
	npA := unstructured.Unstructured{Object: map[string]any{
		"metadata": map[string]any{"name": "a-pool"},
		"spec": map[string]any{"disruption": map[string]any{
			"budgets": []any{map[string]any{"nodes": "10%"}},
		}},
	}}
	data := &cluster.ClusterData{
		NodePools: []unstructured.Unstructured{npZ, npA},
	}
	summaries := analyzeBudgets(data, fixedNow)
	if len(summaries) != 2 {
		t.Fatalf("want 2 summaries, got %d", len(summaries))
	}
	if summaries[0].PoolName != "a-pool" || summaries[1].PoolName != "z-pool" {
		t.Errorf("want sorted [a-pool, z-pool], got [%s, %s]",
			summaries[0].PoolName, summaries[1].PoolName)
	}
}

func TestAnalyzeBudgets_DefaultBudget(t *testing.T) {
	// Empty budgets slice → Karpenter default: nodes=10%, reasons=nil, windowActive=true
	np := unstructured.Unstructured{Object: map[string]any{
		"metadata": map[string]any{"name": "default"},
		"spec": map[string]any{
			"disruption": map[string]any{
				"consolidationPolicy": "WhenEmptyOrUnderutilized",
				// no budgets field
			},
		},
	}}
	data := &cluster.ClusterData{
		NodePools: []unstructured.Unstructured{np},
	}
	summaries := analyzeBudgets(data, fixedNow)
	if len(summaries) != 1 {
		t.Fatalf("want 1 summary, got %d", len(summaries))
	}
	if len(summaries[0].Rules) != 1 {
		t.Fatalf("want 1 rule, got %d", len(summaries[0].Rules))
	}
	r := summaries[0].Rules[0]
	if r.Nodes != "10%" {
		t.Errorf("want nodes=10%%, got %q", r.Nodes)
	}
	if r.Reasons != nil {
		t.Errorf("want reasons=nil, got %v", r.Reasons)
	}
	if !r.WindowActive {
		t.Error("want WindowActive=true for default budget")
	}
}
