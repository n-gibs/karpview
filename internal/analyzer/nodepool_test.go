package analyzer

import (
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// nodePool builds a minimal NodePool unstructured object for tests.
func nodePool(name, policy, consolidateAfter string) unstructured.Unstructured {
	u := unstructured.Unstructured{}
	u.SetName(name)
	disruption := map[string]any{}
	if policy != "" {
		disruption["consolidationPolicy"] = policy
	}
	if consolidateAfter != "" {
		disruption["consolidateAfter"] = consolidateAfter
	}
	u.Object["spec"] = map[string]any{
		"disruption": disruption,
	}
	return u
}

func TestBuildNodePoolInfoMap_WhenEmptyPolicy(t *testing.T) {
	nps := []unstructured.Unstructured{nodePool("default", "WhenEmpty", "")}
	m := buildNodePoolInfoMap(nps)
	got, ok := m["default"]
	if !ok {
		t.Fatal("expected 'default' in map")
	}
	if got.ConsolidationPolicy != "WhenEmpty" {
		t.Errorf("expected WhenEmpty, got %q", got.ConsolidationPolicy)
	}
	if got.ConsolidateAfter != "" {
		t.Errorf("expected empty ConsolidateAfter, got %q", got.ConsolidateAfter)
	}
}

func TestBuildNodePoolInfoMap_WhenEmptyOrUnderutilizedWithTimer(t *testing.T) {
	nps := []unstructured.Unstructured{nodePool("spot", "WhenEmptyOrUnderutilized", "30s")}
	m := buildNodePoolInfoMap(nps)
	got := m["spot"]
	if got.ConsolidationPolicy != "WhenEmptyOrUnderutilized" {
		t.Errorf("expected WhenEmptyOrUnderutilized, got %q", got.ConsolidationPolicy)
	}
	if got.ConsolidateAfter != "30s" {
		t.Errorf("expected 30s, got %q", got.ConsolidateAfter)
	}
}

func TestBuildNodePoolInfoMap_ConsolidateAfterNever(t *testing.T) {
	nps := []unstructured.Unstructured{nodePool("default", "WhenEmpty", "Never")}
	m := buildNodePoolInfoMap(nps)
	if m["default"].ConsolidateAfter != "Never" {
		t.Errorf("expected Never, got %q", m["default"].ConsolidateAfter)
	}
}

func TestBuildNodePoolInfoMap_NilInput(t *testing.T) {
	m := buildNodePoolInfoMap(nil)
	if len(m) != 0 {
		t.Errorf("expected empty map, got %v", m)
	}
}

func TestBuildNodePoolInfoMap_MissingDisruptionSpec(t *testing.T) {
	u := unstructured.Unstructured{}
	u.SetName("default")
	// no spec at all — should return a zero-value NodePoolInfo, not panic
	m := buildNodePoolInfoMap([]unstructured.Unstructured{u})
	got, ok := m["default"]
	if !ok {
		t.Fatal("expected 'default' in map even with missing spec")
	}
	if got.ConsolidationPolicy != "" || got.ConsolidateAfter != "" {
		t.Errorf("expected zero-value NodePoolInfo, got %+v", got)
	}
}

func TestBuildNodePoolInfoMap_SkipsUnnamedNodePools(t *testing.T) {
	u := unstructured.Unstructured{}
	// intentionally no name
	m := buildNodePoolInfoMap([]unstructured.Unstructured{u})
	if len(m) != 0 {
		t.Errorf("expected empty map for unnamed NodePool, got %v", m)
	}
}

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
