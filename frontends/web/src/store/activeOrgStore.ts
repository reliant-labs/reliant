// Copyright (c) 2025 Reliant Labs

import { create } from "zustand";

/**
 * The org this web session acts in: the one `org_id` every org-scoped
 * control-plane call carries (api/orgInterceptor.ts).
 *
 * ── Where the choice lives ─────────────────────────────────────────────
 *
 * Per USER, in this browser's localStorage (`reliant.activeOrg.<user id>`), so
 * two people sharing a browser never inherit each other's org. It is a
 * view-level choice: it is NOT the server's default org, which is a separate
 * per-person preference (SetDefaultOrganization) that follows them to the CLI
 * and other devices. Switching here changes only what this app shows and acts
 * on.
 *
 * `null` means "name none": requests omit org_id and the server uses the
 * person's default org. That is the right answer while the org list loads,
 * for a machine credential (no orgs to choose from), and for a signup still
 * provisioning. It is never a guess at "the first org".
 *
 * This module imports nothing from the auth store on purpose: the transport
 * reads it, and the auth store sits on the transport's import cycle. The
 * binding component hands it the user id (components/Orgs/ActiveOrgBinding).
 */

const KEY_PREFIX = "reliant.activeOrg.";

interface ActiveOrgState {
  /** The signed-in user the selection belongs to; null when signed out. */
  userId: string | null;
  /** What the person chose, persisted; null when they never did. */
  chosenId: string | null;
  /**
   * The org requests name: the choice while it is still a membership, else the
   * server's default (ActiveOrgBinding reconciles). Never persisted, so a
   * default changed elsewhere (the CLI, another device) reaches a browser
   * that never chose.
   */
  effectiveId: string | null;
}

export const useActiveOrgStore = create<ActiveOrgState>(() => ({ userId: null, chosenId: null, effectiveId: null }));

function readPersisted(userId: string): string | null {
  try {
    return localStorage.getItem(KEY_PREFIX + userId) || null;
  } catch {
    return null;
  }
}

function writePersisted(userId: string, orgId: string | null): void {
  try {
    if (orgId) localStorage.setItem(KEY_PREFIX + userId, orgId);
    else localStorage.removeItem(KEY_PREFIX + userId);
  } catch {
    // A browser that refuses storage keeps the choice for this page load only.
  }
}

/**
 * Point the store at `userId` and load their persisted choice. Idempotent, and
 * cheap enough to call on every render of the binding: the transport must
 * already see the right org when the first query of a page load fires. Until
 * the membership list answers, the saved choice is what requests name.
 */
export function bindActiveOrgUser(userId: string | null): void {
  if (useActiveOrgStore.getState().userId === userId) return;
  const chosen = userId ? readPersisted(userId) : null;
  useActiveOrgStore.setState({ userId, chosenId: chosen, effectiveId: chosen });
}

/** The org requests act in right now, or null to name none. */
export function getActiveOrgId(): string | null {
  return useActiveOrgStore.getState().effectiveId;
}

/** The person picks an org: remembered for them, and effective at once. */
export function chooseActiveOrg(orgId: string): void {
  const { userId } = useActiveOrgStore.getState();
  if (!userId) return;
  writePersisted(userId, orgId);
  useActiveOrgStore.setState({ chosenId: orgId, effectiveId: orgId });
}

/** The membership list resolved `orgId` as the org to act in (a choice or the default). */
export function setEffectiveOrg(orgId: string | null): void {
  if (useActiveOrgStore.getState().effectiveId !== orgId) useActiveOrgStore.setState({ effectiveId: orgId });
}

/** The remembered choice no longer names an org the person is in: forget it. */
export function forgetChosenOrg(): void {
  const { userId, chosenId } = useActiveOrgStore.getState();
  if (!userId || chosenId === null) return;
  writePersisted(userId, null);
  useActiveOrgStore.setState({ chosenId: null });
}

/** Another tab chose: adopt its choice. */
export function adoptPersistedActiveOrg(): void {
  const { userId, chosenId } = useActiveOrgStore.getState();
  if (!userId) return;
  const persisted = readPersisted(userId);
  if (persisted !== chosenId) useActiveOrgStore.setState({ chosenId: persisted, effectiveId: persisted });
}

export const ACTIVE_ORG_STORAGE_PREFIX = KEY_PREFIX;

/**
 * Query-key roots whose answers depend on the active org. A switch resets
 * these (not the whole cache: chats, projects and files are the user's, not
 * an org's). A new control-plane query family joins this list.
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
];
