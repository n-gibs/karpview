package analyzer

import (
	"fmt"
	"strings"
	"testing"

	"github.com/nikgibson/karpview/internal/cluster"
	corev1 "k8s.io/api/core/v1"
	policyv1 "k8s.io/api/policy/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// --- helpers ---

func node(name string, annotations, labels map[string]string) corev1.Node {
	return corev1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name:        name,
			Annotations: annotations,
			Labels:      labels,
		},
	}
}

func pod(name, namespace, nodeName string, lbls map[string]string) corev1.Pod {
	return corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
			Labels:    lbls,
		},
		Spec: corev1.PodSpec{NodeName: nodeName},
	}
}

func pdb(name, namespace string, disruptionsAllowed int32, selector map[string]string) policyv1.PodDisruptionBudget {
	return policyv1.PodDisruptionBudget{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
		Spec: policyv1.PodDisruptionBudgetSpec{
			Selector: &metav1.LabelSelector{MatchLabels: selector},
		},
		Status: policyv1.PodDisruptionBudgetStatus{
			DisruptionsAllowed: disruptionsAllowed,
			ObservedGeneration: 1,
			ExpectedPods:       1,
		},
	}
}

func nodeClaim(nodeName, nodePool string) unstructured.Unstructured {
	u := unstructured.Unstructured{}
	u.SetLabels(map[string]string{"karpenter.sh/nodepool": nodePool})
	u.Object["status"] = map[string]interface{}{"nodeName": nodeName}
	return u
}

func runningPodWithResources(name, namespace, nodeName, ownerKind, cpuReq, memReq string) corev1.Pod {
	p := corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
		Spec: corev1.PodSpec{
			NodeName: nodeName,
			Containers: []corev1.Container{
				{
					Resources: corev1.ResourceRequirements{
						Requests: corev1.ResourceList{
							corev1.ResourceCPU:    resource.MustParse(cpuReq),
							corev1.ResourceMemory: resource.MustParse(memReq),
						},
					},
				},
			},
		},
		Status: corev1.PodStatus{Phase: corev1.PodRunning},
	}
	if ownerKind != "" {
		p.OwnerReferences = []metav1.OwnerReference{{Kind: ownerKind}}
	}
	return p
}

func nodeWithAllocatable(name, cpu, mem string) corev1.Node {
	return corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Status: corev1.NodeStatus{
			Allocatable: corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse(cpu),
				corev1.ResourceMemory: resource.MustParse(mem),
			},
		},
	}
}

// --- tests ---

func TestExitCode_AllConsolidatable_ReturnsZero(t *testing.T) {
	results := []NodeResult{
		{Status: StatusConsolidatable},
		{Status: StatusConsolidatable},
	}
	if got := ExitCode(results); got != 0 {
		t.Errorf("expected 0, got %d", got)
	}
}

func TestExitCode_AnyBlocked_ReturnsOne(t *testing.T) {
	results := []NodeResult{
		{Status: StatusConsolidatable},
		{Status: StatusBlocked},
	}
	if got := ExitCode(results); got != 1 {
		t.Errorf("expected 1, got %d", got)
	}
}

func TestExitCode_EmptyResults_ReturnsZero(t *testing.T) {
	if got := ExitCode(nil); got != 0 {
		t.Errorf("expected 0, got %d", got)
	}
}

func TestAnalyze_NilInput(t *testing.T) {
	results := Analyze(nil)
	if results != nil {
		t.Fatalf("expected nil, got %v", results)
	}
}

func TestAnalyze_NodeWithNoBlockers_IsConsolidatable(t *testing.T) {
	input := &cluster.ClusterData{
		Nodes: []corev1.Node{node("node-1", nil, nil)},
	}
	results := Analyze(input)

	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if results[0].Status != StatusConsolidatable {
		t.Errorf("expected READY, got %s", results[0].Status)
	}
	if len(results[0].Blockers) != 0 {
		t.Errorf("expected no blockers, got %v", results[0].Blockers)
	}
	// Node with no pods is empty.
	if results[0].ConsolidationClass != ConsolidationEmpty {
		t.Errorf("expected ConsolidationClass=empty, got %q", results[0].ConsolidationClass)
	}
	if results[0].CPURequestFraction != 0 {
		t.Errorf("expected CPURequestFraction=0, got %f", results[0].CPURequestFraction)
	}
	if results[0].MemRequestFraction != 0 {
		t.Errorf("expected MemRequestFraction=0, got %f", results[0].MemRequestFraction)
	}
}

