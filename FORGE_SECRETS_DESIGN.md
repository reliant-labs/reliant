# Design: Forge Secrets and Electron Deployment

## Executive Summary

This design extends forge's secret management to support **GCP Secret Manager** as a cloud provider, unifies configuration across the three copy-pasted release.yml build jobs, and models the Electron packaged-app deployment as an `External` forge service with a custom deploy script.

**Key recommendation**: Implement in three independently shippable phases:
1. **Phase 1 (forge)**: Add `GCPSecretManager` provider to `forge/internal/secrets`
2. **Phase 2 (control-plane)**: Declare Electron as `External` service in KCL; update release.yml to consume forge-managed config
3. **Phase 3 (reliant)**: Migrate build config to resolve secrets at build time

**Core trade-off**: Between resolving secrets at build time (values baked into the binary) and at deploy time (values never enter the codebase). For a packaged desktop app, build-time resolution is unavoidable—there is no runtime "deploy" step for a shipped binary. This design accepts that trade-off and uses GCP Secret Manager to centralize the truth, with strict access controls and version immutability to prevent accidents.

---

## 1. GCP Secret Manager Provider for forge

### Design Rationale

Forge's `secrets.Provider` interface is deliberately abstract:
- `Kind() string` — identifies the provider ("file", "external", "gcp-secret-manager")
- `Resolve(name) (value, ok)` — resolves an env-var NAME to its secret VALUE
- `All() map[string]string` — returns every value the provider supplies (used for validation)

Three existing implementations:
- **`fileProvider`** (dev/local): reads from a gitignored YAML file; renders plaintext k8s Secrets
- **`externalProvider`** (prod/staging on k8s): returns nil for all queries; forge validates wiring only, k8s ESO or sealed Secrets resolve values at runtime
- **`noopProvider`**: always returns false; used when no provider is declared

**A GCP Secret Manager provider** adds a **fourth implementation** that:
- Reads secrets from GCP Secret Manager via gRPC (Google Cloud Go SDK)
- Works in three contexts: CI/CD pipeline, local dev machine with gcloud ADC, in-cluster workload with workload identity
- Fills the gap between "dev machine with gitignored YAML" and "k8s cluster with ESO"—specifically for CI builds that must bake secrets into a binary

### Implementation Details

#### New type: `gcpProvider`

```go
// gcpProvider resolves secrets from GCP Secret Manager.
// It is used exclusively at build/CI time (e.g., forge secret resolve,
// release.yml building an Electron app). In-cluster, workload identity
// and ESO replace this (use External secrets).
type gcpProvider struct {
    projectID  string
    client     *secretmanager.Client  // gcloud.google.com/go/secretmanager
    cache      map[string]string      // resolved values (immutable per build)
    cacheErr   error                  // load error, if any
}
```

#### Auth modes supported

1. **Application Default Credentials (ADC)** — default on local machines with `gcloud auth application-default login`
2. **Service account JSON key** — via `GOOGLE_APPLICATION_CREDENTIALS` env var (GitHub Actions secret)
3. **Workload identity** — in-cluster (though this context would use `External` secrets instead)

The Go SDK auto-selects based on environment; no explicit auth code is needed.

#### Lifecycle

**Resolve-once semantics**: Secrets are fetched once at the start of a build job and cached. Failures are fail-fast and loud.

