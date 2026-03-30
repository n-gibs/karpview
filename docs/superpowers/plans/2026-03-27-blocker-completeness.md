# Blocker Completeness (B1, B2, B3) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add `DRAINING` status, terminating-pod blockers, and PDB pod attribution to KarpView's disruption analysis.

**Architecture:** Approach B — `isDraining()` pre-check in `Analyze()` short-circuits before blocker analysis for nodes already being drained. B2 (terminating pods) and B3 (PDB attribution) extend `analyzeNode()` inline. Printer gains a third color (yellow), updated sort, and updated footer.

**Tech Stack:** Go 1.21+, `k8s.io/api/core/v1`, `k8s.io/api/policy/v1`, `golang.org/x/term`

---

## File Map

| File | Change |
|------|--------|
| `internal/analyzer/blocker.go` | Add `StatusDraining`, `BlockReasonTerminating`, `PodName` to `BlockReason`, add `isDraining()`, update `Analyze()` loop, extend `analyzeNode()` |
| `internal/analyzer/blocker_test.go` | 9 new tests covering B1, B2, B3, exit code |
| `internal/printer/printer.go` | Add yellow color, `DRAINING` formatting, `statusRank()`, updated sort, updated footer, PDB pod attribution, terminating reason |
| `internal/printer/printer_test.go` | 5 new tests covering DRAINING display, sort, footer, reason formatting |
| `main.go` | Add `NodesDraining` to `logRecord`, count draining nodes in result loop, update stderr text line |

---

## Task 1: Data model — extend `BlockReason` and add new constants

**Files:**
- Modify: `internal/analyzer/blocker.go`

- [ ] **Step 1: Add `StatusDraining` constant**

In `blocker.go`, in the `const` block that defines `StatusBlocked`, `StatusConsolidatable`, `StatusUnknown`, add:

```go
const (
	StatusBlocked        NodeStatus = "BLOCKED"
	StatusConsolidatable NodeStatus = "READY"
	StatusDraining       NodeStatus = "DRAINING"
	// StatusUnknown is a zero-value sentinel. analyzeNode never produces it;
	// it exists as a defensive declaration for callers that construct
	// NodeResult values without going through Analyze.
	StatusUnknown NodeStatus = "UNKNOWN"
)
```

- [ ] **Step 2: Add `BlockReasonTerminating` constant**

In the block that defines `BlockReasonPDB` and `BlockReasonAnnotation`, add:

```go
const (
	BlockReasonPDB        = "PDB"
	BlockReasonAnnotation = "Annotation"
	BlockReasonTerminating = "Terminating"
)
```

- [ ] **Step 3: Add `PodName` field to `BlockReason`**

Replace the existing `BlockReason` struct:

```go
// BlockReason describes why a node is blocked from consolidation.
type BlockReason struct {
	Type      string // BlockReasonPDB | BlockReasonAnnotation | BlockReasonTerminating
	Name      string // PDB name, annotation key, or terminating pod name
	Namespace string // e.g. "prod"
	PodName   string // PDB blockers only — the pod whose labels matched the PDB selector
}
```

- [ ] **Step 4: Verify the build compiles**

```bash
cd /Users/nikgibson/karpview && go build ./...
```

Expected: no output (clean build). If `formatReason` in printer references `BlockReason` fields, it will still compile — `PodName` is additive.

---

## Task 2: `isDraining` helper — TDD

**Files:**
- Modify: `internal/analyzer/blocker_test.go`
- Modify: `internal/analyzer/blocker.go`

- [ ] **Step 1: Write three failing tests**

Add to `blocker_test.go` after the existing helper functions:

```go
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
```

- [ ] **Step 2: Run tests — verify they fail**

```bash
cd /Users/nikgibson/karpview && go test ./internal/analyzer/ -run TestIsDraining -v
```

Expected: `FAIL` — `isDraining` undefined.

- [ ] **Step 3: Implement `isDraining`**

Add to `blocker.go` (after the `compilePDBSelectors` function):

```go
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
```

- [ ] **Step 4: Run tests — verify they pass**

```bash
cd /Users/nikgibson/karpview && go test ./internal/analyzer/ -run TestIsDraining -v
```

Expected: all three `PASS`.

---