func TestAnalyze_DoNotDisruptAnnotation_BlocksNode(t *testing.T) {
	input := &cluster.ClusterData{
		Nodes: []corev1.Node{
			node("node-1", map[string]string{"karpenter.sh/do-not-disrupt": "true"}, nil),
		},
	}
	results := Analyze(input)

	if results[0].Status != StatusBlocked {
		t.Errorf("expected BLOCKED, got %s", results[0].Status)
	}
	if len(results[0].Blockers) != 1 || results[0].Blockers[0].Type != BlockReasonAnnotation {
		t.Errorf("expected Annotation blocker, got %v", results[0].Blockers)
	}
}

func TestAnalyze_DoNotDisruptAnnotationFalse_NotBlocked(t *testing.T) {
	input := &cluster.ClusterData{
		Nodes: []corev1.Node{
			node("node-1", map[string]string{"karpenter.sh/do-not-disrupt": "false"}, nil),
		},
	}
	results := Analyze(input)

	if results[0].Status != StatusConsolidatable {
		t.Errorf("expected READY, got %s", results[0].Status)
	}
}

func TestAnalyze_PDBWithZeroDisruptions_BlocksNode(t *testing.T) {
	input := &cluster.ClusterData{
		Nodes: []corev1.Node{node("node-1", nil, nil)},
		Pods: []corev1.Pod{
			pod("app-pod", "prod", "node-1", map[string]string{"app": "payments"}),
		},
		PDBs: []policyv1.PodDisruptionBudget{
			pdb("payments-pdb", "prod", 0, map[string]string{"app": "payments"}),
		},
	}
	results := Analyze(input)

	if results[0].Status != StatusBlocked {
		t.Errorf("expected BLOCKED, got %s", results[0].Status)
	}
	if len(results[0].Blockers) != 1 {
		t.Fatalf("expected 1 blocker, got %d", len(results[0].Blockers))
	}
	b := results[0].Blockers[0]
	if b.Type != BlockReasonPDB || b.Name != "payments-pdb" || b.Namespace != "prod" {
		t.Errorf("unexpected blocker: %+v", b)
	}
}

func TestAnalyze_PDBWithDisruptionsAllowed_NotBlocked(t *testing.T) {
	input := &cluster.ClusterData{
		Nodes: []corev1.Node{node("node-1", nil, nil)},
		Pods: []corev1.Pod{
			pod("app-pod", "prod", "node-1", map[string]string{"app": "payments"}),
		},
		PDBs: []policyv1.PodDisruptionBudget{
			pdb("payments-pdb", "prod", 1, map[string]string{"app": "payments"}),
		},
	}
	results := Analyze(input)

	if results[0].Status != StatusConsolidatable {
		t.Errorf("expected READY, got %s", results[0].Status)
	}
}

func TestAnalyze_PDBInDifferentNamespace_NotBlocked(t *testing.T) {
	input := &cluster.ClusterData{
		Nodes: []corev1.Node{node("node-1", nil, nil)},
		Pods: []corev1.Pod{
			pod("app-pod", "prod", "node-1", map[string]string{"app": "payments"}),
		},
		PDBs: []policyv1.PodDisruptionBudget{
			pdb("payments-pdb", "staging", 0, map[string]string{"app": "payments"}),
		},
	}
	results := Analyze(input)

	if results[0].Status != StatusConsolidatable {
		t.Errorf("PDB in different namespace should not block; got %s", results[0].Status)
	}
}

func TestAnalyze_NodePool_ResolvedFromNodeClaim(t *testing.T) {
	input := &cluster.ClusterData{
		Nodes:      []corev1.Node{node("node-1", nil, nil)},
		NodeClaims: []unstructured.Unstructured{nodeClaim("node-1", "general-purpose")},
	}
	results := Analyze(input)

	if results[0].NodePool != "general-purpose" {
		t.Errorf("expected NodePool 'general-purpose', got %q", results[0].NodePool)
	}
}

