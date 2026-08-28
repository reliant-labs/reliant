/**
 * Installs browser→file log forwarding as the FIRST thing that runs.
 *
 * ── Why this is a separate module ─────────────────────────────────────
 *
 * ES imports are hoisted: every `import` in a module is evaluated before any
 * statement in that module's body. So this in `main.tsx`
 *
 *     import { installBrowserLogForwarding } from "./lib/browser-log-forward";
 *     installBrowserLogForwarding();          // ← looks first, runs LAST
 *     import "./lib/debug.ts";
 *     import { Root } from "./components/Root";
 *
 * does NOT install the forwarder first. `debug.ts`, `Root.tsx` and their whole
 * transitive graph evaluate before that call, and every log line they emit —
 * i.e. all of app startup — is written to a console that is not yet wrapped.
 *
 * A bare `import "./lib/browser-log-boot"` placed above the others works
 * because imports are evaluated in ORDER: this module's side effect runs
 * during its own evaluation, before the later imports are evaluated at all.
 */
import { installBrowserLogForwarding } from "./browser-log-forward";

if (import.meta.env.DEV) {
  installBrowserLogForwarding();
}
