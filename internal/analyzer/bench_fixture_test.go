package analyzer

// bench_fixture_test.go — deterministic fixture generator for benchmark stability.
//
// All benchmark fixtures MUST be constructed via newLargeClusterFixture so that
// the data is identical between the main-branch baseline run and the PR run.
// Using a seeded PRNG (source=42) guarantees reproducibility without committing
// large binary testdata files.

import (
	"fmt"
	"math/rand"

	"github.com/nikgibson/karpview/internal/cluster"
	corev1 "k8s.io/api/core/v1"
	policyv1 "k8s.io/api/policy/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// FixtureConfig controls the scale of a generated benchmark fixture.
// Keep the defaults below in sync with BenchmarkAnalyze_LargeCluster.
type FixtureConfig struct {
	NumNodes    int
	NumPDBs     int
	PodsPerNode int
	// Fraction of nodes that carry the do-not-disrupt annotation (0.0–1.0).
	// Zero means no annotated nodes, which matches the current baseline.
	AnnotatedFraction float64
	// Seed for the PRNG. Must never change once a baseline has been recorded.
	Seed int64
}

// DefaultFixtureConfig is the canonical large-cluster fixture.
// Changing ANY field here invalidates previously recorded baselines.
var DefaultFixtureConfig = FixtureConfig{
	NumNodes:          500,
	NumPDBs:           50,
	PodsPerNode:       20,
	AnnotatedFraction: 0.0,
	Seed:              42,
}

// newLargeClusterFixture builds a deterministic *cluster.ClusterData from cfg.
// Every call with the same cfg produces byte-identical object graphs.
func newLargeClusterFixture(cfg FixtureConfig) *cluster.ClusterData {
	//nolint:gosec // weak rand is intentional — this is test fixture generation only
	rng := rand.New(rand.NewSource(cfg.Seed))

	nodes := make([]corev1.Node, cfg.NumNodes)
	for i := range nodes {
		annotations := map[string]string(nil)
		if cfg.AnnotatedFraction > 0 && rng.Float64() < cfg.AnnotatedFraction {
			annotations = map[string]string{"karpenter.sh/do-not-disrupt": "true"}
		}
		nodes[i] = corev1.Node{
			ObjectMeta: metav1.ObjectMeta{
				Name:        fmt.Sprintf("node-%d", i),
				Annotations: annotations,
			},
		}
	}

	pods := make([]corev1.Pod, cfg.NumNodes*cfg.PodsPerNode)
	for i := range pods {
		nodeIdx := i / cfg.PodsPerNode
		appLabel := fmt.Sprintf("app-%d", i%cfg.NumPDBs)
		pods[i] = corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:      fmt.Sprintf("pod-%d", i),
				Namespace: "prod",
				Labels:    map[string]string{"app": appLabel},
			},
			Spec: corev1.PodSpec{
				NodeName: fmt.Sprintf("node-%d", nodeIdx),
			},
		}
	}

	pdbs := make([]policyv1.PodDisruptionBudget, cfg.NumPDBs)
	for i := range pdbs {
		pdbs[i] = policyv1.PodDisruptionBudget{
			ObjectMeta: metav1.ObjectMeta{
				Name:      fmt.Sprintf("pdb-%d", i),
				Namespace: "prod",
			},
			Spec: policyv1.PodDisruptionBudgetSpec{
				Selector: &metav1.LabelSelector{
					MatchLabels: map[string]string{"app": fmt.Sprintf("app-%d", i)},
				},
			},
			Status: policyv1.PodDisruptionBudgetStatus{
				DisruptionsAllowed: 0,
				ObservedGeneration: 1,
				ExpectedPods:       int32(cfg.PodsPerNode), //nolint:gosec
			},
		}
	}

	// Optional NodeClaims — none in the default fixture to match the baseline.
	nodeClaims := make([]unstructured.Unstructured, 0)

	_ = rng // consumed above; retained to document intent
	return &cluster.ClusterData{
		Nodes:      nodes,
		Pods:       pods,
		PDBs:       pdbs,
		NodeClaims: nodeClaims,
	}
}
