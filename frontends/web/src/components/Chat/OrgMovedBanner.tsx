// Copyright (c) 2025 Reliant Labs

/**
 * OrgMovedBanner — shown to a chat's owner when the chat belongs to another
 * organization than the one this app is acting in. That is what an old link or
 * a sidebar entry shows once the chat's project moved to another organization
 * (control-plane docs/design/org-project-move.md §4.3): the owner can still read
 * the chat, but it is no longer in this org's lists, and its next turn bills the
 * other org. One button switches the app to that org.
 *
 * It reads only the chat's `orgId` against the active org, so it says nothing
 * about how the chat got there. A viewer of a shared chat never sees it: a
 * shared chat is always in the org that shares it.
 */
import { ArrowRightLeft } from "lucide-react";

import { useActiveOrg } from "@/hooks/useOrgs";
import type { MyOrg } from "@/services/controlPlane/orgs";
import { useActiveOrgStore } from "@/store/activeOrgStore";
import { statusBannerFrame, useStatusSlotCollapsed, useStatusSlotSummary } from "./ComposerStatusSlot";

/**
 * The org to offer a switch to, or null when there is nothing to say: the chat
 * is in the active org, has no org, is not the caller's, the org list has not
 * answered, or the caller is not a member of the chat's org (the server would
 * not have served it; there is nothing to switch to).
 */
export function movedToOrg(args: {
  chatOrgId?: string;
  chatUserId?: string;
  meUserId: string | null;
  activeOrgId: string | null;
  orgs: MyOrg[];
}): MyOrg | null {
  const { chatOrgId, chatUserId, meUserId, activeOrgId, orgs } = args;
  if (!chatOrgId || !activeOrgId || chatOrgId === activeOrgId) return null;
  if (!meUserId || chatUserId !== meUserId) return null;
  return orgs.find((o) => o.id === chatOrgId) ?? null;
}

export function OrgMovedBanner({ chatOrgId, chatUserId }: { chatOrgId?: string; chatUserId?: string }) {
  const { orgs, active, activeOrgId, select } = useActiveOrg();
  const me = useActiveOrgStore((s) => s.userId);
  const target = movedToOrg({ chatOrgId, chatUserId, meUserId: me, activeOrgId, orgs });
  useStatusSlotSummary(target ? { label: `In ${target.name}`, tone: "info" } : null);
  const inSheet = useStatusSlotCollapsed();
  if (!target) return null;

  return (
    <div
      role="status"
      data-testid="org-moved-banner"
      className={statusBannerFrame(inSheet, "flex-shrink-0 border-t border-info/30 px-4 py-3", "bg-info/10 text-sm")}
    >
      <div className="flex items-center justify-between gap-4">
        <div className="flex min-w-0 items-start gap-2">
          <ArrowRightLeft className="mt-0.5 h-4 w-4 flex-shrink-0 text-info" aria-hidden="true" />
          <div className="min-w-0">
            <p className="font-medium">This project is now in {target.name}</p>
            <p className="text-xs text-foreground/80">
              {active ? `You are working in ${active.name}. ` : ""}
              Switch to {target.name} to see it with the rest of that organization's work.
            </p>
          </div>
        </div>
        <button
          type="button"
          onClick={() => select(target.id)}
          className="inline-flex flex-shrink-0 items-center gap-1 rounded-md border border-primary/40 px-3 py-1 text-xs font-medium text-primary-ink transition-colors hover:bg-primary/10 pointer-coarse:min-h-11"
        >
          Switch to {target.name}
        </button>
      </div>
    </div>
  );
}
