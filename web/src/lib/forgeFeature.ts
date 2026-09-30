/**
 * The one gate for the whole forge UI (topology, secrets, env status + audit,
 * promote, deploy).
 *
 * WHY A SINGLE MODULE. The feature spans five screens, three routes, a sidebar
 * entry, and two write paths — one of which deploys to a live cluster. Gating
 * those independently would mean five places that can disagree, and the failure
 * mode is the worst kind: a route still reachable by URL after the nav entry is
 * hidden, so the thing you thought was off is one paste away from a prod deploy.
 * So every surface asks THIS function, and turning the feature off turns all of
 * it off.
 *
 * DEFAULT IS ON, EVERYWHERE. The feature shipped: every user gets the forge UI
 * unless they have explicitly turned it off. This was the author's call to make
 * and it has now been made, so the gate no longer distinguishes a packaged
 * build from a dev one.
 *
 * The gate itself stays, and that is deliberate — it is still the single point
 * that turns all five screens and three routes off together, which is what the
 * explicit off-switch in Settings → Developer needs in order to mean anything.
 *
 * THE TWO WAYS IT RESOLVES, in precedence order:
 *
 *  1. An explicit preference stored in localStorage under FORGE_UI_FLAG_KEY.
 *     This is the switch the Developer settings toggle writes. It wins over
 *     everything, in BOTH directions — opting out is how a user who hits a
 *     problem gets the old behaviour back without waiting for a release.
 *  2. Otherwise ON.
 *
 * WHY localStorage RATHER THAN THE SETTINGS SERVICE. This gate is read during
 * render by the sidebar and by route guards, so it must be synchronous and
 * available before any RPC resolves. A server round-trip would make the nav
 * entry flicker in and out on every load, and — worse — would leave the gate
 * OPEN during the window before the answer arrives. A local, synchronous read
 * cannot fail open. The trade-off is that the opt-in is per-browser rather than
 * per-account, which is the right shape for "let me try the unfinished thing".
 *
 * There is deliberately NO env-var / build-time flag. A build-time gate cannot be
 * turned off by the person holding the packaged app, which is exactly who needs
 * to turn it off if something is wrong.
 */
/** localStorage key holding the explicit opt-in. Exported so tests and the
 *  settings toggle cannot drift from the reader. */
export const FORGE_UI_FLAG_KEY = "reliant.experimental.forgeUI";

/**
 * readStoredFlag returns the explicit opt-in, or undefined when the user has
 * expressed no preference.
 *
 * Returns undefined rather than false on a storage failure (Safari private mode
 * throws on access, and a non-browser test environment has no localStorage), so
 * an unreadable store falls through to the default instead of silently pinning
 * the feature off for a user who never asked for that.
 */
function readStoredFlag(): boolean | undefined {
  try {
    if (typeof window === "undefined" || !window.localStorage) return undefined;
    const raw = window.localStorage.getItem(FORGE_UI_FLAG_KEY);
    if (raw === "true") return true;
    if (raw === "false") return false;
    return undefined;
  } catch {
    return undefined;
  }
}

/**
 * isForgeUIEnabled reports whether the forge UI should be reachable AT ALL.
 *
 * Read it at render time, never cached at module scope: the settings toggle
 * changes the answer within a session, and a module-level snapshot would need a
 * reload to take effect.
 */
export function isForgeUIEnabled(): boolean {
  const stored = readStoredFlag();
  if (stored !== undefined) return stored;
  return true;
}

/**
 * setForgeUIEnabled persists the explicit opt-in. Used by the Developer settings
 * toggle.
 *
 * Writing the CURRENT effective value is still meaningful: it converts an
 * implicit default into an explicit choice, which is what stops a later change
 * of default from silently flipping the feature underneath the user.
 */
export function setForgeUIEnabled(enabled: boolean): void {
  try {
    if (typeof window === "undefined" || !window.localStorage) return;
    window.localStorage.setItem(FORGE_UI_FLAG_KEY, enabled ? "true" : "false");
  } catch {
    // A storage write that fails leaves the effective value at its default.
    // Silent because there is no user-actionable recovery, and throwing here
    // would break a settings page over a preference.
  }
}

/**
 * clearForgeUIPreference removes the explicit preference, returning the gate to
 * its default (on). Exported for tests and for a "reset to default" affordance.
 */
export function clearForgeUIPreference(): void {
  try {
    if (typeof window === "undefined" || !window.localStorage) return;
    window.localStorage.removeItem(FORGE_UI_FLAG_KEY);
  } catch {
    // See setForgeUIEnabled.
  }
}
