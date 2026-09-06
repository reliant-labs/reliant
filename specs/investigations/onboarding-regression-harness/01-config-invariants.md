# 01 — Config / env invariants: the cross-environment configuration contract

Area owner: config-invariants agent. Analysis only; no product code was edited.

**This file is the primary deliverable of the investigation.** Three independent
agents — config (this one), billing/daemon-backend, and auth/OAuth — converged
on the same detector from three different directions. It is specified here.

Verified by reading `control-plane/deploy/kcl/`, the Go consumers in
`control-plane/internal/`, `reliant/web/src/lib/`, three CI workflows, and
`reliant/.github/scripts/sync-release-config.mjs`. Findings inherited from the
billing and auth agents are credited inline and were independently re-verified
where they are load-bearing; one is **corrected** (§5, I-B2).

---

## 0. The headline

**The detector three agents converged on is a set of semantic invariants over
per-env config. It should be implemented as KCL `assert`s, and it finds a bug
that is in the tree right now.**

Two mechanisms already exist in this repo, each on exactly one surface:

| Mechanism | Where it exists | What it proves |
|---|---|---|
| **Single-declaration + drift gate** | `reliant_endpoints` → `reliant_desktop_release_config` → `electron/release.config.json`, gated by `sync-release-config.mjs --check` (control-plane `ci.yml:271`) | the generated artifact MATCHES the declaration |
| **Artifact assertion** | `deploy-reliant-web.yml:226-316` — three `grep`-the-`dist` gates | the BUILT BUNDLE carries this env's values |

Neither proves **the declaration is correct**. `GITHUB_REDIRECT_URI` pointing at
the admin host (`6d74bc3d`) or a `CORS_ORIGINS` missing `app://bundle`
(`b1146f16`) passes both: the files agree, and the bundle faithfully carries the
wrong value. **That third detector — the semantic invariant check — exists
nowhere, and every Tier-1 historical bug in this area is one of its violations.**

**Exhibit A (§2): a live latent bug, source-level confirmed today.** In `dev`,
`config.k:21` hardcodes `http://localhost:3000/auth/github/callback` while
`main.k` derives `APP_URL`, `CORS_ORIGINS`, `ALLOWED_REDIRECT_HOSTS` and
`VITE_APP_URL` from `fp.allocate_port(3000, _key)`. On any worktree not drawing
port block 0, those four agree and the GitHub redirect disagrees with all four.
Same shape as `6d74bc3d`, hosts swapped. A 200ms check finds it.

Recommendations, in order: **(A)** semantic invariants as KCL assertions —
~200 lines, an afternoon, rides CI that already runs; **(B)** derive the backend
literals from the `reliant_endpoints` table that already exists and already
feeds the frontend half; **(C)** three small fixes to the gates that exist.

---

## 1. The pipeline — where a value gets to be wrong

```
proto/config/v1/config.proto
        │  forge generate
        ▼
deploy/kcl/config_gen.k          AppConfig schema + <BINARY>ConfigEnvMap
deploy/kcl/frontend_config_gen.k <Frontend>Config + runtime projection
        │
        ├── deploy/kcl/<env>/config.k   ← typed per-env values (AppConfig instance)
        │        prod/config.k:18  github_redirect_uri = "https://app.reliantlabs.io/..."
        │
        ├── deploy/kcl/<env>/main.k     ← RAW forge.EnvVar literals, env_merge'd on top
        │        prod/main.k:274  CORS_ORIGINS = "...,app://bundle"
        │
        └── deploy/kcl/lib/env.k        ← shared helpers + the reliant_endpoints table
                 ├─► reliant_web_vite_env(env) ──────┐
                 │                                   ├─► VITE_* baked into a Vite build
                 └─► reliant_desktop_release_config ─┘
                          │  kcl run desktop_release.k -D env=prod -S release_config
                          ▼
                     reliant/electron/release.config.json  (committed, GATED)
        │
        │  forge env render / forge env deploy
        ▼
Kubernetes Deployment env: [...]  → Go: os.Getenv / cfg.GetX()
```

**Layer 1 — proto/schema.** Type-safe, forge-enforced. Not a bug source.

**Layer 2 — `<env>/config.k`.** KCL type-checks the *field name*; the *value* is
an unconstrained `str`. `github_redirect_uri` lives here — `6d74bc3d` and
Exhibit A are both Layer-2: well-typed strings that are semantically wrong.

**Layer 3 — raw `forge.EnvVar` literals in `<env>/main.k`.** The dangerous
layer, and where most load-bearing redirect vars live. `APP_URL`,
`CORS_ORIGINS`, `ALLOWED_REDIRECT_HOSTS`, `WORKSPACE_BASE_DOMAIN` are raw
literals merged over the typed config via `forge.env_merge` — **no schema, no
type, no cross-check.** `b1146f16` is Layer-3.

