# Patching and upgrading the Flux that `forge.flux_chart()` installs

forge pins one Flux chart (`FLUX_CHART_VERSION` in `kcl/lib/flux.k`, mirrored
in `internal/flux`), which a consumer that runs a Flux of its own (a control
plane reconciling hosted workloads) declares with `forge.flux_chart()`. forge
never installs Flux into a deploy target by itself; see `forge skill load
deploy/flux`. There is deliberately no version knob: the chart's hardening and
the revision checks a control plane's wait depends on are properties of a
specific source-controller. So a Flux patch is a **forge release**, and a
consumer picks it up by bumping its forge pin.

## Cadence

| Trigger                                                                                            | Deadline                                                                                                        |
| -------------------------------------------------------------------------------------------------- | --------------------------------------------------------------------------------------------------------------- |
| Flux **security** release (any controller forge installs: source-controller, kustomize-controller) | forge release within **7 days**, consumers deployed dev → prod right after                                      |
| Flux **minor** release                                                                             | forge release within **30 days**                                                                                |
| Advisory against a controller forge does **not** install (helm, notification, image-\*)            | note it here; no release needed, but re-read "Two controllers, not six" in `kcl/lib/flux.k` before enabling one |

## CVE watch

forge does not run a feed; whoever owns the pin does.

- Watch the GitHub Security Advisories of `fluxcd/flux2`, `fluxcd/source-controller`,
  `fluxcd/kustomize-controller` and `kubernetes-sigs/kustomize`, plus OSV for the
  Go modules they import (`sigs.k8s.io/kustomize/*`, `github.com/fluxcd/pkg/*`).
  `gh api repos/fluxcd/flux2/security-advisories` lists them without a login.
- A shared (multi-author) Flux should have a daily check that raises an alert
  (`forge.AlertRule`) when an advisory's affected range includes the pinned
  controller version.
- Advisory classes and where this chart's defaults stand:

| Class                                                          | Mitigation in the defaults                                                                                           |
| -------------------------------------------------------------- | -------------------------------------------------------------------------------------------------------------------- |
| Secret/kubeconfig injection (CVE-2021-41254, CVE-2022-24817)   | no `--insecure-kubeconfig-exec`; the controller reads Secrets only in namespaces bound to `flux-tenant-*`            |
| Kustomize / YAML path traversal (GHSA-j77r-2fxf-5jrw)          | `--no-remote-bases`, plugins off by construction; validate the artifact at ingest (`release.ValidateManifestsLayer`) |
| Source-controller non-OCI paths (CVE-2026-47680)               | OCIRepository only; do not declare Bucket/GitRepository sources                                                      |
| notification-controller network exposure (GHSA-mwcp-qpcg-fr7c) | controller not installed; `allow-webhooks` NetworkPolicy not rendered                                                |

## Procedure

1. **Find the chart that carries the Flux release.** The chart version and the
   Flux version are different lineages. Never increment:

   ```bash
   helm show chart oci://ghcr.io/fluxcd-community/charts/flux2 --version <candidate> | grep -E '^(version|appVersion)'
   ```

   Pick the chart whose `appVersion` is the Flux release you need, then confirm
   the images: `helm template x oci://ghcr.io/fluxcd-community/charts/flux2 --version <candidate> | grep 'image:'`
   must show source-controller and kustomize-controller at the expected tags.

2. **Diff the render against the current pin.** Defaults change in a chart bump
   without notice; this is the check that the posture still means what it says:

   ```bash
   forge env render <env> --target flux > /tmp/before.yaml     # on the old pin
   # bump FLUX_CHART_VERSION (kcl/lib/flux.k) and ChartVersion (internal/flux/flux.go)
   forge env render <env> --target flux > /tmp/after.yaml
   diff /tmp/before.yaml /tmp/after.yaml
   ```

   Read for: a renamed Deployment (a `placement` patch then fails the render
   naming the candidates — that is the intended loud failure), a changed
   container arg or feature-gate name, a new controller enabled by default, a
   new cluster-wide RBAC rule, a NetworkPolicy that gained a peer.

3. **Read the controller release notes** for source-controller and
   kustomize-controller between the two versions: removed or renamed flags,
   changed feature-gate defaults, API version changes. The gates the defaults
   set (`CancelHealthCheckOnNewRevision`, `DisableConfigWatchers`) are checked by
   the controller at start-up: an unknown gate is a hard failure (`feature-gate
'X' not supported`), so confirm both still exist in
   `internal/features/features.go` at the new kustomize-controller tag.
4. **Run the gates:** `task test -- ./internal/templates/... ./internal/cluster/...`
   — the KCL corpus (`kcl/tests/positive_flux_chart*.k`, run by
   `internal/templates`) and `TestFluxChart_*` in `internal/cluster`, which
   render the real chart through helm and assert the objects.
5. **Roll it dev first.** Flux upgrades CRDs in place (forge applies them first,
   Established-gated). Watch `kubectl -n flux-system rollout status
deploy/kustomize-controller deploy/source-controller`, then confirm a
   Kustomization reconciles to a new revision.
6. **Release forge**, then bump the pin in each consumer.

## Rolling back

There is no rollback command (`deploy`). A bad Flux is a forge release that pins
the previous chart version; CRD changes are additive, so the older controller
still reads them.

## Migrating a consumer onto the hardened defaults

See the v0.1.46 migration, section "Hardened `forge.flux_chart()`".
