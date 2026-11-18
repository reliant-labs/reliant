# Verifying auth events and `forge obs users` after release

This PR's standard auth events (`observe.TrackAuth`, devidp's
`auth_signed_up` / `auth_signed_in`, the server's daily `resumed` heartbeat,
the browser's `trackAuthEvent` / `identifyUser`) are product events: they
reach the control plane's per-org `product_events` table through the hosted
ingest path, and `forge obs users` reads them back with the statement the
console's Users card runs (`pkg/obsquery`). Neither half can be proven by a
unit test: the ingest is the production control plane's, and the read is its
`GetUserActivity` endpoint. So both are checked once, against a live env,
after the first release carrying this forge and a control-plane prod release
that pins it.

## Check after deploy

Use a hosted env of a project on this forge whose app signs people in with
managed identity (devidp) or calls `TrackAuth` itself. Run each command from
that project's checkout.

1. **A sign-in reaches `product_events`.** Sign in to the app in a browser
   without GPC or Do Not Track, as an account you own. Within five minutes:

   ```bash
   reliant forge obs users <env> --since 1h --json
   ```

   Expected: exit 0, `scope.backend` is the hosted control plane, and the env's
   `envs[].current.active` is at least 1 with `clients` listing `backend`
   (devidp's sign-in and the server heartbeat). A browser app that calls
   `identifyUser` also lists `web`.

2. **The user is the opaque subject.** With the account's provider subject
   (never its email):

   ```bash
   reliant forge obs sessions <env> --user <subject> --since 1h
   ```

   Expected: at least one session for that id. No record anywhere carries an
   email: the emitters refuse an id containing `@`.

3. **The heartbeat is daily.** Reload the app several times, and call any
   authenticated rpc a few times. `reliant forge obs users <env> --since 1h
--series 1 --json` still counts the account once for today.

4. **The console agrees.** The env's Overview Users card and the Users
   dashboard show the same active count as step 1 for the same range.

A step that answers "could not look" (exit 2) is not a pass: the control
plane's user-activity read is not deployed, or the env is not hosted. Report
it as not proven.

## If a check fails: disable, then fix forward

There is no rollback of a release; each half switches off without a redeploy
of the code:

- **Server-side events** (devidp's sign-up/sign-in events and the daily
  heartbeat): `forge.ObservabilitySignals {product_events = False}` in the
  env's KCL renders `FORGE_PRODUCT_EVENTS=off`, which forge's automatic
  product events honour (`observe.AutoProductEvents`). An app's own
  `TrackAuth` calls are its code's decision and are removed in its code.
- **Browser events** (`identifyUser`, `trackAuthEvent`, page views):
  `ANALYTICS_MODE=off` in the env's runtime config (`forge.Analytics {mode =
"off"}`, or `runtime_config = {ANALYTICS_MODE = "off"}`) is served as
  `config.js`, so it applies on the next page load with no rebuild.
- **`forge obs users`** only reads; it writes nothing and needs no rollback. A
  wrong count is a defect in `pkg/obsquery`'s statement, fixed forward in
  forge and picked up by the control plane at its next pin.

Events already recorded stay in `product_events` until its retention expires;
they carry only the opaque subject, so nothing needs deleting for privacy.
