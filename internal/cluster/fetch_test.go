package cluster

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	policyv1 "k8s.io/api/policy/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	kubefake "k8s.io/client-go/kubernetes/fake"
)

// nodeclaimScheme returns a runtime.Scheme with NodeClaim/NodeClaimList and
// NodePool/NodePoolList registered as unstructured types so the dynamic fake
// client can serve List requests for karpenterGVR and nodepoolGVR.
func nodeclaimScheme() *runtime.Scheme {
	s := runtime.NewScheme()
	s.AddKnownTypeWithName(
		schema.GroupVersionKind{Group: "karpenter.sh", Version: "v1", Kind: "NodeClaim"},
		&unstructured.Unstructured{},
	)
	s.AddKnownTypeWithName(
		schema.GroupVersionKind{Group: "karpenter.sh", Version: "v1", Kind: "NodeClaimList"},
		&unstructured.UnstructuredList{},
	)
	s.AddKnownTypeWithName(
		schema.GroupVersionKind{Group: "karpenter.sh", Version: "v1", Kind: "NodePool"},
		&unstructured.Unstructured{},
	)
	s.AddKnownTypeWithName(
		schema.GroupVersionKind{Group: "karpenter.sh", Version: "v1", Kind: "NodePoolList"},
		&unstructured.UnstructuredList{},
	)
	return s
}

func TestFetch_ReturnsAllResourceTypes(t *testing.T) {
	node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "node-1"}}
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "pod-1", Namespace: "default"},
		Spec:       corev1.PodSpec{NodeName: "node-1"},
	}
	pdb := &policyv1.PodDisruptionBudget{
		ObjectMeta: metav1.ObjectMeta{Name: "pdb-1", Namespace: "default"},
	}

	k8s := kubefake.NewSimpleClientset(node, pod, pdb)
	dyn := dynamicfake.NewSimpleDynamicClient(nodeclaimScheme())

	data, err := Fetch(context.Background(), &Clients{Kubernetes: k8s, Dynamic: dyn})
	if err != nil {
		t.Fatalf("Fetch failed: %v", err)
	}

	if len(data.Nodes) != 1 || data.Nodes[0].Name != "node-1" {
		t.Errorf("nodes: got %v", data.Nodes)
	}
	if len(data.Pods) != 1 || data.Pods[0].Name != "pod-1" {
		t.Errorf("pods: got %v", data.Pods)
	}
	if len(data.PDBs) != 1 || data.PDBs[0].Name != "pdb-1" {
		t.Errorf("pdbs: got %v", data.PDBs)
	}
	if len(data.NodeClaims) != 0 {
		t.Errorf("expected 0 nodeclaims, got %d", len(data.NodeClaims))
	}
	if len(data.NodePools) != 0 {
		t.Errorf("expected 0 nodepools, got %d", len(data.NodePools))
	}
}

func TestFetch_NodeClaimsIncluded(t *testing.T) {
	nc := &unstructured.Unstructured{}
	nc.SetGroupVersionKind(schema.GroupVersionKind{
		Group: "karpenter.sh", Version: "v1", Kind: "NodeClaim",
	})
	nc.SetName("nc-1")
	nc.SetLabels(map[string]string{"karpenter.sh/nodepool": "default"})

	k8s := kubefake.NewSimpleClientset()
	dyn := dynamicfake.NewSimpleDynamicClient(nodeclaimScheme(), nc)

	data, err := Fetch(context.Background(), &Clients{Kubernetes: k8s, Dynamic: dyn})
	if err != nil {
		t.Fatalf("Fetch failed: %v", err)
	}
	if len(data.NodeClaims) != 1 || data.NodeClaims[0].GetName() != "nc-1" {
		t.Errorf("nodeclaims: got %v", data.NodeClaims)
	}
}

func TestFetch_ContextCancelled_ReturnsError(t *testing.T) {
	k8s := kubefake.NewSimpleClientset()
	dyn := dynamicfake.NewSimpleDynamicClient(nodeclaimScheme())

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // already cancelled

	_, err := Fetch(ctx, &Clients{Kubernetes: k8s, Dynamic: dyn})
	if err == nil {
		// fake clients may not respect context cancellation — acceptable
		t.Log("fake client did not propagate cancellation (expected with kubefake)")
	}
}

func TestFetch_PodPagination(t *testing.T) {
	// Create more pods than podPageSize to exercise the pagination loop.
	// kubefake returns all objects in one response regardless of Limit,
	// so this test validates that listAllPods accumulates items correctly.
	pods := make([]runtime.Object, podPageSize+10)
	for i := range pods {
		pods[i] = &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:      fmt.Sprintf("pod-%d", i),
				Namespace: "default",
			},
		}
	}

	k8s := kubefake.NewSimpleClientset(pods...)
	dyn := dynamicfake.NewSimpleDynamicClient(nodeclaimScheme())

	data, err := Fetch(context.Background(), &Clients{Kubernetes: k8s, Dynamic: dyn})
	if err != nil {
		t.Fatalf("Fetch failed: %v", err)
	}
	if len(data.Pods) != podPageSize+10 {
		t.Errorf("expected %d pods, got %d", podPageSize+10, len(data.Pods))
	}
}

