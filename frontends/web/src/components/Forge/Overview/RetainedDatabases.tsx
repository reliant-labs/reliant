// Copyright (c) 2025 Reliant Labs

/**
 * KEPT DATABASES: the databases this app's environments left behind when they
 * were deleted with "Keep data" (DeleteEnvironment keep_data), with Purge
 * (control-plane docs/design/unified-shell-and-projects.md, Lifecycle, L1).
 *
 * They still exist and still bill as storage, and before this list nothing in
 * the UI said so. ListRetainedDatabases is org-wide; this card shows the rows
 * whose project is this app. It renders nothing when there are none, or when
 * the control plane cannot answer for a reason that is not a fault (no
 * access, an older control plane without the rpc).
 *
 * PURGE IS IRREVERSIBLE and needs the database's name typed. It needs an org
 * owner or admin (orgAuthority.ts): a known member sees no Purge button and a
 * line saying who can. No automatic purge is offered or implied here: a grace
 * window for kept databases is an open decision (D18).
 *
 * The control plane records the purge and its converger destroys the volumes;
 * the row stays listed until they are gone, so a requested purge shows
 * "Purge requested" in place of the button.
 *
 * KEEP DATA IS CONFIRMED HERE (keptDataChecks.ts). After a keep-data delete
 * this page re-reads the list until the environment's databases appear, and
 * warns when they do not within the window, or at once when the control
 * plane cannot list kept databases (Unimplemented), rather than let a
 * dropped keep_data pass as kept.
 */

import { useEffect, useState } from "react";
import { Code, ConnectError } from "@connectrpc/connect";
import { AlertTriangle, Database, Loader2 } from "lucide-react";

import Button from "@/components/forge-ui/button";
import Card, { CardHeader } from "@/components/forge-ui/card";
import { FormField } from "@/components/forge-ui/form";
import Input from "@/components/forge-ui/input";
import Label from "@/components/forge-ui/label";
import Modal from "@/components/forge-ui/modal";
import { usePurgeRetainedDatabase, useRetainedDatabases } from "@/hooks/forge-teardown-queries";
import { cloudErrorDetail, isUnimplemented, type RetainedDatabase } from "@/services/forge/cloudEnvs";

import { useObsReadOnly } from "../Observability/obsReadOnly";
import { PURGE_NEEDS_ADMIN, refusalText, useOrgAuthority } from "../orgAuthority";
import { CONFIRM_WINDOW_MS, endKeptDataCheck, useKeptDataChecks, type KeptDataCheck } from "./keptDataChecks";

/** How often the list is re-read while a keep-data delete is being confirmed. */
const CONFIRM_POLL_MS = 5_000;

/** Refusals that mean "nothing to show here", not a fault. */
const QUIET_CODES: ReadonlySet<Code> = new Set([Code.PermissionDenied, Code.Unimplemented, Code.Unauthenticated]);

function keptOn(iso: string | undefined): string | null {
  if (!iso) return null;
  return new Date(iso).toLocaleDateString(undefined, { year: "numeric", month: "short", day: "numeric" });
}