func TestAnalyze_NodePool_FallbackToNodeLabel(t *testing.T) {
	input := &cluster.ClusterData{
		Nodes: []corev1.Node{
			node("node-1", nil, map[string]string{"karpenter.sh/nodepool": "spot-pool"}),
		},
	}
	results := Analyze(input)

	if results[0].NodePool != "spot-pool" {
		t.Errorf("expected NodePool 'spot-pool', got %q", results[0].NodePool)
	}
}

func TestAnalyze_NodePool_UnknownWhenNoMatch(t *testing.T) {
	input := &cluster.ClusterData{
		Nodes: []corev1.Node{node("node-1", nil, nil)},
	}
	results := Analyze(input)

	if results[0].NodePool != "unknown" {
		t.Errorf("expected NodePool 'unknown', got %q", results[0].NodePool)
	}
}

func TestAnalyze_MultipleNodes_IndependentResults(t *testing.T) {
	input := &cluster.ClusterData{
		Nodes: []corev1.Node{
			node("node-blocked", map[string]string{"karpenter.sh/do-not-disrupt": "true"}, nil),
			node("node-clean", nil, nil),
		},
	}
	results := Analyze(input)

	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(results))
	}

	byName := make(map[string]NodeResult)
	for _, r := range results {
		byName[r.NodeName] = r
	}

	if byName["node-blocked"].Status != StatusBlocked {
		t.Errorf("node-blocked: expected BLOCKED, got %s", byName["node-blocked"].Status)
	}
	if byName["node-clean"].Status != StatusConsolidatable {
		t.Errorf("node-clean: expected READY, got %s", byName["node-clean"].Status)
	}
}

func TestAnalyze_PodDoNotDisruptAnnotation_BlocksNode(t *testing.T) {
	input := &cluster.ClusterData{
		Nodes: []corev1.Node{node("node-1", nil, nil)},
		Pods: []corev1.Pod{
			pod("critical-pod", "prod", "node-1", map[string]string{"app": "critical"}),
		},
	}
	input.Pods[0].Annotations = map[string]string{"karpenter.sh/do-not-disrupt": "true"}

	results := Analyze(input)

	if results[0].Status != StatusBlocked {
		t.Errorf("expected BLOCKED, got %s", results[0].Status)
	}
	if len(results[0].Blockers) != 1 {
		t.Fatalf("expected 1 blocker, got %d: %v", len(results[0].Blockers), results[0].Blockers)
	}
	b := results[0].Blockers[0]
	if b.Type != BlockReasonAnnotation || b.Name != "critical-pod" || b.Namespace != "prod" {
		t.Errorf("unexpected blocker: %+v", b)
	}
}

func TestAnalyze_PodDoNotDisruptAnnotationFalse_NotBlocked(t *testing.T) {
	input := &cluster.ClusterData{
		Nodes: []corev1.Node{node("node-1", nil, nil)},
		Pods: []corev1.Pod{
			pod("app-pod", "prod", "node-1", map[string]string{"app": "payments"}),
		},
	}
	input.Pods[0].Annotations = map[string]string{"karpenter.sh/do-not-disrupt": "false"}

	results := Analyze(input)

	if results[0].Status != StatusConsolidatable {
		t.Errorf("expected READY, got %s", results[0].Status)
	}
}

func TestAnalyze_PDB_ObservedGenerationZero_NotBlocked(t *testing.T) {
	// Simulate an unreconciled PDB: DisruptionsAllowed=0 but Status not yet
	// initialized by the disruption controller. Must not be treated as a blocker.
	p := pdb("payments-pdb", "prod", 0, map[string]string{"app": "payments"})
	p.Status.ObservedGeneration = 0
	p.Status.ExpectedPods = 0

	input := &cluster.ClusterData{
		Nodes: []corev1.Node{node("node-1", nil, nil)},
		Pods: []corev1.Pod{
			pod("app-pod", "prod", "node-1", map[string]string{"app": "payments"}),
		},
		PDBs: []policyv1.PodDisruptionBudget{p},
	}
	results := Analyze(input)

	if results[0].Status != StatusConsolidatable {
		t.Errorf("unreconciled PDB (ObservedGeneration=0) should not block; got %s", results[0].Status)
	}
}

