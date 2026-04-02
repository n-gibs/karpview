package analyzer

import (
	"time"

	"github.com/nikgibson/karpview/internal/cluster"
	corev1 "k8s.io/api/core/v1"
	policyv1 "k8s.io/api/policy/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
)

const emDash = "—"

// NodeStatus represents whether a node is blocked from consolidation.
type NodeStatus string

const (
	StatusBlocked        NodeStatus = "BLOCKED"
	StatusConsolidatable NodeStatus = "READY"
	StatusDraining       NodeStatus = "DRAINING"
	// StatusUnknown is a zero-value sentinel. analyzeNode never produces it;
	// it exists as a defensive declaration for callers that construct
	// NodeResult values without going through Analyze.
	StatusUnknown NodeStatus = "UNKNOWN"
)

// BlockReason type constants.
const (
	BlockReasonPDB         = "PDB"
	BlockReasonAnnotation  = "Annotation"
	BlockReasonTerminating = "Terminating"
)

// Karpenter annotation and label key constants.
const (
	annotationDoNotDisrupt = "karpenter.sh/do-not-disrupt"
	labelNodePool          = "karpenter.sh/nodepool"
)

// BlockReason describes why a node is blocked from consolidation.
type BlockReason struct {
	Type      string // BlockReasonPDB | BlockReasonAnnotation | BlockReasonTerminating
	Name      string // PDB name, annotation key, or terminating pod name
	Namespace string // e.g. "prod"
	PodName   string // PDB blockers only — the pod whose labels matched the PDB selector
}

// NodeResult is the analysis result for a single node.
type NodeResult struct {
	NodeName            string
	NodePool            string
	Status              NodeStatus
	Blockers            []BlockReason
	ConsolidationClass  string  // "empty" | "daemon-only" | "normal"
	CPURequestFraction  float64 // non-daemon running pod requests / allocatable (0.0–1.0)
	MemRequestFraction  float64
	NodePoolPolicy      string  // "WhenEmpty" | "WhenEmptyOrUnderutilized" | ""
	ConsolidateAfter    string  // e.g. "30s" | "Never" | ""
	BudgetDisplay       string  // pre-formatted BUDGET column value
	BudgetBlocked       bool    // true if effective headroom <= 0
	HealthIssues        []string // adverse node conditions e.g. ["MemoryPressure"]
	ExpiryState         string   // "" | "expiring" | "expired"
	Drifted             bool     // NodeClaim has Drifted=True condition
	DisruptionDisplay   string   // pre-formatted DISRUPTION column value
}

// ExitCode returns 1 if any node is blocked, 0 otherwise.
// Callers should map a non-zero return to their own exit-blocked constant
// and use a separate exit code (e.g. 2) for runtime errors.
func ExitCode(results []NodeResult) int {
	for i := range results {
		if results[i].Status == StatusBlocked {
			return 1
		}
	}
	return 0
}

// Analyze is a pure function that determines which nodes are blocked
// from Karpenter consolidation and why.
func Analyze(data *cluster.ClusterData) []NodeResult {
	return analyze(data, time.Now())
}

