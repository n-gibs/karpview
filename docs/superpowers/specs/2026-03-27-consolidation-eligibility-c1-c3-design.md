# KarpView — Consolidation Eligibility Design (C1, C2, C3)

**Date:** 2026-03-27
**Status:** Approved
**Scope:** Adds per-node consolidation classification and utilization to KarpView output. No new API calls required — all data is derived from existing `Nodes` and `Pods` fetched in `cluster.ClusterData`.

---

## Background

KarpView currently shows whether a node is blocked from disruption (`BLOCKED`, `READY`, `DRAINING`). It does not show *why Karpenter would want to consolidate a node* — i.e. whether the node is empty, daemon-only, or how utilised it is.

Per the [Karpenter disruption docs](https://karpenter.sh/docs/concepts/disruption/):

> "If a node has no running non-daemon pods, it is considered empty."

Karpenter targets empty and daemon-only nodes first (Empty Node Consolidation), before attempting multi-node or single-node consolidation. Surfacing this classification gives operators immediate signal on which nodes are highest-priority consolidation candidates.

| ID | Signal |
|----|--------|
| C1 | CPU and memory request utilization as % of `node.Status.Allocatable` (non-daemon pods only) |
| C2 | Empty node — zero non-daemon, non-succeeded/failed pods |
| C3 | Daemon-only node — Karpenter treats identically to empty for consolidation purposes |

---

## Decisions

- **Approach B** — `classifyNode` is a separate function from `analyzeNode`, composed in `Analyze()`. Classification and blocker detection are orthogonal: a node can be `BLOCKED` and `empty` simultaneously.
- **Non-daemon utilization only** — daemon pods travel with the node and cannot be bin-packed elsewhere. Including them in utilization would make daemon-only nodes appear busy when Karpenter sees them as empty.
- **Running pods only** — filter by `pod.Status.Phase == Running` before counting. Succeeded/Failed pods retain `spec.nodeName` and would otherwise inflate counts or misclassify empty nodes.
- **Static pods excluded** — mirror pods (`kubernetes.io/config.mirror` annotation) do not appear on Karpenter-managed worker nodes in practice. No special handling needed.
- **CONSOLIDATION column** — new column in the printer table. Orthogonal to STATUS. Renders as `empty`, `daemon-only`, or `34% cpu / 18% mem`.
- **JSON output** — no changes needed. `NodeResult` is marshalled directly; new fields are included automatically.

---

## Data Model

### `internal/analyzer/blocker.go`

Two new fields on `NodeResult`:

```go
type NodeResult struct {
    NodeName           string
    NodePool           string
    Status             NodeStatus
    Blockers           []BlockReason
    ConsolidationClass  string   // "empty" | "daemon-only" | "normal"
    CPURequestFraction  float64  // non-daemon running pod requests / allocatable (0.0–1.0)
    MemRequestFraction  float64
}
```

### `internal/analyzer/consolidation.go`

Constants live here alongside the logic that produces them — nothing in `blocker.go` references them:

```go
const (
    ConsolidationEmpty      = "empty"
    ConsolidationDaemonOnly = "daemon-only"
    ConsolidationNormal     = "normal"
)
```

---

## Implementation

### `internal/analyzer/consolidation.go` (new file)

```go
type NodeConsolidation struct {
    Class              string
    CPURequestFraction float64
    MemRequestFraction float64
}

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

func isDaemonPod(pod *corev1.Pod) bool {
    for _, ref := range pod.OwnerReferences {
        if ref.Kind == "DaemonSet" {
            return true
        }
    }
    return false
}

func safeFraction(used, total int64) float64 {
    if total == 0 {
        return 0
    }
    return float64(used) / float64(total)
}
```

### `internal/analyzer/blocker.go` — `Analyze()` change

```go
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

### `internal/printer/printer.go` — new column

```go
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

Table header:
```
STATUS     NODE                    NODEPOOL    CONSOLIDATION       BLOCKERS
BLOCKED    ip-10-0-1-5.ec2...      default     34% cpu / 18% mem  PDB: my-pdb
READY      ip-10-0-1-9.ec2...      spot        empty              —
READY      ip-10-0-1-12.ec2...     default     daemon-only        —
```

---

## Testing

### `internal/analyzer/consolidation_test.go` (new file)

| Scenario | Input | Expected Class | Expected Fractions |
|----------|-------|---------------|-------------------|
| No pods | — | `empty` | 0.0 / 0.0 |
| Daemon pods only | 2 DaemonSet pods (Running) | `daemon-only` | 0.0 / 0.0 |
| Daemon + workload | 1 DaemonSet + 1 Deployment (Running) | `normal` | computed |
| Completed job pod | 1 pod Phase=Succeeded | `empty` | 0.0 / 0.0 |
| Workload with requests | 1 pod 500m cpu / 256Mi mem, allocatable 2000m / 1Gi | `normal` | 0.25 / 0.25 |
| Zero allocatable | any pods | any class | 0.0 / 0.0 |
| No resource requests set | 1 Running pod, empty ResourceList | `normal` | 0.0 / 0.0 |

### `internal/analyzer/blocker_test.go`

Existing tests extended with assertions on `ConsolidationClass`, `CPURequestFraction`, `MemRequestFraction` in `Analyze()` output.
