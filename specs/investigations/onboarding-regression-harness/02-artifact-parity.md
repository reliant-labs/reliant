# 02 — Artifact / runtime parity

Scope: regressions invisible to any test run against `npm run dev`, because the
defect is *compiled into the artifact* or is a property of the *packaged
runtime*. Companion to `BRIEFING.md` — VERIFIED facts there are not re-derived.

**The headline finding is not the one I expected.** The post-build static
assertion this investigation was chartered to design **already exists, is
excellent, and is deployed on exactly one of the two artifact paths.**
`control-plane/.github/workflows/deploy-reliant-web.yml` gates the hosted SPA
with three assertions over `dist/`. Reliant's own `release.yml`, which builds
the desktop artifact users actually download, has **zero**. So the correct
recommendation is not "build a detector" but "**move the detector that works to
the path that lacks it**," which is dramatically cheaper and better-evidenced
than anything I would have designed from scratch.

The second finding is that the frontend e2e gate was **dark** — cancelled at
timeout in ~50 consecutive runs — during the window when several of these
regressions shipped. Gate trustworthiness outranks new harness design.

---

## 1. The parity surface

Four runtimes. CI *proves* behaviour in only one of them.

| # | Runtime | Origin | Config | Proxy? | Post-build assertion? | Ever launched in CI? |
|---|---|---|---|---|---|---|
| 1 | Vite dev | `http://localhost:<port>` | shell env | yes | n/a | yes |
| 2 | `vite preview` over `dist/` | `http://localhost:4173` | baked `VITE_*` from `release.config.json` | no | no | yes (`e2e-frontend`) |
| 3 | Firebase Hosting (prod web) | `app.reliantlabs.io` | baked `VITE_*` from KCL | no | **yes — 3 gates** | no |
| 4 | Packaged Electron | `app://bundle` | baked `VITE_*` **+** async `window.RELIANT_CONFIG` | no | **no** | **no — built, then discarded** |

Runtime 4 is where the user-visible desktop regressions live, and it is the one
row with no assertion and no launch.

### 1.1 Origin scheme (`app://`)

`electron/src/app-protocol.js` registers `app://bundle` as `standard` + `secure`
and serves `web/dist` with an SPA fallback that refuses to rewrite anything
carrying a file extension. Before `487fb19a` the renderer was `loadFile()`'d,
giving a `file://` origin where root-absolute `/assets/…` resolved against the
*filesystem* root — a blank window in which `did-finish-load` fired normally.

`web/src/lib/protocol.ts::isPackagedRendererOrigin` centralises the
discriminator (matching both `app:` and `file:`) because the question used to be
asked as `protocol === "file:"` in seven places, all of which silently flipped to
their browser branch at the scheme change (`3fcd9f79`). Control-plane learned the
same origin for CORS (`b1146f16`).

**No CI job ever produces a page whose `window.location.protocol` is `app:`.**
vitest is jsdom on `http:`; Playwright preview is `http:`. Every branch gated by
`isPackagedRendererOrigin()` is untested in its true state — and that predicate
decides whether to block on async config, whether to use same-origin transport,
and what origin is handed to an identity provider.

### 1.2 Base path and route depth

`base: "/"` in `vite.config.ts`, with a comment explaining that `"./"` breaks any
2+-segment route: `./assets/x.js` resolves against the route's directory, 404s,
receives the SPA fallback's `index.html`, and dies with a MIME error. This is the
current branch's bug (`ed96ce8`), and it interacts with §1.1 — `app://` works
*because* the base is absolute.

`web/src/routes.tsx` declares **21 routes with 2+ segments**, including every
external landing site: `/auth/callback`, `/auth/github/callback`,
`/settings/connectors/authorize`, `/oauth/consent`, `/m/chats/$chatId/workflow`.
That list is the highest-value artifact in this document. It is exactly the set
of URLs an identity provider or Stripe redirects a user to — arrived at *cold*,
with no warm bundle, which is the only condition under which the bug manifests.
Hence this defect class presenting as an "OAuth/billing redirect" bug.

### 1.3 Baked `VITE_*` vs runtime `RELIANT_CONFIG`

Two channels reach the packaged renderer, from one KCL declaration:

- **Baked**: `.vite` block of `electron/release.config.json` → exported to env →
  `vite build` inlines into the bundle.
- **Runtime**: `.main` block → `generate-build-config.mjs` → `build-config.js` →
  main process → preload → `window.RELIANT_CONFIG`.

