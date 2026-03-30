# KarpView — In-Cluster Deployment & Optimization

> Covers Helm chart structure, `--namespace` scoping, memory-streaming output,
> CronJob alerting integration, and resource sizing for constrained environments.

---

## 1. Helm Chart Structure

```
helm/karpview/
├── Chart.yaml
├── values.yaml
└── templates/
    ├── _helpers.tpl
    ├── serviceaccount.yaml
    ├── clusterrole.yaml
    ├── clusterrolebinding.yaml
    └── cronjob.yaml
```

### `helm/karpview/Chart.yaml`

```yaml
apiVersion: v2
name: karpview
description: Karpenter consolidation analysis — in-cluster CronJob
type: application
version: 0.1.0
appVersion: "0.1.0"
keywords:
  - karpenter
  - kubernetes
  - consolidation
maintainers:
  - name: nikgibson
```

### `helm/karpview/values.yaml`

```yaml
# Image
image:
  repository: ghcr.io/nikgibson/karpview
  tag: ""          # defaults to .Chart.AppVersion
  pullPolicy: IfNotPresent

# Schedule (UTC). Default: every hour on the hour.
schedule: "0 * * * *"
successfulJobsHistoryLimit: 3
failedJobsHistoryLimit: 3

# karpview flags
karpview:
  timeout: 60s
  outputFormat: json    # always json in-cluster
  namespace: ""         # leave empty for cluster-wide; set to scope a namespace

# Alerting sidecar / wrapper (see section 4)
alerting:
  enabled: false
  slackWebhookSecretName: ""   # name of Secret with key SLACK_WEBHOOK_URL
  minBlockedNodes: 1           # alert when blocked nodes >= this value

# RBAC
serviceAccount:
  create: true
  name: ""
  annotations: {}

# Pod resource requests/limits (see section 5 for sizing table)
resources:
  requests:
    cpu: 50m
    memory: 64Mi
  limits:
    cpu: 200m
    memory: 128Mi

# Pod security
podSecurityContext:
  runAsNonRoot: true
  runAsUser: 65534
  seccompProfile:
    type: RuntimeDefault

containerSecurityContext:
  allowPrivilegeEscalation: false
  readOnlyRootFilesystem: true
  capabilities:
    drop: ["ALL"]

nodeSelector: {}
tolerations: []
affinity: {}
```

### `helm/karpview/templates/serviceaccount.yaml`

```yaml
{{- if .Values.serviceAccount.create }}
apiVersion: v1
kind: ServiceAccount
metadata:
  name: {{ include "karpview.serviceAccountName" . }}
  namespace: {{ .Release.Namespace }}
  labels: {{- include "karpview.labels" . | nindent 4 }}
  {{- with .Values.serviceAccount.annotations }}
  annotations: {{- toYaml . | nindent 4 }}
  {{- end }}
automountServiceAccountToken: true
{{- end }}
```

### `helm/karpview/templates/clusterrole.yaml`

Minimum RBAC — read-only access to the four resource types karpview lists:

```yaml
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  name: {{ include "karpview.fullname" . }}
  labels: {{- include "karpview.labels" . | nindent 4 }}
rules:
  - apiGroups: [""]
    resources: ["nodes"]
    verbs: ["list"]
  - apiGroups: [""]
    resources: ["pods"]
    verbs: ["list"]
  - apiGroups: ["policy"]
    resources: ["poddisruptionbudgets"]
    verbs: ["list"]
  - apiGroups: ["karpenter.sh"]
    resources: ["nodeclaims"]
    verbs: ["list"]
---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRoleBinding
metadata:
  name: {{ include "karpview.fullname" . }}
  labels: {{- include "karpview.labels" . | nindent 4 }}
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: ClusterRole
  name: {{ include "karpview.fullname" . }}
subjects:
  - kind: ServiceAccount
    name: {{ include "karpview.serviceAccountName" . }}
    namespace: {{ .Release.Namespace }}
```

> **Note:** If `values.karpview.namespace` is set, the ClusterRole can be
> downgraded to a namespaced Role + RoleBinding for `pods` and
> `poddisruptionbudgets`. `nodes` and `nodeclaims` are cluster-scoped and
> always require a ClusterRole.

### `helm/karpview/templates/cronjob.yaml`

