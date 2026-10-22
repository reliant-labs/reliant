// Copyright (c) 2025 Reliant Labs

/**
 * THE CUSTOM DOMAINS THAT POINT AT THIS ENVIRONMENT — read-only.
 *
 * A domain is an org resource and is MANAGED on /forge/domains (see
 * ForgeDomainsPage for why it is not filed under an environment). What an
 * environment page can honestly answer is the reverse question — "what
 * hostnames serve this env right now?" — which is the list of domains whose
 * binding names this environment's id. Adding, re-binding and removing stay
 * on the Domains screen; this links there rather than growing a second copy
 * of those controls.
 *
 * Bindings are written against an environment the platform RUNS, so an
 * environment on the customer's own cluster has none, and says so.
 */

import { ExternalLink, Globe } from "lucide-react";

import { Button } from "@/components/ui/Button";
import type { DomainsState } from "@/hooks/forge-domain-queries";
import { DOMAIN_AVAILABILITY_EXPLANATIONS, type ForgeDomain } from "@/services/forge/domains";

import { DomainStateBadge } from "../Domains/DomainStateBadge";

/** The domains bound to `environmentId`, in a stable order. */
export function domainsForEnvironment(domains: ForgeDomain[], environmentId: string): ForgeDomain[] {
  return domains
    .filter((domain) => domain.binding?.environmentId === environmentId)
    .sort((a, b) => a.hostname.localeCompare(b.hostname));
}

export function EnvDomains({
  environmentId,
  placed,
  state,
  isLoading,
  limit,
  onManage,
}: {
  environmentId: string;
  /** Whether the platform runs this environment — the only kind a domain can bind to. */
  placed: boolean;
  state: DomainsState | undefined;
  isLoading: boolean;
  /** Show at most this many (the Overview's summary); the rest are counted. */
  limit?: number;
  onManage: () => void;
}) {
  if (!placed) {
    return (
      <p data-testid="env-domains-not-placed" className="text-sm text-muted-foreground">
        Custom domains are served for environments Reliant runs. This one runs on your own cluster, so its
        hostnames are yours to route.
      </p>
    );
  }
  if (isLoading && !state) {
    return (
      <p data-testid="env-domains-loading" className="text-sm text-muted-foreground">
        Loading domains…
      </p>
    );
  }
  if (state && state.availability !== "available") {
    return (
      <p data-testid="env-domains-unavailable" className="text-sm text-muted-foreground">
        {DOMAIN_AVAILABILITY_EXPLANATIONS[state.availability]}
      </p>
    );
  }

  const bound = domainsForEnvironment(state?.domains ?? [], environmentId);
  if (bound.length === 0) {
    return (
      <div data-testid="env-domains-empty" className="flex flex-wrap items-center justify-between gap-3">
        <p className="text-sm text-muted-foreground">No custom domain points here — it serves on its generated URLs.</p>
        <Button variant="outline" size="sm" onClick={onManage} leftIcon={<Globe className="h-3.5 w-3.5" />}>
          Add a domain
        </Button>
      </div>
    );
  }

  const shown = limit !== undefined ? bound.slice(0, limit) : bound;
  const hidden = bound.length - shown.length;
  return (
    <div className="space-y-3" data-testid="env-domains">
      <ul className="overflow-hidden rounded-lg border border-border/60 bg-background">
        {shown.map((domain) => (
          <li
            key={domain.id}
            data-testid={`env-domain-${domain.hostname}`}
            className="grid grid-cols-[minmax(0,1fr)_auto] items-center gap-x-3 gap-y-1 border-b border-border/60 px-3 py-2 last:border-0"
          >
            <span className="flex min-w-0 items-center gap-1.5">
              {domain.state === "live" ? (
                <a
                  href={`https://${domain.hostname}`}
                  target="_blank"
                  rel="noreferrer"
                  title={domain.hostname}
                  className="inline-flex min-w-0 items-center gap-1 font-mono text-sm text-foreground hover:underline"
                >
                  <span className="truncate">{domain.hostname}</span>
                  <ExternalLink className="h-3 w-3 shrink-0 text-muted-foreground" aria-hidden="true" />
                </a>
              ) : (
                <span className="truncate font-mono text-sm text-foreground" title={domain.hostname}>
                  {domain.hostname}
                </span>
              )}
            </span>
            <DomainStateBadge state={domain.state} />
            <span className="col-span-2 min-w-0 truncate text-xs text-muted-foreground">
              {domain.binding?.redirectTo ? `Redirects to ${domain.binding.redirectTo}` : `Serves ${domain.binding?.target || "—"}`}
            </span>
          </li>
        ))}
      </ul>
      <div className="flex flex-wrap items-center justify-between gap-2">
        <span className="text-xs text-muted-foreground">
          {hidden > 0 ? `and ${hidden} more` : ""}
        </span>
        <Button variant="ghost" size="sm" onClick={onManage} data-testid="env-domains-manage">
          Manage domains
        </Button>
      </div>
    </div>
  );
}
