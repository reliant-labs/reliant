// Copyright (c) 2025 Reliant Labs

/**
 * THE FRONTENDS OF AN ENVIRONMENT, each with the address it is live at.
 *
 * A static site is live the moment the platform allocates its default hostname
 * (`<word-pair>.sites.<platform domain>`); a custom domain is an addition, not
 * a prerequisite. So the headline of every row is the address the site is
 * opened at — its custom domain once that is live and healthy, else the
 * default URL — and the generated address stays beside a custom headline,
 * muted. The other custom domains sit under it with their own state — an
 * "Add a domain" link where there are none, rather than a blank that reads as
 * "not live".
 *
 * Shared by the environment page and the Overview (frontendsOf is the model);
 * this component only draws rows and never reads a query.
 */

import { useEffect, useRef, useState } from "react";
import { Check, Copy, ExternalLink, Globe, Plus } from "lucide-react";

import Button from "@/components/forge-ui/button";
import Tag from "@/components/forge-ui/tag";
import { cn } from "@/lib/utils";
import type { Frontend } from "@/services/forge/frontends";

import { DOCS, GettingStarted } from "../GettingStarted";
import { VerdictChip } from "../HostedWorkloads";
import { DomainStateBadge } from "../Domains/DomainStateBadge";
import { shortVersion } from "../Releases/releaseModel";

const ICON_BUTTON =
  "inline-flex h-6 w-6 shrink-0 items-center justify-center rounded text-ink-muted transition-colors hover:bg-ink/5 hover:text-ink focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-accent";

/** Copies `value`; the icon turns into a tick for a moment. Silent without a clipboard — the URL is on screen to select. */
function CopyButton({ value, label }: { value: string; label: string }) {
  const [copied, setCopied] = useState(false);
  const timer = useRef<ReturnType<typeof setTimeout> | undefined>(undefined);
  useEffect(() => () => clearTimeout(timer.current), []);

  async function copy() {
    try {
      await navigator.clipboard.writeText(value);
    } catch {
      return;
    }
    setCopied(true);
    clearTimeout(timer.current);
    timer.current = setTimeout(() => setCopied(false), 1500);
  }

  return (
    <button
      type="button"
      onClick={() => void copy()}
      aria-label={copied ? `Copied ${label}` : `Copy ${label}`}
      title={copied ? "Copied" : "Copy"}
      className={ICON_BUTTON}
      data-testid="frontend-copy"
    >
      {copied ? <Check className="h-3.5 w-3.5" aria-hidden="true" /> : <Copy className="h-3.5 w-3.5" aria-hidden="true" />}
    </button>
  );
}

/** One address: the link, a copy button, and nothing else. */
function UrlLine({ url, host, testId }: { url: string; host: string; testId: string }) {
  return (
    <span className="flex min-w-0 items-center gap-1">
      <a
        href={url}
        target="_blank"
        rel="noreferrer"
        title={`Open ${url}`}
        data-testid={testId}
        className="no-color inline-flex min-w-0 items-center gap-1 font-mono text-sm text-ink underline-offset-2 hover:underline focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-accent"
      >
        <span className="truncate">{host}</span>
        <ExternalLink className="h-3 w-3 shrink-0 text-ink-muted" aria-hidden="true" />
      </a>
      <CopyButton value={url} label={url} />
    </span>
  );
}