```yaml
apiVersion: batch/v1
kind: CronJob
metadata:
  name: {{ include "karpview.fullname" . }}
  namespace: {{ .Release.Namespace }}
  labels: {{- include "karpview.labels" . | nindent 4 }}
spec:
  schedule: {{ .Values.schedule | quote }}
  concurrencyPolicy: Forbid
  successfulJobsHistoryLimit: {{ .Values.successfulJobsHistoryLimit }}
  failedJobsHistoryLimit: {{ .Values.failedJobsHistoryLimit }}
  jobTemplate:
    spec:
      backoffLimit: 2
      activeDeadlineSeconds: 300
      template:
        metadata:
          labels: {{- include "karpview.selectorLabels" . | nindent 12 }}
        spec:
          serviceAccountName: {{ include "karpview.serviceAccountName" . }}
          restartPolicy: OnFailure
          securityContext: {{- toYaml .Values.podSecurityContext | nindent 12 }}
          {{- if .Values.alerting.enabled }}
          volumes:
            - name: results
              emptyDir: {}
          {{- end }}
          containers:
            - name: karpview
              image: "{{ .Values.image.repository }}:{{ .Values.image.tag | default .Chart.AppVersion }}"
              imagePullPolicy: {{ .Values.image.pullPolicy }}
              securityContext: {{- toYaml .Values.containerSecurityContext | nindent 16 }}
              resources: {{- toYaml .Values.resources | nindent 16 }}
              args:
                - --output=json
                - --timeout={{ .Values.karpview.timeout }}
                {{- if .Values.karpview.namespace }}
                - --namespace={{ .Values.karpview.namespace }}
                {{- end }}
              {{- if .Values.alerting.enabled }}
              volumeMounts:
                - name: results
                  mountPath: /results
              {{- end }}
          {{- if .Values.alerting.enabled }}
            - name: alerter
              image: curlimages/curl:8.7.1
              securityContext: {{- toYaml .Values.containerSecurityContext | nindent 16 }}
              resources:
                requests: {cpu: 10m, memory: 16Mi}
                limits:  {cpu: 50m, memory: 32Mi}
              env:
                - name: SLACK_WEBHOOK_URL
                  valueFrom:
                    secretKeyRef:
                      name: {{ .Values.alerting.slackWebhookSecretName }}
                      key: SLACK_WEBHOOK_URL
                - name: MIN_BLOCKED_NODES
                  value: {{ .Values.alerting.minBlockedNodes | quote }}
              volumeMounts:
                - name: results
                  mountPath: /results
              command: ["/bin/sh", "-c"]
              args:
                - |
                  # Wait for karpview to write results.json
                  while [ ! -f /results/results.json ]; do sleep 1; done
                  BLOCKED=$(jq '[.[] | select(.status=="BLOCKED")] | length' /results/results.json)
                  if [ "$BLOCKED" -ge "$MIN_BLOCKED_NODES" ]; then
                    curl -sf -X POST "$SLACK_WEBHOOK_URL" \
                      -H 'Content-Type: application/json' \
                      -d "{\"text\":\":warning: KarpView: *${BLOCKED}* node(s) blocked from consolidation. Check cluster logs for details.\"}"
                  fi
          {{- end }}
          {{- with .Values.nodeSelector }}
          nodeSelector: {{- toYaml . | nindent 12 }}
          {{- end }}
          {{- with .Values.tolerations }}
          tolerations: {{- toYaml . | nindent 12 }}
          {{- end }}
          {{- with .Values.affinity }}
          affinity: {{- toYaml . | nindent 12 }}
          {{- end }}
```

---

## 2. `--namespace` Flag — Implementation Plan

### Why it matters in-cluster

In a large multi-tenant cluster with 10,000+ pods, fetching all pods is the
dominant memory and API cost. A `--namespace` flag scopes the pod and PDB list
calls to a single namespace, reducing both. Nodes and NodeClaims remain
cluster-wide (they are not namespaced), but irrelevant pods are never loaded.

### Code changes

**`internal/cluster/fetch.go` — add namespace filtering**

Add a `Namespace` field to `FetchOptions`:

```go
// FetchOptions configures optional behavior for Fetch.
type FetchOptions struct {
    VerboseWriter io.Writer
    // Namespace, when non-empty, restricts pod and PDB fetches to this
    // namespace. Nodes and NodeClaims are always fetched cluster-wide.
    Namespace string
}
```

In `FetchWithOptions`, the pods goroutine becomes:

