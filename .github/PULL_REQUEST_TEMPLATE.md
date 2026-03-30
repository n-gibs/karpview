## Summary

<!-- One sentence: what does this PR do and why? -->

## Performance checklist

> Required for PRs touching `internal/analyzer/` or `internal/cluster/`.
> Skip individual items with a brief justification if the PR is docs-only or test-only.

- [ ] `make bench-compare` shows no regressions beyond CI thresholds (+10% time/op, +15% B/op, +10% allocs/op).
- [ ] New slices or maps in hot paths use pre-allocated capacity (`make([]T, 0, N)` or `make(map[K]V, N)`).
- [ ] Any string processing called per-node, per-pod, or per-PDB is single-pass (no chained `strings.Replace`, no `regexp.MustCompile` inside a loop).
- [ ] New loops over nodes, pods, or PDBs are O(N) or O(N log N) — not O(N * M) unless M is provably bounded and small.
- [ ] `make size` confirms binary is under 25 MB.
- [ ] No new `init()` or package-level `regexp.MustCompile` added outside `internal/analyzer/`.
- [ ] Optional features (new flags, env vars) have zero cost on the default code path.

### Benchmark delta

<!-- Paste output of: benchstat internal/analyzer/testdata/bench-baseline.txt /tmp/bench-local.txt -->
<!-- Run: make bench-compare -->

```
(paste here)
```

### Binary size

<!-- Paste output of: make size -->

```
(paste here)
```

## Test plan

- [ ] `make test` passes
- [ ] `make lint` passes
- [ ] `make vet` passes