// analyze is the testable core of Analyze. now is injected for deterministic testing.
func analyze(data *cluster.ClusterData, now time.Time) []NodeResult {
	if data == nil {
		return nil
	}

	nodePoolMap := buildNodePoolMap(data.NodeClaims)
	nodePoolInfos := buildNodePoolInfoMap(data.NodePools)
	podsByNode := indexPodsByNode(data.Pods)
	pdbEntries := compilePDBSelectors(data.PDBs)
	statsMap := buildPoolStats(data.Nodes, nodePoolMap)
	nodeClaimMap := buildNodeClaimMap(data.NodeClaims)

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
			budgetDisplay = emDash
		} else {
			var budgetHeadroom int
			budgetHeadroom, budgetBlocked, budgetDisplay = evaluateBudgets(
				npInfo.Budgets, reason, npInfo.ConsolidationPolicy, stats, now,
			)
			_ = budgetHeadroom
		}

		nc := nodeClaimMap[node.Name]
		healthIssues := checkNodeHealth(node)
		expiryState := checkNodeExpiry(nc, now)
		drifted := checkNodeDrift(nc)
		disruptionDisplay := formatDisruption(healthIssues, expiryState, drifted)

		if isDraining(node) {
			results = append(results, NodeResult{
				NodeName:           node.Name,
				NodePool:           poolName,
				Status:             StatusDraining,
				ConsolidationClass: c.Class,
				CPURequestFraction: c.CPURequestFraction,
				MemRequestFraction: c.MemRequestFraction,
				NodePoolPolicy:     npInfo.ConsolidationPolicy,
				ConsolidateAfter:   npInfo.ConsolidateAfter,
				BudgetDisplay:      budgetDisplay,
				BudgetBlocked:      budgetBlocked,
				HealthIssues:       healthIssues,
				ExpiryState:        expiryState,
				Drifted:            drifted,
				DisruptionDisplay:  disruptionDisplay,
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
		result.HealthIssues = healthIssues
		result.ExpiryState = expiryState
		result.Drifted = drifted
		result.DisruptionDisplay = disruptionDisplay
		results = append(results, result)
	}
	return results
}

// buildPoolStats computes node counts per NodePool from the existing Nodes slice.
// Keyed by NodePool name. Nodes with pool "unknown" are counted under "unknown".
func buildPoolStats(nodes []corev1.Node, nodePoolMap map[string]string) map[string]PoolStats {
	m := make(map[string]PoolStats)
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

// pdbEntry holds a pre-filtered, pre-compiled PDB selector ready for matching.
type pdbEntry struct {
	pdb      *policyv1.PodDisruptionBudget
	selector labels.Selector
}

// compilePDBSelectors filters ineligible PDBs and pre-compiles label selectors
// so the work is O(PDBs) rather than O(nodes × PDBs).
func compilePDBSelectors(pdbs []policyv1.PodDisruptionBudget) []pdbEntry {
	out := make([]pdbEntry, 0, len(pdbs))
	for i := range pdbs {
		pdb := &pdbs[i]
		// Skip unreconciled PDBs: ObservedGeneration == 0 means the disruption
		// controller has not initialized Status yet; DisruptionsAllowed is unreliable.
		if pdb.Status.ObservedGeneration == 0 || pdb.Status.ExpectedPods == 0 {
			continue
		}
		if pdb.Status.DisruptionsAllowed != 0 {
			continue
		}
		// A nil selector would produce a universal selector matching every pod.
		if pdb.Spec.Selector == nil {
			continue
		}
		sel, err := metav1.LabelSelectorAsSelector(pdb.Spec.Selector)
		if err != nil {
			continue
		}
		out = append(out, pdbEntry{pdb: pdb, selector: sel})
	}
	return out
}

// isDraining returns true when the node has the karpenter.sh/disrupted:NoSchedule
// taint, indicating Karpenter has already begun draining this node.
func isDraining(node *corev1.Node) bool {
	for _, t := range node.Spec.Taints {
		if t.Key == "karpenter.sh/disrupted" && t.Effect == corev1.TaintEffectNoSchedule {
			return true
		}
	}
	return false
}

// analyzeNode checks a single node for consolidation blockers.
func analyzeNode(
	node *corev1.Node,
	nodePoolMap map[string]string,
	podsByNode map[string][]*corev1.Pod,
	pdbEntries []pdbEntry,
) NodeResult {
	result := NodeResult{
		NodeName: node.Name,
		NodePool: resolveNodePool(node, nodePoolMap),
		Blockers: make([]BlockReason, 0, 2),
	}

	// Check do-not-disrupt annotation on the node itself.
	if val, ok := node.Annotations[annotationDoNotDisrupt]; ok && val == "true" {
		result.Blockers = append(result.Blockers, BlockReason{
			Type: BlockReasonAnnotation,
			Name: annotationDoNotDisrupt,
		})
	}

	// Check do-not-disrupt annotation on pods running on this node.
	nodePods := podsByNode[node.Name]
	for _, pod := range nodePods {
		if val, ok := pod.Annotations[annotationDoNotDisrupt]; ok && val == "true" {
			result.Blockers = append(result.Blockers, BlockReason{
				Type:      BlockReasonAnnotation,
				Name:      pod.Name,
				Namespace: pod.Namespace,
			})
		}
	}

	// Check for pods stuck in Terminating with finalizers.
	// A pod with DeletionTimestamp set but finalizers remaining will never
	// complete eviction without external intervention, blocking node drain.
	for _, pod := range nodePods {
		if pod.DeletionTimestamp != nil && len(pod.Finalizers) > 0 {
			result.Blockers = append(result.Blockers, BlockReason{
				Type:      BlockReasonTerminating,
				Name:      pod.Name,
				Namespace: pod.Namespace,
			})
		}
	}

	// Check pre-compiled PDB entries against pods on this node.
	for _, entry := range pdbEntries {
		for _, pod := range nodePods {
			if pod.Namespace != entry.pdb.Namespace {
				continue
			}
			if entry.selector.Matches(labels.Set(pod.Labels)) {
				result.Blockers = append(result.Blockers, BlockReason{
					Type:      BlockReasonPDB,
					Name:      entry.pdb.Name,
					Namespace: entry.pdb.Namespace,
					PodName:   pod.Name,
				})
				break // one match per PDB is enough
			}
		}
	}

	if len(result.Blockers) > 0 {
		result.Status = StatusBlocked
	} else {
		result.Status = StatusConsolidatable
	}
	return result
}

// buildNodePoolMap maps node names to their Karpenter NodePool by examining
// NodeClaim resources. It matches via status.nodeName.
func buildNodePoolMap(nodeClaims []unstructured.Unstructured) map[string]string {
	m := make(map[string]string, len(nodeClaims))
	for i := range nodeClaims {
		nc := &nodeClaims[i]
		nodePool := ""
		if lbls := nc.GetLabels(); lbls != nil {
			nodePool = lbls[labelNodePool]
		}
		if nodePool == "" {
			continue
		}
		if status, ok := nc.Object["status"].(map[string]any); ok {
			if nodeName, ok := status["nodeName"].(string); ok && nodeName != "" {
				m[nodeName] = nodePool
			}
		}
	}
	return m
}

// resolveNodePool returns the NodePool name for a node, or "unknown".
func resolveNodePool(node *corev1.Node, nodePoolMap map[string]string) string {
	if np, ok := nodePoolMap[node.Name]; ok {
		return np
	}
	if lbls := node.Labels; lbls != nil {
		if np := lbls[labelNodePool]; np != "" {
			return np
		}
	}
	return "unknown"
}

// indexPodsByNode groups pods by their spec.nodeName, storing pointers to
// avoid copying pod structs into every node's slice.
func indexPodsByNode(pods []corev1.Pod) map[string][]*corev1.Pod {
	hint := len(pods) / 4
	if hint < 8 {
		hint = 8
	}
	m := make(map[string][]*corev1.Pod, hint)
	for i := range pods {
		if pods[i].Spec.NodeName != "" {
			m[pods[i].Spec.NodeName] = append(m[pods[i].Spec.NodeName], &pods[i])
		}
	}
	return m
}