```go
g.Go(func() error {
    var progress PodProgressFunc
    if opts.VerboseWriter != nil {
        progress = func(pageNum, cumulativePods int) {
            fmt.Fprintf(opts.VerboseWriter, "fetched %d pods...\n", cumulativePods)
        }
    }
    var err error
    pods, err = listAllPods(ctx, c, opts.Namespace, progress)
    return err
})
```

The PDB goroutine changes from `""` (all namespaces) to `opts.Namespace`:

```go
g.Go(func() error {
    return withRetry(ctx, func() error {
        ns := opts.Namespace  // "" = all namespaces (existing behavior)
        list, err := c.Kubernetes.PolicyV1().PodDisruptionBudgets(ns).List(ctx, metav1.ListOptions{})
        if err != nil {
            return fmt.Errorf("listing pdbs: %w", err)
        }
        pdbs = list.Items
        return nil
    })
})
```

Update `listAllPods` signature:

```go
// listAllPods fetches all pods in namespace (empty string = cluster-wide)
// using pagination.
func listAllPods(ctx context.Context, c *Clients, namespace string, progress PodProgressFunc) ([]corev1.Pod, error) {
    var all []corev1.Pod
    opts := metav1.ListOptions{Limit: podPageSize}
    pageNum := 0
    for {
        var page *corev1.PodList
        err := withRetry(ctx, func() error {
            var listErr error
            page, listErr = c.Kubernetes.CoreV1().Pods(namespace).List(ctx, opts)
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
```

Update the `Fetch` wrapper on `*Clients` to thread namespace through:

```go
// The Clients struct gains a Namespace field set from the flag.
type Clients struct {
    Kubernetes    kubernetes.Interface
    Dynamic       dynamic.Interface
    ClusterName   string
    Namespace     string   // "" = cluster-wide
}

func (c *Clients) Fetch(ctx context.Context) (*ClusterData, error) {
    data, err := FetchWithOptions(ctx, c, &FetchOptions{Namespace: c.Namespace})
    if err != nil {
        return nil, err
    }
    data.ClusterName = c.ClusterName
    return data, nil
}
```

**`main.go` — wire the flag**

```go
namespace := fs.String("namespace", "", "restrict pod/PDB fetch to this namespace (default: all namespaces)")
fs.StringVar(namespace, "n", "", "shorthand for --namespace")
```

Then when constructing `Clients`:

```go
clients, err := cluster.New(*kubeContext)
if err != nil {
    fmt.Fprintf(stderr, "error: %v\n", err)
    return exitError
}
clients.Namespace = *namespace
fetcher = clients
```

**`internal/cluster/client.go`** — add the `Namespace` field to the `Clients`
struct (no other change needed there).

### Analyzer behavior

`analyzer.Analyze` is unaffected. Because pods outside the scoped namespace are
never loaded, PDB matching naturally only considers in-scope workloads. A node
may show `READY` even if pods in other namespaces would block it — this is the
intended trade-off for the scoped mode. Document this in help text:

```
--namespace string
    Restrict pod and PDB analysis to this namespace.
    Nodes outside this namespace's workloads may appear READY
    even if other-namespace pods would block them. Use without
    this flag for a full cluster-wide analysis.
```

---

## 3. Memory Optimization — Streaming Output

### Current behavior

`main.go` calls `fetcher.Fetch` (loads all pods into `[]corev1.Pod`), then
`analyzer.Analyze` (builds full `[]NodeResult`), then writes the entire result
set at once. Peak memory = all pods + all results in memory simultaneously.

### Streaming approach

The bottleneck is pods. Nodes, NodeClaims, and PDBs are small. The fetch cannot
easily stream because the per-node analysis needs the complete pod index and PDB
list. However, output streaming (writing each node result as it is analyzed) is
straightforward and eliminates the second full-results buffer.

**Change `analyzer.Analyze` to accept a callback:**

```go
// AnalyzeFunc calls fn for each NodeResult as it is produced.
// This allows callers to stream output without buffering all results.
func AnalyzeFunc(data *cluster.ClusterData, fn func(NodeResult)) {
    if data == nil {
        return
    }
    nodePoolMap := buildNodePoolMap(data.NodeClaims)
    podsByNode := indexPodsByNode(data.Pods)
    pdbEntries := compilePDBSelectors(data.PDBs)

    for i := range data.Nodes {
        fn(analyzeNode(&data.Nodes[i], nodePoolMap, podsByNode, pdbEntries))
    }
}
```

