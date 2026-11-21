// Copyright (c) 2026 Reliant Labs

/**
 * The free static hosting allowance: whether this org holds it, what it has
 * used, and — for the founder only — moving it here from another org.
 *
 * ── ONE ALLOWANCE PER FOUNDER, MOVED DELIBERATELY ──────────────────────────
 *
 * The allowance belongs to a person and is designated to one of the orgs they
 * founded. Moving it stops the old org's sites being served free, and a move
 * starts a 30-day cooldown, so the move is always previewed first (a dry run
 * naming every site that would stop) and only an explicit confirmation sends
 * the real one.
 *
 * ── NEVER REVEAL ANOTHER ORG ───────────────────────────────────────────────
 *
 * The server names the org holding the allowance only to its founder. Anyone
 * else sees that this org has no allowance, nothing about where it is.
 */

import { useState } from "react";
import { Code, ConnectError } from "@connectrpc/connect";

import Button from "@/components/forge-ui/button";
import ProgressBar from "@/components/forge-ui/progress_bar";
import { day } from "./observabilityBillingModel";
import { hasErrorReason } from "@/lib/errorReasons";
import {
  isUnimplemented,
  useFreeHostingStatus,
  useMoveFreeHostingDesignation,
  type FreeHostingStatus,
  type MoveFreeHostingDesignationResponse,
} from "./hostingBillingQueries";

export interface FreeTierCardProps {
  orgId: string;
  orgName: string;
}

export function FreeTierCard({ orgId, orgName }: FreeTierCardProps) {
  const status = useFreeHostingStatus(orgId);
  const move = useMoveFreeHostingDesignation(orgId);
  return (
    <FreeTierView
      orgId={orgId}
      orgName={orgName}
      status={status.data}
      loading={status.isLoading}
      error={status.error}
      onRetry={() => void status.refetch()}
      onMove={(dryRun) => move.mutateAsync(dryRun)}
    />
  );
}

export interface FreeTierViewProps {
  /**
   * The org the card is for. A move's preview and confirmation belong to one
   * org: switching orgs starts the move over, so a preview fetched for one org
   * can never confirm a move to another.
   */
  orgId?: string;
  orgName: string;
  status?: FreeHostingStatus;
  loading?: boolean;
  error?: unknown;
  onRetry?: () => void;
  /** Founder-only. dryRun=true previews the move; false performs it. */
  onMove: (dryRun: boolean) => Promise<MoveFreeHostingDesignationResponse>;
}

const heading = <h3 className="text-section text-foreground">Free static hosting</h3>;

export function FreeTierView({ orgId, orgName, status, loading, error, onRetry, onMove }: FreeTierViewProps) {
  if (loading) {
    return (
      <section className="space-y-3" data-testid="free-tier-card">
        {heading}
        <p role="status" className="text-ui text-muted-foreground">
          Loading free tier status…
        </p>
      </section>
    );
  }

  if (isUnimplemented(error)) {
    return (
      <section className="space-y-3" data-testid="free-tier-card">
        {heading}
        <p role="status" className="text-ui text-muted-foreground">
          Free tier status isn&apos;t available on this server yet.
        </p>
      </section>
    );
  }

  if (error || !status) {
    return (
      <section className="space-y-3" data-testid="free-tier-card">
        {heading}
        <div role="alert" className="space-y-3">
          <p className="text-ui text-foreground">Could not load free tier status. This isn&apos;t a sign the allowance changed.</p>
          {onRetry && (
            <Button size="sm" variant="outline" onClick={onRetry}>
              Try again
            </Button>
          )}
        </div>
      </section>
    );
  }

  return (
    <section className="space-y-4" data-testid="free-tier-card">
      {heading}
      {status.isDesignated ? (
        <DesignatedAllowance status={status} />
      ) : status.callerIsFounder ? (
        <MoveAllowance key={orgId} orgName={orgName} status={status} onMove={onMove} />
      ) : (
        <p className="text-ui text-muted-foreground">
          This organization doesn&apos;t have a free hosting allowance. Hosting here needs billing.
        </p>
      )}
    </section>
  );
}

function Meter({ label, used, limit, value, max, note }: { label: string; used: string; limit: string; value?: number; max: number; note?: string }) {
  return (
    <div className="space-y-1.5">
      <div className="flex items-baseline justify-between gap-3">
        <span className="text-ui font-medium text-foreground">{label}</span>
        <span className="text-ui tabular-nums text-muted-foreground">
          <span className="text-foreground">{used}</span>
          {value === undefined ? ` · ${limit} allowance` : ` of ${limit}`}
        </span>
      </div>
      {value !== undefined && (
        <div className="forge-ui">
          <ProgressBar value={value} max={max} variant="default" ariaLabel={`${label} used against the free allowance`} />
        </div>
      )}
      {note && <p className="text-meta text-muted-foreground">{note}</p>}
    </div>
  );
}

