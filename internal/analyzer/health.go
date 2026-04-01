package analyzer

import (
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// buildNodeClaimMap maps node names to their NodeClaim via status.nodeName.
// Nodes with no matching NodeClaim are absent from the map (callers get nil
// on lookup). Follows the same lookup pattern as buildNodePoolMap.
func buildNodeClaimMap(nodeClaims []unstructured.Unstructured) map[string]*unstructured.Unstructured {
	m := make(map[string]*unstructured.Unstructured, len(nodeClaims))
	for i := range nodeClaims {
		nc := &nodeClaims[i]
		if status, ok := nc.Object["status"].(map[string]any); ok {
			if nodeName, ok := status["nodeName"].(string); ok && nodeName != "" {
				m[nodeName] = nc
			}
		}
	}
	return m
}

// checkNodeHealth returns the names of adverse node conditions.
// Returns nil when the node is fully healthy.
//
// Adverse conditions:
//   - Ready=False or Ready=Unknown
//   - MemoryPressure=True
//   - DiskPressure=True
//   - PIDPressure=True
func checkNodeHealth(node *corev1.Node) []string {
	var issues []string
	for _, c := range node.Status.Conditions {
		switch c.Type {
		case corev1.NodeReady:
			if c.Status == corev1.ConditionFalse || c.Status == corev1.ConditionUnknown {
				issues = append(issues, "Ready")
			}
		case corev1.NodeMemoryPressure:
			if c.Status == corev1.ConditionTrue {
				issues = append(issues, "MemoryPressure")
			}
		case corev1.NodeDiskPressure:
			if c.Status == corev1.ConditionTrue {
				issues = append(issues, "DiskPressure")
			}
		case corev1.NodePIDPressure:
			if c.Status == corev1.ConditionTrue {
				issues = append(issues, "PIDPressure")
			}
		}
	}
	return issues
}

// checkNodeExpiry returns "expired", "expiring" (within 24h), or "" based on
// the NodeClaim's spec.expireAfter duration and creation timestamp.
// Returns "" if nc is nil, expireAfter is absent, or expireAfter is "Never".
func checkNodeExpiry(nc *unstructured.Unstructured, now time.Time) string {
	if nc == nil {
		return ""
	}
	spec, ok := nc.Object["spec"].(map[string]any)
	if !ok {
		return ""
	}
	raw, ok := spec["expireAfter"].(string)
	if !ok || raw == "" || raw == "Never" {
		return ""
	}
	d, err := time.ParseDuration(raw)
	if err != nil {
		return ""
	}
	expiry := nc.GetCreationTimestamp().Time.Add(d)
	if expiry.Before(now) {
		return "expired"
	}
	if expiry.Before(now.Add(24 * time.Hour)) {
		return "expiring"
	}
	return ""
}

// checkNodeDrift returns true when the NodeClaim has a Drifted=True status condition.
// Returns false if nc is nil.
func checkNodeDrift(nc *unstructured.Unstructured) bool {
	return false // implemented in Task 3
}

// formatDisruption builds the DISRUPTION column display value.
// Returns "—" when no signals are present.
// Signal order: health first, then drift, then expiry.
func formatDisruption(issues []string, expiryState string, drifted bool) string {
	return emDash // implemented in Task 3
}

// placeholder to satisfy compiler — remove in Task 3
var _ = strings.Join
