# Responsive UI acceptance standards

Implementation contract for the re-port of closed PR #961.
These are layout/input policies, not a list of supported device model names.
Preserve desktop behavior and original message/block identities; do not replace
the timeline virtualizer or silently hide approvals, questions, errors, or the
final answer inside collapsed tool activity.

## Available width and input

- Phones: support 320 CSS px through ordinary phone widths, portrait and
  landscape. A compact composer must keep send/stop/queue reachable; settings,
  presets, required parameters and attachments belong in accessible sheets.
- Tablets: adapt to available width, including narrow multitasking windows.
  Touch capability alone must not redirect every tablet into phone navigation.
  Wide touch layouts retain useful workspace navigation and desktop capability.
- Duo/foldables: treat each available pane/window width as authoritative. Never
  infer a posture or hinge from a model string. The 540px automated pane case is
  a width regression check, **not** proof of an actual Duo or hinge behavior.
- Input is independent of width: mouse, touch and hardware keyboard must work.
  Touch targets are at least 44×44 CSS px on a coarse pointer, including
  icon-only controls, composer chips' × and sheet close buttons; with a fine
  pointer (the `chromium` project's mouse runs) controls keep their desktop
  size, so assert 44×44 only when the project has touch. Editable fields use
  at least 16px text to avoid iOS focus zoom.
- Return in the composer follows the pointer, not the width: on a coarse
  pointer it inserts a newline and only the action button or Cmd/Ctrl+Enter
  sends (or queues); a fine pointer keeps Enter-to-send
  (`dev-docs/design/mobile-chat-redesign.md` §5.3, decision D2).
- No page-level horizontal overflow. Long prose, paths and URLs wrap. Only
  explicitly identified code/table viewers may scroll horizontally locally;
  never whitelist all overflowing descendants or conceal a failure with root
  `overflow-x: hidden`. Their surrounding cards still fit the available width.
- Safe areas and the visual viewport constrain fixed chrome and composer height.
  A reduced-height viewport is not a software keyboard simulation.

## Feature parity is independent of presentation

Chat configuration must use the same schema, values, validation, permissions and
mutation paths on desktop and touch layouts. Desktop may expose popovers and
inline controls while phones/tablets use sheets, but model/provider selection,
thinking and sampling controls, supported runtime policies, workflow presets,
required inputs, toolbar parameters and advanced inputs must remain discoverable.
Use shared settings content with adaptive shells, not a second reduced mobile
form. Any omission requires a named product/permission/lifecycle reason, never
just `isMobile`. Disabled and unavailable features explain why; missing/loading
workflow data must not look like a deliberately attachment-only options menu.

Parity fixtures must include a populated workflow schema, model catalog and
presets, and assertions must change configuration and verify the submitted values.
An empty RPC fallback and a passing 'sheet opens' test do not establish parity.
Native preview fixtures must use the same meaningful data; label mocked evidence
separately from authenticated backend journeys.

## Interaction and state

Dialogs have an accessible name, initial focus, a contained tab sequence, Escape
 dismissal, and focus restoration. Controls must be centre-hit-testable and fully
reachable, not merely present in the DOM. Native text selection remains available.
`forge-ui/modal.tsx` stays a verbatim Forge install. Safari never focuses a button
on tap, so a sheet's wrapper calls `focusDialogOpener()` (`src/lib/dialogOpener.ts`)
in a layout effect, as `ComposerSheet` does, to make Forge's Modal restore focus to
the control that opened it. A sheet whose last focusable is a link needs its own Tab
trap like `MobileNavDrawer`'s, because WebKit's Tab order skips links.
Rotation, resizing, keyboard opening and sheet dismissal must preserve drafts,
uploads, selected workflow/parameters, pending question answers and chat identity.
Failures retain user input and offer recovery; repeated submission must not create
duplicate chats. Streaming completion and stream recovery need explicit evidence,
not a green shell or a cleanly ended empty stream fixture.

## Automated guardrail and bounded matrix

`frontends/web/e2e/mobile-responsive.spec.ts` uses real rendered `/m/chats` and
`/m/inbox` routes, plus new/existing chat composers, with RPC-boundary fixtures
from `mobileMocks.ts`. Composer drafts survive wide, pane and short-window resizes
and options-sheet dismissal, in the same textarea element throughout (a remount
that refills the value would still drop the caret, undo stack and IME
composition, so the test marks the element and requires it back). It asserts
navigation dialog focus/escape/restore, target geometry and hit testing,
page overflow, and question selection/submission through rotation, 540px pane,
320px narrow window and restoration. Required tests have no runtime skips and
must run against the built preview as well as dev. Existing mobile specs and
all desktop Chromium tests remain intact.

The shared `e2e/helpers/responsive.ts` width/input contract drives four runs:

| Project | Scope |
| --- | --- |
| chromium | Existing desktop suite plus the four responsive journeys with mouse |
| responsive-chromium-touch | Four journeys at 390×844, touch |
| responsive-webkit-phone | Four journeys at 390×844, touch |
| responsive-webkit-tablet | Four journeys at 834×1112, touch |

This is sixteen representative executions, not all specs × all widths × all
browsers. The journeys resize within each execution. Use targeted geometry tests
for additional breakpoints rather than multiplying the entire suite. Explicit
mobile URLs test those production surfaces at wide sizes; they do not prove
that a wide tablet should automatically route there.

Run `task test:web:e2e` for the full browser lane, or
`task test:web:e2e -- e2e/mobile-responsive.spec.ts` for the focused guardrail.
The task builds the renderer, installs Chromium **and WebKit**, forces production
preview mode, and selects the four projects. CI supplies `PLAYWRIGHT_DEPS=--with-deps`
for Linux system dependencies. CI runs the lane as two parallel jobs,
`task test:web:e2e:desktop` (the Chromium project) and
`task test:web:e2e:responsive` (the three touch projects), so the desktop suite
never waits on WebKit's system-package install. `task test:e2e` is the Go story
suite, not this lane.
The CI path selector already includes all `frontends/web/` implementation, E2E,
helper and config changes; changes to the Taskfile or browser job select all lanes.

Dev-only console harness tests and opt-in audit captures remain supplemental;
their skips cannot stand in for this required route suite. Failure traces and
screenshots are retained. No screenshot goldens are added until font rendering,
timestamps, animation and engine/platform are fixed and the images are reviewed.
Source-string/unit checks complement, never replace, rendered assertions.

## Native acceptance remains separate

Playwright WebKit is not iOS Safari. Browser touch emulation does not prove
software keyboard, Home Screen installation, Safari chrome, split-screen,
fold/hinge posture, or native safe areas. Device Hub acceptance must record actual
OS/device, isolated device ownership, orientation/posture and keyboard evidence.
Refresh the inventory and create/claim isolated simulators; never reset, stop or
reuse another run's booted device. Follow the brief's iPhone, Duo and iPad targets.
Record unsupported simulator capabilities and missing evidence as blockers, not
passes. No native simulator testing is claimed by this automated guardrail.
