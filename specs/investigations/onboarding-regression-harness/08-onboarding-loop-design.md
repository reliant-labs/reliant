# 08 — Onboarding loop: a design proposal

**Status:** design only. No product code was written for this document.
**Scope:** what onboarding should *be*, not how to test it. The regression-harness
question is answered in `00-SYNTHESIS.md` and `05-onboarding-state-machine.md`;
this file assumes those findings and does not re-derive them.

**Prior art inherited as given:** `00-SYNTHESIS.md`, `05-onboarding-state-machine.md`
(the state machine, its three exits, the 240-state space), `06-billing-ux.md`
(the 5-navigation billing detour and its recommended redesign), `03-auth-oauth.md`
(the four distinct OAuth flows), `ONBOARDING_FINDINGS.md` (the ejection bug and
the production-shaped funnel data).

---

## 0. The number this document is written around

From `ONBOARDING_FINDINGS.md`, read out of the control-plane dev database:

> **29 of 43 users** have `onboarding_completed_at IS NULL`.
> **8 distinct users** have `onboarding_completed_at IS NULL` **and** at least one
> daemon — they paid the provisioning cost and never reached the step that
> records completion. All 8 daemon rows are named `onboarding-daemon` at
> `status = 3` (SUSPENDED).

Two thirds of users do not finish. Eight of them **provisioned real infrastructure
and then fell out anyway** — we spent money on their behalf and they got nothing,
and the machine is now suspended.

It is tempting to read that as "we had bugs, we fixed them, the number will
recover." I do not believe that, and the whole argument below rests on why:
`useReturningUserHeal` exists *specifically* because this population exists. The
codebase's response to a two-thirds drop-off has been to build a repair mechanism
for the flag, not to ask why the flow is losing people. The repair is good work —
it is the best-reasoned file in the tree — but repairing the record of a journey
nobody completed is treating the symptom.

The flow is losing people because **it asks four configuration questions before it
has earned the right to ask any of them.** That is the thesis. Everything else
here follows from it.

---

## 1. What onboarding is actually for

There are three candidate answers, and picking the wrong one produces exactly the
flow we have.

**Answer A — "collect the configuration the product needs to run."**
This is what the current flow implements. It is not wrong on its own terms: a
Reliant agent genuinely cannot execute a tool call without compute, cannot call an
LLM without a model provider, and cannot edit code without a project. Each of the
four questions is *real*. The wizard is a competent solution to the problem "we
need four facts."

**Answer B — "teach the user what the product is."**
The tour (`components/Onboarding/`, spotlights and a checklist) implements this
separately, after setup. Worth noting that the split already exists in the
codebase and is sound — do not merge them.

**Answer C — "get the user to the first moment where the agent does something
useful for them, and collect configuration as a side effect of that."**

**I recommend C, and I want to argue it rather than assert it**, because A has a
real case and the failure mode of C is genuine.

The case for A: configuration questions asked later are *interruptions*. An
interruption during work is more expensive per-unit than the same question asked
during a phase explicitly framed as setup, because it breaks a task the user cares
about. A wizard front-loads the cost into a moment where the user has already
consented to being asked things. This is why wizards exist and why the pattern
persists. If a user is going to answer all four questions anyway, asking them
consecutively is cheaper than asking them scattered across the first hour.

The case for C, which I find decisive:

1. **The premise of A is that the user will answer all four questions anyway.**
   Our data says two thirds of them do not — they answer *some* and leave. A
   wizard's efficiency argument only holds conditional on completion, and
   completion is precisely what we do not have. At a 33% completion rate, a wizard
   is not a cheap way to collect four facts; it is an expensive way to collect
   zero facts from most people.

2. **The questions are not answerable by a new user.** "Cloud or local daemon?" is
   a question about an architecture the user has not been told about. "Which model
   provider?" presumes they know what they will use Reliant for and what the
   trade-offs are. The compute step's own history is evidence: the step originally
   led with a topology diagram, and the comment in `stepConfig.ts` records that it
   was removed because "being the largest thing on screen it read as the interface
   rather than as an illustration." The step needed a diagram because **the
   question requires explaining the system to ask it**. A question that needs a
   diagram is a question asked too early.