function gib(bytes: number | bigint): string {
  return `${(Number(bytes) / 2 ** 30).toLocaleString(undefined, { maximumFractionDigits: 3 })} GiB`;
}

function DesignatedAllowance({ status }: { status: FreeHostingStatus }) {
  const sitesLimit = Number(status.sitesLimit);
  const siteCount = status.sites.length;
  const perSiteLimit = status.siteStorageLimitBytes ?? 0;
  // Older servers may omit the measured flag; an explicit false always means unknown.
  const storageMeasured = status.storageMeasured !== false;
  return (
    <div className="space-y-4">
      <p className="text-ui text-foreground">This organization holds the free static hosting allowance.</p>
      <div className="grid gap-4 sm:grid-cols-3">
        {sitesLimit > 0 ? (
          <Meter label="Sites" used={String(siteCount)} limit={String(sitesLimit)} value={siteCount} max={sitesLimit} />
        ) : (
          <div className="flex items-baseline justify-between gap-3">
            <span className="text-ui font-medium text-foreground">Sites</span>
            <span className="text-ui tabular-nums text-foreground">{siteCount}</span>
          </div>
        )}
        <Meter
          label="Storage"
          used={storageMeasured ? gib(status.storageBytes) : "Not measured yet"}
          limit={gib(status.storageLimitBytes)}
          value={storageMeasured ? Number(status.storageBytes) : undefined}
          max={Number(status.storageLimitBytes)}
          note={perSiteLimit > 0 ? `Up to ${gib(perSiteLimit)} per site.` : undefined}
        />
        <Meter
          label="Transfer this month"
          used={gib(status.transferBytes)}
          limit={gib(status.transferLimitBytes)}
          value={Number(status.transferBytes)}
          max={Number(status.transferLimitBytes)}
          note={status.periodEnd ? `Resets ${day(status.periodEnd)}.` : undefined}
        />
      </div>
      {status.sites.length === 0 ? (
        <p className="text-ui text-muted-foreground">No sites are using the allowance yet.</p>
      ) : (
        <ul className="divide-y divide-border-subtle" data-testid="free-tier-sites">
          {status.sites.map((site) => (
            <li key={`${site.deploymentId}/${site.environmentName}`} className="flex flex-wrap items-baseline justify-between gap-x-3 py-2 text-ui">
              <span className="min-w-0">
                <span className="text-foreground">{site.name}</span>
                <span className="ml-2 text-meta text-muted-foreground">{site.environmentName}</span>
                {site.url && (
                  <a href={site.url} target="_blank" rel="noopener noreferrer" className="ml-3 break-all text-primary-ink hover:underline">
                    {site.url}
                  </a>
                )}
              </span>
              <span className="tabular-nums text-muted-foreground">{site.storageMeasured ? gib(site.storageBytes) : "not measured yet"}</span>
            </li>
          ))}
        </ul>
      )}
    </div>
  );
}

type MovePhase =
  | { kind: "idle" }
  | { kind: "previewing" }
  | { kind: "confirm"; preview: MoveFreeHostingDesignationResponse }
  | { kind: "moving"; preview: MoveFreeHostingDesignationResponse }
  | { kind: "moved" }
  | { kind: "already" };

