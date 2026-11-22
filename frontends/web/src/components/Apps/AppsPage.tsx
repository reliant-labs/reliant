// Copyright (c) 2025 Reliant Labs

/**
 * The apps list — everything the user has built and deployed, with the links
 * to open it. Projects shows it (`/projects`, and `?show=apps` for it alone;
 * the nav's Apps heading lands there, and the retired /apps redirects there).
 *
 * An app is a forge project (services/forge/apps.ts), and the page is ONE
 * table of them (forge-ui DataTable, the list every console page is made of):
 * the app, the site it is live at (its custom domain once that is live), the
 * health of each long-lived environment, and its last deploy. A row opens the
 * app's console for everything else (releases, observability, secrets,
 * domains); the site link opens the app itself.
 *
 * The page answers "what have I shipped, and where is it?", so a user who has
 * shipped nothing is offered the way to: a chat that builds an app with forge
 * and deploys it.
 */

import { useEffect, useMemo, useRef } from "react";
import { Plus } from "lucide-react";

import Button from "@/components/forge-ui/button";
import Card from "@/components/forge-ui/card";
import DataTable, { type DataTableColumn } from "@/components/forge-ui/data_table";
import PageHeader from "@/components/forge-ui/page_header";
import Status from "@/components/forge-ui/status";
import { useApps } from "@/hooks/apps-queries";
import { useAlerts, useObservabilityOrg } from "@/hooks/observability-queries";
import { formatAbsoluteTime, formatRelativeTime } from "@/lib/relativeTime";
import { isDeployed, isLive, isNeverDeployed, primaryLink, type AppEnv, type AppSummary } from "@/services/forge/apps";

import { consolePath } from "../Forge/Console/paths";
import { envHealth, LiveLink } from "./appParts";
import { ClaimedBanner, useClaimedApp } from "./ClaimedBanner";
import { useStartBuildingApp } from "./startBuilding";
import { ForgeFirstRun } from "../ForgeShell/ForgeFirstRun";

/**
 * The list itself, without the page's chrome. `variant="forge"` says how to
 * deploy an app from a checkout instead of offering "Build an app", for a
 * surface where no chat builds apps.
 */
export function AppsBody({
  variant = "reliant",
  header: showHeader = true,
}: {
  variant?: "reliant" | "forge";
  /** False under a page that heads the list itself (Projects). */
  header?: boolean;
}) {
  const forge = variant === "forge";
  const { apps, loading, availability, detail } = useApps();
  const startBuilding = useStartBuildingApp();
  const claimed = useClaimedApp(apps.map((a) => a.name), loading);

  const live = apps.filter(isLive).length;
  const header = (
    <PageHeader
      title="Apps"
      meta={
        loading ? undefined : apps.length === 0 ? (
          <span>{forge ? "What you deploy with forge, with the links to open it." : "What you build and deploy with Reliant, with the links to open it."}</span>
        ) : (
          <span data-testid="apps-counts">
            {apps.length} {apps.length === 1 ? "app" : "apps"}
            {live > 0 && ` · ${live} live`}
          </span>
        )
      }
      actions={
        forge
          ? []
          : [{ label: "Build an app", onClick: startBuilding, variant: "primary", icon: <Plus className="h-4 w-4" aria-hidden="true" /> }]
      }
      className="mb-0"
    />
  );

  return (
    <div className="space-y-5" data-testid="apps-page">
      {showHeader && header}
      {claimed.name && <ClaimedBanner name={claimed.name} listed={claimed.found} loading={loading} paused={claimed.paused} />}
      <AvailabilityNote availability={availability} detail={detail} />
      {!loading && apps.length === 0 ? (
        forge ? (
          <ForgeFirstRun testId="apps-empty" />
        ) : (
          <NoApps onBuild={startBuilding} />
        )
      ) : (
        <AppsTable apps={apps} loading={loading} deploysKnown={availability === "available"} highlighted={claimed.name} />
      )}
    </div>
  );
}

type AppRow = { name: string; app: AppSummary };

