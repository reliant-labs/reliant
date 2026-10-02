// Copyright (c) 2025 Reliant Labs

/**
 * Route component for /forge/env/$env — ONE ENVIRONMENT.
 *
 * Everything a reader wants to know about "prod" is on this page, in the
 * order they ask it: where it runs and what it is on (the header), what is
 * running there (Workloads), its secrets (Secrets), what it has been on
 * (Releases), and — for a LOCAL env only — the stack `forge env up` runs on
 * this machine (Dev stack). That used to be four pages, each a different
 * forge command's report, each re-fetching the same env status.
 *
 * ── EVERY SECTION PICKS ITS OWN SOURCE, AND DEGRADES ON ITS OWN ─────────────
 *
 *                 Reliant cloud env        local env           cluster env
 *   header        control plane            daemon (+cp kind)   daemon
 *   Workloads     control plane GetStatus  (nothing deployed)  daemon env status
 *   Secrets       managed store            managed store       managed store*
 *   Releases      control plane ledger     (never promoted)    daemon ledger
 *   Dev stack     —                        daemon env status   when env up runs it here
 *
 *   * when the env has a control-plane id; otherwise one sentence saying its
 *     secrets are not in the managed store.
 *
 * So for a Reliant cloud env, a missing daemon costs NOTHING on this page but
 * the Promote/Deploy actions (which forge plans on the daemon). For a local
 * env it costs the Dev stack section, which says so. It never blanks the page.
 *
 * The Dev stack section is also shown for a NON-local env whose stack
 * `forge env up` is running on this machine right now (devStackRunsHere) —
 * control-plane's `dev` is `mixed` (host processes + k3d + compose), and its
 * dev stack is the very thing a developer opens it for.
 */

import { useCallback, useMemo, useState } from "react";
import { useNavigate, useParams, useSearch } from "@tanstack/react-router";
import { ArrowLeft, RefreshCw, Rocket, Upload } from "lucide-react";

import { Button } from "@/components/ui/Button";
import { cn } from "@/lib/utils";
import {
  useCloudEnvStatus,
  useCloudPromotions,
  useForgeEnvironments,
  useForgeEnvStatus,
  useVerifyForgeEnv,
} from "@/hooks/forge-queries";
import {
  cloudRunIdOf,
  devStackRunsHere,
  envFacts,
  isCloudLocal,
  offersShipping,
  type ForgeEnvSummary,
} from "@/services/forge/environments";
import { endpointHost } from "@/services/forge/topology";
import { useProjectStore } from "@/store/projectStore";

import { DeployDialog } from "../Deploy/DeployDialog";
import { HealthChip, verdictSentence, WhereBadge } from "../EnvBadges";
import { ForgeMalformed, ForgeUnsupported, NotForgeProject } from "../ForgeStates";
import { formatTimestamp } from "../Overview/EnvironmentTable";
import { PromoteDialog } from "../Promote/PromoteDialog";
import { CloudNotice, DaemonOfflineNotice } from "../SourceNotices";
import { DevStackPanel } from "../Status/DevStackPanel";
import { ReleasesSection, type ReleasesSource } from "./ReleasesSection";
import { SecretsSection } from "./SecretsSection";
import { WorkloadsSection, type WorkloadsSource } from "./WorkloadsSection";