`a9c4e172` and `2c859d2d` are both "the baked half was wrong or absent." The
generator's header records the measurable form: a hand-written `.env` heredoc set
15 variables while the app reads 22, so v1.7.5 shipped with
`VITE_CONTROL_PLANE_API_URL` undefined — confirmed by grepping the released
`.dmg`, whose 87 renderer assets contained `api.reliantapi.com` but neither
`admin.reliantapi.com` nor `gateway.reliantapi.com`. Clicking a coupon threw
"Control plane API URL not configured."

**That grep is a proven detector.** It is the exact assertion
`deploy-reliant-web.yml` now runs on every hosted deploy, and the exact assertion
the desktop path still lacks. See §3.1.

The **order** of the resolution ladder is separately load-bearing.
`grpc-client.ts::getGRPCBaseURL` short-circuits on `isSameOriginTransport()`
*before* consulting `RELIANT_CONFIG.grpcUrl`, and that predicate is
`import.meta.env.DEV && !isPackagedRendererOrigin()`. `cli-commands.ts` documents
the mirror-image bug: it now reads only `VITE_CLI_DEFAULTS_BAKED` because
checking `import.meta.env.DEV` first was wrong (`7f403355` is the KCL half).

The general shape: **`import.meta.env.DEV` is a proxy for a question it does not
answer.** It is true in dev, false in preview/prod/packaged — but the axis that
matters is "is there a proxy in front of me," which preview, prod and packaged
all share and dev does not.

### 1.4 `NODE_ENV` leakage — a live asymmetry

`deploy-reliant-web.yml` sets `NODE_ENV=production` **scoped to the build
command**, with a comment recording both halves of the lesson: Vite honours
`NODE_ENV` even under `vite build`, so a leaked `development` folds
`import.meta.env.DEV` to **true inside a production bundle**; but setting it at
step level made npm skip devDependencies and the build died on `Cannot find
module 'vite'`. The live bundle really did ship DEV-true, and the symptom was
`/onboarding` hanging forever with every request returning 200 — because RPCs
POSTed to the Hosting origin and the SPA rewrite answered them with `index.html`.

Reliant's own `npm run build:alpha` in `pr-ci.yml` and `release.yml` sets no
`NODE_ENV` at all. It happens to be correct (Vite defaults `vite build` to
production), but nothing asserts it, and the desktop path has no equivalent of
the `?window.location.origin:` grep that catches it on the web path.

### 1.5 Fail-loud vs white-screen

`3d79e158`: `supabase-js` threw at module scope on a missing key, React never
mounted, `#root` stayed empty, and the only evidence was a console error nobody
can see in a packaged app.

Current mechanisms are genuinely good and should not be re-litigated:
- `supabase.ts` is lazy behind a Proxy — import always succeeds; failure lands in
  the error boundary naming the variable.
- `generate-build-config.mjs` hard-fails on empty `RELIANT_SERVER_URL` /
  `RELIANT_GATEWAY_URL` / `RELIANT_CONTROL_PLANE_URL`, and on the server URL
  matching `localhost|127.0.0.1`.
- `validate-endpoint.mjs` does DNS + reachability on four endpoints in
  `release.yml`'s `prepare`, before any platform build spends minutes.
- `electron/src/renderer-health.js` is a real boot canary: probes
  `#root.childElementCount` after an 8s grace period, and its `did-fail-load`
  listener names the failed subresource by URL.

**The gap is coverage, not mechanism.** Those guards protect the `.main`
(runtime) half and four hostnames. The `.vite` (baked) half has no equivalent on
the desktop path, and `renderer-health` writes a log line on a *user's* machine —
nothing in CI reads it, because nothing in CI launches the packaged app.

### 1.6 `drop_console` closes the diagnostic channel

`terserOptions.compress.drop_console: true` strips `console.log/info/debug/trace`
from every production build. The browser-log pipeline
(`vite-plugin-browser-logs.ts`, `[browser:<level>]` into
`control-plane/.forge/logs/dev/`) is therefore dev-only twice over: the plugin is
`apply: "serve"` *and* the calls are compiled out. Any detector that expects to
diagnose a prod/packaged failure by reading console output is designed against a
runtime that does not exist. Detectors must assert on DOM state, network
requests, or exit codes. It also means `renderer-health.js`'s subresource
listener is the only diagnostic that survives minification — do not weaken it.

---

## 2. What CI already builds that a test could consume

This sets the true marginal cost of everything in §3.

