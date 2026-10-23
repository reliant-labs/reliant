/**
 * Publishes the Inbox badge (`useInboxBadge`, the desktop sidebar's rule) to
 * `mobileDrawerStore`, where every screen's menu button and the drawer's Inbox
 * row read it. Mounted once, by `MobileShell`; renders nothing.
 */

import { useEffect } from "react";
import { useInboxBadge } from "../inbox/InboxNavItem";
import { useMobileDrawerStore } from "../../store/mobileDrawerStore";

export function MobileInboxBadgeSync() {
  const { count, informational, degraded, announcement } = useInboxBadge();
  const setInboxBadge = useMobileDrawerStore((s) => s.setInboxBadge);
  useEffect(() => {
    setInboxBadge({ count, informational, degraded, announcement });
  }, [count, informational, degraded, announcement, setInboxBadge]);
  return null;
}
