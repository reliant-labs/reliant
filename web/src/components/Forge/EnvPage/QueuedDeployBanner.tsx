// Copyright (c) 2025 Reliant Labs

/**
 * A QUEUED DEPLOY, said once at the top of the environment's page, on every
 * tab:
 *
 *   ⌛ Waiting on billing                                    [Set up billing]
 *      Release v2.0.0 is recorded. This runs compute (1 workload) and the
 *      organization has no active compute plan.
 *      Subscribe to a Reliant Compute plan … nothing needs to be re-run.
 *      Queued 5 minutes ago
 *
 * The control plane accepted the deploy instead of refusing it, so there is
 * nothing to retry and nothing failed — which is why this is the waiting
 * register (amber, an hourglass), never the error one. The release goes out
 * by itself once the hold clears.
 *
 * ── THE SENTENCES ARE THE CONTROL PLANE'S ───────────────────────────────────
 *
 * `reason` and `fix` render verbatim: the control plane decided what holds and
 * why, and a paraphrase here would drift from what `forge env deploy` printed
 * to the person who ran it. The page adds only what a page can: the action.
 *
 * ── WHO CAN ACT ─────────────────────────────────────────────────────────────
 *
 * Billing is an org admin's to set up. When the control plane says the viewer
 * can (`callerCanResolve`), the banner takes them to billing and back. When it
 * says they cannot, a button into checkout would strand them at a purchase
 * they are not allowed to make — so it says who can, and hands them this
 * page's link to send: the control plane's `actionUrl`, which opens this page
 * for whoever receives it.
 */

import { useCallback, useState } from "react";
import { Check, Copy, Hourglass } from "lucide-react";

import { Button } from "@/components/ui/Button";
import { formatRelativeTime } from "@/lib/relativeTime";
import { queuedOnLabel, type LiveHold } from "@/services/forge/live";

export interface QueuedDeployBannerProps {
  /** The queued release, or "" when the control plane did not name one. */
  release: string;
  holds: LiveHold[];
  /** Go to billing, carrying this page as the way back. */
  onSetUpBilling: () => void;
  now?: number;
}

export function QueuedDeployBanner({ release, holds, onSetUpBilling, now }: QueuedDeployBannerProps) {
  if (holds.length === 0) return null;

  const label = queuedOnLabel(holds);
  const billing = holds.some((hold) => hold.kind === "billing");
  const canResolve = holds.some((hold) => hold.callerCanResolve);
  // The control plane's own link to this page. Never this window's address
  // instead: in the desktop app that is a local URL nobody else can open.
  const link = holds.find((hold) => hold.actionUrl !== "")?.actionUrl ?? "";
  const since = earliest(holds.map((hold) => hold.heldSince));
  const reasons = unique(holds.map((hold) => sentence(hold.reason)));
  const fixes = unique(holds.map((hold) => hold.fix.trim()));

  return (
    <section
      className="flex flex-col gap-3 rounded-lg border border-warning/40 bg-warning/5 px-4 py-3 sm:flex-row sm:items-start sm:justify-between"
      data-testid="queued-deploy-banner"
      data-can-resolve={canResolve}
      aria-labelledby="queued-deploy-title"
    >
      <div className="flex min-w-0 gap-3">
        <Hourglass className="mt-0.5 h-4 w-4 shrink-0 text-warning-ink" aria-hidden="true" />
        <div className="min-w-0 space-y-1">
          <h2 id="queued-deploy-title" className="text-sm font-medium text-foreground">
            Waiting on {label}
          </h2>
          <p className="text-sm text-muted-foreground" data-testid="queued-deploy-reason">
            {release !== "" ? (
              <>
                Release <span className="font-mono text-foreground">{release}</span> is recorded.
              </>
            ) : (
              "This deploy is recorded."
            )}
            {reasons.map((reason) => ` ${reason}`)}
          </p>
          {fixes.map((fix) => (
            <p key={fix} className="text-sm text-muted-foreground" data-testid="queued-deploy-fix">
              {fix}
            </p>
          ))}
          {since && (
            <p className="text-xs text-muted-foreground" title={since}>
              Queued {formatRelativeTime(since, now)}
            </p>
          )}
        </div>
      </div>

      <div className="flex shrink-0 flex-col items-start gap-2 sm:items-end">
        {canResolve && billing ? (
          <Button size="sm" variant="primary" onClick={onSetUpBilling} data-testid="queued-deploy-set-up-billing">
            Set up billing
          </Button>
        ) : (
          <>
            <p className="max-w-xs text-xs text-muted-foreground sm:text-right" data-testid="queued-deploy-ask">
              {(billing
                ? "Only an organization admin can set up billing."
                : "Someone with access has to act on this.") +
                (link !== "" ? " Send them this page:" : " Ask them to — it goes out on its own once they have.")}
            </p>
            {link !== "" && <CopyLinkButton value={link} />}
          </>
        )}
      </div>
    </section>
  );
}

function CopyLinkButton({ value }: { value: string }) {
  const [copied, setCopied] = useState(false);

  const onCopy = useCallback(() => {
    // Clipboard access can be refused (an insecure origin, a denied
    // permission); the button then simply does not confirm.
    void navigator.clipboard
      ?.writeText(value)
      .then(() => {
        setCopied(true);
        setTimeout(() => setCopied(false), 1500);
      })
      .catch(() => setCopied(false));
  }, [value]);

  return (
    <Button
      size="sm"
      variant="outline"
      onClick={onCopy}
      leftIcon={copied ? <Check className="h-3.5 w-3.5" /> : <Copy className="h-3.5 w-3.5" />}
      data-testid="queued-deploy-copy-link"
      data-link={value}
    >
      {copied ? "Link copied" : "Copy link"}
    </Button>
  );
}

/** "this runs compute …" → "This runs compute ….": the control plane writes a clause. */
function sentence(text: string): string {
  const trimmed = text.trim();
  if (trimmed === "") return "";
  const capitalized = trimmed.charAt(0).toUpperCase() + trimmed.slice(1);
  return /[.!?]$/.test(capitalized) ? capitalized : `${capitalized}.`;
}

function unique(values: string[]): string[] {
  return values.filter((value, index) => value !== "" && values.indexOf(value) === index);
}

function earliest(times: Array<string | undefined>): string | undefined {
  return times.filter((time): time is string => !!time).sort()[0];
}