Keep `Analyze` as a thin wrapper for backward compatibility:

```go
func Analyze(data *cluster.ClusterData) []NodeResult {
    var results []NodeResult
    AnalyzeFunc(data, func(r NodeResult) {
        results = append(results, r)
    })
    return results
}
```

**`main.go` streaming JSON writer:**

```go
case "json":
    // Stream results as a JSON array without buffering all nodes.
    blockedCount := 0
    if err := streamJSON(stdout, data, &blockedCount); err != nil {
        fmt.Fprintf(stderr, "error: streaming json: %v\n", err)
        return exitError
    }
    if blockedCount > 0 {
        return exitBlocked
    }
    return exitOK
```

```go
func streamJSON(w io.Writer, data *cluster.ClusterData, blockedOut *int) error {
    enc := json.NewEncoder(w)
    fmt.Fprintln(w, "[")
    first := true
    var encErr error
    analyzer.AnalyzeFunc(data, func(r analyzer.NodeResult) {
        if encErr != nil {
            return
        }
        if !first {
            fmt.Fprintln(w, ",")
        }
        first = false
        node := jsonNode{
            NodeName: r.NodeName,
            NodePool: r.NodePool,
            Status:   string(r.Status),
            Blockers: toJSONBlockers(r.Blockers),
        }
        encErr = enc.Encode(node)
        if r.Status == analyzer.StatusBlocked {
            *blockedOut++
        }
    })
    fmt.Fprintln(w, "]")
    return encErr
}
```

**Memory savings estimate:**

| Cluster size | Without streaming | With streaming |
|---|---|---|
| 500 nodes / 5,000 pods | ~25 MB peak | ~20 MB peak |
| 1,000 nodes / 10,000 pods | ~50 MB peak | ~38 MB peak |
| 2,000 nodes / 20,000 pods | ~100 MB peak | ~72 MB peak |

The pod slice remains in memory (required for the analysis index). The saving
comes from not allocating `[]NodeResult` with all nodes before any output is
written. For the JSON path this also allows a future optimization where output
is piped directly to a log collector without buffering in the OS pipe buffer.

---

## 4. CronJob + Alerting Pattern

### Option A — Shell wrapper in the same container (simplest)

Override the container command in values to wrap karpview:

```yaml
# values-alerting.yaml override
karpview:
  command: ["/bin/sh", "-c"]
  args:
    - |
      set -e
      OUTPUT=$(karpview --output=json --timeout=60s 2>/dev/null)
      BLOCKED=$(printf '%s' "$OUTPUT" | jq '[.[] | select(.status=="BLOCKED")] | length')
      printf '%s\n' "$OUTPUT"
      if [ "${BLOCKED:-0}" -ge "${MIN_BLOCKED_NODES:-1}" ]; then
        curl -sf -X POST "$SLACK_WEBHOOK_URL" \
          -H 'Content-Type: application/json' \
          -d "{\"text\":\":fire: KarpView: ${BLOCKED} node(s) blocked from consolidation.\"}"
        exit 1   # fail the Job so PagerDuty / alertmanager fires on job failure
      fi
```

This requires the image to include `jq` and `curl`. Use a multi-stage
Dockerfile:

```dockerfile
FROM alpine:3.19 AS tools
RUN apk add --no-cache jq curl

FROM gcr.io/distroless/static:nonroot AS final
COPY --from=tools /usr/bin/jq /usr/local/bin/jq
COPY --from=tools /usr/bin/curl /usr/local/bin/curl
COPY karpview /usr/local/bin/karpview
USER nonroot:nonroot
ENTRYPOINT ["/usr/local/bin/karpview"]
```

### Option B — Init container pattern (no shell in main image)

Keep the main image distroless. Use an init container that writes a script to a
shared volume, and a sidecar container (as shown in the CronJob template above)
that reads the results and fires the alert.

The CronJob YAML in section 1 already implements this pattern when
`alerting.enabled: true`. The karpview container writes to stdout (captured by
the kubelet log driver) and also writes `results.json` to the shared emptyDir.
The alerter container waits for the file and runs the Slack call.

### Option C — Kubernetes Job failure + Alertmanager

The simplest production pattern requires no curl/jq at all:

1. karpview exits `1` when blocked nodes are found (this is already the
   behavior for exit code 1).