```go
func newGCPProvider(projectID string) (*gcpProvider, error) {
    ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
    defer cancel()
    
    client, err := secretmanager.NewClient(ctx)
    if err != nil {
        return nil, fmt.Errorf("gcp secret manager: create client: %w", err)
    }
    
    p := &gcpProvider{
        projectID: projectID,
        client:    client,
        cache:     make(map[string]string),
    }
    return p, nil
}

// Resolve returns the secret value by name (env-var name == secret name in GCP).
// Caches the result so the value is fetched exactly once per build.
func (p *gcpProvider) Resolve(name string) (string, bool) {
    if p.cacheErr != nil {
        return "", false  // Fail-fast: if we hit an error, stop resolving
    }
    
    if v, ok := p.cache[name]; ok {
        return v, true  // Hit the cache
    }
    
    ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
    defer cancel()
    
    // GCP path: projects/PROJECT/secrets/SECRET/versions/latest
    req := &secretmanagerpb.AccessSecretVersionRequest{
        Name: fmt.Sprintf("projects/%s/secrets/%s/versions/latest", p.projectID, name),
    }
    
    result, err := p.client.AccessSecretVersion(ctx, req)
    if err != nil {
        p.cacheErr = fmt.Errorf("resolve %s: %w", name, err)
        return "", false
    }
    
    value := string(result.Payload.Data)
    p.cache[name] = value
    return value, true
}

// All returns every secret in the cache. For GCP, this requires a list call;
// to avoid N+1 queries, All() is only called during validation (not during
// normal resolve). If N is small, fetch all at once; if large, return nil
// and defer to single-secret resolve (trade-off).
func (p *gcpProvider) All() map[string]string {
    // GCP Secret Manager requires listing secrets explicitly.
    // For now, return nil — validation runs at build time, and the user
    // receives clear errors per-secret. Listing all secrets is optional
    // (the Go guard in ValidateDeclaredRefs skips GCP anyway).
    return nil
}

func (p *gcpProvider) Kind() string { return "gcp-secret-manager" }

func (p *gcpProvider) Close() error { return p.client.Close() }
```

#### KCL schema

```kcl
schema GCPSecretManager:
    """GCP Secret Manager provider: cloud-hosted secrets for CI/build time.
    
    Use this when a build job must resolve secrets to bake into a shipped
    artifact (packaged Electron app, Docker image, binary). Requires
    credentials via Application Default Credentials, GOOGLE_APPLICATION_CREDENTIALS,
    or in-cluster workload identity.
    
    NOT for k8s runtime: use ExternalSecrets (External Secrets Operator)
    and workload identity instead. This provider is for BUILD time.
    
    Environment variables:
      * GCP_PROJECT_ID: GCP project ID (or auto-detect from GOOGLE_CLOUD_PROJECT)
      * GOOGLE_APPLICATION_CREDENTIALS: path to service account JSON (optional;
                                        ADC is tried first)
    
    Secret names must match env-var names (UPPER_SNAKE_CASE). Values are
    fetched once at build time and cached. Access failures are fail-fast
    and loud — a missing secret aborts the build rather than silently
    leaving an empty value.
    """
    type: str = "gcp-secret-manager"
    project_id: str  # GCP project ID
    
    check:
        type == "gcp-secret-manager", "GCPSecretManager.type must be 'gcp-secret-manager'"
        project_id, "GCPSecretManager.project_id is required"
        # GCP is build-time only; it would be misused in prod k8s
        option("env") in ["dev", "staging", "ci"] or option("env") == None, \
            "GCPSecretManager is for build/CI environments; use ExternalSecrets for prod k8s"
```

#### Extending `NewProvider`

Update `forge/internal/secrets/secrets.go`:

```go
func NewProvider(cfg *ProviderConfig) (Provider, error) {
    if cfg == nil {
        return noopProvider{}, nil
    }
    switch strings.ToLower(strings.TrimSpace(cfg.Type)) {
    case "external":
        return externalProvider{}, nil
    case "file":
        // ... existing file logic ...
    case "gcp-secret-manager":
        projectID := cfg.Path  // Path field reused as GCP project ID
        if projectID == "" {
            return nil, fmt.Errorf("gcp-secret-manager: project_id is required")
        }
        return newGCPProvider(projectID)
    // ... rest of existing cases ...
    }
}
```

### Caching and Failure Modes

**Single-pass resolution**: Secrets are fetched exactly once per build. If `Resolve` is called and the secret is not in the cache, a network call is made. If it fails, `cacheErr` is set and all subsequent `Resolve` calls fail fast.

**Why fail-fast**: The old bug was *silent* — a missing secret baked an empty string, and the app shipped silently broken. This design **requires** an explicit confirmation that the secret exists. If GCP Secret Manager is unreachable or the secret is missing, the build **must fail and report it**.

**Timeout**: 10-second timeout per secret fetch (reasonable for GCP API latency + auth); 5-second timeout for client creation. If a secret fetch times out, the build fails with a clear error message naming the secret and the timeout.

**Version immutability**: GCP Secret Manager never overwrites a version—new values create new versions. This means:
- A past version can always be inspected (audit trail)
- Disabling a version is non-destructive (old versions stay intact)
- Rotating a secret is just: create new version → update build config reference (if needed) → disable old version

