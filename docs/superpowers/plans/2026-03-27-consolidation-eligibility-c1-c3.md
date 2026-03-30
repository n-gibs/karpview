# Consolidation Eligibility (C1, C2, C3) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add per-node consolidation classification (`empty`, `daemon-only`, utilization %) to KarpView output as a new CONSOLIDATION column, derived from existing fetched data with no new API calls.

**Architecture:** `classifyNode` is a new pure function in `consolidation.go` that is composed into the existing `Analyze()` loop in `blocker.go`. Classification is orthogonal to blocker detection — a node can be `BLOCKED` and `empty` simultaneously. The printer gains a CONSOLIDATION column between NodePool and the blockers reason.

**Tech Stack:** Go 1.21+, `k8s.io/api/core/v1`, `k8s.io/apimachinery/pkg/api/resource`

---

## File Map

| File | Change |
|------|--------|
| `internal/analyzer/blocker.go` | Add `ConsolidationClass`, `CPURequestFraction`, `MemRequestFraction` fields to `NodeResult` |
| `internal/analyzer/consolidation.go` | New file — constants, `NodeConsolidation`, `classifyNode`, `isDaemonPod`, `safeFraction` |
| `internal/analyzer/consolidation_test.go` | New file — 7 test cases for `classifyNode` |
| `internal/analyzer/blocker_test.go` | Extend existing `Analyze()` tests to assert new fields |
| `internal/printer/printer.go` | Add `formatConsolidation`, CONSOLIDATION column, update `formatReason` for READY nodes |
| `internal/printer/printer_test.go` | Extend existing tests to assert CONSOLIDATION column output |

---

## Task 1: Extend `NodeResult` with consolidation fields

**Files:**
- Modify: `internal/analyzer/blocker.go`

- [ ] **Step 1: Add fields to `NodeResult`**

In `internal/analyzer/blocker.go`, update `NodeResult` from:

```go
type NodeResult struct {
	NodeName string
	NodePool string
	Status   NodeStatus
	Blockers []BlockReason
}
```

to:

```go
type NodeResult struct {
	NodeName            string
	NodePool            string
	Status              NodeStatus
	Blockers            []BlockReason
	ConsolidationClass  string  // "empty" | "daemon-only" | "normal"
	CPURequestFraction  float64 // non-daemon running pod requests / allocatable (0.0–1.0)
	MemRequestFraction  float64
}
```

- [ ] **Step 2: Verify it compiles**

```bash
cd ~/karpview && go build ./...
```

Expected: no errors. (Existing tests may still pass since new fields are zero-valued by default.)

- [ ] **Step 3: Commit**

```bash
git add internal/analyzer/blocker.go
git commit -m "feat(analyzer): add ConsolidationClass and utilization fields to NodeResult"
```

---

## Task 2: Create `consolidation.go` with failing tests first

**Files:**
- Create: `internal/analyzer/consolidation_test.go`
- Create: `internal/analyzer/consolidation.go`

- [ ] **Step 1: Write the failing tests**

Create `internal/analyzer/consolidation_test.go`:

