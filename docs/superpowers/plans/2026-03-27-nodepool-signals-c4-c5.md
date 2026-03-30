# NodePool Signals (C4, C5) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a POLICY column to KarpView output showing each node's NodePool consolidation policy (`WhenEmpty` / `WhenEmptyOrUnderutilized`) and `consolidateAfter` timer, with a `[skip]` flag when the policy means Karpenter will never target the node.

**Architecture:** A new `nodepoolGVR` is added to `fetch.go`, fetching NodePool CRDs as a fifth concurrent goroutine alongside the existing four. `nodepool.go` provides a pure `buildNodePoolInfoMap` function (same pattern as `buildNodePoolMap`) which `Analyze()` calls to populate two new fields on `NodeResult`. The printer gains a POLICY column with `formatPolicy` rendering the abbreviated policy and optional duration and skip flag.

**Tech Stack:** Go 1.21+, `k8s.io/apimachinery/pkg/apis/meta/v1/unstructured`, existing `dynamicfake` test client.

---

## File Map

| File | Change |
|------|--------|
| `internal/cluster/fetch.go` | Add `nodepoolGVR`, `NodePools` field, fifth fetch goroutine |
| `internal/cluster/fetch_test.go` | Update scheme helper, add NodePool fetch test |
| `internal/analyzer/nodepool.go` | New — `NodePoolInfo`, `buildNodePoolInfoMap` |
| `internal/analyzer/nodepool_test.go` | New — 6 unit tests |
| `internal/analyzer/blocker.go` | Add fields to `NodeResult`, refactor `Analyze()` loop |
| `internal/analyzer/blocker_test.go` | 3 new tests |
| `internal/printer/printer.go` | Add `formatPolicy`, POLICY column |
| `internal/printer/printer_test.go` | 7 new tests |

---

## Task 1: Add `NodePools` to `ClusterData` and fetch

**Files:**
- Modify: `internal/cluster/fetch.go`
- Modify: `internal/cluster/fetch_test.go`

- [ ] **Step 1: Add `nodepoolGVR` and `NodePools` field**

In `internal/cluster/fetch.go`, add the GVR constant after `karpenterGVR`:

```go
// nodepoolGVR is the GroupVersionResource for Karpenter NodePools.
var nodepoolGVR = schema.GroupVersionResource{
    Group:    "karpenter.sh",
    Version:  "v1",
    Resource: "nodepools",
}
```

In `ClusterData`, add the `NodePools` field after `NodeClaims`:

```go
type ClusterData struct {
    ClusterName string
    Nodes       []corev1.Node
    NodeClaims  []unstructured.Unstructured
    NodePools   []unstructured.Unstructured
    Pods        []corev1.Pod
    PDBs        []policyv1.PodDisruptionBudget
}
```

- [ ] **Step 2: Write the failing test first**

In `internal/cluster/fetch_test.go`, update `nodeclaimScheme()` to also register NodePool types (add after the existing `NodeClaimList` registration):

```go
s.AddKnownTypeWithName(
    schema.GroupVersionKind{Group: "karpenter.sh", Version: "v1", Kind: "NodePool"},
    &unstructured.Unstructured{},
)
s.AddKnownTypeWithName(
    schema.GroupVersionKind{Group: "karpenter.sh", Version: "v1", Kind: "NodePoolList"},
    &unstructured.UnstructuredList{},
)
```

Add new test at the end of `fetch_test.go`:

```go
func TestFetch_NodePoolsIncluded(t *testing.T) {
    np := &unstructured.Unstructured{}
    np.SetGroupVersionKind(schema.GroupVersionKind{
        Group: "karpenter.sh", Version: "v1", Kind: "NodePool",
    })
    np.SetName("default")

    k8s := kubefake.NewSimpleClientset()
    dyn := dynamicfake.NewSimpleDynamicClient(nodeclaimScheme(), np)

    data, err := Fetch(context.Background(), &Clients{Kubernetes: k8s, Dynamic: dyn})
    if err != nil {
        t.Fatalf("Fetch failed: %v", err)
    }
    if len(data.NodePools) != 1 || data.NodePools[0].GetName() != "default" {
        t.Errorf("nodepools: got %v", data.NodePools)
    }
}
```

Also update `TestFetch_ReturnsAllResourceTypes` — add this assertion after the `NodeClaims` check:

```go
if len(data.NodePools) != 0 {
    t.Errorf("expected 0 nodepools, got %d", len(data.NodePools))
}
```

