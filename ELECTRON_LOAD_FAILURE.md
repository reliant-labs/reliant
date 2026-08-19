# Why the packaged Electron app won't load (v1.6.3)

## Summary

The renderer never executes. `index.html` loads fine, so every signal the main
process has says success — `did-finish-load` fires, the preload runs, the window
is shown — but the page's JavaScript is never fetched, `#root` stays empty, and
the user gets a white window.

The cause is a mismatch between how the web bundle is built and how the packaged
app loads it:

- Vite builds with `base: "/"`, so `index.html` requests `/assets/index-<hash>.js`.
- The packaged app loads that file with `loadFile()`, giving the page a `file://`
  origin.
- Under `file://`, a root-absolute path resolves against the **filesystem root**.
  The renderer asked for `file:///assets/index-D98vjdRH.js`, which does not exist.

Nothing reports this, because the *document* loaded successfully. `did-fail-load`
only fires for the top-level frame; a subresource that 404s is silent.

This is **not** a config-injection bug. That hypothesis was checked first and
ruled out with direct evidence (below).

## Direct evidence from the shipped artifact

Downloaded `Reliant-1.6.3-mac-arm64.dmg`, mounted it, and extracted `app.asar`.

**`build-config.js` shipped fully populated.** Every key the renderer needs was
present, so the v1.6.2-class defect did not recur:

```js
module.exports = {
  "STATSIG_CLIENT_KEY": "client-uAMxcftbx55c1rBoKgUhEzaqVrHdx44qS1AJQtD0r4j",
  "SENTRY_DSN": "https://c84a8011...@o4509000353447936.ingest.us.sentry.io/4509933645856778",
  "SUPABASE_URL": "https://dash.reliantlabs.io",
  "SUPABASE_ANON_KEY": "sb_publishable_KKiB3B0EdEv7nguwKfEE5A_iY9rVXod",
  "RELIANT_SERVER_URL": "https://api.reliant.so/api",
  "RELIANT_API_URL": "https://api.reliant.so/api",
  "RELIANT_GATEWAY_URL": "",
};
```

`RELIANT_GATEWAY_URL` is empty by design — the daemon derives it (confirmed in the
log: `gateway="https://gateway-api.reliant.so/api (derived from server ...)"`).

**The shipped `index.html` asks for root-absolute assets:**

```html
<script type="module" crossorigin src="/assets/index-D98vjdRH.js"></script>
<link rel="stylesheet" crossorigin href="/assets/index-BZqrqbFH.css">
```

The files are really at `Contents/Resources/web/assets/` (179 of them). Nothing in
`main.js` rewrites these paths — the repo has never contained a
`registerFileProtocol` or `interceptFileProtocol` call.

**Loading the shipped `index.html` in Electron exactly as the app does:**

```
did-finish-load fired:      true
#root innerHTML length:     0        (EMPTY => blank screen)
module script resolved URL: file:///assets/index-D98vjdRH.js
subresource failures:       []
```

The resolved URL is the whole bug. `did-finish-load` fires and no failure is
reported, which is why the app appears to start and then shows nothing.

Copying the same bundle and rewriting only `/assets/` → `./assets/` makes it
render (`#root` length 2763), confirming the path is the sole difference.

## Which releases are affected

`base: "/"` has been in `web/vite.config.ts` since **v1.4.1** (commit `0bc523c2`,
"admin UI integration"), which changed it from `base: "./"`. v1.6.2's `index.html`
is byte-identical in its asset paths to v1.6.3's — I mounted both DMGs and
compared. So this is a long-standing break in the packaged app, not a v1.6.3
regression.

The commit that introduced it has a correct rationale for the *web* deploy:

> Relative "./" breaks fresh loads/refreshes on any 2+-segment route (e.g.
> /auth/github/callback) because "./assets/x.js" resolves against the route's dir

That reasoning is right, and it is why the fix is not "change the base back."

## The real constraint

The same bundle ships to two places with incompatible requirements:

| | needs |
|---|---|
| `app.reliantlabs.io` | `base: "/"` — a relative base breaks deep routes on refresh |
| packaged Electron | not `file://` — root-absolute paths escape to the filesystem root |

`file://` also cannot support the router at all. TanStack Router uses path-based
history, and under `file://` `history.pushState` produces `file:///chat/abc`
(verified), which 404s on reload. That is why the old code had a `will-navigate`
handler that force-reloaded `index.html` — it was papering over the same
limitation and discarding the user's route in the process.

## The fix: serve the renderer over `app://`

A custom standard scheme satisfies both sides at once. `app://bundle/` is a real
origin with a real root, so `/assets/...` resolves inside the packaged web
directory, and unknown paths fall back to `index.html` the way a web server's SPA
fallback does. The web build is untouched.