```go
package analyzer

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// helpers

func runningPod(name, nodeName string, ownerKind string, cpuReq, memReq string) *corev1.Pod {
	p := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec: corev1.PodSpec{
			NodeName: nodeName,
			Containers: []corev1.Container{
				{
					Resources: corev1.ResourceRequirements{
						Requests: corev1.ResourceList{},
					},
				},
			},
		},
		Status: corev1.PodStatus{Phase: corev1.PodRunning},
	}
	if cpuReq != "" {
		p.Spec.Containers[0].Resources.Requests[corev1.ResourceCPU] = resource.MustParse(cpuReq)
	}
	if memReq != "" {
		p.Spec.Containers[0].Resources.Requests[corev1.ResourceMemory] = resource.MustParse(memReq)
	}
	if ownerKind != "" {
		p.OwnerReferences = []metav1.OwnerReference{{Kind: ownerKind}}
	}
	return p
}

func allocatableNode(cpu, mem string) *corev1.Node {
	return &corev1.Node{
		Status: corev1.NodeStatus{
			Allocatable: corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse(cpu),
				corev1.ResourceMemory: resource.MustParse(mem),
			},
		},
	}
}

// tests

func TestClassifyNode_NoPods_ReturnsEmpty(t *testing.T) {
	n := allocatableNode("2", "4Gi")
	got := classifyNode(n, nil)
	if got.Class != ConsolidationEmpty {
		t.Errorf("expected empty, got %q", got.Class)
	}
	if got.CPURequestFraction != 0 || got.MemRequestFraction != 0 {
		t.Errorf("expected 0.0 fractions, got cpu=%f mem=%f", got.CPURequestFraction, got.MemRequestFraction)
	}
}

func TestClassifyNode_DaemonPodsOnly_ReturnsDaemonOnly(t *testing.T) {
	n := allocatableNode("2", "4Gi")
	pods := []*corev1.Pod{
		runningPod("ds-1", "node-a", "DaemonSet", "100m", "128Mi"),
		runningPod("ds-2", "node-a", "DaemonSet", "100m", "128Mi"),
	}
	got := classifyNode(n, pods)
	if got.Class != ConsolidationDaemonOnly {
		t.Errorf("expected daemon-only, got %q", got.Class)
	}
	if got.CPURequestFraction != 0 || got.MemRequestFraction != 0 {
		t.Errorf("expected 0.0 fractions, got cpu=%f mem=%f", got.CPURequestFraction, got.MemRequestFraction)
	}
}

func TestClassifyNode_DaemonPluWorkload_ReturnsNormal(t *testing.T) {
	n := allocatableNode("2", "4Gi")
	pods := []*corev1.Pod{
		runningPod("ds-1", "node-a", "DaemonSet", "100m", "128Mi"),
		runningPod("app-1", "node-a", "ReplicaSet", "500m", "256Mi"),
	}
	got := classifyNode(n, pods)
	if got.Class != ConsolidationNormal {
		t.Errorf("expected normal, got %q", got.Class)
	}
}

func TestClassifyNode_SucceededPodOnly_ReturnsEmpty(t *testing.T) {
	n := allocatableNode("2", "4Gi")
	p := runningPod("job-1", "node-a", "Job", "500m", "256Mi")
	p.Status.Phase = corev1.PodSucceeded
	got := classifyNode(n, []*corev1.Pod{p})
	if got.Class != ConsolidationEmpty {
		t.Errorf("expected empty (succeeded pod filtered), got %q", got.Class)
	}
}

func TestClassifyNode_WorkloadWithRequests_ComputesFraction(t *testing.T) {
	// 500m / 2000m = 0.25 cpu; 256Mi / 1Gi = 0.25 mem
	n := allocatableNode("2000m", "1Gi")
	pods := []*corev1.Pod{
		runningPod("app-1", "node-a", "ReplicaSet", "500m", "256Mi"),
	}
	got := classifyNode(n, pods)
	if got.Class != ConsolidationNormal {
		t.Errorf("expected normal, got %q", got.Class)
	}
	if abs(got.CPURequestFraction-0.25) > 0.001 {
		t.Errorf("expected cpu fraction 0.25, got %f", got.CPURequestFraction)
	}
	if abs(got.MemRequestFraction-0.25) > 0.001 {
		t.Errorf("expected mem fraction 0.25, got %f", got.MemRequestFraction)
	}
}

func TestClassifyNode_ZeroAllocatable_ReturnsFractionZero(t *testing.T) {
	n := &corev1.Node{} // no Allocatable set
	pods := []*corev1.Pod{
		runningPod("app-1", "node-a", "ReplicaSet", "500m", "256Mi"),
	}
	got := classifyNode(n, pods)
	if got.CPURequestFraction != 0 || got.MemRequestFraction != 0 {
		t.Errorf("expected 0.0 fractions on zero-allocatable node, got cpu=%f mem=%f",
			got.CPURequestFraction, got.MemRequestFraction)
	}
}

func TestClassifyNode_NoResourceRequests_ReturnsNormalWithZeroFraction(t *testing.T) {
	n := allocatableNode("2", "4Gi")
	p := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "app-1"},
		Spec:       corev1.PodSpec{NodeName: "node-a"},
		Status:     corev1.PodStatus{Phase: corev1.PodRunning},
	}
	got := classifyNode(n, []*corev1.Pod{p})
	if got.Class != ConsolidationNormal {
		t.Errorf("expected normal, got %q", got.Class)
	}
	if got.CPURequestFraction != 0 || got.MemRequestFraction != 0 {
		t.Errorf("expected 0.0 fractions, got cpu=%f mem=%f", got.CPURequestFraction, got.MemRequestFraction)
	}
}

func abs(f float64) float64 {
	if f < 0 {
		return -f
	}
	return f
}
```

