// Copyright (c) 2025 Reliant Labs

/**
 * Settings → Integrations: every connection the caller has made to an
 * integration (GitHub, Slack, Gmail, Twilio, HTTP…), where they can be found
 * again after being made from a builder or an automation's setup.
 *
 * Grouped by integration. Each connection shows the account it acts as, its
 * status, whether it is that integration's default, when it was last used,
 * and which automations name it. Its actions are the ConnectionService's own
 * writes: Rename, Make default, Test, and Remove (which revokes at the
 * provider where it can) behind a confirmation that names the automations
 * that will lose it. Nothing here ever reads a secret.
 */

import { useMemo, useState } from "react";
import { toast } from "sonner";

import {
  connectionErrorMessage,
  useAllConnections,
  useDeleteConnection,
  useIntegrationNames,
  useRenameConnection,
  useSetDefaultConnection,
  useTestConnection,
  type Connection,
} from "@/hooks/connection-queries";
import { useTriggers } from "@/hooks/trigger-queries";
import type { Trigger } from "@/hooks/trigger-model";
import { formatAbsoluteTime, formatRelativeTime } from "@/lib/relativeTime";
import Button from "../forge-ui/button";
import Card from "../forge-ui/card";
import ConfirmationDialog from "../forge-ui/confirmation_dialog";
import { integrationDisplayName } from "../workflow/inputs/inputTypeNouns";
import { RowMenu } from "../workflows/RowMenu";
import { WorkflowBadge } from "../workflows/WorkflowSourceBadge";
import { SettingsPageHeader } from "./SettingsPageHeader";

/** The automations that name a connection, by connection id. */
export function automationsByConnection(triggers: readonly Trigger[]): Map<string, Trigger[]> {
  const out = new Map<string, Trigger[]>();
  for (const trigger of triggers) {
    if (!trigger.connectionId) continue;
    out.set(trigger.connectionId, [...(out.get(trigger.connectionId) ?? []), trigger]);
  }
  return out;
}

/** Connections grouped by integration, default first, then by name. */
export function groupConnections(connections: readonly Connection[]): Array<{ integrationId: string; connections: Connection[] }> {
  const groups = new Map<string, Connection[]>();
  for (const connection of connections) {
    groups.set(connection.integrationId, [...(groups.get(connection.integrationId) ?? []), connection]);
  }
  return [...groups.entries()]
    .sort(([a], [b]) => a.localeCompare(b))
    .map(([integrationId, list]) => ({
      integrationId,
      connections: list.sort((a, b) => Number(b.isDefault) - Number(a.isDefault) || a.name.localeCompare(b.name)),
    }));
}

/** "Used by Nightly triage and PR review" / "Used by 4 automations" / "". */
export function usedBySentence(automations: readonly Trigger[]): string {
  if (automations.length === 0) return "";
  if (automations.length <= 2) return `Used by ${automations.map((a) => a.name || "an unnamed automation").join(" and ")}`;
  return `Used by ${automations.length} automations`;
}

const STATUS: Record<Connection["status"], { label: string; variant: "neutral" | "info" | "warning" | "error" }> = {
  active: { label: "Connected", variant: "neutral" },
  needs_reauth: { label: "Needs sign-in again", variant: "warning" },
  revoked: { label: "Revoked", variant: "error" },
  unknown: { label: "Unknown", variant: "neutral" },
};

