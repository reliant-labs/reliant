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
import type { Timestamp } from "@bufbuild/protobuf/wkt";

import Button from "@/components/forge-ui/button";
import ProgressBar from "@/components/forge-ui/progress_bar";
import { day, gb } from "./observabilityBillingModel";
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
  orgName: string;
  status?: FreeHostingStatus;
  loading?: boolean;
  error?: unknown;
  onRetry?: () => void;
  /** Founder-only. dryRun=true previews the move; false performs it. */
  onMove: (dryRun: boolean) => Promise<MoveFreeHostingDesignationResponse>;
}

const heading = <h3 className="text-section text-foreground">Free static hosting</h3>;

export function FreeTierView({ orgName, status, loading, error, onRetry, onMove }: FreeTierViewProps) {
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
        <MoveAllowance orgName={orgName} status={status} onMove={onMove} />
      ) : (
        <p className="text-ui text-muted-foreground">
          This organization doesn&apos;t have a free hosting allowance. Hosting here needs billing.
        </p>
      )}
    </section>
  );
}

function Meter({ label, used, limit, value, max, note }: { label: string; used: string; limit: string; value: number; max: number; note?: string }) {
  return (
    <div className="space-y-1.5">
      <div className="flex items-baseline justify-between gap-3">
        <span className="text-ui font-medium text-foreground">{label}</span>
        <span className="text-ui tabular-nums text-muted-foreground">
          <span className="text-foreground">{used}</span> of {limit}
        </span>
      </div>
      <div className="forge-ui">
        <ProgressBar value={value} max={max} variant="default" ariaLabel={`${label} used against the free allowance`} />
      </div>
      {note && <p className="text-meta text-muted-foreground">{note}</p>}
    </div>
  );
}

function DesignatedAllowance({ status }: { status: FreeHostingStatus }) {
  const sitesLimit = Number(status.sitesLimit);
  return (
    <div className="space-y-4">
      <p className="text-ui text-foreground">This organization holds the free static hosting allowance.</p>
      <div className="grid gap-4 sm:grid-cols-3">
        <Meter
          label="Sites"
          used={String(status.sites.length)}
          limit={String(sitesLimit)}
          value={status.sites.length}
          max={sitesLimit}
        />
        <Meter
          label="Storage"
          used={status.storageMeasured === false ? "Not measured yet" : gb(status.storageBytes)}
          limit={gb(status.storageLimitBytes)}
          value={Number(status.storageBytes)}
          max={Number(status.storageLimitBytes)}
          note={`Up to ${status.siteStorageLimitBytes ? gb(status.siteStorageLimitBytes) : "1 GiB"} per site.`}
        />
        <Meter
          label="Transfer this month"
          used={gb(status.transferBytes)}
          limit={gb(status.transferLimitBytes)}
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
              <span className="tabular-nums text-muted-foreground">{site.storageMeasured ? gb(site.storageBytes) : "not measured yet"}</span>
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
  | { kind: "moved" };

function MoveAllowance({ orgName, status, onMove }: { orgName: string; status: FreeHostingStatus; onMove: FreeTierViewProps["onMove"] }) {
  const [phase, setPhase] = useState<MovePhase>({ kind: "idle" });
  const [problem, setProblem] = useState<string | null>(null);

  const preview = async () => {
    setProblem(null);
    setPhase({ kind: "previewing" });
    try {
      const response = await onMove(true);
      setPhase({ kind: "confirm", preview: response });
    } catch (error) {
      setPhase({ kind: "idle" });
      setProblem(moveErrorMessage(error, status.nextMoveAllowedAt));
    }
  };

  const confirm = async (pending: MoveFreeHostingDesignationResponse) => {
    setProblem(null);
    setPhase({ kind: "moving", preview: pending });
    try {
      const response = await onMove(false);
      if (response.moved) {
        setPhase({ kind: "moved" });
      } else {
        setPhase({ kind: "confirm", preview: pending });
        setProblem("The allowance wasn't moved. Nothing changed; try again.");
      }
    } catch (error) {
      setPhase({ kind: "confirm", preview: pending });
      setProblem(moveErrorMessage(error, status.nextMoveAllowedAt));
    }
  };

  if (phase.kind === "moved") {
    return (
      <p role="status" className="text-ui text-foreground" data-testid="free-tier-moved">
        Moved. {orgName ? `${orgName} now holds your free static hosting allowance.` : ""}
      </p>
    );
  }

  const confirming = phase.kind === "confirm" || phase.kind === "moving" ? phase.preview : null;

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
          {confirming.cooldownApplies && (
            <p className="text-ui text-muted-foreground" data-testid="free-tier-move-cooldown">
              You can move it again after {day(confirming.nextMoveAllowedAt)}.
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
              disabled={phase.kind === "moving"}
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

function moveErrorMessage(error: unknown, nextMoveAllowedAt?: Timestamp): string {
  if (error instanceof ConnectError) {
    if (error.code === Code.Unimplemented) return "Moving the allowance isn't available yet.";
    if (error.code === Code.FailedPrecondition)
      return nextMoveAllowedAt
        ? `You moved it recently; you can move it again after ${day(nextMoveAllowedAt)}.`
        : "You moved it recently; you can move it again once the 30-day cooldown ends.";
    if (error.code === Code.PermissionDenied) return "Only the founder of these organizations can move the free allowance.";
  }
  return "The allowance could not be moved. Nothing changed; try again.";
}