- [ ] **Step 3: Run test — verify it fails**

```bash
cd ~/karpview && go test ./internal/cluster/... -run TestFetch_NodePoolsIncluded -v
```

Expected: FAIL — `data.NodePools` is nil or empty.

- [ ] **Step 4: Add the fetch goroutine**

In `FetchWithOptions`, add `var nodePools []unstructured.Unstructured` alongside the other `var` declarations, then add a new goroutine after the NodeClaims goroutine:

```go
var nodePools []unstructured.Unstructured

g.Go(func() error {
    return withRetry(ctx, func() error {
        list, err := c.Dynamic.Resource(nodepoolGVR).List(ctx, metav1.ListOptions{})
        if err != nil {
            return fmt.Errorf("listing nodepools: %w", err)
        }
        nodePools = list.Items
        return nil
    })
})
```

Update the return statement to include `NodePools`:

```go
return &ClusterData{
    Nodes:      nodes,
    NodeClaims: nodeClaims,
    NodePools:  nodePools,
    Pods:       pods,
    PDBs:       pdbs,
}, nil
```

- [ ] **Step 5: Run tests — verify they pass**

```bash
cd ~/karpview && go test ./internal/cluster/... -v
```

Expected: all tests pass including `TestFetch_NodePoolsIncluded`.

- [ ] **Step 6: Commit**

```bash
git add internal/cluster/fetch.go internal/cluster/fetch_test.go
git commit -m "feat(cluster): fetch NodePool CRDs and expose in ClusterData"
```

---

## Task 2: Create `nodepool.go` with failing tests first

**Files:**
- Create: `internal/analyzer/nodepool_test.go`
- Create: `internal/analyzer/nodepool.go`

- [ ] **Step 1: Write the failing tests**

Create `internal/analyzer/nodepool_test.go`:

```go
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
```

- [ ] **Step 2: Run tests — verify they fail**

```bash
cd ~/karpview && go test ./internal/analyzer/... -run TestBuildNodePoolInfoMap -v
```

Expected: compile error — `buildNodePoolInfoMap` and `NodePoolInfo` not defined.

- [ ] **Step 3: Create `nodepool.go`**

Create `internal/analyzer/nodepool.go`:

```go
package analyzer

import (
    "k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// NodePoolInfo holds the disruption configuration extracted from a NodePool CRD.
type NodePoolInfo struct {
    ConsolidationPolicy string // "WhenEmpty" | "WhenEmptyOrUnderutilized" | ""
    ConsolidateAfter    string // duration string e.g. "30s" | "Never" | ""
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
        spec, ok := np.Object["spec"].(map[string]any)
        if !ok {
            m[name] = info
            continue
        }
        disruption, ok := spec["disruption"].(map[string]any)
        if !ok {
            m[name] = info
            continue
        }
        if policy, ok := disruption["consolidationPolicy"].(string); ok {
            info.ConsolidationPolicy = policy
        }
        if after, ok := disruption["consolidateAfter"].(string); ok {
            info.ConsolidateAfter = after
        }
        m[name] = info
    }
    return m
}
```

- [ ] **Step 4: Run tests — verify they pass**

```bash
cd ~/karpview && go test ./internal/analyzer/... -run TestBuildNodePoolInfoMap -v
```

Expected: all 6 tests PASS.

- [ ] **Step 5: Run full test suite**

```bash
cd ~/karpview && go test ./...
```

Expected: all tests pass.

- [ ] **Step 6: Commit**

```bash
git add internal/analyzer/nodepool.go internal/analyzer/nodepool_test.go
git commit -m "feat(analyzer): add buildNodePoolInfoMap for NodePool disruption config (C4, C5)"
```

---

## Task 3: Add `NodePoolPolicy` and `ConsolidateAfter` to `NodeResult` and wire into `Analyze()`

**Files:**
- Modify: `internal/analyzer/blocker.go`
- Modify: `internal/analyzer/blocker_test.go`

- [ ] **Step 1: Write the failing tests**

In `internal/analyzer/blocker_test.go`, add these tests at the end of the file. (The `nodePool()` helper is already available from `nodepool_test.go` — same package.)

```go
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
```

- [ ] **Step 2: Run tests — verify they fail**

```bash
cd ~/karpview && go test ./internal/analyzer/... -run "TestAnalyze_NodePoolPolicy|TestAnalyze_UnknownNodePool|TestAnalyze_DrainingNode_Policy" -v
```

