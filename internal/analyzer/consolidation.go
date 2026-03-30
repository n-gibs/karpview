package analyzer

import (
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
)

const (
	ConsolidationEmpty      = "empty"
	ConsolidationDaemonOnly = "daemon-only"
	ConsolidationNormal     = "normal"
)

// NodeConsolidation holds the consolidation classification and utilization
// fractions for a single node.
type NodeConsolidation struct {
	Class              string
	CPURequestFraction float64
	MemRequestFraction float64
}

// classifyNode computes the consolidation class and non-daemon workload
// utilization for a node. Only Running pods are counted; Succeeded/Failed
// pods retain spec.nodeName but must not influence classification.
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

// isDaemonPod returns true when the pod is owned by a DaemonSet.
func isDaemonPod(pod *corev1.Pod) bool {
	for _, ref := range pod.OwnerReferences {
		if ref.Kind == "DaemonSet" {
			return true
		}
	}
	return false
}

// safeFraction returns used/total, or 0 when total is zero.
func safeFraction(used, total int64) float64 {
	if total == 0 {
		return 0
	}
	return float64(used) / float64(total)
}
