# Visual baseline

Screenshots of the web app's key surfaces, compared pixel for pixel with the
committed baselines in `__screenshots__/`. It exists so a styling change shows
exactly what it moved, and so a change meant to touch one surface proves it
touched nothing else.

| Project | Viewport | Surfaces |
|---|---|---|
| `desktop-dark` | 1440×900, dark | home + sidebar, chat (simplified and detailed), app overview, app code card, observability errors, environment, the project selector, Projects, a project's page, billing, usage, Organization GitHub |
| `desktop-light` | 1440×900, light | the same |
| `mobile-dark` | 390×844, dark, touch | chat list, chat, app overview, app code card, observability errors, environment, the project selector (the console's app switcher), Projects, a project's page |

The surface list is `SURFACES` in `surfaces.visual.ts`. The data is the shop
fixture pack (`src/fixtures/shop/`): the same world the public demo and the
hosted preview show (the app `shop`, its services api, worker, inventory and
shop-web, releases v1.8.x), served by the production build itself. The chat
uses every tool renderer the timeline has
(`src/fixtures/shop/data/transcripts/cart-totals.json`). A surface that needs a
variant of the world names an **overlay** (`harness.overlays`), registered in
`src/fixtures/shop/overlays.ts`:

| Overlay | What changes |
|---|---|
| `no-daemons` | Alex has no machine (the daemonless home, Agents with no sandbox) |
| `tests-unimplemented` | the control plane does not serve TestsService |
| `chat-no-failed-send` | the chat stream without the client-only failed send (the phone's chat screen) |
| `project-selector` | two pins in `sidebar.projects` (they also move the sidebar, so only these surfaces see them) |
| `projects-page` | the pins, plus a second app with no code here (`ledger-worker`) |
| `app-code-card` | shop deployed from two repositories, also held by a fork and a teammate's project |
| `setup-guide-expanded`, `setup-guide-collapsed` | the Setup Guide shown, six of eight done (saved settings) |
| `project-pickers` | both projects on the cloud machine, and the ListChats/StartChat requests noted for `mobile-project-pickers.visual.ts` (StartChat still refused) |
| `empty-reads` | every page: a read no fixture answers gets the empty response, as a quiet backend would |
| `chat-attached-projects` | the fixture chat with docs attached beside shop (the composer's project chips) |
| `new-chat-installs` | where Alex's projects are checked out, for the phone's new chat |
| `chat-agents` | a chat opened by URL, never listed: one turn that starts twelve sub-agents (8 done, 3 running, 1 failed) |
| `chart-hover` | a dashboard opened by URL (a figure and a two-series line) for the chart hover surfaces |
| `sandboxes-dashboard` | the platform's Sandboxes dashboard, opened by URL, every panel answered |
| `pending:reliant.v1.ChatService/ListChats` | the harness's own self-test: the chat list never answers |

A new surface gets its data the same way: from the pack, or from a named
overlay, never from a fixture file of its own under `e2e/visual/`.

**One world: an edit to `src/fixtures/shop/**` moves these baselines.** That
is the design, not a side effect. The demo's story and the screenshots are the
same data, so a PR that changes the pack runs this suite and commits what it
moves (`dev-docs/testing-affected.md` says why no affected-test lane does it
for you).

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

## Where it gates

- **The merge queue** (owner, 2026-11-24; reliant#3446) runs it, compare only,
  on the merge group's own commit (main plus the entries ahead of yours),
  whenever the group reaches the lane's inputs: `frontends/web` sources,
  styles, assets, build config and lockfile, the `sdk/ts` sources, and this
  directory (`scripts/ci-lanes.sh`, lane `visual`). It is the queue's
  `Visual baseline` job (`scripts/visual-gate.sh`), and `CI result` needs it.
  A capture that differs fails the entry, names the surfaces, and says how to
  re-capture; the diffs are the run's `visual-results` artifact. So capture on
  a branch rebased onto current main, and re-capture after any later commit,
  a pr-fix push included, that moves a surface.
- **Every night on main** (`.github/workflows/visual-nightly.yml`), the same
  script: a mismatch opens or updates the one `visual:stale` issue, and the
  next green run closes it. It catches what moved a surface from outside the
  queue's paths, and never blocks a merge.

## Re-capture without docker: `visual-recapture.yml`

No docker where you are (pr-fix's machine has none)? Dispatch the re-capture on
a hosted runner, always from main:

```sh
gh workflow run visual-recapture.yml --ref main -f pr=<n> -f head=<full head sha>
```

It merges current main into the head, runs `task test:web:visual --
--update-snapshots=changed` in the pinned image, and commits ONLY the changed
PNGs under `__screenshots__/` onto the head as `test(web): re-capture visual
baselines`, pushed without force (a head that moved refuses it). The job
that runs the PR's code can only read; the job that pushes runs main's
`.github/scripts/visual-recapture-apply.sh` and nothing from the PR, refuses a
symlink anywhere on a baseline's path in the PR's checkout, and dispatches no
workflow (on the PR's branch that would run the PR's own definition with this
repository's secrets). The push is the Reliant App's, with a token minted for
this repository alone in the push step (repository variable
`RELIANT_APP_CLIENT_ID`, secret `RELIANT_APP_PRIVATE_KEY`); the job's
`GITHUB_TOKEN` can only read, and without the App's credentials nothing is
pushed. An App push starts `ci.yml` like any push, and control-plane's push gate
(cp#3624) trusts a same-repository head the bot pushed, so its re-trigger of a
required check no run reported is allowed too; it refuses a `GITHUB_TOKEN`
push. control-plane's pr-fix
dispatches it when a merge group failed only on `Visual baseline`. A run
where every capture already matches pushes nothing: the failure came from an
entry ahead in the queue, or a nondeterministic surface (below). Still list
the moved surfaces in the PR body.

Neither gate ever re-captures or retries. A surface that differs on a commit whose
UI did not change is nondeterministic: delete it from `SURFACES` with a
`flake:removed` issue holding its entry and the diff, never re-run to green.

## What makes it deterministic

The app is the real production build (`vite preview` of `dist/`), with no
backend: the harness turns on **fixture mode**, the same switch the hosted
preview uses. Before the app loads, `harness.ts`:

- serves `config.js` with `FIXTURES_SCENARIO = "shop"` (read-only), so the
  build lazily installs the shop pack in the page (`src/fixtures/install.ts`):
  every Connect RPC goes through the app's own clients and its REAL interceptor
  chain to an in-page fixture transport. Live subscriptions stay open, as they
  do in production, so nothing reconnects on a timer. A read no fixture
  answers gets the empty response (the `empty-reads` overlay), and
  `VISUAL_FXLOG=1` lists those (and any refused write) per surface. Writes are
  refused, never answered, by the proto's read-only rule;
- leaves the rest of the network to `src/fixtures/outside.ts`, fixture mode's
  fetch boundary: the fake Supabase session for the pack's user (Alex), health
  and telemetry are answered in the page, and anything else throws. Every
  surface fails on an RPC that escaped the transport, a fetch outside that
  boundary, a request off the page's origin, a page error, or a refused write
  it is not known to make (`KNOWN_REFUSALS` in `surfaces.visual.ts`, asserted
  exactly per surface);
- freezes `Date` at `FIXED_NOW` (2026-11-18 15:30 UTC, `clock.ts`) and pins the
  pack's clock to it (`window.__forgeFixtures.now`). Every fixture timestamp
  is relative to it, so "5m ago" never changes;
- seeds `Math.random`, stubs WebSockets and `sendBeacon` (`shim.ts`), and pins
  the locale (en-US) and time zone (UTC);
- stores the project's theme, and uses the default color scheme and reduced
  motion.

Before each capture, `settle` waits for web fonts and for the fixture probe's
counters (`window.__forgeFixtures.started` / `inflight`) to go quiet. It times itself in Node (the page's clock is frozen), and fails with
"the network never went quiet" after 20s, so a request that never finishes
is a red test, not a hang (`harness.visual.ts` proves it). Then
`toHaveScreenshot` disables animations, hides the caret and waits for two
identical frames. A capture may differ from its baseline by at most
100 pixels past the default per-pixel threshold (measured noise: under 110
pixels, each at most 10/255 off, all inside the threshold). The pointer is
parked off the page so no row shows its hover state; a surface whose subject
IS a hover state (`shell-chart-hover`, `shell-sparkline-hover`,
`shell-dashboards-card-hover`: a chart's crosshair and tooltip) puts it back in
its `beforeShot` step, which runs after the park, right before the capture.
A long page (`shell-sandboxes-dashboard`) grows the viewport to its whole
height there instead (`fitViewportToContent`), so it is one capture. The build's version
string (settings footer) is pinned and masked, so a baseline does not depend
on the commit that built it: `task test:web:visual` builds with
`RELEASE_VERSION=0.0.0-visual`, `scripts/visual-docker.sh` refuses a `dist/`
built with any other version, and the footer never wraps (a long version is
truncated). The composer's example prompt is a function of the chat id
(`src/components/Chat/examplePrompt.ts`), not of `Math.random`, so it does
not move when a change reorders the seeded random draws.

The harness began as a port of the November UX review's fake network
(`dev-docs/design/app-ux-review-2026-11/HARNESS.md`); its fetch shim and its
own fixture world were replaced by fixture mode and the shop pack.

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
is visible only once the surface has its data. The data comes from the shop
pack (run it with `VISUAL_FXLOG=1` to see the reads that answered empty): add
what the surface needs to the pack, or, for a variant only this surface may
see, a named overlay in `src/fixtures/shop/overlays.ts`. Then capture it with
`--update-snapshots`. Run the suite twice more without
`--update-snapshots`: a surface that fails a clean re-run is not deterministic
yet, and must not merge.
