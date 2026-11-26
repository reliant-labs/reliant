// Copyright (c) 2026 Reliant Labs

/**
 * ONE STATIC SITE: /forge/p/:app/services/:site (Site insights R2, design D4).
 *
 *   header         the environment (a picker when the site runs in more than
 *                  one: the scope's, else the one with a live custom domain,
 *                  else the first), the verdict, the live URL, the release,
 *                  and what is wrong: drift, a suspension, the last error.
 *   site insights  the Real Experience Score (the Speed view's, narrowed to
 *                  this site), SEO and accessibility (placeholders until the
 *                  audits ship), and the way into Site insights.
 *   deploys        the environment's promotions that moved this site
 *                  (RecentDeploys, filtered to its `sites/<name>/` artifact).
 *   domains        its custom domains: state, where their DNS points, and
 *                  where to add one.
 *   traffic, usage and free tier, configuration
 *                  the site's hosting (SiteHosting.tsx, R3): its CDN traffic,
 *                  its usage against the free allowance, and how the platform
 *                  serves it, from control-plane's StaticSiteService.
 *
 * Every fact but the deploys, the score, the domains' setup lines and the
 * hosting sections is GetStatus's (servicesModel.siteEnvs).
 */

import { useMemo, type ReactNode } from "react";
import { AlertTriangle, ExternalLink, PauseCircle } from "lucide-react";

import Card from "@/components/forge-ui/card";
import Link from "@/components/forge-ui/link";
import Status from "@/components/forge-ui/status";
import Tag from "@/components/forge-ui/tag";
import { usePanelQuery } from "@/hooks/observability-queries";
import { useForgeDomains } from "@/hooks/forge-domain-queries";
import { cn } from "@/lib/utils";
import { workloadLastError, type ForgeHostedWorkload } from "@/services/forge/topology";

import { DomainStateBadge } from "../Domains/DomainStateBadge";
import { SCORE_LABEL, siteRows, siteServiceNames, sitesPanel, type Rating } from "../Analytics/speedInsightsModel";
import { SITE_SPEED_DEFAULT_RANGE, useSiteInsightsScope, useSiteSpeedSummary } from "../Analytics/useSiteSpeedSummary";
import { useForgeConsole } from "../Console/consoleContext";
import { Header, Page } from "../Console/consolePageFrame";
import { formatCount } from "../Observability/format";
import { Segmented } from "../Observability/Dashboards/parts";
import { INK_LINK } from "../Releases/releaseParts";
import { shortVersion } from "../Releases/releaseModel";
import { runStateOf } from "../runStateVocabulary";
import { DriftTag } from "../Workloads/workloadParts";
import { RecentDeploys } from "./RecentDeploys";
import { Section } from "./serviceParts";
import { SiteConfiguration, SiteTraffic, SiteUsage } from "./SiteHosting";
import { defaultSiteEnv, siteArtifactPrefix, siteDomainRows, siteEnvs, type SiteEnv } from "./servicesModel";

export interface SiteDetailProps {
  name: string;
  /** GetStatus's static deployments named `name`, by environment (servicesModel.sitesOf). */
  entries: readonly { envName: string; deployment: ForgeHostedWorkload }[];
  /** The console's environments: their ids (for the deploys) and releases. */
  envs: readonly { id: string; name: string; release: string }[];
}

export function SiteDetail({ name, entries, envs }: SiteDetailProps) {
  const { env: scopeEnv, setEnv, href } = useForgeConsole();
  const sites = useMemo(() => siteEnvs(entries, envs), [entries, envs]);
  const site = defaultSiteEnv(sites, scopeEnv);
  if (!site) return null;
  return (
    <Page testId="console-service-site">
      <SiteHeader name={name} site={site} sites={sites} onEnv={setEnv} />
      <SiteInsightsCard name={name} site={site} />
      {site.envId ? (
        <RecentDeploys env={{ id: site.envId, name: site.envName }} name={name} artifactPrefix={siteArtifactPrefix(name)} testId="site-deploys" />
      ) : (
        <Section title="Recent deploys" testId="site-deploys">
          <p className="text-ui text-ink-muted">The console does not list {site.envName}, so its deploys cannot be read here.</p>
        </Section>
      )}
      <SiteDomains name={name} site={site} />
      <SiteTraffic name={name} site={site} />
      <SiteUsage name={name} site={site} />
      <SiteConfiguration name={name} site={site} />
      <p className="pt-2 text-ui text-ink-muted">
        Visitors, page views and browser errors are on{" "}
        <Link href={href({ kind: "obs-view", view: "sessions" }, { env: site.envName })} className="no-color text-primary-ink hover:underline">
          Sessions
        </Link>{" "}
        and{" "}
        <Link href={href({ kind: "obs-view", view: "errors" }, { env: site.envName })} className="no-color text-primary-ink hover:underline">
          Errors
        </Link>
        .
      </p>
    </Page>
  );
}