---

## 2. Secret References vs. Values: Version-Controlled Config

### The Design Principle

**Non-sensitive data (references) goes in git. Sensitive data (values) does not.**

```
VERSION CONTROL                    RUNTIME
┌─────────────────┐               ┌──────────┐
│ release.yml     │               │ GCP      │
│ deploy/kcl      │───resolves──→ │ Secret   │
│ (references)    │               │ Manager  │
│ REVIEWABLE      │               │ (values) │
└─────────────────┘               └──────────┘
```

### Current Problem (Release.yml)

Three identical, copy-pasted blocks at lines 510, 709, 894:

```bash
node -e "
  const config = {
    STATSIG_CLIENT_KEY: process.env.STATSIG_CLIENT_KEY || '',
    SENTRY_DSN: process.env.SENTRY_DSN || '',
    RELIANT_API_URL: process.env.RELIANT_API_URL || '',
  };
  ...
"
```

**Failure mode**: Each copy-paste is independent. One job uses an old env var name; another uses a new one. The drift is invisible until a release silently ships broken.

### Proposed Solution

#### Step 1: Centralize config in KCL (control-plane)

```kcl
# control-plane/deploy/kcl/lib/electron-config.k

electron_build_config = lambda -> {
    # Non-sensitive references (version-controlled, reviewable)
    config_refs: {
        STATSIG_CLIENT_KEY: "STATSIG_CLIENT_KEY",
        SENTRY_DSN: "SENTRY_DSN",
        SUPABASE_URL: "SUPABASE_URL",
        SUPABASE_ANON_KEY: "SUPABASE_ANON_KEY",
        RELIANT_API_URL: "RELIANT_API_URL",
        RELIANT_GATEWAY_URL: "RELIANT_GATEWAY_URL",
    }
    
    # Validation rules (also version-controlled)
    validation: {
        "RELIANT_API_URL must be set": lambda c -> c["RELIANT_API_URL"] != "",
        "RELIANT_API_URL must not target localhost": lambda c -> not ("localhost" in c["RELIANT_API_URL"]),
    }
}
```

#### Step 2: Update release.yml to reference KCL

```bash
# .github/workflows/release.yml

- name: Generate build config
  run: |
    # Read the config refs from KCL (single source of truth)
    # This is a design sketch; the actual invocation depends on forge expose
    CONFIG_SPEC=$(reliant forge kcl get-electron-config)
    
    # For each ref, resolve from GitHub Secrets (or GCP if a future phase adds support)
    node -e "
      const refs = $CONFIG_SPEC.config_refs;
      const config = {};
      for (const [key, ref] of Object.entries(refs)) {
        config[key] = process.env[ref] || '';
      }
      console.log(JSON.stringify(config, null, 2));
    " > src/build-config.js
    
    # Run validation (also from KCL)
    node -e "
      const config = require('./src/build-config.js');
      const rules = $CONFIG_SPEC.validation;
      for (const [name, check] of Object.entries(rules)) {
        if (!check(config)) {
          console.error('Validation failed:', name);
          process.exit(1);
        }
      }
    "
```

#### Step 3: GitHub Actions → GCP Secret Manager

**Option A (near term)**: Dual-write. GitHub Actions continues to hold secrets; release.yml reads them.

```bash
env:
  RELIANT_API_URL: ${{ secrets.RELIANT_API_URL }}
  RELIANT_GATEWAY_URL: ${{ secrets.RELIANT_GATEWAY_URL }}
  # ... etc
```

**Option B (future)**: Unified GCP Secret Manager. GitHub Actions fetes secrets from GCP:

```bash
# Not yet implemented; requires gcloud CLI or a GitHub Action
- name: Fetch secrets from GCP
  uses: google-github-actions/auth@v1
  with:
    workload_identity_provider: ...
    service_account: ci@reliant-labs-475814.iam.gserviceaccount.com
    
- name: Load secrets
  run: |
    gcloud secrets versions access latest --secret="RELIANT_API_URL" > /tmp/api_url
    export RELIANT_API_URL=$(cat /tmp/api_url)
    # ... rest of build
```

### What This Achieves