export function IntegrationsSettings() {
  const connections = useAllConnections();
  const names = useIntegrationNames();
  const triggers = useTriggers(undefined);
  const usedBy = useMemo(() => automationsByConnection(triggers.data ?? []), [triggers.data]);
  const groups = useMemo(() => groupConnections(connections.data ?? []), [connections.data]);
  const nameOf = (id: string) => names.get(id) || integrationDisplayName(id);

  return (
    <div className="space-y-6">
      <SettingsPageHeader
        title="Integrations"
        description="The accounts your automations and agents act through: GitHub, Slack, Gmail, Twilio and HTTP. You connect one when something first needs it; this is where to find it again, check it still works, or revoke it."
      />
      {connections.isLoading ? (
        <Card padding="md" aria-busy="true">
          <p className="text-sm text-muted-foreground">Loading connections…</p>
        </Card>
      ) : connections.isError ? (
        <Card padding="md" role="alert">
          <p className="text-sm font-medium text-foreground">Connections could not be loaded.</p>
          <p className="mt-1 text-sm text-muted-foreground">{connectionErrorMessage(connections.error)}</p>
          <Button className="mt-3" variant="outline" size="sm" onClick={() => void connections.refetch()}>
            Try again
          </Button>
        </Card>
      ) : groups.length === 0 ? (
        <Card padding="md">
          <p className="text-sm font-medium text-foreground">No connections yet.</p>
          <p className="mt-1 text-sm text-muted-foreground">
            You connect an integration when an automation or a workflow step first needs it, from its setup or the
            workflow builder. Each one you make shows up here.
          </p>
        </Card>
      ) : (
        groups.map((group) => (
          <section key={group.integrationId} aria-labelledby={`integration-${group.integrationId}`} className="space-y-2">
            <h2 id={`integration-${group.integrationId}`} className="text-sm font-semibold text-foreground">
              {nameOf(group.integrationId)}
            </h2>
            <Card padding="none">
              <ul className="divide-y divide-border/60" aria-label={`${nameOf(group.integrationId)} connections`}>
                {group.connections.map((connection) => (
                  <ConnectionRow
                    key={connection.id}
                    connection={connection}
                    integrationName={nameOf(group.integrationId)}
                    automations={usedBy.get(connection.id) ?? []}
                  />
                ))}
              </ul>
            </Card>
          </section>
        ))
      )}
    </div>
  );
}

