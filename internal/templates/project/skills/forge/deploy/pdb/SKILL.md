---
name: deploy/pdb
description: PodDisruptionBudgets — the pdb-blocks-drain lint and render warning for a PDB that can never allow a disruption, and the pruning of stale forge-labelled PDBs when a workload drops to one replica.
---

# PodDisruptionBudgets

forge renders a PDB (`maxUnavailable: 1`) only above one replica. Two guards keep PDBs from blocking node drains:

- **A PDB that can never allow a disruption is flagged** — `minAvailable >= replicas`, `maxUnavailable: 0`, or `minAvailable: 100%`, on forge-rendered PDBs and your own `forge.Manifests` alike. `forge lint` reports it as `pdb-blocks-drain` (warning) and `forge env deploy` / `up` print a render-time warning naming the object. Fix: `maxUnavailable: 1`, or delete the PDB while the workload runs one replica.
- **Stale PDBs are pruned.** Dropping a workload to one replica stops rendering its PDB. Under Flux, `prune: true` removes it (PDBs are never prune-guarded). On the direct-apply path, `forge env deploy` deletes forge-labelled (`managed-by=forge`) PDBs of any workload in the render whose PDB the render no longer carries — PDBs only, and never one forge did not label.
