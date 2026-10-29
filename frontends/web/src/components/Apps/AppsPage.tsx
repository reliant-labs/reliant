// Copyright (c) 2025 Reliant Labs

/**
 * /apps — everything the user has built and deployed, with the links to open
 * it. The sidebar's Apps entry lands here.
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

import { useEffect, useMemo, useRef, useState } from "react";
import { useNavigate, useSearch } from "@tanstack/react-router";
import { CircleCheck, Plus } from "lucide-react";

import Button from "@/components/forge-ui/button";
import Card from "@/components/forge-ui/card";
import DataTable, { type DataTableColumn } from "@/components/forge-ui/data_table";
import PageHeader from "@/components/forge-ui/page_header";
import Status from "@/components/forge-ui/status";
import { useApps } from "@/hooks/apps-queries";
import { useGoToBilling } from "@/hooks/useGoToBilling";
import { formatAbsoluteTime, formatRelativeTime } from "@/lib/relativeTime";
import { isDeployed, isLive, primaryLink, type AppEnv, type AppSummary } from "@/services/forge/apps";
import { verdictLabel } from "@/services/forge/topology";

import { consolePath } from "../Forge/Console/paths";
import { AreaShell } from "../Layout/AreaShell";
import { LiveLink, verdictTone } from "./appParts";
import { useStartBuildingApp } from "./startBuilding";

export function AppsPage() {
  return (
    <AreaShell areaPath="/apps" areaLabel="Apps" areaNoun="apps">
      <AppsBody />
    </AreaShell>
  );
}

function AppsBody() {
  const { apps, loading, availability, detail } = useApps();
  const startBuilding = useStartBuildingApp();
  const claimed = useClaimedApp(apps.map((a) => a.name), loading);

  const live = apps.filter(isLive).length;
  const header = (
    <PageHeader
      title="Apps"
      meta={
        loading ? undefined : apps.length === 0 ? (
          <span>What you build and deploy with Reliant, with the links to open it.</span>
        ) : (
          <span data-testid="apps-counts">
            {apps.length} {apps.length === 1 ? "app" : "apps"}
            {live > 0 && ` · ${live} live`}
          </span>
        )
      }
      actions={[{ label: "Build an app", onClick: startBuilding, variant: "primary", icon: <Plus className="h-4 w-4" aria-hidden="true" /> }]}
      className="mb-0"
    />
  );

  return (
    <div className="space-y-5" data-testid="apps-page">
      {header}
      {claimed.name && <ClaimedBanner name={claimed.name} listed={claimed.found} loading={loading} paused={claimed.paused} />}
      <AvailabilityNote availability={availability} detail={detail} />
      {!loading && apps.length === 0 ? (
        <NoApps onBuild={startBuilding} />
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
      render: (_, { app }) => <EnvsCell app={app} />,
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
  const why = !deploysKnown ? "Unavailable" : isDeployed(app) ? "No public site" : "Not deployed yet";
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
function EnvsCell({ app }: { app: AppSummary }) {
  if (app.envs.length === 0 && app.previews === 0) return <span className="text-ink-muted">—</span>;
  return (
    <span className="inline-flex items-center gap-3">
      {app.envs.map((env) => (
        <EnvChip key={env.id} env={env} />
      ))}
      {app.previews > 0 && (
        <span className="text-xs text-ink-muted">
          +{app.previews} preview{app.previews === 1 ? "" : "s"}
        </span>
      )}
    </span>
  );
}

function EnvChip({ env }: { env: AppEnv }) {
  if (env.loading) {
    return (
      <span className="inline-flex items-center gap-1.5 text-ink-muted" data-testid={`app-env-${env.name}`}>
        <span className="h-1.5 w-1.5 animate-pulse rounded-full bg-ink/20 motion-reduce:animate-none" aria-hidden="true" />
        {env.name}
      </span>
    );
  }
  const facts = [verdictLabel(env.verdict), env.release || "not deployed yet"].join(" · ");
  return (
    <span title={`${env.name}: ${facts}`} data-testid={`app-env-${env.name}`} data-verdict={env.verdict}>
      <Status tone={verdictTone(env.verdict)} label={env.name} size="sm" />
      <span className="sr-only">: {facts}</span>
    </span>
  );
}

function LastDeployCell({ app, deploysKnown }: { app: AppSummary; deploysKnown: boolean }) {
  if (!app.lastDeployedAt) return <span>{deploysKnown ? "Never" : "—"}</span>;
  return (
    <time dateTime={app.lastDeployedAt} title={formatAbsoluteTime(app.lastDeployedAt)} className="tabular-nums">
      {formatRelativeTime(app.lastDeployedAt)}
    </time>
  );
}

/**
 * What a finished claim did, said where its result is. The site is already in
 * the user's own org by the time this shows; the list below may still be
 * catching up, which the banner says rather than leaving the user to wonder.
 */
function ClaimedBanner({
  name,
  listed,
  loading,
  paused,
}: {
  name: string;
  listed: boolean;
  loading: boolean;
  paused: PausedSite | null;
}) {
  const goToBilling = useGoToBilling("upgrade");
  return (
    <p
      className="flex items-start gap-2 rounded-lg border border-border bg-card px-4 py-3 text-sm text-foreground"
      role="status"
      data-testid="apps-claimed"
    >
      <CircleCheck className="mt-0.5 h-4 w-4 shrink-0 text-success-ink" aria-hidden="true" />
      <span>
        <strong className="font-semibold">{name}</strong> is yours now.
        {!loading && !listed && " It can take a moment to appear in this list."}
        {paused && (
          <span data-testid="apps-claimed-paused">
            {" "}
            {paused.kept === "claimed" ? "You kept this site live, so " : "You kept your existing site live, so "}
            <strong className="font-semibold">{paused.name}</strong> is paused: the free plan includes one static site.{" "}
            <button type="button" onClick={goToBilling} className="font-medium underline underline-offset-2">
              Subscribe
            </button>{" "}
            to run both.
          </span>
        )}
      </span>
    </p>
  );
}

/**
 * The app a finished claim just made the user's own (`/apps?claimed=<project>`,
 * set by components/Claim/useClaimFlow.ts). Remembered, then dropped from the
 * URL, so a reload or a shared link does not highlight it again. Holds off
 * until the list has loaded: the claim moved the site into the user's org, and
 * the list is what shows it.
 */
function useClaimedApp(names: string[], loading: boolean): { name: string | null; found: boolean; paused: PausedSite | null } {
  const navigate = useNavigate();
  const { claimed, pausedSite, kept } = useSearch({ strict: false }) as {
    claimed?: string;
    pausedSite?: string;
    kept?: "claimed" | "existing";
  };
  const [name, setName] = useState<string | null>(null);
  const [paused, setPaused] = useState<PausedSite | null>(null);

  useEffect(() => {
    if (!claimed) return;
    setName(claimed);
    setPaused(pausedSite && kept ? { name: pausedSite, kept } : null);
    void navigate({ to: "/apps", search: {}, replace: true });
  }, [claimed, pausedSite, kept, navigate]);

  return { name, found: !loading && name !== null && names.includes(name), paused };
}

interface PausedSite {
  /** The site that is paused. */
  name: string;
  /** Which one the person kept live. */
  kept: "claimed" | "existing";
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
