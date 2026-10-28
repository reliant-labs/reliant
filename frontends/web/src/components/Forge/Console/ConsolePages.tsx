// Copyright (c) 2025 Reliant Labs

/**
 * The console's pages, in the new frame (a page template:
 * PageHeader with one meta line, then the body).
 *
 * The BODIES are the components the console already had, mounted where the
 * IA puts them (the Overview, Releases, Dashboards and Metrics have pages of
 * their own: ../Workloads, ../Releases, ../Observability/Dashboards,
 * ../Observability/Metrics).
 *
 * Every list page honours the top-bar scope (useForgeConsole().env): one
 * environment, or all of them.
 */

import { useCallback, useMemo, type ReactNode } from "react";
import { useNavigate, useSearch } from "@tanstack/react-router";
import { Rocket } from "lucide-react";

import Card from "@/components/forge-ui/card";
import { DOCS, GettingStarted } from "@/components/Forge/GettingStarted";
import Link from "@/components/forge-ui/link";
import PageHeader, { type PageHeaderAction } from "@/components/forge-ui/page_header";
import SkeletonLoader from "@/components/forge-ui/skeleton_loader";
import Tag from "@/components/forge-ui/tag";
import { useObservabilityOrg } from "@/hooks/observability-queries";
import type { LiveEnv } from "@/services/forge/live";
import type { RosterEnv } from "@/services/forge/roster";

import { ActivityFeed } from "../Activity/ActivityFeed";
import { Panel, SecretsTab } from "../EnvPage/EnvTabPanels";
import { SecretsMatrix } from "../Secrets/SecretsMatrix";
import { CloudNotice } from "../SourceNotices";
import { useForgeConsole } from "./consoleContext";
import { ENV_ALL, envsInScope } from "./envScope";
import { DashboardsPage } from "../Observability/Dashboards/DashboardsPages";
import { MetricsExplorerPage } from "../Observability/Metrics/MetricsExplorerPage";
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

function NothingDeployed({ what, about }: { what: string; about: string }) {
  return (
    <GettingStarted
      testId="console-nothing-deployed"
      icon={<Rocket className="h-4 w-4" />}
      title="Nothing deployed yet"
      about={about}
      steps={[
        {
          text: "Deploy an environment through Reliant from your checkout. It builds, cuts a release and deploys it.",
          command: "forge env deploy <env>",
        },
      ]}
      docs={{ href: DOCS.deploys, label: "How environments and deploys work" }}
    >
      <p>
        An environment has {what} once a release is promoted to it. A local environment (forge env up) runs your working tree and
        records none.
      </p>
    </GettingStarted>
  );
}

// ── Observability ───────────────────────────────────────────────────────────

/**
 * Logs, Traces, Profiles, Alerts — and Metrics, the explorer
 * (../Observability/Metrics). Dashboards has a route of its own
 * (observability/dashboards), which wins.
 */
export function ObservabilityViewPage() {
  const { page } = useForgeConsole();
  if (page.kind !== "obs-view") return null;
  if (page.view === "dashboards") return <DashboardsPage />;
  if (page.view === "metrics") {
    return (
      <Page testId="console-metrics">
        <Header title={OBS_NAV_LABELS.metrics} />
        <MetricsExplorerPage />
      </Page>
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

/** All scope compares metadata; a scoped environment owns the editing flow. */
export function SecretsPage() {
  const { env, roster, projectId, forgeProject, href } = useForgeConsole();
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
        <SecretsMatrix
          envs={roster.envs}
          projectId={projectId}
          secretHref={(name, secretName) => href({ kind: "secrets" }, { env: name, secret: secretName })}
        />
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

// ── Activity ────────────────────────────────────────────────────────────────

/**
 * The AUDIT LOG: the typed activity stream for the project — who changed what,
 * through what. Promotions are not repeated here as their own timelines: a
 * release's promotions, rollbacks and deploys are the Releases page's.
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
            <Panel title="Everything that changed" description="Who did what, and through what: promotions, triage, database queries, issues Reliant opened." testId="activity-stream" flush>
              <ActivityFeed scope={feedScope} subject={forgeProject} empty="Nothing recorded yet." testId="activity-stream-feed" />
            </Panel>
          )}
          {envs.length === 0 && !scoped && (
            <NothingDeployed
              what="activity"
              about="Activity is everything that changed in this project: each promotion and rollback, what the platform observed after it, and what people and agents did."
            />
          )}
        </>
      )}
    </Page>
  );
}
