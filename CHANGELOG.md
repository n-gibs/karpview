# Changelog

All notable changes to this project will be documented in this file.
Format: [Keep a Changelog](https://keepachangelog.com/en/1.0.0/)

## [0.2.0] - 2026-04-01

### Added
- `DISRUPTION` column showing forceful-disruption signals alongside consolidation analysis
- Node health detection: `Ready=False/Unknown`, `MemoryPressure`, `DiskPressure`, `PIDPressure` conditions (H1)
- Node expiry detection: warns when `expireAfter` deadline is within 24h or already past (H2)
- Drift detection: surfaces NodeClaims with `Drifted=True` status condition (H3)
- JSON output includes `healthIssues`, `expiryState`, `drifted` fields

## [0.1.1] - 2026-04-01

### Fixed
- Removed unnecessary rbac.yaml — CLI inherits user kubeconfig permissions

## [0.1.0] - 2026-03-31

### Added
- Node consolidation status table with BLOCKED/DRAINING/READY sort
- PDB blocker detection with pod attribution (B1-B3)
- CPU/memory utilization % per node (C1)
- Empty and daemon-only node classification (C2-C3)
- NodePool consolidation policy and `consolidateAfter` timer (C4-C5)
- Per-reason disruption budget headroom and schedule windows (C6)
- `karpview budgets` subcommand — pool-centric budget drill-down (C7)
- JSON output (`-o json`) for all commands
- Exit codes: 0 = all clear, 1 = blocked nodes, 2 = runtime error