- [ ] **Step 2: Run tests — verify they fail**

```bash
cd ~/karpview && go test ./internal/analyzer/... -run TestClassify -v
```

Expected: compile error — `classifyNode`, `ConsolidationEmpty`, etc. not defined yet.

- [ ] **Step 3: Create `consolidation.go`**

Create `internal/analyzer/consolidation.go`:

```go
package analyzer

import (
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
)

const (
	ConsolidationEmpty      = "empty"
	ConsolidationDaemonOnly = "daemon-only"
	ConsolidationNormal     = "normal"
)

// NodeConsolidation holds the classification and utilization for a single node.
type NodeConsolidation struct {
	Class              string
	CPURequestFraction float64
	MemRequestFraction float64
}

// classifyNode determines the consolidation class and request utilization for a node.
// Only Running, non-daemon pods contribute to utilization. Daemon pods are excluded
// because they cannot be bin-packed elsewhere — including them would make daemon-only
// nodes appear busy when Karpenter treats them as empty.
func classifyNode(node *corev1.Node, pods []*corev1.Pod) NodeConsolidation {
	var cpuReq, memReq resource.Quantity
	daemonCount := 0
	totalCount := 0

	for _, pod := range pods {
		if pod.Status.Phase != corev1.PodRunning {
			continue
		}
		if isDaemonPod(pod) {
			daemonCount++
			continue
		}
		totalCount++
		for _, c := range pod.Spec.Containers {
			cpuReq.Add(*c.Resources.Requests.Cpu())
			memReq.Add(*c.Resources.Requests.Memory())
		}
	}

	class := ConsolidationNormal
	if totalCount == 0 && daemonCount == 0 {
		class = ConsolidationEmpty
	} else if totalCount == 0 {
		class = ConsolidationDaemonOnly
	}

	allocCPU := node.Status.Allocatable[corev1.ResourceCPU]
	allocMem := node.Status.Allocatable[corev1.ResourceMemory]

	return NodeConsolidation{
		Class:              class,
		CPURequestFraction: safeFraction(cpuReq.MilliValue(), allocCPU.MilliValue()),
		MemRequestFraction: safeFraction(memReq.Value(), allocMem.Value()),
	}
}

// isDaemonPod returns true if the pod is owned by a DaemonSet.
func isDaemonPod(pod *corev1.Pod) bool {
	for _, ref := range pod.OwnerReferences {
		if ref.Kind == "DaemonSet" {
			return true
		}
	}
	return false
}

// safeFraction returns used/total as a float64, returning 0 when total is zero.
func safeFraction(used, total int64) float64 {
	if total == 0 {
		return 0
	}
	return float64(used) / float64(total)
}
```

- [ ] **Step 4: Run tests — verify they pass**

```bash
cd ~/karpview && go test ./internal/analyzer/... -run TestClassify -v
```

Expected: all 7 tests PASS.

- [ ] **Step 5: Run full test suite**

```bash
cd ~/karpview && go test ./...
```

Expected: all tests pass.

- [ ] **Step 6: Commit**

```bash
git add internal/analyzer/consolidation.go internal/analyzer/consolidation_test.go
git commit -m "feat(analyzer): add classifyNode for consolidation eligibility (C1, C2, C3)"
```

---

## Task 3: Wire `classifyNode` into `Analyze()`

**Files:**
- Modify: `internal/analyzer/blocker.go`