They are also duplicated *within* one env file (§5, I6).

**Layer 4 — the Vite bake.** Compiled into minified JS. No runtime test of a dev
server and no backend health check can observe it. `7f403355`, `a9c4e172`.

**Layer 5 — the desktop artifact.** Crosses a repo boundary as a committed
generated file, then `jq` → `$GITHUB_ENV` → build. Best-gated layer — and,
tellingly, the one that had the worst failure (`2c859d2d`, `a9c4e172`). The gate
is a response to the outage, not a precaution.

**A sixth place: the runtime ladder.** `reliant/web/src/lib/constants.ts`
`getAppURL()` resolves `VITE_APP_URL` → `window.location.origin` (only if
`!isLocalOrigin`, which rejects `file:`, `app:`, `localhost`, `127.0.0.1`,
`[::1]`) → hardcoded `DEFAULT_APP_URL = "https://app.reliantlabs.io"`.
`lib/env.k:528-535` records what that fallback did: the **dev** desktop app sent
GitHub sign-in to the **prod** callback, and prod worked "only by COINCIDENCE"
because the constant happened to equal prod's origin (`72390d35`, `3fcd9f79`).
The mitigation landed; **the coincidence is still load-bearing** — nothing
asserts `DEFAULT_APP_URL == reliant_endpoints("prod").app_url`.

---

## 2. Exhibit A — a live latent bug the check finds today

Credit: **auth/OAuth agent**. Independently re-derived here by reading both
files. **Source-level confirmed; runtime symptom unconfirmed** — I did not run a
non-block-0 worktree to observe a failed GitHub sign-in, and that caveat is
carried deliberately rather than smoothed away.

`deploy/kcl/dev/config.k:21`:

```python
github_redirect_uri = "http://localhost:3000/auth/github/callback"   # hardcoded
```

`deploy/kcl/dev/main.k` derives everything else from an allocated port:

```python
_reliant_web_port      = fp.allocate_port(3000, _key)          # :326
_internal_console_port = fp.allocate_port(3002, _key)          # :329
:633  CORS_ORIGINS           = "http://localhost:${_reliant_web_port},..."
:710  ALLOWED_REDIRECT_HOSTS = "localhost:${_reliant_web_port},..."
:1543 APP_URL                = "http://localhost:${_reliant_web_port}"
:1952 VITE_APP_URL           = "http://localhost:${_reliant_web_port}"
```