func TestAnalyze_PDB_NilSelector_NotBlocked(t *testing.T) {
	p := pdb("nil-selector-pdb", "prod", 0, map[string]string{"app": "payments"})
	p.Spec.Selector = nil
	p.Status.ObservedGeneration = 1
	p.Status.ExpectedPods = 1

	input := &cluster.ClusterData{
		Nodes: []corev1.Node{node("node-1", nil, nil)},
		Pods: []corev1.Pod{
			pod("app-pod", "prod", "node-1", map[string]string{"app": "payments"}),
		},
		PDBs: []policyv1.PodDisruptionBudget{p},
	}
	results := Analyze(input)

	if results[0].Status != StatusConsolidatable {
		t.Errorf("PDB with nil selector should not block; got %s", results[0].Status)
	}
}

func TestAnalyze_BothAnnotationAndPDB_TwoBlockers(t *testing.T) {
	input := &cluster.ClusterData{
		Nodes: []corev1.Node{
			node("node-1", map[string]string{"karpenter.sh/do-not-disrupt": "true"}, nil),
		},
		Pods: []corev1.Pod{
			pod("app-pod", "prod", "node-1", map[string]string{"app": "payments"}),
		},
		PDBs: []policyv1.PodDisruptionBudget{
			pdb("payments-pdb", "prod", 0, map[string]string{"app": "payments"}),
		},
	}
	results := Analyze(input)

	if results[0].Status != StatusBlocked {
		t.Errorf("expected BLOCKED, got %s", results[0].Status)
	}
	if len(results[0].Blockers) != 2 {
		t.Errorf("expected 2 blockers, got %d: %v", len(results[0].Blockers), results[0].Blockers)
	}
}

// TEST-6: StatusUnknown is never produced by analyzeNode but ExitCode must
// treat it as non-blocked (exit 0). This test documents that contract.
func TestExitCode_UnknownStatus_ReturnsZero(t *testing.T) {
	results := []NodeResult{{Status: StatusUnknown}}
	if got := ExitCode(results); got != 0 {
		t.Errorf("expected 0 for StatusUnknown, got %d", got)
	}
}

// TEST-8: buildNodePoolMap must skip NodeClaims missing the nodepool label
// or missing status.nodeName — both are valid Karpenter edge cases.
func TestBuildNodePoolMap_MissingNodepoolLabel_Skipped(t *testing.T) {
	nc := unstructured.Unstructured{Object: map[string]any{}}
	nc.Object["status"] = map[string]any{"nodeName": "node-1"}
	// no karpenter.sh/nodepool label

	m := buildNodePoolMap([]unstructured.Unstructured{nc})
	if _, ok := m["node-1"]; ok {
		t.Error("expected node-1 absent when nodepool label missing")
	}
}

func TestBuildNodePoolMap_MissingNodeName_Skipped(t *testing.T) {
	nc := unstructured.Unstructured{}
	nc.SetLabels(map[string]string{"karpenter.sh/nodepool": "default"})
	// no status field at all

	m := buildNodePoolMap([]unstructured.Unstructured{nc})
	if len(m) != 0 {
		t.Errorf("expected empty map, got %v", m)
	}
}

// TEST-4: Benchmark compilePDBSelectors + selector matching at scale.
// 500 nodes × 50 PDBs × 20 pods/node — the O(nodes × PDBs × pods) hot path.
func BenchmarkAnalyze_LargeCluster(b *testing.B) {
	const (
		numNodes    = 500
		numPDBs     = 50
		podsPerNode = 20
	)

	nodes := make([]corev1.Node, numNodes)
	for i := range nodes {
		nodes[i] = node(fmt.Sprintf("node-%d", i), nil, nil)
	}

	pods := make([]corev1.Pod, numNodes*podsPerNode)
	for i := range pods {
		nodeIdx := i / podsPerNode
		pods[i] = pod(
			fmt.Sprintf("pod-%d", i),
			"prod",
			fmt.Sprintf("node-%d", nodeIdx),
			map[string]string{"app": fmt.Sprintf("app-%d", i%numPDBs)},
		)
	}

	pdbs := make([]policyv1.PodDisruptionBudget, numPDBs)
	for i := range pdbs {
		pdbs[i] = pdb(
			fmt.Sprintf("pdb-%d", i),
			"prod",
			0,
			map[string]string{"app": fmt.Sprintf("app-%d", i)},
		)
	}

	input := &cluster.ClusterData{
		Nodes: nodes,
		Pods:  pods,
		PDBs:  pdbs,
	}

	b.ResetTimer()
	for range b.N {
		Analyze(input)
	}
}

