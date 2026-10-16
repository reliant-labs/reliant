// Copyright (c) 2025 Reliant Labs

/**
 * The bodies of an environment page's tabs. See envTabs.ts for which
 * environment gets which, and which tabs read the daemon.
 *
 * Every panel is built from existing pieces — LiveState, LiveWorkloads,
 * LiveReleases, LiveSecretsSection, DevStackPanel, RegisterEnvPanel —
 * re-grouped by the question they
 * answer. Sections are forge-ui Cards with a CardHeader, rather than three
 * hand-rolled copies of the same heading-and-rule.
 */

import { lazy, Suspense, useState, type ReactNode } from "react";
import { Play, RefreshCw } from "lucide-react";

import Card, { CardHeader } from "@/components/forge-ui/card";
import SkeletonLoader from "@/components/forge-ui/skeleton_loader";
import { Button } from "@/components/ui/Button";
import type { DomainsState } from "@/hooks/forge-domain-queries";
import type { CloudEnvStatus, CloudPromotion } from "@/services/forge/cloudEnvs";
import { declaredNotBuilt, isPlacedKind, neverBuilt, type LiveConvergence, type LiveEnv } from "@/services/forge/live";
import { devStackRunsHere, type DaemonSide } from "@/services/forge/environments";
import type { ForgeEnvStatusReport } from "@/services/forge/status";
import type { ForgeOutcome, ForgeTopologyEnv } from "@/services/forge/topology";
import { isChunkLoadError, staleChunkModule } from "@/lib/lazyRoute";

import { formatTimestamp } from "../Overview/EnvironmentTable";
import { RegisterEnvPanel } from "../Preview/RegisterEnvPanel";
import { DevStackPanel } from "../Status/DevStackPanel";
import { DaemonNeeded } from "./DaemonNeeded";
import { EnvDomains } from "./EnvDomains";
import { LiveActivity } from "./LiveActivity";
import { LiveReleases } from "./LiveReleases";
import { LiveSecretsSection } from "./LiveSecretsSection";
import { LiveState } from "./LiveState";
import { LiveWorkloads } from "./LiveWorkloads";
import type { ObsNavigate, ObsSearch } from "../Observability/obsSearch";

// The whole Observability area — uPlot, TanStack Table, the waterfall, every
// view — is its own chunk, fetched the first time the tab opens. A chunk gone
// after a deploy resolves to lazyRoute's reload-once screen, never a reload
// from inside render.
const ObservabilityPanel = lazy(() =>
  import("../Observability/ObservabilityPanel").catch((error: unknown) => {
    if (!isChunkLoadError(error)) throw error;
    return staleChunkModule(error, "default") as unknown as typeof import("../Observability/ObservabilityPanel");
  })
);

/** A titled section. */
export function Panel({
  title,
  description,
  actions,
  testId,
  children,
  flush = false,
}: {
  title: ReactNode;
  description?: ReactNode;
  actions?: ReactNode;
  testId: string;
  children: ReactNode;
  /** No body padding — for a table or list that draws its own rows. */
  flush?: boolean;
}) {
  return (
    <Card padding="none" data-testid={testId}>
      <CardHeader title={title} description={description} actions={actions} />
      <div className={flush ? "" : "p-4"}>{children}</div>
    </Card>
  );
}

function PanelSkeleton({ testId, rows = 3 }: { testId: string; rows?: number }) {
  return (
    <Card padding="md" data-testid={testId} aria-busy="true">
      <SkeletonLoader variant="list-item" count={rows} />
    </Card>
  );
}

// ── Observability (control plane) ───────────────────────────────────────────

