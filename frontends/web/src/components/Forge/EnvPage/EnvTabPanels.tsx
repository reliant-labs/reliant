// Copyright (c) 2025 Reliant Labs

/**
 * The bodies of an environment page's tabs. See envTabs.ts for which
 * environment gets which, and which tabs read the daemon.
 *
 * Every panel is built from existing pieces — LiveState,
 * LiveSecretsSection, DevStackPanel, RegisterEnvPanel —
 * re-grouped by the question they
 * answer. Sections are forge-ui Cards with a CardHeader, rather than three
 * hand-rolled copies of the same heading-and-rule.
 */

import { useState, type ReactNode } from "react";
import { Play, RefreshCw } from "lucide-react";

import Card, { CardHeader } from "@/components/forge-ui/card";
import Link from "@/components/forge-ui/link";
import SkeletonLoader from "@/components/forge-ui/skeleton_loader";
import Button from "@/components/forge-ui/button";
import type { DomainsState } from "@/hooks/forge-domain-queries";
import type { ProjectCheckoutMissing } from "@/gen/reliant/v1/filesystem_pb";
import { projectCheckoutMissing } from "@/lib/daemon-errors";
import { isForgeAPIDisabled } from "@/lib/forgeApiDisabled";
import type { CloudEnvStatus } from "@/services/forge/cloudEnvs";
import { frontendsOf } from "@/services/forge/frontends";
import { declaredNotBuilt, isPlacedKind, neverBuilt, type LiveEnv } from "@/services/forge/live";
import { devStackRunsHere, type DaemonSide } from "@/services/forge/environments";
import type { ForgeEnvStatusReport } from "@/services/forge/status";
import type { ForgeOutcome, ForgeTopologyEnv } from "@/services/forge/topology";

import { FrontendList, FrontendsEmpty, FrontendsLoading } from "../Frontends/FrontendList";
import { formatTimestamp } from "../Overview/EnvironmentList";
import { RegisterEnvPanel } from "../Preview/RegisterEnvPanel";
import { DevStackPanel } from "../Status/DevStackPanel";
import { CheckoutMissing } from "./CheckoutMissing";
import { DaemonNeeded } from "./DaemonNeeded";
import { EnvDomains } from "./EnvDomains";
import { LiveSecretsSection } from "./LiveSecretsSection";
import { LiveState } from "./LiveState";

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
      {/* The header's own padding: forge-ui's CardHeader has none, so in a
          `padding="none"` card it would sit on the border. */}
      <div className="px-4 pt-3">
        <CardHeader title={title} description={description} actions={actions} />
      </div>
      <div className={flush ? "" : "px-4 pb-4"}>{children}</div>
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

// ── Overview (deployed env) ─────────────────────────────────────────────────

