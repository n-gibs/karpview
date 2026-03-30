package cluster

import (
	"os"
	"testing"
)

// minimalKubeconfig returns a kubeconfig with two contexts pointing at a
// non-existent server (fine — we only test context/cluster name resolution,
// not actual API calls).
func minimalKubeconfig(t *testing.T) string {
	t.Helper()
	content := `apiVersion: v1
kind: Config
current-context: ctx-a
contexts:
- name: ctx-a
  context:
    cluster: cluster-alpha
    user: user-a
- name: ctx-b
  context:
    cluster: cluster-beta
    user: user-b
clusters:
- name: cluster-alpha
  cluster:
    server: https://fake-alpha:6443
- name: cluster-beta
  cluster:
    server: https://fake-beta:6443
users:
- name: user-a
  user: {}
- name: user-b
  user: {}
`
	f, err := os.CreateTemp(t.TempDir(), "kubeconfig-*.yaml")
	if err != nil {
		t.Fatalf("creating temp kubeconfig: %v", err)
	}
	if _, err := f.WriteString(content); err != nil {
		t.Fatalf("writing temp kubeconfig: %v", err)
	}
	f.Close()
	return f.Name()
}

func TestNew_DefaultContext_UsesCurrentContext(t *testing.T) {
	kc := minimalKubeconfig(t)
	t.Setenv("KUBECONFIG", kc)

	clients, err := New("")
	if err != nil {
		t.Fatalf("New(\"\") failed: %v", err)
	}
	if clients.ClusterName != "cluster-alpha" {
		t.Errorf("expected 'cluster-alpha', got %q", clients.ClusterName)
	}
}

func TestNew_ContextOverride_UsesSpecifiedContext(t *testing.T) {
	kc := minimalKubeconfig(t)
	t.Setenv("KUBECONFIG", kc)

	clients, err := New("ctx-b")
	if err != nil {
		t.Fatalf("New(\"ctx-b\") failed: %v", err)
	}
	if clients.ClusterName != "cluster-beta" {
		t.Errorf("expected 'cluster-beta', got %q", clients.ClusterName)
	}
}

func TestNew_UnknownContext_ReturnsError(t *testing.T) {
	kc := minimalKubeconfig(t)
	t.Setenv("KUBECONFIG", kc)

	_, err := New("ctx-does-not-exist")
	if err == nil {
		t.Fatal("expected error for unknown context, got nil")
	}
}
