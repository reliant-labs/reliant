/**
 * Open/close state for the mobile navigation drawer, and the Inbox badge its
 * trigger and its Inbox row show.
 *
 * A store rather than local `MobileShell` state because the trigger button
 * has to live inside each screen's own header (for visual consistency with
 * that screen's existing chrome) while the drawer itself is mounted once at
 * the shell level (so it portals above every screen regardless of which one
 * is routed). Passing a callback down through props would mean plumbing it
 * through every screen; a store lets any header opt in with one line.
 *
 * The badge is here for the same reason: every screen's menu button shows it,
 * and `MobileShell` reads the Inbox counts once (`MobileInboxBadgeSync`)
 * rather than each header running its own query.
 */

import { create } from "zustand";
import type { InboxBadgeState } from "../components/inbox/InboxNavItem";

export const EMPTY_INBOX_BADGE: InboxBadgeState = {
  count: 0,
  informational: false,
  degraded: false,
  announcement: "",
};

interface MobileDrawerStore {
  isOpen: boolean;
  open: () => void;
  close: () => void;
  inboxBadge: InboxBadgeState;
  setInboxBadge: (badge: InboxBadgeState) => void;
}

export const useMobileDrawerStore = create<MobileDrawerStore>((set) => ({
  isOpen: false,
  open: () => set({ isOpen: true }),
  close: () => set({ isOpen: false }),
  inboxBadge: EMPTY_INBOX_BADGE,
  setInboxBadge: (inboxBadge) => set({ inboxBadge }),
}));