`allocate_port(base, key)` returns `base + block(key)*100`. For block 0 the
frontend is `:3000` and all five agree. **For any other block — every parallel
worktree beyond the first, which this project runs by design ("10 reliant
processes at a time," per repo memory) — the four derived values move together
to `:3100`, `:3200`… and `github_redirect_uri` stays pinned at `:3000`.**

Four values agree with each other; the fifth silently disagrees with all four.
That is `6d74bc3d` with the hosts swapped, and it is present in the tree now.

Why this is the strongest possible evidence for the proposal: it is not
history. Invariant I3 (§5) fails on it in ~200ms, at author time, with an error
message naming both values. Also note what would *not* have found it: the
value is well-typed, the KCL renders fine, `forge ci validate-kcl` passes, and
in the block-0 worktree where a developer most likely tests, it works.

**The fix is the same as the detector**: derive `github_redirect_uri` from
`APP_URL` rather than typing it. That is Proposal B, and it makes the class
unrepresentable rather than merely caught.

---

## 3. Drift vs. semantics — the crux

Three distinct properties, three distinct detectors:

| Property | Detector | Exists? |
|---|---|---|
| the generated file matches the declaration | drift gate (`--check`) | desktop only |
| the built artifact carries the declared values | artifact assertion (grep `dist/`) | web only, hostnames hardcoded |
| **the declaration is internally coherent** | **semantic invariant check** | **nowhere** |

A drift gate is a *consistency* check between two representations of one value.
It is structurally incapable of noticing the value is wrong. Every Tier-1 bug
below is a wrong-value bug, and all of them pass every gate in the repo today.

### Where the semantic check goes: KCL `assert`, at author time

**Verified: `forge ci validate-kcl` can carry assertions today with no forge
change.** Two facts:

- KCL `assert <cond>, "<msg>"` is already used throughout this codebase and
  fires at evaluation: `config_gen.k:213` (with an exemplary message naming the
  offending keys and the fix), `lib/builds.k:195/273/275`, `dev/main.k:424/451`,
  `desktop_release.k:21`.
- `forge ci validate-kcl` already runs on every PR (control-plane `ci.yml:252`),
  in a job that installs KCL, and it resolves *and renders*. An assertion that
  fires during evaluation fails that job.

So the marginal cost of Tier-1 enforcement is **writing the assertions** — no
new workflow, no new tool, no forge feature. It also fires earliest of any
option: `kcl run` fails in the editor, before a commit exists. Given the user's
pain — "we don't discover until after we deploy" — author-time is the strongest
available position.

**On the sibling proposal (billing + auth agents): "assertions over
`forge env render` output."** I agree with the invariants and differ on the
mechanism, and the difference is worth stating because it is the one place three
investigations diverge. A Go test shelling `forge env render <env>` per env is
entirely feasible — the command is documented as read-only, cluster-free, and
"usable as a CI gate," and it exits non-zero on a KCL error. But relative to
KCL asserts it fires **strictly later** (CI only, never in the editor), catches
**strictly less** (it validates rendered output, so it cannot see that two
literals were *meant* to be one fact), needs KCL installed in the Go test env,
and creates a **second place invariants are written** — the exact duplication
this document is about. Same invariants, better host. The three agents agree on
the substance; take the KCL siting.

(KCL `check:` blocks are the schema-instance equivalent — the natural home for
constraints on an `Endpoints` schema. Module-level `assert` is right for
constraints spanning bindings. Both fire under `kcl run`, so both are gated
identically.)

**Implementation hazard, and it is the bug class under study:** a KCL `assert`
inside a lambda fires only when the lambda is **called**. If an env's `main.k`
never calls the checker, the assertion silently does not run — the
"unreachable declaration" failure `desktop_release.k`'s own comment warns about
at length (a workload that "never ran, and could not have"). Put the checks at
**module level** in `lib/env.k`, iterating a materialized env list, so importing
the module runs them. Then adding an env without checking it requires deleting a
line rather than forgetting one.

---

## 4. The silent-skip hazard — a check that no-ops looks green

Verified in `sync-release-config.mjs`. `renderFromKCL` returns `{unavailable}`
when there is no sibling checkout **or** when `kcl` is not installed
(`err.code === "ENOENT"`), and `--check` then does:

```js
if (unavailable) {
  console.log(`sync-release-config: skipping drift check — ${unavailable}`);
  validate(committed, committed.env);
  process.exit(0);          // ← GREEN
}
```

**Two independent ways the authoritative drift gate exits 0 without comparing
anything.** The skip is deliberate and correct for forks (an outside
contributor cannot check out a private repo), but it makes the *authoritative*
and *best-effort* invocations indistinguishable by exit code.

1. **In reliant's `pr-ci.yml:41` (`release-config`) the script ALWAYS skips** —
   there is no control-plane checkout and there cannot be. It does run
   `validate()` on the committed file (a real check: no empty, no localhost),
   but never the drift comparison. The job is weaker than its name reads.
2. **In control-plane's `ci.yml` it does genuinely run today** — KCL installed
   at `:250`, `reliant-sibling` checked out at `:266`, `CONTROL_PLANE_DIR` set.
   But nothing *asserts* that, and `ci.yml:~245` records this exact failure
   already happening once: *"This job passed only because a cache hit skipped
   the install and `forge ci validate-kcl` found kcl on the default PATH
   anyway."* A gate that has already passed for the wrong reason should not be
   trusted to announce its own liveness.
3. **The fix is four lines.** Add `--require-render` (or honour `CI_STRICT=1`)
   making `unavailable` a hard `fail()`; pass it only from control-plane's
   `ci.yml:271`. Forks and `pr-ci.yml` keep the lenient default.

Generalized: **a check that can no-op must announce in its exit status whether
it ran.** Where a gate is authoritative, "input missing" is a failure, not a
skip. This applies to every detector proposed here.

---

## 5. The invariants

Consolidated from all three investigations. Prefix **I-B** = contributed by the
billing/daemon agent, **I-A** = auth/OAuth agent, unprefixed = this one.
The auth agent's five couplings — `APP_URL` ↔ `ALLOWED_REDIRECT_HOSTS` ↔
`CORS_ORIGINS` ↔ `VITE_APP_URL` ↔ `github_redirect_uri` — are I1-I4 below.

### Tier 1 — each maps to a historical outage or a live bug; none is enforced

**I1. `host(APP_URL) ∈ ALLOWED_REDIRECT_HOSTS`.** Consumer:
`svcbilling/service.go` `validateRedirectURL` via `providers.go:707`. Stripe
`successURL`/`cancelURL` are minted from the app origin and validated against
this list; disagreement means a user completes a checkout — money moves — and
lands on an error.

*Correction to the original brief, from the billing agent and confirmed here:*
the three `successURL`/`cancelURL` call sites are **not** divergent duplicated
validation. All four RPCs funnel through one `checkRedirectURL` helper
(`service.go:387-393`), and `service_test.go:94-151` covers every branch. Not a
gap; do not spend effort there.

**I2. Every origin the app can present ∈ `CORS_ORIGINS`** —
`{app(E), the Firebase site origin, app://bundle}`. `app://bundle` is the
packaged renderer's origin; go-chi/cors matches **exactly**, no scheme or suffix
wildcarding. This is `b1146f16`. Must hold independently for `_reliant_api_env`
and `_admin_env`, doubling the surface.

**I3. `GITHUB_REDIRECT_URI == app(E) + "/auth/github/callback"`.** `6d74bc3d`,
and **Exhibit A** (§2) — the live one. A three-way constraint whose third party
is outside the repo (the GitHub OAuth app's registered callback); KCL enforces
the first two legs only, and the assert message must say so.

**I4. `VITE_APP_URL == APP_URL` for the same env.** Backend `APP_URL`
(prod/main.k:270) is a literal; `VITE_APP_URL` (lib/env.k:572) comes from the
table. They agree by inspection only — the `72390d35` class.

**I5. Desktop `main` and `vite` blocks name the same hosts.** Holds **by
construction** — both built from one `_e`. The model for everything else, and
the reason Proposal B ranks where it does.

**I-B5 / I6. Duplicated vars across containers must agree — a finding, not
just a rule.** Verified independently:

```
prod/main.k:275   ALLOWED_REDIRECT_HOSTS = "app.reliantlabs.io,reliant-prod.web.app"  (_admin_env)
prod/main.k:452   ALLOWED_REDIRECT_HOSTS = "app.reliantlabs.io,reliant-prod.web.app"  (_controller_env)
```

Byte-identical today, with **nothing keeping them so**. Same shape for
`CORS_ORIGINS` (:240 `_reliant_api_env` vs :274 `_admin_env`) and
`WORKSPACE_BASE_DOMAIN` (:269, :481, lib/env.k:256). A future divergence —
admin-server allowing `app://bundle` while reliant-api does not — presents as
"the desktop app works for billing but not for chat," which is miserable to
diagnose. This is latent, not yet broken; Proposal B removes it outright.

**I-B3. `DEPLOY_ENV` must be a value `plansconfig.SelectByDeployEnv`
recognizes.** Verified (`plansconfig.go:66-81`): it substring-matches
`preprod` → staging catalog, `staging` → staging, `prod` → **prod, the only
path emitting LIVE Stripe price IDs**, and **`default` → the dev null-price
catalog**. There is no error return for an unknown value — an unrecognized
`DEPLOY_ENV` silently yields null prices. `e2e/main.k:250` sets
`DEPLOY_ENV = "e2e"` and its comment says so deliberately, which is correct and
also proves the silent-default is load-bearing rather than theoretical.

Two sharp edges worth asserting: the match is `strings.Contains`, so a
hypothetical `"prod-canary"` selects LIVE prices, and `"nonprod"` **also
contains `prod`** and would select LIVE prices. Assert `DEPLOY_ENV` against an
explicit allowlist per env rather than trusting the substring.

**I-B4. The implied catalog's price mode must match the Stripe key's mode.**
Live key + test price IDs (or the reverse) fails at the Stripe API, after the
user has clicked upgrade. `dev-k8s/main.k` documents its `stripe_price_id`s as
deliberately null. Assert: `DEPLOY_ENV` implying the prod catalog ⟺ the env's
`stripe_secret_key` resolves to a live-mode key. **Partially checkable only** —
KCL sees a `ConfigSecretRef`, not the key's value, so this can assert the
*catalog/env pairing* but not the key's actual mode. The residual belongs in the
provider-probe (§8).

### I-B2, corrected: `ALLOWED_REDIRECT_HOSTS` non-empty where `isDevReliantEnv` is false

The billing agent's framing is right — key the invariant off the code's own
notion of "real env" rather than inventing a parallel one. Verified
(`service.go`):

```go
func isDevReliantEnv(env string) bool {
	switch strings.ToLower(strings.TrimSpace(env)) {
	case "local", "dev", "development":
		return true
	}
	return false
}
```

**Correction, and it matters for how the assert is written.** The value passed
in is **`cfg.Environment`** (`providers.go:716` passes `cfg.Environment` as
`reliantEnv`), i.e. the **`ENVIRONMENT`** var — *not* `RELIANT_ENV`, which also
exists in the KCL and carries different values. Getting this wrong would write
an assert keyed off the wrong variable, which is exactly this document's
subject. Resolved per env:

| env | `environment` (config.k) | `isDevReliantEnv` | ⇒ needs `ALLOWED_REDIRECT_HOSTS`? | set? |
|---|---|---|---|---|
| dev | `development` | true | no (fails open, WARN) | yes, :710 |
| dev-k8s | `development` | true | no | **no** — correct |
| e2e | `e2e` | **false** | **yes** | yes, :278 |
| prod | `production` | false | yes | yes, :275/:452 |

So the invariant is **satisfied everywhere today**, and `e2e` is the
interesting row: `environment = "e2e"` is not in the dev set, so e2e fails
*closed* — and it sets the var, so it works. Nothing enforces that pairing;
changing `e2e`'s `environment` label, or dropping its
`ALLOWED_REDIRECT_HOSTS`, silently breaks e2e billing. Worth asserting exactly
because it currently holds by care rather than by construction.

### Tier 2

**I7. `DEFAULT_APP_URL` (reliant `constants.ts`) `== reliant_endpoints("prod").app_url`.**
Cross-repo, true by coincidence.

**I8. `CORS_ALLOW_CREDENTIALS` must not combine with `*`.** Already rejected at
startup (`config_gen.k:21`) — an example of the good pattern.

**I9. Zitadel / internal-console redirect coherence.** Derived from
`INTERNAL_CONSOLE_ORIGIN + INTERNAL_CONSOLE_BASE_PATH`; the schema comment names
the failure — *"A wrong value yields a redirect_uri mismatch AFTER the user has
already typed a password."* Derived-by-construction on the default path, but
overridable via `zitadel_admin_redirect_uris`, which the in-cluster Job passes
literally. **The override is the unguarded gap.**

**I-A11. Every env branch in `lib/env.k` has a corresponding `deploy/kcl/<env>/`
directory.** Flagged by the auth agent, confirmed: `reliant_endpoints` has a
full `preprod` branch (`lib/env.k:473-485`) and `reliant_web_vite_env` /
`desktop_release.k` both accept `preprod`, but **there is no
`deploy/kcl/preprod/`** — the directory listing is `dev`, `dev-k8s`, `e2e`,
`prod`. Consistent with `deploy-reliant-web.yml:288` ("prod is the only
environment this workflow deploys (**preprod was deleted**)"). So this is very
likely dead config, but the desktop release path still accepts `-D env=preprod`
and would render endpoints for an environment that does not exist. Pre-launch,
deletion is the right call — but it is a live design question, not a mechanical
cleanup, so it is flagged rather than assumed. The invariant is cheap and worth
having either way.

**I10. Supabase / IdP allowlist.** Out of scope — see §8. Note prod's
`VITE_SUPABASE_URL` is the vanity domain `dash.reliantlabs.io` while the JWT
**issuer** is the underlying project ref (prod/main.k:242-247 explains why).
Those must stay deliberately different; an "obvious" cleanup unifying them
breaks issuer validation. Worth an assert **pinning the difference** so nobody
fixes it.

### Failure taxonomy

| Class | Behaviour | Examples | Reaches prod? |
|---|---|---|---|
| **A. Absent → fail loud at startup** | refuses to boot | `CORS_ALLOW_CREDENTIALS`+`*`; `TLS_CERT_PATH` without `TLS_KEY_PATH`; empty `ZITADEL_OPERATOR_EMAILS` | rarely |
| **B. Absent → fail closed at first use** | boots, feature refuses | `ALLOWED_REDIRECT_HOSTS` empty in non-dev: startup `logger.Error`, then every Stripe session refused | yes, but visibly and safely |
| **C. Absent → silently degrade** | boots, wrong behaviour, no log | `VITE_APP_URL` unset → hardcoded default; `VITE_CLI_DEFAULTS_BAKED` unset (`7f403355`); unrecognized `DEPLOY_ENV` → null-price catalog | **yes** |
| **D. Present but wrong** | boots, wrong value, no log at all | `6d74bc3d`, `b1146f16`, **Exhibit A** | **yes, and worst** |

A and B are already handled well. C and D are where every historical bug lives,
and **neither is detectable at runtime by the process holding the value** —
`admin-server` cannot know that `app.reliantlabs.io` belonged in
`ALLOWED_REDIRECT_HOSTS`. Only the env's declaration knows. **Therefore the
detector must live where the declaration lives.** That is the central
conclusion, and it is why Proposal A outranks everything.

What turns an absence into class C rather than B is a **convenience default**:
`AppConfig.app_url = "http://localhost:3000"`, `github_redirect_uri`'s localhost
fallback, and `SelectByDeployEnv`'s silent `default:` branch are all
silent-in-prod by construction.

---

## 6. Proposals

### A. Semantic invariants as KCL assertions — **do this first**

Module-level in `lib/env.k`, iterating an explicit env list so importing the
module runs them. House error-message style: explain the why, name the fix.

```python
_check_env_origins = lambda env: str -> bool {
    _e = reliant_endpoints(env)
    _host = _e.app_url.replace("https://", "").replace("http://", "")
    assert _host in _e.redirect_hosts, \
        "env '${env}': APP_URL host '${_host}' is not in ALLOWED_REDIRECT_HOSTS " \
        "'${_e.redirect_hosts}'. A Stripe checkout would COMPLETE and then be refused " \
        "by checkRedirectURL (svcbilling/service.go) — the user is charged and lands " \
        "on an error. Add the host to reliant_endpoints('${env}').redirect_hosts."
    assert "app://bundle" in _e.cors_origins, \
        "env '${env}': the packaged desktop origin 'app://bundle' is missing from " \
        "CORS_ORIGINS. Every desktop RPC would be blocked by the browser with NO " \
        "server-side error (regression b1146f16). go-chi/cors matches EXACTLY."
    assert _e.github_redirect_uri == _e.app_url + "/auth/github/callback", \
        "env '${env}': GITHUB_REDIRECT_URI '${_e.github_redirect_uri}' does not sit " \
        "under APP_URL '${_e.app_url}' (regression 6d74bc3d; and the dev port-block " \
        "bug). NOTE: changing this ALSO requires updating the GitHub OAuth app's " \
        "registered callback — KCL cannot check that half (see 07)."
    assert _e.deploy_env in ["dev", "e2e", "staging", "preprod", "prod"], \
        "env '${env}': DEPLOY_ENV '${_e.deploy_env}' is not an allowlisted value. " \
        "plansconfig.SelectByDeployEnv has NO error branch — an unrecognized value " \
        "silently selects the null-price dev catalog, and because it matches by " \
        "substring, anything containing 'prod' (e.g. 'nonprod') selects LIVE prices."
    True
}
_ = [_check_env_origins(e) for e in ["dev", "dev-k8s", "e2e", "prod"]]
```

- **(a) Bugs caught:** **Exhibit A (live, today)**; `6d74bc3d` (I3);
  `b1146f16` (I2); `d277c59e` (I3, config-source-of-truth move);
  `72390d35`/`3fcd9f79` (I4); `7f403355` if `VITE_CLI_DEFAULTS_BAKED` becomes a
  table field with a prod assert; `bb0e939a` (the empty-interpolation port-block
  key) if a non-empty/well-formed assert is added on composed keys. That is six
  historical bugs plus one present one — matching the billing agent's
  "six-plus" assessment, which I concur with.
- **(b) Fires:** **author time** (`kcl run` in the editor), then PR CI via the
  existing `forge ci validate-kcl`, then again at `forge env deploy` (renders
  first). Three gates, all pre-prod.
- **(c) Cost:** ~200 lines of KCL — I agree with the billing agent's estimate —
  one afternoon, seconds to run. No new workflow, tool, or forge change.
  Upkeep is low for the reason that agent identified and which is worth
  repeating: **invariants are relationships, so they do not churn when values
  change.** Changing prod's hostname edits one table row; the assertions are
  untouched. They also replace four prose comments that currently explain the
  same couplings to human readers only.
- **(d) Cannot catch:** third-party registries (§8); a coherent-but-wrong table
  (change `app_url` to `app.wrong.io` and update `redirect_hosts` to match and
  everything passes); values injected outside KCL (release.yml's Sentry/Statsig
  secrets, anything set in the Actions UI); the actual *mode* of a Stripe key
  behind a `ConfigSecretRef`; and a **build** that failed to receive a correctly
  declared value — that is what §6C's artifact gates are for. The two detectors
  are complements, not alternatives.

### B. Derive the backend literals from `reliant_endpoints`

`reliant_endpoints` (`lib/env.k:471`) is already the single declarative table
the brief asks for — its own comment explains that three copies of the same
hostnames drifted and shipped a desktop build with no
`VITE_CONTROL_PLANE_API_URL`. **It feeds only the frontend half.**

Extend it with `web_app_origin`, `redirect_hosts`, `cors_origins`,
`workspace_base_domain`, `github_redirect_uri`, `deploy_env`; add
`reliant_backend_origin_env(env)` returning the `forge.EnvVar` list for
`APP_URL` / `CORS_ORIGINS` / `ALLOWED_REDIRECT_HOSTS`; have each env's `main.k`
call it, and **both** CORS-needing workloads call the same lambda. Cover
`dev-k8s` and `e2e` too, or the table stays a prod concept while their literals
keep drifting. In `dev`, derive `github_redirect_uri` from the allocated port —
which is Exhibit A's fix.

- **(a)** Same bugs as A, converted from *caught* to *unrepresentable* — the
  mechanism that already makes I5 and I6 true.
- **(b)** Author time (they cease to be expressible).
- **(c)** ~1 day. Mechanical, but touches four env `main.k` files other agents
  may be editing, and the render must be byte-compared
  (`forge env render prod > before.yaml`, refactor, diff) because
  `lib/env.k:569-571` carries an explicit fidelity contract about env-var
  **ordering**. Maintenance is negative: a new env becomes one table row instead
  of ~12 literals.
- **(d)** Same residual as A.

Sequence A before B: A is an afternoon and immediately guards the refactor B
performs.

### C. Fix the gates that already exist — small, high leverage

**C1. De-hardcode `deploy-reliant-web.yml`'s expected hostnames.** Lines
:295-297 hand-type `api.reliantapi.com` / `admin.reliantapi.com` /
`dash.reliantlabs.io` — a **fourth** copy of the table, in YAML — with an
`if env != prod` bail because it cannot generalize. Render them instead:
`kcl run deploy/kcl/desktop_release.k -D env=$env -S release_config --format json`
already emits this list (its `vite` block *is* `reliant_web_vite_env`), so a
`jq -r '.vite | .VITE_API_URL, .VITE_CONTROL_PLANE_API_URL, .VITE_SUPABASE_URL'`
replaces the literals and the bail. ~10 lines.

**C2. Make the authoritative drift gate prove it ran** (§4). `--require-render`
turning `unavailable` into a hard failure, passed only from `ci.yml:271`. ~4 lines.

**C3. Pin `DEFAULT_APP_URL`** inside `sync-release-config.mjs`'s existing
`validate()`: assert it equals `reliant_endpoints("prod").app_url`. Converts I7
from coincidence to checked fact. ~20 lines, runs in both repos.

- **(a)** C1 generalizes the gate that catches `a9c4e172`/`2c859d2d` to future
  envs; C2 protects every gate from the failure `ci.yml:245` already records;
  C3 covers `72390d35`/`3fcd9f79` at the constant itself.
- **(b)** C1 pre-deploy; C2/C3 PR CI. **(c)** All three well under a day.
- **(d)** C1 checks *presence of a substring* in the bundle, not semantic
  correctness, and is bundler-fragile (a host split across chunks would
  false-negative). Treat a miss as a hard failure anyway — false positives are
  the cheap direction.

### D. A source-level lint: nothing but `getAppURL()` builds a redirect from `window.location.origin`

Inherited from the auth agent. It fits here as a **sibling rule, not a
rendered-config check** — it is a static check over `reliant/web/src`, so it
belongs beside these invariants conceptually while living in a different
mechanism (an ESLint rule or a ~20-line grep test in the web test suite).
Recorded here so it is not dropped; whoever synthesizes should decide whether it
ships with A or with the frontend area's recommendations.

- **(a)** reliant `72390d35` ("never send a loopback origin as the OAuth
  redirect") — directly. **(b)** PR CI / pre-commit. **(c)** ~20 lines.
  **(d)** Cannot catch a redirect assembled indirectly (via a variable or a
  helper), so it is a tripwire against the specific regression, not a proof.

### E. Fail-fast at startup — **I concur with the billing agent's objection, and draw the line**

The billing agent recommends **against** boot-time refusal on incoherent
billing config, and the reasoning is correct: billing currently fails closed
*per request* with a loud `logger.Error`, so a billing misconfiguration
degrades **billing alone**. Refusing to boot converts it into a full
control-plane outage — including daemon lifecycle, which is how users' work
actually runs. Trading a broken upgrade button for a dead control plane is a bad
trade, and I withdraw the general fail-fast suggestion.

There is a defensible middle, and here is where I would draw it:

**Fail fast only on a var whose absence or wrongness already breaks every
request path, so refusing to boot removes no working functionality.** By that
test: `DATABASE_URL`, the auth issuer/JWKS material, `INTERNAL_SERVICE_SECRET`.
A pod that cannot serve an authenticated request is not degraded, it is down —
crashing is strictly more legible than 100% 500s, and these already largely
fail this way.

**Warn loudly and fail closed per-request for anything scoped to one feature:**
`ALLOWED_REDIRECT_HOSTS`, `STRIPE_*`, `GITHUB_*`, `LITELLM_*`. Exactly today's
behaviour, and it is right.

The one change I would still make is narrow and does not touch boot: **remove
the two silent defaults that create class C.** `AppConfig.app_url`'s
`"http://localhost:3000"` and `github_redirect_uri`'s localhost fallback should
be `""`, with the *consumer* logging an error when empty and
`environment == "production"`. That converts silent-wrong to loud-wrong without
converting anything to a crash. Similarly, `SelectByDeployEnv`'s `default:`
branch should log at minimum — silently selecting a null-price catalog on an
unrecognized `DEPLOY_ENV` is the same hazard one layer down.

- **(a)** No historical bug maps cleanly — hardening against a shape, hence last.
- **(b)** Startup / first-use. **(c)** Small, but touches product code; a wrong
  fail-closed check takes prod down, so land behind a canary.
- **(d)** Cannot catch present-but-wrong (class D) at all.

---

## 7. Forge

Everything A, B and C need, forge already does: `forge ci validate-kcl` runs
assertions on every PR; `forge env render` is documented as a CI gate ("Exits
non-zero with the KCL error when the environment does not render") with
`--target`, `--kind`, `--list`, `--fail-on-write`. **No workaround is required
and none is proposed.**

**Genuine gap: no `forge env render --all` / no all-envs `validate-kcl`.**
Every gate here wants "render every declared environment and fail if any does
not." Today that is a hand-maintained `for env in ...` loop in YAML — and a
hand-maintained env list is exactly what goes stale when someone adds an env
(and is adjacent to I-A11's orphaned `preprod` branch). Forge knows the list
from `forge.yaml`. Proposed: `forge ci validate-kcl` renders **every** declared
env (confirm whether it already does before building on it), or
`forge env render --all` exists. Where: `forge/internal/cli/env_render.go` for
the flag, `forge/internal/cli/ci.go` (`newCIValidateKCLCmd`, line 120) for the
CI behaviour. ~50 lines. This is the difference between "our assertions cover
the envs someone remembered" and "our assertions cover every env that exists."

**Already-filed friction, noted:** `deploy-reliant-web.yml:138-149` records that
`forge build <env> -t reliant-web` does not work for a `GitSource` frontend
(`-t` resolves against `forge.yaml`'s `frontends:`, unpopulated here), so the
workflow scrapes build dir and build env out of
`forge env deploy --dry-run | tee` with `sed`. Correctly reported rather than
papered over. Relevant because C1 makes that scraped output load-bearing for a
*gate* rather than just a build; closing the forge gap simplifies C1.

---

## 8. Coverage — what this check reaches, and what it structurally cannot

**Covered — all three config surfaces, at author time:**

| Surface | Reached how | Invariants |
|---|---|---|
| **Backend service env, per env** (`deploy/kcl/<env>/main.k` + `config.k`) | the assertions evaluate the same bindings the manifests are rendered from | I1, I2, I3, I6, I-B2, I-B3, I9 |
| **Frontend baked Vite env** (`reliant_web_vite_env`) | projected from `reliant_endpoints`, which the assertions constrain | I4, I7 |
| **Desktop release config** (`reliant_desktop_release_config`) | same table; plus the existing drift gate and `validate()` | I5, I4 |

**Structurally cannot catch — the honest residual:**

1. **Provider-side registries.** The GitHub OAuth app's registered callbacks,
   the Supabase Auth allow-list, Google console settings, Zitadel's registered
   URIs. **Out of scope by boundary, not by omission**: they live outside both
   repos, a human can change them with no commit, and no static check can reach
   them. Owned by the observability agent as a live provider-acceptance probe —
   **see 07**. This is the clean seam: *these five couplings are checkable; the
   provider registration is not.*
2. **A coherent-but-wrong table.** Change `app_url` and update every dependent
   value to match and all assertions pass. Only a human or a live probe knows
   the domain is wrong.
3. **Secret *values*.** KCL sees a `ConfigSecretRef`, never the bytes. So I-B4's
   "live key with test prices" is only half-checkable: the catalog/env pairing
   yes, the key's actual mode no.
4. **A build that dropped a correctly-declared value.** The bundle is the only
   witness — §6C's artifact gates, not this check.
5. **Manifest-vs-cluster divergence.** A stale rollout or a manual
   `kubectl edit` makes the rendered config right and the running config wrong.
   Only a post-deploy probe sees that.
6. **Anything injected outside KCL** — release.yml's Sentry/Statsig secrets,
   values set in the GitHub Actions UI.

---

## 9. Recommendation

**A** (the KCL assertions) first: ~200 lines, an afternoon, rides CI that
already runs, fires at author time, covers I1-I4 plus I-B2/I-B3 — six-plus
historical bugs **and one live bug in the tree right now**. **Then B** (collapse
the backend literals into `reliant_endpoints`), converting those from checked to
unrepresentable and fixing Exhibit A at its root. **Add C** (~1 day for all
three): de-hardcode the web artifact gate, make the drift gate prove it ran, pin
`DEFAULT_APP_URL`. **D** as a sibling lint. **E** narrowed to removing two
silent defaults — not boot-time refusal.

Roughly two days, no new infrastructure, no forge change, closing every Tier-1
invariant plus the silent-skip hazard.

**The single highest-value invariant is I3** — `GITHUB_REDIRECT_URI` must sit
under `APP_URL`. It is the only one with both a historical outage (`6d74bc3d`)
and a live present-day violation (Exhibit A), it is two lines to assert, and it
is the one whose failure is hardest to diagnose from symptoms: OAuth simply
returns the user to the wrong place, with nothing logged server-side.

**Open item for whoever synthesizes:** the orphaned `preprod` branch in
`lib/env.k` with no `deploy/kcl/preprod/` directory (I-A11). Almost certainly
dead config — `deploy-reliant-web.yml:288` says "preprod was deleted" — but
`desktop_release.k` still accepts `-D env=preprod`, so a desktop release could
be cut against endpoints for an environment that does not exist. Pre-launch,
deleting it is fine; it needs a human decision, not a guess.