3. **Three of the four questions have a defensible default; only one does not.**
   Worked through below in §2. Once you notice that, the wizard is mostly asking
   the user to confirm defaults they cannot evaluate, which is the worst kind of
   question — it carries the full cognitive cost of a decision and produces no
   information.

4. **The failure modes cluster in setup because setup is where we put everything
   that can fail.** `00-SYNTHESIS.md` makes this point about cold deep-link entry
   for OAuth/billing URLs. It generalizes: onboarding is the least forgiving moment
   in the product — no investment, no context, no mental model to diagnose with —
   and we have chosen to run daemon provisioning, billing, OAuth, and GitHub
   connection there, all at once, all for the first time. Moving work out of that
   window is worth real money independent of any UX argument.

**So: onboarding optimizes for time-to-first-agent-turn, and every configuration
question must justify its position ahead of that moment.** The success metric is
not `onboarding_completed_at IS NOT NULL`; it is **the user saw the agent do
something and came back the next day**. I would go further and say the
`onboarding_completed` flag should stop being a gate at all — see §7.

### The devil's advocate against my own recommendation, stated up front

**The strongest objection is that a shorter flow relocates the configuration
burden to a worse moment, not a better one.** Concretely: a user who defaults into
cloud compute, does two useful things, then hits "your trial machine needs a
payment method" mid-task is *angrier* than a user who was told about billing at
step 1. They have now invested effort and the wall arrives after the investment.
That is a real and well-documented pattern (the "bait and switch" reading of
freemium onboarding), and I do not think it can be waved away.

My answer is not that the objection is wrong; it is that it applies to **exactly
one** of the four questions — the money one — and the design below treats that
question differently from the other three for precisely this reason (§2, §3). The
mitigation is: never let a deferred decision arrive as a *wall*. It must arrive as
a *ceiling the user was told about* — stated once, cheaply, at a moment when they
have no reason to disbelieve it, and then enforced without surprise. If we cannot
do that, A beats C on the billing question and the flow should keep an up-front
money step. §2 commits to a specific answer.

A second objection, weaker but real: **defaults hide the product's shape.** A user
who is never asked "cloud or local" may not learn that local compute exists, and
local is the option that makes Reliant appealing to the privacy-sensitive user
running against a proprietary codebase. Defaulting silently could cost us that
user entirely. The mitigation is that the default must be *visible and reversible
in one click from the surface where it matters*, not merely reversible in settings
three levels deep. Named as an explicit requirement in §3.

---

## 2. The four questions, interrogated one at a time

For each: is it needed before the first agent turn, does it have a defensible
default, and what does deferring it cost?

### Compute (`cloud_free_trial` / `cloud_paid` / `local_daemon` / `undecided`)

**Needed before the first turn: yes, genuinely.** A tool call needs a daemon.
This is the one hard blocker.

**Defensible default: yes, and it is runtime-dependent.**

- In **Electron**, a local daemon is bundled, auto-starts, and registers within
  ~1.2s of sign-in (`ComputeStep.tsx:106-108`). The step already auto-skips when
  it finds one (`ComputeStep.tsx:433-449`, `computeAutoSkipped`). **So for desktop
  users this question is already, in the common case, not asked.** The flow
  contains the evidence for its own removal.
- In **web**, there is no local daemon and no way to get one without installing
  the desktop app. Cloud is the only option that exists. The question
  "cloud or local?" in a browser is a question with one answer.

So in *both* runtimes the compute question has a forced or overwhelmingly likely
answer, and the step exists to handle the cases where it does not. **Recommendation:
delete compute as a step. Resolve it as a rule, and surface the resolution as a
reversible statement rather than a question.**

