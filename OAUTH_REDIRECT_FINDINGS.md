# OAuth / redirect findings

Investigation of three reported issues in the released desktop app (1.6.3).

## Summary

| # | Report | Verdict |
|---|---|---|
| 1 | "Back to first project" navigates to `http://127.0.0.1:61851/` | **Not reproduced as a separate bug.** No code builds a URL for that control. Best-supported explanation is the same root cause as #2 — see below. |
| 2 | Google sign-in calls back to `http://127.0.0.1:61655/auth/callback?code=…` | **Root cause found and fixed.** A redirect helper fell back to the packaged renderer's own local origin. |
| 3 | No loading signal on remote sign-in | **Confirmed and fixed.** A pending state existed but was driven by a shared flag, so it was wrong in a way that also lit unrelated buttons. |

Bugs 1 and 2 were investigated together, as instructed. They are **the same
defect** to the extent that #1 is real: a single helper is the only thing in the
codebase that turns a local address into a user-visible navigation target, and
fixing it removes the mechanism behind both reports. Details in "Are #1 and #2
the same?" below.

The observed ports (61851, 61655) are **not** the documented `reliant auth serve`
receiver on 19284, and they are not the daemon either — the daemon allocates
from the fixed 9190–9290 range (`electron/src/backend-manager.js`). That
correctly ruled both out, as the task suspected.

---

## Bug 2 — the root cause

### The defect

`web/src/store/authStore.ts` resolved the OAuth redirect like this:

```ts
const isElectron = !!window.electronAPI
const getOAuthRedirectUrl = async (): Promise<string> => {
  if (!isElectron || !window.electronAPI?.getOAuthRedirectUrl) {
    return `${window.location.origin}/auth/callback`   // ← the bug
  }
  ...
}
```

The guard reads "if we're not in Electron, or Electron can't give us a redirect
URL, use our own origin". In the packaged app **both halves are true at once**:

- `window.electronAPI` **exists**, so `isElectron` is `true`.
- `window.electronAPI.getOAuthRedirectUrl` **does not exist**, so the guard
  still falls through.

`getOAuthRedirectUrl` is declared in `web/src/types/electron.d.ts` but was
**never implemented in `electron/src/preload.js`**. The type declaration made it
look implemented; nothing at runtime ever provided it. The same is true of
`onOAuthCallback`, which `authStore.initialize()` also tries to register.

So every packaged build fell through to `window.location.origin` and handed the
desktop app's *own* local address to the identity provider as a public redirect
target.

### Verified against the shipped build, not just the source

Downloaded and mounted `Reliant-1.6.3-mac-arm64.dmg`:

```
$ grep -c "getOAuthRedirectUrl" .../Reliant.app/Contents/Resources/preload.js
0
$ grep -c "onOAuthCallback"     .../Reliant.app/Contents/Resources/preload.js
0
```

The shipped preload exposes `electronAPI` with ~40 methods (`openExternal`,
`authLoad`, `analyticsTrack`, …) and neither OAuth method among them. The
shipped renderer bundle contains the faulty fallback verbatim:

```
f(!bQ||!window.electronAPI?.getOAuthRedirectUrl)return`${window.location.origin}/auth/callback`;
```

…where `bQ=!!window.electronAPI`. That is the exact code path, in the exact
binary users are running.

### Why the port differs every time, and why it is intermittent

The port is whatever local address the renderer was served from for that
launch, so it changes per launch — which is why the two reports show different
ports (61851, 61655) and why it does not reproduce every time. Anything that
changes how the window was loaded on a given run changes the origin, and a
`file://` origin fails differently again (`"null"`), which is consistent with
"sometimes, not everytime".

### The fix

New `getAppURL()` in `web/src/lib/constants.ts` resolves the app's *public*
origin, and the redirect is built from that.

The important subtlety: **a local origin is not always wrong.** In web
development the app is served from `localhost:3000`, and that is a perfectly
real redirect target the browser navigates back to. Rejecting all loopback
origins would have broken local dev — an early version of this fix did exactly
that and broke three existing tests, which is what surfaced the distinction.

The real discriminator is the runtime, not the address:

- **Browser** (dev or deployed): the document origin *is* the app's address. Use it.
- **Electron**: OAuth completes in the *system browser*, which cannot reach the
  desktop shell's local address. A loopback/`file://` origin there is
  unusable, so fall back to the hosted app URL.

A `VITE_APP_URL` build-time override takes precedence for staging/preview.

### Second defect found and fixed alongside it

`StartOAuthSignIn` was missing from the long-timeout table in
`web/src/api/transport.ts`, so it inherited the 10s default while the user was
still on the provider's consent screen. Its sibling `StartOAuthFlow` — which
blocks on the same thing — was already registered at no-timeout. This is an
independent contributor to flaky sign-in and is fixed in the same PR.

---

## Are #1 and #2 the same?