| Job | Produces | Config injected? | Reusable |
|---|---|---|---|
| `pr-ci` / `web` | `web/dist`, uploaded as artifact `web-dist` | **no `VITE_*`** | artifact exists, but endpoints undefined — asserting on it is meaningless |
| `pr-ci` / `e2e-frontend` | its own `web/dist`, built after `jq -er '.vite …' release.config.json >> $GITHUB_ENV` | **yes** | **the faithful prod artifact in PR CI** |
| `pr-ci` / `electron` | `electron-builder --dir` unpacked app via `npm run dist:pr` | yes, through `with-release-config.mjs` | **launchable, and currently discarded** |
| `release.yml` / `prepare` | `web/dist` with real config; uploaded as `web-dist` and consumed by all three platform matrix jobs | yes | **the release artifact — one place, before three expensive matrix jobs** |
| `deploy-reliant-web.yml` | prod `dist/` from the KCL-pinned source | yes, from the plan forge printed | already gated |

Three consequences:

1. **CI builds `web/dist` twice in PR, once uselessly.** Only `e2e-frontend`'s
   dist is a faithful artifact.
2. **`release.yml`'s `prepare` is the ideal single chokepoint** for a desktop
   bundle assertion — one job, before the macOS/Linux/Windows matrix spends its
   time. `00a8698a` unthrottled that matrix precisely to save ~7m; failing before
   it is worth more than failing inside it.
3. **The `electron` job already produces a launchable app and throws it away**,
   asserting only that electron-builder exited 0. The expensive part — install,
   build, package, on a 30-minute budget — is already paid.

---

## 3. Proposals, ranked by evidence

### P0 — Origin-parametrised unit tests for the packaged-runtime policy modules

**Evaluated concretely at the orchestrator's request. Verdict: the specific
proposal — a table-driven test over {`app://bundle`, `http://localhost:PORT`} ×
{https external, own-domain, internal, loopback} for `navigation-policy.js` —
ALREADY EXISTS and is already green. I am not going to recommend building it
twice. But the reasoning behind the request is right, and it points at the
module that genuinely lacks the treatment.**

`electron/test/navigation-policy.test.js` already runs exactly that matrix:

- `"app:// routes stay in-app"` — `app://bundle/chat/abc`, `/auth`, `/` against
  `PACKAGED = "app://bundle/"`, all `false`.
- `"outbound links still leave a packaged window"` — `https://reliantlabs.io/docs`
  and `mailto:` against the packaged origin, both `true`.
- `"same-origin route changes stay in the app"` — the dev origin
  (`http://127.0.0.1:5183/chat/abc`), covering the 401-redirect regression.
- `"a different port or host is a different origin"`, `"scheme changes are
  external"`, `"non-http schemes are handed to the OS"`, `"unparseable targets"`,
  `"an unknown current URL keeps navigation in-app"`.
- `"the scheme matches the one app-protocol actually serves"` — imports
  `APP_ORIGIN` from `app-protocol.js` and asserts against it, so the deliberate
  duplication of the scheme constant cannot drift.

`shouldOpenExternally(targetUrl, currentUrl)` is a pure function of exactly the
two inputs the proposal names, deliberately dependency-free "so it can be
unit-tested in plain node," and the test file is in `electron/package.json`'s
`test` script. **This is the pattern working as intended and it should be the
model for everything else in the Electron main process.**

Which reframes the billing finding. The Stripe hand-off going to the system
browser is **not a bug in `shouldOpenExternally`** — by its own contract an
`https://checkout.stripe.com` navigation from an `app://bundle` window *is*
external, and externalising it is correct for `https://github.com/login`. The
defect is that Stripe checkout is a **round trip**, not an outbound link: the app
needs the user to come back. So the missing piece is a return path (a deep link,
or a BrowserWindow that keeps the redirect in-app), not a different
classification. A test asserting `shouldOpenExternally` returns `false` for
Stripe would encode the wrong rule. **Flagging this because a synthesis that
files the billing bug under "navigation-policy origin divergence" will produce
the wrong fix.** The origin divergence is real and is why the packaged app
behaves differently from dev — where `http://localhost:3000` → `https://checkout.
stripe.com` is *also* cross-origin, so this likely reproduces in dev too, making
it less artifact-specific than it first appears.

**What this pattern should be extended to.** `app-protocol.js` has the same
shape and the same discipline — `resolveRequestPath` is split out with an
injectable `isFile` probe explicitly so traversal, SPA fallback and encoded paths
are testable without booting Electron, and `test/app-protocol.test.js` exists.
The genuine gap is on the **renderer** side: `web/src/lib/protocol.ts`'s
`isPackagedRendererOrigin` / `isSameOriginTransport` are the browser-side twins
of this logic, they gate the transport ladder and the OAuth redirect origin, and
they read `window.location.protocol` and `import.meta.env.DEV` from ambient
globals. In jsdom both are stubbable, so the same origin matrix — {`app:`,
`file:`, `http:`} × {DEV true, DEV false} — is a cheap vitest table. That is
where `3fcd9f79`'s "seven checks silently flipped" lived.

