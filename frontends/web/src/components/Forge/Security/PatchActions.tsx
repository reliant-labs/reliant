// Copyright (c) 2025 Reliant Labs

/**
 * Patch and Patch with agent: start the vuln-patch workflow for a selection of
 * findings (the Vulnerabilities table) or one advisory (its page), then link
 * to the run. Both buttons are always shown; when they cannot start, they are
 * disabled and the reason is said beside them, never left for the reader to
 * guess (no linked repository, read-only access, no GitHub, every environment
 * in scope, nothing fixable selected).
 */

import { useMutation } from "@tanstack/react-query";
import { ConnectError } from "@connectrpc/connect";

import Button from "@/components/forge-ui/button";
import Link from "@/components/forge-ui/link";

import type { PatchMode, PatchPlan, VulnPatchAccess } from "./vulnPatchModel";
import { startVulnPatch, type StartedVulnPatch, type VulnPatchStart } from "./vulnPatchRuntime";

export interface PatchActionsProps {
  access: VulnPatchAccess;
  plan: PatchPlan;
}

const startError = (error: unknown) => (error instanceof ConnectError ? error.rawMessage : error instanceof Error ? error.message : String(error));

export function PatchActions({ access, plan }: PatchActionsProps) {
  const start = useMutation<StartedVulnPatch, unknown, VulnPatchStart>({ mutationFn: startVulnPatch });
  const ready = access.state === "ready" && plan.ok ? { access, plan } : null;
  const reason =
    access.state === "checking"
      ? "Checking whether this project can be patched…"
      : access.state === "blocked"
        ? access.reason
        : !plan.ok
          ? plan.reason
          : null;

  const run = (mode: PatchMode) => {
    if (!ready) return;
    start.mutate({ projectId: ready.access.projectId, projectPath: ready.access.projectPath, env: ready.plan.env, advisories: ready.plan.advisories, mode });
  };
  const disabled = !ready || start.isPending;
  const pendingMode = start.isPending ? start.variables?.mode : undefined;

  return (
    <div className="space-y-1.5" data-testid="vuln-patch">
      <div className="flex flex-wrap items-center gap-2">
        <Button
          variant="primary"
          size="sm"
          onClick={() => run("deterministic")}
          disabled={disabled}
          loading={pendingMode === "deterministic"}
          title={reason ?? "Bump each vulnerable package to its fix; an agent takes over only if the bump breaks the build or tests"}
          data-testid="vuln-patch-deterministic"
        >
          Patch
        </Button>
        <Button
          variant="outline"
          size="sm"
          onClick={() => run("agent")}
          disabled={disabled}
          loading={pendingMode === "agent"}
          title={reason ?? "Let an agent make the whole patch: for a major-version fix, or one the bump refused"}
          data-testid="vuln-patch-agent"
        >
          Patch with agent
        </Button>
        {reason ? (
          <span className="text-meta text-ink-muted" data-testid="vuln-patch-reason">
            {reason}
          </span>
        ) : ready ? (
          <span className="text-meta text-ink-muted" data-testid="vuln-patch-scope">
            {ready.plan.advisories.length === 1 ? ready.plan.advisories[0] : `${ready.plan.advisories.length} advisories`} in {ready.plan.env}
            {ready.plan.withoutFix > 0 && `; ${ready.plan.withoutFix} selected ${ready.plan.withoutFix === 1 ? "has" : "have"} no fix and ${ready.plan.withoutFix === 1 ? "is" : "are"} left out`}
          </span>
        ) : null}
      </div>
      {start.isSuccess && (
        <p role="status" className="text-ui text-ink" data-testid="vuln-patch-started">
          Patch started. It opens a pull request once the patch passes the project&apos;s checks.{" "}
          <Link href={start.data.href} className="text-primary-ink underline-offset-2 hover:underline" data-testid="vuln-patch-run-link">
            Open the run
          </Link>
        </p>
      )}
      {start.isError && (
        <p role="alert" className="text-ui text-danger-ink" data-testid="vuln-patch-error">
          The patch did not start: {startError(start.error)}
        </p>
      )}
    </div>
  );
}