**Probably yes, and the fix covers both — but I could not reproduce #1
directly, and I want to be straight about that.**

What I checked. The "Back to <project>" control is
`web/src/components/Projects/ProjectPicker.tsx:1133`. It is **not a link** — no
`href`, no URL. It is a `<button>` whose handler calls `handleProjectClick` →
`onProjectSelected` → `selectProject`, which is in-app state plus a client-side
router navigation. There is no code path there that constructs
`http://127.0.0.1:61851/`, and I found none anywhere else for that control.

Why they are still very likely one bug:

- `getAppURL()`/`window.location.origin` is the **only** mechanism in the app
  that turns an ephemeral local address into a user-visible navigation target.
- Both reports show the same signature: loopback host, ephemeral high port,
  differing between sightings.
- A bare origin with no path (`http://127.0.0.1:61851/`) is what you get when
  the renderer's own origin is treated as the app's home — the shape of an
  origin used as a base URL, not of a route.

The most likely sequence is that #1 was observed *after* an OAuth round trip had
already put the window on the bad origin: once the renderer is sitting on
`http://127.0.0.1:61851`, any in-app navigation resolves against that origin and
"back to project" lands on its root. That makes #1 a downstream symptom rather
than a separate defect, and removing the loopback origin removes it.

**Caveat worth stating plainly:** since I could not trigger #1 on demand, this
is a well-supported inference, not a verified reproduction. If it recurs after
this fix ships, the next thing to capture is `window.location.href` at the
moment of the click — that single value distinguishes "the window was already on
a bad origin" from "something else builds this URL".

Also worth flagging, though it did not produce the reported symptom: the
packaged `index.html` references assets absolutely (`/assets/index-*.js`,
`/favicon.svg`) while `window-manager.js` and `main.js` load it via
`loadFile()`. Absolute paths do not resolve under `file://`. That tension is
what the current branch name (`fix/vite-base-absolute-deep-routes`) is about and
is adjacent to this area; it is left alone here to keep the diff to the bug.

---

## Bug 3 — remote sign-in loading state

The report was "nothing appears to happen". A pending state *did* exist
(`OAuthButton` already accepted `loading`), so the real defect is subtler than
"it is missing":

`AuthScreen` passed the **same shared `loading` flag to every provider button**.
Clicking Google put *both* Google and GitHub into "Connecting…", so the UI
claimed to be signing in with a provider the user never chose.
`UpgradeAccount` had the identical problem with its `submitting` flag.

Fixed by tracking *which* provider is pending:

- the clicked provider shows the spinner, names itself
  (`Connecting to Google...`), and sets `aria-busy`;
- the others are disabled but visually idle, so a stray second click cannot
  start a competing sign-in.

`OAuthButton` was also brought onto the repo's styling contract: `cn()` instead
of a template-literal `className`, and the shared `Loader2` spinner instead of a
hand-rolled bordered `div`. Colors and layout are unchanged.

---

## Tests

Every test below was confirmed to **fail before** the corresponding fix and
**pass after**.

`web/src/store/__tests__/authStore.redirect-origin.test.ts` — reproduces the bug
exactly. Pre-fix it produced the reported URL:

```
Expected: "https://app.reliantlabs.io/auth/callback"
Received: "http://127.0.0.1:61655/auth/callback"
```

`web/src/lib/__tests__/appUrl.test.ts` — 7 cases for `getAppURL`, including the
one that matters most: a browser on `localhost:3000` keeps its origin, while
Electron on the same origin does not.

`web/src/components/__tests__/AuthScreen.oauth-pending.test.tsx` — the clicked
provider goes busy, the other one does not, and a second click while one is in
flight is refused.

Full web suite on both branches: **256 files / 1908 tests passing**, plus
`tsc --noEmit` clean.

---

## What was ruled out

- **`reliant auth serve` (port 19284)** — documented localhost receiver;
  neither observed port matches, and it is not running in the desktop app.
- **The daemon** — allocates from the fixed range 9190–9290
  (`backend-manager.js`), not ephemeral high ports.
- **`internal/auth/oauthcallback`** — binds an OS-assigned port and builds
  `http://localhost:<port>/auth/callback`, which *looks* like a match, but it is
  the Claude/Codex provider-login receiver reached via `auth.start_oauth`. It is
  a legitimate loopback receiver: the local server is the intended recipient, and
  the URL never leaves the machine.
- **`internal/auth/oauth.go`** — `LoginWithOAuthProvider` also binds
  `127.0.0.1:0` and builds `http://127.0.0.1:<port>/auth/callback`, the closest
  textual match in the codebase. It is reached from `StartOAuthSignIn` and is
  correct by design for the CLI/daemon flow, where the local server is genuinely
  the endpoint. It is not what the renderer sent to Google.

The distinction that matters: a loopback callback is *correct* when the local
process is the intended receiver, and *wrong* when it is published to a
third-party provider as the app's public address. Only the renderer's redirect
did the latter.