function FrontendRow({ frontend, showEnv, domainsHref }: { frontend: Frontend; showEnv: boolean; domainsHref: string }) {
  const { domains } = frontend;
  return (
    <li
      className="space-y-2 border-b border-border/60 px-4 py-3 last:border-0"
      data-testid={`frontend-${frontend.env}-${frontend.name}`}
      data-verdict={frontend.verdict}
    >
      <div className="flex flex-wrap items-center gap-x-2 gap-y-1">
        <span className="text-sm font-medium text-ink">{frontend.name}</span>
        <Tag title="Built once, uploaded to Reliant's static hosting and served from a CDN. No compute.">Static site</Tag>
        {showEnv && <span className="text-xs text-ink-muted">{frontend.env}</span>}
        {frontend.stopped ? (
          <Tag tone="warning" title="Stopped, so it is not serving. Start it from the workload's page.">
            Stopped
          </Tag>
        ) : (
          <VerdictChip verdict={frontend.verdict} testId="frontend-verdict" />
        )}
        {frontend.release && (
          <span className="ml-auto text-xs text-ink-muted" title={frontend.release} data-testid="frontend-release">
            {shortVersion(frontend.release)}
          </span>
        )}
      </div>

      {frontend.url ? (
        <div className="flex flex-wrap items-center gap-x-3 gap-y-1">
          <UrlLine url={frontend.url} host={frontend.host} testId="frontend-url" />
          {frontend.platformUrl && frontend.platformUrl !== frontend.url && (
            <span className="flex min-w-0 items-center gap-1.5 text-xs text-ink-muted" data-testid="frontend-platform-url">
              also at
              <a
                href={frontend.platformUrl}
                target="_blank"
                rel="noreferrer"
                title={`Open ${frontend.platformUrl}`}
                className="no-color truncate font-mono text-ink-muted underline-offset-2 hover:text-ink hover:underline focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-accent"
              >
                {frontend.platformHost}
              </a>
            </span>
          )}
        </div>
      ) : (
        <p className="text-xs text-ink-muted" data-testid="frontend-url-pending">
          No address yet. The platform allocates one on this site&apos;s first deploy, and it appears here.
        </p>
      )}

      {domains !== null && (
        <div className="flex flex-wrap items-center gap-x-4 gap-y-1.5 text-xs" data-testid="frontend-domains">
          {/* The domain the headline opens is not repeated here. */}
          {domains.filter((domain) => domain.hostname !== frontend.host).map((domain) => (
            <span key={domain.hostname} className="flex min-w-0 items-center gap-1.5" data-testid={`frontend-domain-${domain.hostname}`}>
              {domain.url ? (
                <UrlLine url={domain.url} host={domain.hostname} testId="frontend-domain-url" />
              ) : (
                <span className="truncate font-mono text-sm text-ink-muted">{domain.hostname}</span>
              )}
              <DomainStateBadge state={domain.state} />
            </span>
          ))}
          {domains.length === 0 && (
            <span className="text-ink-muted" data-testid="frontend-no-domain">
              Serves on its generated address.
            </span>
          )}
          <Button
            href={domainsHref}
            variant="ghost"
            size="xs"
            leftIcon={domains.length === 0 ? <Plus className="h-3 w-3" /> : <Globe className="h-3 w-3" />}
            data-testid="frontend-add-domain"
          >
            {domains.length === 0 ? "Add a domain" : "Manage domains"}
          </Button>
        </div>
      )}
    </li>
  );
}

export function FrontendList({
  frontends,
  showEnv = false,
  domainsHref,
}: {
  frontends: Frontend[];
  /** Name the environment on each row (when the list spans several). */
  showEnv?: boolean;
  /** Where the Domains screen is: "Add a domain" lands there. */
  domainsHref: string;
}) {
  return (
    <ul className={cn("overflow-hidden")} aria-label="Frontends" data-testid="frontend-list">
      {frontends.map((frontend) => (
        <FrontendRow key={`${frontend.env}/${frontend.name}`} frontend={frontend} showEnv={showEnv} domainsHref={domainsHref} />
      ))}
    </ul>
  );
}

/** No frontend in scope: what one is, and how to ship it. */
export function FrontendsEmpty({ env }: { env?: string }) {
  return (
    <GettingStarted
      testId="frontends-empty"
      icon={<Globe className="h-4 w-4" />}
      title="No frontend deployed here yet"
      about="A frontend is a website forge builds and Reliant hosts: it gets a live address on its first deploy, and you can add your own domain later."
      steps={[
        { text: "Add a frontend to your project (a landing page, a docs site, a web app).", command: "forge scaffold frontend web" },
        { text: "Deploy the environment. The address appears here once it is live.", command: `forge env deploy ${env ?? "<env>"}` },
      ]}
      docs={{ href: DOCS.deploys, label: "How environments and deploys work" }}
    />
  );
}

/** The status read has not answered yet. */
export function FrontendsLoading() {
  return (
    <div className="space-y-2 px-4 py-3" aria-busy="true" data-testid="frontends-loading">
      <span className="sr-only">Loading frontends…</span>
      <div aria-hidden="true" className="h-4 w-40 animate-pulse rounded bg-ink/5 motion-reduce:animate-none" />
      <div aria-hidden="true" className="h-4 w-64 animate-pulse rounded bg-ink/5 motion-reduce:animate-none" />
    </div>
  );
}
