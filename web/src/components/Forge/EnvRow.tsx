// Copyright (c) 2025 Reliant Labs

/**
 * One environment's row in the topology matrix.
 *
 * ONE FACT PER CELL. This row used to stack the release, the lag, the promote
 * time, the cluster, the note, the action buttons AND both dialogs into a single
 * `<th>` with a `flex-col`, which made every row ~200px tall while the image
 * columns sat on one baseline — a table in name only. Each fact now has its own
 * `<td>` under a real `<th scope="col">` in TopologyView, so a row is one line
 * and the columns actually line up. The env column stays sticky so row identity
 * survives horizontal scrolling on a project with many images.
 *
 * The facts an operator reads before looking at any cell are what the env
 * column carries, plus the two caveats that are invisible everywhere else —
 *
 *   PROMOTE time, labelled as such. `promoted_at` looks like a deploy timestamp
 *   and is not one: promotion writes a pointer, deployment moves bytes, and the
 *   gap between them being invisible is the entire reason `forge env verify`
 *   exists. The caveat survives the move into its own column three ways: the
 *   column header says "Promoted", the cell carries the word for a screen
 *   reader reading it out of context, and the tooltip states it in full.
 *
 *   DIRTY tree. A release cut from a tree with uncommitted changes ships bytes
 *   that correspond to no reviewable commit. This row is the only place that
 *   fact surfaces in the UI, so it stays a hard `Badge` — never a tooltip-only
 *   hint.
 *
 * The two non-active binding states get their own treatment rather than being
 * dropped: `unbound` (never promoted) is NORMAL and says so, and `undeclared`
 * (bound in the ledger, but no deploy/kcl/<env>/ in this checkout) is a real
 * reportable state that carries forge's own explanation as VISIBLE text, not
 * only as a tooltip.
 */

import { useState } from "react";
import { AlertTriangle, ChevronRight, Clock, RefreshCw, Rocket, Upload } from "lucide-react";

import { cn } from "@/lib/utils";
import { Badge } from "@/components/ui/Badge";
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
  /**
   * Navigate to this env's status screen. Absent = the env name is inert text
   * rather than a control, which is the correct rendering when the host screen
   * has nowhere to send the reader.
   */
  onOpenEnv?: (env: string) => void;
}

/** Shared padding so every cell in the row sits on the same baseline. */
const CELL = "px-3 py-2 align-middle";

