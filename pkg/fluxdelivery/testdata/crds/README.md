# Pinned CRD schemas

The schemas the rendered objects are validated against (`crd_schema_test.go`),
by the API server's own structural pruning and OpenAPI validation. A field the
schema does not declare is a test failure here instead of a field the API
server silently drops.

| File | Source |
|---|---|
| `ocirepository-source-v1.json`, `kustomization-kustomize-v1.json` | `crd-schemas.tar.gz` of the Flux v2.9.5 release (source- and kustomize-controller v1.9.5), the version `forge/pkg/flux.ChartVersion` pins |
| `fluxcd.controlplane.io_resourcesets.yaml` | flux-operator v0.62.1, `config/crd/bases/` |

Refresh on a Flux or flux-operator bump:

```bash
curl -fsSL https://github.com/fluxcd/flux2/releases/download/v<flux>/crd-schemas.tar.gz | tar xz \
  ocirepository-source-v1.json kustomization-kustomize-v1.json
curl -fsSLO https://raw.githubusercontent.com/controlplaneio-fluxcd/flux-operator/v<operator>/config/crd/bases/fluxcd.controlplane.io_resourcesets.yaml
```