func taint(key string, effect corev1.TaintEffect) corev1.Taint {
	return corev1.Taint{Key: key, Effect: effect}
}

func TestIsDraining_TaintPresent(t *testing.T) {
	n := node("node-1", nil, nil)
	n.Spec.Taints = []corev1.Taint{
		taint("karpenter.sh/disrupted", corev1.TaintEffectNoSchedule),
	}
	if !isDraining(&n) {
		t.Error("expected isDraining=true for karpenter.sh/disrupted:NoSchedule")
	}
}

func TestIsDraining_WrongEffect(t *testing.T) {
	n := node("node-1", nil, nil)
	n.Spec.Taints = []corev1.Taint{
		taint("karpenter.sh/disrupted", corev1.TaintEffectPreferNoSchedule),
	}
	if isDraining(&n) {
		t.Error("expected isDraining=false for PreferNoSchedule effect")
	}
}

func TestIsDraining_NoTaints(t *testing.T) {
	n := node("node-1", nil, nil)
	if isDraining(&n) {
		t.Error("expected isDraining=false for node with no taints")
	}
}

func TestAnalyze_DrainingNode_StatusDraining(t *testing.T) {
	n := node("node-1", nil, nil)
	n.Spec.Taints = []corev1.Taint{
		taint("karpenter.sh/disrupted", corev1.TaintEffectNoSchedule),
	}
	// Even with a blocking PDB on the node, status must be DRAINING.
	input := &cluster.ClusterData{
		Nodes: []corev1.Node{n},
		Pods: []corev1.Pod{
			pod("app-pod", "prod", "node-1", map[string]string{"app": "payments"}),
		},
		PDBs: []policyv1.PodDisruptionBudget{
			pdb("payments-pdb", "prod", 0, map[string]string{"app": "payments"}),
		},
	}
	results := Analyze(input)

	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if results[0].Status != StatusDraining {
		t.Errorf("expected DRAINING, got %s", results[0].Status)
	}
	if len(results[0].Blockers) != 0 {
		t.Errorf("expected no blockers for draining node, got %v", results[0].Blockers)
	}
}

func TestAnalyze_DrainingNode_SkipsBlockerCheck(t *testing.T) {
	n := node("node-1", map[string]string{"karpenter.sh/do-not-disrupt": "true"}, nil)
	n.Spec.Taints = []corev1.Taint{
		taint("karpenter.sh/disrupted", corev1.TaintEffectNoSchedule),
	}
	input := &cluster.ClusterData{Nodes: []corev1.Node{n}}
	results := Analyze(input)

	if results[0].Status != StatusDraining {
		t.Errorf("do-not-disrupt on draining node should not produce BLOCKED; got %s", results[0].Status)
	}
}

func TestAnalyze_TerminatingPodWithFinalizer_BlocksNode(t *testing.T) {
	now := metav1.Now()
	p := pod("stuck-pod", "prod", "node-1", map[string]string{"app": "payments"})
	p.DeletionTimestamp = &now
	p.Finalizers = []string{"example.com/my-finalizer"}

	input := &cluster.ClusterData{
		Nodes: []corev1.Node{node("node-1", nil, nil)},
		Pods:  []corev1.Pod{p},
	}
	results := Analyze(input)

	if results[0].Status != StatusBlocked {
		t.Errorf("expected BLOCKED, got %s", results[0].Status)
	}
	if len(results[0].Blockers) != 1 {
		t.Fatalf("expected 1 blocker, got %d", len(results[0].Blockers))
	}
	b := results[0].Blockers[0]
	if b.Type != BlockReasonTerminating {
		t.Errorf("expected BlockReasonTerminating, got %q", b.Type)
	}
	if b.Name != "stuck-pod" {
		t.Errorf("expected Name=stuck-pod, got %q", b.Name)
	}
	if b.Namespace != "prod" {
		t.Errorf("expected Namespace=prod, got %q", b.Namespace)
	}
}

