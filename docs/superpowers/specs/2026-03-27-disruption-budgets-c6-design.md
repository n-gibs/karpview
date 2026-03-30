# KarpView — Disruption Budgets Design (C6)

**Date:** 2026-03-27
**Status:** Approved
**Scope:** Adds NodePool disruption budget evaluation to KarpView output. Surfaces per-reason headroom and budget-blocking state in a new BUDGET column. No new API calls — NodePools already fetched in C4/C5. Adds `robfig/cron/v3` for schedule window evaluation.

---

## Background

NodePool `spec.disruption.budgets` rate-limits how many nodes Karpenter may voluntarily disrupt at once. A pool can have multiple budgets, each scoped to specific disruption reasons (`Empty`, `Underutilized`, `Drifted`) with either an always-active node count/percentage or a cron-based time window.

Without C6, KarpView shows a node as `READY` with no blockers, and the operator has no way to tell whether Karpenter is simply rate-limited by the pool budget right now.

| ID | Signal | Source |
|----|--------|--------|
| C6 | NodePool disruption budget headroom per reason; schedule window state | NodePool CRD (`spec.disruption.budgets`) |

**Relationship to existing signals:**
- `do-not-disrupt` (pod/node annotation) — already surfaces as a BLOCKER. Nodes with this annotation count toward pool total but not toward deleting. Budget evaluation is unaffected.
- A node can be simultaneously BLOCKED (PDB / do-not-disrupt) and budget-blocked. Both signals appear in their respective columns.

---

## Decisions

- **`robfig/cron/v3` added as dependency** — handles standard 5-field cron and macros (`@daily`, `@weekly`, `@monthly`, `@hourly`, `@yearly`). No other new dependencies.
- **Schedule window evaluation** — `prev = schedule.Next(now - duration)`. Window is active if `prev <= now`. `now` is injected so all logic is purely testable.
- **Reason mapping from consolidation class** — `empty` and `daemon-only` both map to reason `"Empty"` (Karpenter treats them identically). `normal` maps to `"Underutilized"`. `"Drifted"` is computed separately (H3, future work) — not derived from consolidation class.
- **Default budget** — if `spec.disruption.budgets` is absent or empty, Karpenter applies `nodes: 10%` implicitly. KarpView evaluates and displays this as `default 10% (N/M avail)` rather than `—`.
- **Strictest budget displayed** — when multiple budgets apply to a reason, take the minimum headroom. Show the most relevant reason for this node first, other reasons after.
- **Policy interaction** — if `consolidationPolicy: WhenEmpty`, `U:` (Underutilized) is omitted from the BUDGET display since that reason will never be targeted by Karpenter for this pool. `D:` (Drifted) is always shown regardless of policy since drift is independent of consolidation policy.
- **Pre-formatted display string** — `evaluateBudgets()` returns a `display string` that `NodeResult.BudgetDisplay` stores. The printer writes it directly, consistent with the existing pattern for pre-formatted fields.
- **`budget.go` is a pure package** — no imports from `blocker.go` internals. `Analyze()` in `blocker.go` computes `poolStats` and calls `evaluateBudgets()`, then stamps results onto `NodeResult`.
- **Future drill-down** — a `karpview budgets` subcommand (or `--budget-detail` flag) showing full per-reason budget breakdown per NodePool is deferred to a separate ticket.

---

## Data Model

### `internal/analyzer/nodepool.go`

Extended `NodePoolInfo`:
```go
type NodePoolInfo struct {
    ConsolidationPolicy string
    ConsolidateAfter    string
    Budgets             []DisruptionBudget  // NEW
}

type DisruptionBudget struct {
    Nodes    string   // "20%" or "5" or "0"
    Reasons  []string // nil = applies to all reasons
    Schedule string   // "" | "@daily" | "0 9 * * 1-5"
    Duration string   // "" | "10m" | "1h30m"
}
```

Extraction path: `spec.disruption.budgets[]` — each entry: `.nodes`, `.reasons[]`, `.schedule`, `.duration`.

### `internal/analyzer/budget.go` (new file)

```go
type poolStats struct {
    Total    int  // all nodes in pool
    Deleting int  // DeletionTimestamp != nil
    NotReady int  // Ready condition = False or Unknown
}

// evaluateBudgets returns headroom, blocked state, and a pre-formatted display string.
// reason is the lead reason for this node (derived from consolidation class).
// policy is used to omit U: when consolidationPolicy=WhenEmpty.
// now is injected for testability.
func evaluateBudgets(budgets []DisruptionBudget, reason string, policy string, stats poolStats, now time.Time) (headroom int, blocked bool, display string)
```

Internal helpers (unexported):
- `budgetAppliesToReason(b DisruptionBudget, reason string) bool`
- `resolveNodes(nodes string, total int) int` — parses "20%" or "5" into an integer
- `scheduleWindowActive(schedule, duration string, now time.Time) bool`
- `formatBudgetDisplay(results []reasonResult, leadReason string) string`

### `internal/analyzer/blocker.go`

Two new fields on `NodeResult`:
```go
type NodeResult struct {
    // ... existing fields ...
    BudgetDisplay string  // pre-formatted BUDGET column value
    BudgetBlocked bool    // true if effective headroom <= 0
}
```