function ConnectionRow({
  connection,
  integrationName,
  automations,
}: {
  connection: Connection;
  integrationName: string;
  automations: Trigger[];
}) {
  const [renaming, setRenaming] = useState(false);
  const [draft, setDraft] = useState(connection.name);
  const [removing, setRemoving] = useState(false);
  const rename = useRenameConnection();
  const makeDefault = useSetDefaultConnection();
  const test = useTestConnection();
  const remove = useDeleteConnection();
  const status = STATUS[connection.status];
  const label = connection.name || connection.accountLabel || integrationName;
  const used = usedBySentence(automations);

  const saveName = () => {
    const name = draft.trim();
    if (!name || name === connection.name) {
      setRenaming(false);
      return;
    }
    rename.mutate(
      { id: connection.id, name },
      {
        onSuccess: () => {
          setRenaming(false);
          toast.success(`Renamed to “${name}”`);
        },
        onError: (error) => toast.error("Could not rename the connection", { description: connectionErrorMessage(error) }),
      },
    );
  };

  const runTest = () =>
    test.mutate(
      { id: connection.id },
      {
        onSuccess: (result) => {
          if (result.ok) {
            toast.success(result.probed ? `${label} works${result.accountLabel ? `: signed in as ${result.accountLabel}` : ""}` : `${label} is saved; ${integrationName} has no way to check it`);
          } else {
            toast.error(`${label} did not work`, { description: result.errorClass ? `${integrationName} said: ${result.errorClass}` : "Sign in again, or remove it." });
          }
        },
        onError: (error) => toast.error("Could not test the connection", { description: connectionErrorMessage(error) }),
      },
    );

  const removeDescription = [
    automations.length > 0
      ? `${automations.length === 1 ? "1 automation names it" : `${automations.length} automations name it`} (${automations
          .slice(0, 3)
          .map((a) => a.name)
          .join(", ")}${automations.length > 3 ? ", …" : ""}) and will stop working until it gets another connection.`
      : "No automation names it.",
    connection.isDefault ? `Steps that use your default ${integrationName} connection will have none until you pick another.` : "",
    `Reliant revokes it at ${integrationName} where it can, and forgets the credential.`,
  ]
    .filter(Boolean)
    .join(" ");

  return (
    <li className="flex flex-wrap items-start gap-x-4 gap-y-2 px-4 py-3" data-testid={`connection-${connection.id}`}>
      <div className="min-w-0 flex-[1_1_16rem]">
        {renaming ? (
          <form
            className="flex items-center gap-2"
            onSubmit={(event) => {
              event.preventDefault();
              saveName();
            }}
          >
            <label htmlFor={`rename-${connection.id}`} className="sr-only">
              Name
            </label>
            <input
              id={`rename-${connection.id}`}
              autoFocus
              value={draft}
              onChange={(event) => setDraft(event.target.value)}
              onKeyDown={(event) => {
                if (event.key === "Escape") {
                  setDraft(connection.name);
                  setRenaming(false);
                }
              }}
              className="h-8 min-w-0 flex-1 rounded-md border border-border bg-background px-2 text-sm text-foreground focus:outline-none focus-visible:ring-2 focus-visible:ring-ring"
            />
            <Button type="submit" size="sm" disabled={rename.isPending}>
              Save
            </Button>
            <Button
              type="button"
              variant="ghost"
              size="sm"
              onClick={() => {
                setDraft(connection.name);
                setRenaming(false);
              }}
            >
              Cancel
            </Button>
          </form>
        ) : (
          <div className="flex min-w-0 flex-wrap items-center gap-2">
            <span className="truncate text-sm font-medium text-foreground">{label}</span>
            {connection.isDefault && <WorkflowBadge label="Default" variant="info" />}
            <WorkflowBadge label={status.label} variant={status.variant} />
          </div>
        )}
        <p className="mt-0.5 text-xs text-muted-foreground">
          {[
            connection.accountLabel && connection.accountLabel !== label ? `Acts as ${connection.accountLabel}` : "",
            connection.lastUsedAt ? `Last used ${formatRelativeTime(connection.lastUsedAt)}` : "Not used yet",
          ]
            .filter(Boolean)
            .join(" · ")}
          {connection.lastUsedAt && <span className="sr-only"> ({formatAbsoluteTime(connection.lastUsedAt)})</span>}
        </p>
        {connection.statusReason && connection.status !== "active" && (
          <p className="mt-0.5 text-xs text-warning-ink">{connection.statusReason}</p>
        )}
        <p className="mt-0.5 text-xs text-muted-foreground" title={automations.map((a) => a.name).join(", ") || undefined}>
          {used || "No automation names it"}.
          {connection.isDefault && ` Steps that name only ${integrationName} use it.`}
        </p>
      </div>
      {!renaming && (
        <div className="flex shrink-0 items-center gap-1.5">
          <Button variant="outline" size="sm" onClick={runTest} disabled={test.isPending} aria-label={`Test ${label}`}>
            {test.isPending ? "Testing…" : "Test"}
          </Button>
          <RowMenu
            label={`More actions for ${label}`}
            actions={[
              { label: "Rename", onSelect: () => setRenaming(true) },
              ...(connection.isDefault
                ? []
                : [
                    {
                      label: `Make default for ${integrationName}`,
                      onSelect: () =>
                        makeDefault.mutate(
                          { id: connection.id },
                          {
                            onSuccess: () => toast.success(`${label} is now your default ${integrationName} connection`),
                            onError: (error) => toast.error("Could not change the default", { description: connectionErrorMessage(error) }),
                          },
                        ),
                    },
                  ]),
              { label: "Remove…", destructive: true, onSelect: () => setRemoving(true) },
            ]}
          />
        </div>
      )}
      <ConfirmationDialog
        open={removing}
        title={`Remove ${label}?`}
        description={removeDescription}
        confirmLabel="Remove connection"
        loading={remove.isPending}
        onCancel={() => setRemoving(false)}
        onConfirm={() =>
          remove.mutate(
            { id: connection.id },
            {
              onSuccess: () => {
                setRemoving(false);
                toast.success(`Removed ${label}`);
              },
              onError: (error) => {
                setRemoving(false);
                toast.error("Could not remove the connection", { description: connectionErrorMessage(error) });
              },
            },
          )
        }
      />
    </li>
  );
}
