// Copyright (c) 2025 Reliant Labs

/**
 * The Overview's environment list: ONE ROW PER ENVIRONMENT, answering the
 * questions a reader brings to a project before opening any environment —
 * where does each one run, what release is it on, is it healthy, when did it
 * last move, and what can I do to it.
 *
 * PURE PROPS, like every forge presentation component: rows and callbacks in,
 * no fetching, so the contract tests hand it object literals.
 *
 * It is a REAL table — each fact in its own `<td>` under its own
 * `<th scope="col">`, actions in a right-aligned column — for the reason the
 * old topology matrix learned the hard way: stacking facts into the row header
 * makes 200px rows that line up with nothing.
 *
 * The per-image digest matrix that used to BE the Releases page lives here as
 * a disclosure under a row that has images. It is detail, not the headline:
 * the headline is the environment.
 */

import { Fragment, useState } from "react";
import { AlertTriangle, ChevronDown, ChevronRight, Clock, Rocket, Upload } from "lucide-react";

import Badge from "@/components/forge-ui/badge";
import { Button } from "@/components/ui/Button";
import { Tooltip } from "@/components/ui/Tooltip";
import { cn } from "@/lib/utils";
import { offersShipping, type EnvFacts, type ForgeEnvSummary } from "@/services/forge/environments";
import { cellFor, endpointHost } from "@/services/forge/topology";

import { HealthChip, WhereBadge } from "../EnvBadges";
import { TopologyCell } from "../TopologyCell";

export interface EnvironmentRow {
  summary: ForgeEnvSummary;
  facts: EnvFacts;
}

export interface EnvironmentTableProps {
  rows: EnvironmentRow[];
  /** The release a promote would target (the project's latest). Absent = no promote offered. */
  promoteRelease?: string | null;
  /** Promote and deploy both go through forge on the daemon; false withdraws them. */
  canShip: boolean;
  onOpen: (env: string) => void;
  onPromote: (env: string) => void;
  onDeploy: (env: string) => void;
}

const HEADER_CELL =
  "whitespace-nowrap px-3 py-2 text-left text-2xs font-medium uppercase tracking-wide text-muted-foreground";
const CELL = "px-3 py-2.5 align-middle";

export function EnvironmentTable({
  rows,
  promoteRelease,
  canShip,
  onOpen,
  onPromote,
  onDeploy,
}: EnvironmentTableProps) {
  const [expanded, setExpanded] = useState<string | null>(null);

  return (
    <div className="overflow-x-auto rounded-lg border border-border bg-card" data-testid="forge-env-table">
      <table className="w-full border-collapse text-sm">
        <caption className="sr-only">
          Every environment in this project: where it runs, its release, its health, and when it last
          moved.
        </caption>
        <thead>
          <tr className="border-b border-border">
            <th scope="col" className={HEADER_CELL}>
              Environment
            </th>
            <th scope="col" className={HEADER_CELL}>
              Runs on
            </th>
            <th scope="col" className={HEADER_CELL}>
              Release
            </th>
            <th scope="col" className={HEADER_CELL}>
              Health
            </th>
            {/* "Promoted", never "deployed": promotion writes a pointer,
                deployment moves bytes. */}
            <th scope="col" className={HEADER_CELL}>
              Promoted
            </th>
            <th scope="col" className={cn(HEADER_CELL, "text-right")}>
              Actions
            </th>
          </tr>
        </thead>
        <tbody>
          {rows.map(({ summary, facts }) => {
            const images = summary.forge?.images ?? [];
            const open = expanded === summary.name;
            const shipping = canShip && offersShipping(summary);
            return (
              <Fragment key={summary.name}>
                <tr
                  data-testid={`env-row-${summary.name}`}
                  data-where={summary.where}
                  className={cn(
                    "border-b border-border/60 last:border-0",
                    open && "border-b-0"
                  )}
                >
                  <th scope="row" className={cn(CELL, "text-left font-normal")}>
                    <div className="flex items-center gap-1.5">
                      {images.length > 0 ? (
                        <button
                          type="button"
                          onClick={() => setExpanded(open ? null : summary.name)}
                          aria-expanded={open}
                          aria-label={`${open ? "Hide" : "Show"} ${summary.name}'s images`}
                          data-testid={`env-images-toggle-${summary.name}`}
                          className="rounded-sm text-muted-foreground hover:text-foreground focus:outline-none focus-visible:ring-2 focus-visible:ring-ring"
                        >
                          {open ? (
                            <ChevronDown className="h-3.5 w-3.5" aria-hidden="true" />
                          ) : (
                            <ChevronRight className="h-3.5 w-3.5" aria-hidden="true" />
                          )}
                        </button>
                      ) : (
                        <span className="inline-block w-3.5" aria-hidden="true" />
                      )}
                      {/* A real control — the actions are siblings, not nested in a clickable row. */}
                      <button
                        type="button"
                        onClick={() => onOpen(summary.name)}
                        aria-label={`Open ${summary.name}`}
                        data-testid={`env-open-${summary.name}`}
                        className="rounded-sm font-mono text-sm font-medium text-foreground hover:text-primary hover:underline focus:outline-none focus-visible:ring-2 focus-visible:ring-ring"
                      >
                        {summary.name}
                      </button>
                    </div>
                  </th>

                  <td className={CELL}>
                    <div className="flex min-w-0 flex-col gap-0.5">
                      <WhereBadge env={summary.name} where={summary.where} />
                      <RunsOnDetail summary={summary} />
                    </div>
                  </td>

                  <td className={cn(CELL, "whitespace-nowrap")}>
                    <ReleaseCell env={summary.name} facts={facts} />
                  </td>

                  <td className={CELL}>
                    <HealthChip env={summary.name} health={facts.health} />
                  </td>

                  <td className={cn(CELL, "whitespace-nowrap")}>
                    {facts.promotedAt ? (
                      <Tooltip content="When this environment was PROMOTED to the release — not when it was deployed. Promotion writes a pointer; deployment moves bytes.">
                        <span
                          data-testid={`promoted-${summary.name}`}
                          className="inline-flex items-center gap-1 text-xs text-muted-foreground"
                        >
                          <Clock className="h-3 w-3 shrink-0" aria-hidden="true" />
                          <span className="sr-only">promoted </span>
                          {formatTimestamp(facts.promotedAt)}
                        </span>
                      </Tooltip>
                    ) : (
                      <Dash label="never promoted" />
                    )}
                  </td>

                  <td className={cn(CELL, "whitespace-nowrap text-right")}>
                    {shipping ? (
                      <div className="inline-flex items-center justify-end gap-1.5">
                        {promoteRelease && (
                          <Button
                            variant="outline"
                            size="sm"
                            onClick={() => onPromote(summary.name)}
                            leftIcon={<Upload className="h-3 w-3" />}
                            aria-label={`Preview promoting ${summary.name} to ${promoteRelease}`}
                            data-testid={`promote-open-${summary.name}`}
                          >
                            Promote…
                          </Button>
                        )}
                        <Button
                          variant="outline"
                          size="sm"
                          onClick={() => onDeploy(summary.name)}
                          leftIcon={<Rocket className="h-3 w-3" />}
                          aria-label={`Preview deploying ${summary.name}`}
                          data-testid={`deploy-open-${summary.name}`}
                        >
                          Deploy…
                        </Button>
                      </div>
                    ) : summary.where === "local" ? (
                      <span className="text-xs text-muted-foreground">
                        runs via <code className="font-mono text-foreground">forge env up</code>
                      </span>
                    ) : (
                      <Dash label="no actions available" />
                    )}
                  </td>
                </tr>

                {open && images.length > 0 && (
                  <tr className="border-b border-border/60 last:border-0" data-testid={`env-images-${summary.name}`}>
                    <td colSpan={6} className="px-3 pb-3 pt-0">
                      {/* Inset inside the table's surface: bg-background recesses in both modes. */}
                      <div className="rounded-md border border-border/60 bg-background">
                        <table className="w-full border-collapse text-xs">
                          <caption className="sr-only">
                            Images in {summary.name}&apos;s release and whether each was verified.
                          </caption>
                          <thead>
                            <tr className="border-b border-border/60">
                              <th scope="col" className={HEADER_CELL}>
                                Image
                              </th>
                              <th scope="col" className={HEADER_CELL}>
                                State
                              </th>
                            </tr>
                          </thead>
                          <tbody>
                            {images.map((img) => (
                              <tr key={img.image} className="border-b border-border/40 last:border-0">
                                <th scope="row" className="px-3 py-1.5 text-left font-mono font-normal text-foreground">
                                  {img.image}
                                </th>
                                <TopologyCell env={summary.name} cell={cellFor(summary.forge!, img.image)} />
                              </tr>
                            ))}
                          </tbody>
                        </table>
                      </div>
                    </td>
                  </tr>
                )}
              </Fragment>
            );
          })}
        </tbody>
      </table>
    </div>
  );
}

