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
 * It is a REAL table (forge-ui's DataTable) — each fact in its own column,
 * actions in a right-aligned one — for the reason the old topology matrix
 * learned the hard way: stacking facts into the row header makes 200px rows
 * that line up with nothing. The whole row opens the environment.
 */

import { ChevronRight, Clock } from "lucide-react";

import Badge from "@/components/forge-ui/badge";
import Button from "@/components/forge-ui/button";
import DataTable, { type DataTableColumn } from "@/components/forge-ui/data_table";
import Tooltip from "@/components/forge-ui/tooltip";
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

type EnvTableRow = { name: string; row: EnvironmentRow };

export function EnvironmentTable({ rows, onOpen }: EnvironmentTableProps) {
  const columns: DataTableColumn<EnvTableRow>[] = [
    {
      key: "name",
      header: "Environment",
      className: "font-mono font-medium",
      render: (_, { row }) => row.env.name,
    },
    {
      key: "runsOn",
      header: "Runs on",
      render: (_, { row }) => <Badge label={liveKindLabel(row.env.kind)} variant="neutral" size="sm" />,
    },
    { key: "release", header: "Release", render: (_, { row }) => <ReleaseCell env={row.env} /> },
    {
      key: "health",
      header: "Health",
      // Health is an OBSERVATION, and only the platform makes one. A
      // self-managed env is applied by forge with no server-side observer, so
      // the honest cell is "not reported" — never a green chip inferred from a
      // promotion.
      render: (_, { row: { env, status, statusLoading } }) =>
        isPlacedKind(env.kind) ? (
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
        ),
    },
    {
      // "Promoted", never "deployed": promotion writes a pointer, deployment moves bytes.
      key: "promoted",
      header: "Promoted",
      interactive: true,
      render: (_, { row: { env } }) =>
        env.promotedAt ? (
          <Tooltip content="When this environment was PROMOTED to the release — not when it was deployed. Promotion writes a pointer; deployment moves bytes.">
            <span data-testid={`promoted-${env.name}`} className="inline-flex items-center gap-1 text-xs text-ink-muted">
              <Clock className="h-3 w-3 shrink-0" aria-hidden="true" />
              <span className="sr-only">promoted </span>
              {formatTimestamp(env.promotedAt)}
            </span>
          </Tooltip>
        ) : (
          <Dash label="never promoted" />
        ),
    },
    {
      // Promote, deploy and history live on the environment's page, in its
      // header, where they can say why one is unavailable.
      key: "open",
      header: "Open",
      srHeader: true,
      interactive: true,
      className: "text-right",
      render: (_, { row: { env } }) => (
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
      ),
    },
  ];
  return (
    <div data-testid="forge-env-table">
      <DataTable<EnvTableRow>
        stacked
        ariaLabel="Environments in this project"
        columns={columns}
        data={rows.map((row) => ({ name: row.env.name, row }))}
        getRowKey={(r) => r.name}
        getRowAction={(r) => () => onOpen(r.name)}
        getRowProps={({ row }) => ({ "data-testid": `env-row-${row.env.name}`, "data-kind": row.env.kind })}
      />
    </div>
  );
}

function ReleaseCell({ env }: { env: LiveEnv }) {
  if (env.kind === "local") {
    return <span className="text-xs text-ink-muted">working tree</span>;
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
        <span className="font-mono text-sm text-ink">{env.release}</span>
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
          className="max-w-[22rem] truncate font-mono text-2xs text-ink-muted"
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
    <span className="text-xs text-ink-muted">
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