## Task 3: `Analyze()` early-exit for draining nodes — TDD

**Files:**
- Modify: `internal/analyzer/blocker_test.go`
- Modify: `internal/analyzer/blocker.go`

- [ ] **Step 1: Write two failing tests**

Add to `blocker_test.go`:

```go
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
```

- [ ] **Step 2: Run tests — verify they fail**

```bash
cd /Users/nikgibson/karpview && go test ./internal/analyzer/ -run TestAnalyze_Draining -v
```

Expected: `FAIL` — `StatusDraining` compiles but `Analyze()` doesn't emit it yet.

- [ ] **Step 3: Update `Analyze()` with the early-exit loop**

Replace the existing results loop in `Analyze()`:

```go
results := make([]NodeResult, 0, len(data.Nodes))
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
	result := analyzeNode(node, nodePoolMap, podsByNode, pdbEntries)
	results = append(results, result)
}
```

- [ ] **Step 4: Run all analyzer tests — verify they pass**

```bash
cd /Users/nikgibson/karpview && go test ./internal/analyzer/ -v
```

Expected: all existing tests plus the two new ones `PASS`.

---

## Task 4: B2 — terminating pod blocker — TDD

**Files:**
- Modify: `internal/analyzer/blocker_test.go`
- Modify: `internal/analyzer/blocker.go`

- [ ] **Step 1: Write two failing tests**

Add to `blocker_test.go`:

```go
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
```

- [ ] **Step 2: Run tests — verify they fail**

```bash
cd /Users/nikgibson/karpview && go test ./internal/analyzer/ -run TestAnalyze_Terminating -v
```

Expected: `FAIL` — terminating pod check not yet implemented.

- [ ] **Step 3: Add terminating pod check to `analyzeNode`**

In `analyzeNode`, after the existing pod `do-not-disrupt` annotation check and before the PDB loop, add:

```go
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
```

- [ ] **Step 4: Run all analyzer tests — verify they pass**

```bash
cd /Users/nikgibson/karpview && go test ./internal/analyzer/ -v
```

Expected: all tests `PASS`.

---

## Task 5: B3 — PDB pod attribution + exit code test — TDD

**Files:**
- Modify: `internal/analyzer/blocker_test.go`
- Modify: `internal/analyzer/blocker.go`

- [ ] **Step 1: Write two failing tests**

Add to `blocker_test.go`:

```go
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

func TestExitCode_DrainingOnly_ReturnsZero(t *testing.T) {
	results := []NodeResult{
		{Status: StatusDraining},
		{Status: StatusDraining},
	}
	if got := ExitCode(results); got != 0 {
		t.Errorf("expected 0 for all-draining results, got %d", got)
	}
}
```

- [ ] **Step 2: Run tests — verify they fail**

```bash
cd /Users/nikgibson/karpview && go test ./internal/analyzer/ -run "TestAnalyze_PDB_RecordsPodName|TestExitCode_DrainingOnly" -v
```

Expected: `TestAnalyze_PDB_RecordsPodName` fails (PodName empty), `TestExitCode_DrainingOnly` passes (ExitCode already ignores non-BLOCKED statuses).

- [ ] **Step 3: Add `PodName` to PDB blocker in `analyzeNode`**

In `analyzeNode`, find the PDB matching block and add `PodName`:

```go
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
				break
			}
		}
	}
```

- [ ] **Step 4: Run all analyzer tests — verify they pass**

```bash
cd /Users/nikgibson/karpview && go test ./internal/analyzer/ -v
```

Expected: all tests `PASS`.

- [ ] **Step 5: Run the benchmark to confirm no regression**

```bash
cd /Users/nikgibson/karpview && go test ./internal/analyzer/ -bench=BenchmarkAnalyze -benchmem -count=3
```

Expected: time/op within 10% of baseline (~10.63 ms/op), allocs/op within 10% (~6,062). The draining path short-circuits before `analyzeNode` — no regression expected.

---

## Task 6: Printer — `DRAINING` status, color, `statusRank` sort — TDD

**Files:**
- Modify: `internal/printer/printer_test.go`
- Modify: `internal/printer/printer.go`

- [ ] **Step 1: Write failing tests**

Add to `printer_test.go`:

