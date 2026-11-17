// Copyright (c) 2025 Reliant Labs

import { create } from "zustand";

/**
 * The org this tab acts in: the one `org_id` every org-scoped request carries
 * (api/orgInterceptor.ts) and the org the product's lists name in their
 * requests and cache keys (lib/orgScope.ts).
 *
 * ── Where the choice lives ─────────────────────────────────────────────
 *
 * Per TAB and per USER, in sessionStorage (`reliant.activeOrg.<user id>`), so
 * switching org in one tab never moves another, and two people sharing a
 * browser never inherit each other's org. A reload keeps the tab's org.
 *
 * The last choice made in ANY tab is also kept in localStorage under the same
 * key, only to seed a tab that has none of its own: a new tab starts in the
 * org you last picked. Nothing listens for it changing afterwards; a tab that
 * already has an org keeps it.
 *
 * It is a view-level choice: it is NOT the server's default org, which is a
 * separate per-person preference (SetDefaultOrganization) that follows them
 * to the CLI and other devices. Switching here changes only what this tab
 * shows and acts on.
 *
 * `null` means "name none": requests omit org_id and the server uses the
 * person's default org. That is the right answer for a machine credential (no
 * orgs to choose from) and for a signup still provisioning. It is never a
 * guess at "the first org". While the membership list loads with nothing
 * saved, `effectiveId` is null too, but `resolved` is false: the product's
 * org-scoped lists wait for it rather than ask with no org (which the server
 * answers with every org's rows).
 *
 * This module imports nothing from the auth store on purpose: the transport
 * reads it, and the auth store sits on the transport's import cycle. The auth
 * store hands it the user id on every sign-in and sign-out, and so does the
 * binding component (components/Orgs/ActiveOrgBinding).
 */

const KEY_PREFIX = "reliant.activeOrg.";

interface ActiveOrgState {
  /** The signed-in user the selection belongs to; null when signed out. */
  userId: string | null;
  /** What the person chose in this tab, persisted; null when they never did. */
  chosenId: string | null;
  /**
   * The org requests name: the choice while it is still a membership, else the
   * server's default (ActiveOrgBinding reconciles). Never persisted, so a
   * default changed elsewhere (the CLI, another device) reaches a tab that
   * never chose.
   */
  effectiveId: string | null;
  /**
   * The membership list has answered for `userId`, so `effectiveId` is the
   * org to act in, even when it is null (the person has none).
   */
  resolved: boolean;
}

export const useActiveOrgStore = create<ActiveOrgState>(() => ({
  userId: null,
  chosenId: null,
  effectiveId: null,
  resolved: false,
}));

function read(storage: () => Storage, key: string): string | null {
  try {
    return storage().getItem(key) || null;
  } catch {
    return null;
  }
}

function write(storage: () => Storage, key: string, value: string | null): void {
  try {
    if (value) storage().setItem(key, value);
    else storage().removeItem(key);
  } catch {
    // A browser that refuses storage keeps the choice for this page load only.
  }
}

const tab = () => sessionStorage;
const lastChoice = () => localStorage;

/**
 * This tab's choice for `userId`. A tab with none of its own takes the last
 * choice made in any tab, once, and keeps it as its own from then on.
 */
function readPersisted(userId: string): string | null {
  const key = KEY_PREFIX + userId;
  const own = read(tab, key);
  if (own) return own;
  const seed = read(lastChoice, key);
  if (seed) write(tab, key, seed);
  return seed;
}

/**
 * Point the store at `userId` and load this tab's choice. Idempotent, and
 * cheap enough to call on every render of the binding: the transport must
 * already see the right org when the first query of a page load fires. Until
 * the membership list answers, the saved choice is what requests name.
 */
export function bindActiveOrgUser(userId: string | null): void {
  if (useActiveOrgStore.getState().userId === userId) return;
  const chosen = userId ? readPersisted(userId) : null;
  useActiveOrgStore.setState({ userId, chosenId: chosen, effectiveId: chosen, resolved: false });
}

/** The org requests act in right now, or null to name none. */
export function getActiveOrgId(): string | null {
  return useActiveOrgStore.getState().effectiveId;
}

/**
 * The person picks an org: this tab's, effective at once, and the org a new
 * tab starts in.
 */
export function chooseActiveOrg(orgId: string): void {
  const { userId } = useActiveOrgStore.getState();
  if (!userId) return;
  write(tab, KEY_PREFIX + userId, orgId);
  write(lastChoice, KEY_PREFIX + userId, orgId);
  useActiveOrgStore.setState({ chosenId: orgId, effectiveId: orgId });
}

/**
 * The membership list resolved `orgId` as the org to act in (a choice or the
 * default; null when the person has none).
 */
export function setEffectiveOrg(orgId: string | null): void {
  const state = useActiveOrgStore.getState();
  if (state.effectiveId !== orgId || !state.resolved) useActiveOrgStore.setState({ effectiveId: orgId, resolved: true });
}

/** The remembered choice no longer names an org the person is in: forget it. */
export function forgetChosenOrg(): void {
  const { userId, chosenId } = useActiveOrgStore.getState();
  if (!userId || chosenId === null) return;
  const key = KEY_PREFIX + userId;
  write(tab, key, null);
  // The seed for new tabs too, unless another tab has chosen since.
  if (read(lastChoice, key) === chosenId) write(lastChoice, key, null);
  useActiveOrgStore.setState({ chosenId: null });
}

/**
 * Query-key roots whose answers depend on the active org. A switch resets
 * these (not the whole cache). A new control-plane query family joins this
 * list. The product's own org-scoped reads (the chat list, projects, saved
 * workflows, triggers, runs) are reset by the product app's onSwitch,
 * components/Orgs/followActiveOrg.ts, which a shell without them omits.
 */
export const ORG_SCOPED_QUERY_ROOTS: readonly string[] = [
  "observability",
  "observability-billing",
  "forge",
  "forgeService",
  "identity",
  "dashboards",
  "reliantAI",
  "cloud-billing",
  "deploy-usage",
  "usage-billing",
  "machine-pricing-readiness",
  "onboarding",
  "cp",
  "controlPlane",
  "orgs-members",
  "org-policy",
];
