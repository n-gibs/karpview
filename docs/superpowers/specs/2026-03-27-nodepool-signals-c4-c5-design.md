# KarpView — NodePool Signals Design (C4, C5)

**Date:** 2026-03-27
**Status:** Approved
**Scope:** Adds NodePool consolidation policy (C4) and consolidateAfter timer (C5) to KarpView output. Requires fetching NodePool CRDs — a new API call added to the existing concurrent fetch.

---

## Background

C1–C3 classify nodes by workload occupancy (empty / daemon-only / utilization %). C4 and C5 add policy-level context from the NodePool:

| ID | Signal | Source |
|----|--------|--------|
| C4 | `spec.disruption.consolidationPolicy` — WhenEmpty vs WhenEmptyOrUnderutilized | NodePool CRD |
| C5 | `spec.disruption.consolidateAfter` — configured timer duration | NodePool CRD |

**C4 is operationally critical.** A `normal`-class node under a `WhenEmpty` policy will never be targeted by Karpenter, regardless of utilization. Without this signal, operators may investigate "why isn't Karpenter consolidating this node?" when the answer is simply the pool policy. This is surfaced as a `[skip]` flag in the POLICY column.

---

## Decisions

- **NodePools fetched as unstructured** — same pattern as NodeClaims. No Karpenter module import needed.
- **NodePools fetched concurrently** — added as a fifth goroutine in `FetchWithOptions`, alongside the existing four. No new roundtrips on the critical path.
- **`buildNodePoolInfoMap` in `nodepool.go`** — new pure function keyed by NodePool name. Follows the same pattern as `buildNodePoolMap` in `blocker.go`. Nothing in `blocker.go` imports it directly; it is called in `Analyze()`.
- **`NodePoolPolicy` and `ConsolidateAfter` fields on `NodeResult`** — empty string when the node's pool is `"unknown"` or not present in the map. Callers can distinguish "no pool resolved" from "pool exists but no disruption config" but the printer treats both as `"—"`.
- **POLICY column** — new column after CONSOLIDATION. `WhenEmpty` is shown verbatim; `WhenEmptyOrUnderutilized` is abbreviated to `WhenUnderutilized` for column width. `consolidateAfter` is shown in parentheses: `WhenEmpty (30s)`. Missing policy shows `"—"`.
- **`[skip]` mismatch flag** — when `NodePoolPolicy=WhenEmpty` and `ConsolidationClass=normal` (node has non-daemon workloads), the POLICY column appends ` [skip]` to signal that Karpenter will not target this node for consolidation regardless of utilization.
- **C5 shows configured duration only** — `consolidateAfter` from the NodePool spec. "Has the timer elapsed?" requires NodeClaim condition timestamps and is deferred to a future enhancement.
- **`Analyze()` refactored slightly** — the draining and non-draining branches now share a single block for setting consolidation fields and NodePool info, eliminating duplicated assignments.

---

## Data Model

### `internal/cluster/fetch.go`

New GVR:
```go
var nodepoolGVR = schema.GroupVersionResource{
    Group:    "karpenter.sh",
    Version:  "v1",
    Resource: "nodepools",
}
```

New field on `ClusterData`:
```go
type ClusterData struct {
    ClusterName string
    Nodes       []corev1.Node
    NodeClaims  []unstructured.Unstructured
    NodePools   []unstructured.Unstructured  // NEW
    Pods        []corev1.Pod
    PDBs        []policyv1.PodDisruptionBudget
}
```

### `internal/analyzer/nodepool.go` (new file)

```go
type NodePoolInfo struct {
    ConsolidationPolicy string // "WhenEmpty" | "WhenEmptyOrUnderutilized" | ""
    ConsolidateAfter    string // duration string e.g. "30s" | "Never" | ""
}

func buildNodePoolInfoMap(nodePools []unstructured.Unstructured) map[string]NodePoolInfo
```

Extraction path: `spec.disruption.consolidationPolicy` and `spec.disruption.consolidateAfter`.

### `internal/analyzer/blocker.go`

Two new fields on `NodeResult`:
```go
type NodeResult struct {
    NodeName            string
    NodePool            string
    Status              NodeStatus
    Blockers            []BlockReason
    ConsolidationClass  string
    CPURequestFraction  float64
    MemRequestFraction  float64
    NodePoolPolicy      string  // "WhenEmpty" | "WhenEmptyOrUnderutilized" | ""  NEW
    ConsolidateAfter    string  // e.g. "30s" | "Never" | ""                      NEW
}
```

