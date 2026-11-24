// Copyright (c) 2025 Reliant Labs

import { useEffect } from "react";

import { clearOrgArrivalNotice, useOrgArrivalNotice } from "@/lib/orgArrivalNotice";
import { toast } from "@/lib/toast-manager";

/** One toast id for every arrival notice: a newer one replaces the last in place, never stacks on it. */
const ORG_ARRIVAL_TOAST_ID = "org-arrival";

/**
 * Shows the notice for a URL that moved the tab's org (lib/orgArrivalNotice.ts)
 * as a toast. Mounted once, at the root, after the Toaster: its effect runs
 * after the Toaster's has subscribed, so a notice queued by a page load's
 * first commit is not lost.
 */
export function OrgArrivalNotice() {
  const notice = useOrgArrivalNotice((state) => state.notice);
  useEffect(() => {
    if (!notice) return;
    toast.info(notice.message, { id: ORG_ARRIVAL_TOAST_ID });
    clearOrgArrivalNotice(notice.id);
  }, [notice]);
  return null;
}