func TestAnalyze_TerminatingPodNoFinalizer_NotBlocked(t *testing.T) {
	now := metav1.Now()
	p := pod("evicting-pod", "prod", "node-1", map[string]string{"app": "payments"})
	p.DeletionTimestamp = &now
	// No finalizers — pod is evicting cleanly, not stuck.

	input := &cluster.ClusterData{
		Nodes: []corev1.Node{node("node-1", nil, nil)},
		Pods:  []corev1.Pod{p},
	}
	results := Analyze(input)

	if results[0].Status != StatusConsolidatable {
		t.Errorf("terminating pod with no finalizers should not block; got %s", results[0].Status)
	}
}

func TestAnalyze_PDB_RecordsPodName(t *testing.T) {
	input := &cluster.ClusterData{
		Nodes: []corev1.Node{node("node-1", nil, nil)},
		Pods: []corev1.Pod{
			pod("app-pod-xyz", "prod", "node-1", map[string]string{"app": "payments"}),
		},
		PDBs: []policyv1.PodDisruptionBudget{
			pdb("payments-pdb", "prod", 0, map[string]string{"app": "payments"}),
		},
	}
	results := Analyze(input)

	if len(results[0].Blockers) != 1 {
		t.Fatalf("expected 1 blocker, got %d", len(results[0].Blockers))
	}
	b := results[0].Blockers[0]
	if b.Type != BlockReasonPDB {
		t.Errorf("expected BlockReasonPDB, got %q", b.Type)
	}
	if b.PodName != "app-pod-xyz" {
		t.Errorf("expected PodName=app-pod-xyz, got %q", b.PodName)
	}
}

func TestAnalyze_EmptyNode_ClassifiedAsEmpty(t *testing.T) {
	n := nodeWithAllocatable("node-a", "2", "4Gi")
	data := &cluster.ClusterData{Nodes: []corev1.Node{n}}
	results := Analyze(data)
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if results[0].ConsolidationClass != ConsolidationEmpty {
		t.Errorf("expected empty, got %q", results[0].ConsolidationClass)
	}
}

func TestAnalyze_DaemonOnlyNode_ClassifiedAsDaemonOnly(t *testing.T) {
	n := nodeWithAllocatable("node-a", "2", "4Gi")
	ds := runningPodWithResources("ds-1", "kube-system", "node-a", "DaemonSet", "100m", "128Mi")
	data := &cluster.ClusterData{
		Nodes: []corev1.Node{n},
		Pods:  []corev1.Pod{ds},
	}
	results := Analyze(data)
	if results[0].ConsolidationClass != ConsolidationDaemonOnly {
		t.Errorf("expected daemon-only, got %q", results[0].ConsolidationClass)
	}
}

func TestAnalyze_WorkloadNode_ComputesUtilization(t *testing.T) {
	n := nodeWithAllocatable("node-a", "2000m", "1Gi")
	app := runningPodWithResources("app-1", "default", "node-a", "ReplicaSet", "500m", "256Mi")
	data := &cluster.ClusterData{
		Nodes: []corev1.Node{n},
		Pods:  []corev1.Pod{app},
	}
	results := Analyze(data)
	if results[0].ConsolidationClass != ConsolidationNormal {
		t.Errorf("expected normal, got %q", results[0].ConsolidationClass)
	}
	if results[0].CPURequestFraction < 0.24 || results[0].CPURequestFraction > 0.26 {
		t.Errorf("expected cpu ~0.25, got %f", results[0].CPURequestFraction)
	}
}

func TestExitCode_DrainingOnly_ReturnsZero(t *testing.T) {
	results := []NodeResult{
		{Status: StatusDraining},
		{Status: StatusDraining},
	}
	if got := ExitCode(results); got != 0 {
		t.Errorf("expected 0 for all-draining results, got %d", got)
	}
}

