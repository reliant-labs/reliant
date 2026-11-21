// Copyright (c) 2026 Reliant Labs

/**
 * After hosting checkout: once the SERVER reports the agreement active, say
 * that the deploys waiting on billing go out on their own.
 *
 * The return from Stripe is never proof of funding (the panel polls the
 * agreement), so this is keyed to two facts together: this browser started a
 * hosting checkout for this org (a session marker set just before the
 * hand-off, which survives Stripe's full-page round trip), and the agreement
 * now reads `active`. It names the queued environments this session saw, from
 * the same watch that sends the "is live" notification, so the promise here
 * and the notification later are about the same deploys.
 */

import { CheckCircle2 } from "lucide-react";

import Button from "@/components/forge-ui/button";
import { useQueuedDeployStore } from "@/store/queuedDeployStore";

const MARKER_PREFIX = "reliant.hostingCheckout.";

/** Record that a hosting checkout for `orgId` was started in this browser session. */
export function markHostingCheckoutStarted(orgId: string): void {
  try {
    sessionStorage.setItem(MARKER_PREFIX + orgId, String(Date.now()));
  } catch {
    // Storage refused (private mode): the notice simply does not show.
  }
}

export function hostingCheckoutStarted(orgId: string): boolean {
  try {
    return sessionStorage.getItem(MARKER_PREFIX + orgId) !== null;
  } catch {
    return false;
  }
}

export function clearHostingCheckout(orgId: string): void {
  try {
    sessionStorage.removeItem(MARKER_PREFIX + orgId);
  } catch {
    // Nothing to clear.
  }
}

export function HostingActivatedNotice({ orgId, onDismiss }: { orgId: string; onDismiss: () => void }) {
  const watched = useQueuedDeployStore((state) => state.watched);
  const queued = Object.values(watched).filter((deploy) => deploy.orgId === orgId && deploy.queued);

  return (
    <div
      role="status"
      className="flex flex-col gap-3 rounded-xl border border-border bg-card px-4 py-3 sm:flex-row sm:items-start sm:justify-between"
      data-testid="hosting-activated-notice"
    >
      <div className="flex items-start gap-2">
        <CheckCircle2 className="mt-0.5 h-4 w-4 shrink-0 text-success-ink" aria-hidden="true" />
        <div className="space-y-1">
          <p className="text-ui font-medium text-foreground">App hosting billing is active</p>
          {queued.length > 0 ? (
            <>
              <p className="text-ui text-muted-foreground">These deploys were waiting on billing and now go out on their own. Nothing needs to be re-run:</p>
              <ul className="list-inside list-disc text-ui text-foreground" data-testid="hosting-activated-queued">
                {queued.map((deploy) => (
                  <li key={`${deploy.forgeProject}/${deploy.envId}`}>
                    {deploy.forgeProject} · {deploy.envName}
                  </li>
                ))}
              </ul>
              <p className="text-meta text-muted-foreground">You&apos;ll get a notification when each one is live.</p>
            </>
          ) : (
            <p className="text-ui text-muted-foreground">
              Any deploy that was queued on billing for this organization now goes out on its own. Nothing needs to be re-run.
            </p>
          )}
        </div>
      </div>
      <Button size="sm" variant="ghost" onClick={onDismiss}>
        Dismiss
      </Button>
    </div>
  );
}
