# 05 — The onboarding state machine and its regression harness

Scope: `reliant/web/src/components/OnboardingFlow/**` plus the two components that
redirect into and out of it (`ModernApp.tsx`, `components/Mobile/MobileShell.tsx`).
Analysis only — no product code was edited. Facts marked VERIFIED in
`BRIEFING.md` are taken as given and not re-derived.

---

## The headline

`deriveStep` is not where the bugs are, and a bigger test suite around it will not
find the next one.

Every onboarding regression in the 90-day history — `458a830c`, `63b18468`,
`44a29607`, `6f78293f` — is a bug in an **effect that ran without the user doing
anything**, or in a **navigation issued from outside the router the route is
mounted under**. `deriveStep` is a pure function of three fields and it has been
correct throughout. The suite is thorough about the part that was never broken.

So the two proposals below that I actually believe in are (1) funnel every exit
through one auditable function and assert on the *reason*, and (2) an exhaustive
enumeration of the pure core — cheap, and it does find two latent defects today,
but it is the smaller of the two. Everything else I considered is ranked lower and
I say so honestly.

---

## 1. How big is the reachable state space, really?

`deriveStep` reads exactly three fields:

| Field | Domain incl. `undefined` | Size |
|---|---|---|
| `compute` | `cloud_free_trial`, `cloud_paid`, `local_daemon`, `undecided`, ⊥ | 5 |
| `modelProvider` | 6 providers + `not_configured` + ⊥ | 8 |
| `intent` | `build_app`, `existing_codebase`, ⊥ | 3 |

**120 total combinations.** `getStepsForPlan` reads two of the three (`compute`,
`intent`) — 15 combinations. `OnboardingPage`'s progress-bar index adds one boolean
(`computeAutoSkipped`), taking the render-relevant space to 240. That is small
enough to enumerate exhaustively in a `for` loop that runs in single-digit
milliseconds; property-based testing (fast-check) would be *more* machinery for
*less* certainty here, and I would not use it.

Enumerating those 240 states against four invariants is worth doing, because two
of them fail today:

**Defect A — `cloud_paid` silently takes the local branch.**
`deriveStep` line 84: `const isCloud = plan.compute === 'cloud_free_trial'`. Every
other cloud-ness test in the flow is the same literal comparison
(`ProjectChoiceStep.tsx:41`, `ProjectPickerStep.tsx:71`,
`GitHubConnectStep`'s gate). So a plan carrying `compute: "cloud_paid"` — a value
`launchPlanSchema` accepts, and which `types.ts` declares as a first-class
`ComputeChoice` — routes to `project-picker`, finalizes with `navigate: true`, and
**never renders `DaemonConnectingGate`**. That is precisely the failure `458a830c`
fixed for the free-trial path, still live on the paid path.

Today no step writes `cloud_paid`, so this is latent rather than firing. But the
plan lives in the URL and is user-editable, and "we add a paid compute tier" is a
routine future change that would arm it. The invariant to assert is *"cloud-ness is
computed in exactly one place, and `cloud_paid` and `cloud_free_trial` agree on
every step decision"* — not the current spelling of the comparison.

**Defect B — `computeAutoSkipped` outlives the field it describes.**
`BACK_CLEARS['model'] = ['compute']` clears `compute` but leaves
`computeAutoSkipped: true`. `OnboardingPage.tsx:33` then filters `compute` out of
the step list while `deriveStep` returns `'compute'`, so `steps.indexOf(actualStep)`
is `-1` and `Math.max(0, -1)` silently highlights the *wrong* step in the progress
bar. The `Math.max` is a swallowed error, not a default. In practice the Back
button is suppressed on that step so the UI cannot reach it, but the URL can, and
the auto-skip effect races to repair it. This is exactly the kind of thing
enumeration finds and hand-written tests do not.

**Invariants worth pinning (all four hold or fail mechanically over 240 states):**

1. `deriveStep(p)` is always a member of `ONBOARDING_STEPS` and
   `STEP_COMPONENTS[deriveStep(p)]` is defined.
2. `deriveStep(p) ∈ getStepsForPlan(p)` — i.e. `indexOf` never returns `-1`, so
   the `Math.max(0, …)` in `OnboardingPage.tsx:37` is provably unreachable rather
   than load-bearing. (Fails today, per Defect B.)
3. Back is a true inverse: for every state `p` and every non-first step,
   applying `BACK_CLEARS[deriveStep(p)]` yields a state whose derived step is
   *earlier in `getStepsForPlan(p)`*. This is strictly stronger than "Back does
   something" and it is what would have caught a regression like the
   suppressed-Back reasoning in `types.ts:43-53` drifting out of sync with
   `BACK_CLEARS`.
4. No absorbing non-terminal state: from every reachable state there is a
   sequence of plan updates the UI can actually issue that reaches a terminal
   step. This is the `63b18468` invariant expressed structurally.

I checked (3) by hand across all five steps and it currently holds. (1) holds for
`deriveStep` itself but see §2's `StepComponent` strand.

**Verdict on this item.** Real, cheap, finds two live defects. But note what it
*cannot* catch: neither `458a830c` nor `63b18468` nor `44a29607` involved a plan
state at all. This is hygiene, not the fix.

---

## 2. Every path that leaves onboarding without the user asking

This is the substance. Enumerated by reading each file; each entry is
file:line and the trigger.

| # | Where | Trigger | Ends onboarding? |
|---|---|---|---|
| 1 | `OnboardingRoute.tsx:86-91` | `useEffect`: `isComplete` true → `navigate({to:"/"})` | **Yes** |
| 2 | `OnboardingRoute.tsx:60-76` → `useReturningUserHeal.ts:147` | `useEffect`: a usable daemon postdating the account → `CompleteOnboarding` **write** + navigate | **Yes**, and it mutates server state |
| 3 | `useOnboardingComplete.ts:101` | `finalizeOnboardingSideEffects(..., {navigate:true})` calls the **global `router` singleton**, not the route's `useNavigate` | **Yes** |
| 4 | `ComputeStep.tsx:433-449` | `useEffect`: a daemon appears → `commitLocalAndAdvance(…, true)` writes `compute:'local_daemon'` | No, but changes the branch under the user |
| 5 | `ComputeStep.tsx:388-407` | `useEffect`: armed `pendingCloudStart` + eligibility flip → `handleCloud()` → **`CreateDaemon`**, a billable side effect | No, but provisions infrastructure with no click |
| 6 | `ModernApp.tsx:1763-1771` / `MobileShell.tsx:61-71` | `useEffect`: `!onboardingCompleted` → navigate **into** `/onboarding` | Inbound half of a potential loop |
| 7 | `OnboardingRoute.tsx:94-101` | `reset-onboarding` param → rewrites the URL, dropping the whole plan | Resets, doesn't exit |
| 8 | `ProjectPickerStep.tsx:57-63` | `useEffect`: zero projects settled → force-opens the create modal | No, but is UI the user didn't request |

**Paths 1, 2 and 3 are the ones that end the flow, and they use three different
mechanisms.** 1 and 2 go through the route's `useNavigate`; 3 goes through the
imported `router` singleton, un-awaited (`void router.navigate(...)`), from inside
an async helper that a step called. That asymmetry *is* bug `458a830c`: the fix was
to thread a `navigate: false` option down to the singleton call so the gate could
render first. The option now has to be passed correctly at **four** call sites
(`ProjectChoiceStep:80`, `ProjectPickerStep:100`, `GitHubConnectStep:253`, plus the
default), and each one re-derives `isCloud` with its own copy of the
`=== 'cloud_free_trial'` literal from Defect A. Getting one of them wrong
reproduces the original bug on one path only, which is exactly how it would ship
again.

**Path 2 deserves specific credit.** `useReturningUserHeal.ts` is the best-reasoned
file in this tree: it already knows about the mid-flow-daemon hazard, documents the
21 ms trace, and defends with a `sessionStorage` mark chosen specifically to survive
the GitHub OAuth full-page navigation (lines 38-42). I could not find a way to
defeat it through the UI. Its residual exposure is narrow and worth stating: the
mark is keyed on `userId`, so it is not written until `currentUser` resolves
(`inProgress.set` is a no-op for `undefined`), and it fails open when storage
throws — Safari private mode, some embedded webviews — falling back to
daemon-evidence alone, which is the pre-`63b18468` behaviour. In packaged Electron
the renderer is `app://bundle`, an origin whose storage partitioning has already
caused one bug in this repo (`3fcd9f79`). **A test that asserts the heal is inert
when `sessionStorage.setItem` throws is the single highest-value missing unit
test in this area**, and it is about six lines.

### Proposal R1 — one exit funnel, with a recorded reason

Replace the three exit mechanisms with a single module-level function:

```ts
// Every departure from /onboarding goes through here. No exceptions.
type ExitReason =
  | "completed_local" | "completed_cloud_gate_continue"
  | "already_complete" | "returning_user_heal" | "signed_out";
export function leaveOnboarding(reason: ExitReason, nav: NavigateFn): void
```

- (a) **Historical bugs caught, by hash.** `458a830c` — with one funnel there is no
  `navigate: true/false` flag threaded through four call sites; the cloud path
  cannot exit before the gate because *the gate's Continue is the only cloud exit
  reason that exists*. `6f78293f` — the `?tour=` handoff is set in one place
  instead of being re-derived at each exit (`useOnboardingComplete.ts:110-121`
  documents that this exact re-derivation was the fix). Partially `63b18468` —
  `signed_out` becomes an enumerated exit rather than an escape hatch racing
  `AuthGuard` (`OnboardingEscapeHatch.tsx:39-43` documents the race it works around).
- (b) **Where it fires.** Unit test: render `OnboardingRoute` with a spy funnel and
  assert the reason for each fixture — the assertion becomes *"which reason"*, not
  *"did navigate get called"*, which is the distinction the current suite lacks.
  Runtime: log the reason. Per repo memory, `console.*` in the renderer lands in
  `control-plane/.forge/logs/dev/frontend_reliant-web.log` prefixed `[browser:…]`,
  in Electron **and** a browser tab — so an e2e test can grep for
  `exit reason=returning_user_heal` and fail on an exit nobody asked for. That is
  the structural detector for the whole class: **an exit with a reason nobody
  triggered is now visible in a log line, where today it is invisible.**
- (c) **Honest cost.** Medium. Roughly 4 call sites plus `OnboardingRoute` and
  `useOnboardingComplete`; deleting the `FinalizeOptions.navigate` flag is a net
  simplification. Pre-launch, so no compatibility burden. Half a day, and it
  touches files other agents are actively editing — sequence it after their fixes
  land.
- (d) **Residual gap.** It cannot stop path 5 (`CreateDaemon` from an effect) —
  that is a side effect *inside* onboarding, not an exit. It also does not fix the
  `isCloud` literal duplication; do that separately (§1, Defect A).

---

## 3. What the existing coverage is blind to

3,485 lines across 14 files, plus a 748-line Playwright spec. It is not thin. But:

**Ten of the fourteen unit files call `vi.mock`, and what they mock is the router.**
`OnboardingRoute.returning.test.tsx:36` mocks `@tanstack/react-router` wholesale
and asserts `mockNavigate` was called. So the test proves *this component asked to
navigate*; it cannot prove *the user ended up somewhere sensible*, and it cannot
see path 3 at all, because path 3 does not use `useNavigate` — it imports the
router singleton from `@/routes`. **`458a830c`'s bug lived in the seam between two
navigation mechanisms, and the mock boundary is drawn exactly along that seam.**
That is the shared blind spot: every test is scoped to one component with its exits
stubbed, so no test observes *composition* — two effects racing, or two components
disagreeing about whether the user is done.

Bug-by-bug, what the suite would have caught:

- `458a830c` (left `/onboarding` while provisioning) — `finalizeOnboarding.cloudGate.test.ts`
  now covers it, **written after the fact**, and it asserts on the flag rather than
  on the outcome. A fifth call site added tomorrow with `navigate: true` passes it.
- `63b18468` (stranded, no way to settings) — `OnboardingEscapeHatch.test.tsx` (62
  lines) checks the buttons render. It cannot check "the escape hatch is reachable
  from every state", and in fact **it is not**: `OnboardingPage.tsx:64` does
  `if (!StepComponent) return null` *before* rendering the card that contains the
  hatch. `STEP_COMPONENTS` is populated by an import side effect
  (`stepConfig.ts:15-20`, `import './steps'`), so a failed lazy chunk — the packaged
  Electron / prod-build shape CI never exercises, per the briefing — yields a blank
  screen with no Back, no Settings, and no Sign out. That is `63b18468`'s bug
  reintroduced by a different door, live today.
- `44a29607` (cloud offer not gated on eligibility) — not in this tree at all; it
  was in `ProjectPicker`. Nothing here would have caught it, correctly.
- `6f78293f` (tour handoff) — no unit test; the `?tour=` reasoning survives only as
  comments in `useOnboardingComplete.ts` and `OnboardingRoute.tsx:32-43`.

**`launchPlanSchema.drift.test.ts` is the best test in the directory and it
generalizes.** Its shape is: two declarations that must agree (the `LaunchPlan`
interface and the Zod schema), a fixture enumerating one side, a loop asserting the
other accepts each key, and — crucially — a fourth test that *proves the guard can
fail* (`still rejects a key that belongs to neither side`). That guard-the-guard
test is the part most people omit and the reason this one is trustworthy.

The same shape applies directly to two more pairs that must agree and currently
don't have it:

- `ONBOARDING_STEPS` ↔ `STEP_COMPONENTS` ↔ `STEP_LABELS` ↔ `BACK_CLEARS`. Four
  parallel records keyed by `OnboardingStepId`. TypeScript catches a missing key in
  the last two (`Record<OnboardingStepId, …>`) but **not** in `STEP_COMPONENTS`,
  which is `{} as any` and filled at runtime. A registration test asserting all four
  key sets are equal is ten lines and closes the blank-screen strand above.
- `ComputeChoice` ↔ the cloud-ness predicate (Defect A).

---

## 4. The async dimension: making "daemon never becomes ready" deterministic

There is **no MSW in this repo** — not in `package.json`, not in
`src/test/setup.ts`. The existing pattern is `vi.mock` at the *hook* boundary
(`vi.mock("@/hooks/useOnboardingQueries")`), and Playwright's `page.route` at the
*wire* boundary for e2e (`e2e/onboarding.spec.ts:33-60`, intercepting
`**/controlplane.v1.**` with canned JSON).

`DaemonConnectingGate.test.tsx` (405 lines) already drives phases by controlling
what the mocked query returns, and the gate's own design cooperates: `derivePhase`
is a pure function of `(daemon, elapsedMs)` and `attemptStartedAt` is component
state, so `vi.useFakeTimers()` plus a scripted sequence of query results covers
connected / failed / timed-out with zero wall-clock waiting and zero flake.

So the timing harness largely **already exists**, and my honest read is that adding
MSW is not worth it for this area. MSW would buy realism at the Connect-RPC wire
level, but the bugs were never in serialization — they were in effect ordering
above the transport. Introducing a second mocking idiom next to `vi.mock` would
make the suite less coherent, not more.

What *is* missing is the composed version: a test that mounts `OnboardingRoute`
(not a step) with a scripted daemon timeline and asserts *where the user is* at
each tick — specifically that a daemon arriving at t+2s does not trigger path 2
while the gate is showing. That test needs the real router, which is exactly what
today's mocks remove.

### Proposal R2 — one composed route-level test with a scripted daemon timeline

- (a) **Bugs caught.** `458a830c` directly (mount, drive to the cloud terminal
  step, assert the gate is on screen and the location is still `/onboarding`).
  The heal-vs-own-daemon race that `useReturningUserHeal` was written for, as a
  live composition rather than a unit fixture. The Defect-B progress-bar
  mismatch, incidentally.
- (b) **Where it fires.** `vitest`, `src/components/OnboardingFlow/__tests__/`, with
  a real `createMemoryHistory` router and `vi.mock` pushed down to the *query* layer
  only. Fake timers for the daemon clock.
- (c) **Honest cost.** Medium-high, and I want to be straight about why: mounting the
  real router pulls in `routes.tsx`, which pulls in the app shell. Expect a day of
  fighting imports, and the result will be the slowest test in the directory. It is
  worth it once, for one test; it is not worth it as a pattern for twenty.
- (d) **Residual gap.** jsdom is not the packaged Electron renderer. Per the
  briefing this is where the config-shaped bugs live, and no vitest test reaches
  them.

---

## 5. Would a state-machine-first refactor make this impossible?

I considered lifting the whole flow into an explicit machine (XState or a
hand-rolled reducer) with declared states, guards and effects, and I do **not**
recommend it.

The argument for: effects become declared transitions, so "what can exit this
state" is enumerable by construction, and every bug in §2 becomes a
statically-checkable question.

The argument against, which I find stronger:

- **The current design's core is already correct.** `deriveStep` as a pure function
  of URL state is genuinely good — it survives the OAuth full-page navigation for
  free, which a machine held in memory would not. `useOnboardingPlan.ts:22-48`
  re-reads the URL inside the updater specifically so concurrent updates compose.
  A refactor would have to reproduce all of that.
- **The bugs are not in the machine, they are at its boundary** — a
  `sessionStorage` write, a router singleton, a query settling. XState would model
  those as invoked actors, which is a new place to get the same ordering wrong,
  not a proof that you cannot.
- **The tree is under active repair by other agents right now.** A structural
  rewrite of five step components and three hooks would collide with in-flight
  fixes, which is the worst possible time.

R1 is the 20% of that refactor that captures most of the value: it makes *exits* —
the thing that actually broke, four times — go through one function, without
touching the derivation that never broke. Pre-launch freedom to delete
(`FinalizeOptions.navigate`, the four duplicated `isCloud` literals) makes R1
cheaper than it would be post-launch, and that is the freedom worth spending here.

---

## Ranked recommendations

**R1 — Funnel every exit through `leaveOnboarding(reason)` and log the reason.**
Highest conviction. Directly addresses the mechanism behind `458a830c` and
`6f78293f`, converts the invisible class ("something ended onboarding") into a
grep-able log line in `frontend_reliant-web.log`, and deletes the
`navigate: true/false` flag rather than adding machinery. Medium cost. Sequence
after the in-flight fixes land.

**R2 — Exhaustive enumeration of the pure core (240 states, 4 invariants), in the
`launchPlanSchema.drift.test.ts` shape, including a guard-the-guard case.**
Cheap — under an hour — and it fails today on Defect A (`cloud_paid` skips the
daemon gate) and Defect B (progress-bar index `-1` swallowed by `Math.max`). Low
conviction that it prevents the *next* regression; high conviction that it is worth
the hour anyway. Bundle the `ONBOARDING_STEPS` ↔ `STEP_COMPONENTS` registration
check with it, which closes the `if (!StepComponent) return null` blank-screen
strand — a live reintroduction of `63b18468` through a different door.

**R3 — Two small targeted tests, both under twenty lines.** First: the heal is inert
when `sessionStorage.setItem` throws (private mode, `app://bundle` partitioning —
the failure mode `3fcd9f79` proves this repo actually hits). Second: the escape
hatch renders on every derived step, including the unregistered-component case.
Lowest cost of anything here, and each closes a specific documented hazard.

**Not recommended:** MSW (duplicates working `vi.mock` + `page.route` infrastructure
at a boundary where the bugs are not); a full XState refactor (rewrites the correct
part, collides with in-flight work); more per-component unit tests in the existing
mocked-router style (that style is the blind spot, so more of it widens nothing).
