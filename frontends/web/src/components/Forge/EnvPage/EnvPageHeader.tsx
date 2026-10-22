// Copyright (c) 2025 Reliant Labs

/**
 * An environment page's header: what it is, what it runs, and what you can do
 * to it — visible from every tab, and kept to two lines so the tabs below it
 * stay in view:
 *
 *   prod  [Reliant cloud]                     [Promote] [Deploy] [Stop] [...]
 *   Release 20261005.2…1f9bf672 (copy)   ● Running · confirmed 3 minutes ago
 *
 * The long explanation (intent, who promoted, drift, provenance) is the
 * Overview's State panel; this is the glance (envHeadline.ts).
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
import { cn } from "@/lib/utils";
import { liveKindLabel, type LiveEnv } from "@/services/forge/live";
import type { EnvLifecycle } from "@/services/forge/roster";

import type { EnvHeadline, HeadlineTone } from "./envHeadline";
import { ReleaseVersion } from "./ReleaseVersion";

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
  /** The one status line (envHeadline.ts). Null when there is nothing to read. */
  headline: EnvHeadline | null;
  /** Null for a local environment — no shipping actions exist. */
  promote: (EnvAction & { target: string | null }) | null;
  deploy: EnvAction | null;
  /** Stop / start / delete — only for an environment the platform places. */
  lifecycleControls?: React.ReactNode;
}

export function EnvPageHeader({
  envName,
  lifecycle,
  live,
  forgeRelease,
  headline,
  promote,
  deploy,
  lifecycleControls,
}: EnvPageHeaderProps) {
  const release = live?.release || forgeRelease || "";
  return (
    <header className="space-y-2" data-testid="env-page-header">
      <div className="flex flex-wrap items-start justify-between gap-x-4 gap-y-3">
        <div className="min-w-0 flex-1 space-y-1.5">
          <div className="flex min-w-0 flex-wrap items-center gap-x-3 gap-y-1">
            <h1 className="min-w-0 truncate font-mono text-2xl font-semibold tracking-tight text-foreground" title={envName}>
              {envName}
            </h1>
            <span className="flex shrink-0 items-center gap-2" data-testid="env-page-badges">
              {lifecycle === "local" ? (
                <Badge label="Runs locally" variant="info" size="sm" />
              ) : live ? (
                <Badge label={liveKindLabel(live.kind)} variant="neutral" size="sm" />
              ) : null}
              {!live && <Badge label="Not registered" variant="warning" size="sm" />}
            </span>
          </div>

          {lifecycle !== "local" && (release !== "" || headline) && (
            <div className="flex min-w-0 flex-wrap items-center gap-x-3 gap-y-1 text-xs text-muted-foreground">
              {release !== "" && (
                <span className="flex min-w-0 max-w-full items-center gap-1.5" data-testid="env-page-release">
                  <span className="shrink-0">Release</span>
                  <ReleaseVersion version={release} className="max-w-full sm:max-w-sm" />
                  {!live?.release && forgeRelease && <span className="shrink-0">(forge ledger)</span>}
                </span>
              )}
              {headline && <StatusLine headline={headline} />}
            </div>
          )}
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
            {lifecycleControls}
          </div>
        )}
      </div>
    </header>
  );
}

const TONE_DOT: Record<HeadlineTone, string> = {
  ok: "bg-success",
  progress: "bg-primary motion-safe:animate-pulse",
  problem: "bg-destructive",
  quiet: "border border-muted-foreground/60",
  // A ring around a fill: waiting is neither the hollow "nothing yet" nor a
  // solid verdict, and the shape says so without the hue.
  waiting: "bg-warning ring-2 ring-warning/30",
};

const TONE_TEXT: Record<HeadlineTone, string> = {
  ok: "text-success-ink",
  progress: "text-foreground",
  problem: "text-destructive-ink",
  quiet: "text-muted-foreground",
  waiting: "text-warning-ink",
};

/**
 * The one status line. Its tone is carried by the dot's SHAPE as well as its
 * hue (a hollow ring for "nothing to say yet"), so a greyscale screenshot
 * still separates confirmed from unconfirmed. The platform's own reason, when
 * there is one, is a tooltip — the Overview's state panel has the long form.
 */
function StatusLine({ headline }: { headline: EnvHeadline }) {
  const line = (
    <span
      className={cn("inline-flex min-w-0 items-center gap-1.5", TONE_TEXT[headline.tone])}
      data-testid="env-page-status"
      data-tone={headline.tone}
    >
      <span className={cn("h-2 w-2 shrink-0 rounded-full", TONE_DOT[headline.tone])} aria-hidden="true" />
      <span className="truncate">{headline.text}</span>
      {headline.detail !== "" && <span className="sr-only">. {headline.detail}</span>}
    </span>
  );
  if (headline.detail === "") return line;
  return (
    <Tooltip content={headline.detail} placement="bottom" wrapperClassName="min-w-0">
      {line}
    </Tooltip>
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
