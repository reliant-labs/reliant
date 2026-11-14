// Copyright (c) 2025 Reliant Labs

/**
 * Automations an admin offered the signed-in member: a former member's
 * automation they may take over. Accepting makes it theirs (paused, on their
 * project and machine); declining ends the offer. Shown on the Members tab
 * only while there is an offer.
 */

import { useState } from "react";
import { useQueryClient } from "@tanstack/react-query";

import Button from "@/components/forge-ui/button";
import type { TriggerOffer } from "@/gen/reliant/v1/automation_takeover_pb";
import {
  automationTakeoverServiceKeys,
  useListTriggerOffers,
  useRevokeTriggerOffer,
} from "@/hooks/automation-takeover-service-hooks_gen";
import { formatRelativeTime } from "@/lib/relativeTime";
import { toast } from "@/lib/toast-manager";
import type { OrgMember } from "@/services/controlPlane/members";
import type { MyOrg } from "@/services/controlPlane/orgs";

import { AdoptAutomationDialog, type AdoptTarget } from "./AdoptAutomationDialog";
import { memberLabel } from "./MemberRoster";
import { sourceLabel, takeoverErrorMessage } from "./takeover";

export function TriggerOffers({ org, members }: { org: MyOrg; members: OrgMember[] }) {
  const qc = useQueryClient();
  const offers = useListTriggerOffers({ orgId: org.id }, { meta: { silenceErrorToast: true } });
  const decline = useRevokeTriggerOffer({ meta: { silenceErrorToast: true } });
  const [accepting, setAccepting] = useState<AdoptTarget | null>(null);
  const [error, setError] = useState<string | null>(null);
  const refresh = () => qc.invalidateQueries({ queryKey: automationTakeoverServiceKeys.all });

  const nameOf = (userId: string) => {
    const m = members.find((x) => x.userId === userId);
    return m ? memberLabel(m) : "an admin";
  };

  const onDecline = async (offer: TriggerOffer) => {
    setError(null);
    try {
      await decline.mutateAsync({ offerId: offer.id });
      toast.success(`Declined “${offer.trigger?.name ?? "the automation"}”.`);
      await refresh();
    } catch (err) {
      setError(takeoverErrorMessage(err, "Could not decline the offer."));
    }
  };

  const list = offers.data?.offers ?? [];
  if (list.length === 0 && !error) return null;

  return (
    <section className="rounded-lg border border-border bg-card" aria-labelledby="offers-heading" data-testid="trigger-offers">
      <div className="border-b border-border px-4 py-3">
        <h3 id="offers-heading" className="text-sm font-semibold text-foreground">
          Automations offered to you
        </h3>
        <p className="mt-0.5 text-xs text-muted-foreground">
          A former member made these. Accept one and it runs as you, on your project and machine.
        </p>
      </div>
      {error && (
        <p role="alert" className="border-b border-border px-4 py-3 text-sm text-destructive-ink" data-testid="offers-error">
          {error}
        </p>
      )}
      <ul className="divide-y divide-border">
        {list.map((offer) => (
          <li key={offer.id} className="flex flex-wrap items-center gap-3 px-4 py-3" data-testid={`offer-${offer.id}`}>
            <div className="min-w-0 flex-1">
              <p className="truncate text-sm font-medium text-foreground">{offer.trigger?.name}</p>
              <p className="truncate text-xs text-muted-foreground">
                {sourceLabel(offer.trigger)} · offered by {nameOf(offer.offeredByUserId)}
                {offer.expiresAt && ` · expires ${formatRelativeTime(offer.expiresAt)}`}
              </p>
            </div>
            <div className="flex items-center gap-2">
              <Button variant="ghost" size="sm" disabled={decline.isPending} onClick={() => void onDecline(offer)} aria-label={`Decline ${offer.trigger?.name}`}>
                Decline
              </Button>
              <Button
                variant="outline"
                size="sm"
                onClick={() => offer.trigger && setAccepting({ kind: "accept", offerId: offer.id, trigger: offer.trigger, paramNames: offer.paramNames })}
                aria-label={`Accept ${offer.trigger?.name}`}
                data-testid={`offer-accept-${offer.id}`}
              >
                Accept
              </Button>
            </div>
          </li>
        ))}
      </ul>
      <AdoptAutomationDialog orgId={org.id} target={accepting} onClose={() => setAccepting(null)} onDone={() => void refresh()} />
    </section>
  );
}