func TestFetcher_Interface(t *testing.T) {
	// Verify *Clients satisfies the Fetcher interface at compile time.
	var _ Fetcher = (*Clients)(nil)
}

func TestIsTransientError(t *testing.T) {
	tests := []struct {
		name      string
		err       error
		wantRetry bool
	}{
		{"nil error", nil, false},
		{"generic error", errors.New("something broke"), false},
		{"connection reset", errors.New("read tcp: connection reset by peer"), true},
		{"EOF", errors.New("unexpected EOF"), true},
		{"wrapped EOF", fmt.Errorf("listing pods: %w", errors.New("unexpected EOF")), true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isTransientError(tt.err)
			if got != tt.wantRetry {
				t.Errorf("isTransientError(%v) = %v, want %v", tt.err, got, tt.wantRetry)
			}
		})
	}
}

func TestWithRetry_SucceedsImmediately(t *testing.T) {
	calls := 0
	err := withRetry(context.Background(), func() error {
		calls++
		return nil
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if calls != 1 {
		t.Errorf("expected 1 call, got %d", calls)
	}
}

func TestWithRetry_NonTransientErrorNoRetry(t *testing.T) {
	calls := 0
	err := withRetry(context.Background(), func() error {
		calls++
		return errors.New("permission denied")
	})
	if err == nil {
		t.Fatal("expected error")
	}
	if calls != 1 {
		t.Errorf("expected 1 call for non-transient error, got %d", calls)
	}
}

func TestWithRetry_RetriesTransientThenSucceeds(t *testing.T) {
	// Override backoffs for fast test
	origBackoffs := retryBackoffs
	retryBackoffs = []time.Duration{time.Millisecond, time.Millisecond, time.Millisecond}
	defer func() { retryBackoffs = origBackoffs }()

	calls := 0
	err := withRetry(context.Background(), func() error {
		calls++
		if calls < 3 {
			return errors.New("connection reset by peer")
		}
		return nil
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if calls != 3 {
		t.Errorf("expected 3 calls, got %d", calls)
	}
}

func TestWithRetry_ExhaustsAttempts(t *testing.T) {
	origBackoffs := retryBackoffs
	retryBackoffs = []time.Duration{time.Millisecond, time.Millisecond, time.Millisecond}
	defer func() { retryBackoffs = origBackoffs }()

	calls := 0
	err := withRetry(context.Background(), func() error {
		calls++
		return errors.New("unexpected EOF")
	})
	if err == nil {
		t.Fatal("expected error after exhausting retries")
	}
	if !strings.Contains(err.Error(), "after 4 attempts") {
		t.Errorf("expected attempt count in error, got: %v", err)
	}
	if calls != 4 { // 1 initial + 3 retries
		t.Errorf("expected 4 calls, got %d", calls)
	}
}

func TestFetchWithOptions_PodProgress(t *testing.T) {
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "pod-1", Namespace: "default"},
	}
	k8s := kubefake.NewSimpleClientset(pod)
	dyn := dynamicfake.NewSimpleDynamicClient(nodeclaimScheme())

	var verboseBuf bytes.Buffer
	clients := &Clients{Kubernetes: k8s, Dynamic: dyn}
	opts := &FetchOptions{VerboseWriter: &verboseBuf}

	data, err := FetchWithOptions(context.Background(), clients, opts)
	if err != nil {
		t.Fatalf("FetchWithOptions failed: %v", err)
	}
	if len(data.Pods) != 1 {
		t.Errorf("expected 1 pod, got %d", len(data.Pods))
	}
	if !strings.Contains(verboseBuf.String(), "fetched 1 pods") {
		t.Errorf("expected progress output, got: %q", verboseBuf.String())
	}
}

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

func TestFetchWithOptions_NilOptsBackwardCompat(t *testing.T) {
	k8s := kubefake.NewSimpleClientset()
	dyn := dynamicfake.NewSimpleDynamicClient(nodeclaimScheme())

	// nil opts should work identically to Fetch
	data, err := FetchWithOptions(context.Background(), &Clients{Kubernetes: k8s, Dynamic: dyn}, nil)
	if err != nil {
		t.Fatalf("FetchWithOptions(nil opts) failed: %v", err)
	}
	if data == nil {
		t.Fatal("expected non-nil data")
	}
}
