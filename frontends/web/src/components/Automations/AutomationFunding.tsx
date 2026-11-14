// Copyright (c) 2025 Reliant Labs

/**
 * Which AI accounts an automation's runs may spend (control-plane
 * docs/design/org-provider-accounts.md §11.3, §11.5): "Runs on Acme's
 * accounts" when it may use only its organization's, "Runs on your accounts"
 * when its owner's own may pay too, with the switch between them.
 *
 * The owner can always tighten an automation to the organization's accounts.
 * Loosening it is the server's call: allowed in an org with no other members,
 * or where the org's automations policy admits members' own accounts, and
 * otherwise refused with reason org_only_automation, whose message is shown.
 */

import { toast } from "sonner";

import { definitionFromTrigger, triggerErrorMessage, type Trigger } from "@/hooks/trigger-model";
import { useUpdateTrigger } from "@/hooks/trigger-queries";
import { useMyOrgs } from "@/hooks/useOrgs";
import ToggleSwitch from "../forge-ui/toggle_switch";

/** "Acme's accounts", or "the organization's accounts" before the org list answers. */
export function orgAccountsLabel(orgName: string | undefined): string {
  return orgName ? `${orgName}'s accounts` : "the organization's accounts";
}

/** Runs on …, for an automation of an org; null for one of no org (it has no stamp). */
export function AutomationFunding({ trigger, label }: { trigger: Trigger; label: string }) {
  const orgs = useMyOrgs();
  const update = useUpdateTrigger();
  if (!trigger.orgId) return null;

  const orgName = orgs.data?.orgs.find((o) => o.id === trigger.orgId)?.name;
  const orgOnly = trigger.unattendedFunding === "orgOnly";

  const onChange = async (next: boolean) => {
    try {
      await update.mutateAsync({
        id: trigger.id,
        input: { ...definitionFromTrigger(trigger), unattendedFunding: next ? "orgOnly" : "any" },
      });
    } catch (error) {
      toast.error(`Couldn't change which accounts ${label} runs on`, { description: triggerErrorMessage(error) });
    }
  };

  return (
    <span className="flex flex-wrap items-center gap-x-3 gap-y-1" data-testid="automation-funding">
      <span>
        Runs on {orgOnly ? orgAccountsLabel(orgName) : "your accounts"}
        <span className="block text-xs text-muted-foreground">
          {orgOnly
            ? "Its runs never spend your own AI accounts."
            : `Its runs may spend your own AI accounts as well as ${orgAccountsLabel(orgName)}.`}
        </span>
      </span>
      <span className="inline-flex items-center gap-2 text-xs text-muted-foreground">
        <ToggleSwitch
          checked={orgOnly}
          onChange={(next) => void onChange(next)}
          disabled={update.isPending}
          ariaLabel={`${label}: ${orgAccountsLabel(orgName)} only`}
          size="sm"
          className="forge-ui"
        />
        {orgAccountsLabel(orgName)} only
      </span>
    </span>
  );
}