/**
 * The apps, one row each. `deploysKnown` is false when the control plane could
 * not be read: a row then cannot say "not deployed yet", only that it does not
 * know.
 */
function AppsTable({
  apps,
  loading,
  deploysKnown,
  highlighted,
}: {
  apps: AppSummary[];
  loading: boolean;
  deploysKnown: boolean;
  highlighted: string | null;
}) {
  const container = useRef<HTMLDivElement>(null);
  const rows = useMemo<AppRow[]>(() => apps.map((app) => ({ name: app.name, app })), [apps]);
  const firing = useFiringByEnv();

  // A just-claimed app is brought into view once its row exists.
  const highlightedListed = !!highlighted && apps.some((app) => app.name === highlighted);
  useEffect(() => {
    if (!highlightedListed) return;
    const row = container.current?.querySelector<HTMLElement>(`[data-app="${highlighted}"]`);
    row?.scrollIntoView?.({ block: "center", behavior: "smooth" });
  }, [highlighted, highlightedListed]);

  const columns: DataTableColumn<AppRow>[] = [
    {
      key: "name",
      header: "App",
      className: "truncate font-medium",
      render: (_, { app }) => <span title={app.name}>{app.name}</span>,
    },
    {
      key: "site",
      header: "Site",
      width: "36%",
      interactive: true,
      className: "truncate",
      render: (_, { app }) => <SiteCell app={app} deploysKnown={deploysKnown} />,
    },
    {
      key: "envs",
      header: "Environments",
      width: "26%",
      className: "truncate max-lg:hidden",
      headerClassName: "max-lg:hidden",
      render: (_, { app }) => <EnvsCell app={app} firing={firing} />,
    },
    {
      key: "deployed",
      header: "Last deploy",
      width: "9rem",
      className: "text-ink-muted max-md:hidden",
      headerClassName: "max-md:hidden",
      render: (_, { app }) => <LastDeployCell app={app} deploysKnown={deploysKnown} />,
    },
  ];

  return (
    <div ref={container} className="forge-ui" data-testid="apps-list" aria-busy={loading || undefined}>
      <DataTable<AppRow>
        ariaLabel="Apps"
        columns={columns}
        data={rows}
        loading={loading}
        pageSize={3}
        layout="fixed"
        getRowKey={(row) => row.name}
        getRowHref={(row) => consolePath(row.name, { kind: "overview" })}
        getRowProps={(row) => ({
          "data-testid": `app-row-${row.name}`,
          "data-app": row.name,
          "data-highlighted": row.name === highlighted ? "true" : undefined,
          className: row.name === highlighted ? "bg-accent-surface" : undefined,
        })}
      />
    </div>
  );
}

/** The app's site — prod's, else the first environment's — or why there is none to open. */
function SiteCell({ app, deploysKnown }: { app: AppSummary; deploysKnown: boolean }) {
  const link = primaryLink(app);
  if (link) return <LiveLink link={link} />;
  if (app.envs.some((env) => env.loading)) {
    return <span className="inline-block h-3 w-40 animate-pulse rounded bg-ink/5 align-middle motion-reduce:animate-none" aria-label="Loading" />;
  }
  const why = isDeployed(app) ? "No public site" : deploysKnown && isNeverDeployed(app) ? "Not deployed yet" : "Unavailable";
  return (
    <span
      className="text-ink-muted"
      title={
        why === "Not deployed yet"
          ? "Ask the agent in a chat on this project to deploy it, or run `reliant forge env deploy prod` in its checkout."
          : why === "No public site"
            ? "Deployed, with no frontend to open. Its services are in the console."
            : undefined
      }
    >
      {why}
    </span>
  );
}

/** Each long-lived environment's health, prod first; previews are counted. */
function EnvsCell({ app, firing }: { app: AppSummary; firing: ReadonlyMap<string, number> }) {
  if (app.envs.length === 0 && app.previews === 0) return <span className="text-ink-muted">—</span>;
  return (
    <span className="inline-flex items-center gap-3">
      {app.envs.map((env) => (
        <EnvChip key={env.id} env={env} firing={firing.get(env.id) ?? 0} />
      ))}
      {app.previews > 0 && (
        <span className="text-xs text-ink-muted">
          +{app.previews} preview{app.previews === 1 ? "" : "s"}
        </span>
      )}
    </span>
  );
}