function MoveAllowance({ orgName, status, onMove }: { orgName: string; status: FreeHostingStatus; onMove: FreeTierViewProps["onMove"] }) {
  const [phase, setPhase] = useState<MovePhase>({ kind: "idle" });
  const [problem, setProblem] = useState<string | null>(null);

  const preview = async () => {
    setProblem(null);
    setPhase({ kind: "previewing" });
    try {
      const response = await onMove(true);
      // The org asked for already holds it: nothing to move, and no error.
      if (response.fromOrgId !== "" && response.fromOrgId === response.toOrgId) setPhase({ kind: "already" });
      else setPhase({ kind: "confirm", preview: response });
    } catch (error) {
      setPhase({ kind: "idle" });
      setProblem(moveErrorMessage(error, orgName));
    }
  };

  const confirm = async (pending: MoveFreeHostingDesignationResponse) => {
    setProblem(null);
    setPhase({ kind: "moving", preview: pending });
    try {
      const response = await onMove(false);
      // moved=false without an error: this org already holds it.
      setPhase({ kind: response.moved ? "moved" : "already" });
    } catch (error) {
      setPhase({ kind: "confirm", preview: pending });
      setProblem(moveErrorMessage(error, orgName));
    }
  };

  if (phase.kind === "moved") {
    return (
      <p role="status" className="text-ui text-foreground" data-testid="free-tier-moved">
        Moved. {orgName ? `${orgName} now holds your free static hosting allowance.` : ""}
      </p>
    );
  }

  if (phase.kind === "already") {
    return (
      <p role="status" className="text-ui text-foreground" data-testid="free-tier-already">
        {orgName ? `${orgName} already holds` : "This organization already holds"} your free static hosting allowance. Nothing was moved.
      </p>
    );
  }

  const confirming = phase.kind === "confirm" || phase.kind === "moving" ? phase.preview : null;
  // cooldown_applies is the server's verdict; next_move_allowed_at alone can be
  // set on a move that is exempt from the cooldown.
  const blocked = confirming?.cooldownApplies ?? false;

  return (
    <div className="space-y-3">
      <p className="text-ui text-foreground">
        {status.heldByOrgName
          ? `Your free allowance is held by ${status.heldByOrgName}.`
          : "Your free allowance isn't assigned to this organization."}
      </p>
      {!confirming && (
        <Button size="sm" variant="outline" onClick={() => void preview()} loading={phase.kind === "previewing"} data-testid="free-tier-move">
          Make this my free hosting org
        </Button>
      )}
      {confirming && (
        <div className="space-y-3 rounded-xl border border-border/60 bg-background p-4" data-testid="free-tier-move-confirm">
          <p className="text-ui font-medium text-foreground">Move your free allowance{orgName ? ` to ${orgName}` : " here"}?</p>
          {confirming.stoppingSites.length > 0 ? (
            <div className="space-y-1">
              <p className="text-ui text-foreground">These sites stop being served free:</p>
              <ul className="divide-y divide-border-subtle">
                {confirming.stoppingSites.map((site) => (
                  <li key={`${site.deploymentId}/${site.env}/${site.name}`} className="flex flex-wrap items-baseline gap-x-2 py-1.5 text-ui">
                    <span className="text-foreground">{site.name}</span>
                    <span className="text-meta text-muted-foreground">
                      {[site.project, site.env].filter(Boolean).join(" · ")}
                    </span>
                    {site.url && <span className="break-all text-meta text-muted-foreground">{site.url}</span>}
                  </li>
                ))}
              </ul>
            </div>
          ) : (
            <p className="text-ui text-muted-foreground">No sites stop being served free.</p>
          )}
          {blocked ? (
            <p className="text-ui text-foreground" data-testid="free-tier-move-cooldown">
              You moved your free allowance in the last 30 days, so it can&apos;t move yet
              {confirming.nextMoveAllowedAt ? `. You can move it after ${day(confirming.nextMoveAllowedAt)}.` : "."}
            </p>
          ) : (
            <p className="text-meta text-muted-foreground" data-testid="free-tier-move-cooldown-note">
              The allowance can usually move once every 30 days.
            </p>
          )}
          {problem && (
            <p role="alert" className="text-meta text-destructive-ink">
              {problem}
            </p>
          )}
          <div className="flex gap-2">
            <Button
              size="sm"
              variant="primary"
              onClick={() => void confirm(confirming)}
              loading={phase.kind === "moving"}
              disabled={phase.kind === "moving" || blocked}
            >
              Move allowance
            </Button>
            <Button
              size="sm"
              variant="ghost"
              disabled={phase.kind === "moving"}
              onClick={() => {
                setProblem(null);
                setPhase({ kind: "idle" });
              }}
            >
              Cancel
            </Button>
          </div>
        </div>
      )}
      {problem && !confirming && (
        <p role="alert" className="text-meta text-destructive-ink">
          {problem}
        </p>
      )}
    </div>
  );
}

/** The RFC3339 date a free_hosting_move_cooldown refusal names in its message, as YYYY-MM-DD. */
export function cooldownEndsFrom(message: string): string | null {
  const match = /\d{4}-\d{2}-\d{2}T[\d:.]+(?:Z|[+-]\d{2}:\d{2})/.exec(message);
  if (!match) return null;
  const date = new Date(match[0]);
  return Number.isNaN(date.getTime()) ? null : date.toISOString().slice(0, 10);
}

/** One sentence per refusal the server names (its reason codes), never its prose. */
export function moveErrorMessage(error: unknown, orgName: string): string {
  const target = orgName || "this organization";
  if (hasErrorReason(error, "not_org_member")) return `You're not a member of ${target}, so your free allowance can't move to it.`;
  if (hasErrorReason(error, "free_hosting_founder_only"))
    return "Only the founder who owns the free allowance can move it. Ask them to make this their free hosting organization.";
  if (hasErrorReason(error, "free_hosting_target_billed"))
    return `${target} already pays for app hosting, so it doesn't need the free allowance. It stays where it is.`;
  if (hasErrorReason(error, "free_hosting_move_cooldown")) {
    const ends = error instanceof ConnectError ? cooldownEndsFrom(error.rawMessage) : null;
    return ends
      ? `You moved your free allowance in the last 30 days. You can move it again after ${ends}.`
      : "You moved your free allowance in the last 30 days. You can move it again once 30 days have passed.";
  }
  if (error instanceof ConnectError) {
    if (error.code === Code.Unimplemented) return "Moving the allowance isn't available yet.";
    if (error.code === Code.PermissionDenied) return "You don't have permission to move the free allowance.";
  }
  return "The allowance could not be moved. Nothing changed; try again.";
}