export function EnvRow({
  env,
  images,
  onVerify,
  isVerifying,
  verifyNotice,
  projectId,
  promoteRelease,
  onOpenEnv,
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

  const cluster = [env.kube_context, env.namespace].filter(Boolean).join(" · ");

  return (
    <tr
      // bg-card on the ROW (not on the sticky cell) is what makes `bg-inherit`
      // below work: a sticky cell must be opaque or the scrolling columns show
      // through it, and inheriting is the only way it can be opaque AND pick up
      // the row's hover tint. bg-muted here is interaction state, which is the
      // one thing --muted is for.
      className="border-b border-border/60 bg-card last:border-0 hover:bg-muted/40"
      data-testid={`env-row-${env.env}`}
    >
      {/* Environment — sticky, so which row you are reading survives a
          horizontal scroll through the image columns. */}
      <th
        scope="row"
        className={cn(CELL, "sticky left-0 z-10 bg-inherit text-left font-normal")}
      >
        <div className="flex items-center gap-2">
          {onOpenEnv ? (
            // A real control, not a row-level click handler: Promote…/Deploy…
            // are siblings in the actions column and nesting them inside a
            // clickable row would make two overlapping hit targets.
            <button
              type="button"
              onClick={() => onOpenEnv(env.env)}
              aria-label={`View ${env.env} status`}
              className={cn(
                "group/env inline-flex items-center gap-1 rounded-sm font-mono text-sm font-medium",
                "text-foreground hover:text-primary hover:underline",
                "focus:outline-none focus-visible:ring-2 focus-visible:ring-ring"
              )}
            >
              {env.env}
              <ChevronRight
                className="h-3.5 w-3.5 shrink-0 text-muted-foreground group-hover/env:text-primary"
                aria-hidden="true"
              />
            </button>
          ) : (
            <span className="font-mono text-sm font-medium text-foreground">{env.env}</span>
          )}

          {binding === "unbound" && (
            // Never promoted. Explicitly not a problem — it has declared
            // nothing that could be wrong.
            <Badge
              variant="outline"
              size="sm"
              data-testid={`binding-${env.env}`}
              className="whitespace-nowrap font-sans text-muted-foreground"
            >
              never promoted
            </Badge>
          )}

          {binding === "undeclared" && (
            <Badge
              variant="outline"
              size="sm"
              data-testid={`binding-${env.env}`}
              className="whitespace-nowrap font-sans text-muted-foreground"
            >
              not declared here
            </Badge>
          )}

          {dirty && (
            // A dirty-tree release corresponds to no reviewable commit. This
            // is the only place that surfaces, so it is a hard badge, not a
            // tooltip-only hint.
            <Tooltip content="This release was cut from a tree with uncommitted changes. The bytes it ships correspond to no reviewable commit.">
              <Badge
                variant="destructive"
                size="sm"
                data-testid={`dirty-${env.env}`}
                className="whitespace-nowrap font-sans"
              >
                <AlertTriangle className="mr-1 h-3 w-3 shrink-0" aria-hidden="true" />
                dirty tree
              </Badge>
            </Tooltip>
          )}
        </div>
      </th>

      {/* Release — an identifier, so mono. */}
      <td className={cn(CELL, "whitespace-nowrap")}>
        {env.release ? (
          <span className="font-mono text-sm text-foreground">{env.release}</span>
        ) : (
          <span className="text-sm text-muted-foreground">
            <span aria-hidden="true">—</span>
            <span className="sr-only">no release bound</span>
          </span>
        )}
      </td>

      {/* Status — how far behind, plus forge's own explanation when the env is
          in a state that would otherwise look like missing data. The note is
          VISIBLE text, not just a tooltip: it is the only thing that explains
          an undeclared env. */}
      <td className={cn(CELL, "max-w-xs")}>
        <div className="flex flex-col gap-0.5">
          {lag && (
            <span
              className={cn(
                "text-xs",
                env.lag?.current ? "text-muted-foreground" : "text-warning"
              )}
            >
              {lag}
            </span>
          )}
          {binding !== "active" && env.note && (
            <span className="truncate text-xs text-muted-foreground" title={env.note}>
              {env.note}
            </span>
          )}
          {verifyNotice && (
            <span
              data-testid={`verify-notice-${env.env}`}
              className="truncate text-xs text-muted-foreground"
              title={verifyNotice}
            >
              {verifyNotice}
            </span>
          )}
          {!lag && !verifyNotice && !(binding !== "active" && env.note) && (
            <span className="text-xs text-muted-foreground">
              <span aria-hidden="true">—</span>
              <span className="sr-only">no lag reported</span>
            </span>
          )}
        </div>
      </td>

      {/* Promoted — never "deployed". See the file comment. */}
      <td className={cn(CELL, "whitespace-nowrap")}>
        {env.promoted_at ? (
          <Tooltip content="When this environment was PROMOTED to the release — not when it was deployed. Promotion writes a pointer; deployment moves bytes. Verify an environment to find out what is actually running.">
            <span
              data-testid={`promoted-${env.env}`}
              className="inline-flex items-center gap-1 text-xs text-muted-foreground"
            >
              <Clock className="h-3 w-3 shrink-0" aria-hidden="true" />
              {/* The word travels with the value, so a screen reader that reads
                  this cell out of its column still gets the caveat. */}
              <span className="sr-only">promoted </span>
              {formatTimestamp(env.promoted_at)}
            </span>
          </Tooltip>
        ) : (
          <span className="text-xs text-muted-foreground">
            <span aria-hidden="true">—</span>
            <span className="sr-only">never promoted</span>
          </span>
        )}
      </td>

      {/* Cluster — kube context and namespace are identifiers, so mono. */}
      <td className={cn(CELL, "max-w-xs")}>
        {cluster ? (
          <span
            className="block truncate font-mono text-xs text-muted-foreground"
            title={cluster}
          >
            {cluster}
          </span>
        ) : (
          <span className="text-xs text-muted-foreground">
            <span aria-hidden="true">—</span>
            <span className="sr-only">no cluster resolved</span>
          </span>
        )}
      </td>

      {images.map((image) => (
        <TopologyCell key={image} env={env.env} cell={cellFor(env, image)} />
      ))}

      {/* Actions — a real column, right-aligned, with real buttons. Verify is
          per-env on purpose: one cluster, a 75s budget, and a slow cluster here
          cannot keep every other row unverifiable. */}
      <td className={cn(CELL, "whitespace-nowrap text-right")}>
        <div className="inline-flex items-center justify-end gap-1.5">
          {binding === "active" && (
            <Button
              variant="outline"
              size="sm"
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
              variant="outline"
              size="sm"
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
              variant="outline"
              size="sm"
              onClick={() => setDeployOpen(true)}
              leftIcon={<Rocket className="h-3 w-3" />}
              aria-label={`Preview deploying ${env.env} to its declared cluster`}
              data-testid={`deploy-open-${env.env}`}
            >
              Deploy…
            </Button>
          )}
        </div>

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
      </td>
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