```go
func TestPrint_SortOrder_BlockedDrainingReady(t *testing.T) {
	results := []analyzer.NodeResult{
		{NodeName: "node-ready", NodePool: "default", Status: analyzer.StatusConsolidatable},
		{NodeName: "node-draining", NodePool: "default", Status: analyzer.StatusDraining},
		{NodeName: "node-blocked", NodePool: "default", Status: analyzer.StatusBlocked,
			Blockers: []analyzer.BlockReason{{Type: "Annotation", Name: "karpenter.sh/do-not-disrupt"}}},
	}
	var buf strings.Builder
	Print(&buf, "test-cluster", results)
	out := buf.String()

	blockedIdx := strings.Index(out, "node-blocked")
	drainingIdx := strings.Index(out, "node-draining")
	readyIdx := strings.Index(out, "node-ready")

	if blockedIdx == -1 || drainingIdx == -1 || readyIdx == -1 {
		t.Fatalf("not all nodes appear in output:\n%s", out)
	}
	if !(blockedIdx < drainingIdx && drainingIdx < readyIdx) {
		t.Errorf("expected BLOCKED < DRAINING < READY order, got blocked=%d draining=%d ready=%d",
			blockedIdx, drainingIdx, readyIdx)
	}
}

func TestPrint_Footer_IncludesDrainingCount(t *testing.T) {
	results := []analyzer.NodeResult{
		{NodeName: "node-blocked", NodePool: "default", Status: analyzer.StatusBlocked,
			Blockers: []analyzer.BlockReason{{Type: "Annotation", Name: "karpenter.sh/do-not-disrupt"}}},
		{NodeName: "node-draining", NodePool: "default", Status: analyzer.StatusDraining},
		{NodeName: "node-ready", NodePool: "default", Status: analyzer.StatusConsolidatable},
	}
	var buf strings.Builder
	Print(&buf, "test-cluster", results)
	out := buf.String()

	if !strings.Contains(out, "1 node(s) blocked, 1 draining / 3 total") {
		t.Errorf("footer missing draining count; got:\n%s", out)
	}
}

func TestFormatStatus_Draining_NoColor(t *testing.T) {
	got := formatStatus(analyzer.StatusDraining, false)
	if got != "DRAINING" {
		t.Errorf("expected DRAINING, got %q", got)
	}
}
```

- [ ] **Step 2: Run tests — verify they fail**

```bash
cd /Users/nikgibson/karpview && go test ./internal/printer/ -run "TestPrint_SortOrder|TestPrint_Footer|TestFormatStatus_Draining" -v
```

Expected: `FAIL`.

- [ ] **Step 3: Add yellow color constant and `statusRank` helper**

In `printer.go`, add the color constant alongside the existing ones:

```go
const (
	colorRed    = "\033[31m"
	colorGreen  = "\033[32m"
	colorYellow = "\033[33m"
	colorReset  = "\033[0m"
)
```

Add `statusRank` as a package-level unexported function:

```go
// statusRank controls print order: lower rank prints first.
// BLOCKED=0, DRAINING=1, READY/UNKNOWN=2.
func statusRank(s analyzer.NodeStatus) int {
	switch s {
	case analyzer.StatusBlocked:
		return 0
	case analyzer.StatusDraining:
		return 1
	default:
		return 2
	}
}
```

- [ ] **Step 4: Update `formatStatus` to handle `StatusDraining`**

Replace the existing `formatStatus` function:

```go
// formatStatus returns the status string, optionally with ANSI color codes.
func formatStatus(s analyzer.NodeStatus, color bool) string {
	switch s {
	case analyzer.StatusBlocked:
		if color {
			return fmt.Sprintf("%s%-8s%s", colorRed, s, colorReset)
		}
		return fmt.Sprintf("%-8s", s)
	case analyzer.StatusDraining:
		if color {
			return fmt.Sprintf("%s%-8s%s", colorYellow, s, colorReset)
		}
		return fmt.Sprintf("%-8s", s)
	case analyzer.StatusConsolidatable:
		if color {
			return fmt.Sprintf("%s%-8s%s", colorGreen, s, colorReset)
		}
		return fmt.Sprintf("%-8s", s)
	default:
		return fmt.Sprintf("%-8s", s)
	}
}
```

