package cluster

import (
	"fmt"
	"os"

	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
)

// Clients holds the Kubernetes API clients and cluster metadata.
type Clients struct {
	Kubernetes  kubernetes.Interface
	Dynamic     dynamic.Interface
	ClusterName string
}

// New creates Kubernetes clients from the default kubeconfig loading rules.
// Kubeconfig is resolved in order: KUBECONFIG env var, ~/.kube/config,
// in-cluster service account token (when running inside a Pod).
// contextOverride selects a specific kubeconfig context; empty string uses the current context.
func New(contextOverride string) (*Clients, error) {
	rules := clientcmd.NewDefaultClientConfigLoadingRules()
	overrides := &clientcmd.ConfigOverrides{}
	if contextOverride != "" {
		overrides.CurrentContext = contextOverride
	}
	loader := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(rules, overrides)

	config, err := loader.ClientConfig()
	if err != nil {
		return nil, fmt.Errorf("loading kubeconfig: %w", err)
	}
	if config.Insecure {
		fmt.Fprintln(os.Stderr, "warning: TLS verification disabled for this cluster")
	}

	rawConfig, err := loader.RawConfig()
	if err != nil {
		return nil, fmt.Errorf("loading raw kubeconfig: %w", err)
	}

	activeContext := rawConfig.CurrentContext
	if contextOverride != "" {
		activeContext = contextOverride
	}
	clusterName := activeContext
	if ctx, ok := rawConfig.Contexts[activeContext]; ok && ctx.Cluster != "" {
		clusterName = ctx.Cluster
	}

	k8s, err := kubernetes.NewForConfig(config)
	if err != nil {
		return nil, fmt.Errorf("creating kubernetes client: %w", err)
	}

	dyn, err := dynamic.NewForConfig(config)
	if err != nil {
		return nil, fmt.Errorf("creating dynamic client: %w", err)
	}

	return &Clients{
		Kubernetes:  k8s,
		Dynamic:     dyn,
		ClusterName: clusterName,
	}, nil
}