- [ ] **Step 1: Update the node loop in `Analyze()`**

In `internal/analyzer/blocker.go`, find the loop in `Analyze()`:

```go
results := make([]NodeResult, 0, len(data.Nodes))
for i := range data.Nodes {
    node := &data.Nodes[i]
    result := analyzeNode(node, nodePoolMap, podsByNode, pdbEntries)
    results = append(results, result)
}
```

Replace with:

```go
results := make([]NodeResult, 0, len(data.Nodes))
for i := range data.Nodes {
    node := &data.Nodes[i]
    result := analyzeNode(node, nodePoolMap, podsByNode, pdbEntries)
    c := classifyNode(node, podsByNode[node.Name])
    result.ConsolidationClass = c.Class
    result.CPURequestFraction = c.CPURequestFraction
    result.MemRequestFraction = c.MemRequestFraction
    results = append(results, result)
}
```

- [ ] **Step 2: Run full test suite**

```bash
cd ~/karpview && go test ./...
```

Expected: all tests pass.

- [ ] **Step 3: Commit**

```bash
git add internal/analyzer/blocker.go
git commit -m "feat(analyzer): wire classifyNode into Analyze() loop"
```

---

## Task 4: Extend `blocker_test.go` to assert consolidation fields from `Analyze()`

**Files:**
- Modify: `internal/analyzer/blocker_test.go`

- [ ] **Step 1: Add imports for resource**

In `internal/analyzer/blocker_test.go`, add to the import block:

```go
"k8s.io/apimachinery/pkg/api/resource"
```

- [ ] **Step 2: Add a helper for pods with resources and phase**

Add this helper below the existing `pod()` helper:

```go
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
```

- [ ] **Step 3: Add consolidation assertion tests**

Add at the end of `internal/analyzer/blocker_test.go`:

```go
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
```

- [ ] **Step 4: Run tests**

```bash
cd ~/karpview && go test ./internal/analyzer/... -v
```

Expected: all tests pass including the 3 new ones.

- [ ] **Step 5: Commit**

```bash
git add internal/analyzer/blocker_test.go
git commit -m "test(analyzer): assert ConsolidationClass and utilization fields in Analyze()"
```

---

## Task 5: Update printer with CONSOLIDATION column

**Files:**
- Modify: `internal/printer/printer.go`

- [ ] **Step 1: Add `formatConsolidation` function**

Add this function to `internal/printer/printer.go` (after `formatReason`):

```go
// formatConsolidation returns the CONSOLIDATION column value for a node result.
func formatConsolidation(r analyzer.NodeResult) string {
	switch r.ConsolidationClass {
	case analyzer.ConsolidationEmpty:
		return "empty"
	case analyzer.ConsolidationDaemonOnly:
		return "daemon-only"
	default:
		return fmt.Sprintf("%d%% cpu / %d%% mem",
			int(r.CPURequestFraction*100),
			int(r.MemRequestFraction*100),
		)
	}
}
```

- [ ] **Step 2: Update `formatReason` — READY nodes now show `—`**

In `formatReason`, change the `StatusConsolidatable` case from:

```go
case analyzer.StatusConsolidatable:
    return "Consolidatable"
```

to:

```go
case analyzer.StatusConsolidatable:
    return "—"
```

- [ ] **Step 3: Add `maxConsolidation` width calculation and update the row format**

In `Print`, find the width calculation loop:

```go
maxName := 0
maxPool := 0
for _, r := range results {
    if len(r.NodeName) > maxName {
        maxName = len(r.NodeName)
    }
    if len(r.NodePool) > maxPool {
        maxPool = len(r.NodePool)
    }
}
```

Replace with:

```go
maxName := 0
maxPool := 0
maxConsolidation := len("daemon-only") // minimum — longest static value
for _, r := range results {
    if len(r.NodeName) > maxName {
        maxName = len(r.NodeName)
    }
    if len(r.NodePool) > maxPool {
        maxPool = len(r.NodePool)
    }
    if w := len(formatConsolidation(r)); w > maxConsolidation {
        maxConsolidation = w
    }
}
```

- [ ] **Step 4: Update the row format string**

