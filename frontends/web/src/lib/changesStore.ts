/**
 * Forge Changes in the web app: the environment's snapshot, loaded once per
 * page load. What each change can be is declared in `changes.yaml` at the repo
 * root, and `forge generate` writes one typed accessor per change into
 * lib/changes_gen.ts (forge docs/changes.md). Who gets which variant is the
 * control plane's, set per environment in the console (Analytics ›
 * Experiments); nothing in this repo launches or ramps a change.
 *
 * startChanges() runs once at boot (main.tsx), so the snapshot is in hand long
 * before a surface that asks for a change renders. changesStore() is what such
 * a surface reads: null until the load settles, then fixed for the page's
 * life, so a treatment never switches under the person. A surface asks once,
 * when it is first shown, and keeps the answer: one that shows before the
 * load settled takes its default and enrolls no one.
 *
 * The snapshot is `/_forge/changes.json` on the page's own origin, which the
 * platform serves for a hosted static site (app.reliantlabs.io and the custom
 * domains bound to it). Missing (a dev server, a self-hosted build), slow or
 * malformed, it is an empty store: every change is its default. The desktop
 * renderer (app://bundle) has no such origin, so it never fetches and every
 * change is its default there.
 */

import { loadChanges, type ChangeEvalContext, type ChangesStore } from "@reliantlabs/forge-web-runtime";

import type { AuthIdentity } from "@/lib/auth/identity";
import { isElectron } from "./constants";

/** How long the snapshot may take before the page settles on every default. */
export const CHANGES_LOAD_TIMEOUT_MS = 3000;

let store: ChangesStore | null = null;
let loading: Promise<ChangesStore | null> | null = null;

const timedFetch: typeof fetch = (input, init) => fetch(input, { ...init, signal: AbortSignal.timeout(CHANGES_LOAD_TIMEOUT_MS) });

/** Load the snapshot, once per page. Never rejects (loadChanges's contract). */
export function startChanges(): Promise<ChangesStore | null> {
  if (loading) return loading;
  loading = isElectron()
    ? Promise.resolve(null)
    : loadChanges({ fetch: timedFetch }).then((loaded) => {
        store = loaded;
        return loaded;
      });
  return loading;
}

/** The loaded store, or null while it loads (and always on the desktop app). */
export function changesStore(): ChangesStore | null {
  return store;
}

/**
 * The evaluation context for a change whose unit is `user`: the signed-in
 * person's id, the same id telemetry carries as the user (lib/telemetry.ts),
 * which is what the readout counts a unit by. A guest or an API-key session is
 * not a person we measure: no unit, so it gets the default and is never
 * enrolled.
 */
export function userChangeContext(identity: AuthIdentity | null): ChangeEvalContext {
  if (!identity || identity.isAnonymous || identity.isApiKey) return {};
  return { targetingKey: identity.id };
}

/** Tests: set the loaded store (null: not loaded), and forget any load. */
export function setChangesStoreForTests(next: ChangesStore | null): void {
  store = next;
  loading = null;
}
