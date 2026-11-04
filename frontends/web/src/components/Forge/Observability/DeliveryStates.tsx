// Copyright (c) 2025 Reliant Labs

/**
 * The answers the release ledger gives on purpose, worded for what each asks
 * of the reader (errors.ts deliveryErrorKind):
 *
 *   not-member       the org is closed to them; nothing on this page helps.
 *   env-unavailable  an environment in view is missing, deleted or another
 *                    org's; the server will not say which, and neither do we.
 *                    Widening to every environment of the project does help.
 *
 * Anything else is an ordinary failure, shown as every observability view
 * shows one.
 */

import type { ReactNode } from "react";
import { Lock, ServerOff } from "lucide-react";

import Button from "@/components/forge-ui/button";
import { deliveryErrorKind } from "@/services/controlPlane/observability/errors";

import { ObsError } from "./ObsStates";

function Frame({ icon, title, testId, children, actions }: { icon: ReactNode; title: string; testId: string; children: ReactNode; actions?: ReactNode }) {
  return (
    <div data-testid={testId} role="status" className="flex items-start gap-3 rounded-lg border border-dashed border-border bg-surface-sunken px-4 py-5">
      <span className="mt-0.5 shrink-0 text-ink-muted" aria-hidden="true">
        {icon}
      </span>
      <div className="min-w-0 space-y-1.5">
        <p className="text-sm font-medium text-ink">{title}</p>
        <div className="space-y-1.5 text-xs text-ink-muted">{children}</div>
        {actions && <div className="flex flex-wrap gap-2 pt-1">{actions}</div>}
      </div>
    </div>
  );
}

export function DeliveryError({
  error,
  subject,
  onShowEveryEnvironment,
  onRetry,
  retrying,
}: {
  error: unknown;
  subject: string;
  /** The project area: drop the environment filter. Absent on an env page, where the environment is the page. */
  onShowEveryEnvironment?: () => void;
  onRetry?: () => void;
  retrying?: boolean;
}) {
  const kind = deliveryErrorKind(error);
  if (kind === "not-member") {
    return (
      <Frame icon={<Lock className="h-4 w-4" />} title="You're not a member of this organization" testId="obs-delivery-not-member">
        <p>
          Releases and delivery metrics come from the organization&apos;s release ledger, which only its members can read. Ask one of its
          admins to invite you, or switch to an organization you belong to.
        </p>
      </Frame>
    );
  }
  if (kind === "env-unavailable") {
    return (
      <Frame
        icon={<ServerOff className="h-4 w-4" />}
        title="An environment in view isn't available to this organization"
        testId="obs-delivery-env-unavailable"
        actions={
          onShowEveryEnvironment ? (
            <Button variant="outline" size="sm" onClick={onShowEveryEnvironment} data-testid="obs-delivery-show-all-envs">
              Show every environment
            </Button>
          ) : undefined
        }
      >
        <p>
          It may have been deleted, or the link you followed names an environment of another organization. Reliant doesn&apos;t say which,
          so that no one can learn about another organization&apos;s environments by asking.
        </p>
        {!onShowEveryEnvironment && (
          <p>
            If <span className="font-mono">{subject}</span> was deleted, its releases went with it.
          </p>
        )}
      </Frame>
    );
  }
  return <ObsError error={error} subject={subject} onRetry={onRetry} retrying={retrying} />;
}
