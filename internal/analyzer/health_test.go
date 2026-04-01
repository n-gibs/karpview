package analyzer

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
	_ "k8s.io/apimachinery/pkg/apis/meta/v1" // used in future tasks
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func TestBuildNodeClaimMap(t *testing.T) {
	nc1 := unstructured.Unstructured{Object: map[string]any{
		"status": map[string]any{"nodeName": "node-a"},
	}}
	nc2 := unstructured.Unstructured{Object: map[string]any{
		"status": map[string]any{"nodeName": "node-b"},
	}}
	ncNoNode := unstructured.Unstructured{Object: map[string]any{
		"status": map[string]any{},
	}}

	m := buildNodeClaimMap([]unstructured.Unstructured{nc1, nc2, ncNoNode})

	if len(m) != 2 {
		t.Fatalf("want 2 entries, got %d", len(m))
	}
	if m["node-a"] == nil {
		t.Error("want entry for node-a, got nil")
	}
	if m["node-b"] == nil {
		t.Error("want entry for node-b, got nil")
	}
	if m["node-c"] != nil {
		t.Error("want nil for unknown node, got non-nil")
	}
}

func TestCheckNodeHealth_Healthy(t *testing.T) {
	n := &corev1.Node{
		Status: corev1.NodeStatus{
			Conditions: []corev1.NodeCondition{
				{Type: corev1.NodeReady, Status: corev1.ConditionTrue},
				{Type: corev1.NodeMemoryPressure, Status: corev1.ConditionFalse},
				{Type: corev1.NodeDiskPressure, Status: corev1.ConditionFalse},
				{Type: corev1.NodePIDPressure, Status: corev1.ConditionFalse},
			},
		},
	}
	if got := checkNodeHealth(n); got != nil {
		t.Errorf("want nil for healthy node, got %v", got)
	}
}

func TestCheckNodeHealth_Ready(t *testing.T) {
	n := &corev1.Node{
		Status: corev1.NodeStatus{
			Conditions: []corev1.NodeCondition{
				{Type: corev1.NodeReady, Status: corev1.ConditionFalse},
			},
		},
	}
	issues := checkNodeHealth(n)
	if len(issues) != 1 || issues[0] != "Ready" {
		t.Errorf("want [Ready], got %v", issues)
	}
}

func TestCheckNodeHealth_ReadyUnknown(t *testing.T) {
	n := &corev1.Node{
		Status: corev1.NodeStatus{
			Conditions: []corev1.NodeCondition{
				{Type: corev1.NodeReady, Status: corev1.ConditionUnknown},
			},
		},
	}
	issues := checkNodeHealth(n)
	if len(issues) != 1 || issues[0] != "Ready" {
		t.Errorf("want [Ready] for Unknown, got %v", issues)
	}
}

func TestCheckNodeHealth_MultipleConditions(t *testing.T) {
	n := &corev1.Node{
		Status: corev1.NodeStatus{
			Conditions: []corev1.NodeCondition{
				{Type: corev1.NodeMemoryPressure, Status: corev1.ConditionTrue},
				{Type: corev1.NodeDiskPressure, Status: corev1.ConditionTrue},
				{Type: corev1.NodePIDPressure, Status: corev1.ConditionFalse},
			},
		},
	}
	issues := checkNodeHealth(n)
	if len(issues) != 2 {
		t.Fatalf("want 2 issues, got %v", issues)
	}
	if issues[0] != "MemoryPressure" || issues[1] != "DiskPressure" {
		t.Errorf("want [MemoryPressure DiskPressure], got %v", issues)
	}
}

func TestCheckNodeHealth_NoConditions(t *testing.T) {
	n := &corev1.Node{}
	if got := checkNodeHealth(n); got != nil {
		t.Errorf("want nil for node with no conditions, got %v", got)
	}
}