export function RetainedDatabases({ project }: { project: string }) {
  const allChecks = useKeptDataChecks((state) => state.checks);
  const checks = allChecks.filter((check) => check.project === "" || check.project === project);
  const retained = useRetainedDatabases({
    enabled: project !== "",
    refetchInterval: checks.length > 0 ? CONFIRM_POLL_MS : false,
  });
  const authority = useOrgAuthority();
  const lock = useObsReadOnly();
  const [purging, setPurging] = useState<RetainedDatabase | null>(null);
  const [requested, setRequested] = useState<ReadonlySet<string>>(new Set());

  // A check is done once its environment's databases are listed as kept.
  const listed = retained.data ?? [];
  const confirmed = checks.filter((check) => listed.some((row) => row.environmentId === check.environmentId));
  const confirmedKey = confirmed.map((check) => check.environmentId).join(",");
  useEffect(() => {
    for (const id of confirmedKey.split(",")) if (id) endKeptDataCheck(id);
  }, [confirmedKey]);
  // Read on every refetch, so a check crosses its window between answers.
  const now = Math.max(retained.dataUpdatedAt, retained.errorUpdatedAt, Date.now());
  const open = checks.filter((check) => !confirmed.includes(check));
  const checkNotices = open.length > 0 && (
    <div className="space-y-2" data-testid="kept-data-checks">
      {open.map((check) => (
        <KeptDataNotice
          key={check.environmentId}
          check={check}
          state={
            isUnimplemented(retained.error)
              ? "unverifiable"
              : now - check.since > CONFIRM_WINDOW_MS
                ? "missing"
                : "pending"
          }
        />
      ))}
    </div>
  );

  if (retained.error) {
    if (retained.error instanceof ConnectError && QUIET_CODES.has(retained.error.code)) return checkNotices || null;
    return (
      <>
        {checkNotices}
        <p role="status" className="text-sm text-ink-muted" data-testid="retained-databases-unavailable">
          Couldn&apos;t read the databases kept from deleted environments: {cloudErrorDetail(retained.error) || "try again later"}.
        </p>
      </>
    );
  }
  const rows = listed.filter((row) => row.project === project);
  if (rows.length === 0) return checkNotices || null;

  const canPurge = authority !== "member";
  return (
    <>
    {checkNotices}
    <Card padding="none" data-testid="retained-databases">
      <div className="px-4 pt-3">
        <CardHeader
          title="Kept databases"
          description="Kept when their environment was deleted with Keep data. They still exist and keep billing as storage until you purge them; Reliant can't show that amount here yet. A new deploy does not reattach them."
        />
      </div>
      <ul className="divide-y divide-border/60">
        {rows.map((row) => {
          const kept = keptOn(row.retainedAt);
          return (
            <li key={row.id} className="flex items-center gap-3 px-4 py-2.5" data-testid={`retained-database-${row.id}`}>
              <Database className="h-4 w-4 shrink-0 text-ink-muted" aria-hidden="true" />
              <div className="min-w-0">
                <p className="truncate font-mono text-sm font-medium text-ink">{row.databaseName}</p>
                <p className="text-xs text-ink-muted">
                  From <span className="font-mono">{row.environmentName || row.environmentId}</span>
                  {row.storageGib > 0 && <> · {row.storageGib} GiB</>}
                  {kept && <> · kept {kept}</>}
                </p>
              </div>
              <div className="ml-auto shrink-0">
                {requested.has(row.id) ? (
                  <span className="text-xs text-ink-muted" data-testid={`retained-database-purging-${row.id}`}>
                    Purge requested
                  </span>
                ) : canPurge ? (
                  <Button
                    variant="outline"
                    size="sm"
                    disabled={lock.readOnly}
                    title={lock.readOnly ? lock.reason : undefined}
                    onClick={() => setPurging(row)}
                    data-testid={`retained-database-purge-${row.id}`}
                  >
                    Purge…
                  </Button>
                ) : null}
              </div>
            </li>
          );
        })}
      </ul>
      {!canPurge && (
        <p className="border-t border-border/60 px-4 py-2.5 text-xs text-ink-muted" data-testid="retained-databases-admin-only">
          {PURGE_NEEDS_ADMIN}
        </p>
      )}
      <PurgeRetainedDatabaseDialog
        database={purging}
        onClose={() => setPurging(null)}
        onPurged={(id) => {
          setRequested((prev) => new Set(prev).add(id));
          setPurging(null);
        }}
      />
    </Card>
    </>
  );
}

/**
 * One keep-data delete not yet confirmed. `pending` while the teardown may
 * still be running; `missing` once the window has passed with nothing
 * listed; `unverifiable` when the control plane cannot list kept databases.
 */