`Analyze()` additions:
1. Build `map[string]poolStats` from `data.Nodes` — keyed by NodePool name. Count total, deleting (`DeletionTimestamp != nil`), NotReady (Ready condition False/Unknown).
2. For each node: resolve its reason from `ConsolidationClass`, call `evaluateBudgets()` with reason + `NodePoolPolicy`, populate `BudgetDisplay` and `BudgetBlocked`.

---

## Display

`formatBudgetDisplay` output rules:

| Situation | Display |
|-----------|---------|
| No budgets configured (default) | `default 10% (1/10 avail)` |
| Simple always-active, not blocked | `20% (8/10 avail)` |
| Simple always-active, blocked | `20% [BLOCKED]` |
| Per-reason, node's reason first | `U:[BLOCKED] E:5/10` |
| Schedule budget, window inactive | `@daily/10m [inactive]` |
| Schedule budget, window active + blocked | `@daily/10m [BLOCKED]` |
| Policy=WhenEmpty, normal node | `E:5/10 D:8/10` (U: omitted) |
| NodePool unresolvable | `—` |

Node's own reason always appears first. Remaining reasons follow in order: E, U, D.

**Full output example:**
```
STATUS   NODE                   NODEPOOL   CONSOLIDATION     POLICY                   BUDGET                    BLOCKERS
BLOCKED  ip-10-0-1-5.ec2...     default    34% cpu/18% mem   WhenUnderutilized (30s)  U:[BLOCKED] E:5/10        PDB: my-pdb (prod)
READY    ip-10-0-1-9.ec2...     spot       empty             WhenEmpty (30s)          E:3/8                     —
READY    ip-10-0-1-12.ec2...    default    daemon-only       WhenEmpty                E:[BLOCKED] D:2/8         —
READY    ip-10-0-1-15.ec2...    default    22% cpu/10% mem   WhenEmpty [skip]         E:3/8                     —
READY    ip-10-0-1-18.ec2...    on-demand  45% cpu/30% mem   —                        default 10% (1/10 avail)  —
READY    ip-10-0-1-21.ec2...    batch      empty             WhenEmpty                @daily/10m [inactive]     —
```

---

## Testing

### `internal/analyzer/budget_test.go` (new file)

| Scenario | Input | Expected |
|----------|-------|----------|
| Always-active %, not blocked | `nodes: "20%"`, total=10, deleting=0, notready=0 | headroom=2, blocked=false |
| Always-active %, blocked | `nodes: "20%"`, total=10, deleting=2, notready=0 | headroom=0, blocked=true |
| Static nodes, blocked | `nodes: "1"`, total=10, deleting=1 | headroom=0, blocked=true |
| Reason filter — reason matches | budget `reasons: ["Empty"]`, reason="Empty" | applies |
| Reason filter — reason excluded | budget `reasons: ["Drifted"]`, reason="Empty" | skipped |
| No reasons on budget | budget `reasons: nil`, reason="Underutilized" | applies to all |
| Schedule inactive | `schedule: "@daily"`, `duration: "10m"`, now=01:00 UTC | window inactive |
| Schedule active | `schedule: "@daily"`, `duration: "10m"`, now=00:05 UTC | window active |
| `@weekly` macro parsed | `schedule: "@weekly"` | no error, correct next time |
| Multiple budgets — most restrictive wins | budgets: `[nodes:"5", nodes:"2"]` | headroom uses 2 |
| Default budget (empty slice) | `budgets: nil`, total=10 | `default 10% (1/10 avail)` |
| Nodes="0" (disable disruption) | `nodes: "0"`, any total | headroom=0, blocked=true |

### `internal/analyzer/blocker_test.go`

- `TestAnalyze_BudgetPopulated` — node in pool with simple budget → `BudgetDisplay` and `BudgetBlocked` populated correctly.
- `TestAnalyze_BudgetUnknownPool` — node with no NodePool resolved → `BudgetDisplay = "—"`.
- `TestAnalyze_BudgetPolicyWhenEmpty_OmitsU` — pool with `WhenEmpty` + normal-class node → U: omitted from display.

### `internal/printer/printer_test.go`

- `TestPrint_BudgetColumn` — integration test: Print call with nodes covering all display cases produces correct BUDGET column values.

---

## Files Changed

| File | Change |
|------|--------|
| `go.mod` / `go.sum` | Add `robfig/cron/v3` |
| `internal/analyzer/nodepool.go` | Add `DisruptionBudget` struct; extend `NodePoolInfo`; extend `buildNodePoolInfoMap` to parse `spec.disruption.budgets` |
| `internal/analyzer/budget.go` | New file — `poolStats`, `evaluateBudgets()`, internal helpers, display formatting |
| `internal/analyzer/budget_test.go` | New file — 12 unit tests |
| `internal/analyzer/blocker.go` | Build `poolStats` map in `Analyze()`; call `evaluateBudgets()` per node; populate `BudgetDisplay`, `BudgetBlocked` on `NodeResult` |
| `internal/analyzer/blocker_test.go` | 3 new integration tests |
| `internal/printer/printer.go` | Add BUDGET column — width calc, header, row format writing `r.BudgetDisplay` |
| `internal/printer/printer_test.go` | 1 integration test |