**The catch, and it is the whole billing problem:** cloud compute costs money.
`ComputeStep.tsx:388-407` already fires a **billable `CreateDaemon` from a
`useEffect`** on an eligibility flip. So we are *already* provisioning paid
infrastructure without a click — we just do it behind a step that looks like it got
consent. Defaulting web users into cloud does not introduce a new class of risk;
it makes an existing one legible. That is an argument for the change, but only if
we handle the money question honestly (below).

### Model provider

**Needed before the first turn: yes** — an agent turn is an LLM call.

**Defensible default: yes.** `reliant_credits` is a first-class `ModelProvider`
and is the option that requires the user to have nothing. Every other value
(`openai`, `anthropic`, `openrouter`, `copilot`) requires the user to go find an
API key, which is a task with an unbounded tail — it can involve creating an
account with a third party and entering a credit card, *inside our onboarding*.

**Recommendation: default to `reliant_credits` with a starter grant, and never ask
during onboarding.** Move "bring your own key" to settings and to a contextual
prompt when the grant runs low. A user who wants to use their own Anthropic key
is, by construction, a user sophisticated enough to find it in settings; a user who
does not have a key is *blocked* by being asked.

This is the highest-confidence deletion in the document. The model step currently
contains a coupon form and a "Set up billing" link (`ModelStep.tsx:422`), i.e. it
is a second door into the billing detour. Deleting the step deletes a door.

### Project source (`build_app` / `existing_codebase`) and GitHub connection

**Needed before the first turn: this is the one that is genuinely required, and
it is also the one the current flow treats most casually.**

The agent cannot do anything useful without something to work on. But note the
asymmetry the current flow creates: `build_app` terminates at `project-choice`
with an `ensureProject` call, while `existing_codebase` routes through
`github-connect` — a full OAuth round trip with a different provider, a different
redirect registry (Flow B in `03-auth-oauth.md`), and its own failure surface —
*before the user has seen the product do anything*.

**Defensible default: no, and this is the question that must stay.** "What should
I work on" is not defaultable; it is the entire content of the user's intent.

**But GitHub connection is separable from it.** Connecting GitHub is not required
to work on an existing codebase in Electron (a local folder works), and it is not
required to try the product at all. It is required to work on a *remote* repo from
*cloud* compute. That is a specific, later, explainable moment.

**Recommendation: keep one project question, defer the GitHub OAuth until the user
picks a path that needs it, and add a fourth option the flow already has a type
for and does not use.** `CodeSource` declares `"sample_project"` and **nothing in
the codebase references it** (verified: one hit, the type declaration itself). The
type system is holding a slot for the demo path that was designed and never built.

### Summary

| Question | Keep up front? | Default | Where it moves |
|---|---|---|---|
| Compute | **No** | Electron → local; web → cloud trial | Resolved by rule; reversible from a persistent affordance |
| Model provider | **No** | `reliant_credits` + starter grant | Settings; contextual prompt at low balance |
| Project source | **Yes — the only one** | none (this *is* the intent) | stays, becomes step 1 of 1 |
| GitHub connect | **No** | not connected | Deferred to the moment a remote repo is chosen |

One question survives. That is the flow.

---

## 3. The recommended flow

### 3.1 Shape

```
sign-in
   ↓
[ONE SCREEN]  "What do you want to work on?"
   ├── Open a folder            (Electron only — native picker)
   ├── Import from GitHub       (triggers GitHub OAuth *here*, in context)
   ├── Start something new      (name it, or don't)
   └── Try a sample project     ← the default-highlighted option
   ↓
workspace, with the agent's first turn already in flight
```

Everything else — compute, model, billing, GitHub for users who did not pick
GitHub — is resolved by rule, surfaced as reversible state, or deferred to the
moment it is needed.

### 3.2 What each option does

**"Try a sample project"** is the load-bearing addition and the answer to
time-to-first-value. It is a small, real, self-contained repository we ship (a
todo API, a CLI, something with a genuine bug in it), cloned into a fresh
workspace, with **the first agent turn pre-seeded** — the agent starts working the
moment the workspace opens, without the user typing anything.