Find the `Fprintf` in the row loop:

```go
fmt.Fprintf(w, "%s  %-*s   NodePool: %-*s   %s\n",
    status,
    maxName, sanitize(r.NodeName),
    maxPool, sanitize(r.NodePool),
    reason,
)
```

Replace with:

```go
fmt.Fprintf(w, "%s  %-*s   NodePool: %-*s   %-*s   %s\n",
    status,
    maxName, sanitize(r.NodeName),
    maxPool, sanitize(r.NodePool),
    maxConsolidation, formatConsolidation(r),
    reason,
)
```

- [ ] **Step 5: Build and verify no errors**

```bash
cd ~/karpview && go build ./...
```

Expected: no errors.

- [ ] **Step 6: Run full test suite**

```bash
cd ~/karpview && go test ./...
```

Expected: all tests pass. (Printer tests that do string matching may fail — fix in Task 6.)

- [ ] **Step 7: Commit**

```bash
git add internal/printer/printer.go
git commit -m "feat(printer): add CONSOLIDATION column to output"
```

---

## Task 6: Update printer tests

**Files:**
- Modify: `internal/printer/printer_test.go`

- [ ] **Step 1: Read the current printer tests**

Read `internal/printer/printer_test.go` to understand existing assertions.

- [ ] **Step 2: Update existing READY node assertions**

Any test that asserts `"Consolidatable"` in the output should be updated to assert `"—"` instead. Any test that checks the full row format should include the new CONSOLIDATION column value (e.g. `"empty"`, `"daemon-only"`, or `"0% cpu / 0% mem"` for zero-request nodes).

- [ ] **Step 3: Add a test for the CONSOLIDATION column values**

Add a test that covers all three classes rendering correctly:

```go
func TestPrint_ConsolidationColumn(t *testing.T) {
	results := []analyzer.NodeResult{
		{NodeName: "node-a", NodePool: "default", Status: analyzer.StatusConsolidatable, ConsolidationClass: analyzer.ConsolidationEmpty},
		{NodeName: "node-b", NodePool: "default", Status: analyzer.StatusConsolidatable, ConsolidationClass: analyzer.ConsolidationDaemonOnly},
		{NodeName: "node-c", NodePool: "default", Status: analyzer.StatusConsolidatable, ConsolidationClass: analyzer.ConsolidationNormal, CPURequestFraction: 0.34, MemRequestFraction: 0.18},
	}
	var buf strings.Builder
	Print(&buf, "test-cluster", results)
	out := buf.String()
	if !strings.Contains(out, "empty") {
		t.Errorf("expected 'empty' in output, got:\n%s", out)
	}
	if !strings.Contains(out, "daemon-only") {
		t.Errorf("expected 'daemon-only' in output, got:\n%s", out)
	}
	if !strings.Contains(out, "34% cpu / 18% mem") {
		t.Errorf("expected '34%% cpu / 18%% mem' in output, got:\n%s", out)
	}
}
```

- [ ] **Step 4: Run printer tests**

```bash
cd ~/karpview && go test ./internal/printer/... -v
```

Expected: all tests pass.

- [ ] **Step 5: Run full test suite**

```bash
cd ~/karpview && go test ./...
```

Expected: all tests pass.

- [ ] **Step 6: Commit**

```bash
git add internal/printer/printer_test.go
git commit -m "test(printer): update assertions for CONSOLIDATION column"
```

---

## Task 7: Final verification

- [ ] **Step 1: Run full test suite with race detector**

```bash
cd ~/karpview && go test -race ./...
```

Expected: all tests pass, no race conditions.

- [ ] **Step 2: Build binary and check size**

```bash
cd ~/karpview && go build -o karpview . && ls -lh karpview
```

Expected: binary builds successfully.

- [ ] **Step 3: Smoke test against kind cluster (if available)**

```bash
./karpview
```

Expected: output includes CONSOLIDATION column between NodePool and blockers. Nodes with no workload pods show `empty` or `daemon-only`. Nodes with workloads show utilization percentages.

- [ ] **Step 4: Clean up binary**

```bash
rm karpview
```