1. **Single source of truth for config references**: KCL defines what keys are needed, not three copies of bash
2. **Reviewable changes**: A PR that changes the build config is visible and reviewable
3. **Validation is part of the schema**: A bad value is caught at config time, not at runtime
4. **Secrets stay out of git**: Only references (env-var NAMES) are in version control; actual values come from GitHub Secrets or GCP Secret Manager

---

## 3. Electron as an External Forge Service

### Rationale

The **dev Electron service already exists** in control-plane:
- `deploy/kcl/lib/services.k:415` — `reliant_electron_base()`
- `deploy/kcl/dev/main.k:1134` — `_electron_svc` (opt-in via `-D electron=1`)

The **prod Electron app** ("packaged app") is a different beast:
- Not a long-running daemon or container
- "Deploy" means: build → sign → notarize → publish to S3/R2
- No meaningful "rollback" (a shipped binary stays shipped; users download the new version)
- Lifecycle is discrete releases, not continuous rollouts

**An `External` service models this perfectly**: arbitrary shell commands for build/deploy/rollback/health.

### Design

#### KCL Schema

```kcl
# control-plane/deploy/kcl/lib/services.k

reliant_electron_package = lambda -> forge.Service {
    forge.Service {
        name = "reliant-electron-packaged"
        image = "reliant-electron-packaged"
        
        # BUILD: compile and package the Electron app.
        # Equivalent to the current release.yml's `npm run build:backend` +
        # `npx electron-builder --config ...` steps.
        build = forge.ShellBuild {
            cwd = "../reliant/electron"
            cmd = r"""
set -e
echo "[electron-package] Building tools-daemon..."
cd ../reliant && npm run build:backend

echo "[electron-package] Packaging Electron app (macOS/Linux/Windows)..."
cd ../reliant/electron
npx electron-builder \
  --config="${ELECTRON_BUILDER_CONFIG}" \
  --publish="${PUBLISH_FLAG}"
"""
        }
        
        # DEPLOY: the `deploy_cmd` is a no-op for packaged apps.
        # The build step handles everything (sign, notarize, publish).
        # This field is required by External schema, but we leave it minimal.
        external = forge.External {
            type = "external"
            deploy_cmd = "echo '[electron-package] Already published during build'"
            health_cmd = "echo '[electron-package] No health check for packaged app'"
            env_file = ".github/workflows/electron-env.sh"  # Secrets & signing creds
            env = {
                "PUBLISH_FLAG": "${PUBLISH_FLAG}",
                "ELECTRON_BUILDER_CONFIG": "electron-builder.config.js",
            }
        }
        
        host = forge.HostOverrides {
            # Packaged app is CI-only (not dev-runnable via `forge env up`).
            # The `runner` here is a placeholder; actual invocation is via CI.
            runner = "go-run"
            command_override = ["true"]  # No-op; forge env up is N/A
            working_dir = "../reliant/electron"
        }
    }
}
```

#### Release.yml Integration

```bash
# .github/workflows/release.yml

# Orchestrate via `forge env deploy` rather than hand-rolled steps.
# This requires a KCL environment for the release pipeline:

# control-plane/deploy/kcl/ci/main.k
_ci_bundle = forge.Bundle {
    secret_provider = forge.GCPSecretManager {
        type = "gcp-secret-manager"
        project_id = "reliant-labs-475814"
    }
    
    services = [
        svc.reliant_electron_package() | {
            # Platform-specific overrides
            build = forge.ShellBuild {
                cmd = "..."  # Platform detection; build macOS on macOS, etc.
            }
        }
    ]
}

# In release.yml:
- name: Build and publish Electron app
  working-directory: reliant
  run: |
    cd ../control-plane
    forge env deploy ci \
      --service reliant-electron-packaged \
      --image-tag "${{ github.ref_name }}"
```

### Semantics of External Deploy/Rollback for Desktop Apps

