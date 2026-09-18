// Copyright (c) 2025 Reliant Labs

/**
 * One environment's row in the topology matrix.
 *
 * The left column carries the facts an operator reads before looking at any
 * cell: what the env is bound to, how far behind it is, where it runs, and the
 * two caveats that are invisible everywhere else —
 *
 *   PROMOTE time, labelled as such. `promoted_at` looks like a deploy timestamp
 *   and is not one: promotion writes a pointer, deployment moves bytes, and the
 *   gap between them being invisible is the entire reason `forge env verify`
 *   exists. Forge's own text output carries the caveat inline; so does this.
 *
 *   DIRTY tree. A release cut from a tree with uncommitted changes ships bytes
 *   that correspond to no reviewable commit. This row is the only place that
 *   fact surfaces in the UI.
 *
 * The two non-active binding states get their own treatment rather than being
 * dropped: `unbound` (never promoted) is NORMAL and says so, and `undeclared`
 * (bound in the ledger, but no deploy/kcl/<env>/ in this checkout) is a real
 * reportable state that carries forge's own explanation.
 */

import { useState } from "react";
import { AlertTriangle, Clock, RefreshCw, Rocket, Upload } from "lucide-react";

import { cn } from "@/lib/utils";
import { Button } from "@/components/ui/Button";
import { Tooltip } from "@/components/ui/Tooltip";
import {
  cellFor,
  describeLag,
  envBindingState,
  isDirtyRelease,
  type ForgeTopologyEnv,
} from "@/services/forge/topology";

import { DeployDialog } from "./Deploy/DeployDialog";
import { PromoteDialog } from "./Promote/PromoteDialog";
import { TopologyCell } from "./TopologyCell";

export interface EnvRowProps {
  env: ForgeTopologyEnv;
  images: string[];
  onVerify: (env: string) => void;
  isVerifying: boolean;
  /** Set when this env's last verify returned something other than a report. */
  verifyNotice?: string | null;
  /**
   * Needed to plan a promote. Absent means the promote entry point is not
   * offered at all — which is the correct behaviour, since a promote cannot be
   * planned without knowing the project.
   */
  projectId?: string | null;
  /**
   * The release a promote from this row would target, normally the project's
   * latest. Absent means no entry point: this row will not invent a target.
   */
  promoteRelease?: string | null;
}