2. The CronJob has `backoffLimit: 0` and `failedJobsHistoryLimit: 3`.
3. A Prometheus `kube_job_failed` alert fires on CronJob failure.
4. Alertmanager routes it to Slack/PagerDuty.

```yaml
# PrometheusRule
apiVersion: monitoring.coreos.com/v1
kind: PrometheusRule
metadata:
  name: karpview-blocked
  namespace: monitoring
spec:
  groups:
    - name: karpview
      rules:
        - alert: KarpViewBlockedNodes
          expr: |
            kube_job_failed{job_name=~"karpview-.+", namespace="karpenter"} > 0
          for: 5m
          labels:
            severity: warning
          annotations:
            summary: "KarpView detected blocked nodes"
            description: "One or more Karpenter nodes are blocked from consolidation. Check karpview CronJob logs."
```

> Exit code 2 (runtime error) also fails the Job — ensure your alert
> annotation distinguishes the two or inspect logs.

### PagerDuty via Events API v2

```sh
# In the shell wrapper (Option A), replace the curl Slack call with:
curl -sf -X POST https://events.pagerduty.com/v2/enqueue \
  -H 'Content-Type: application/json' \
  -H "Authorization: Token token=$PAGERDUTY_ROUTING_KEY" \
  -d "{
    \"routing_key\": \"$PAGERDUTY_ROUTING_KEY\",
    \"event_action\": \"trigger\",
    \"payload\": {
      \"summary\": \"KarpView: ${BLOCKED} node(s) blocked from consolidation\",
      \"severity\": \"warning\",
      \"source\": \"karpview-cronjob\"
    }
  }"
```

---

## 5. Resource Requests / Limits

Resource consumption is driven by:
- **Node count** (small — node objects are ~4 KB each)
- **Pod count** (dominant — pod objects are ~8-12 KB each)
- **NodeClaim count** (negligible)
- **PDB count** (negligible)

### Sizing table

| Cluster profile | Pods | Nodes | CPU request | CPU limit | Mem request | Mem limit |
|---|---|---|---|---|---|---|
| Small dev cluster | < 500 | < 50 | 50m | 100m | 32Mi | 64Mi |
| Medium production | 500–3,000 | 50–300 | 50m | 200m | 64Mi | 128Mi |
| Large production | 3,000–10,000 | 300–1,000 | 100m | 500m | 128Mi | 256Mi |
| Very large | > 10,000 | > 1,000 | 200m | 1000m | 256Mi | 512Mi |

### Recommended Helm defaults (medium production)

```yaml
resources:
  requests:
    cpu: 50m
    memory: 64Mi
  limits:
    cpu: 200m
    memory: 128Mi
```

The CPU limit is intentionally loose. karpview is a bursty workload: it does
parallel API calls and then CPU-bound analysis, then exits. Throttling it
extends wall-clock time which increases the chance of hitting the timeout. Use
`--timeout=90s` and a `activeDeadlineSeconds: 300` on the Job to be safe.

### Memory tuning with `--namespace`

With `--namespace` scoping, pod count drops to only the target namespace. For a
500-pod namespace in a 10,000-pod cluster:

```yaml
resources:
  requests:
    cpu: 50m
    memory: 32Mi
  limits:
    cpu: 100m
    memory: 64Mi
```

### OOMKill mitigation

If the limit is hit, the CronJob will retry (up to `backoffLimit`) and
eventually mark the Job failed, triggering alerts via Option C above. Add this
annotation for observability:

```yaml
# In the Job pod spec
annotations:
  karpview.io/cluster-pods-estimate: "5000"
```

Then use it in a Grafana dashboard to correlate OOMKills with cluster growth.

---

## Summary of Changes Required

| Area | File | Change |
|---|---|---|
| `--namespace` flag | `main.go` | Add `--namespace` / `-n` flags; pass to `Clients` |
| Namespace on Clients | `internal/cluster/client.go` | Add `Namespace string` field |
| Namespace-scoped fetch | `internal/cluster/fetch.go` | `FetchOptions.Namespace`; thread into `listAllPods` and PDB list call |
| Streaming output | `internal/analyzer/blocker.go` | Add `AnalyzeFunc(data, fn)` |
| Streaming JSON | `main.go` | Replace `printJSON` with `streamJSON` using `AnalyzeFunc` |
| Helm chart | `helm/karpview/` | New directory with all files from section 1 |
| Alerting | `helm/karpview/templates/cronjob.yaml` | Alerter sidecar controlled by `alerting.enabled` |
