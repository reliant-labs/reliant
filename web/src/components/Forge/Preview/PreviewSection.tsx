// Copyright (c) 2025 Reliant Labs

/**
 * PREVIEW — the ONLY daemon-dependent surface, and deliberately so.
 *
 * Everything here needs the user's CHECKOUT, which is the one thing the
 * control plane does not have and cannot be given: forge's render of the KCL,
 * the cluster inventory it reads by kube context, the dev stack `forge env up`
 * launched on this machine, the project audit, and the plans that authorise a
 * promote or a deploy. So this tab is where the daemon lives, and Live (the
 * default tab) never touches it.
 *
 * Two consequences, and they are the point of the split (design §8.0, O-14):
 *
 *   1. When the daemon is offline, PREVIEW ALONE is unavailable, with one
 *      short plain line. Not a banner over the page, and not an error that
 *      makes the environment look broken — because it is not: Live is right
 *      there, fully rendered, from the control plane.
 *   2. Live is never degraded by daemon state. A daemon that is offline, slow,
 *      or has never existed changes nothing about what Live shows.
 *
 * ── THE COPY REGISTER ───────────────────────────────────────────────────────
 *
 * "Daemon offline. Preview needs it to render your code." Short and literal.
 * Explanatory framing — "showing what the control plane knows; connect your
 * machine to preview changes" — is NOT the register: it explains the
 * architecture to someone who only wanted to know why a tab is empty.
 *
 * ── WHAT IT DOES BEYOND SHOWING ─────────────────────────────────────────────
 *
 * REGISTER (see RegisterEnvPanel) is the bootstrap for an environment that
 * exists only in the KCL. Preview reads forge's shape; the BROWSER writes the
 * row. That write is not a daemon call, which is why registering works the
 * moment Preview can render once and then never needs the daemon again.
 *
 * R3 grows this tab into the full what-if surface: a checkout picker and a
 * diff against Live. Until then it holds the daemon surfaces that already
 * exist, including the Deploy and Promote dialogs unchanged.
 */

import { useState } from "react";
import { RefreshCw, Rocket, Upload } from "lucide-react";

import { Button } from "@/components/ui/Button";
import { useForgeAudit, useForgeEnvStatus, useVerifyForgeEnv } from "@/hooks/forge-queries";
import {
  devStackRunsHere,
  type DaemonSide,
  type ForgeEnvSummary,
} from "@/services/forge/environments";
import type { LiveEnv } from "@/services/forge/live";
import type { ForgeOutcome, ForgeTopologyReport } from "@/services/forge/topology";

import { AuditStrip } from "../Audit/AuditStrip";
import { DeployDialog } from "../Deploy/DeployDialog";
import { ForgeMalformed, ForgeUnsupported, NotForgeProject } from "../ForgeStates";
import { PromoteDialog } from "../Promote/PromoteDialog";
import { DevStackPanel } from "../Status/DevStackPanel";
import { WorkloadInventory } from "../Environments/WorkloadInventory";
import { RegisterEnvPanel } from "./RegisterEnvPanel";

/** The one line Preview shows when the daemon is not answering. */
export const DAEMON_OFFLINE_COPY = "Daemon offline. Preview needs it to render your code.";

export interface PreviewSectionProps {
  projectId: string | null;
  envName: string;
  /** The Reliant project's display name, for the daemon panels' empty states. */
  projectName?: string;
  /** The forge project name — Register's (org, project, name) project half. */
  forgeProject: string | null;

  /** forge's own row for this env, when the daemon answered and declares it. */
  summary: ForgeEnvSummary | null;
  /** The control plane's row. Null means Register is what this tab offers. */
  liveEnv: LiveEnv | null;

  daemonState: DaemonSide;
  daemonError: Error | null;
  /** forge's topology outcome, for the non-ok states' own meta (forge version). */
  topologyOutcome: ForgeOutcome<ForgeTopologyReport> | undefined;
  /** forge's latest release in this checkout — what a promote would target. */
  latestRelease: string | null;
}