/** The one-line "where exactly": a cluster context, a control-plane host, or a sentence. */
function RunsOnDetail({ summary }: { summary: ForgeEnvSummary }) {
  const forge = summary.forge;
  let text = "";
  if (summary.where === "local") text = "forge env up";
  else if (summary.where === "cloud") text = endpointHost(forge?.endpoint) || "this control plane";
  else if (forge?.kube_context) text = [forge.kube_context, forge.namespace].filter(Boolean).join(" · ");
  if (!text) return null;
  return (
    <span className="max-w-[16rem] truncate font-mono text-2xs text-muted-foreground" title={text}>
      {text}
    </span>
  );
}

function ReleaseCell({ env, facts }: { env: string; facts: EnvFacts }) {
  if (facts.binding === "local") {
    return <span className="text-xs text-muted-foreground">working tree</span>;
  }
  if (!facts.release) {
    return (
      <Badge
        label={facts.binding === "undeclared" ? "not declared here" : "never promoted"}
        variant="neutral"
        size="sm"
      />
    );
  }
  return (
    <div className="flex flex-col gap-0.5">
      <span className="inline-flex items-center gap-1.5">
        <span className="font-mono text-sm text-foreground">{facts.release}</span>
        {facts.rolledBack && <Badge label="rolled back" variant="warning" size="sm" />}
        {facts.dirty && (
          <Tooltip content="This release was cut from a tree with uncommitted changes. The bytes it ships correspond to no reviewable commit.">
            <span data-testid={`dirty-${env}`} className="inline-flex items-center gap-1 text-2xs text-foreground">
              <AlertTriangle className="h-3 w-3 text-destructive" aria-hidden="true" />
              dirty tree
            </span>
          </Tooltip>
        )}
      </span>
      {facts.lag && <span className="text-2xs text-muted-foreground">{facts.lag}</span>}
    </div>
  );
}

function Dash({ label }: { label: string }) {
  return (
    <span className="text-xs text-muted-foreground">
      <span aria-hidden="true">—</span>
      <span className="sr-only">{label}</span>
    </span>
  );
}

/** RFC3339 → local; the raw string rather than "Invalid Date" when unparseable. */
export function formatTimestamp(value: string): string {
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