Registering it `standard: true, secure: true` is load-bearing: without it the
renderer is an opaque origin and `localStorage` throws, which would break every
Zustand persist store.

### Verified end-to-end against the real broken artifact

Serving the **actual shipped v1.6.3 bundle** over the new scheme:

```
renderer URL   : app://bundle/auth
#root children : 3          => REACT MOUNTED
storage        : localStorage OK: 1
deep route     : app://bundle/chat/abc
health watchdog: healthy
```

The app mounts, routes itself to `/auth`, and deep routes resolve — with no change
to the bundle.

### A trap worth naming

`protocol.handle()` takes the scheme **without** a trailing colon.
`protocol.handle("app:")` does not throw; it registers nothing, and every load
then fails with `ERR_FAILED` before the handler is consulted. I hit this during
validation. The code now asserts `protocol.isProtocolHandled()` after
registering and throws if it did not take, so this cannot fail silently again.

`shouldOpenExternally()` also needed updating: it treated any non-http(s) scheme
as external, so every in-app `app://` navigation would have been handed to
`shell.openExternal` — a browser tab per route change. Confirmed against the
pre-fix code, now covered by a test.

## Making the next failure diagnosable

The reason this took an artifact download to diagnose is that a packaged build
could not tell us anything. Three changes, in order of how much they matter:

### 1. The log file is the primary diagnostic

**It already worked, and nobody could find it.** `~/Library/Logs/reliant/main.log`
had everything needed to diagnose this bug the whole time. A blank renderer cannot
show its own console, and the console would not have held the interesting part
anyway — the asset resolution and the daemon spawn both happen in the main
process. So the View menu now has **Open Log File** (reveals it in Finder) and
**Copy Diagnostics** (version, renderer URL, backend state, log path as
copy-pasteable text). Both work when the window is blank, because the menu bar
still works.

### 2. A renderer health watchdog

The failure mode here is that *everything reports success*. So we now assert the
thing that actually matters — that React mounted — by checking whether `#root` has
children a few seconds after load, and logging the failed subresources if not.

Verified in both directions. Against the original `file://` load of the shipped
bundle:

```
ERROR [RendererHealth] BLANK WINDOW DETECTED after 3000ms: #root is empty —
      the app bundle never executed (assets likely failed to resolve)
ERROR [RendererHealth] renderer URL: file:///.../web/index.html
```

and silent when the app mounts correctly. It diagnoses only — it does not reload,
because a blank renderer means the bundle cannot resolve its assets and a retry
would reproduce it exactly while hiding the evidence.

### 3. DevTools, available but deliberate

Packaged builds previously forced DevTools closed via a `devtools-opened`
handler, so a broken window could not be inspected at all.

**Rejected: gating on prerelease (`-rc`) builds.** The failure we need to debug is
the one a user hits on a *stable* build; telling them to install an RC changes the
artifact under test. The threat model does not support hiding DevTools either —
the app ships its renderer as readable files on disk, so DevTools expose nothing
an attacker with local access could not already read, while the lockdown cost real
diagnosability.

DevTools are now reachable in packaged builds via an explicit **View → Toggle
Developer Tools** menu item, or `RELIANT_DEVTOOLS=1` to auto-open at launch. The
keyboard shortcuts stay disabled so an ordinary user cannot trip them by accident.

## Note on the daemon (separate problem)

The log shows the daemon failing independently of the renderer:

```
Daemon failed to become ready within 30000ms — no runtime record written
[daemon-creds] mint failed, falling back to daemon flow: fetch failed
Daemon process exited (code: null, signal: SIGKILL)
```

It spawns, resolves its target correctly (`https://api.reliant.so/api`), tries to
authenticate, and is SIGKILLed. **This is not why the window is blank** — the
renderer fails before the daemon matters, and it would fail identically with a
healthy daemon. It is a real second bug and is left for separate work; it likely
overlaps with the `reliant auth serve` env-prefix problem another agent is on.

## Files changed

| File | Change |
|---|---|
| `electron/src/app-protocol.js` | new — `app://` scheme, path resolution, SPA fallback, traversal refusal |
| `electron/src/renderer-health.js` | new — blank-window detection |
| `electron/src/diagnostics.js` | new — DevTools policy, diagnostics report |
| `electron/src/main.js` | load via `app://`, register scheme, watchdog, View-menu diagnostics, DevTools policy |
| `electron/src/window-manager.js` | second window path had the same `loadFile` bug |
| `electron/src/navigation-policy.js` | treat `app://` as in-app |

Tests: 120 pass (`npm test` in `electron/`), 26 of them new.
