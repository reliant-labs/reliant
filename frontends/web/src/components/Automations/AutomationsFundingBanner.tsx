// Copyright (c) 2025 Reliant Labs

/**
 * The release banner of org-provider-accounts §11.5: once automations in a team
 * org run on the org's accounts by default, the person's older ones that still
 * spend their own accounts are named, one banner per org. Its data is the
 * Inbox's automations-on-personal-accounts item, so dismissing the banner
 * dismisses that item, and it comes back only when another automation joins
 * the set. Review opens the oldest of them, where its "AI accounts" switch is.
 */

import { useDismissInboxItems, useInbox } from "@/hooks/inbox-queries";
import { useMyOrgs } from "@/hooks/useOrgs";
import { automationRedirectPath } from "@/lib/workflowsArea";
import AlertBanner from "../forge-ui/alert_banner";
import { orgAccountsLabel } from "./AutomationFunding";

/** The banner's sentence: "New automations in Acme run on Acme's accounts. 2 of yours still use your own accounts." */
export function automationsFundingMessage(orgName: string | undefined, count: number): string {
  const inOrg = orgName ? `in ${orgName}` : "in this organization";
  const yours = count === 1 ? "1 of yours still uses" : `${count} of yours still use`;
  return `New automations ${inOrg} run on ${orgAccountsLabel(orgName)}. ${yours} your own accounts.`;
}

export function AutomationsFundingBanner() {
  const inbox = useInbox();
  const orgs = useMyOrgs();
  const dismiss = useDismissInboxItems();

  const items = (inbox.data?.items ?? []).flatMap((item) =>
    item.payload.case === "automationsOnPersonalAccounts" && item.payload.value.count > 0
      ? [{ itemId: item.itemId, ...item.payload.value }]
      : [],
  );
  if (items.length === 0) return null;

  return (
    <div className="space-y-2" data-testid="automations-funding-banner">
      {items.map((item) => {
        const orgName = orgs.data?.orgs.find((o) => o.id === item.orgId)?.name;
        const first = item.triggerIds[0];
        return (
          <AlertBanner
            key={item.itemId}
            variant="info"
            role="status"
            message={
              <>
                {automationsFundingMessage(orgName, item.count)}{" "}
                {first && (
                  <a href={automationRedirectPath(first)} className="font-medium underline">
                    Review
                  </a>
                )}
              </>
            }
            dismissible
            onDismiss={() => dismiss.mutate({ itemIds: [item.itemId] })}
          />
        );
      })}
    </div>
  );
}
