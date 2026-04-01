package analyzer

import (
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
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

// fixedHealthNow is noon UTC on 2026-04-01, injected into expiry tests.
var fixedHealthNow = time.Date(2026, 4, 1, 12, 0, 0, 0, time.UTC)

func makeExpiryNodeClaim(expireAfter string, createdAt time.Time) *unstructured.Unstructured {
	nc := &unstructured.Unstructured{Object: map[string]any{}}
	if expireAfter != "" {
		nc.Object["spec"] = map[string]any{"expireAfter": expireAfter}
	}
	nc.SetCreationTimestamp(metav1.NewTime(createdAt))
	return nc
}

func TestCheckNodeExpiry_NilClaim(t *testing.T) {
	if got := checkNodeExpiry(nil, fixedHealthNow); got != "" {
		t.Errorf("want '' for nil claim, got %q", got)
	}
}

func TestCheckNodeExpiry_NoField(t *testing.T) {
	nc := &unstructured.Unstructured{Object: map[string]any{}}
	if got := checkNodeExpiry(nc, fixedHealthNow); got != "" {
		t.Errorf("want '' when expireAfter absent, got %q", got)
	}
}

func TestCheckNodeExpiry_Never(t *testing.T) {
	nc := makeExpiryNodeClaim("Never", fixedHealthNow.Add(-30*24*time.Hour))
	if got := checkNodeExpiry(nc, fixedHealthNow); got != "" {
		t.Errorf("want '' for Never, got %q", got)
	}
}

func TestCheckNodeExpiry_Expired(t *testing.T) {
	// Created 31 days ago, expireAfter=720h (30 days) → expired 1 day ago
	nc := makeExpiryNodeClaim("720h", fixedHealthNow.Add(-31*24*time.Hour))
	if got := checkNodeExpiry(nc, fixedHealthNow); got != "expired" {
		t.Errorf("want 'expired', got %q", got)
	}
}

func TestCheckNodeExpiry_Expiring(t *testing.T) {
	// Created 29 days and 13 hours ago, expireAfter=720h → expires in 11h → within 24h
	nc := makeExpiryNodeClaim("720h", fixedHealthNow.Add(-(29*24+13)*time.Hour))
	if got := checkNodeExpiry(nc, fixedHealthNow); got != "expiring" {
		t.Errorf("want 'expiring', got %q", got)
	}
}

func TestCheckNodeExpiry_NotYet(t *testing.T) {
	// Created 1 day ago, expireAfter=720h → expires in 29 days → not within 24h
	nc := makeExpiryNodeClaim("720h", fixedHealthNow.Add(-24*time.Hour))
	if got := checkNodeExpiry(nc, fixedHealthNow); got != "" {
		t.Errorf("want '' when >24h remaining, got %q", got)
	}
}
