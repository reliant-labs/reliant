// Copyright (c) 2025 Reliant Labs

/**
 * The Overview's environment list: ONE ROW PER ENVIRONMENT, answering the
 * questions a reader brings to a project before opening any of them — where
 * does each run, what is it on, WHERE DID THAT COME FROM, is it healthy, and
 * when did it last move.
 *
 * Every column is read from the control plane (services/forge/live.ts), so the
 * whole table renders with the daemon offline. The per-image digest matrix
 * that used to hang off these rows was forge's, read on the daemon, and it
 * belongs to Preview now.
 *
 * PURE PROPS, like every forge presentation component: rows and callbacks in,
 * no fetching, so the contract tests hand it object literals.
 *
 * It is a REAL table — each fact in its own `<td>` under its own
 * `<th scope="col">`, actions in a right-aligned column — for the reason the
 * old topology matrix learned the hard way: stacking facts into the row header
 * makes 200px rows that line up with nothing.
 */

import { ChevronRight, Clock } from "lucide-react";

import Badge from "@/components/forge-ui/badge";
import { Button } from "@/components/ui/Button";
import { Tooltip } from "@/components/ui/Tooltip";
import { cn } from "@/lib/utils";
import type { CloudEnvStatus } from "@/services/forge/cloudEnvs";
import {
  declaredNotBuilt,
  isPlacedKind,
  isQueued,
  liveKindLabel,
  queuedOnLabel,
  type LiveEnv,
} from "@/services/forge/live";

import { HealthChip } from "../EnvBadges";

export interface EnvironmentRow {
  env: LiveEnv;
  /** The platform's observation. Only ever present for a placed env. */
  status: CloudEnvStatus | undefined;
  statusLoading: boolean;
}

export interface EnvironmentTableProps {
  rows: EnvironmentRow[];
  onOpen: (env: string) => void;
}

const HEADER_CELL =
  "whitespace-nowrap px-3 py-2 text-left text-2xs font-medium uppercase tracking-wide text-muted-foreground";
const CELL = "px-3 py-2.5 align-middle";

export function EnvironmentTable({ rows, onOpen }: EnvironmentTableProps) {
  return (
    <div className="overflow-x-auto rounded-lg border border-border bg-card" data-testid="forge-env-table">
      <table className="w-full border-collapse text-sm">
        <caption className="sr-only">
          Every environment in this project: where it runs, its release, where that release came
          from, its health, and when it last moved.
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
              <span className="sr-only">Open</span>
            </th>
          </tr>
        </thead>
        <tbody>
          {rows.map(({ env, status, statusLoading }) => (
            <tr
              key={env.name}
              data-testid={`env-row-${env.name}`}
              data-kind={env.kind}
              className="border-b border-border/60 last:border-0"
            >
              <th scope="row" className={cn(CELL, "text-left font-normal")}>
                <button
                  type="button"
                  onClick={() => onOpen(env.name)}
                  aria-label={`Open ${env.name}`}
                  data-testid={`env-open-${env.name}`}
                  className="rounded-sm font-mono text-sm font-medium text-foreground hover:text-primary hover:underline focus:outline-none focus-visible:ring-2 focus-visible:ring-ring"
                >
                  {env.name}
                </button>
              </th>

              <td className={CELL}>
                <Badge label={liveKindLabel(env.kind)} variant="neutral" size="sm" />
              </td>

              <td className={cn(CELL, "whitespace-nowrap")}>
                <ReleaseCell env={env} />
              </td>

              <td className={CELL}>
                {/* Health is an OBSERVATION, and only the platform makes one.
                    A self-managed env is applied by forge with no server-side
                    observer, so the honest cell is "not reported" — never a
                    green chip inferred from a promotion. */}
                {isPlacedKind(env.kind) ? (
                  <HealthChip
                    env={env.name}
                    health={{
                      kind: "hosted",
                      verdict: status?.verdict ?? "unknown",
                      loading: statusLoading && !status,
                    }}
                  />
                ) : (
                  <Dash label="not observed from here" />
                )}
              </td>

              <td className={cn(CELL, "whitespace-nowrap")}>
                {env.promotedAt ? (
                  <Tooltip content="When this environment was PROMOTED to the release — not when it was deployed. Promotion writes a pointer; deployment moves bytes.">
                    <span
                      data-testid={`promoted-${env.name}`}
                      className="inline-flex items-center gap-1 text-xs text-muted-foreground"
                    >
                      <Clock className="h-3 w-3 shrink-0" aria-hidden="true" />
                      <span className="sr-only">promoted </span>
                      {formatTimestamp(env.promotedAt)}
                    </span>
                  </Tooltip>
                ) : (
                  <Dash label="never promoted" />
                )}
              </td>

              <td className={cn(CELL, "whitespace-nowrap text-right")}>
                {/* Promote, deploy and history live on the environment's page,
                    in its header, where they can say why one is unavailable.
                    A row button that opened a daemon-only tab promised an
                    action it could not take from here. */}
                <Button
                  variant="ghost"
                  size="sm"
                  onClick={() => onOpen(env.name)}
                  data-testid={`env-row-open-${env.name}`}
                  aria-label={`Open ${env.name}`}
                  rightIcon={<ChevronRight className="h-3.5 w-3.5" />}
                >
                  Open
                </Button>
              </td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}

function ReleaseCell({ env }: { env: LiveEnv }) {
  if (env.kind === "local") {
    return <span className="text-xs text-muted-foreground">working tree</span>;
  }
  if (env.release === "") {
    return (
      <Badge
        label={declaredNotBuilt(env) ? "declared, not built" : "never promoted"}
        variant="neutral"
        size="sm"
      />
    );
  }
  return (
    <div className="flex flex-col gap-0.5">
      <span className="flex items-center gap-2">
        <span className="font-mono text-sm text-foreground">{env.release}</span>
        {/* The release is RECORDED but not rolling out: it waits on a person.
            Said here, on the release it is about, so a reader does not take
            the version for what is running. */}
        {isQueued(env) && (
          <Tooltip content="Queued: this release is recorded and goes out on its own once that is resolved. Open the environment for what to do.">
            <span data-testid={`queued-${env.name}`}>
              <Badge label={`Waiting on ${queuedOnLabel(env.holds)}`} variant="warning" size="sm" dot />
            </span>
          </Tooltip>
        )}
      </span>
      {/* The provenance line (§2.1) — images from the release's source, config
          from the render's — is the fact this screen gained. Truncated here
          and shown in full on the environment's page. */}
      {env.provenance !== "" && (
        <span
          data-testid={`provenance-${env.name}`}
          className="max-w-[22rem] truncate font-mono text-2xs text-muted-foreground"
          title={env.provenance}
        >
          {env.provenance}
        </span>
      )}
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
