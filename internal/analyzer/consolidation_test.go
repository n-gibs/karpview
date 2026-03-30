package analyzer

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// helpers

func runningPod(name, nodeName string, ownerKind string, cpuReq, memReq string) *corev1.Pod {
	p := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec: corev1.PodSpec{
			NodeName: nodeName,
			Containers: []corev1.Container{
				{
					Resources: corev1.ResourceRequirements{
						Requests: corev1.ResourceList{},
					},
				},
			},
		},
		Status: corev1.PodStatus{Phase: corev1.PodRunning},
	}
	if cpuReq != "" {
		p.Spec.Containers[0].Resources.Requests[corev1.ResourceCPU] = resource.MustParse(cpuReq)
	}
	if memReq != "" {
		p.Spec.Containers[0].Resources.Requests[corev1.ResourceMemory] = resource.MustParse(memReq)
	}
	if ownerKind != "" {
		p.OwnerReferences = []metav1.OwnerReference{{Kind: ownerKind}}
	}
	return p
}

func allocatableNode(cpu, mem string) *corev1.Node {
	return &corev1.Node{
		Status: corev1.NodeStatus{
			Allocatable: corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse(cpu),
				corev1.ResourceMemory: resource.MustParse(mem),
			},
		},
	}
}

// tests

func TestClassifyNode_NoPods_ReturnsEmpty(t *testing.T) {
	n := allocatableNode("2", "4Gi")
	got := classifyNode(n, nil)
	if got.Class != ConsolidationEmpty {
		t.Errorf("expected empty, got %q", got.Class)
	}
	if got.CPURequestFraction != 0 || got.MemRequestFraction != 0 {
		t.Errorf("expected 0.0 fractions, got cpu=%f mem=%f", got.CPURequestFraction, got.MemRequestFraction)
	}
}

func TestClassifyNode_DaemonPodsOnly_ReturnsDaemonOnly(t *testing.T) {
	n := allocatableNode("2", "4Gi")
	pods := []*corev1.Pod{
		runningPod("ds-1", "node-a", "DaemonSet", "100m", "128Mi"),
		runningPod("ds-2", "node-a", "DaemonSet", "100m", "128Mi"),
	}
	got := classifyNode(n, pods)
	if got.Class != ConsolidationDaemonOnly {
		t.Errorf("expected daemon-only, got %q", got.Class)
	}
	if got.CPURequestFraction != 0 || got.MemRequestFraction != 0 {
		t.Errorf("expected 0.0 fractions, got cpu=%f mem=%f", got.CPURequestFraction, got.MemRequestFraction)
	}
}

func TestClassifyNode_DaemonPluWorkload_ReturnsNormal(t *testing.T) {
	n := allocatableNode("2", "4Gi")
	pods := []*corev1.Pod{
		runningPod("ds-1", "node-a", "DaemonSet", "100m", "128Mi"),
		runningPod("app-1", "node-a", "ReplicaSet", "500m", "256Mi"),
	}
	got := classifyNode(n, pods)
	if got.Class != ConsolidationNormal {
		t.Errorf("expected normal, got %q", got.Class)
	}
}

func TestClassifyNode_SucceededPodOnly_ReturnsEmpty(t *testing.T) {
	n := allocatableNode("2", "4Gi")
	p := runningPod("job-1", "node-a", "Job", "500m", "256Mi")
	p.Status.Phase = corev1.PodSucceeded
	got := classifyNode(n, []*corev1.Pod{p})
	if got.Class != ConsolidationEmpty {
		t.Errorf("expected empty (succeeded pod filtered), got %q", got.Class)
	}
}

func TestClassifyNode_WorkloadWithRequests_ComputesFraction(t *testing.T) {
	// 500m / 2000m = 0.25 cpu; 256Mi / 1Gi = 0.25 mem
	n := allocatableNode("2000m", "1Gi")
	pods := []*corev1.Pod{
		runningPod("app-1", "node-a", "ReplicaSet", "500m", "256Mi"),
	}
	got := classifyNode(n, pods)
	if got.Class != ConsolidationNormal {
		t.Errorf("expected normal, got %q", got.Class)
	}
	if abs(got.CPURequestFraction-0.25) > 0.001 {
		t.Errorf("expected cpu fraction 0.25, got %f", got.CPURequestFraction)
	}
	if abs(got.MemRequestFraction-0.25) > 0.001 {
		t.Errorf("expected mem fraction 0.25, got %f", got.MemRequestFraction)
	}
}

func TestClassifyNode_ZeroAllocatable_ReturnsFractionZero(t *testing.T) {
	n := &corev1.Node{} // no Allocatable set
	pods := []*corev1.Pod{
		runningPod("app-1", "node-a", "ReplicaSet", "500m", "256Mi"),
	}
	got := classifyNode(n, pods)
	if got.CPURequestFraction != 0 || got.MemRequestFraction != 0 {
		t.Errorf("expected 0.0 fractions on zero-allocatable node, got cpu=%f mem=%f",
			got.CPURequestFraction, got.MemRequestFraction)
	}
}

func TestClassifyNode_NoResourceRequests_ReturnsNormalWithZeroFraction(t *testing.T) {
	n := allocatableNode("2", "4Gi")
	p := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "app-1"},
		Spec:       corev1.PodSpec{NodeName: "node-a"},
		Status:     corev1.PodStatus{Phase: corev1.PodRunning},
	}
	got := classifyNode(n, []*corev1.Pod{p})
	if got.Class != ConsolidationNormal {
		t.Errorf("expected normal, got %q", got.Class)
	}
	if got.CPURequestFraction != 0 || got.MemRequestFraction != 0 {
		t.Errorf("expected 0.0 fractions, got cpu=%f mem=%f", got.CPURequestFraction, got.MemRequestFraction)
	}
}

func abs(f float64) float64 {
	if f < 0 {
		return -f
	}
	return f
}