The user's first experience of Reliant is watching it read the codebase, form a
plan, and edit a file. That is the product. Nothing in the current flow shows it.

This also solves a problem the current flow has no answer for: **a brand-new user
has no idea what to type into a chat box.** We currently deliver them to an empty
workspace and hope. `ProjectPickerStep.tsx:57-63` force-opens a create modal when
zero projects settle, which is the flow admitting the user has nothing to do and
guessing on their behalf.

**"Import from GitHub"** runs the GitHub OAuth (Flow B) *at this point*, where the
user has just asked for exactly the thing OAuth provides. The redirect is
explained by the button they pressed one second ago. Contrast with today, where
`ProjectChoiceStep.handleConnectExisting` fires a full-page navigation to the
control plane as a side effect of choosing an *intent*.

**"Open a folder"** is Electron-only and should be visually first there, because it
is the fastest path to real value for the user who came to work on their own code.

**"Start something new"** creates an empty project. It should not require a name —
default one, let it be renamed later. Every required field here is a chance to
bounce.

### 3.3 What happens to compute

Resolved by rule at workspace-open, not asked:

- **Electron:** use the bundled local daemon. It is already running. If it is not
  ready yet, the workspace opens anyway and the first turn queues behind it (§4).
- **Web:** start a cloud trial daemon.
- **Either, if the user already has a usable daemon:** use it. This is the existing
  `hasUsableDaemonForOnboarding` logic, unchanged and now doing its job as the
  primary path rather than as an auto-skip inside a step.

The resolution is displayed as a **persistent, always-visible compute chip in the
workspace chrome** — "Local machine" / "Cloud trial · 4 days left" — that is
clickable and opens the compute settings. This is the mitigation for the
"defaults hide the product's shape" objection in §1: the choice is not hidden, it
is *made and shown*, and changing it is one click from the surface where it
matters.

### 3.4 What happens to money — the part I am least comfortable with

This is where my own recommendation is weakest and I want to be direct about it.

Deferring billing entirely is the version of this design that produces the angry
mid-task wall from §1. So it must not be deferred *silently*. The commitment:

1. **The trial ceiling is stated once, in the sample-project workspace, in the
   compute chip, from the first second.** "Cloud trial · 4 days left" is present
   before the user has done anything. It is not a modal, it is not an
   interruption, and it is not hidden in settings. A user who reads nothing still
   sees it in their peripheral vision every time they look at the workspace.
2. **The ceiling is enforced with warning, never as a surprise.** At the point the
   trial is genuinely about to end, we say so with enough runway to act — and
   critically, **the user's work is not lost or locked**. Their project, chats,
   and history remain readable. What stops is new agent turns on cloud compute,
   and the offered remedies are "add a payment method" *and* "switch to a local
   machine" (which is free, and on desktop is one click).
3. **Billing, when it happens, is the redesigned flow from `06-billing-ux.md`** —
   deep-linked to Plans with intent preserved, identity link as an in-place modal
   rather than a screen that looks like a sign-in wall, `?checkout=success|cancelled`
   so the return is honest, and Electron opening Stripe in a controlled
   `BrowserWindow`. That work is a prerequisite, not an optional companion. **If
   the billing redesign does not land, do not ship the deferral** — deferring a
   payment ask into a flow that costs 5 navigations and looks like a re-auth demand
   is strictly worse than asking up front, because the user hits it with more
   invested.

That last sentence is the honest sequencing constraint, and it mirrors the one
`06-billing-ux.md` already flagged about `useGoToBilling`.

**One thing I would additionally change and cannot decide alone:** the sample
project should ideally run on compute that costs us nothing per user, so that
"try Reliant" is not gated on provisioning a paid machine. Whether that is
technically available is a question for the owner (§8, Q2).

---

## 4. Async provisioning

`05-onboarding-state-machine.md` calls this the crux and I agree. Three models:

**(a) Block with a good waiting experience.** Today's `DaemonConnectingGate`.
Honest, but it puts a spinner between the user and the product at the exact moment
they are deciding whether to care. It also creates the surface where most of the
historical bugs live — every "advanced too early" and "stranded" bug is a bug in
the code that manages this wait.

**(b) Proceed and reconcile.** The user enters the workspace immediately; the
daemon arrives when it arrives.

**(c) Provision speculatively before the user commits.** Fastest, and we already do
a version of it (`ComputeStep.tsx:388-407`). It spends money on users who may
bounce — and the 8 suspended `onboarding-daemon` rows are the receipt for exactly
that.

**Recommendation: (b), with a bounded speculative element that costs nothing.**

The design principle: **the workspace is not gated on the daemon; the first agent
turn is queued behind it.** The user lands in a real workspace with real files
visible (the sample repo is cloned client-side / server-side independent of the
daemon), types or watches the pre-seeded turn, and the turn shows an honest
"starting your machine — about 30 seconds" state *in the conversation*, where it is
context rather than a wall.

Why this is better than (a) and not merely a repackaging of it:

- The waiting state is **inside a surface the user will use again**, so the time is
  spent learning the interface rather than staring at a gate they will never see
  twice.
- There is no "advance past the gate" decision to get wrong — **the entire class of
  bugs from `458a830c` disappears, because there is no gate to leave early.** The
  reason those bugs recur is that the code has to decide *when the user may
  proceed*; remove that decision and the bug has nowhere to live.
- Failure is recoverable in place. If provisioning fails, the conversation shows
  the failure and the remedies. Compare today, where failure during the gate
  strands the user on a screen whose only escape is the escape hatch.

The bounded speculative element: **on Electron, start the local daemon at
sign-in** (already happens) and, on web, **begin cloud provisioning the moment the
user picks a project option** rather than after the workspace mounts. That buys a
few seconds and commits money only after a real user gesture — not on an
eligibility flip from a background effect, which is what `ComputeStep.tsx:388-407`
does today and which I would remove.

**Devil's advocate:** (b) makes the workspace lie a little — the user sees a file
tree and a chat box that cannot yet act. If the daemon takes 90 seconds rather than
30, "queued behind provisioning" becomes a worse experience than an honest gate,
because the user has been invited to act and then made to wait. The mitigation is
that the queued state must be *unmistakable* (the composer states it, the turn
states it, the compute chip states it) and must degrade into a real error with real
options at a threshold. If measured p95 provisioning is worse than ~45s, revisit —
below that, (b) wins; above it, (a) may be more honest. **This needs a measurement
we do not currently have (§8, Q3).**

---

## 5. Interruption and resumption

**Keep the URL-derived plan. It is the best thing in the current design and the
proposal preserves it wholesale.** `deriveStep` is a pure function of state held in
the URL; that is why it survives the OAuth full-page navigation *for free*, and it
is why `05-onboarding-state-machine.md` and two other agents recommended against a
state-machine library. Nothing here changes that.

What the shorter flow changes is how much there is to resume. With one screen and
one field, the resumable state is `?source=github` plus whatever the OAuth
round-trip carries. The 120-combination space collapses to a handful, and Defects
A and B from `05` (the `cloud_paid` branch and the orphaned `computeAutoSkipped`)
**cease to exist because the fields they describe are gone.**

Three interruption cases:

**GitHub OAuth mid-flow.** The only remaining redirect in onboarding. It returns to
the same one screen with `source=github` set and the credential landed;
`OnboardingRoute`'s existing `github_connected` handling already does the toast,
the cache invalidation, and the param strip. Unchanged and correct.

**Closing the laptop mid-flow.** With one screen there is no meaningful mid-flow
state to lose. The user returns to the same question. Contrast today, where a user
three steps in returns to a URL-encoded plan that may or may not match server state
— and where `useReturningUserHeal` exists to repair the mismatch.