export function ObservabilityTab({
  envName,
  live,
  liveLoading,
  search,
  navigate,
  onWiden,
}: {
  envName: string;
  live: LiveEnv | null;
  liveLoading: boolean;
  search: ObsSearch;
  navigate: ObsNavigate;
  /** The project's Observability area, carrying this view: the env tab is a pre-filtered deep link into it. */
  onWiden?: (search: ObsSearch) => void;
}) {
  if (liveLoading && !live) return <PanelSkeleton testId="obs-tab-loading" rows={4} />;
  // Telemetry is filed under the control plane's environment id (the env_id
  // its telemetry token carries), so an environment it has no record of has
  // nothing filed under it yet.
  if (!live) {
    return (
      <p
        data-testid="obs-unregistered"
        className="rounded-lg border border-dashed border-border bg-background px-4 py-5 text-sm text-muted-foreground"
      >
        Reliant has no record of <span className="font-mono text-foreground">{envName}</span>, so no telemetry is
        filed under it. Register it from the Overview tab; once it is deployed through Reliant, its logs, traces and
        metrics appear here.
      </p>
    );
  }
  return (
    <Suspense fallback={<PanelSkeleton testId="obs-tab-loading" rows={4} />}>
      <ObservabilityPanel place={{ kind: "env", env: { id: live.id, name: envName, declared: live.declaredShape }, onWiden }} search={search} navigate={navigate} />
    </Suspense>
  );
}

// ── Overview (deployed env) ─────────────────────────────────────────────────

export interface OverviewTabProps {
  envName: string;
  live: LiveEnv | null;
  liveLoading: boolean;
  status: CloudEnvStatus | undefined;
  statusLoading: boolean;
  statusError: Error | null;
  /** forge's own ledger row for this env, when the daemon answered. */
  forgeEnv: ForgeTopologyEnv | null;
  daemon: DaemonSide;
  projectId: string | null;
  forgeProject: string | null;
  /** The org's domains; this tab shows the ones bound here. */
  domains: DomainsState | undefined;
  domainsLoading: boolean;
  onOpenDomains: () => void;
  onManageDomains: () => void;
}

export function OverviewTab(props: OverviewTabProps) {
  const { live, envName } = props;
  // Sticky for this mount. A successful Register makes the control plane hold
  // the env, so `live` turns non-null and the register panel would VANISH on
  // the very render that should confirm it worked.
  const [registered, setRegistered] = useState(false);

  if (props.liveLoading && !live) return <PanelSkeleton testId="env-overview-loading" rows={4} />;

  // ── No record in Reliant. Not "never built": forge's own ledger may well
  // have this env on a release (control-plane's prod is on v1.7.15 by its
  // file ledger and has no row here). Say what Reliant lacks, show what forge
  // knows, and offer the one action that fixes it.
  if (!live) {
    return (
      <div className="space-y-4" data-testid="env-overview-unregistered">
        <Panel
          title="Not registered in Reliant"
          description="Reliant has no record of this environment, so its releases and secrets aren't tracked here yet. Register it from your checkout — after that it shows here with your daemon offline."
          testId="env-register-panel"
        >
          {props.daemon === "ok" ? (
            <RegisterEnvPanel
              projectId={props.projectId}
              envName={envName}
              forgeProject={props.forgeProject}
              declaredHere={!!props.forgeEnv}
              onRegistered={() => setRegistered(true)}
            />
          ) : props.daemon === "loading" ? (
            <SkeletonLoader variant="form-field" />
          ) : (
            <DaemonNeeded what={`the shape forge renders for ${envName}, which registering records`} />
          )}
        </Panel>
        {props.forgeEnv && <ForgeLedgerFacts env={props.forgeEnv} />}
      </div>
    );
  }

  return (
    <div className="space-y-4" data-testid="live-section" data-kind={live.kind}>
      {registered && (
        <p data-testid="register-done" className="rounded-lg border border-border bg-card px-4 py-3 text-sm text-foreground">
          Registered. <span className="font-mono">{envName}</span> is tracked in Reliant now — its secrets can be set
          from the Secrets tab, with your daemon offline.
        </p>
      )}
      <Panel title="State" testId="env-state">
        {declaredNotBuilt(live) ? (
          <div className="space-y-2">
            <p data-testid="live-declared-not-built" className="text-sm text-muted-foreground">
              Declared, not built yet. Its secrets can be set; a build records the first release.
            </p>
            {live.provenance !== "" && (
              <p data-testid="live-provenance" className="font-mono text-xs text-muted-foreground">
                {live.provenance}
              </p>
            )}
          </div>
        ) : neverBuilt(live) ? (
          <p data-testid="live-row-blank" className="text-sm text-muted-foreground">
            Registered, with nothing recorded yet. <code className="font-mono text-foreground">forge env build {live.name} --release</code>{" "}
            records the first release.
          </p>
        ) : (
          <div className="space-y-3">
            <LiveState env={live} />
            {/* THE PROVENANCE LINE (§2.1): images from the release's source,
                config from the render's. Descriptive, not approving. */}
            {live.provenance !== "" && (
              <p data-testid="live-provenance" className="font-mono text-xs text-muted-foreground">
                {live.provenance}
              </p>
            )}
          </div>
        )}
      </Panel>

      <Panel
        title="Workloads"
        description={
          isPlacedKind(live.kind)
            ? "Observed by Reliant."
            : "What this environment's configuration declares — Reliant does not place workloads on your own cluster."
        }
        testId="section-workloads"
      >
        <LiveWorkloads env={live} status={props.status} isLoading={props.statusLoading} error={props.statusError} />
      </Panel>

      {/* A summary of what serves this env; the Domains tab has the full
          list. Omitted for an env the platform does not run — no domain can
          bind there, and an always-empty panel is noise. */}
      {isPlacedKind(live.kind) && (
        <Panel
          title="Domains"
          actions={
            <Button variant="ghost" size="sm" onClick={props.onOpenDomains} data-testid="open-domains">
              All domains
            </Button>
          }
          testId="env-overview-domains"
        >
          <EnvDomains
            environmentId={live.id}
            placed
            state={props.domains}
            isLoading={props.domainsLoading}
            limit={3}
            onManage={props.onManageDomains}
          />
        </Panel>
      )}
    </div>
  );
}