// ── Header ──────────────────────────────────────────────────────────────────

function SiteHeader({ name, site, sites, onEnv }: { name: string; site: SiteEnv; sites: readonly SiteEnv[]; onEnv: (env: string) => void }) {
  const { href } = useForgeConsole();
  const suspended = site.suspendReason !== "" || site.deployment.observed_state === "suspended";
  const run = suspended ? runStateOf(site.deployment) : null;
  // A last error on a site that has since converged is history (workloadLastError).
  const lastError = workloadLastError(site.deployment);
  return (
    <>
      <Header
        title={name}
        meta={
          <>
            <Status tone={site.tone} label={site.status} />
            <span>Static site</span>
            {site.release && (
              <span className="inline-flex items-center gap-1.5" data-testid="site-release">
                <Link href={href({ kind: "release", version: site.release })} className={INK_LINK} title={site.release}>
                  {shortVersion(site.release)}
                </Link>
                {site.drifted && <DriftTag workload={site.deployment} />}
              </span>
            )}
            <Tag>{site.envName}</Tag>
          </>
        }
      />
      <div className="flex flex-wrap items-center gap-x-4 gap-y-2 text-ui" data-testid="site-header">
        {site.url ? (
          <a href={site.url} target="_blank" rel="noreferrer" className="inline-flex items-center gap-1 text-primary-ink hover:underline" data-testid="site-url">
            {site.url.replace(/^https?:\/\//, "")}
            <ExternalLink className="size-3" aria-hidden="true" />
          </a>
        ) : (
          <span className="text-ink-muted">No public URL yet</span>
        )}
        {sites.length > 1 && (
          <Segmented label="Environment" value={site.envName} options={sites.map((s) => ({ value: s.envName, label: s.envName }))} onChange={onEnv} testId="site-env-picker" />
        )}
      </div>
      {run && (
        <Note icon={<PauseCircle className="size-4 text-warning-ink" aria-hidden="true" />} testId="site-suspended">
          <span className="font-medium">{run.label}.</span> {run.detail}
        </Note>
      )}
      {site.drifted && (
        <Note icon={<AlertTriangle className="size-4 text-danger-ink" aria-hidden="true" />} testId="site-drifted">
          The platform is serving files other than the ones {site.release ? shortVersion(site.release) : "the published release"} asks for. Deploying the release again puts them back.
        </Note>
      )}
      {lastError && !run && (
        <Note icon={<AlertTriangle className="size-4 text-danger-ink" aria-hidden="true" />} testId="site-last-error">
          <span className="font-medium">Last error:</span> {lastError}
        </Note>
      )}
    </>
  );
}

function Note({ icon, testId, children }: { icon: ReactNode; testId: string; children: ReactNode }) {
  return (
    <p className="flex items-start gap-2 text-ui text-ink" data-testid={testId}>
      <span className="mt-0.5 shrink-0">{icon}</span>
      <span className="min-w-0">{children}</span>
    </p>
  );
}

// ── Site insights ───────────────────────────────────────────────────────────

const RATING_INK: Record<Rating, string> = { good: "text-success-ink", ni: "text-warning-ink", poor: "text-danger-ink" };

/** What SEO and accessibility say until the audits (Site insights R4, R5) report. */
export const FIRST_AUDIT_PENDING = "Your first audit runs after the next release";

function SiteInsightsCard({ name, site }: { name: string; site: SiteEnv }) {
  const { forgeProject, href } = useForgeConsole();
  const services = useMemo(() => siteServiceNames(forgeProject, name), [forgeProject, name]);
  const envId = site.envId || undefined;
  const speed = useSiteSpeedSummary({ project: forgeProject, envId, services });
  // The service.name Site insights' picker knows this site by: the one that sent vitals, else forge's own derivation.
  const scope = useSiteInsightsScope(forgeProject, envId);
  const panel = useMemo(() => sitesPanel(), []);
  const measured = siteRows(usePanelQuery(scope, SITE_SPEED_DEFAULT_RANGE, panel, []).data);
  const siteParam = services.find((service) => measured.some((row) => row.service === service)) ?? services[0];
  return (
    <Section
      title="Site insights"
      meta="last 7 days"
      actions={
        <Link href={href({ kind: "site-insights" }, { env: site.envName, site: siteParam })} className="no-color text-meta text-primary-ink hover:underline" data-testid="site-insights-link">
          View site insights →
        </Link>
      }
      testId="site-insights-card"
    >
      <Card padding="none" className="grid divide-y divide-border-subtle sm:grid-cols-3 sm:divide-x sm:divide-y-0">
        <Score title="Speed" testId="site-insights-speed" loading={speed.isLoading}>
          {speed.score === null ? (
            <>
              <Figure>—</Figure>
              <Caption>No measured visit to {name} in {site.envName} yet.</Caption>
            </>
          ) : (
            <>
              <Figure className={speed.rating ? RATING_INK[speed.rating] : undefined}>{speed.score}</Figure>
              <Caption>
                Real Experience Score{speed.rating ? ` · ${SCORE_LABEL[speed.rating]}` : ""} · {formatCount(speed.views)} page loads
              </Caption>
            </>
          )}
        </Score>
        <Score title="SEO" testId="site-insights-seo">
          <Figure className="text-ink-muted">—</Figure>
          <Caption>{FIRST_AUDIT_PENDING}</Caption>
        </Score>
        <Score title="Accessibility" testId="site-insights-accessibility">
          <Figure className="text-ink-muted">—</Figure>
          <Caption>{FIRST_AUDIT_PENDING}</Caption>
        </Score>
      </Card>
    </Section>
  );
}

function Score({ title, testId, loading, children }: { title: string; testId: string; loading?: boolean; children: ReactNode }) {
  return (
    <div className="min-w-0 px-4 py-3" data-testid={testId}>
      <h3 className="text-ui font-medium text-ink-muted">{title}</h3>
      {loading ? <div className="mt-1 h-7 w-16 animate-pulse rounded bg-ink/5 motion-reduce:animate-none" /> : children}
    </div>
  );
}

function Figure({ className, children }: { className?: string; children: ReactNode }) {
  return <div className={cn("mt-0.5 text-2xl font-semibold tabular-nums text-ink", className)}>{children}</div>;
}

function Caption({ children }: { children: ReactNode }) {
  return <p className="text-meta text-ink-muted">{children}</p>;
}

// ── Domains ─────────────────────────────────────────────────────────────────

const SERVING_PATH: Record<string, string> = {
  edge: "Served from the edge",
  ingress: "Served through the ingress",
  elsewhere: "DNS points away from Reliant",
};

function SiteDomains({ name, site }: { name: string; site: SiteEnv }) {
  const { href } = useForgeConsole();
  const org = useForgeDomains();
  const rows = siteDomainRows(site, name, org.data?.domains ?? []);
  return (
    <Section
      title="Domains"
      meta={`in ${site.envName}`}
      actions={
        <Link href={href({ kind: "domains" }, { env: site.envName })} className="no-color text-meta text-primary-ink hover:underline" data-testid="site-add-domain">
          Add a domain →
        </Link>
      }
      testId="site-domains"
    >
      {rows.length === 0 ? (
        <p className="text-ui text-ink-muted">
          No custom domain serves {name} in {site.envName}. Visitors reach it at {site.url ? site.url.replace(/^https?:\/\//, "") : "its platform URL"}.
        </p>
      ) : (
        <ul className="divide-y divide-border-subtle rounded-xl border border-border bg-surface">
          {rows.map((row) => (
            <li key={row.hostname} className="flex flex-wrap items-center gap-x-3 gap-y-1 px-3 py-2.5 text-ui" data-testid={`site-domain-${row.hostname}`}>
              {row.state === "live" ? (
                <a href={`https://${row.hostname}`} target="_blank" rel="noreferrer" className="no-color inline-flex items-center gap-1 font-medium text-ink hover:underline">
                  {row.hostname}
                  <ExternalLink className="size-3 text-ink-muted" aria-hidden="true" />
                </a>
              ) : (
                <span className="font-medium text-ink">{row.hostname}</span>
              )}
              <DomainStateBadge state={row.state} />
              {SERVING_PATH[row.servingPath] && (
                <span className="inline-flex items-center gap-1 text-meta text-ink-muted" data-testid="site-domain-serving-path">
                  {row.servingPath === "elsewhere" && <AlertTriangle className="size-3.5 text-warning-ink" aria-hidden="true" />}
                  {SERVING_PATH[row.servingPath]}
                </span>
              )}
              {row.statusLine && <span className={cn("basis-full text-meta", row.statusLine.tone === "danger" ? "text-danger-ink" : "text-ink-muted")}>{row.statusLine.text}</span>}
            </li>
          ))}
        </ul>
      )}
    </Section>
  );
}
