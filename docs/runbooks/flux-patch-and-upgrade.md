# Patching and upgrading the Flux that `forge.flux_chart()` installs

`forge.flux_chart()` installs ControlPlane's Flux Operator (chart
`FLUX_CHART_VERSION` in `kcl/lib/flux.k`, image pinned by digest
`FLUX_OPERATOR_IMAGE_DIGEST`, mirrored in `pkg/flux`) and a FluxInstance that pins
the Flux **minor** (`FLUX_VERSION`, `2.9.x`). With no `distribution.artifact`,
the operator installs Flux from the manifests embedded in its own image, so the
Flux **patch** that runs is the newest one the pinned operator embeds. forge
never installs Flux into a deploy target by itself; see `forge skill load
deploy/flux`. There is deliberately no version knob: forge replaces the
controllers' flags wholesale, and they are verified against one minor. So a Flux
patch is a **forge release** (an operator bump, or a minor bump), and a consumer
picks it up by bumping its forge pin.

A consumer that must ship a Flux CVE patch without waiting for an operator
release can mirror the operator's `flux-operator-manifests` artifact into its
own registry and set `distribution = {artifact = "oci://<registry>/..."}`: the
operator then polls that artifact (within the minor) instead of its embedded
copy. Only artifacts you push are ever installed.

## Cadence

| Trigger                                                                                            | Deadline                                                                                                        |
| -------------------------------------------------------------------------------------------------- | --------------------------------------------------------------------------------------------------------------- |
| Flux **security** release (any controller forge installs: source-controller, kustomize-controller) | forge release within **7 days**, consumers deployed dev → prod right after                                      |
| Flux **minor** release                                                                             | forge release within **30 days**                                                                                |
| Flux Operator **security** release                                                                 | forge release within **7 days**: the operator is cluster-admin on every cluster that declares Flux              |
| Advisory against a controller forge does **not** install (helm, notification, image-\*)            | note it here; no release needed, but re-read "Two controllers, not six" in `kcl/lib/flux.k` before enabling one |

## CVE watch

forge does not run a feed; whoever owns the pin does.

- Watch the GitHub Security Advisories of `controlplaneio-fluxcd/flux-operator`, `fluxcd/flux2`, `fluxcd/source-controller`,
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
| notification-controller network exposure (GHSA-mwcp-qpcg-fr7c) | controller not installed; `allow-webhooks` NetworkPolicy not rendered (FluxInstance `networkPolicy` off)             |

## Procedure

1. **Pick the operator release.** Bump `FLUX_CHART_VERSION` and
   `FLUX_OPERATOR_IMAGE_DIGEST` (`kcl/lib/flux.k`) and `ChartVersion`
   (`pkg/flux/flux.go`). The digest is the multi-arch index of
   `ghcr.io/controlplaneio-fluxcd/flux-operator:v<version>`:

   ```bash
   crane digest ghcr.io/controlplaneio-fluxcd/flux-operator:v<version>
   ```

   A Flux minor bump is `FLUX_VERSION` (and `FluxVersion`), only to a minor the
   operator embeds (`config/data/flux/` in the operator's source at that tag).

2. **Build what the operator would install, before and after.** The operator's
   own builder renders a FluxInstance offline; nothing else shows the
   controllers' final flags:

   ```bash
   go install github.com/controlplaneio-fluxcd/flux-operator/cmd/cli@v<version>   # binary: cli
   forge env render <env> --target flux-operator > /tmp/render.yaml
   # extract the FluxInstance document, then:
   cli build instance -f /tmp/fluxinstance.yaml > /tmp/after.yaml
   ```

   `FLUX_OPERATOR_CLI=$(go env GOPATH)/bin/cli task test -- ./internal/cluster/...`
   runs `TestFluxInstance_OperatorBuildIsHardened` against that build. Read the
   diff for: a renamed Deployment or RBAC object (a delete or args patch that
   stops matching — the `flux-controller-bindings` and
   `flux-connected-shard-identity` admission policies then refuse the
   operator's apply, which is the intended loud failure), a stock arg the
   wholesale `args` replacement now drops (re-derive the list from the
   distribution's `<component>.yaml`), a new component enabled by default, a
   NetworkPolicy, a sharding profile change.

3. **Read the controller release notes** for source-controller and
   kustomize-controller between the two versions: removed or renamed flags,
   changed feature-gate defaults, API version changes. The gates the defaults
   set (`CancelHealthCheckOnNewRevision`, `DisableConfigWatchers`) are checked by
   the controller at start-up: an unknown gate is a hard failure (`feature-gate
'X' not supported`), so confirm both still exist in
   `internal/features/features.go` at the new kustomize-controller tag. Read the
   operator's release notes for FluxInstance, ResourceSet and uninstall
   semantics (`internal/controller/fluxinstance_uninstaller.go`).
4. **Run the gates:** `task test -- ./internal/templates/... ./internal/cluster/...`
   — the KCL corpus (`kcl/tests/positive_flux_chart*.k`) and `TestFluxChart_*` /
   `TestFluxInstance_*` / `TestFluxConnectedShardAdmission` in `internal/cluster`.
5. **Roll it dev first.** The operator upgrades the controllers and CRDs in
   place. Watch `kubectl -n flux-system get fluxinstance flux` (Ready, the
   new `lastAppliedRevision`),
   `kubectl -n flux-system rollout status deploy/kustomize-controller deploy/source-controller`,
   then confirm a
   Kustomization reconciles to a new revision.
6. **Release forge**, then bump the pin in each consumer.

## Rolling back

There is no rollback command (`deploy`). A bad Flux is a forge release that pins
the previous operator version; CRD changes are additive, so the older controller
still reads them. To stop the operator from touching Flux while you work,
annotate the FluxInstance `fluxcd.controlplane.io/reconcile=disabled`. **Never
delete the FluxInstance to roll back**: deletion uninstalls Flux (forge marks
the CRDs prune-disabled, but the controllers go).

## Migrating a consumer onto the hardened defaults

See the v0.1.46 migration, section "Hardened `forge.flux_chart()`", and the
v1.0.14 migration for the move from the community chart to the Flux Operator.