export interface OverviewTabProps {
  envName: string;
  live: LiveEnv | null;
  liveLoading: boolean;
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
  /** The Overview scoped to this environment: where its services and jobs are. */
  workloadsHref: string;
  /** The control plane's GetStatus for this environment: where its frontends and their URLs come from. */
  cloudStatus?: CloudEnvStatus;
  cloudStatusLoading?: boolean;
  /** The Domains screen, where "Add a domain" lands. */
  domainsHref?: string;
  /** Topology's ProjectCheckoutMissing, when that is why the daemon side failed. */
  checkoutMissing?: ProjectCheckoutMissing | null;
  /** The Reliant project's name, for the checkout-missing sentence. */
  projectName?: string;
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
          ) : props.daemon === "no-checkout" && props.checkoutMissing ? (
            <CheckoutMissing
              missing={props.checkoutMissing}
              projectName={props.projectName}
              what={`the shape forge renders for ${envName}, which registering records`}
            />
          ) : (
            <DaemonNeeded
              what={`the shape forge renders for ${envName}, which registering records`}
              forgeDisabled={props.daemon === "disabled"}
            />
          )}
        </Panel>
        {props.forgeEnv && <ForgeLedgerFacts env={props.forgeEnv} />}
      </div>
    );
  }

  return (
    <div className="space-y-4" data-testid="live-section" data-kind={live.kind}>
      {registered && (
        <p data-testid="register-done" className="rounded-lg border border-border bg-surface px-4 py-3 text-sm text-ink">
          Registered. <span className="font-mono">{envName}</span> is tracked in Reliant now — its secrets can be set
          from the Secrets tab, with your daemon offline.
        </p>
      )}
      <Panel title="State" testId="env-state">
        {declaredNotBuilt(live) ? (
          <div className="space-y-2">
            <p data-testid="live-declared-not-built" className="text-sm text-ink-muted">
              Declared, not built yet. Its secrets can be set; a build records the first release.
            </p>
            {live.provenance !== "" && (
              <p data-testid="live-provenance" className="font-mono text-xs text-ink-muted">
                {live.provenance}
              </p>
            )}
          </div>
        ) : neverBuilt(live) ? (
          <p data-testid="live-row-blank" className="text-sm text-ink-muted">
            Registered, with nothing recorded yet. <code className="font-mono text-ink">forge env build {live.name} --release</code>{" "}
            records the first release.
          </p>
        ) : (
          <div className="space-y-3">
            <LiveState env={live} />
            {/* THE PROVENANCE LINE (§2.1): images from the release's source,
                config from the render's. Descriptive, not approving. */}
            {live.provenance !== "" && (
              <p data-testid="live-provenance" className="font-mono text-xs text-ink-muted">
                {live.provenance}
              </p>
            )}
          </div>
        )}
      </Panel>

      {/* Frontends: the sites people open. Each is live at its generated
          address already; a custom domain only adds a second one. Omitted for
          an env the platform does not run — it has no generated addresses. */}
      {isPlacedKind(live.kind) && (
        <Panel
          title="Frontends"
          description="Websites Reliant hosts for this environment, and the address each is live at."
          testId="env-frontends"
          flush
        >
          <EnvFrontends
            env={live}
            status={props.cloudStatus}
            loading={props.cloudStatusLoading ?? false}
            domains={props.domains}
            domainsHref={props.domainsHref ?? props.workloadsHref}
          />
        </Panel>
      )}

      {/* The workloads themselves — health, pods, releases, restarts — are the
          Overview's (one place, every environment), not a second copy here. */}
      <Panel title="Workloads" testId="section-workloads">
        <p className="text-sm text-ink-muted">
          {props.envName}&apos;s services and jobs, their health and their pods are on the{" "}
          <Link href={props.workloadsHref} className="text-ink underline underline-offset-2" data-testid="env-workloads-link">
            Overview, scoped to {props.envName}
          </Link>
          .
        </p>
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

function EnvFrontends({
  env,
  status,
  loading,
  domains,
  domainsHref,
}: {
  env: LiveEnv;
  status: CloudEnvStatus | undefined;
  loading: boolean;
  domains: DomainsState | undefined;
  domainsHref: string;
}) {
  if (!status) {
    return loading ? (
      <FrontendsLoading />
    ) : (
      <p className="px-4 pb-4 text-sm text-ink-muted" data-testid="env-frontends-unavailable">
        Reliant could not read this environment&apos;s deployments just now, so its frontends are not shown. They will appear when the next read succeeds.
      </p>
    );
  }
  const frontends = frontendsOf(
    { id: env.id, name: env.name, release: env.release },
    status.workloads,
    domains?.availability === "available" ? domains.domains : null
  );
  if (frontends.length === 0) {
    return (
      <div className="px-4 pb-4">
        <FrontendsEmpty env={env.name} />
      </div>
    );
  }
  return <FrontendList frontends={frontends} domainsHref={domainsHref} />;
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
          <dt className="text-ink-muted">Release</dt>
          <dd className="font-mono text-ink">{env.release}</dd>
          {env.promoted_at && (
            <>
              <dt className="text-ink-muted">Promoted</dt>
              <dd className="text-ink">{formatTimestamp(env.promoted_at)}</dd>
            </>
          )}
          {env.kube_context && (
            <>
              <dt className="text-ink-muted">Cluster</dt>
              <dd className="font-mono text-ink">
                {env.kube_context}
                {env.namespace ? ` / ${env.namespace}` : ""}
              </dd>
            </>
          )}
        </dl>
      ) : (
        <p className="text-sm text-ink-muted">Never promoted in forge&apos;s ledger.</p>
      )}
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
  checkoutMissing,
}: {
  envName: string;
  daemon: DaemonSide;
  envStatus: ForgeOutcome<ForgeEnvStatusReport> | undefined;
  envStatusLoading: boolean;
  envStatusError: Error | null;
  onRetry: () => void;
  retrying: boolean;
  projectName?: string;
  /** Topology's ProjectCheckoutMissing, when that is why the daemon side failed. */
  checkoutMissing?: ProjectCheckoutMissing | null;
}) {
  // Not on a machine that could answer: say so, with the remedy — never
  // "your daemon isn't answering", which a retry cannot fix.
  const missing = projectCheckoutMissing(envStatusError) ?? (daemon === "no-checkout" ? checkoutMissing : null);
  if (missing) {
    return (
      <CheckoutMissing
        missing={missing}
        projectName={projectName}
        what={`what forge env up is running for ${envName}`}
        onRetry={onRetry}
        retrying={retrying}
      />
    );
  }
  if (daemon === "offline" || daemon === "no-checkout" || daemon === "disabled" || (envStatusError && !envStatus)) {
    return (
      <DaemonNeeded
        what={`what forge env up is running for ${envName} on your machine`}
        detail={envStatusError?.message}
        onRetry={onRetry}
        retrying={retrying}
        forgeDisabled={daemon === "disabled" || isForgeAPIDisabled(envStatusError)}
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
        className="flex items-start gap-3 rounded-lg border border-dashed border-border bg-surface-sunken px-4 py-4"
      >
        <Play className="mt-0.5 h-4 w-4 shrink-0 text-ink-muted" aria-hidden="true" />
        <div className="space-y-1">
          <p className="text-sm text-ink">
            <span className="font-mono">{envName}</span> isn&apos;t running on this machine.
          </p>
          <p className="text-xs text-ink-muted">
            Start it with <code className="font-mono text-ink">forge env up {envName}</code>.
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
