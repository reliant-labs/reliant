---
name: v1.0.2
description: Upgrade a project to forge v1.0.2. (1) pkg/servicecatalog.MaxBytes is removed — a service catalog has no size bound of its own; the reader that fetched the bytes owns the bound (release.ManifestsLayerMaxBytes for a bundle layer). Use when Go code names servicecatalog.MaxBytes.
version: v1.0.2
detection: grep -rqsE --include='*.go' --exclude-dir=gen --exclude-dir=vendor --exclude-dir=node_modules 'servicecatalog\.MaxBytes' . && exit 0; exit 1
---

# Upgrading to v1.0.2

Each section below is independent: apply the ones whose "Use when" matches.

## 1. `servicecatalog.MaxBytes` is removed

**Use when** Go code names `servicecatalog.MaxBytes`.

**What changed.** The 1 MiB cap refused real projects (reliant's catalog is
2.1 MB compact) and bounded bytes already in memory. `servicecatalog.Decode`
now validates shape only. A catalog in a bundle is a manifests-layer artifact,
bounded like every other artifact by `release.ManifestsLayerMaxBytes`.

**What to do.** Bound the READ that fetches the catalog, not the decode:

- reading it from a bundle layer: `release.ValidateManifestsLayer` /
  `release.ReadSnapshotAssets` already apply the layer bound — delete your check;
- reading it from anywhere else (a registry blob, a request body): bound that
  read with the limit of the thing you read it from, e.g.
  `io.LimitReader(r, release.ManifestsLayerMaxBytes+1)`;
- returning it in a response: fit it to your server's send limit
  (`connect.WithSendMaxBytes`), not to a catalog constant.

A decision about which bound applies is yours to make, so there is no codemod.