**Switching web → desktop.** This is the case the current design handles worst and
the proposal handles by accident. Today a user who onboards on web (cloud daemon,
`onboarding_completed = true`) and then installs the desktop app arrives with a
bundled local daemon *and* a cloud daemon, and the compute decision they made no
longer describes their situation — with no prompt to revisit it. Under the
proposal, compute is not a stored decision but a resolution over available
machines, re-evaluated at workspace-open and displayed in the chip. Installing the
desktop app makes a local machine available, the chip shows it, and switching is
one click. **The state that could go stale has been deleted rather than
synchronized.**

---

## 6. Web vs Electron

They must differ, and the discipline is about *where*.

**The principle: the runtimes differ in the set of options available and in nothing
else.** Same screen, same copy, same code path — a different set of buttons,
because a different set of things is possible.

| | Electron | Web |
|---|---|---|
| "Open a folder" | present, listed first | **absent** — no filesystem access |
| Default compute | bundled local daemon | cloud trial |
| GitHub OAuth | loopback bridge (Flow A shape) / Flow B for connect | hosted redirect |
| Sample project | local clone, local daemon | server-side clone, cloud daemon |
| Stripe (when reached) | controlled `BrowserWindow` | same-tab redirect |

Where differing has *caused* bugs historically, the pattern is consistent: the
difference was expressed as a **runtime sniff deep inside shared logic**, rather
than as a **different value supplied at the edge**. `getAppURL()` in
`03-auth-oauth.md` is the good version — one function owns the runtime question,
everything downstream consumes a value. `ComputeStep`'s tangle of `daemonLoading` /
`hasUsableDaemon` / `pendingCloudStart` / `startingCloud` refs is the bad version:
four interacting guards whose comments are almost entirely about which runtime
produces which race.

**Concretely: one `resolveRuntimeCapabilities()` at the edge**, returning
`{ canOpenLocalFolder, defaultCompute, oauthMode }`, consumed as data. The single
onboarding screen renders the options it is given. No step component asks "am I in
Electron."

The one place they should *not* differ, and do today: **the escape hatch and the
failure surface** (§7). Every failure mode in packaged Electron is harder to
diagnose than in web (no devtools by default for a user, `app://bundle` storage
partitioning, no URL bar), so if anything Electron needs the *stronger* recovery
affordance. Today it gets the same one, and `OnboardingPage.tsx:64`'s
`if (!StepComponent) return null` removes it in exactly the runtime where it is
least recoverable.

---

## 7. The failure experience

Most historical bugs surfaced during onboarding. That is not because onboarding is
unusually fragile; it is because it is where we do everything for the first time,
for a user with nothing invested.

**The design principle: onboarding must never render a state from which the only
recovery is knowing something. Every failure resolves to a screen with (a) a plain
statement of what did not work, (b) at least one action that makes progress
anyway, and (c) a way out of the flow entirely — always, unconditionally, in every
render path.**

The third one is where we currently fail structurally.
`OnboardingPage.tsx:64` returns `null` before rendering the card that contains
`OnboardingEscapeHatch` — so the one state where a user most needs the exit (the
step component failed to register / the chunk failed to load) is the one state
where it is absent. That is bug `63b18468` reintroduced through a different door,
and it is live.

**Structural fix: the escape hatch is rendered by the route, not the page, and it
is outside every conditional return.** It should be impossible to render
`/onboarding` — in any state, including an exception — without it. An error
boundary around the step content that renders the failure and *keeps the chrome*
is the right shape. The hatch's presence should not be a fact any component can
decide.

Per-failure:

| Failure | What the user gets |
|---|---|
| Daemon won't provision | In-conversation, not a gate: what failed, retry, and **switch to a local machine** (Electron) or "we'll email you when it's ready" (web). Never a dead spinner. |
| Trial exhausted / coupon fails | Work stays readable. New turns stop. Two offers: add payment, or switch to local. Never a modal over a blank app. |
| GitHub unreachable | The other three project options remain on screen; GitHub import is the one that failed, not the flow. |
| White screen / chunk failure | Error boundary renders the failure plus the escape hatch. Reload, go to settings, sign out. |
| Anything unclassified | Same boundary, same three actions. |