export function EnvRow({
  env,
  images,
  onVerify,
  isVerifying,
  verifyNotice,
  projectId,
  promoteRelease,
}: EnvRowProps) {
  const binding = envBindingState(env);
  const lag = describeLag(env.lag);
  const dirty = isDirtyRelease(env);

  /**
   * The promote flow is a DIALOG, never an action on this row.
   *
   * That is the guard, not a layout preference: the write is reachable only from
   * a rendered plan, so the row's job is to open a preview and nothing more.
   * Clicking here computes a read-only plan — it writes nothing, and the confirm
   * step does not exist until that plan is on screen.
   */
  const [promoteOpen, setPromoteOpen] = useState(false);
  const canPromote = !!projectId && !!promoteRelease;

  /**
   * The deploy flow is a DIALOG for the same structural reason, and the stakes
   * are higher: a deploy applies manifests to a live cluster. Clicking here
   * computes a read-only `--dry-run` preview and nothing else — the confirm step
   * does not exist until that plan is on screen and nothing blocks it.
   *
   * Offered for every env with a project, including one that has never been
   * promoted: an unbound env deploys by resolved tag, which is the case the
   * confirmation token's expect_unbound half exists for. What is NOT offered is a
   * deploy from a row that cannot name a project — a plan cannot be computed
   * without one, and this row will not invent a target.
   */
  const [deployOpen, setDeployOpen] = useState(false);
  const canDeploy = !!projectId;

  return (
    <tr className="border-b border-border last:border-0" data-testid={`env-row-${env.env}`}>
      <th
        scope="row"
        className="sticky left-0 z-10 bg-background px-3 py-2 text-left align-top font-normal"
      >
        <div className="flex flex-col gap-1">
          <div className="flex items-center gap-2">
            <span className="font-mono text-sm font-medium text-foreground">{env.env}</span>
            {binding === "unbound" && (
              // Never promoted. Explicitly not a problem — it has declared
              // nothing that could be wrong.
              <span
                data-testid={`binding-${env.env}`}
                className="rounded-full border border-dashed border-border px-2 py-0.5 text-2xs text-muted-foreground"
              >
                never promoted
              </span>
            )}
            {binding === "undeclared" && (
              <Tooltip
                content={
                  env.note ||
                  "This environment is bound in the release ledger, but this checkout has no deploy/kcl/<env>/ for it, so its cluster and namespace cannot be resolved."
                }
              >
                <span
                  data-testid={`binding-${env.env}`}
                  className="rounded-full border border-dashed border-border px-2 py-0.5 text-2xs text-muted-foreground"
                >
                  not declared here
                </span>
              </Tooltip>
            )}
            {dirty && (
              // A dirty-tree release corresponds to no reviewable commit. This
              // is the only place that surfaces, so it is a hard badge, not a
              // tooltip-only hint.
              <Tooltip content="This release was cut from a tree with uncommitted changes. The bytes it ships correspond to no reviewable commit.">
                <span
                  data-testid={`dirty-${env.env}`}
                  className="inline-flex items-center gap-1 rounded-full bg-destructive/15 px-2 py-0.5 text-2xs text-destructive"
                >
                  <AlertTriangle className="h-3 w-3" aria-hidden="true" />
                  dirty tree
                </span>
              </Tooltip>
            )}
          </div>

          {env.release && (
            <span className="font-mono text-xs text-muted-foreground">{env.release}</span>
          )}

          {lag && (
            <span
              className={cn(
                "text-2xs",
                env.lag?.current ? "text-muted-foreground" : "text-warning"
              )}
            >
              {lag}
            </span>
          )}

          {env.promoted_at && (
            // "Promoted" is the label, never "deployed". See the file comment.
            <Tooltip content="When this environment was PROMOTED to the release — not when it was deployed. Promotion writes a pointer; deployment moves bytes. Verify an environment to find out what is actually running.">
              <span
                data-testid={`promoted-${env.env}`}
                className="inline-flex items-center gap-1 text-2xs text-muted-foreground"
              >
                <Clock className="h-3 w-3" aria-hidden="true" />
                promoted {formatTimestamp(env.promoted_at)}
              </span>
            </Tooltip>
          )}

          {(env.kube_context || env.namespace) && (
            <span className="truncate font-mono text-2xs text-muted-foreground/80">
              {env.kube_context}
              {env.kube_context && env.namespace ? " · " : ""}
              {env.namespace}
            </span>
          )}

          {binding !== "active" && env.note && (
            <span className="text-2xs text-muted-foreground">{env.note}</span>
          )}

          {/* Verify is per-env on purpose: one cluster, a 75s budget, and a slow
              cluster here cannot keep every other row unverifiable. */}
          <div className="flex flex-wrap items-center gap-1 pt-1">
            {binding === "active" && (
              <Button
                variant="ghost"
                size="xs"
                onClick={() => onVerify(env.env)}
                loading={isVerifying}
                disabled={isVerifying}
                leftIcon={<RefreshCw className="h-3 w-3" />}
                aria-label={`Verify ${env.env} against its live cluster`}
              >
                {isVerifying ? "Reading cluster…" : "Verify"}
              </Button>
            )}

            {/* Offered for unbound envs too — a first promote is a normal
                operation, and it is the case the confirmation token's
                expect_unbound half exists for. */}
            {canPromote && (
              <Button
                variant="ghost"
                size="xs"
                onClick={() => setPromoteOpen(true)}
                leftIcon={<Upload className="h-3 w-3" />}
                aria-label={`Preview promoting ${env.env} to ${promoteRelease}`}
                data-testid={`promote-open-${env.env}`}
              >
                Promote…
              </Button>
            )}

            {/* Deploy sits beside promote because they are the two halves of
                shipping, and the pair being adjacent is what makes the difference
                legible: promote moves a pointer in the repo, deploy moves bytes to
                a cluster. The label carries the ellipsis for the same reason
                promote's does — this opens a preview, it does not deploy. */}
            {canDeploy && (
              <Button
                variant="ghost"
                size="xs"
                onClick={() => setDeployOpen(true)}
                leftIcon={<Rocket className="h-3 w-3" />}
                aria-label={`Preview deploying ${env.env} to its declared cluster`}
                data-testid={`deploy-open-${env.env}`}
              >
                Deploy…
              </Button>
            )}
          </div>

          {verifyNotice && (
            <span data-testid={`verify-notice-${env.env}`} className="text-2xs text-muted-foreground">
              {verifyNotice}
            </span>
          )}

          {/* Mounted only while open, so a closed dialog holds no plan and
              therefore no confirmation token from a previous session. */}
          {canPromote && promoteOpen && (
            <PromoteDialog
              isOpen={promoteOpen}
              onClose={() => setPromoteOpen(false)}
              projectId={projectId}
              env={env.env}
              release={promoteRelease as string}
            />
          )}

          {/* Mounted only while open, so a closed dialog holds no plan and
              therefore no confirmation token — and in particular no remembered
              declared context — from a previous session. */}
          {canDeploy && deployOpen && (
            <DeployDialog
              isOpen={deployOpen}
              onClose={() => setDeployOpen(false)}
              projectId={projectId}
              env={env.env}
            />
          )}
        </div>
      </th>

      {images.map((image) => (
        <TopologyCell key={image} env={env.env} cell={cellFor(env, image)} />
      ))}
    </tr>
  );
}

/**
 * formatTimestamp renders an RFC3339 stamp locally, and falls back to the raw
 * string rather than to "Invalid Date" when forge sends something unparseable.
 */
function formatTimestamp(value: string): string {
  const parsed = new Date(value);
  if (Number.isNaN(parsed.getTime())) return value;
  return parsed.toLocaleString(undefined, {
    year: "numeric",
    month: "short",
    day: "numeric",
    hour: "2-digit",
    minute: "2-digit",
  });
}
