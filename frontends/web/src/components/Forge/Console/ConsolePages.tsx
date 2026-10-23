// Copyright (c) 2025 Reliant Labs

/**
 * The console's pages, in the new frame (PROPOSAL §3.4 page template: a
 * PageHeader with one meta line, then the body).
 *
 * The BODIES are the components the console already had, mounted unchanged
 * where the new IA puts them — Activity gets its own design in a later PR
 * (the Overview, Releases and Dashboards have theirs: ../Workloads,
 * ../Releases, ../Observability/Dashboards). A
 * page shows a placeholder only where nothing existed (Metrics, Monitoring
 * settings).
 *
 * Every list page honours the top-bar scope (useForgeConsole().env): one
 * environment, or all of them.
 */

import { useCallback, useMemo, type ReactNode } from "react";
import { useNavigate, useSearch } from "@tanstack/react-router";
import { KeyRound } from "lucide-react";

import Card, { CardHeader } from "@/components/forge-ui/card";
import EmptyState from "@/components/forge-ui/empty_state";
import Link from "@/components/forge-ui/link";
import PageHeader, { type PageHeaderAction } from "@/components/forge-ui/page_header";
import SkeletonLoader from "@/components/forge-ui/skeleton_loader";
import Tag from "@/components/forge-ui/tag";
import { useCloudPromotions, useLiveConvergences } from "@/hooks/forge-queries";
import { useObservabilityOrg } from "@/hooks/observability-queries";
import type { LiveEnv } from "@/services/forge/live";
import type { RosterEnv } from "@/services/forge/roster";

import { ActivityFeed } from "../Activity/ActivityFeed";
import { LiveActivity } from "../EnvPage/LiveActivity";
import { Panel, SecretsTab } from "../EnvPage/EnvTabPanels";
import { CloudNotice } from "../SourceNotices";
import { useForgeConsole } from "./consoleContext";
import { ENV_ALL, envsInScope } from "./envScope";
import { DashboardsPage, ObservabilityHomePage } from "../Observability/Dashboards/DashboardsPages";
import { ObservabilityHost } from "./ObservabilityHost";
import { OBS_NAV_LABELS } from "./paths";

// ── Shared pieces ───────────────────────────────────────────────────────────

/** The scope, as the page header's meta line states it. */
function ScopeMeta({ children }: { children?: ReactNode }) {
  const { env } = useForgeConsole();
  return (
    <>
      {env === ENV_ALL ? <span>All environments</span> : <Tag>{env}</Tag>}
      {children}
    </>
  );
}

function Header({ title, meta, actions }: { title: string; meta?: ReactNode; actions?: PageHeaderAction[] }) {
  return <PageHeader title={title} meta={meta ?? <ScopeMeta />} actions={actions} className="mb-0" />;
}

function Page({ testId, children }: { testId: string; children: ReactNode }) {
  return (
    <div className="space-y-6" data-testid={testId}>
      {children}
    </div>
  );
}

/** A page that has a place in the IA and nothing behind it yet. */
function Placeholder({ title, line, testId }: { title: string; line: ReactNode; testId: string }) {
  return (
    <Page testId={testId}>
      <Header title={title} meta={<span>Coming soon</span>} />
      <Card>
        <p className="text-sm text-ink-muted">{line}</p>
      </Card>
    </Page>
  );
}

function RosterLoading() {
  return (
    <Card padding="none" aria-label="Loading" aria-busy="true">
      <SkeletonLoader variant="table-row" count={4} />
    </Card>
  );
}

/**
 * The recorded, deployed environments in scope — what has releases and
 * activity. All leaves previews out: there can be dozens, and the scope menu
 * picks one.
 */