Two supporting pieces, both already recommended elsewhere and both cheap:

- **`leaveOnboarding(reason)`** — the single exit funnel from
  `05-onboarding-state-machine.md` R1. Under this design it becomes *nearly
  trivial*, because there are far fewer exits to funnel: one screen, one
  completion, one escape hatch. It remains valuable for the reason `05` gives —
  an exit nobody asked for becomes a greppable log line in
  `frontend_reliant-web.log` rather than an invisible event.
- **Retire the `onboarding_completed` gate as a *redirect* condition.** This is
  the deeper fix and it deserves stating plainly. Today `ModernApp.tsx:1763-1771`
  bounces any user with `!onboardingCompleted` *into* `/onboarding`, which is what
  makes a stuck user **stuck forever** rather than merely inconvenienced —
  `useReturningUserHeal`'s doc comment says exactly this. With one question and a
  sample-project default, the flag can become **advisory**: if a user has no
  project, show the project question *in the workspace*; do not exile them to a
  separate route they cannot escape. **This deletes the entire returning-user-heal
  problem class**, because there is no longer a flag whose falseness imprisons
  anyone. It is the single most valuable structural change in this document after
  the flow reduction itself.

---

## 8. Re-onboarding and the second project

Onboarding is treated as once-ever. It is not. Users add projects, connect GitHub
later, change compute, and hit trial limits.

**Recommendation: there is no "re-onboarding," because after this redesign the
first-run flow is not a special flow.** "What do you want to work on" is the same
question as "add a project," asked at a moment when the answer is empty. It should
be the **same component**, rendered in a modal for an existing user and full-screen
for a first-run user, and it should write through the same code path.

This is a real simplification, not a framing trick. Today `ProjectPicker.tsx` is a
*second* daemon-connect surface, and `ONBOARDING_FINDINGS.md` records that the
user's reported "duplicate screens" were the picker, seen after being ejected from
onboarding. Two surfaces answering the same question is how that confusion is
possible. One surface makes it impossible.

Likewise: "connect GitHub" is a capability the user acquires when they need it, in
any of the places they need it — not a step. "Change compute" is the chip.
"Upgrade" is billing. None of these is onboarding; they are the product having
settings.

**What legitimately remains first-run-only:** the tour
(`components/Onboarding/`, already separate, already suppresses itself on the
onboarding route) and the sample project's pre-seeded turn. Keep both, keep them
separate.

---

## 9. Migration path

The current derivation is good and the proposal keeps it. This is a **reduction of
the plan's field set**, not a rewrite of the mechanism — which is what makes it
incrementally shippable. Each phase is independently valuable and independently
revertable.

**Phase 0 — prerequisites (not optional).**
The billing redesign from `06-billing-ux.md` steps 1–4, and the error boundary +
unconditional escape hatch from §7. **Phase 3 must not ship without the billing
work**, per §3.4.

**Phase 1 — delete the model step.** Default `modelProvider: 'reliant_credits'`.
`deriveStep`'s `if (!plan.modelProvider) return 'model'` becomes unreachable and
the step is removed. Lowest risk, highest immediate return: it removes a full step
*and* one of the two doors into the billing detour, and it changes no async
behavior at all. Ship this alone and measure.

**Phase 2 — add the sample project.** Implement the `sample_project` `CodeSource`
that the type system already declares. Add it as a fourth option on the project
step with the pre-seeded first turn. Purely additive — no existing path changes.
This is where time-to-first-value actually moves, and it is measurable in
isolation: does the sample cohort return on day 2 at a higher rate?

**Phase 3 — resolve compute by rule.** Replace the compute step with
`resolveRuntimeCapabilities()` + the compute chip. Delete the four interacting
guard refs in `ComputeStep`, the `computeAutoSkipped` field (Defect B evaporates),
and the `cloud_paid`/`cloud_free_trial` literal duplicated across four files
(Defect A evaporates). **This is the phase that removes the daemon gate**, so it
carries the async-provisioning change from §4 with it and is the riskiest single
step. Gate on the p95 provisioning measurement (Q3).