function KeptDataNotice({ check, state }: { check: KeptDataCheck; state: "pending" | "missing" | "unverifiable" }) {
  const names = check.databases.length > 0 ? check.databases.join(", ") : "its databases";
  if (state === "pending") {
    return (
      <p role="status" className="flex items-center gap-2 text-sm text-ink-muted" data-testid={`kept-data-pending-${check.environmentId}`}>
        <Loader2 className="h-3.5 w-3.5 shrink-0 animate-spin" aria-hidden="true" />
        Confirming that {check.envName}&apos;s databases were kept. They are listed here once its teardown finishes.
      </p>
    );
  }
  return (
    <div
      role="alert"
      className="flex items-start gap-2 rounded-lg border border-border bg-surface px-4 py-3 text-sm text-ink"
      data-testid={`kept-data-warning-${check.environmentId}`}
    >
      <AlertTriangle className="mt-0.5 h-4 w-4 shrink-0 text-warning-ink" aria-hidden="true" />
      <div className="min-w-0 space-y-1">
        <p className="font-medium">
          {check.envName} was deleted with Keep data, but Reliant can&apos;t confirm {names} {check.databases.length === 1 ? "was" : "were"} kept.
        </p>
        <p className="text-ink-muted">
          {state === "unverifiable"
            ? "This control plane can't list kept databases, so whether the data was kept is unknown."
            : `No kept database from ${check.envName} is listed ${Math.round(CONFIRM_WINDOW_MS / 60_000)} minutes after the delete. If its teardown has finished, the data may have been deleted.`}{" "}
          Contact support before relying on that data.
        </p>
        <Button variant="outline" size="sm" onClick={() => endKeptDataCheck(check.environmentId)} data-testid={`kept-data-dismiss-${check.environmentId}`}>
          Dismiss
        </Button>
      </div>
    </div>
  );
}

export function PurgeRetainedDatabaseDialog({
  database,
  onClose,
  onPurged,
}: {
  database: RetainedDatabase | null;
  onClose: () => void;
  onPurged: (id: string) => void;
}) {
  const purge = usePurgeRetainedDatabase();
  const [typed, setTyped] = useState("");
  const name = database?.databaseName ?? "";
  const matches = name !== "" && typed === name;

  const close = () => {
    if (purge.isPending) return;
    setTyped("");
    purge.reset();
    onClose();
  };

  return (
    <Modal open={database !== null} onClose={close} size="sm" title={`Purge ${name}?`}>
      <div className="space-y-4 text-sm text-ink" data-testid="purge-retained-database-dialog">
        <p>
          This destroys <span className="font-mono">{name}</span>&apos;s database cluster and volumes, data included. It
          cannot be undone. It stays listed until its volumes are gone, then its storage stops billing.
        </p>
        <FormField className="forge-ui">
          <Label>Type {name} to confirm</Label>
          <Input
            value={typed}
            onChange={(event) => setTyped(event.target.value)}
            autoComplete="off"
            spellCheck={false}
            disabled={purge.isPending}
            data-testid="purge-retained-database-input"
          />
        </FormField>
        {purge.isError && (
          <p role="alert" className="text-xs text-danger-ink" data-testid="purge-retained-database-error">
            {refusalText(purge.error, PURGE_NEEDS_ADMIN)}
          </p>
        )}
        <div className="flex justify-end gap-2">
          <Button variant="outline" size="sm" onClick={close} disabled={purge.isPending}>
            Cancel
          </Button>
          <Button
            variant="danger"
            size="sm"
            loading={purge.isPending}
            disabled={!matches || purge.isPending}
            onClick={() => {
              if (!database) return;
              purge.mutate(
                { id: database.id },
                {
                  onSuccess: () => {
                    setTyped("");
                    purge.reset();
                    onPurged(database.id);
                  },
                }
              );
            }}
            data-testid="purge-retained-database-confirm"
          >
            Purge database
          </Button>
        </div>
      </div>
    </Modal>
  );
}
