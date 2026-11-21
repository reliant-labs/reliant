# Visual baseline

Screenshots of the web app's key surfaces, compared pixel for pixel with the
committed baselines in `__screenshots__/`. It exists so a styling change shows
exactly what it moved, and so a change meant to touch one surface proves it
touched nothing else.

| Project | Viewport | Surfaces |
|---|---|---|
| `desktop-dark` | 1440×900, dark | home + sidebar, chat (simplified and detailed), app overview, observability errors, environment, the project selector, Projects, a project's page, billing, usage |
| `desktop-light` | 1440×900, light | the same |
| `mobile-dark` | 390×844, dark, touch | chat list, chat, app overview, observability errors, environment, the project selector (the console's app switcher), Projects, a project's page |

The surface list is `SURFACES` in `surfaces.visual.ts`. The chat uses every
tool renderer the timeline has (`fixtures/chat.ts`). The project catalog
(`fixtures/projects.ts`) is in the shared table; the selector, Projects and a
project's page add their pins per surface (`selectorSurfaceFixtures`,
`projectsPageSurfaceFixtures`), because the `sidebar.projects` setting also
moves every other surface's sidebar.

## Running it

```bash
task test:web:visual                                # build, then compare every surface
task test:web:visual VISUAL_SKIP_BUILD=1            # reuse frontends/web/dist
task test:web:visual -- --grep chat                 # one surface (titles: "<surface> (<viewport>)")
task test:web:visual -- --project=desktop-light     # one project
task test:web:visual -- --update-snapshots          # re-capture the baselines
```

It takes about a minute after the build (two to five on a loaded machine).
A failure writes the expected, actual and diff images to
`frontends/web/test-results-visual/`, and `npx playwright show-report
playwright-report-visual` (from `frontends/web`) shows them side by side.

It runs in the pinned Playwright Linux image (`scripts/visual-docker.sh`:
`mcr.microsoft.com/playwright`, the exact release the lockfile installs,
pinned by digest), so the baselines are the same pixels on a Mac, a Linux
laptop and a cloud machine. It needs Docker, and says so when Docker is
missing. Bumping `@playwright/test` means bumping the image in that script
(it refuses to run until you do) and re-capturing. Run outside the image,
the suite refuses rather than compares; `VISUAL_ALLOW_HOST=1 npx playwright
test -c playwright.visual.config.ts` runs it on the host for debugging, with
pixels that are not comparable to the baselines.

It is opt-in: no gate runs it, because it needs a production build and a
browser.

## What makes it deterministic

The app is the real production build (`vite preview` of `dist/`), with no
backend. Before the app loads, `harness.ts`:

- answers every network call from the typed fixtures in `fixtures/`. The
  page's `fetch` is replaced (`shim.ts`) and forwards each Connect RPC to
  Node (`rpc.ts`), which encodes binary streams with the app's own generated
  schemas. Live subscriptions stay open, as they do in production, so nothing
  reconnects on a timer. An RPC with no fixture answers `{}`, and
  `VISUAL_FXLOG=1` lists those;
- freezes `Date` at `FIXED_NOW` (2026-11-18 15:30 UTC). Every fixture
  timestamp is relative to it, so "5m ago" never changes;
- seeds `Math.random`, and pins the locale (en-US) and time zone (UTC);
- stores the project's theme, and uses the default color scheme and reduced
  motion.

Before each capture, `settle` waits for web fonts and for the network to go
quiet. It times itself in Node (the page's clock is frozen), and fails with
"the network never went quiet" after 20s, so a request that never finishes
is a red test, not a hang (`harness.visual.ts` proves it). Then
`toHaveScreenshot` disables animations, hides the caret and waits for two
identical frames. A capture may differ from its baseline by at most
100 pixels past the default per-pixel threshold (measured noise: under 110
pixels, each at most 10/255 off, all inside the threshold). The pointer is
parked off the page so no row shows its hover state. The build's version
string (settings footer) is masked, so a baseline does not depend on the
commit that built it.

This harness is a port of the November UX review's fake network
(`dev-docs/design/app-ux-review-2026-11/HARNESS.md`), with its fixtures
rewritten as TypeScript.

## Before and after in a pull request

A UI pull request carries its own visual diff:

1. Run `task test:web:visual` on the branch. A failure that matches the change
   is the expected diff. A failure on a surface the change should not touch is
   a finding: fix it, or explain it in the PR body.
2. Re-capture: `task test:web:visual VISUAL_SKIP_BUILD=1 -- --update-snapshots`.
3. Commit the changed PNGs with the change. GitHub's image diff (2-up, swipe,
   onion skin) on those files is the before and after. The reviewer reads it.
4. In the PR body, list the surfaces that moved and say why. A change meant to
   leave the UI alone says "baseline unchanged".

A pull request that changes the fixtures or the harness re-captures every
baseline it moves, in its own commit, and moves nothing else.

## Adding a surface

Add a row to `SURFACES`: the route, the viewports, and a `ready` locator that
is visible only once the surface has its data. Add the fixtures it needs to
`fixtures/` (run it with `VISUAL_FXLOG=1` to see the RPCs that answered `{}`),
then capture it with `--update-snapshots`. Run the suite twice more without
`--update-snapshots`: a surface that fails a clean re-run is not deterministic
yet, and must not merge.