---

## Printer

`formatPolicy` renders the POLICY column:

```
WhenEmpty               → "WhenEmpty"
WhenEmpty + 30s         → "WhenEmpty (30s)"
WhenEmpty + normal class → "WhenEmpty [skip]"
WhenEmpty + 30s + normal → "WhenEmpty (30s) [skip]"
WhenEmptyOrUnderutilized → "WhenUnderutilized"
WhenEmptyOrUnderutilized + 30s → "WhenUnderutilized (30s)"
""                      → "—"
```

Example output:
```
STATUS    NODE                    NODEPOOL   CONSOLIDATION       POLICY                   BLOCKERS
BLOCKED   ip-10-0-1-5.ec2...     default    34% cpu / 18% mem   WhenUnderutilized (30s)  PDB: my-pdb (prod)
READY     ip-10-0-1-9.ec2...     spot       empty               WhenEmpty (30s)          —
READY     ip-10-0-1-12.ec2...    default    daemon-only         WhenEmpty                —
READY     ip-10-0-1-15.ec2...    default    22% cpu / 10% mem   WhenEmpty [skip]         —
READY     ip-10-0-1-18.ec2...    on-demand  45% cpu / 30% mem   —                        —
```

---

## Testing

### `internal/cluster/fetch_test.go`
- Update `nodeclaimScheme()` to register NodePool and NodePoolList unstructured types.
- Add `TestFetch_NodePoolsIncluded`: create a fake NodePool object, assert `ClusterData.NodePools` contains it.
- Update `TestFetch_ReturnsAllResourceTypes` to assert `len(data.NodePools) == 0`.

### `internal/analyzer/nodepool_test.go` (new file)

| Scenario | Input | Expected |
|----------|-------|---------|
| WhenEmpty policy | NodePool `consolidationPolicy: WhenEmpty` | `NodePoolInfo{ConsolidationPolicy: "WhenEmpty", ConsolidateAfter: ""}` |
| WhenEmptyOrUnderutilized + timer | `consolidationPolicy: WhenEmptyOrUnderutilized`, `consolidateAfter: 30s` | both fields populated |
| consolidateAfter Never | `consolidateAfter: Never` | `ConsolidateAfter: "Never"` |
| Nil/empty input | nil slice | empty map |
| Missing disruption spec | NodePool with no `spec.disruption` | zero-value `NodePoolInfo` in map |
| Unnamed NodePool | no name set | skipped — not in map |

### `internal/analyzer/blocker_test.go`
- `TestAnalyze_NodePoolPolicyPopulated` — node with nodepool label, matching NodePool → fields populated.
- `TestAnalyze_UnknownNodePool_NoPolicy` — no nodepool label, no NodePool → empty strings.
- `TestAnalyze_DrainingNode_PolicyPopulated` — draining node with NodePool → policy still populated.

### `internal/printer/printer_test.go`
- `TestFormatPolicy_WhenEmpty`
- `TestFormatPolicy_WhenEmptyOrUnderutilized`
- `TestFormatPolicy_WithConsolidateAfter`
- `TestFormatPolicy_NoPolicyKnown`
- `TestFormatPolicy_WhenEmpty_NormalClass_ShowsSkip`
- `TestFormatPolicy_WhenEmpty_EmptyClass_NoSkip`
- `TestPrint_PolicyColumn` — integration test covering all four display cases in one `Print` call.

---

## Files Changed

| File | Change |
|------|--------|
| `internal/cluster/fetch.go` | Add `nodepoolGVR`, `NodePools` field on `ClusterData`, fifth goroutine in `FetchWithOptions` |
| `internal/cluster/fetch_test.go` | Update `nodeclaimScheme()`, add `TestFetch_NodePoolsIncluded`, update `TestFetch_ReturnsAllResourceTypes` |
| `internal/analyzer/nodepool.go` | New file — `NodePoolInfo`, `buildNodePoolInfoMap` |
| `internal/analyzer/nodepool_test.go` | New file — 6 unit tests |
| `internal/analyzer/blocker.go` | Add `NodePoolPolicy`, `ConsolidateAfter` to `NodeResult`; refactor `Analyze()` loop; call `buildNodePoolInfoMap` |
| `internal/analyzer/blocker_test.go` | 3 new integration tests |
| `internal/printer/printer.go` | Add `formatPolicy`, POLICY column in width calc and row format |
| `internal/printer/printer_test.go` | 6 unit tests + 1 integration test |