/** What forge's own (file) ledger says, for an env Reliant has no record of. */
function ForgeLedgerFacts({ env }: { env: ForgeTopologyEnv }) {
  return (
    <Panel
      title="In forge's ledger"
      description="From your checkout's forge ledger, read through your daemon. Register to track it here."
      testId="env-forge-ledger"
    >
      {env.release ? (
        <dl className="grid grid-cols-[auto_1fr] gap-x-6 gap-y-1.5 text-sm">
          <dt className="text-muted-foreground">Release</dt>
          <dd className="font-mono text-foreground">{env.release}</dd>
          {env.promoted_at && (
            <>
              <dt className="text-muted-foreground">Promoted</dt>
              <dd className="text-foreground">{formatTimestamp(env.promoted_at)}</dd>
            </>
          )}
          {env.kube_context && (
            <>
              <dt className="text-muted-foreground">Cluster</dt>
              <dd className="font-mono text-foreground">
                {env.kube_context}
                {env.namespace ? ` / ${env.namespace}` : ""}
              </dd>
            </>
          )}
        </dl>
      ) : (
        <p className="text-sm text-muted-foreground">Never promoted in forge&apos;s ledger.</p>
      )}
    </Panel>
  );
}

// ── Releases ────────────────────────────────────────────────────────────────

export function ReleasesTab({
  live,
  promotions,
  isLoading,
  error,
  forgeEnv,
}: {
  live: LiveEnv | null;
  promotions: CloudPromotion[] | undefined;
  isLoading: boolean;
  error: Error | null;
  forgeEnv: ForgeTopologyEnv | null;
}) {
  if (!live) {
    return forgeEnv ? (
      <ForgeLedgerFacts env={forgeEnv} />
    ) : (
      <p className="text-sm text-muted-foreground" data-testid="releases-unregistered">
        Reliant has no record of this environment, so it has no release history here. Register it from Overview.
      </p>
    );
  }
  return (
    <Panel
      title="Releases"
      description="Every release promoted to this environment, newest first. Promotion writes a pointer; deploying moves bytes."
      testId="section-releases"
      flush
    >
      <LiveReleases env={live} promotions={promotions} isLoading={isLoading} error={error} />
    </Panel>
  );
}

// ── Activity ────────────────────────────────────────────────────────────────

export function ActivityTab({
  live,
  promotions,
  convergences,
  isLoading,
  error,
}: {
  live: LiveEnv | null;
  promotions: CloudPromotion[] | undefined;
  convergences: LiveConvergence[] | undefined;
  isLoading: boolean;
  error: Error | null;
}) {
  if (!live) {
    return (
      <p className="text-sm text-muted-foreground" data-testid="activity-unregistered">
        Reliant has no record of this environment, so it has no activity here. Register it from Overview.
      </p>
    );
  }
  return (
    <Panel
      title="Activity"
      description="What happened here, newest first: each promotion, and what Reliant observed on the cluster after it."
      testId="section-activity"
      flush
    >
      <LiveActivity env={live} promotions={promotions} convergences={convergences} isLoading={isLoading} error={error} />
    </Panel>
  );
}