/** Firing alerts by control-plane environment id: what makes a converged environment not "Healthy". */
function useFiringByEnv(): ReadonlyMap<string, number> {
  const org = useObservabilityOrg();
  const alerts = useAlerts(org.orgId);
  return useMemo(() => {
    const counts = new Map<string, number>();
    for (const alert of alerts.data ?? []) if (alert.state === "firing") counts.set(alert.envId, (counts.get(alert.envId) ?? 0) + 1);
    return counts;
  }, [alerts.data]);
}

function EnvChip({ env, firing }: { env: AppEnv; firing: number }) {
  if (env.loading) {
    return (
      <span className="inline-flex items-center gap-1.5 text-ink-muted" data-testid={`app-env-${env.name}`}>
        <span className="h-1.5 w-1.5 animate-pulse rounded-full bg-ink/20 motion-reduce:animate-none" aria-hidden="true" />
        {env.name}
      </span>
    );
  }
  const health = envHealth(env.verdict, firing);
  const release = env.release || (env.unread ? "release unknown" : "not deployed yet");
  const facts = [health.label, release].join(" · ");
  return (
    <span title={`${env.name}: ${facts}`} data-testid={`app-env-${env.name}`} data-verdict={env.verdict} data-firing={firing || undefined}>
      <Status tone={health.tone} label={env.name} size="sm" busy={health.busy} />
      {health.alerting ? (
        <span className="ml-1 text-xs text-ink-muted" data-testid={`app-env-${env.name}-alerts`}>
          <span className="sr-only">: </span>
          {health.label}
          <span className="sr-only"> · {release}</span>
        </span>
      ) : (
        <span className="sr-only">: {facts}</span>
      )}
    </span>
  );
}

function LastDeployCell({ app, deploysKnown }: { app: AppSummary; deploysKnown: boolean }) {
  if (!app.lastDeployedAt) return <span>{deploysKnown && isNeverDeployed(app) ? "Never" : "—"}</span>;
  return (
    <time dateTime={app.lastDeployedAt} title={formatAbsoluteTime(app.lastDeployedAt)} className="tabular-nums">
      {formatRelativeTime(app.lastDeployedAt)}
    </time>
  );
}

function NoApps({ onBuild }: { onBuild: () => void }) {
  return (
    <Card padding="lg" data-testid="apps-empty">
      <div className="flex flex-col items-start gap-3 sm:flex-row sm:items-center sm:justify-between">
        <div className="max-w-xl space-y-1">
          <p className="text-sm font-medium text-foreground">Nothing deployed yet.</p>
          <p className="text-sm text-muted-foreground">
            Describe an app in a chat. Reliant builds it with forge, deploys it to Reliant hosting, and it shows up here with
            the links to open it — one per environment.
          </p>
        </div>
        <Button onClick={onBuild} variant="primary" leftIcon={<Plus aria-hidden="true" />}>
          Build an app
        </Button>
      </div>
    </Card>
  );
}

/** Why the list may be short of the truth. Silent when it is the whole truth. */
function AvailabilityNote({ availability, detail }: { availability: string; detail: string }) {
  if (availability === "available" || availability === "no-control-plane") return null;
  const sentence =
    availability === "no-access"
      ? "Your role in this organization cannot read deployments, so apps are listed without their live links. An org admin can see them."
      : availability === "not-configured"
        ? "This control plane does not host apps, so none are listed."
        : `Reliant could not reach the control plane, so deployed apps are missing from this list.${detail ? ` ${detail}` : ""}`;
  return (
    <p className="rounded-lg border border-dashed border-border bg-card px-4 py-3 text-sm text-muted-foreground" role="status" data-testid="apps-availability">
      {sentence}
    </p>
  );
}