Expected: compile error — `NodePoolPolicy` and `ConsolidateAfter` fields not yet on `NodeResult`.

- [ ] **Step 3: Add fields to `NodeResult`**

In `internal/analyzer/blocker.go`, update `NodeResult` from:

```go
type NodeResult struct {
    NodeName            string
    NodePool            string
    Status              NodeStatus
    Blockers            []BlockReason
    ConsolidationClass  string
    CPURequestFraction  float64
    MemRequestFraction  float64
}
```

to:

```go
type NodeResult struct {
    NodeName            string
    NodePool            string
    Status              NodeStatus
    Blockers            []BlockReason
    ConsolidationClass  string
    CPURequestFraction  float64
    MemRequestFraction  float64
    NodePoolPolicy      string  // "WhenEmpty" | "WhenEmptyOrUnderutilized" | ""
    ConsolidateAfter    string  // e.g. "30s" | "Never" | ""
}
```

- [ ] **Step 4: Refactor `Analyze()` to wire in `buildNodePoolInfoMap`**

In `internal/analyzer/blocker.go`, update `Analyze()` from:

```go
func Analyze(data *cluster.ClusterData) []NodeResult {
    if data == nil {
        return nil
    }

    nodePoolMap := buildNodePoolMap(data.NodeClaims)
    podsByNode := indexPodsByNode(data.Pods)
    pdbEntries := compilePDBSelectors(data.PDBs)

    results := make([]NodeResult, 0, len(data.Nodes))
    for i := range data.Nodes {
        node := &data.Nodes[i]
        c := classifyNode(node, podsByNode[node.Name])
        if isDraining(node) {
            results = append(results, NodeResult{
                NodeName:           node.Name,
                NodePool:           resolveNodePool(node, nodePoolMap),
                Status:             StatusDraining,
                ConsolidationClass: c.Class,
                CPURequestFraction: c.CPURequestFraction,
                MemRequestFraction: c.MemRequestFraction,
            })
            continue
        }
        result := analyzeNode(node, nodePoolMap, podsByNode, pdbEntries)
        result.ConsolidationClass = c.Class
        result.CPURequestFraction = c.CPURequestFraction
        result.MemRequestFraction = c.MemRequestFraction
        results = append(results, result)
    }
    return results
}
```

to:

```go
func Analyze(data *cluster.ClusterData) []NodeResult {
    if data == nil {
        return nil
    }

    nodePoolMap := buildNodePoolMap(data.NodeClaims)
    nodePoolInfos := buildNodePoolInfoMap(data.NodePools)
    podsByNode := indexPodsByNode(data.Pods)
    pdbEntries := compilePDBSelectors(data.PDBs)

    results := make([]NodeResult, 0, len(data.Nodes))
    for i := range data.Nodes {
        node := &data.Nodes[i]
        c := classifyNode(node, podsByNode[node.Name])

        var result NodeResult
        if isDraining(node) {
            result = NodeResult{
                NodeName: node.Name,
                NodePool: resolveNodePool(node, nodePoolMap),
                Status:   StatusDraining,
            }
        } else {
            result = analyzeNode(node, nodePoolMap, podsByNode, pdbEntries)
        }

        result.ConsolidationClass = c.Class
        result.CPURequestFraction = c.CPURequestFraction
        result.MemRequestFraction = c.MemRequestFraction
        if info, ok := nodePoolInfos[result.NodePool]; ok {
            result.NodePoolPolicy = info.ConsolidationPolicy
            result.ConsolidateAfter = info.ConsolidateAfter
        }
        results = append(results, result)
    }
    return results
}
```

- [ ] **Step 5: Run new tests — verify they pass**

```bash
cd ~/karpview && go test ./internal/analyzer/... -run "TestAnalyze_NodePoolPolicy|TestAnalyze_UnknownNodePool|TestAnalyze_DrainingNode_Policy" -v
```

Expected: all 3 tests PASS.

- [ ] **Step 6: Run full test suite**

```bash
cd ~/karpview && go test ./...
```

Expected: all tests pass.

- [ ] **Step 7: Commit**

```bash
git add internal/analyzer/blocker.go internal/analyzer/blocker_test.go
git commit -m "feat(analyzer): add NodePoolPolicy and ConsolidateAfter to NodeResult (C4, C5)"
```

---

## Task 4: Add POLICY column to the printer

**Files:**
- Modify: `internal/printer/printer.go`
- Modify: `internal/printer/printer_test.go`

