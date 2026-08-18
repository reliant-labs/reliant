# Pinned chat header: wrong handoff level + scroll jitter

Briefing for the agent fixing two reported defects in the interleaved timeline's
pinned user-message header. Written by the orchestrating agent after reading the
code; the VERIFIED facts below are already established and must not be
re-derived.

## The two reports

**(a) The pin doesn't hand off at the right level.** As you scroll, the header
should swap from one user message to the next at the moment the boundary between
their sections crosses the top of the viewport. It currently swaps at the wrong
moment — the outgoing pin lingers, or the incoming one takes over early.

**(b) It jitters.** The header (and/or the timeline under it) visibly shakes
during scroll.

These are almost certainly the same defect seen twice: the pin is driven by a
row-granular signal, and a row can be much taller than the swap it is being
asked to express.

## VERIFIED — file map

All of this was read directly. Do not re-search for it.

- `web/src/components/Chat/thread-views/InterleavedTimeline.tsx` (1626 lines) —
  owns everything. Relevant regions:
  - **L775-788** `userMessageForItem`: a `useMemo` producing a positional array
    where `mapping[i]` = index of the user message heading item `i`'s section.
  - **L790-792** `pinnedUserMessageIdx` state + `isHoveringPinned`.
  - **L893-916** `applyPinnedUserMessage(firstVisible)` — the whole handoff rule:
    ```ts
    const layerUserIdx = userMessageForItem[firstVisible] ?? null;
    const nextPinned = layerUserIdx !== null && layerUserIdx < firstVisible ? layerUserIdx : null;
    ```
    Guarded by `pinnedUserMessageIdxRef` so setState only fires on change.
  - **L918-930** an effect re-running the above when the mapping changes (fixes a
    prior "wrong chat pinned" bug — do not regress it).
  - **L932-951** `handleRangeChanged` — converts Virtuoso's SHIFTED index to DATA
    index via `toDataIndex`, stores `lastRangeRef`, calls
    `applyPinnedUserMessage(dataRange.startIndex)`.
  - **L953-1006** per-thread scroll memory + thread-switch reset (clears the pin).
  - **L1009-1024** `handleJumpToPinned` and the `pinnedUserMsg` derivation.
  - **L1159-1183** publishes `--chat-pinned-header-h` from
    `pinnedHeaderRef.current.offsetHeight` via a `ResizeObserver`.
  - **L1557-1598** the header render: `absolute inset-x-0 top-0 z-50 ... bg-background`
    overlay, containing `<ChatMessage ... pinned />` and a hover "Jump to" button.
  - **L1599-1623** the `<Virtuoso>` element. Props that matter here:
    `firstItemIndex`, `rangeChanged={handleRangeChanged}`, `overscan={200}`,
    `increaseViewportBy={200}`, `atBottomThreshold={80}`.
- `web/src/components/Chat/ChatMessage.tsx` — consumes `pinned` (L86, L174) to
  change layout (L908-960) and reads `var(--chat-pinned-header-h, 0px)` for its
  sticky hover toolbar (L841).
- `web/src/components/Chat/thread-views/__tests__/pinnedHeader.test.ts` — 79
  lines, pure-function tests that RE-IMPLEMENT `buildUserMessageForItem` and
  `pinnedFor` locally rather than importing them. Extend or replace; see
  "Suggested approach".
- `web/src/lib/scrollDebug.ts` — a purpose-built scroll recorder for exactly this
  jitter class. Read its header comment. It is **on by default in dev**, samples
  `scrollTop` every animation frame, attributes each frame to
  `user | ours | correction | unknown | none`, auto-detects a jitter pattern, and
  **dumps surrounding frames to disk**. `window.__scrollDebug.*` is available in
  the console. Use it before theorising.
- `web/src/components/Chat/thread-views/followState.ts` + its test — the
  follow-to-bottom state machine. Related but a different concern; touch only if
  the evidence points there.

## VERIFIED — the two index spaces (this is a trap)

`InterleavedTimeline` sets `firstItemIndex` (base `100_000`) for Virtuoso's
prepend protocol. That splits callbacks into two index spaces:

- **SHIFTED** (`data index + firstItemIndex`): `rangeChanged`, `startReached`,
  and the index given to `itemContent` / `computeItemKey`.
- **DATA** (plain index into `timelineItems`): `scrollToIndex`,
  `initialTopMostItemIndex`.

Everything below the component boundary is DATA space; conversion happens at
exactly three call sites via `toDataIndex`. If you add a new Virtuoso callback,
convert at the boundary. The long comment at L800-830 explains why this is
mandatory rather than an optimization — read it before changing `firstItemIndex`
handling.