function useDeployedInScope(): { envs: (RosterEnv & { live: LiveEnv })[]; scoped: RosterEnv | null } {
  const { env, roster } = useForgeConsole();
  return useMemo(() => {
    const deployed = roster.envs.filter(
      (candidate): candidate is RosterEnv & { live: LiveEnv } =>
        !!candidate.live && candidate.live.kind !== "local" && (env !== ENV_ALL || candidate.live.kind !== "preview")
    );
    return {
      envs: envsInScope(env, deployed),
      scoped: env === ENV_ALL ? null : (roster.envs.find((candidate) => candidate.name === env) ?? null),
    };
  }, [env, roster.envs]);
}

/** Why a single-environment scope has nothing to list here, or null when it does. */
function ScopedEnvNote({ scoped, what }: { scoped: RosterEnv | null; what: string }) {
  const { env, href } = useForgeConsole();
  if (env === ENV_ALL) return null;
  const envLink = (
    <Link href={href({ kind: "environment", env })} className="font-medium text-ink underline-offset-2 hover:underline">
      {env}
    </Link>
  );
  const sentence = !scoped ? (
    <>There is no environment named {env} in this project.</>
  ) : scoped.lifecycle === "local" ? (
    <>{envLink} runs locally from your working tree, so it has no {what}.</>
  ) : !scoped.live ? (
    <>Reliant has no record of {envLink} yet, so it has no {what} here. Register it from its environment page.</>
  ) : null;
  if (!sentence) return null;
  return (
    <p className="rounded-lg border border-dashed border-border bg-surface px-4 py-5 text-sm text-ink-muted" data-testid="scoped-env-note">
      {sentence}
    </p>
  );
}

// The Overview (with the workloads) and a workload are their own pages
// (../Workloads/WorkloadPages).

// Releases and a release are their own pages (../Releases/ReleasesPages).

function NothingDeployed({ what }: { what: string }) {
  return (
    <EmptyState
      title="Nothing deployed yet"
      description={`An environment has ${what} once a release is promoted to it. Run forge env deploy <env> to deploy one through Reliant.`}
    />
  );
}

// ── Observability ───────────────────────────────────────────────────────────

/** The Observability home (PROPOSAL §4.6), adapted in ../Observability/Dashboards/DashboardsPages. */
export const ObservabilityOverviewPage = ObservabilityHomePage;

/**
 * Logs, Traces, Profiles, Alerts — and Metrics, which has no view yet.
 * Dashboards has a route of its own (observability/dashboards), which wins.
 */
export function ObservabilityViewPage() {
  const { page, href } = useForgeConsole();
  if (page.kind !== "obs-view") return null;
  if (page.view === "dashboards") return <DashboardsPage />;
  if (page.view === "metrics") {
    return (
      <Placeholder
        testId="console-metrics"
        title="Metrics"
        line={
          <>
            A metrics explorer — any metric, filtered and grouped — lands here. Until then, metric panels live in{" "}
            <Link href={href({ kind: "obs-view", view: "dashboards" })} className="text-ink underline-offset-2 hover:underline">
              Dashboards
            </Link>
            .
          </>
        }
      />
    );
  }
  return (
    <Page testId={`console-${page.view}`}>
      <Header title={OBS_NAV_LABELS[page.view]} />
      <ObservabilityHost key={page.view} view={page.view} />
    </Page>
  );
}

// ── Configure ───────────────────────────────────────────────────────────────

/**
 * Secrets are set per environment, so with every environment in scope there
 * is no one list to show: the page asks which.
 */