- [ ] **Step 1: Write the failing unit tests for `formatPolicy`**

In `internal/printer/printer_test.go`, add these tests at the end of the file:

```go
func TestFormatPolicy_WhenEmpty(t *testing.T) {
    r := analyzer.NodeResult{NodePoolPolicy: "WhenEmpty"}
    if got := formatPolicy(r); got != "WhenEmpty" {
        t.Errorf("expected WhenEmpty, got %q", got)
    }
}

func TestFormatPolicy_WhenEmptyOrUnderutilized(t *testing.T) {
    r := analyzer.NodeResult{NodePoolPolicy: "WhenEmptyOrUnderutilized"}
    if got := formatPolicy(r); got != "WhenUnderutilized" {
        t.Errorf("expected WhenUnderutilized, got %q", got)
    }
}

func TestFormatPolicy_WithConsolidateAfter(t *testing.T) {
    r := analyzer.NodeResult{NodePoolPolicy: "WhenEmpty", ConsolidateAfter: "30s"}
    if got := formatPolicy(r); got != "WhenEmpty (30s)" {
        t.Errorf("expected 'WhenEmpty (30s)', got %q", got)
    }
}

func TestFormatPolicy_NoPolicyKnown(t *testing.T) {
    r := analyzer.NodeResult{NodePoolPolicy: ""}
    if got := formatPolicy(r); got != "—" {
        t.Errorf("expected '—', got %q", got)
    }
}

func TestFormatPolicy_WhenEmpty_NormalClass_ShowsSkip(t *testing.T) {
    r := analyzer.NodeResult{
        NodePoolPolicy:     "WhenEmpty",
        ConsolidationClass: analyzer.ConsolidationNormal,
    }
    if got := formatPolicy(r); got != "WhenEmpty [skip]" {
        t.Errorf("expected 'WhenEmpty [skip]', got %q", got)
    }
}

func TestFormatPolicy_WhenEmpty_NormalClass_WithTimer_ShowsSkip(t *testing.T) {
    r := analyzer.NodeResult{
        NodePoolPolicy:     "WhenEmpty",
        ConsolidateAfter:   "30s",
        ConsolidationClass: analyzer.ConsolidationNormal,
    }
    if got := formatPolicy(r); got != "WhenEmpty (30s) [skip]" {
        t.Errorf("expected 'WhenEmpty (30s) [skip]', got %q", got)
    }
}
```

- [ ] **Step 2: Run tests — verify they fail**

```bash
cd ~/karpview && go test ./internal/printer/... -run TestFormatPolicy -v
```

Expected: compile error — `formatPolicy` not defined.

- [ ] **Step 3: Add `formatPolicy` to `printer.go`**

Add this function to `internal/printer/printer.go` after `formatConsolidation`:

```go
// formatPolicy returns the POLICY column value for a node result.
// WhenEmptyOrUnderutilized is abbreviated to WhenUnderutilized for column width.
// A [skip] suffix is added when the policy is WhenEmpty but the node has
// non-daemon workload pods — Karpenter will never target this node for
// consolidation regardless of utilization, so the CONSOLIDATION column alone
// is misleading without this flag.
func formatPolicy(r analyzer.NodeResult) string {
    if r.NodePoolPolicy == "" {
        return "—"
    }
    var policy string
    switch r.NodePoolPolicy {
    case "WhenEmpty":
        policy = "WhenEmpty"
    case "WhenEmptyOrUnderutilized":
        policy = "WhenUnderutilized"
    default:
        policy = r.NodePoolPolicy
    }
    if r.ConsolidateAfter != "" {
        policy = fmt.Sprintf("%s (%s)", policy, r.ConsolidateAfter)
    }
    if r.NodePoolPolicy == "WhenEmpty" && r.ConsolidationClass == analyzer.ConsolidationNormal {
        policy += " [skip]"
    }
    return policy
}
```

- [ ] **Step 4: Run unit tests — verify they pass**

```bash
cd ~/karpview && go test ./internal/printer/... -run TestFormatPolicy -v
```

Expected: all 6 tests PASS.

- [ ] **Step 5: Add POLICY column to the `Print` function**

In `internal/printer/printer.go`, update the width calculation loop:

Find:
```go
maxName := 0
maxPool := 0
maxConsolidation := len("daemon-only")
for _, r := range results {
    if len(r.NodeName) > maxName {
        maxName = len(r.NodeName)
    }
    if len(r.NodePool) > maxPool {
        maxPool = len(r.NodePool)
    }
    if n := len(formatConsolidation(r)); n > maxConsolidation {
        maxConsolidation = n
    }
}
```