## Leading hypotheses (NOT verified — your job to confirm or kill)

1. **Row granularity is the handoff bug.** `range.startIndex` is the index of the
   first *partially* visible row. A long assistant message or a tall tool card
   can occupy several viewport heights, so `startIndex` stays constant across a
   large scroll and the pin cannot change mid-row. The correct handoff moment is
   geometric — "the next user message's top edge has crossed the header's bottom
   edge" — not "the first visible row's index changed". Suspect the fix is to
   resolve the pin from measured offsets (a top-sentinel `IntersectionObserver`
   per user-message row, or `Virtuoso`'s item offsets) rather than from
   `startIndex` alone.

2. **The `layerUserIdx < firstVisible` guard is the early/late swap.** It hides
   the pin whenever the heading user message is itself the first visible row —
   but "first visible" includes a row scrolled 95% off the top, which is exactly
   when the pin is most needed. That single comparison is the most likely direct
   cause of report (a).

3. **The jitter is a feedback loop through the header's own height.** Showing the
   header does not change the scroller's geometry (it is `absolute`), but
   `--chat-pinned-header-h` changes, `ChatMessage` sticky offsets move, and
   `ChatMessage` renders differently when `pinned`. If a pin toggle changes any
   measured height that feeds back into `rangeChanged`, you get oscillation at
   the boundary: pin on → geometry shifts → `startIndex` changes → pin off →
   repeat. `scrollDebug`'s `correction` attribution will show this directly.
   Virtuoso's internal `SIZE_INCREASED` correction is the other known writer of
   `scrollTop` and emits no event — the recorder exists to attribute it.

4. **`overscan`/`increaseViewportBy` of 200px may be skewing `startIndex`**
   relative to the true visual top. Confirm which one Virtuoso 4.18 reports in
   `rangeChanged` before trusting `startIndex` as "the top row".

Do not stop at the first plausible story. Report (b) may have a cause entirely
independent of (a).

## Constraints

- **This project has not launched. No backwards compatibility is required.**
  Delete old code paths rather than branching around them. If the positional
  `userMessageForItem` approach is the wrong primitive, replace it.
- Do not regress the two documented prior fixes, both of which have comments in
  place explaining them: (i) the pin must be recomputed when rows are inserted
  *above* the viewport, not only on scroll (L918-930); (ii) the pin must be
  cleared on thread switch rather than carried across (L963-980).
- Styling: follow the web styling contract in the project memory — semantic
  Tailwind classes and `cn()`, no ad-hoc inline styles except genuine runtime
  geometry (a measured pixel offset qualifies).
- **Do not run any git command.** Not `commit`, not `stash`, not `checkout`.
  Multiple agents share this checkout. Leave changes uncommitted and report them.
- Do not kill processes, and do not run `forge env down`. Vite hot-reloads; you
  should never need to restart anything.

## Suggested approach

1. Reproduce with instrumentation before editing. `scrollDebug` is on in dev and
   auto-dumps; get a dump of an actual jitter episode and read the attribution
   column. That tells you whether the jitter is ours, Virtuoso's correction, or a
   render loop — and it costs one scroll.
2. Write a failing test first for each defect. The existing `pinnedHeader.test.ts`
   is pure-function and duplicates the logic locally; prefer **exporting the real
   resolver from a module** (e.g. a `pinnedHeader.ts` beside `followState.ts`)
   and testing that, so the test can no longer pass while the component is wrong.
   For the geometric behaviour, `scrollUpReleasesFollow.test.tsx` in the same
   directory is the precedent for a component-level test with a mocked scroller.
3. Confirm each test FAILS before the fix and PASSES after. A fix without a
   reproduction is a guess.
4. Verify: `cd web && npx vitest run src/components/Chat` (or the project's
   configured test command — check `web/package.json` scripts). Also
   `npm run lint:css` if you touch stylesheets.

## Reporting

Report actual command output. If you cannot reproduce the jitter, say so plainly
and describe what you tried — that is a useful result, and far better than a
speculative fix presented as a confirmed one. Update this file with what you
found; it is the durable record.

---

# OUTCOME — both defects fixed (2026-08-18)

## Root cause: one defect, not two

Hypotheses 1 and 2 were both correct and are the **same** defect. Hypothesis 3
(feedback loop through the header's height) was real but latent — it is guarded
now rather than having been the trigger. Hypothesis 4 is confirmed as the
mechanism behind hypothesis 1.

The pin was a pure function of `rangeChanged.startIndex`. That single choice
produced both reports, because `startIndex` is **the first RENDERED row, not the
visual top**, and a row is not the unit the handoff happens in:

- It is inflated by `overscan={200}` / `increaseViewportBy={200}` (hypothesis 4
  — confirmed against Virtuoso 4.18.1's source: `rangeChanged` is derived from
  `listState.items`, the rendered set, not from a visible-range calculation).
- It is **not monotonic**, so it is not a usable proxy for scroll position.
- A single row can be several viewport heights tall, so `startIndex` cannot
  change at all while you scroll through one long assistant message — the
  handoff moment is simply not expressible in row indices.

## Evidence — measured, from real recorded sessions

`scrollDebug` had **already auto-captured 55 jitter dumps** from real use, in
`.reliant/logs/main.log` and two rotated archives. No new reproduction was
needed; the recorder did its job. Reassembling the chunked JSON payloads and
measuring consecutive `rangeChanged` pairs:

```
consecutive rangeChanged pairs across 55 real dumps: 94
  startIndex moved BACKWARD (non-monotonic):          23
  startIndex jumped >1 row while scrollTop moved <5px: 13
```

A representative frame sequence (dump 4, no user input at all — every frame
attributed `correction`) shows `startIndex` stepping across single animation
frames:

```
115 -> 111 -> 112 -> 105 -> 115 -> 104
```

Driving a visible overlay from that toggles it on and off between frames. That
is defect (b). The component test reproduces it exactly: with **geometry held
constant** and only `startIndex` varying over the recorded pattern, the old code
produced `[null, "m2", null, "m0", null, "m2", null]` — the header flickering
on and off, and briefly showing the *wrong* message.

Note the attribution: the jitter frames are `correction`, i.e. Virtuoso's
unobservable `SIZE_INCREASED` pass, **not** our follow layer. The follow-state
machine was not at fault and was not touched.

## The fix

`web/src/components/Chat/thread-views/pinnedHeader.ts` (new) — the real
resolver, extracted so it can be tested directly:

- `resolvePinnedUserMessage()` decides from **measured row geometry**: find the
  row whose bottom edge has not yet passed the crossing line, take its section
  heading, and pin it if its top edge is at or above that line.
- The crossing line is the **header's own bottom edge**, not the viewport top —
  the header occludes that band, so a heading is "taken over" once it is behind
  the header. This is what fixes the *level* in defect (a).
- `measureRows()` does one batched `getBoundingClientRect` pass over the
  rendered rows only (bounded by viewport + overscan, not conversation length).

**The `layerUserIdx < firstVisible` guard is gone**, per hypothesis 2. It hid
the pin exactly when the heading was scrolled 95% off the top — when the header
is most needed.

**Release hysteresis (24px), applied to releasing a pin but never to engaging
one.** This closes hypothesis 3 by construction: the header's height *is* the
crossing line, so the decision reads a geometry the decision itself produces.
A symmetric threshold could be crossed back and forth by a sub-pixel layout
correction; an asymmetric band cannot oscillate, because leaving the pinned
state costs strictly more than entering it.

**Index spaces**: rows carry a new `data-timeline-index` attribute in **DATA**
space. Virtuoso's own `data-item-index` sits on the wrapper above and is
**SHIFTED** — reading that one would offset every lookup into
`userMessageForItem`. The trap is documented at the attribute's definition.

Both prior fixes are preserved: the pin is still recomputed when the mapping
changes (rows inserted above the viewport), and still cleared on thread switch.
The first is now *structurally* safer — the pin is re-resolved from live
geometry every time rather than remembered, so a stale index cannot survive.

## Tests

- `__tests__/pinnedHeader.test.ts` — **rewritten**. It previously
  re-implemented `buildUserMessageForItem`/`pinnedFor` locally, so it passed
  while both defects were live in the component. It now imports and exercises
  the real resolver (13 tests), including the engage/release asymmetry.
- `__tests__/pinnedHeaderHandoff.test.tsx` — **new**, component-level with a
  mocked scroller (following `scrollUpReleasesFollow.test.tsx`), driving real
  geometry. 4 tests: pins while scrolled off the top; hands off exactly at the
  crossing; **does not flicker under the recorded `startIndex` pattern**; no
  header at the top of the transcript.

Verified failing before the fix (3 of 4 failed, including both defects) and
passing after. Full suite: **254 files / 1908 tests passed**. `lint:css` clean,
`eslint` clean on all touched files.

## Note for whoever is next

`tsc -b` reports one error in `src/components/Chat/useBackgroundWork.ts:110`
(`ThreadOrigin` argument type). That file is **another agent's uncommitted
in-flight edit** — untouched by this work, and unrelated to the pinned header.