Note: width changes from `%-7s` to `%-8s` because `DRAINING` is 8 chars. This keeps all status columns aligned.

- [ ] **Step 5: Update the sort in `Print` to use `statusRank`**

Replace the existing `sort.SliceStable` call in `Print`:

```go
sort.SliceStable(results, func(i, j int) bool {
	ri, rj := statusRank(results[i].Status), statusRank(results[j].Status)
	if ri != rj {
		return ri < rj
	}
	return results[i].NodeName < results[j].NodeName
})
```

- [ ] **Step 6: Update the footer and blocked counter in `Print`**

Replace the existing blocked-count loop and footer line in `Print`:

```go
	blockedCount := 0
	drainingCount := 0
	for _, r := range results {
		status := formatStatus(r.Status, color)
		reason := formatReason(r)

		fmt.Fprintf(w, "%s  %-*s   NodePool: %-*s   %s\n",
			status,
			maxName, sanitize(r.NodeName),
			maxPool, sanitize(r.NodePool),
			reason,
		)
		switch r.Status {
		case analyzer.StatusBlocked:
			blockedCount++
		case analyzer.StatusDraining:
			drainingCount++
		}
	}
	fmt.Fprintf(w, "\n%d node(s) blocked, %d draining / %d total\n\n",
		blockedCount, drainingCount, len(results))
```

- [ ] **Step 7: Run printer tests — verify they pass**

```bash
cd /Users/nikgibson/karpview && go test ./internal/printer/ -v
```

Expected: all tests `PASS`.

---

## Task 7: Printer — reason formatting for DRAINING, Terminating, PDB attribution — TDD

**Files:**
- Modify: `internal/printer/printer_test.go`
- Modify: `internal/printer/printer.go`

- [ ] **Step 1: Write failing tests**

Add to `printer_test.go`:

```go
func TestFormatReason_Draining(t *testing.T) {
	r := analyzer.NodeResult{Status: analyzer.StatusDraining}
	got := formatReason(r)
	if got != "Disruption in progress" {
		t.Errorf("expected 'Disruption in progress', got %q", got)
	}
}

func TestFormatReason_PDB_IncludesPodName(t *testing.T) {
	r := analyzer.NodeResult{
		Status: analyzer.StatusBlocked,
		Blockers: []analyzer.BlockReason{
			{Type: "PDB", Name: "payments-pdb", Namespace: "prod", PodName: "app-pod-xyz"},
		},
	}
	got := formatReason(r)
	want := "PDB: payments-pdb (prod) via app-pod-xyz"
	if got != want {
		t.Errorf("expected %q, got %q", want, got)
	}
}

func TestFormatReason_Terminating(t *testing.T) {
	r := analyzer.NodeResult{
		Status: analyzer.StatusBlocked,
		Blockers: []analyzer.BlockReason{
			{Type: "Terminating", Name: "stuck-pod", Namespace: "prod"},
		},
	}
	got := formatReason(r)
	want := "Terminating: stuck-pod (prod)"
	if got != want {
		t.Errorf("expected %q, got %q", want, got)
	}
}
```

- [ ] **Step 2: Run tests — verify they fail**

```bash
cd /Users/nikgibson/karpview && go test ./internal/printer/ -run "TestFormatReason" -v
```

Expected: `TestFormatReason_Draining` and `TestFormatReason_Terminating` fail, `TestFormatReason_PDB_IncludesPodName` fails (no pod name in output yet).

- [ ] **Step 3: Update `formatReason`**

Replace the existing `formatReason` function:

```go
// formatReason returns a human-readable reason string for a node result.
func formatReason(r analyzer.NodeResult) string {
	switch r.Status {
	case analyzer.StatusConsolidatable:
		return "Consolidatable"
	case analyzer.StatusDraining:
		return "Disruption in progress"
	case analyzer.StatusUnknown:
		return "Unknown"
	}

	parts := make([]string, 0, len(r.Blockers))
	for _, b := range r.Blockers {
		switch b.Type {
		case "PDB":
			if b.PodName != "" {
				parts = append(parts, fmt.Sprintf("PDB: %s (%s) via %s",
					sanitize(b.Name), sanitize(b.Namespace), sanitize(b.PodName)))
			} else {
				parts = append(parts, fmt.Sprintf("PDB: %s (%s)",
					sanitize(b.Name), sanitize(b.Namespace)))
			}
		case "Terminating":
			parts = append(parts, fmt.Sprintf("Terminating: %s (%s)",
				sanitize(b.Name), sanitize(b.Namespace)))
		case "Annotation":
			parts = append(parts, fmt.Sprintf("Annotation: %s", sanitize(b.Name)))
		default:
			parts = append(parts, fmt.Sprintf("%s: %s", sanitize(b.Type), sanitize(b.Name)))
		}
	}
	return strings.Join(parts, ", ")
}
```