Replace with:
```go
maxName := 0
maxPool := 0
maxConsolidation := len("daemon-only")
maxPolicy := len("WhenUnderutilized") // minimum — longest static abbreviation
for _, r := range results {
    if len(r.NodeName) > maxName {
        maxName = len(r.NodeName)
    }
    if len(r.NodePool) > maxPool {
        maxPool = len(r.NodePool)
    }
    if n := len(formatConsolidation(r)); n > maxConsolidation {
        maxConsolidation = n
    }
    if n := len(formatPolicy(r)); n > maxPolicy {
        maxPolicy = n
    }
}
```

Update the row `Fprintf` call:

Find:
```go
fmt.Fprintf(w, "%s  %-*s   NodePool: %-*s   %-*s   %s\n",
    status,
    maxName, sanitize(r.NodeName),
    maxPool, sanitize(r.NodePool),
    maxConsolidation, formatConsolidation(r),
    reason,
)
```

Replace with:
```go
fmt.Fprintf(w, "%s  %-*s   NodePool: %-*s   %-*s   %-*s   %s\n",
    status,
    maxName, sanitize(r.NodeName),
    maxPool, sanitize(r.NodePool),
    maxConsolidation, formatConsolidation(r),
    maxPolicy, formatPolicy(r),
    reason,
)
```

- [ ] **Step 6: Build and verify no errors**

```bash
cd ~/karpview && go build ./...
```

Expected: no errors.

- [ ] **Step 7: Write the integration test for `Print`**

Add to `internal/printer/printer_test.go`:

```go
func TestPrint_PolicyColumn(t *testing.T) {
    results := []analyzer.NodeResult{
        {
            NodeName:           "node-a",
            NodePool:           "default",
            Status:             analyzer.StatusConsolidatable,
            ConsolidationClass: analyzer.ConsolidationEmpty,
            NodePoolPolicy:     "WhenEmpty",
            ConsolidateAfter:   "30s",
        },
        {
            NodeName:           "node-b",
            NodePool:           "spot",
            Status:             analyzer.StatusConsolidatable,
            ConsolidationClass: analyzer.ConsolidationNormal,
            CPURequestFraction: 0.25,
            MemRequestFraction: 0.10,
            NodePoolPolicy:     "WhenEmptyOrUnderutilized",
        },
        {
            NodeName:           "node-c",
            NodePool:           "default",
            Status:             analyzer.StatusConsolidatable,
            ConsolidationClass: analyzer.ConsolidationNormal,
            NodePoolPolicy:     "WhenEmpty",
        },
        {
            NodeName: "node-d",
            NodePool: "unknown",
            Status:   analyzer.StatusConsolidatable,
        },
    }
    var buf strings.Builder
    Print(&buf, "test-cluster", results)
    out := buf.String()

    if !strings.Contains(out, "WhenEmpty (30s)") {
        t.Errorf("expected 'WhenEmpty (30s)' in output, got:\n%s", out)
    }
    if !strings.Contains(out, "WhenUnderutilized") {
        t.Errorf("expected 'WhenUnderutilized' in output, got:\n%s", out)
    }
    if !strings.Contains(out, "WhenEmpty [skip]") {
        t.Errorf("expected 'WhenEmpty [skip]' for normal-class WhenEmpty node, got:\n%s", out)
    }
}
```

- [ ] **Step 8: Run all printer tests**

```bash
cd ~/karpview && go test ./internal/printer/... -v
```

Expected: all tests pass including `TestPrint_PolicyColumn`.

- [ ] **Step 9: Run full test suite**

```bash
cd ~/karpview && go test ./...
```

Expected: all tests pass.

- [ ] **Step 10: Commit**

```bash
git add internal/printer/printer.go internal/printer/printer_test.go
git commit -m "feat(printer): add POLICY column showing NodePool consolidation policy (C4, C5)"
```

---

## Task 5: Final verification

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

- [ ] **Step 3: Smoke test output format (optional — requires a cluster)**

```bash
./karpview
```

Expected: output includes POLICY column between CONSOLIDATION and BLOCKERS. Nodes with `WhenEmpty` policy and workload pods show `WhenEmpty [skip]`. Nodes without a NodePool label show `—` in POLICY.

- [ ] **Step 4: Clean up binary**

```bash
rm karpview
```