export function SecretsPage() {
  const { env, roster, projectId, forgeProject, setEnv } = useForgeConsole();
  const navigate = useNavigate();
  const { secret } = useSearch({ strict: false }) as { secret?: string };
  const selectSecret = useCallback(
    (name: string | null) =>
      void navigate({
        to: ".",
        search: (prev: Record<string, unknown>) => ({ ...prev, secret: name ?? undefined }),
        replace: true,
      }),
    [navigate]
  );
  const scoped = roster.envs.find((candidate) => candidate.name === env) ?? null;

  return (
    <Page testId="console-secrets">
      <Header title="Secrets" />
      {roster.isLoading || roster.resolvingName ? (
        <RosterLoading />
      ) : env === ENV_ALL ? (
        <Card data-testid="secrets-pick-env">
          <CardHeader title="Secrets are set per environment" description="Choose one to see its secrets." />
          <div className="flex flex-wrap gap-2">
            {roster.envs.map((candidate) => (
              <button
                key={candidate.name}
                type="button"
                onClick={() => setEnv(candidate.name)}
                className="inline-flex h-8 items-center gap-1.5 rounded-md border border-border bg-surface px-3 text-sm text-ink transition-colors hover:bg-ink/5 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-accent"
              >
                <KeyRound className="h-3.5 w-3.5 text-ink-muted" aria-hidden="true" />
                {candidate.name}
              </button>
            ))}
          </div>
        </Card>
      ) : (
        <SecretsTab
          projectId={projectId}
          live={scoped?.live ?? null}
          envName={env}
          forgeProject={forgeProject}
          selectedSecret={secret ?? null}
          onSelectSecret={selectSecret}
        />
      )}
    </Page>
  );
}

export function MonitoringSettingsPage() {
  return (
    <Placeholder
      testId="console-monitoring"
      title="Monitoring settings"
      line="Alert routing, retention and telemetry sampling for this project will be configured here."
    />
  );
}

/** Project settings. Usage (the organization's allowance) moved here from Observability (D9). */
export function ProjectSettingsPage() {
  return (
    <Page testId="console-settings">
      <Header title="Project settings" meta={<span>Usage is your organization's, shared by its projects</span>} />
      <section className="space-y-3" aria-labelledby="settings-usage">
        <h2 id="settings-usage" className="text-sm font-medium text-ink">
          Usage
        </h2>
        <ObservabilityHost view="usage" needsEnvironments={false} />
      </section>
    </Page>
  );
}

// ── Activity ────────────────────────────────────────────────────────────────

/**
 * What happened: the typed activity stream (D3) for the project — who changed
 * what, through what — and, per environment in scope, the promotions with what
 * Reliant observed after each.
 */
export function ActivityPage() {
  const { roster, forgeProject, env } = useForgeConsole();
  const { envs, scoped } = useDeployedInScope();
  const org = useObservabilityOrg();
  // A scoped environment with no live record has no id to ask for: its feed is the org-wide one, not everything.
  const scopedId = scoped?.live?.id;
  const feedScope = useMemo(
    () => (org.orgId && (env === ENV_ALL || scopedId) ? { orgId: org.orgId, projectId: forgeProject, envIds: scopedId ? [scopedId] : [] } : null),
    [org.orgId, forgeProject, env, scopedId]
  );
  return (
    <Page testId="console-activity">
      <Header title="Activity" />
      {roster.isLoading || roster.resolvingName ? (
        <RosterLoading />
      ) : (
        <>
          <CloudNotice availability={roster.live.data?.availability} detail={roster.live.data?.detail} />
          <ScopedEnvNote scoped={scoped} what="activity" />
          {feedScope && (
            <Panel title="Everything that changed" description="Who did what, and through what: triage, database queries, issues Reliant opened." testId="activity-stream" flush>
              <ActivityFeed scope={feedScope} subject={forgeProject} empty="Nothing recorded yet." testId="activity-stream-feed" />
            </Panel>
          )}
          {envs.map((candidate) => (
            <EnvActivity key={candidate.name} env={candidate.live} />
          ))}
          {envs.length === 0 && !scoped && <NothingDeployed what="activity" />}
        </>
      )}
    </Page>
  );
}

function EnvActivity({ env }: { env: LiveEnv }) {
  const promotions = useCloudPromotions(env.id);
  const convergences = useLiveConvergences(env.id);
  return (
    <Panel
      title={env.name}
      description="Each promotion, and what Reliant observed on the cluster after it."
      testId={`activity-${env.name}`}
      flush
    >
      <LiveActivity
        env={env}
        promotions={promotions.data}
        convergences={convergences.data}
        isLoading={promotions.isLoading}
        error={promotions.error as Error | null}
      />
    </Panel>
  );
}
