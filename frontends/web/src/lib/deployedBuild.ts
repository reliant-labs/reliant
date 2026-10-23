/**
 * Has a newer web build been deployed since this tab loaded?
 *
 * A tab keeps running the bundle it loaded until the user reloads, and a web
 * tab can stay open for days. The only signal it used to get was an RPC the
 * server no longer serves (api/versionSkew.ts → the out-of-date modal), which
 * fires only when the skew is already breaking something. A prod bug traced to
 * a tab on a pre-deploy bundle that silently hid async questions: nothing it
 * called was removed, so nothing ever told the user to reload.
 *
 * So the tab asks the deployment directly. Every production build emits
 * /version.json beside index.html (vite-plugin-version-json.ts), the same
 * BuildInfo this bundle baked in as __RELIANT_BUILD__. Comparing the two says
 * whether a reload would change anything. It is checked when the tab regains
 * focus or becomes visible (the moment a user returns to a stale tab) and on a
 * slow interval while it stays open.
 *
 * A difference is only a suggestion: NewBuildPrompt shows a dismissible
 * "Reload" toast, and never reloads by itself — the user may be mid-sentence.
 * The blocking modal stays reserved for a real incompatibility (versionSkew).
 *
 * Not in Electron: the desktop app serves its own bundle over app://, so its
 * /version.json is always itself, and it updates through electron-updater.
 */

import type { BuildInfo } from "./releaseIdentity";
import { UNKNOWN } from "./releaseIdentity";

/** Where the deployment says what it is; a real file the SPA rewrite cannot shadow. */
export const DEPLOYED_BUILD_URL = "/version.json";

/** The slow poll while the tab stays visible. Focus/visibility cover the rest. */
export const POLL_INTERVAL_MS = 5 * 60_000;

/** A burst of focus + visibilitychange (one tab switch fires both) is one check. */
export const MIN_CHECK_GAP_MS = 30_000;

/**
 * The identity two builds are compared on: the commit when stamped (a rebuild
 * of one commit is the same code), else the version. Null when the build cannot
 * say — and an unnamed build is never reported as different.
 */
export function buildIdentity(info: Partial<BuildInfo> | null | undefined): string | null {
  for (const v of [info?.commit, info?.version]) {
    if (typeof v === "string" && v.trim() !== "" && v.trim() !== UNKNOWN) return v.trim();
  }
  return null;
}

/**
 * The deployed build, or null when it cannot be read: offline, a non-2xx, a
 * dev server answering with index.html, malformed JSON. Every failure means
 * "no news", never "new build".
 */
export async function fetchDeployedBuild(
  fetchImpl: typeof fetch = fetch,
  url: string = DEPLOYED_BUILD_URL,
): Promise<Partial<BuildInfo> | null> {
  try {
    // no-store plus a unique query: the file must come from the origin, not
    // from the browser's or a CDN's copy of the build this tab already runs.
    const res = await fetchImpl(`${url}?t=${Date.now()}`, { cache: "no-store" });
    if (!res.ok) return null;
    const body: unknown = await res.json();
    return body && typeof body === "object" ? (body as Partial<BuildInfo>) : null;
  } catch {
    return null;
  }
}

export interface NewBuildWatchOptions {
  /** This bundle's identity (appBuild()). */
  running: BuildInfo | null;
  /** Called once per newly deployed build: the same build is never re-reported. */
  onNewBuild: (deployed: Partial<BuildInfo>) => void;
  fetchImpl?: typeof fetch;
  intervalMs?: number;
  minGapMs?: number;
  now?: () => number;
}

/**
 * Starts watching for a new deployment; returns the function that stops it.
 * Does nothing (and never fetches) when this bundle cannot name itself.
 */
export function watchForNewBuild(opts: NewBuildWatchOptions): () => void {
  const running = buildIdentity(opts.running);
  if (running === null) return () => {};
  const {
    onNewBuild,
    fetchImpl,
    intervalMs = POLL_INTERVAL_MS,
    minGapMs = MIN_CHECK_GAP_MS,
    now = Date.now,
  } = opts;

  let stopped = false;
  let inFlight = false;
  let lastCheck = Number.NEGATIVE_INFINITY;
  let reported: string | null = null;

  const check = async () => {
    if (stopped || inFlight || document.visibilityState === "hidden") return;
    if (now() - lastCheck < minGapMs) return;
    lastCheck = now();
    inFlight = true;
    try {
      const deployed = await fetchDeployedBuild(fetchImpl);
      const id = buildIdentity(deployed);
      if (stopped || deployed === null || id === null || id === running || id === reported) return;
      reported = id;
      onNewBuild(deployed);
    } finally {
      inFlight = false;
    }
  };

  const onWake = () => void check();
  window.addEventListener("focus", onWake);
  document.addEventListener("visibilitychange", onWake);
  const timer = setInterval(onWake, intervalMs);
  void check();

  return () => {
    stopped = true;
    window.removeEventListener("focus", onWake);
    document.removeEventListener("visibilitychange", onWake);
    clearInterval(timer);
  };
}
