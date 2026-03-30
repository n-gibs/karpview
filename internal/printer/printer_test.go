package printer

import (
	"bytes"
	"strings"
	"testing"

	"github.com/nikgibson/karpview/internal/analyzer"
)

func TestPrint_EmptyResults_PrintsNoNodesFound(t *testing.T) {
	var buf bytes.Buffer
	Print(&buf, "test-cluster", []analyzer.NodeResult{})

	out := buf.String()
	if !strings.Contains(out, "No nodes found.") {
		t.Errorf("expected 'No nodes found.' in output; got: %q", out)
	}
	if !strings.Contains(out, "test-cluster") {
		t.Errorf("expected cluster name in output; got: %q", out)
	}
}

func TestPrint_NilResults_PrintsNoNodesFound(t *testing.T) {
	var buf bytes.Buffer
	Print(&buf, "my-cluster", nil)

	out := buf.String()
	if !strings.Contains(out, "No nodes found.") {
		t.Errorf("expected 'No nodes found.'; got: %q", out)
	}
}

func TestPrint_ConsolidatableNode_ShowsReady(t *testing.T) {
	results := []analyzer.NodeResult{
		{NodeName: "node-1", NodePool: "general", Status: analyzer.StatusConsolidatable},
	}
	var buf bytes.Buffer
	Print(&buf, "test-cluster", results)

	out := buf.String()
	if !strings.Contains(out, "node-1") {
		t.Errorf("expected node name in output; got: %q", out)
	}
	if !strings.Contains(out, "—") {
		t.Errorf("expected '—' in output; got: %q", out)
	}
}

func TestPrint_BlockedNode_ShowsPDBBlocker(t *testing.T) {
	results := []analyzer.NodeResult{
		{
			NodeName: "node-1",
			NodePool: "spot",
			Status:   analyzer.StatusBlocked,
			Blockers: []analyzer.BlockReason{
				{Type: analyzer.BlockReasonPDB, Name: "payments-pdb", Namespace: "prod"},
			},
		},
	}
	var buf bytes.Buffer
	Print(&buf, "test-cluster", results)

	out := buf.String()
	if !strings.Contains(out, "payments-pdb") {
		t.Errorf("expected PDB name in output; got: %q", out)
	}
	if !strings.Contains(out, "prod") {
		t.Errorf("expected namespace in output; got: %q", out)
	}
}

func TestPrint_BlockedNode_ShowsAnnotationBlocker(t *testing.T) {
	results := []analyzer.NodeResult{
		{
			NodeName: "node-1",
			NodePool: "general",
			Status:   analyzer.StatusBlocked,
			Blockers: []analyzer.BlockReason{
				{Type: analyzer.BlockReasonAnnotation, Name: "karpenter.sh/do-not-disrupt"},
			},
		},
	}
	var buf bytes.Buffer
	Print(&buf, "test-cluster", results)

	out := buf.String()
	if !strings.Contains(out, "karpenter.sh/do-not-disrupt") {
		t.Errorf("expected annotation name in output; got: %q", out)
	}
}

func TestSanitize_StripsANSISequences(t *testing.T) {
	got := sanitize("\x1b[31mred text\x1b[0m")
	if got != "red text" {
		t.Errorf("expected 'red text', got %q", got)
	}
}

func TestSanitize_StripsNonPrintable(t *testing.T) {
	got := sanitize("hello\x00world\x07")
	if got != "helloworld" {
		t.Errorf("expected 'helloworld', got %q", got)
	}
}

func TestSanitize_PreservesUnicode(t *testing.T) {
	input := "日本語テスト"
	got := sanitize(input)
	if got != input {
		t.Errorf("expected %q, got %q", input, got)
	}
}

func TestSanitize_PreservesNormalASCII(t *testing.T) {
	input := "ip-10-0-1-100.ec2.internal"
	got := sanitize(input)
	if got != input {
		t.Errorf("expected %q unchanged, got %q", input, got)
	}
}

func TestPrint_SortOrder_BlockedDrainingReady(t *testing.T) {
	results := []analyzer.NodeResult{
		{NodeName: "node-ready", NodePool: "default", Status: analyzer.StatusConsolidatable},
		{NodeName: "node-draining", NodePool: "default", Status: analyzer.StatusDraining},
		{NodeName: "node-blocked", NodePool: "default", Status: analyzer.StatusBlocked,
			Blockers: []analyzer.BlockReason{{Type: "Annotation", Name: "karpenter.sh/do-not-disrupt"}}},
	}
	var buf bytes.Buffer
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
	var buf bytes.Buffer
	Print(&buf, "test-cluster", results)
	out := buf.String()

	if !strings.Contains(out, "1 node(s) blocked, 1 draining / 3 total") {
		t.Errorf("footer missing draining count; got:\n%s", out)
	}
}

func TestFormatStatus_Draining_NoColor(t *testing.T) {
	got := formatStatus(analyzer.StatusDraining, false)
	if got != "DRAINING" {
		t.Errorf("expected \"DRAINING\" (8 chars), got %q", got)
	}
}

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

func TestPrint_BudgetColumn(t *testing.T) {
	results := []analyzer.NodeResult{
		{
			NodeName:           "node-a",
			NodePool:           "default",
			Status:             analyzer.StatusConsolidatable,
			ConsolidationClass: analyzer.ConsolidationEmpty,
			NodePoolPolicy:     "WhenEmpty",
			BudgetDisplay:      "20% (2/10 avail)",
		},
		{
			NodeName:           "node-b",
			NodePool:           "batch",
			Status:             analyzer.StatusConsolidatable,
			ConsolidationClass: analyzer.ConsolidationNormal,
			NodePoolPolicy:     "WhenEmptyOrUnderutilized",
			BudgetDisplay:      "U:[BLOCKED] E:2/10",
			BudgetBlocked:      true,
		},
		{
			NodeName:           "node-c",
			NodePool:           "spot",
			Status:             analyzer.StatusConsolidatable,
			ConsolidationClass: analyzer.ConsolidationDaemonOnly,
			BudgetDisplay:      "@daily/10m [inactive]",
		},
		{
			NodeName:           "node-orphan",
			NodePool:           "unknown",
			Status:             analyzer.StatusConsolidatable,
			ConsolidationClass: analyzer.ConsolidationNormal,
			BudgetDisplay:      "—",
		},
	}

	var buf strings.Builder
	Print(&buf, "test-cluster", results)
	out := buf.String()

	for _, want := range []string{
		"20% (2/10 avail)",
		"U:[BLOCKED] E:2/10",
		"@daily/10m [inactive]",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q\ngot:\n%s", want, out)
		}
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