(a) **Caught**: for `navigation-policy` — already caught; the 401-redirect
regression and the packaged-origin half of `487fb19a` are pinned today. For the
proposed renderer-side extension: `3fcd9f79` directly, and the ordering class
that `7f403355` and the `cli-commands.ts` comment describe.

(b) **Fires**: pre-commit / PR CI unit lane. Milliseconds.

(c) **Cost**: **hours** for the renderer-side table. Nothing for
`navigation-policy` — already done.

(d) **Cannot catch**: anything about whether the module is *wired up* correctly.
`shouldOpenExternally` is fully tested and the Stripe hand-off is still wrong,
because the bug is in what `main.js` does with a correct answer. Pure-function
tests pin the policy, never the integration — which is precisely the residual
that P4 exists to cover.

---

### P0b — Chunk-load-failure detector

The onboarding agent's finding: `OnboardingPage.tsx:64` does `if
(!StepComponent) return null` before rendering the card that holds the escape
hatch, so a step component that fails to resolve yields a blank screen with no
Back, no Settings, no Sign out — `63b18468` re-entering through a different door.

**One correction to the framing, which changes the cost but not the value.**
The onboarding steps are **not lazy-loaded**. `stepConfig.ts` declares
`STEP_COMPONENTS` as an empty object populated by `registerStepComponents`,
called at module scope from `steps/index.ts`, which imports all five step
components **statically**. `React.lazy` appears only in the Settings tree
(`AISettings.tsx`, `SettingsContent.tsx`, `SettingsViewerTab.tsx`). So the
trigger is not a failed dynamic import of a step — it is the **registration
side-effect not having run**, i.e. `steps/index.ts` never being imported on some
path, or the whole entry chunk failing. Both are more likely in a packaged build
where chunk resolution differs, but the mechanism is module-init ordering, not a
lazy boundary.

That means route-interception in Playwright is a *less* direct reproduction than
it looks for this specific defect, but it is still the right detector for the
class — the Settings lazy boundaries are real, and a failed entry chunk is
exactly the `ed96ce8` / `487fb19a` symptom.

Two variants, both cheap:

1. **Playwright route interception** — `page.route('**/assets/vendor-*.js',
   r => r.abort())` on a preview build, then assert the app renders *something*
   with an escape hatch rather than an empty `#root`. Rides P2's job. Hours.
2. **A vitest render of `OnboardingPage` with `STEP_COMPONENTS` empty** — a
   direct unit assertion that the `return null` path does not eat the escape
   hatch. Minutes to write, and it pins the actual mechanism rather than a proxy
   for it. This is the better test *for this bug*; variant 1 is the better test
   for the class.

(a) **Caught**: `63b18468` (escape hatch), the present live defect, and the
white-screen family (`3d79e158`, `487fb19a`) in its "app partially booted"
form — which no other proposal covers, since P1 asserts strings and P2/P4 assert
a *successful* boot.

(b) **Fires**: PR CI unit lane (variant 2) and `e2e-frontend` (variant 1).

(c) **Cost**: variant 2 is under an hour; variant 1 is hours and adds seconds of
wall-clock. Both far cheaper than packaging.

(d) **Cannot catch**: whether real packaged chunk resolution works — it
*simulates* a failure that P1's base-path assertion is meant to prevent. Complements
P1; does not replace it.

---

### P1 — Port the existing `dist/` assertions to the DESKTOP path

**Not a new detector. An existing, proven, production-deployed detector applied
to the artifact path that lacks it.**

`deploy-reliant-web.yml` runs three gates on the hosted bundle, each with the
outage that motivated it recorded in-line:

1. *"Assert the bundle is a real SPA build"* — `index.html` exists, ≥1 hashed
   `assets/index-*.js`.
2. *"Assert the bundle is not a dev build pointed at its own origin"* —
   `grep -rlF '?window.location.origin:' dist/assets` must find nothing. In a
   correct production build that conjunct is statically false and the branch is
   dead-code-eliminated; if it survives, `NODE_ENV=development` leaked.
