# karpview

CLI tool that shows which nodes in your cluster are blocked from [Karpenter](https://karpenter.sh/) consolidation and why.

```
Cluster: prod-us-east-1

READY    ip-10-0-1-100.ec2.internal   NodePool: general-purpose   Consolidatable
BLOCKED  ip-10-0-1-101.ec2.internal   NodePool: spot-pool         PDB: payments-pdb (prod), Annotation: karpenter.sh/do-not-disrupt
READY    ip-10-0-1-102.ec2.internal   NodePool: general-purpose   Consolidatable
```

## Requirements

- Go 1.23+ (to build from source)
- Karpenter **>= 1.0** — uses the `karpenter.sh/v1` NodeClaim API
- `kubectl` access to the target cluster with the permissions listed below

## Installation

```bash
go install github.com/nikgibson/karpview@latest
```

Or build from source:

```bash
git clone https://github.com/nikgibson/karpview
cd karpview
go build -o karpview .
```

## Usage

```
karpview [flags]
```

| Flag | Default | Description |
|------|---------|-------------|
| `-context` | current context | kubeconfig context to use |
| `-timeout` | 30s | timeout for Kubernetes API calls |

### Examples

```bash
# Use the current kubeconfig context
karpview

# Target a specific context
karpview -context prod-us-east-1

# Extend the timeout for slow API servers
karpview -timeout 60s
```

## Exit codes

| Code | Meaning |
|------|---------|
| `0` | All nodes are consolidatable (or no nodes found) |
| `1` | One or more nodes are blocked from consolidation |

This makes `karpview` suitable for use in CI/CD pipelines and scripts:

```bash
karpview || echo "Consolidation is blocked — check output above"
```

## What blocks consolidation

| Blocker | Description |
|---------|-------------|
| `Annotation` on node | Node has `karpenter.sh/do-not-disrupt: "true"` |
| `Annotation` on pod | A pod on the node has `karpenter.sh/do-not-disrupt: "true"` |
| `PDB` | A PodDisruptionBudget with `DisruptionsAllowed: 0` covers a pod on the node |

PDBs that have not yet been reconciled by the disruption controller (`ObservedGeneration == 0`) and PDBs with a nil selector are intentionally ignored to avoid false positives.

## RBAC

Apply the bundled manifest to create a least-privilege ServiceAccount:

```bash
kubectl apply -f deploy/rbac.yaml
```

This creates a `karpview` ServiceAccount in the `default` namespace bound to a ClusterRole with `list` on:
- `nodes`, `pods` (core API)
- `poddisruptionbudgets` (policy API)
- `nodeclaims` (karpenter.sh/v1)

To run karpview as a one-off Job using that ServiceAccount:

```bash
kubectl run karpview --image=ghcr.io/nikgibson/karpview:latest \
  --restart=Never --serviceaccount=karpview -- karpview
```

## How it works

1. Fetches Nodes, NodeClaims, Pods, and PDBs concurrently from the cluster API.
2. For each node, checks:
   - Node-level `karpenter.sh/do-not-disrupt` annotation
   - Pod-level `karpenter.sh/do-not-disrupt` annotation (any pod scheduled on the node)
   - PDBs whose selector matches a pod on the node and `DisruptionsAllowed == 0`
3. Resolves the NodePool name from the NodeClaim `status.nodeName` field, falling back to the `karpenter.sh/nodepool` node label.