- [ ] **Step 4: Run all printer tests — verify they pass**

```bash
cd /Users/nikgibson/karpview && go test ./internal/printer/ -v
```

Expected: all tests `PASS`.

---

## Task 8: `main.go` — draining count in log output

**Files:**
- Modify: `main.go`

- [ ] **Step 1: Add `NodesDraining` to `logRecord`**

In the `logRecord` struct, add the draining field alongside `NodesBlocked`:

```go
type logRecord struct {
	Ts                  string `json:"ts"`
	Level               string `json:"level"`
	Msg                 string `json:"msg"`
	Version             string `json:"version"`
	Cluster             string `json:"cluster,omitempty"`
	PhaseFetchMs        int64  `json:"phase_fetch_ms"`
	PhaseAnalyzeMs      int64  `json:"phase_analyze_ms"`
	NodesTotal          int    `json:"nodes_total"`
	NodesBlocked        int    `json:"nodes_blocked"`
	NodesDraining       int    `json:"nodes_draining"`
	NodesConsolidatable int    `json:"nodes_consolidatable"`
	ExitCode            int    `json:"exit_code"`
	RunID               string `json:"run_id"`
	Error               string `json:"error,omitempty"`
}
```

- [ ] **Step 2: Update the result-counting loop in `run()`**

Replace the existing `blocked` counter loop:

```go
	blocked := 0
	draining := 0
	for _, r := range results {
		switch r.Status {
		case analyzer.StatusBlocked:
			blocked++
		case analyzer.StatusDraining:
			draining++
		}
	}
```

- [ ] **Step 3: Update the `writeLog` call with draining count**

Replace the existing `writeLog` call at the end of `run()`:

```go
	writeLog(stderr, jsonLog, logRecord{
		Ts:                  time.Now().UTC().Format(time.RFC3339Nano),
		Level:               "info",
		Msg:                 "run complete",
		Version:             version,
		Cluster:             data.ClusterName,
		PhaseFetchMs:        fetchDur.Milliseconds(),
		PhaseAnalyzeMs:      analyzeDur.Milliseconds(),
		NodesTotal:          len(data.Nodes),
		NodesBlocked:        blocked,
		NodesDraining:       draining,
		NodesConsolidatable: len(data.Nodes) - blocked - draining,
		ExitCode:            finalCode,
		RunID:               rid,
	}, fmt.Sprintf("fetch=%s analyze=%s nodes=%d blocked=%d draining=%d\n",
		fetchDur.Round(time.Millisecond),
		analyzeDur.Round(time.Millisecond),
		len(data.Nodes),
		blocked,
		draining,
	))
```

- [ ] **Step 4: Build and run all tests**

```bash
cd /Users/nikgibson/karpview && go build ./... && go test ./...
```

Expected: clean build, all tests `PASS`.

- [ ] **Step 5: Update the JSON output for `jsonBlocker`**

In `main.go`, add `PodName` to `jsonBlocker`:

```go
type jsonBlocker struct {
	Type      string `json:"type"`
	Name      string `json:"name"`
	Namespace string `json:"namespace,omitempty"`
	PodName   string `json:"podName,omitempty"`
}
```

Update `printJSON` to populate `PodName`:

```go
for j, b := range r.Blockers {
	blockers[j] = jsonBlocker{
		Type:      b.Type,
		Name:      b.Name,
		Namespace: b.Namespace,
		PodName:   b.PodName,
	}
}
```

- [ ] **Step 6: Final build and full test run**

```bash
cd /Users/nikgibson/karpview && go build ./... && go test ./... -v
```

Expected: clean build, all tests `PASS` including the 14 new tests across analyzer and printer packages.