3. *"Assert the bundle carries this environment's real backend config"* — each of
   `api.reliantapi.com`, `admin.reliantapi.com`, `dash.reliantlabs.io` appears in
   `dist/assets`, and no `VITE_API_URL|VITE_GRPC_URL|VITE_CONTROL_PLANE_API_URL`
   is followed within 40 chars by `localhost`.

Gate 3 **is the v1.7.5 forensic grep, promoted to a gate.** It is the strongest
evidence in this document that the check works, because it is how the bug was
confirmed in the first place.

The desktop release runs none of them. Port them, with two improvements:

- **Derive the expected hostnames from `release.config.json` rather than
  hardcoding.** The web gate hardcodes three URLs and guards with an
  `if [ "$env_name" != "prod" ]` bail-out — honest, but it is a fourth copy of
  values KCL already owns. On the desktop path the same file that *supplied* the
  build env can supply the oracle: for every URL-valued key in `.vite`, assert
  its host appears in `dist/assets`. That closes the 15-vs-22 gap by
  construction, since the assertion enumerates the file rather than a list
  someone remembered to update.
- **Add the base-path assertion** (see P2): `index.html` references `/assets/…`,
  never `./assets/…`.

Then run the shared script in three places: `release.yml`'s `prepare` (before the
platform matrix), `pr-ci`'s `e2e-frontend` (right after `Build (alpha)`), and —
replacing the hardcoded copy — `deploy-reliant-web.yml`.

(a) **Historical bugs caught**: `a9c4e172`, `2c859d2d` (gate 3, directly and
provenly); `7f403355` and the `VITE_CLI_DEFAULTS_BAKED` class (a baked key
present or absent is exactly what gate 3 sees); `3d79e158` in its root form (the
key is a literal in the bundle or it is not); `ed96ce8` and the web half of
`487fb19a` via the base-path check; the DEV-true `/onboarding` hang via gate 2.
Six or seven, more than any other proposal.

(b) **Where it fires**: PR CI (pre-merge) *and* `release.yml` `prepare`
(pre-publish) *and* the hosted deploy (pre-upload). All three are before a user
sees anything — the user's stated pain is "we don't discover until after we
deploy," and this fires strictly before every deploy.

(c) **Cost**: the logic already exists and is battle-tested; this is
consolidation into one script plus wiring, not invention. Estimate **half a day**,
most of it making the hostname list derive from `release.config.json` instead of
being hardcoded. Maintenance goes *down* — the hardcoded prod URLs in
`deploy-reliant-web.yml` stop being a fourth copy. CI wall-clock: a `grep -r`
over `dist/assets`, **sub-second**, hung off builds CI already performs. Given
`459783ad`'s explicit wall-clock discipline, this is effectively free.

(d) **Cannot catch**: a value present, well-formed, and *wrong for the
environment* — a preprod endpoint in a prod build passes every check, since it is
a real hostname in a real position. That is the KCL-coupling problem and belongs
to the sibling agent. Also cannot catch runtime behaviour: a bundle can bake
every endpoint perfectly and still white-screen for a logic reason, and cannot
catch `app://`'s runtime semantics (localStorage, secure context) — only strings.

**Verdict: do this first. It is the highest-conviction item in the
investigation, and most of the work is already done in the wrong repo.**

---

### P2 — Deep-route cold loads in the existing preview-mode Playwright job

Roughly twenty lines added to a job that already exists and already runs the
right way. The infrastructure is correct: `playwright.config.ts` uses
`command: isCI ? 'npm run preview' : 'npm run dev'` on `:4173`, serving real
built output with real endpoints. Everything needed to catch `ed96ce8` was
present. See §5 for why it did not fire.

The change: a table-driven spec over the 2+-segment routes from `routes.tsx`,
each visited **cold** (`page.goto`, never client-side navigation), asserting
(i) no failed request for `/assets/*`, (ii) no `.js` request answered with
`content-type: text/html`, (iii) `#root` has children.

(a) **Caught**: `ed96ce8` directly; the web manifestation of `487fb19a`; the
white-screen shape of `3d79e158` via the `#root` probe.

(b) **Fires**: PR CI, `e2e-frontend`.

(c) **Cost**: hours. CI wall-clock: 21 routes cold, single worker — estimate
**60–90s** on a job whose realistic post-rewrite runtime the workflow comments
put at 3–4 minutes. `retries: 2` in CI triples any flake, so assertions must be
deterministic: asset-loading facts and the `#root` probe only, no real network,
no auth, no page content.

