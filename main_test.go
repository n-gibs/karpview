package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/nikgibson/karpview/internal/cluster"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// fakeFetcher is a test double that returns pre-configured cluster data.
type fakeFetcher struct {
	data *cluster.ClusterData
	err  error
}

func (f *fakeFetcher) Fetch(_ context.Context) (*cluster.ClusterData, error) {
	return f.data, f.err
}

func TestRun_AllConsolidatable_ExitsZero(t *testing.T) {
	fetcher := &fakeFetcher{
		data: &cluster.ClusterData{
			ClusterName: "test-cluster",
			Nodes: []corev1.Node{
				{ObjectMeta: metav1.ObjectMeta{Name: "node-1"}},
			},
		},
	}

	var stdout, stderr bytes.Buffer
	code := run([]string{}, &stdout, &stderr, fetcher)

	if code != 0 {
		t.Errorf("expected exit 0, got %d (stderr: %s)", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "test-cluster") {
		t.Errorf("expected cluster name in output; got: %s", stdout.String())
	}
}

func TestRun_BlockedNode_ExitsOne(t *testing.T) {
	fetcher := &fakeFetcher{
		data: &cluster.ClusterData{
			Nodes: []corev1.Node{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:        "node-1",
						Annotations: map[string]string{"karpenter.sh/do-not-disrupt": "true"},
					},
				},
			},
		},
	}

	var stdout, stderr bytes.Buffer
	code := run([]string{}, &stdout, &stderr, fetcher)

	if code != 1 {
		t.Errorf("expected exit 1, got %d", code)
	}
}

func TestRun_FetchError_ExitsTwo(t *testing.T) {
	fetcher := &fakeFetcher{err: fmt.Errorf("api unavailable")}

	var stdout, stderr bytes.Buffer
	code := run([]string{}, &stdout, &stderr, fetcher)

	if code != 2 {
		t.Errorf("expected exit 2, got %d", code)
	}
	if !strings.Contains(stderr.String(), "api unavailable") {
		t.Errorf("expected error message in stderr; got: %s", stderr.String())
	}
}

func TestRun_VersionFlag_PrintsVersionAndExitsZero(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := run([]string{"-version"}, &stdout, &stderr, nil)

	if code != 0 {
		t.Errorf("expected exit 0, got %d", code)
	}
	if !strings.Contains(stdout.String(), "dev") {
		t.Errorf("expected version string in output; got: %s", stdout.String())
	}
}

func TestRun_NoNodes_ExitsZero(t *testing.T) {
	fetcher := &fakeFetcher{data: &cluster.ClusterData{}}

	var stdout, stderr bytes.Buffer
	code := run([]string{}, &stdout, &stderr, fetcher)

	if code != 0 {
		t.Errorf("expected exit 0, got %d", code)
	}
}

func TestRun_JSONOutput_ValidJSON(t *testing.T) {
	fetcher := &fakeFetcher{
		data: &cluster.ClusterData{
			ClusterName: "test-cluster",
			Nodes: []corev1.Node{
				{ObjectMeta: metav1.ObjectMeta{Name: "node-1"}},
				{ObjectMeta: metav1.ObjectMeta{
					Name:        "node-2",
					Annotations: map[string]string{"karpenter.sh/do-not-disrupt": "true"},
				}},
			},
		},
	}

	var stdout, stderr bytes.Buffer
	code := run([]string{"-o", "json"}, &stdout, &stderr, fetcher)

	if code != exitBlocked {
		t.Errorf("expected exit %d, got %d", exitBlocked, code)
	}

	var nodes []jsonNode
	if err := json.Unmarshal(stdout.Bytes(), &nodes); err != nil {
		t.Fatalf("invalid JSON output: %v\nraw: %s", err, stdout.String())
	}
	if len(nodes) != 2 {
		t.Fatalf("expected 2 nodes, got %d", len(nodes))
	}

	// Verify the blocked node has a blocker
	var found bool
	for _, n := range nodes {
		if n.NodeName == "node-2" && n.Status == "BLOCKED" && len(n.Blockers) > 0 {
			found = true
		}
	}
	if !found {
		t.Error("expected node-2 to be BLOCKED with blockers in JSON output")
	}
}

func TestRun_JSONOutput_EmptyNodes(t *testing.T) {
	fetcher := &fakeFetcher{data: &cluster.ClusterData{}}

	var stdout, stderr bytes.Buffer
	code := run([]string{"--output", "json"}, &stdout, &stderr, fetcher)

	if code != exitOK {
		t.Errorf("expected exit 0, got %d", code)
	}

	var nodes []jsonNode
	if err := json.Unmarshal(stdout.Bytes(), &nodes); err != nil {
		t.Fatalf("invalid JSON output: %v\nraw: %s", err, stdout.String())
	}
	if len(nodes) != 0 {
		t.Errorf("expected 0 nodes, got %d", len(nodes))
	}
}

func TestRun_JSONOutput_TimingOnStderr(t *testing.T) {
	fetcher := &fakeFetcher{
		data: &cluster.ClusterData{
			Nodes: []corev1.Node{{ObjectMeta: metav1.ObjectMeta{Name: "node-1"}}},
		},
	}

	var stdout, stderr bytes.Buffer
	run([]string{"-o", "json"}, &stdout, &stderr, fetcher)

	// Phase timing should be on stderr, not in JSON stdout
	if !strings.Contains(stderr.String(), "fetch=") {
		t.Error("expected timing info on stderr")
	}
	// stdout should be pure JSON (no timing)
	if strings.Contains(stdout.String(), "fetch=") {
		t.Error("timing info should not appear in stdout JSON")
	}
}

func TestRun_InvalidOutputFormat_ExitsTwo(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := run([]string{"-o", "yaml"}, &stdout, &stderr, nil)

	if code != exitError {
		t.Errorf("expected exit %d, got %d", exitError, code)
	}
	if !strings.Contains(stderr.String(), "unsupported output format") {
		t.Errorf("expected error message about unsupported format; got: %s", stderr.String())
	}
}
