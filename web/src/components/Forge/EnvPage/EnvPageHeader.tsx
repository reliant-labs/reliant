// Copyright (c) 2025 Reliant Labs

/**
 * An environment page's header: what it is, what it runs, and what you can do
 * to it — visible from every tab.
 *
 * ── ACTIONS ARE NEVER HIDDEN, ONLY DISABLED WITH A REASON ──────────────────
 *
 * Promote and Deploy are planned by forge against the user's checkout, so both
 * need the daemon. They used to appear only on the Preview tab, and only when
 * the daemon happened to answer, so the Live view of prod had no actions at
 * all and nothing said why. Here they are always present on a deployed
 * environment and say what is missing when they cannot run.
 *
 * A LOCAL environment has neither: `forge env up` runs the working tree, the
 * control plane refuses both for a local env, and there is nothing to ship.
 * The badge says so instead of offering a button that cannot work.
 */

import { Cpu, Rocket, Upload } from "lucide-react";

import Badge from "@/components/forge-ui/badge";
import { Button } from "@/components/ui/Button";
import { Tooltip } from "@/components/ui/Tooltip";
import { liveKindLabel, type LiveEnv } from "@/services/forge/live";
import type { EnvLifecycle } from "@/services/forge/roster";

export interface EnvAction {
  /** Null when the action can run; otherwise the sentence that says why not. */
  disabledReason: string | null;
  /** The click is waiting on the daemon's answer before it can open. */
  pending?: boolean;
  onClick: () => void;
}

export interface EnvPageHeaderProps {
  envName: string;
  lifecycle: EnvLifecycle;
  live: LiveEnv | null;
  /** The release forge's own ledger has bound, when Reliant has none recorded. */
  forgeRelease: string | null;
  /** Null for a local environment — no shipping actions exist. */
  promote: (EnvAction & { target: string | null }) | null;
  deploy: EnvAction | null;
}

export function EnvPageHeader({ envName, lifecycle, live, forgeRelease, promote, deploy }: EnvPageHeaderProps) {
  const release = live?.release || forgeRelease || "";
  return (
    <header className="space-y-2" data-testid="env-page-header">
      <div className="flex flex-wrap items-start justify-between gap-4">
        <div className="min-w-0 space-y-1.5">
          <h1 className="font-mono text-2xl font-semibold tracking-tight text-foreground">{envName}</h1>
          <div className="flex flex-wrap items-center gap-2" data-testid="env-page-badges">
            {lifecycle === "local" ? (
              <Badge label="Runs locally" variant="info" size="sm" />
            ) : live ? (
              <Badge label={liveKindLabel(live.kind)} variant="neutral" size="sm" />
            ) : null}
            {!live && <Badge label="Not registered" variant="warning" size="sm" />}
            {release !== "" && lifecycle !== "local" && (
              <span className="text-xs text-muted-foreground" data-testid="env-page-release">
                on <span className="font-mono text-foreground">{release}</span>
                {!live?.release && forgeRelease ? " (forge ledger)" : ""}
              </span>
            )}
          </div>
        </div>

        {lifecycle === "local" ? (
          <p className="flex max-w-xs items-center gap-1.5 text-xs text-muted-foreground" data-testid="env-page-local-note">
            <Cpu className="h-3.5 w-3.5 shrink-0" aria-hidden="true" />
            Runs your working tree via <code className="font-mono text-foreground">forge env up</code> — never built or released.
          </p>
        ) : (
          <div className="flex flex-wrap items-center gap-2" data-testid="env-page-actions">
            {promote && (
              <ActionButton
                action={promote}
                testId="env-action-promote"
                icon={<Upload className="h-3.5 w-3.5" />}
                label={promote.target ? `Promote to ${promote.target}…` : "Promote…"}
              />
            )}
            {deploy && (
              <ActionButton
                action={deploy}
                testId="env-action-deploy"
                icon={<Rocket className="h-3.5 w-3.5" />}
                label="Deploy…"
                primary
              />
            )}
          </div>
        )}
      </div>
    </header>
  );
}

function ActionButton({
  action,
  testId,
  icon,
  label,
  primary = false,
}: {
  action: EnvAction;
  testId: string;
  icon: React.ReactNode;
  label: string;
  primary?: boolean;
}) {
  const button = (
    <Button
      variant={primary ? "primary" : "outline"}
      size="sm"
      onClick={action.onClick}
      loading={action.pending}
      disabled={action.disabledReason !== null || action.pending}
      leftIcon={icon}
      data-testid={testId}
      aria-describedby={action.disabledReason ? `${testId}-reason` : undefined}
    >
      {label}
    </Button>
  );
  if (!action.disabledReason) return button;
  return (
    <Tooltip content={action.disabledReason} placement="bottom">
      <span className="inline-flex">
        {button}
        <span id={`${testId}-reason`} className="sr-only">
          {action.disabledReason}
        </span>
      </span>
    </Tooltip>
  );
}
