# KarpView — Blocker Completeness Design (B1, B2, B3)

**Date:** 2026-03-27
**Status:** Approved
**Scope:** Extends voluntary-disruption blocker detection with three missing signals from the Karpenter disruption model.

---

## Background

KarpView currently detects two voluntary-disruption blockers:

1. `karpenter.sh/do-not-disrupt=true` annotation on the node
2. `karpenter.sh/do-not-disrupt=true` annotation on a pod
3. PDB with `DisruptionsAllowed==0`

Three gaps remain after reviewing the [Karpenter disruption docs](https://karpenter.sh/docs/concepts/disruption/):

| ID | Gap |
|----|-----|
| B1 | Node already has `karpenter.sh/disrupted:NoSchedule` taint — disruption is in-flight, not pending |
| B2 | Pod stuck in `Terminating` with finalizers — drain hangs indefinitely |
| B3 | PDB blocker doesn't identify which pod triggered it — hard to debug on large nodes |

---

## Decisions

- **B1 produces `DRAINING` status**, not `BLOCKED`. A draining node is already being disrupted — it is not blocking consolidation. `DRAINING` short-circuits all blocker analysis.
- **B2 produces `BLOCKED`** with a new `BlockReasonTerminating` type. A terminating pod with finalizers actively prevents drain completion.
- **B3 adds `PodName` to `BlockReason`** for PDB blockers only. The pod name is the secondary identifier ("which pod caused this PDB to fire"), while `Name` remains the primary identifier (the PDB name).
- **Exit code 1 is unchanged** — only `StatusBlocked` triggers it. `StatusDraining` exits 0.
- **Approach B** — `isDraining` pre-check in `Analyze()` short-circuits before `analyzeNode`. B2 and B3 extend `analyzeNode` inline.

---

## Data Model

### `internal/analyzer/blocker.go`

**New status constant:**
```go
StatusDraining NodeStatus = "DRAINING"
```

**New block reason type constant:**
```go
BlockReasonTerminating = "Terminating"
```

**Updated `BlockReason` struct** — adds `PodName` field:
```go
type BlockReason struct {
    Type      string // PDB | Annotation | Terminating
    Name      string // PDB name, annotation key, or pod name
    Namespace string
    PodName   string // PDB blockers only — which pod triggered the PDB
}
```

`PodName` is only populated for `BlockReasonPDB`. For `BlockReasonAnnotation` and `BlockReasonTerminating`, `Name` already identifies the relevant pod.

---

## Analyzer Changes

### `isDraining(node *corev1.Node) bool`

New pure helper. Checks `node.Spec.Taints` for the key `karpenter.sh/disrupted` with effect `NoSchedule`:

```go
func isDraining(node *corev1.Node) bool {
    for _, t := range node.Spec.Taints {
        if t.Key == "karpenter.sh/disrupted" && t.Effect == corev1.TaintEffectNoSchedule {
            return true
        }
    }
    return false
}
```

### `Analyze()` — early-exit for draining nodes

Before calling `analyzeNode`, check `isDraining`:

```go
for i := range data.Nodes {
    node := &data.Nodes[i]
    if isDraining(node) {
        results = append(results, NodeResult{
            NodeName: node.Name,
            NodePool: resolveNodePool(node, nodePoolMap),
            Status:   StatusDraining,
        })
        continue
    }
    results = append(results, analyzeNode(node, nodePoolMap, podsByNode, pdbEntries))
}
```

### `analyzeNode()` — B2: terminating pod check

Added after the existing pod `do-not-disrupt` annotation check in the pod loop:

```go
if pod.DeletionTimestamp != nil && len(pod.Finalizers) > 0 {
    result.Blockers = append(result.Blockers, BlockReason{
        Type:      BlockReasonTerminating,
        Name:      pod.Name,
        Namespace: pod.Namespace,
    })
}
```

Condition: `DeletionTimestamp != nil` means Kubernetes has issued a delete. `len(pod.Finalizers) > 0` means something is holding the pod open — the eviction will never complete without external intervention.

Pods with `DeletionTimestamp != nil` but no finalizers are already evicting cleanly and are not a concern.

### `analyzeNode()` — B3: pod attribution on PDB blockers

When a pod matches a blocking PDB, record its name:

```go
result.Blockers = append(result.Blockers, BlockReason{
    Type:      BlockReasonPDB,
    Name:      entry.pdb.Name,
    Namespace: entry.pdb.Namespace,
    PodName:   pod.Name,
})
```

---

## Printer Changes

### `internal/printer/printer.go`

**New color constant:**
```go
colorYellow = "\033[33m"
```

**`formatStatus`** — new `StatusDraining` case. Column width increases from 7 to 8 chars (`DRAINING` is 8):
```go
case analyzer.StatusDraining:
    if color {
        return fmt.Sprintf("%sDRAINING%s", colorYellow, colorReset)
    }
    return "DRAINING"
```

**`formatReason`** — new `StatusDraining` case:
```go
case analyzer.StatusDraining:
    return "Disruption in progress"
```

**PDB blocker reason format** — adds pod attribution:
```
Before: PDB: payments-pdb (prod)
After:  PDB: payments-pdb (prod) via pod-name
```

**Terminating blocker reason format:**
```
Terminating: pod-name (prod)
```

**Sort order** — BLOCKED first, DRAINING second, READY last:
```go
sort.SliceStable(results, func(i, j int) bool {
    return statusRank(results[i].Status) < statusRank(results[j].Status)
})

func statusRank(s analyzer.NodeStatus) int {
    switch s {
    case analyzer.StatusBlocked:  return 0
    case analyzer.StatusDraining: return 1
    default:                      return 2
    }
}
```

**Summary footer** — adds draining count:
```
2 node(s) blocked, 1 draining / 10 total
```

### JSON output

`jsonNode.Status` emits `"DRAINING"`. `jsonBlocker` gains `podName` (omitempty):

```json
{
  "type": "PDB",
  "name": "payments-pdb",
  "namespace": "prod",
  "podName": "app-pod-xyz"
}
```

---

## Exit Code Behaviour

| Status | Exit code contribution |
|--------|----------------------|
| `READY` | 0 |
| `DRAINING` | 0 |
| `BLOCKED` | 1 |

`ExitCode()` in `blocker.go` requires no change — it already only counts `StatusBlocked`.

---

## Testing Plan

### `internal/analyzer/blocker_test.go`

| Test | Scenario |
|------|----------|
| `TestIsDraining_TaintPresent` | Node with `karpenter.sh/disrupted:NoSchedule` → returns true |
| `TestIsDraining_WrongEffect` | Same key, `PreferNoSchedule` effect → returns false |
| `TestIsDraining_NoTaints` | Clean node → returns false |
| `TestAnalyze_DrainingNode_StatusDraining` | Draining node with blocking PDB → status `DRAINING`, zero blockers |
| `TestAnalyze_DrainingNode_SkipsBlockerCheck` | `do-not-disrupt` annotation on draining node → still `DRAINING`, not `BLOCKED` |
| `TestAnalyze_TerminatingPodWithFinalizer_BlocksNode` | Pod with `DeletionTimestamp` + finalizer → `BlockReasonTerminating` |
| `TestAnalyze_TerminatingPodNoFinalizer_NotBlocked` | Pod with `DeletionTimestamp`, no finalizers → not blocked |
| `TestAnalyze_PDB_RecordsPodName` | PDB blocker → `BlockReason.PodName` matches triggering pod |
| `TestExitCode_DrainingOnly_ReturnsZero` | All nodes draining → exit code 0 |

### `internal/printer/printer_test.go`

| Test | Scenario |
|------|----------|
| `TestPrint_DrainingNode_ShowsYellow` | DRAINING renders with yellow ANSI when color enabled |
| `TestPrint_SortOrder_BlockedDrainingReady` | BLOCKED → DRAINING → READY sort order |
| `TestPrint_Footer_IncludesDrainingCount` | Footer: `X blocked, Y draining / Z total` |
| `TestFormatReason_PDB_IncludesPodName` | PDB reason includes `via pod-name` |
| `TestFormatReason_Terminating` | `Terminating: pod-name (namespace)` format |

### Benchmark impact

`BenchmarkAnalyze_LargeCluster` is unaffected. `DefaultFixtureConfig` has no draining nodes or terminating pods. Draining nodes short-circuit before `analyzeNode` — the change is net-faster for draining nodes, neutral for all others.

---

## Files Changed

| File | Change |
|------|--------|
| `internal/analyzer/blocker.go` | `StatusDraining`, `BlockReasonTerminating`, `PodName` field, `isDraining()`, `Analyze()` early-exit, B2+B3 in `analyzeNode` |
| `internal/printer/printer.go` | Yellow color, `DRAINING` formatting, pod attribution in reasons, updated sort, updated footer |
| `internal/analyzer/blocker_test.go` | 9 new tests |
| `internal/printer/printer_test.go` | 5 new tests |
| `main.go` | Footer counter update for draining nodes |
