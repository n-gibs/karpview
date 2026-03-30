package analyzer

import (
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// DisruptionBudget represents one entry from spec.disruption.budgets on a NodePool.
type DisruptionBudget struct {
	Nodes    string   // "20%" or "5" or "0"
	Reasons  []string // nil means applies to all reasons
	Schedule string   // "" | "@daily" | "0 9 * * 1-5"
	Duration string   // "" | "10m" | "1h30m"
}

// NodePoolInfo holds the disruption configuration extracted from a NodePool CRD.
type NodePoolInfo struct {
	ConsolidationPolicy string            // "WhenEmpty" | "WhenEmptyOrUnderutilized" | ""
	ConsolidateAfter    string            // duration string e.g. "30s" | "Never" | ""
	Budgets             []DisruptionBudget // parsed from spec.disruption.budgets
}

// buildNodePoolInfoMap returns a map from NodePool name to its disruption config.
// Fields absent from the NodePool spec are left as empty strings.
// Unnamed NodePools are skipped.
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