**Deploy semantics**:
- `deploy_cmd` runs (in this case, it's a no-op; everything happens in the build)
- `health_cmd` runs (reports success)
- Deploy state is recorded: `(image, tag)` → `.forge/state/external-ci-reliant-electron-packaged.json`

**Rollback semantics**:
- `rollback_cmd` is invoked with `${LAST_TAG}` (the previous version)
- For a desktop app, "rollback" means: publish the previous binary again

This is *not* a real rollback in the Kubernetes sense (you can't unpublish a shipped binary), but it allows the deploy state machine to track what was last shipped and make it available for a re-release if needed.

**Example rollback_cmd**:
```bash
rollback_cmd = r"""
# Download the previous release from S3/R2 and publish it again
aws s3 cp s3://reliant-releases/reliant-${LAST_TAG}-x64.exe .
# Re-run the signed/notarized binary through the publish pipeline
npx electron-builder publish never --config electron-builder.config.js
"""
```

### Why External Fits (and Where It Strains)

**Fits**:
- Reuses existing `ServiceGroup`, `ExternalProvider`, substitution tokens
- One shell escape hatch handles sign/notarize/publish, which is already CLI-driven (electron-builder)
- Deploy state tracking (tag history) is useful for manual recovery

**Strains**:
- Health checks are meaningless (the app is already published; no service is "up")
- Rollback semantics don't match reality (you can't actually unpublish)
- The `External` model assumes deploy is orthogonal to build, but for packaged apps they're entangled

**Acceptable trade-off**: The strain is cosmetic. The alternative is building a new deploy target just for desktop apps (overkill). Using `External` with mostly-no-op health/rollback is simpler and consistent.

---

## 4. Trade-offs and Alternatives

### Alternative 1: GitHub Secrets + ESO (External Secrets Operator)

**How it works**:
- Secrets stay in GitHub (no GCP dependency)
- Kubernetes cluster runs ESO, which syncs GitHub secrets → k8s Secrets
- For CI builds, GitHub Actions directly reads `secrets.*`

**Pros**:
- No new GCP auth/permissions needed
- All secrets in one place (GitHub)
- ESO is well-established for k8s

**Cons**:
- ESO is k8s-only; CI builds that need to resolve secrets (like Electron) must still implement their own auth
- GitHub has no "sync this to GCP" without a third-party action
- GitHub secrets are not auditable the way GCP Secret Manager is (version history, rotation trails)

**Verdict**: Rejected. GitHub secrets + ESO doesn't solve the Electron build problem (CI still needs to resolve at build time).

### Alternative 2: Committed non-secret config + GitHub Secrets

**How it works**:
- Version-controlled KCL defines all config keys and validation
- GitHub Actions reads env vars from `secrets.*`, never writes them to git
- For k8s, ESO syncs to Secrets; for desktop, values are injected at build time

**Pros**:
- Minimal new infrastructure (GitHub Actions is already there)
- Version control is pure (no secrets in git at all)
- Clear separation: references in git, values in GitHub

**Cons**:
- Still doesn't prevent the release.yml copy-paste drift
- No audit trail for secret changes (GitHub doesn't version secrets the way GCP does)
- Scaling to N environments with M secrets means N×M copy-pasted secret references

**Verdict**: Partial solution. It unifies the references in KCL, but doesn't solve the Electron build-time resolution problem cleanly.

### Alternative 3: sops/age (Encrypted Secrets in Git)

**How it works**:
- Secrets are encrypted with age keys and stored in git
- Build time: decrypt with `sops`, resolve values
- Transparent to the app

**Pros**:
- Secrets are versioned and audited (in git history)
- Single source of truth

**Cons**:
- Requires careful key management (age key to every developer, CI/CD)
- Tempting to accidentally commit unencrypted
- More operational overhead (key rotation, distribution)
- Not aligned with GCP workload identity (another auth system to manage)

**Verdict**: Rejected for this project. GCP workload identity is already in use for control-plane; adding sops/age is redundant and introduces a second secret-management system.

### Alternative 4: HashiCorp Vault

**How it works**:
- Vault is the single source of truth for all secrets
- Apps read from Vault at startup via AppRole, JWT, or workload identity
- Desktop: build step reads from Vault, bakes into binary

**Pros**:
- Powerful, widely used
- Excellent audit trail and rotation tools

**Cons**:
- Overkill for this project (already using GCP Secret Manager)
- Another service to run and manage
- Vault doesn't replace GCP Secret Manager; they'd run in parallel

**Verdict**: Rejected. GCP Secret Manager is already deployed and in use. Vault is unnecessary.

### Chosen: GCP Secret Manager + Unified KCL Config

**Why**:
1. **Least infrastructure**: Secrets already live in GCP Secret Manager (per BRIEFING.md); no new service
2. **Audit trail**: GCP versions immutably; each secret change is recorded
3. **Workload identity parity**: In-cluster pods use workload identity; CI uses ADC or service account keys — same auth system
4. **Gradual adoption**: Doesn't require moving GitHub secrets immediately; can start with build-time only (Electron)
5. **Not too clever**: Direct GCP API calls (no ESO, no sops, no Vault)

**Trade-off accepted**: Secrets must be resolved at build time (values baked into binary) for packaged Electron. This is unavoidable—there is no runtime to fetch secrets in a shipped desktop app. GCP Secret Manager centralizes the truth; strict version immutability and access controls prevent accidents.

---

## 5. Implementation Plan (Staged, Independently Shippable)

### Phase 1: Forge Secrets Provider (Forge Release)

**Forge repo**; merge into main, tag new release (e.g., `v0.99.0`).

**Deliverables**:
- `forge/internal/secrets/gcp_provider.go` — new `gcpProvider` implementation
- Update `forge/internal/secrets/secrets.go` — add "gcp-secret-manager" case to `NewProvider`
- `forge/kcl/schema.k` — add `GCPSecretManager` schema
- Tests: `forge/internal/secrets/gcp_provider_test.go`
- Doc comment in `forge/internal/secrets/secrets.go` explaining when GCP is used (build-time, not k8s runtime)

**Dependencies**:
- `github.com/googleapis/go-genproto` (already in forge, used by other GCP clients)
- `cloud.google.com/go/secretmanager` (new dependency)

**Breaking changes**: None. Backward compatible.

**Testing**:
- Unit tests with a fake GCP client (use `github.com/googleapis/gapic-generators-go` test utilities or mock)
- Manually test: `forge secret resolve --provider gcp-secret-manager --project reliant-labs-475814`
- Verify auth modes: ADC, service account JSON, (optional: workload identity in-cluster)

**Review gate**: Forge team approves secret-handling code; no security shortcuts.

---

### Phase 2: Control-Plane KCL + Release.yml Unification (Control-Plane Release)

**Control-plane repo**; pin forge to the new version (go.mod replace).

**Deliverables**:

1. **KCL updates**:
   - `deploy/kcl/lib/electron-config.k` (NEW) — centralized Electron build config + validation rules
   - `deploy/kcl/lib/services.k` — add `reliant_electron_package()` (the packaged-app External service)
   - `deploy/kcl/ci/main.k` (NEW) — bundle for CI environment with GCP Secret Manager provider

2. **Release.yml updates**:
   - Remove the three identical config-generation blocks (lines 510, 709, 894)
   - Replace with single call: `forge env resolve` or equivalent to read from KCL
   - Wire env vars from GitHub Actions secrets (short term) or GCP (future)

3. **Tests**:
   - `deploy/kcl/tests/positive_electron_packaged.k` — KCL validation
   - Manual: trigger a release workflow and verify build-config.js is generated correctly

**Dependencies**:
- Forge version bump (to the Phase 1 release)

**Breaking changes**: None. Old release.yml still works; new version just consolidates.

**Testing**:
- Verify: `forge env deploy ci --service reliant-electron-packaged --dry-run` outputs the right commands
- Manually verify: build config contains correct URLs (no localhost)
- Smoke test: Stage a release workflow and check the artifacts

---

### Phase 3: Reliant Build-Time Config Resolution (Reliant Release)

**Reliant repo**; update references to control-plane KCL + GCP Secret Manager.

**Deliverables**:

1. **Electron app updates**:
   - No changes to `electron/src/backend-manager.js` (it continues to read env vars, which are now injected by release.yml)
   - Update `electron/src/build-config.js` generation to validate using KCL rules (done by release.yml, not the app)

2. **CI/release.yml updates**:
   - Swap GitHub Secrets → GCP Secret Manager (requires GitHub Actions + gcloud auth)
   - Or defer this to a later phase; short term, GitHub Secrets + resolved config validation via KCL is sufficient

3. **Documentation**:
   - `RELEASE.md` — how to cut a release, what secrets are needed, where they live
   - `docs/electron-build-config.md` — what build-config.js contains and why

**Testing**:
- Build an actual release and verify the packaged app points at the correct hostname
- Check logs for secret-loading errors (if any secret is missing, fail-fast with a clear message)

---

## 6. Detailed Implementation Notes

### GCP Secret Manager API Usage

```go
import (
    "cloud.google.com/go/secretmanager/apiv1"
    "cloud.google.com/go/secretmanager/apiv1/secretmanagerpb"
)

// Access a secret version (latest or specific)
client.AccessSecretVersion(ctx, &secretmanagerpb.AccessSecretVersionRequest{
    Name: "projects/PROJECT_ID/secrets/SECRET_NAME/versions/latest",
})

// List all secrets in a project
client.ListSecrets(ctx, &secretmanagerpb.ListSecretsRequest{
    Parent: "projects/PROJECT_ID",
})

// For CI builds, only AccessSecretVersion is needed (single-pass fetch).
```

### Schema Change: ProviderConfig → GCPSecretManager

**Current `ProviderConfig`** (in `forge/internal/secrets/secrets.go`):
```go
type ProviderConfig struct {
    Type string // "file" | "external"
    Path string // secret store file path
}
```

**For GCP**, the KCL bridge maps:
```kcl
GCPSecretManager {
    type = "gcp-secret-manager"
    project_id = "reliant-labs-475814"
}
```

The KCL→Go mapping (handled by forge's codegen):
```go
ProviderConfig{
    Type: "gcp-secret-manager",
    Path: "reliant-labs-475814",  // Project ID goes in Path field
}
```

This is a pragmatic reuse of the existing config structure; no schema changes needed in Go.

### External Service State File for Electron

Deploy state for packaged apps:
```json
{
  ".forge/state/external-ci-reliant-electron-packaged.json": {
    "image": "reliant-electron-packaged",
    "tag": "v1.2.3"
  }
}
```

The tag is useful for manual rollback (redownload and republish v1.2.2 if v1.2.3 is broken).

### Validation Rules (KCL + Go)

Example validation for RELIANT_API_URL:

**In KCL** (version-controlled, reviewable):
```kcl
electron_build_config = lambda -> {
    config_refs: { ... },
    validation: {
        "RELIANT_API_URL required": lambda c -> c["RELIANT_API_URL"] != "",
        "RELIANT_API_URL no localhost": lambda c -> not ("localhost" in c["RELIANT_API_URL"]),
    }
}
```

**In release.yml** (runs the checks):
```bash
node -e "
  const config = require('./src/build-config.js');
  const ref = <KCL validation rules from control-plane>;
  for (const [name, fn] of Object.entries(ref)) {
    if (!fn(config)) {
      console.error('Validation error:', name);
      process.exit(1);
    }
  }
"
```

### Error Messages (Fail-Fast Design)

When a secret is missing or unreachable:

```
[electron-package] gcp-secret-manager: resolve RELIANT_API_URL: status code 404: secret not found
  This secret must exist in GCP Secret Manager (projects/reliant-labs-475814/secrets/RELIANT_API_URL).
  Create it with: gcloud secrets create RELIANT_API_URL --data-file=- (or use the GCP Console).
  If it exists, verify:
    - gcloud auth is logged in (gcloud auth list)
    - GOOGLE_CLOUD_PROJECT is set correctly
    - the secret is not disabled (gcloud secrets versions list RELIANT_API_URL)
```

Clear error messages are essential; silence was the original bug.

---

## 7. Security Considerations

### Secret Immutability

GCP Secret Manager **never overwrites** a secret version. New values create new versions.

**Implication for release workflow**:
1. Update secret in GCP: `gcloud secrets versions add RELIANT_API_URL --data-file=-`
2. This creates a new version (e.g., version 5) and leaves version 4 intact
3. Release workflow uses "latest" (version 5)
4. If version 5 is wrong, disable it and restore version 4 (non-destructive)

**Audit trail**:
```bash
$ gcloud secrets versions list RELIANT_API_URL
NAME                                CREATED              DESTROYED  REPLICATED  ACCESSED
5                                   2024-01-15T10:00:00Z            Yes         2024-01-15T10:05:00Z
4                                   2024-01-14T15:00:00Z            Yes         2024-01-14T15:30:00Z
3                                   2024-01-10T12:00:00Z Destroyed  Yes         2024-01-10T12:15:00Z
```

Every access is logged; rotation is visible.

### Access Control (IAM)

GCP Secret Manager integrates with Cloud IAM. For reliant-labs-475814:

**CI service account** (used by GitHub Actions):
```
roles/secretmanager.secretAccessor
# Can read secret versions; cannot create/destroy
```

**Developers** (local machine, `gcloud auth application-default login`):
```
roles/secretmanager.admin
# Can create/read/rotate/destroy (full control)
# OR: roles/secretmanager.secretAccessor (read-only)
```

**In-cluster workload** (future, if k8s reads GCP):
```
# Workload identity binding:
# kubernetes.io/service-account: reliant-api-server → 
# google.iam.gserviceaccount.com/reliant-api-server@reliant-labs-475814.iam.gserviceaccount.com
# With: roles/secretmanager.secretAccessor
```

### Fail-Safe Design

1. **No silent fallbacks**: If a secret is missing, the build fails immediately with a clear error.
2. **Timeout-guarded**: 10s timeout per secret; if GCP is slow, the build doesn't hang indefinitely.
3. **Single-pass fetch**: Secrets are resolved once at build start; no retry loops or jitter.
4. **No caching across builds**: Each CI job is fresh; no old cached values leak.
5. **Validation is early**: Build config is validated before any network calls.

---

## 8. FAQ and Gotchas

**Q: Why not store secrets in Kubernetes and just fetch at runtime?**
A: Packaged Electron app has no runtime—it's a shipped binary. Secrets must be baked in at build time or fetched from an external service on startup (adds latency and a network dependency for every app launch). Build-time is simpler.

**Q: What if GCP Secret Manager is down during a release?**
A: The build fails with a clear error. The developer must wait for GCP to recover or use a previous release's binary.

**Q: Can I rotate a secret without re-releasing the app?**
A: Only if the app reads the secret at startup (e.g., from a config service). Packaged app has the value baked in; rotation requires a new build and release.

**Q: What about local development (forge env up)?**
A: Dev environment uses `FileSecrets` (gitignored YAML). Developers run `forge secret set dev RELIANT_API_URL` to populate a local file. No GCP dependency for local dev.

**Q: Does this work for the web app (Vite)?**
A: Yes, but web app is served by a backend that injects config. A future phase could unify web + Electron config via KCL. For now, web uses env vars injected by the deployment platform (k8s Secrets, CloudRun env vars, etc.).

**Q: What if I mess up and accidentally commit a secret to git?**
A: GCP Secret Manager versions are immutable; GitHub secret history is not. Use GitHub's secret scanning and invalidate the secret immediately. For pre-existing secrets, deploy a new version to GCP Secret Manager and rotate credentials everywhere it was used.

---

## 9. Success Criteria

✅ **Phase 1 (Forge)**: 
- [ ] `gcpProvider` is implemented and tested
- [ ] CI runs `forge secret resolve --provider gcp-secret-manager` successfully
- [ ] Code review approved by forge team

✅ **Phase 2 (Control-Plane)**:
- [ ] `reliant_electron_package` External service is defined in KCL
- [ ] KCL validation passes: `reliant forge lint`
- [ ] Release.yml generates `src/build-config.js` from KCL
- [ ] Validation rejects bad config (e.g., localhost URLs)

✅ **Phase 3 (Reliant)**:
- [ ] Manual release build succeeds with correct config values
- [ ] Packaged app (macOS/Linux/Windows) connects to correct prod hostname
- [ ] Error messages are clear if a secret is missing

---

## Conclusion

This design unifies forge's secret management across three contexts: dev (file-based), k8s production (external/ESO), and CI build-time (GCP Secret Manager). It replaces three copy-pasted release.yml blocks with a single KCL definition, making configuration reviewable and maintainable. It models Electron packaging as a first-class forge deployment target, enabling `forge env deploy ci --service reliant-electron-packaged`.

The trade-off—build-time secret resolution—is unavoidable for packaged apps and is the right choice. GCP Secret Manager's version immutability and audit trail provide safety and visibility that prevent a repeat of the `api.reliant.so` silent-failure bug.