func TestAnalyze_NodePoolPolicyPopulated(t *testing.T) {
	n := nodeWithAllocatable("node-a", "2", "4Gi")
	n.Labels = map[string]string{"karpenter.sh/nodepool": "default"}
	np := nodePool("default", "WhenEmpty", "30s")
	data := &cluster.ClusterData{
		Nodes:     []corev1.Node{n},
		NodePools: []unstructured.Unstructured{np},
	}
	results := Analyze(data)
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if results[0].NodePoolPolicy != "WhenEmpty" {
		t.Errorf("expected WhenEmpty, got %q", results[0].NodePoolPolicy)
	}
	if results[0].ConsolidateAfter != "30s" {
		t.Errorf("expected 30s, got %q", results[0].ConsolidateAfter)
	}
}

func TestAnalyze_UnknownNodePool_NoPolicy(t *testing.T) {
	n := nodeWithAllocatable("node-a", "2", "4Gi")
	// no nodepool label, no NodeClaim, no NodePool
	data := &cluster.ClusterData{Nodes: []corev1.Node{n}}
	results := Analyze(data)
	if results[0].NodePoolPolicy != "" {
		t.Errorf("expected empty policy for unknown pool, got %q", results[0].NodePoolPolicy)
	}
	if results[0].ConsolidateAfter != "" {
		t.Errorf("expected empty consolidateAfter for unknown pool, got %q", results[0].ConsolidateAfter)
	}
}

func TestAnalyze_DrainingNode_PolicyPopulated(t *testing.T) {
	n := nodeWithAllocatable("node-a", "2", "4Gi")
	n.Labels = map[string]string{"karpenter.sh/nodepool": "spot"}
	n.Spec.Taints = []corev1.Taint{
		{Key: "karpenter.sh/disrupted", Effect: corev1.TaintEffectNoSchedule},
	}
	np := nodePool("spot", "WhenEmptyOrUnderutilized", "1m")
	data := &cluster.ClusterData{
		Nodes:     []corev1.Node{n},
		NodePools: []unstructured.Unstructured{np},
	}
	results := Analyze(data)
	if results[0].Status != StatusDraining {
		t.Fatalf("expected DRAINING, got %q", results[0].Status)
	}
	if results[0].NodePoolPolicy != "WhenEmptyOrUnderutilized" {
		t.Errorf("expected WhenEmptyOrUnderutilized, got %q", results[0].NodePoolPolicy)
	}
	if results[0].ConsolidateAfter != "1m" {
		t.Errorf("expected 1m, got %q", results[0].ConsolidateAfter)
	}
}

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

func TestAnalyze_DrainingNodeShowsDisruption(t *testing.T) {
	n := corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: "draining-node"},
		Spec: corev1.NodeSpec{
			Taints: []corev1.Taint{{
				Key:    "karpenter.sh/disrupted",
				Effect: corev1.TaintEffectNoSchedule,
			}},
		},
		Status: corev1.NodeStatus{
			Conditions: []corev1.NodeCondition{{
				Type:   corev1.NodeMemoryPressure,
				Status: corev1.ConditionTrue,
			}},
		},
	}
	data := &cluster.ClusterData{Nodes: []corev1.Node{n}}
	results := Analyze(data)
	if len(results) != 1 {
		t.Fatalf("want 1 result, got %d", len(results))
	}
	r := results[0]
	if r.Status != StatusDraining {
		t.Errorf("want DRAINING, got %s", r.Status)
	}
	if r.DisruptionDisplay != "unhealthy:MemoryPressure" {
		t.Errorf("want DisruptionDisplay=%q, got %q", "unhealthy:MemoryPressure", r.DisruptionDisplay)
	}
	if len(r.HealthIssues) != 1 || r.HealthIssues[0] != "MemoryPressure" {
		t.Errorf("want HealthIssues=[MemoryPressure], got %v", r.HealthIssues)
	}
}

func TestAnalyze_NilClaimNodeNoDisruption(t *testing.T) {
	// Node with no NodeClaim — nodeClaimMap lookup returns nil — should show emDash
	n := corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: "clean-node"},
	}
	data := &cluster.ClusterData{Nodes: []corev1.Node{n}}
	results := Analyze(data)
	if len(results) != 1 {
		t.Fatalf("want 1 result, got %d", len(results))
	}
	if results[0].DisruptionDisplay != emDash {
		t.Errorf("want DisruptionDisplay=%q, got %q", emDash, results[0].DisruptionDisplay)
	}
}