**Phase 4 — collapse to one screen and make the flag advisory.** Merge
`project-choice`, `github-connect` and `project-picker` into one component shared
with the existing project-add surface. Remove the `!onboardingCompleted` redirect
from `ModernApp` / `MobileShell`; retire `useReturningUserHeal` (it has nothing
left to repair). `deriveStep` survives with a much smaller domain, and the URL
plan keeps carrying the OAuth round trip.

**Phase 5 — cleanup.** Delete `DaemonConnectingGate` and the
`FinalizeOptions.navigate` flag. Rename `components/Onboarding/` → `components/Tour/`
so the two trees stop being confusable (flagged as cosmetic and the owner's call in
`ONBOARDING_FINDINGS.md`; it is cheap and it is now clearly correct).

**What is deliberately preserved throughout:** `deriveStep` as a pure function of
URL state; the plan-in-URL mechanism; the GitHub OAuth return handling in
`OnboardingRoute`; the tour handoff via `?tour=`. The good part is not touched.

---

## 10. Open questions for the owner

Each has my recommended default, so nothing blocks on an answer.

**Q1 — Is `reliant_credits` with a starter grant commercially acceptable as the
universal default?** This is the assumption Phase 1 rests on. It means every signup
gets some inference on us. *Recommended default: yes, with a small grant sized to
comfortably cover a sample-project session plus a few real turns.* If the answer is
no, Phase 1 still works but the model step becomes a "bring your own key" prompt at
first use rather than a deletion — worse, but survivable.

**Q2 — Can the sample project run on compute that costs us nothing per user?**
A shared/pooled runner, or a heavily-constrained sandbox. If yes, "try Reliant"
becomes free for us and the whole billing tension in §3.4 relaxes dramatically. If
no, the sample project provisions a real trial daemon and we accept the cost per
signup. *Recommended default: ship on real trial compute first (it is what exists),
and treat pooled sandbox compute as a fast follow if signup volume makes the cost
bite.*

**Q3 — What is p95 cloud daemon provisioning time?** §4's choice of
"proceed and queue" over "block with a gate" is conditional on this being under
roughly 45 seconds. I could not find this measured. *Recommended default: instrument
it before Phase 3, and if p95 exceeds 45s, keep a gate for the cloud path only and
revisit — do not ship the queued model on top of a two-minute wait.*

**Q4 — What is the product brief?** Per the repo's own guidance, a UI built around
the entity list rather than what the product does is placeholder, and shipping
needs a brief nobody has written. This design specifies *structure* (one question,
sample project, chip, in-conversation waiting) and deliberately does not specify
voice, visual identity, or what the sample project should be. *Recommended default:
the sample project should be a small repo with a real, findable bug, because
"watch it find and fix a bug" is the most legible demonstration of an agentic
coding tool — but this is exactly the kind of judgement that should be the owner's,
and it materially affects whether the first-run experience lands.*

**Q5 — Should trial exhaustion stop new turns, or degrade to a slower/cheaper
tier?** §3.4 assumes it stops turns while keeping work readable. A degraded tier
would be gentler but costs us money on non-converting users indefinitely.
*Recommended default: stop new cloud turns, keep everything readable, and offer
local compute as the free escape — on desktop that is a genuinely good outcome, and
on web it is an honest reason to install the desktop app.*

---

## 11. What I would do first, if only one thing ships

**Phase 1 (delete the model step) and the unconditional escape hatch from §7.**

Together they are perhaps a day of work. The first removes a step and a billing
door with no async risk. The second closes a live reintroduction of a bug we have
already shipped once, and it is the difference between a user who hits a problem
and a user who is trapped by it.

If a *second* thing ships, it is Phase 2 — the sample project — because it is
purely additive, it is the only item here that directly attacks
time-to-first-value, and it is the one whose effect on the 33% completion rate can
be measured cleanly against the existing cohort.
