package cluster

import (
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	"golang.org/x/sync/errgroup"
	corev1 "k8s.io/api/core/v1"
	policyv1 "k8s.io/api/policy/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
)

// karpenterGVR is the GroupVersionResource for Karpenter NodeClaims.
// Requires Karpenter >= 1.0 (karpenter.sh/v1 API).
var karpenterGVR = schema.GroupVersionResource{
	Group:    "karpenter.sh",
	Version:  "v1",
	Resource: "nodeclaims",
}

// nodepoolGVR is the GroupVersionResource for Karpenter NodePools.
var nodepoolGVR = schema.GroupVersionResource{
	Group:    "karpenter.sh",
	Version:  "v1",
	Resource: "nodepools",
}

// podPageSize is the maximum number of pods fetched per API request.
const podPageSize = 500

// ClusterData holds the raw Kubernetes resources needed for analysis.
type ClusterData struct {
	ClusterName string
	Nodes       []corev1.Node
	// NodeClaims are fetched as unstructured because karpenter.sh types are
	// not a direct dependency; using unstructured avoids importing the full
	// Karpenter module while still allowing label and status field access.
	NodeClaims []unstructured.Unstructured
	NodePools  []unstructured.Unstructured
	Pods       []corev1.Pod
	PDBs       []policyv1.PodDisruptionBudget
}

// PodProgressFunc is called after each page of pods is fetched during
// pagination. pageNum is 1-indexed and cumulativePods is the running total.
type PodProgressFunc func(pageNum, cumulativePods int)

// FetchOptions configures optional behavior for Fetch.
type FetchOptions struct {
	// VerboseWriter, when non-nil, receives progress messages on stderr.
	VerboseWriter io.Writer
}

// Fetcher is implemented by any value that can retrieve cluster data.
// *Clients satisfies this interface; callers can substitute a test double.
type Fetcher interface {
	Fetch(ctx context.Context) (*ClusterData, error)
}

// retryBackoffs defines the sleep durations between retry attempts.
var retryBackoffs = []time.Duration{500 * time.Millisecond, 1 * time.Second, 2 * time.Second}

// isTransientError returns true for errors worth retrying.
func isTransientError(err error) bool {
	if err == nil {
		return false
	}
	// Check k8s API status errors for transient HTTP codes.
	if statusErr, ok := err.(*apierrors.StatusError); ok {
		code := statusErr.Status().Code
		switch code {
		case 429, 500, 502, 503, 504:
			return true
		}
	}
	// Check error strings for network-level transient errors.
	msg := err.Error()
	for _, substr := range []string{"connection reset", "EOF", "connection refused", "i/o timeout"} {
		if strings.Contains(msg, substr) {
			return true
		}
	}
	return false
}

// withRetry executes fn up to len(retryBackoffs)+1 times, sleeping between
// attempts only when fn returns a transient error. Non-transient errors are
// returned immediately.
func withRetry(ctx context.Context, fn func() error) error {
	var err error
	for attempt := 0; attempt <= len(retryBackoffs); attempt++ {
		err = fn()
		if err == nil {
			return nil
		}
		if !isTransientError(err) {
			return err
		}
		if attempt < len(retryBackoffs) {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(retryBackoffs[attempt]):
			}
		}
	}
	return fmt.Errorf("after %d attempts: %w", len(retryBackoffs)+1, err)
}

// Fetch implements Fetcher on *Clients.
func (c *Clients) Fetch(ctx context.Context) (*ClusterData, error) {
	data, err := Fetch(ctx, c)
	if err != nil {
		return nil, err
	}
	data.ClusterName = c.ClusterName
	return data, nil
}

// FetchWithOptions implements fetching with configurable options such as
// verbose progress output.
func FetchWithOptions(ctx context.Context, c *Clients, opts *FetchOptions) (*ClusterData, error) {
	if opts == nil {
		opts = &FetchOptions{}
	}

	g, ctx := errgroup.WithContext(ctx)

	var (
		nodes      []corev1.Node
		nodeClaims []unstructured.Unstructured
		nodePools  []unstructured.Unstructured
		pods       []corev1.Pod
		pdbs       []policyv1.PodDisruptionBudget
	)

	g.Go(func() error {
		return withRetry(ctx, func() error {
			list, err := c.Kubernetes.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
			if err != nil {
				return fmt.Errorf("listing nodes: %w", err)
			}
			nodes = list.Items
			return nil
		})
	})

	g.Go(func() error {
		return withRetry(ctx, func() error {
			list, err := c.Dynamic.Resource(karpenterGVR).List(ctx, metav1.ListOptions{})
			if err != nil {
				return fmt.Errorf("listing nodeclaims: %w", err)
			}
			nodeClaims = list.Items
			return nil
		})
	})

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

	g.Go(func() error {
		var progress PodProgressFunc
		if opts.VerboseWriter != nil {
			progress = func(pageNum, cumulativePods int) {
				fmt.Fprintf(opts.VerboseWriter, "fetched %d pods...\n", cumulativePods)
			}
		}
		var err error
		pods, err = listAllPods(ctx, c, progress)
		return err
	})

	g.Go(func() error {
		return withRetry(ctx, func() error {
			list, err := c.Kubernetes.PolicyV1().PodDisruptionBudgets("").List(ctx, metav1.ListOptions{})
			if err != nil {
				return fmt.Errorf("listing pdbs: %w", err)
			}
			pdbs = list.Items
			return nil
		})
	})

	if err := g.Wait(); err != nil {
		return nil, err
	}

	return &ClusterData{
		Nodes:      nodes,
		NodeClaims: nodeClaims,
		NodePools:  nodePools,
		Pods:       pods,
		PDBs:       pdbs,
	}, nil
}

// Fetch retrieves Nodes, NodeClaims, Pods, and PDBs from the cluster
// using concurrent API calls to minimize wall-clock latency.
func Fetch(ctx context.Context, c *Clients) (*ClusterData, error) {
	return FetchWithOptions(ctx, c, nil)
}

// listAllPods fetches all pods cluster-wide using pagination to handle
// large clusters without hitting API server memory limits.
// progress, when non-nil, is called after each page is fetched.
func listAllPods(ctx context.Context, c *Clients, progress PodProgressFunc) ([]corev1.Pod, error) {
	var all []corev1.Pod
	opts := metav1.ListOptions{Limit: podPageSize}
	pageNum := 0
	for {
		var page *corev1.PodList
		err := withRetry(ctx, func() error {
			var listErr error
			page, listErr = c.Kubernetes.CoreV1().Pods("").List(ctx, opts)
			if listErr != nil {
				return fmt.Errorf("listing pods: %w", listErr)
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
		all = append(all, page.Items...)
		pageNum++
		if progress != nil {
			progress(pageNum, len(all))
		}
		if page.Continue == "" {
			break
		}
		opts.Continue = page.Continue
	}
	return all, nil
}