export function PreviewSection(props: PreviewSectionProps) {
  const { projectId, envName, summary, liveEnv, daemonState, forgeProject } = props;

  const [promoteOpen, setPromoteOpen] = useState(false);
  const [deployOpen, setDeployOpen] = useState(false);
  // Sticky for this mount: see the Register panel's conditional below.
  const [registered, setRegistered] = useState(false);

  const daemonOk = daemonState === "ok";
  const envStatus = useForgeEnvStatus(daemonOk ? projectId : null, envName);
  const audit = useForgeAudit(daemonOk ? projectId : null);
  const { verify, pendingEnv, lastOutcome } = useVerifyForgeEnv(projectId);

  // ── The daemon is not answering: one line, nothing else. ──
  if (daemonState === "offline" || daemonState === "loading") {
    return daemonState === "loading" ? (
      <p data-testid="preview-loading" className="text-sm text-muted-foreground">
        Reading your checkout…
      </p>
    ) : (
      <div className="space-y-2" data-testid="preview-daemon-offline">
        <p className="text-sm text-muted-foreground">{DAEMON_OFFLINE_COPY}</p>
        {props.daemonError?.message && (
          <p className="font-mono text-2xs text-muted-foreground">{props.daemonError.message}</p>
        )}
      </div>
    );
  }

  // ── forge's own answers about the PROJECT. Facts, rendered as themselves.
  //
  // The meta comes from forge's actual outcome rather than being
  // reconstructed: it carries the forge version these panels tell the user to
  // upgrade, and a synthesised empty meta would render the remedy with the
  // version missing.
  if (daemonState !== "ok") {
    const outcome = props.topologyOutcome;
    return (
      <div data-testid="preview-forge-state">
        {outcome?.kind === "not-forge-project" ? (
          <NotForgeProject projectName={props.projectName} />
        ) : outcome?.kind === "unsupported" ? (
          <ForgeUnsupported meta={outcome.meta} />
        ) : outcome?.kind === "malformed" ? (
          <ForgeMalformed meta={outcome.meta} />
        ) : (
          <p className="text-sm text-muted-foreground">{DAEMON_OFFLINE_COPY}</p>
        )}
      </div>
    );
  }

  const envStatusReport = envStatus.data?.kind === "report" ? envStatus.data.report : null;
  const showDevStack = summary?.where === "local" || devStackRunsHere(envStatusReport);
  const canVerify = (summary?.forge?.images?.length ?? 0) > 0;
  const verifyNotice =
    lastOutcome && lastOutcome.env === envName
      ? lastOutcome.outcome.kind === "unreachable"
        ? `The cluster could not be read, so ${envName} is still unverified.`
        : lastOutcome.outcome.kind === "unsupported"
          ? `forge ${lastOutcome.outcome.meta.forgeVersion} cannot verify this environment.`
          : `forge's verify report could not be read, so ${envName} is still unverified.`
      : null;

  return (
    <div className="space-y-8" data-testid="preview-section">
      {/* ── Register: the environment is in the code and Reliant has no
          record of it, so Live cannot show it. This is the bootstrap. ──

          `registered` keeps the panel mounted after a successful add, and it
          is not a cosmetic nicety: the add makes Live know the environment, so
          `liveEnv` becomes non-null and the panel would VANISH on the very
          render that should confirm it worked. The user would be left having
          clicked a button that erased itself. */}
      {(!liveEnv || registered) && (
        <RegisterEnvPanel
          projectId={projectId}
          envName={envName}
          forgeProject={forgeProject}
          declaredHere={!!summary?.forge}
          onRegistered={() => setRegistered(true)}
        />
      )}

      {/* ── Act on it. Both plans are computed by forge on the daemon, which
          is why they are here and not on Live. R3 replaces them. ── */}
      <div className="flex flex-wrap items-center gap-2" data-testid="preview-actions">
        {canVerify && (
          <Button
            variant="outline"
            size="sm"
            onClick={() => verify(envName)}
            loading={pendingEnv === envName}
            disabled={pendingEnv === envName}
            leftIcon={<RefreshCw className="h-3 w-3" />}
            data-testid={`verify-${envName}`}
            aria-label={`Verify ${envName} against its live cluster`}
          >
            {pendingEnv === envName ? "Reading cluster…" : "Verify"}
          </Button>
        )}
        {props.latestRelease && (
          <Button
            variant="outline"
            size="sm"
            onClick={() => setPromoteOpen(true)}
            leftIcon={<Upload className="h-3 w-3" />}
            data-testid={`promote-open-${envName}`}
            aria-label={`Preview promoting ${envName} to ${props.latestRelease}`}
          >
            Promote…
          </Button>
        )}
        <Button
          variant="outline"
          size="sm"
          onClick={() => setDeployOpen(true)}
          leftIcon={<Rocket className="h-3 w-3" />}
          data-testid={`deploy-open-${envName}`}
          aria-label={`Preview deploying ${envName}`}
        >
          Deploy…
        </Button>
      </div>
      {verifyNotice && (
        <p data-testid={`verify-notice-${envName}`} className="text-xs text-muted-foreground">
          {verifyNotice}
        </p>
      )}

      <Section
        title="What forge sees"
        subtitle="forge's render of this environment in your checkout, and what it reads from its cluster."
        testId="preview-inventory"
      >
        <WorkloadInventory
          outcome={envStatus.data}
          isLoading={envStatus.isLoading}
          error={envStatus.error as Error | null}
          env={envName}
          projectName={props.projectName}
        />
      </Section>

      {showDevStack && (
        <Section
          title="Dev stack"
          subtitle="What forge env up runs on this machine, and forge's runtime checks against it."
          testId="preview-dev-stack"
        >
          <DevStackPanel
            outcome={envStatus.data}
            isLoading={envStatus.isLoading}
            error={envStatus.error as Error | null}
            env={envName}
            projectName={props.projectName}
          />
        </Section>
      )}

      <Section
        title="Project audit"
        subtitle="forge's static analysis over this checkout's files."
        testId="preview-audit"
      >
        <AuditStrip
          outcome={audit.data}
          isLoading={audit.isLoading}
          error={audit.error as Error | null}
          projectName={props.projectName}
        />
      </Section>

      {promoteOpen && props.latestRelease && (
        <PromoteDialog
          isOpen
          onClose={() => setPromoteOpen(false)}
          projectId={projectId}
          env={envName}
          release={props.latestRelease}
          projectName={props.projectName}
        />
      )}
      {deployOpen && (
        <DeployDialog
          isOpen
          onClose={() => setDeployOpen(false)}
          projectId={projectId}
          env={envName}
          projectName={props.projectName}
        />
      )}
    </div>
  );
}

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
    <section className="space-y-3" data-testid={testId} aria-labelledby={`${testId}-heading`}>
      <div className="space-y-0.5 border-b border-border pb-2">
        <h3 id={`${testId}-heading`} className="text-sm font-semibold text-foreground">
          {title}
        </h3>
        {subtitle && <p className="text-xs text-muted-foreground">{subtitle}</p>}
      </div>
      {children}
    </section>
  );
}