// ── Domains ─────────────────────────────────────────────────────────────────

export function DomainsTab({
  live,
  domains,
  domainsLoading,
  onManageDomains,
}: {
  live: LiveEnv | null;
  domains: DomainsState | undefined;
  domainsLoading: boolean;
  onManageDomains: () => void;
}) {
  if (!live) {
    return (
      <p className="text-sm text-muted-foreground" data-testid="domains-unregistered">
        Reliant has no record of this environment, so no domain can point at it yet. Register it from Overview.
      </p>
    );
  }
  return (
    <Panel
      title="Custom domains"
      description="Hostnames that serve this environment. Domains belong to your organization and are added, moved and removed on the Domains screen."
      testId="section-domains"
    >
      <EnvDomains
        environmentId={live.id}
        placed={isPlacedKind(live.kind)}
        state={domains}
        isLoading={domainsLoading}
        onManage={onManageDomains}
      />
    </Panel>
  );
}

// ── Secrets ─────────────────────────────────────────────────────────────────

export function SecretsTab(props: {
  projectId: string | null;
  live: LiveEnv | null;
  envName: string;
  forgeProject: string | null;
  selectedSecret: string | null;
  onSelectSecret: (name: string | null) => void;
}) {
  return (
    <div data-testid="section-secrets">
      <LiveSecretsSection
        projectId={props.projectId}
        env={props.live}
        envName={props.envName}
        forgeProject={props.forgeProject}
        selectedSecret={props.selectedSecret}
        onSelectSecret={props.onSelectSecret}
      />
    </div>
  );
}

// ── Running (local env) ─────────────────────────────────────────────────────

export function RunningTab({
  envName,
  daemon,
  envStatus,
  envStatusLoading,
  envStatusError,
  onRetry,
  retrying,
  projectName,
}: {
  envName: string;
  daemon: DaemonSide;
  envStatus: ForgeOutcome<ForgeEnvStatusReport> | undefined;
  envStatusLoading: boolean;
  envStatusError: Error | null;
  onRetry: () => void;
  retrying: boolean;
  projectName?: string;
}) {
  if (daemon === "offline" || (envStatusError && !envStatus)) {
    return (
      <DaemonNeeded
        what={`what forge env up is running for ${envName} on your machine`}
        detail={envStatusError?.message}
        onRetry={onRetry}
        retrying={retrying}
      />
    );
  }
  if ((envStatusLoading && !envStatus) || daemon === "loading") {
    return <PanelSkeleton testId="running-loading" rows={4} />;
  }

  const report = envStatus?.kind === "report" ? envStatus.report : null;
  // forge answered, and nothing it launched is up: not running, which is a
  // normal state with a one-command remedy — not an error.
  if (report && !devStackRunsHere(report)) {
    return (
      <div
        data-testid="running-stopped"
        className="flex items-start gap-3 rounded-lg border border-dashed border-border bg-background px-4 py-4"
      >
        <Play className="mt-0.5 h-4 w-4 shrink-0 text-muted-foreground" aria-hidden="true" />
        <div className="space-y-1">
          <p className="text-sm text-foreground">
            <span className="font-mono">{envName}</span> isn&apos;t running on this machine.
          </p>
          <p className="text-xs text-muted-foreground">
            Start it with <code className="font-mono text-foreground">forge env up {envName}</code>.
          </p>
        </div>
      </div>
    );
  }

  return (
    <div className="space-y-2" data-testid="running-tab">
      <div className="flex justify-end">
        <Button
          variant="ghost"
          size="sm"
          onClick={onRetry}
          loading={retrying}
          disabled={retrying}
          leftIcon={<RefreshCw className="h-3 w-3" />}
          data-testid="running-refresh"
        >
          Re-check
        </Button>
      </div>
      <DevStackPanel
        outcome={envStatus}
        isLoading={envStatusLoading}
        error={envStatusError}
        env={envName}
        projectName={projectName}
        servicesFirst
      />
    </div>
  );
}
