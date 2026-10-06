// Copyright (c) 2025 Reliant Labs

/**
 * One domain, expanded: what is happening, what you do next, and the records.
 *
 * ── THE ORDER IS THE ARGUMENT ───────────────────────────────────────────────
 *
 * State, then next step, then records, then what it serves, then the
 * destructive actions. A tenant opens this because something is not working
 * yet, and the question underneath is always "is this me or you?" — so the
 * two sentences that answer it come before anything else, and the records
 * they will need come immediately after, not below a fold.
 *
 * ── WHY THE ERROR IS QUOTED VERBATIM ────────────────────────────────────────
 *
 * `last_error` is written by the control plane in terms a tenant can act on
 * ("no TXT record found at _reliant-challenge.example.com"). Rewriting it
 * into our own vocabulary would be the second place that sentence is
 * authored, and the copy here would drift from what the checker actually
 * complained about. So the error is shown as-is, under a heading that frames
 * it, with the retry beside it.
 *
 * ── WHAT THIS CANNOT SHOW ───────────────────────────────────────────────────
 *
 * There is no per-record check result in the API. `Domain` carries one
 * aggregate state and one `last_error` for the whole domain, so this view
 * cannot say "the A record resolves, the TXT is missing" — the thing a
 * tenant most wants when a domain will not verify. It says what it knows and
 * does not simulate the rest; a green tick per row derived from the
 * aggregate state would be a lie in exactly the case that matters.
 */

import { AlertTriangle, Loader2, RefreshCw, Trash2, Unlink } from "lucide-react";

import {
  DOMAIN_STATE_EXPLANATIONS,
  DOMAIN_STATE_NEXT_STEPS,
  bindingSummary,
  type ForgeDomain,
} from "@/services/forge/domains";

import { DnsRecordsTable } from "./DnsRecordsTable";

export interface DomainDetailProps {
  domain: ForgeDomain;
  /** Maps a binding's control-plane environment id back to the name a user knows. */
  envNameFor: (environmentId: string) => string;
  onVerify: () => void;
  onRebind: () => void;
  onUnbind: () => void;
  onRemove: () => void;
  isVerifying: boolean;
  /** The most recent action's failure, if any. Distinct from the domain's own last_error. */
  actionError: string | null;
}

function formatWhen(iso: string | undefined): string | null {
  if (!iso) return null;
  const date = new Date(iso);
  return Number.isNaN(date.getTime()) ? null : date.toLocaleString();
}

export function DomainDetail({
  domain,
  envNameFor,
  onVerify,
  onRebind,
  onUnbind,
  onRemove,
  isVerifying,
  actionError,
}: DomainDetailProps) {
  const nextStep = DOMAIN_STATE_NEXT_STEPS[domain.state];
  const liveSince = formatWhen(domain.liveSince);
  const verifiedAt = formatWhen(domain.verifiedAt);

  return (
    <div className="flex flex-col gap-5" data-testid={`domain-detail-${domain.hostname}`}>
      <section className="flex flex-col gap-2">
        <p className="text-sm text-foreground">{DOMAIN_STATE_EXPLANATIONS[domain.state]}</p>
        {nextStep && (
          <p className="text-sm text-muted-foreground" data-testid="domain-next-step">
            {nextStep}
          </p>
        )}
        {(liveSince || verifiedAt) && (
          <p className="text-xs text-muted-foreground">
            {liveSince && <>Live since {liveSince}. </>}
            {verifiedAt && <>Ownership last proven {verifiedAt}.</>}
          </p>
        )}
      </section>

      {domain.lastError && (
        <section
          className="flex flex-col gap-1 rounded-lg border border-destructive/40 bg-destructive/10 p-3"
          role="alert"
          data-testid="domain-last-error"
        >
          <span className="flex items-center gap-1.5 text-xs font-medium text-destructive-ink">
            <AlertTriangle className="h-3.5 w-3.5" aria-hidden="true" />
            What went wrong
          </span>
          {/* The checker's own words. See the header comment. */}
          <p className="break-words text-xs text-destructive-ink">{domain.lastError}</p>
        </section>
      )}

      <section className="flex flex-col gap-2">
        <h4 className="text-xs font-medium uppercase tracking-wide text-muted-foreground">
          DNS records
        </h4>
        <DnsRecordsTable records={domain.requiredRecords} />
        <p className="text-2xs text-muted-foreground">
          Reliant cannot publish these for you — they live in a zone we do not control. After you
          save them, propagation usually takes a few minutes and can take up to an hour.
        </p>
      </section>

      <section className="flex flex-col gap-2">
        <h4 className="text-xs font-medium uppercase tracking-wide text-muted-foreground">
          Serving
        </h4>
        <p className="text-sm text-foreground" data-testid="domain-binding">
          {domain.binding
            ? domain.binding.redirectTo
              ? bindingSummary(domain.binding)
              : `${domain.binding.target} in ${envNameFor(domain.binding.environmentId)}`
            : "Not serving anything yet. Choose a target to point it at."}
        </p>
      </section>

      {actionError && (
        <p className="text-xs text-destructive-ink" role="alert">
          {actionError}
        </p>
      )}

      <div className="flex flex-wrap items-center gap-2 border-t border-border/60 pt-4">
        <button
          type="button"
          onClick={onVerify}
          disabled={isVerifying}
          className="inline-flex items-center gap-1.5 rounded-lg border border-border px-3 py-1.5 text-xs text-foreground transition hover:bg-muted disabled:opacity-50"
        >
          {isVerifying ? (
            <Loader2 className="h-3.5 w-3.5 animate-spin" aria-hidden="true" />
          ) : (
            <RefreshCw className="h-3.5 w-3.5" aria-hidden="true" />
          )}
          Check DNS now
        </button>
        <button
          type="button"
          onClick={onRebind}
          className="rounded-lg border border-border px-3 py-1.5 text-xs text-foreground transition hover:bg-muted"
        >
          {domain.binding ? "Change target" : "Choose a target"}
        </button>
        {domain.binding && (
          <button
            type="button"
            onClick={onUnbind}
            className="inline-flex items-center gap-1.5 rounded-lg border border-border px-3 py-1.5 text-xs text-foreground transition hover:bg-muted"
          >
            <Unlink className="h-3.5 w-3.5" aria-hidden="true" />
            Stop serving
          </button>
        )}
        <button
          type="button"
          onClick={onRemove}
          className="ml-auto inline-flex items-center gap-1.5 rounded-lg border border-destructive/40 px-3 py-1.5 text-xs text-destructive-ink transition hover:bg-destructive/10"
        >
          <Trash2 className="h-3.5 w-3.5" aria-hidden="true" />
          Remove domain
        </button>
      </div>
      <p className="text-2xs text-muted-foreground">
        Stopping serving keeps the domain and its verification, so re-binding later needs no DNS
        work and no new certificate. Removing it releases the hostname for anyone to claim.
      </p>
    </div>
  );
}