(d) **Cannot catch**: anything Electron-specific — this is `http:`, so
`isPackagedRendererOrigin()` is false and the packaged branch stays dark. Cannot
catch Firebase-rewrite-specific behaviour (`vite preview`'s fallback and
Firebase's `** → /index.html` are similar, not identical).

---

### P3 — Keep the gate trustworthy (process, not code)

**This may matter more than any detector, and it costs almost nothing.**

`pr-ci.yml`'s `E2E Frontend (Playwright)` comment block is primary evidence and
should be read in full. Its own words:

> "this job was cancelled at 15m in every one of the last 50 runs that reached
> the test step (the other 8/50 died in <3s on an unrelated `goose install`
> flake)" … "It was never a slow-but-passing job; the Playwright suite itself was
> genuinely failing" … "26 onboarding.spec.ts tests are genuinely failing against
> a refactored onboarding step model"

So the frontend e2e gate was **producing no signal at all** across ~50
consecutive runs, during the window several of these regressions shipped. A job
that is red or cancelled tells you nothing about what it is *not* testing, which
is precisely how a deep-route gap goes unnoticed (§5).

**Current state is materially better and I verified it rather than assuming**:
`1dccce7f` rewrote `onboarding.spec.ts` (748 lines) against the plan-derived step
model and the `AuthGuard` path, and `3d79e158`'s measured table shows the
progression 31 failed / 5 passed → 10 failed / 26 passed → **0 failed, 12
skipped, 36 passed**. The gate appears healthy today. That makes this the right
moment to protect it, not to fix it.

Concretely, and deliberately small:
- **`retries: 2` with a single worker is a signal-destroying combination for a
  systematically failing suite** — it triples the cost of every failure and
  converted "this suite is broken" into "this job times out," which reads as
  infrastructure noise rather than a product defect. Consider `retries: 1`, or
  keep 2 but fail the job fast once N distinct tests have failed.
- **Distinguish "timed out" from "failed."** A cancelled job is not a red job,
  and it is the state that persisted for 50 runs.
- **Alert on a job red or cancelled for N consecutive runs on `main`.** Nothing
  currently notices a permanently-dark gate.
- **12 tests are still skipped.** Confirm that is intentional; a skip is the
  other way a gate goes quietly dark.

(a) **Caught**: indirectly, most of the list — a working gate is the precondition
for every other proposal here having value. Directly, none.

(b) **Fires**: continuously, on `main`.

(c) **Cost**: a config change and an alert. Hours.

(d) **Cannot catch**: any bug at all, by itself. It is the multiplier on
everything else.

---

### P4 — Launch the packaged Electron app in CI

The `electron` job already produces an unpacked app and asserts only the exit
code. Playwright's `_electron.launch()` can start that tree under `xvfb-run` and
assert three things:

1. `page.url()` starts with `app://bundle` — the renderer loaded over the custom
   scheme, not `file://`.
2. `#root` has children — the canary `renderer-health.js` already implements, run
   somewhere a human sees it.
3. `window.RELIANT_CONFIG` carries non-empty `grpcUrl` and `controlPlaneURL` —
   the preload injection actually happened.

That triple is precisely the state `487fb19a`, `a9c4e172` and `2c859d2d` each
shipped broken.

(a) **Caught**: `487fb19a` (blank packaged window); `a9c4e172` / `2c859d2d`
(assertion 3 is the direct runtime test, where P1 is the static one); `3fcd9f79`
partially — it makes the `app:` branch *executable* in a test for the first time,
which no other proposal does; `3d79e158` in packaged form.

(b) **Fires**: PR CI, extending the existing `electron` job; worth a run in
`release.yml` before publish.

(c) **Cost**: highest here, honestly. Feasibility is good on the axes that
usually kill it: `electron-builder.pr.js` already sets `identity: null`,
`hardenedRuntime: false`, `afterSign: null`, `publish: null`, and already builds
`--dir` on Linux — no signing or notarization constraint, and headless needs only
`xvfb-run`. The real costs are (i) Playwright's Electron support is less stable
than its browser support and will flake, and (ii) the job packages an **empty**
`electron/resources/server` directory — the workflow comment states CI has never
populated it — so the app launches with no backend binary and the test can assert
only renderer-boot facts. **2–3 days** including flake shakeout; ~1–2 min on a
30-minute-budget job.

(d) **Cannot catch**: anything macOS- or Windows-specific (PR build is
Linux-only; the `file://` bug was cross-platform, but a signing defect would not
be). No flow requiring the backend. A wrong-but-present config value.

**Verdict: worth building after P1–P3.** It is the only proposal that ever
executes the `app://` branch, and the only one with a real flake budget.

---

### P5 — Boot canary: do NOT build, it exists

`renderer-health.js` is already the right design: pure, testable
`assessRendererHealth`; an injectable probe string; a `did-fail-load`-independent
`#root` check; subresource capture that names the missing asset.
`supabase.ts`'s lazy Proxy and `generate-build-config.mjs`'s hard failures cover
fail-loud on the main-process side.

There is nothing to build, only something to *observe* — the canary currently
fires into a log file on a user's machine. P4 is the proposal that reads it.
The cheapest reliable "did it boot" assertion is `#root.childElementCount > 0`,
already written; P2 and P4 should reuse the existing probe constant rather than
adding third and fourth copies.

---

## 4. Forge-side notes

Per the house rule, named rather than worked around.

- **`forge build <env> -t reliant-web` does not work and is already reported.**
  `deploy-reliant-web.yml` documents it: `-t` resolves against `forge.yaml`'s
  `frontends:`, which this project does not have — frontend topology is declared
  in KCL, and `forge generate` strips the legacy `forge.yaml` block. It fails
  with `target "reliant-web" not found in project config`. The workflow uses
  `forge env deploy --frontends-only --dry-run` as the build/deploy seam instead,
  which is a legitimate forge path, not a bypass. **The forge fix: `-t` should
  resolve frontend targets from the KCL render, which is where the declaration
  now lives.** Worth restating since any artifact-assertion work rides this seam.

- **The desktop artifact is outside forge entirely.** `desktop_release.k` is
  candid: there is no desktop workload anywhere in control-plane,
  `reliant_desktop_service` is defined at `lib/builds.k:272` and called by
  nothing, and Electron releases run from reliant's own `release.yml`.
  control-plane owns only the config. So `forge build` cannot produce the
  artifact P4 launches. **Either wire the desktop build into forge as a
  first-class target, or state deliberately that client artifacts are out of
  scope** — the current middle state (a defined-but-uncalled service plus a
  comment explaining it is uncalled) is the worst of the three. P4 as scoped
  consumes what `pr-ci.yml` already builds, so it does not bypass forge; it
  simply has no forge path to use.

- **`app://bundle` is a cross-repo string constant with no enforcement.** It
  lives in `electron/src/app-protocol.js` and in `CORS_ORIGINS` in
  `control-plane/deploy/kcl/prod/main.k`. `b1146f16` is what happens when they
  disagree. `sync-release-config.mjs` plus control-plane `ci.yml`'s drift gate
  already establish the pattern for projecting a KCL fact into reliant and
  failing on drift; the packaged origin should ride that channel rather than
  being typed twice. (Sibling agent owns the KCL-invariant analysis — flagged
  here only because the constant is on my side of the boundary.)

- **Stale comment, flagged not fixed** (product config, other agents active):
  `prod/main.k`'s `reliant-web` block asserts that "vite.config.ts reads
  `process.env.FRONTEND_PORT`" and that forge's injected `PORT` is ignored. The
  current `vite.config.ts` reads `process.env.PORT || process.env.FRONTEND_PORT`,
  PORT first, with a long comment saying the opposite order *was* the bug. The
  KCL comment describes pre-fix behaviour. Harmless today (KCL sets both) but it
  will mislead the next reader.

---

## 5. Why the existing preview-mode Playwright did not catch `ed96ce8`

**Two independent reasons, and both must be stated — either alone would have
sufficed.**

**Reason 1 — the gate was dark.** `pr-ci.yml`'s own comment: the job "was
cancelled at 15m in every one of the last 50 runs that reached the test step,"
and "was never a slow-but-passing job; the Playwright suite itself was genuinely
failing" — 26 onboarding tests against a retired step model, each failure costing
3 attempts under `retries: 2` with one worker. A job that never finishes cannot
catch anything. This is the more important reason, because it is not specific to
this bug: during that window the frontend e2e gate caught *nothing at all*.

**Reason 2 — even fully green, the suite could not have caught it, because every
test navigates to a depth-0 or depth-1 route and the bug only manifests at depth
2+.**

A relative base breaks asset resolution only when the *document's* path has a
directory component. At `/`, `./assets/index-x.js` resolves to
`/assets/index-x.js` — identical to the absolute form, so the app loads
perfectly. The failure needs a cold load at depth: at `/auth/github/callback` the
SPA fallback serves `index.html`, whose `./assets/index-x.js` resolves to
`/auth/github/assets/index-x.js`, 404s, gets `index.html` back as `text/html`,
and dies with "Expected a JavaScript module."

Grepped, every `page.goto()` in the directory: `'/'` (chat, activity-indicator,
branding, websocket, onboarding, debug-canvas), `'/onboarding'`,
`'/onboarding?plan=…'`, `'/onboarding?step=goal'`. **A query string adds no path
segment**, so `?plan=…` looks like a deep case and is not one. The one genuinely
deep navigation in the directory is `workflow-builder-validation.spec.ts`'s
hardcoded `localhost:3046`, which `testIgnore` excludes because it can never pass
against `:4173`.

Client-side navigation would not have caught it either: once the SPA has booted
at `/`, assets are loaded and `pushState` to a deep route fetches nothing. Only a
*cold* `goto` at depth reproduces — which is also why this is disproportionately
an **OAuth and billing-redirect** bug. Those are exactly the URLs a user arrives
at cold from an external redirect with no warm bundle.

The setup was right; the inputs were wrong and the job was not running. The
correction is correspondingly small: **P3 keeps the gate honest, P2 points it at
the right URLs.** No new harness — the expensive infrastructure was built and
correct.

---

## 6. Recommendation

**P1 + P2 + P3, plus P0b's one-hour unit test.** Together roughly one and a half
engineer-days, adding well under two minutes of CI wall-clock, all firing before
any deploy.

- **P1** is the strongest item and is mostly *relocation*: three proven `dist/`
  assertions exist in `deploy-reliant-web.yml` and protect the hosted SPA; the
  desktop release path — which produces the artifact users download — has none.
  Consolidate into one script, derive expected hostnames from
  `release.config.json` rather than hardcoding, add the base-path check, and run
  it in `release.yml`'s `prepare` (before the platform matrix), in
  `e2e-frontend`, and in the web deploy.
- **P2** adds cold deep-route loads to the preview job that already builds a
  faithful artifact.
- **P3** protects the gate itself — cheapest item here and plausibly the highest
  leverage, given the gate produced no signal for ~50 runs.
- **P0b variant 2** pins the escape hatch against an empty `STEP_COMPONENTS` in
  under an hour.

**P0 is already built** for `navigation-policy.js` and should be extended to
`web/src/lib/protocol.ts`'s origin predicates (hours). It is the model for
main-process testing, not a gap.

## 7. On the packaged-app gap: closing it, not accepting it

The auth agent's residual gap — "nothing proposed builds a packaged Electron app
and exercises it, which is exactly where four of the historical bugs lived" — is
mine to answer. **My conclusion is that it should be closed, via P4, but that it
is correctly the fourth thing built, not the first.**

The case for closing rather than accepting:

- The cost is far lower than the general "package an Electron app in CI" scare
  suggests, because **CI already packages one**. `pr-ci.yml`'s `electron` job
  runs `npm run dist:pr` → `electron-builder --dir` on `ubuntu-latest`, with
  `identity: null`, `hardenedRuntime: false`, `afterSign: null`, `publish: null`.
  No signing, no notarization, no code to write to *produce* the artifact — the
  30-minute-budget job builds it and then asserts only the exit code. The
  marginal ask is `xvfb-run` plus `_electron.launch()` and three assertions.
- It is the **only** proposal that ever executes the `app://` branch. P0's unit
  tests pin the policy; P1 pins strings in a bundle; P2 runs on `http:`. Every
  one of them is a proxy for a runtime nothing observes. Four historical bugs
  lived there, and the Stripe finding says a fifth does now.

The case for it being fourth:

- P1 catches strictly more historical bugs, costs half a day, and adds no
  wall-clock. P4 catches fewer, costs 2–3 days, and carries a flake budget in a
  repo that has already been burned by a flaky-then-dark e2e gate (§5, P3).
- The PR job packages an **empty** `electron/resources/server` directory — the
  workflow comment states CI has never populated it — so the launched app has no
  backend and can assert only renderer-boot facts. Real: that covers `487fb19a`,
  `a9c4e172`, `2c859d2d` and the packaged form of `3d79e158`. But it cannot
  exercise daemon spawn, OAuth round trips, or the Stripe return path, which is
  where the remaining packaged-runtime risk sits.

**So for synthesis: the gap is being closed for the boot-and-config half, and
consciously accepted for the backend-dependent half.** A CI-launched packaged app
will tell you the renderer loaded over `app://`, mounted, and has its config. It
will not tell you a purchase completes or a daemon connects. Closing *that*
requires populating `resources/server` in CI and a per-platform matrix, which I
do not think is worth it — the same flows are covered against real backends by
`control-plane/e2e/user_stories/`, and the artifact-specific risk they add over
those is the renderer-boot layer P4 already covers.
