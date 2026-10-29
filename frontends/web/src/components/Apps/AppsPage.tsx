// Copyright (c) 2025 Reliant Labs

/**
 * /apps — everything the user has built and deployed, with the links to open
 * it. The sidebar's Apps entry lands here.
 *
 * An app is a forge project (services/forge/apps.ts). Each card leads with
 * the app's live frontend, then one row per long-lived environment — its live
 * URLs, its health, and its last deploy — and opens the app's console for
 * everything else (releases, observability, secrets, domains).
 *
 * The page answers "what have I shipped, and where is it?", so a user who has
 * shipped nothing is offered the way to: a chat that builds an app with forge
 * and deploys it.
 */

import { useEffect, useRef, useState } from "react";
import { useNavigate, useSearch } from "@tanstack/react-router";
import { ArrowRight, Boxes, CircleCheck, ExternalLink, Plus } from "lucide-react";

import Button from "@/components/forge-ui/button";
import Card, { CardInset } from "@/components/forge-ui/card";
import PageHeader from "@/components/forge-ui/page_header";
import SkeletonLoader from "@/components/forge-ui/skeleton_loader";
import { useApps } from "@/hooks/apps-queries";
import { useGoToBilling } from "@/hooks/useGoToBilling";
import { formatRelativeTime } from "@/lib/relativeTime";
import { isLive, primaryLink, type AppSummary } from "@/services/forge/apps";

import { consolePath } from "../Forge/Console/paths";
import { AreaShell } from "../Layout/AreaShell";
import { AppEnvRow } from "./appParts";
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

  let body: React.ReactNode;
  if (loading) {
    body = (
      <Card padding="lg" aria-busy="true">
        <SkeletonLoader variant="text" count={4} />
      </Card>
    );
  } else if (apps.length === 0) {
    body = <NoApps onBuild={startBuilding} />;
  } else {
    body = (
      <div className="space-y-4" data-testid="apps-list">
        {apps.map((app) => (
          <AppCard
            key={app.name}
            app={app}
            deploysKnown={availability === "available"}
            highlighted={app.name === claimed.name}
          />
        ))}
      </div>
    );
  }

  return (
    <div className="space-y-5" data-testid="apps-page">
      {header}
      {claimed.name && (
        <ClaimedBanner name={claimed.name} listed={claimed.found} loading={loading} overAllowance={claimed.overAllowance} />
      )}
      <AvailabilityNote availability={availability} detail={detail} />
      {body}
    </div>
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
  overAllowance,
}: {
  name: string;
  listed: boolean;
  loading: boolean;
  overAllowance: boolean;
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
        {overAllowance && (
          <span data-testid="apps-claimed-over-allowance">
            {" "}
            You already have a free static site, and the free plan includes one: Reliant keeps your oldest live and pauses
            the other until you{" "}
            <button type="button" onClick={goToBilling} className="font-medium underline underline-offset-2">
              subscribe
            </button>{" "}
            or delete a site.
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
function useClaimedApp(names: string[], loading: boolean): { name: string | null; found: boolean; overAllowance: boolean } {
  const navigate = useNavigate();
  const { claimed, overAllowance: over } = useSearch({ strict: false }) as { claimed?: string; overAllowance?: boolean };
  const [name, setName] = useState<string | null>(null);
  const [overAllowance, setOverAllowance] = useState(false);

  useEffect(() => {
    if (!claimed) return;
    setName(claimed);
    setOverAllowance(over === true);
    void navigate({ to: "/apps", search: {}, replace: true });
  }, [claimed, over, navigate]);

  return { name, found: !loading && name !== null && names.includes(name), overAllowance };
}

/**
 * `deploysKnown` is false when the control plane could not be read: the card
 * then cannot say "not deployed yet", only that it does not know.
 */
function AppCard({ app, deploysKnown, highlighted }: { app: AppSummary; deploysKnown: boolean; highlighted: boolean }) {
  const anchor = useRef<HTMLDivElement>(null);
  useEffect(() => {
    if (highlighted) anchor.current?.scrollIntoView({ block: "center", behavior: "smooth" });
  }, [highlighted]);
  const primary = primaryLink(app);
  const consoleHref = consolePath(app.name, { kind: "overview" });
  return (
    <div ref={anchor}>
    <Card
      padding="md"
      data-testid={`app-card-${app.name}`}
      data-highlighted={highlighted ? "true" : undefined}
      className={highlighted ? "ring-2 ring-primary" : undefined}
    >
      <div className="flex flex-wrap items-start justify-between gap-3">
        <div className="min-w-0">
          <div className="flex items-center gap-2">
            <Boxes className="h-4 w-4 shrink-0 text-muted-foreground" aria-hidden="true" />
            <h2 className="truncate text-base font-semibold text-foreground">{app.name}</h2>
            {isLive(app) && (
              <span className="rounded bg-success/15 px-1.5 py-0.5 text-2xs font-medium uppercase tracking-wider text-success-ink">Live</span>
            )}
          </div>
          <p className="mt-0.5 text-xs text-muted-foreground">
            {app.lastDeployedAt
              ? `Last deployed ${formatRelativeTime(app.lastDeployedAt)}`
              : deploysKnown
                ? "Not deployed yet"
                : "Deployments unavailable"}
            {app.previews > 0 && ` · ${app.previews} preview${app.previews === 1 ? "" : "s"}`}
          </p>
        </div>
        <div className="flex shrink-0 items-center gap-2">
          {primary && (
            <Button
              href={primary.url}
              target="_blank"
              rel="noreferrer"
              size="sm"
              variant="primary"
              rightIcon={<ExternalLink aria-hidden="true" />}
              data-testid="app-open-live"
            >
              Open app
            </Button>
          )}
          <Button href={consoleHref} size="sm" variant="outline" rightIcon={<ArrowRight aria-hidden="true" />} data-testid="app-open-console">
            Console
          </Button>
        </div>
      </div>

      {app.envs.length > 0 ? (
        <CardInset className="mt-4 space-y-3" padding="sm">
          {app.envs.map((env) => (
            <AppEnvRow key={env.id} env={env} />
          ))}
        </CardInset>
      ) : !deploysKnown ? null : (
        <p className="mt-3 text-sm text-muted-foreground">
          Its live links appear here once it is deployed. Ask the agent in a chat on this project to deploy it, or run{" "}
          <code className="rounded bg-background px-1 py-0.5 font-mono text-xs text-foreground">reliant forge env deploy prod</code> in its
          checkout.
        </p>
      )}
    </Card>
    </div>
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