export function ForgeEnvPage() {
  const navigate = useNavigate();
  const { env: envName } = useParams({ from: "/_authenticated/_forge/forge/env/$env" });
  const { project: projectParam, secret: secretParam } = useSearch({
    from: "/_authenticated/_forge/forge/env/$env",
  });
  const currentProject = useProjectStore((state) => state.currentProject);
  const projectId = projectParam ?? currentProject?.id ?? null;

  const state = useForgeEnvironments(projectId);
  const { envs, topology, daemon, cloud } = state;
  const summary: ForgeEnvSummary | undefined = envs.find((candidate) => candidate.name === envName);

  const daemonOk = daemon === "ok";
  const daemonOffline = daemon === "offline";

  // ── The per-source queries. Each is enabled only when it is THE source. ──
  const cloudRunId = summary ? cloudRunIdOf(summary) : null;
  const cloudStatus = useCloudEnvStatus(cloudRunId);
  const promotions = useCloudPromotions(cloudRunId);

  const local = !!summary && summary.where === "local";
  // env status is the daemon's: the cluster inventory for a deployed env, the
  // dev stack for a local one. Never asked for a cloud env (the control plane
  // answers for it) and never while the daemon is known to be offline.
  const needsEnvStatus = !!summary && !cloudRunId && daemonOk && !!summary.forge;
  const envStatus = useForgeEnvStatus(needsEnvStatus ? projectId : null, envName);

  // Verify reads the env's live cluster and upgrades its image cells in the
  // cached topology from `not_verified` — the only way a cluster binding's
  // health ever becomes anything but "Not verified".
  const { verify, pendingEnv, lastOutcome } = useVerifyForgeEnv(projectId);
  const verifyNotice =
    lastOutcome && lastOutcome.env === envName
      ? lastOutcome.outcome.kind === "unreachable"
        ? `The cluster could not be read, so ${envName} is still unverified.`
        : lastOutcome.outcome.kind === "unsupported"
          ? `forge ${lastOutcome.outcome.meta.forgeVersion} cannot verify this environment.`
          : `forge's verify report could not be read, so ${envName} is still unverified.`
      : null;

  const facts = useMemo(
    () => (summary ? envFacts(summary, cloudStatus.data, cloudStatus.isLoading) : null),
    [summary, cloudStatus.data, cloudStatus.isLoading]
  );

  const report = topology.data?.kind === "report" ? topology.data.report : null;
  const [promoteOpen, setPromoteOpen] = useState(false);
  const [deployOpen, setDeployOpen] = useState(false);

  const selectSecret = useCallback(
    (name: string | null) => {
      void navigate({
        to: ".",
        search: (prev: Record<string, unknown>) => ({ ...prev, secret: name ?? undefined }),
        replace: true,
      });
    },
    [navigate]
  );

  const backToOverview = (
    <button
      type="button"
      onClick={() => void navigate({ to: "/forge", search: { project: projectId ?? undefined } })}
      className="inline-flex items-center gap-1.5 text-xs text-muted-foreground transition-colors hover:text-foreground"
    >
      <ArrowLeft className="h-3.5 w-3.5" aria-hidden="true" />
      All environments
    </button>
  );

  // ── Not (yet) a known environment ──
  if (!summary) {
    let body: React.ReactNode;
    if (state.isLoading) {
      body = (
        <p data-testid="forge-env-loading" className="text-sm text-muted-foreground">
          Reading <span className="font-mono">{envName}</span>…
        </p>
      );
    } else if (topology.data?.kind === "not-forge-project") {
      body = <NotForgeProject projectName={currentProject?.name} />;
    } else if (topology.data?.kind === "unsupported") {
      body = <ForgeUnsupported meta={topology.data.meta} />;
    } else if (topology.data?.kind === "malformed") {
      body = <ForgeMalformed meta={topology.data.meta} />;
    } else {
      body = (
        <div
          data-testid="forge-env-unknown"
          className="space-y-2 rounded-lg border border-dashed border-border px-6 py-10 text-center text-sm text-muted-foreground"
        >
          <p>
            <span className="font-mono text-foreground">{envName}</span> is not an environment this
            project declares{cloud.data?.availability === "available" ? " or the control plane holds" : ""}.
          </p>
          {daemonOffline && <p>Your daemon is offline, so environments only it knows about are not listed.</p>}
        </div>
      );
    }
    return (
      <div className="space-y-6">
        {backToOverview}
        {body}
      </div>
    );
  }

  const shipping = daemonOk && !!projectId && offersShipping(summary);
  const canVerify = daemonOk && !cloudRunId && (summary.forge?.images?.length ?? 0) > 0;
  const envStatusReport = envStatus.data?.kind === "report" ? envStatus.data.report : null;
  const showDevStack = local || devStackRunsHere(envStatusReport);
  const workloadsSource: WorkloadsSource = cloudRunId
    ? {
        kind: "cloud",
        status: cloudStatus.data,
        isLoading: cloudStatus.isLoading,
        error: cloudStatus.error as Error | null,
      }
    : local
      ? { kind: "local" }
      : {
          kind: "daemon",
          outcome: envStatus.data,
          isLoading: envStatus.isLoading || daemon === "loading",
          error: envStatus.error as Error | null,
          daemonOffline,
        };
  const releasesSource: ReleasesSource = cloudRunId
    ? {
        kind: "cloud",
        promotions: promotions.data,
        isLoading: promotions.isLoading,
        error: promotions.error as Error | null,
      }
    : local || isCloudLocal(summary)
      ? { kind: "local" }
      : { kind: "ledger", daemonOffline };

  const sentence = facts ? verdictSentence(facts.health) : null;
  const endpoint = summary.forge?.endpoint ? endpointHost(summary.forge.endpoint) : "";

  return (
    <div className="space-y-8" data-testid="forge-env-page" data-where={summary.where}>
      {backToOverview}

      {/* ── Header: where it runs, what it is on, is it healthy, act on it ── */}
      <header className="space-y-3" data-testid="forge-env-header">
        <div className="flex flex-wrap items-start justify-between gap-4">
          <div className="min-w-0 space-y-2">
            <div className="flex flex-wrap items-center gap-2">
              <h1 className="font-mono text-xl font-semibold text-foreground">{summary.name}</h1>
              <WhereBadge env={summary.name} where={summary.where} />
              {facts && <HealthChip env={summary.name} health={facts.health} />}
            </div>
            <dl className="flex flex-wrap gap-x-6 gap-y-1 text-xs text-muted-foreground">
              <HeaderFact label="Runs on">
                {summary.where === "local" ? (
                  <code className="font-mono text-foreground">forge env up</code>
                ) : summary.where === "cloud" ? (
                  <span className="font-mono text-foreground">{endpoint || "Reliant cloud"}</span>
                ) : summary.forge?.kube_context ? (
                  <span className="font-mono text-foreground">
                    {[summary.forge.kube_context, summary.forge.namespace].filter(Boolean).join(" · ")}
                  </span>
                ) : (
                  <span>—</span>
                )}
              </HeaderFact>
              <HeaderFact label="Release">
                {facts?.binding === "local" ? (
                  <span>working tree</span>
                ) : facts?.release ? (
                  <span className="font-mono text-foreground">
                    {facts.release}
                    {facts.rolledBack ? " (rollback)" : ""}
                  </span>
                ) : (
                  <span>never promoted</span>
                )}
              </HeaderFact>
              {facts?.promotedAt && (
                <HeaderFact label="Promoted">
                  <span className="text-foreground">{formatTimestamp(facts.promotedAt)}</span>
                </HeaderFact>
              )}
            </dl>
            {sentence && <p className="max-w-2xl text-xs text-muted-foreground">{sentence}</p>}
            {verifyNotice && (
              <p data-testid={`verify-notice-${summary.name}`} className="max-w-2xl text-xs text-muted-foreground">
                {verifyNotice}
              </p>
            )}
          </div>

          {(shipping || canVerify) && (
            <div className="flex shrink-0 items-center gap-2">
              {canVerify && (
                <Button
                  variant="outline"
                  size="sm"
                  onClick={() => verify(summary.name)}
                  loading={pendingEnv === summary.name}
                  disabled={pendingEnv === summary.name}
                  leftIcon={<RefreshCw className="h-3 w-3" />}
                  data-testid={`verify-${summary.name}`}
                  aria-label={`Verify ${summary.name} against its live cluster`}
                >
                  {pendingEnv === summary.name ? "Reading cluster…" : "Verify"}
                </Button>
              )}
              {shipping && report?.latest_release && (
                <Button
                  variant="outline"
                  size="sm"
                  onClick={() => setPromoteOpen(true)}
                  leftIcon={<Upload className="h-3 w-3" />}
                  data-testid={`promote-open-${summary.name}`}
                  aria-label={`Preview promoting ${summary.name} to ${report.latest_release}`}
                >
                  Promote…
                </Button>
              )}
              {shipping && (
              <Button
                variant="outline"
                size="sm"
                onClick={() => setDeployOpen(true)}
                leftIcon={<Rocket className="h-3 w-3" />}
                data-testid={`deploy-open-${summary.name}`}
                aria-label={`Preview deploying ${summary.name}`}
              >
                Deploy…
              </Button>
              )}
            </div>
          )}
        </div>

        {daemonOffline && (
          <DaemonOfflineNotice
            scope={
              cloudRunId
                ? "Everything on this page comes from the control plane except Promote and Deploy, which forge plans on the daemon."
                : local
                  ? "Secrets are read from the control plane below; the dev stack forge env up runs needs the daemon."
                  : "Secrets are read from the control plane below; this environment's workloads and ledger are read by forge on the daemon."
            }
            detail={(topology.error as Error | null)?.message}
          />
        )}
        {!summary.cloud && <CloudNotice availability={cloud.data?.availability} detail={cloud.data?.detail} />}
      </header>

      <Section title="Workloads" testId="section-workloads">
        <WorkloadsSection summary={summary} source={workloadsSource} projectName={currentProject?.name} />
      </Section>

      <Section title="Secrets" testId="section-secrets">
        <SecretsSection
          projectId={projectId}
          summary={summary}
          daemonAvailable={daemonOk}
          // forge's OWN name for the project (forge.yaml `name`), not the
          // Reliant project's display name: the control plane files
          // environments under forge's name, so it is the join key. The
          // live report's when the daemon answered, else the one persisted on
          // the project row — so a secret can still be set with forge down.
          forgeProject={state.projectName.name}
          selectedSecret={secretParam ?? null}
          onSelectSecret={selectSecret}
        />
      </Section>

      <Section title="Releases" testId="section-releases">
        {facts && <ReleasesSection summary={summary} facts={facts} source={releasesSource} />}
      </Section>

      {showDevStack && (
        <Section
          title="Dev stack"
          subtitle="What forge env up runs on this machine, and forge's runtime checks against it."
          testId="section-dev-stack"
        >
          {daemonOffline ? (
            <p data-testid="dev-stack-daemon-offline" className="text-sm text-muted-foreground">
              The dev stack runs on your machine and is read by your daemon, which is offline.
            </p>
          ) : (
            <DevStackPanel
              outcome={envStatus.data}
              isLoading={envStatus.isLoading || daemon === "loading"}
              error={envStatus.error as Error | null}
              env={summary.name}
              projectName={currentProject?.name}
            />
          )}
        </Section>
      )}

      {promoteOpen && report?.latest_release && (
        <PromoteDialog
          isOpen
          onClose={() => setPromoteOpen(false)}
          projectId={projectId}
          env={summary.name}
          release={report.latest_release}
          projectName={currentProject?.name}
        />
      )}
      {deployOpen && (
        <DeployDialog
          isOpen
          onClose={() => setDeployOpen(false)}
          projectId={projectId}
          env={summary.name}
          projectName={currentProject?.name}
        />
      )}
    </div>
  );
}

function HeaderFact({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <div className="flex items-baseline gap-1.5">
      <dt className="text-2xs font-medium uppercase tracking-wide">{label}</dt>
      <dd>{children}</dd>
    </div>
  );
}

/** A page section: panel heading on the ladder (text-sm semibold), then its body. */
function Section({
  title,
  subtitle,
  testId,
  children,
}: {
  title: string;
  subtitle?: string;
  testId: string;
  children: React.ReactNode;
}) {
  return (
    <section className={cn("space-y-3")} data-testid={testId} aria-labelledby={`${testId}-heading`}>
      <div className="space-y-0.5 border-b border-border pb-2">
        <h2 id={`${testId}-heading`} className="text-sm font-semibold text-foreground">
          {title}
        </h2>
        {subtitle && <p className="text-xs text-muted-foreground">{subtitle}</p>}
      </div>
      {children}
    </section>
  );
}
