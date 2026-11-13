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
 *
 * It is also where to make one: "Connect" lists the integrations this
 * deployment offers and opens the same dialog the workflow builder and an
 * automation's Enable step use (sign in at the provider, or paste a key), so a
 * workflow that only runs on a schedule can still get the connection its steps
 * need.
 *
 * Below the caller's own: the active organization's connections (its Slack
 * bot, its API key), which any live member may use in that org's runs
 * (org-sharing.md §7). Members see them read-only; the org's admins and
 * owners also add, test, rename, make default and remove them, each acted on
 * as the org's (owner_org_id). While the deployment has not enabled org
 * connections (orgs.connections_enabled) adding one is refused with
 * ORG_CONNECTIONS_DISABLED: the section then says so and stops offering it,
 * and the org's existing connections stay listed.
 */

import { useMemo, useState } from "react";
import { Plus } from "lucide-react";
import { toast } from "sonner";

import {
  connectionErrorMessage,
  useAllConnections,
  useDeleteConnection,
  useIntegrationNames,
  useIntegrations,
  useRenameConnection,
  useSetDefaultConnection,
  useTestConnection,
  splitByOwner,
  type Connection,
  type Integration,
} from "@/hooks/connection-queries";
import { useActiveOrg } from "@/hooks/useOrgs";
import { canAdministerMembers } from "@/services/controlPlane/members";
import type { MyOrg } from "@/services/controlPlane/orgs";
import { useTriggers } from "@/hooks/trigger-queries";
import type { Trigger } from "@/hooks/trigger-model";
import { methodsUnavailableHere } from "@/lib/integrationAvailability";
import { formatAbsoluteTime, formatRelativeTime } from "@/lib/relativeTime";
import Button from "../forge-ui/button";
import Card from "../forge-ui/card";
import ConfirmationDialog from "../forge-ui/confirmation_dialog";
import Modal from "../forge-ui/modal";
import { IntegrationLogoTile } from "../icons/IntegrationLogo";
import { ConnectIntegrationDialog, type ConnectIntegrationTarget } from "../workflow/connections/ConnectIntegrationDialog";
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

/** Where OAuth returns to, and where a run's "no <integration> connection" error points. */
export const INTEGRATIONS_SETTINGS_PATH = "/settings/integrations";

/** The integrations someone can connect here: they take a credential of some kind. */
export function connectableIntegrations(integrations: readonly Integration[]): Integration[] {
  return integrations
    .filter((integration) => integration.methods.some((method) => method.kind !== "none"))
    .sort((a, b) => a.displayName.localeCompare(b.displayName));
}

function connectTargetOf(integration: Integration): ConnectIntegrationTarget {
  return {
    integrationId: integration.id,
    displayName: integration.displayName,
    icon: integration.id,
    connection: { methods: integration.methods, params: integration.params },
  };
}

const STATUS: Record<Connection["status"], { label: string; variant: "neutral" | "info" | "warning" | "error" }> = {
  active: { label: "Connected", variant: "neutral" },
  needs_reauth: { label: "Needs sign-in again", variant: "warning" },
  revoked: { label: "Revoked", variant: "error" },
  unknown: { label: "Unknown", variant: "neutral" },
};

export function IntegrationsSettings() {
  const connections = useAllConnections();
  const integrations = useIntegrations();
  const connectable = useMemo(() => connectableIntegrations(integrations.data ?? []), [integrations.data]);
  const names = useIntegrationNames();
  const [picking, setPicking] = useState(false);
  const [target, setTarget] = useState<ConnectIntegrationTarget | null>(null);
  const connect = (integration: Integration) => {
    setPicking(false);
    setTarget(connectTargetOf(integration));
  };
  const triggers = useTriggers(undefined);
  const usedBy = useMemo(() => automationsByConnection(triggers.data ?? []), [triggers.data]);
  // The list may also carry the active org's connections (the web names the
  // active org on every request): those are the org section's, below.
  const { active: org } = useActiveOrg();
  const split = useMemo(() => splitByOwner(connections.data ?? [], org?.id), [connections.data, org?.id]);
  const groups = useMemo(() => groupConnections(split.mine), [split.mine]);
  const nameOf = (id: string) => names.get(id) || integrationDisplayName(id);
  const integrationOf = (id: string) => connectable.find((integration) => integration.id === id);

  return (
    <div className="space-y-6">
      <SettingsPageHeader
        title="Integrations"
        description="The accounts your automations and agents act through: GitHub, Slack, Gmail, Twilio and HTTP. Connect one here, or when a workflow step first needs it; this is also where to check it still works, or revoke it."
        actions={
          <Button variant="primary" size="lg" leftIcon={<Plus />} onClick={() => setPicking(true)} disabled={connectable.length === 0}>
            Connect
          </Button>
        }
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
            Connect an integration so the workflows and automations that use it can run, including ones that start on a
            schedule. Each one you make shows up here.
          </p>
          {connectable.length > 0 && (
            <div className="mt-3 flex flex-wrap gap-2">
              {connectable.map((integration) => (
                <Button key={integration.id} variant="outline" size="sm" onClick={() => connect(integration)}>
                  Connect {integration.displayName}
                </Button>
              ))}
            </div>
          )}
        </Card>
      ) : (
        groups.map((group) => (
          <section key={group.integrationId} aria-labelledby={`integration-${group.integrationId}`} className="space-y-2">
            <div className="flex items-center justify-between gap-2">
              <h2 id={`integration-${group.integrationId}`} className="text-sm font-semibold text-foreground">
                {nameOf(group.integrationId)}
              </h2>
              {integrationOf(group.integrationId) && (
                <Button
                  variant="ghost"
                  size="xs"
                  aria-label={`Add another ${nameOf(group.integrationId)} connection`}
                  onClick={() => connect(integrationOf(group.integrationId)!)}
                >
                  Add another
                </Button>
              )}
            </div>
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
      {org && !connections.isLoading && !connections.isError && (
        <OrgConnectionsSection
          org={org}
          connections={split.org}
          connectable={connectable}
          usedBy={usedBy}
          nameOf={nameOf}
        />
      )}
      <ConnectPicker open={picking} integrations={connectable} onClose={() => setPicking(false)} onPick={connect} />
      <ConnectIntegrationDialog target={target} onClose={() => setTarget(null)} redirectAfter={INTEGRATIONS_SETTINGS_PATH} />
    </div>
  );
}

/** The active org's connections: read-only for members, managed by its admins. */
export function OrgConnectionsSection({
  org,
  connections,
  connectable,
  usedBy,
  nameOf,
}: {
  org: MyOrg;
  connections: Connection[];
  connectable: Integration[];
  usedBy: Map<string, Trigger[]>;
  nameOf: (integrationId: string) => string;
}) {
  const isAdmin = canAdministerMembers(org.role);
  const [picking, setPicking] = useState(false);
  const [target, setTarget] = useState<ConnectIntegrationTarget | null>(null);
  // Learned from the server's refusal: the deployment has not turned org
  // connections on yet. There is no read for it, and guessing would be wrong
  // the other way.
  const [createDisabled, setCreateDisabled] = useState(false);
  const groups = useMemo(() => groupConnections(connections), [connections]);
  const connect = (integration: Integration) => {
    setPicking(false);
    setTarget(connectTargetOf(integration));
  };

  return (
    <section aria-labelledby="org-connections-heading" className="space-y-3" data-testid="org-connections">
      <div className="flex flex-wrap items-start justify-between gap-2">
        <div className="min-w-0">
          <h2 id="org-connections-heading" className="text-sm font-semibold text-foreground">
            {org.name}&apos;s connections
          </h2>
          <p className="mt-0.5 text-xs text-muted-foreground">
            Accounts that belong to {org.name}, not to anyone in it. Any member may use one in {org.name}&apos;s automations, by
            choosing it; {isAdmin ? "as an admin you add and manage them." : "only its admins and owners add and manage them."}
          </p>
        </div>
        {isAdmin && (
          <Button
            variant="outline"
            size="sm"
            leftIcon={<Plus />}
            onClick={() => setPicking(true)}
            disabled={connectable.length === 0 || createDisabled}
            data-testid="org-connections-add"
          >
            Add for {org.name}
          </Button>
        )}
      </div>
      {isAdmin && createDisabled && (
        <Card padding="md" role="status" data-testid="org-connections-disabled">
          <p className="text-sm text-muted-foreground">
            Adding connections for an organization isn&apos;t enabled on this deployment yet. {org.name}&apos;s existing
            connections still work and are listed here.
          </p>
        </Card>
      )}
      {groups.length === 0 ? (
        <Card padding="md" data-testid="org-connections-empty">
          <p className="text-sm text-muted-foreground">
            {org.name} has no connections yet.{isAdmin ? "" : " An admin or owner of it can add one."}
          </p>
        </Card>
      ) : (
        groups.map((group) => (
          <Card key={group.integrationId} padding="none">
            <ul className="divide-y divide-border/60" aria-label={`${org.name}'s ${nameOf(group.integrationId)} connections`}>
              {group.connections.map((connection) => (
                <ConnectionRow
                  key={connection.id}
                  connection={connection}
                  integrationName={nameOf(group.integrationId)}
                  automations={usedBy.get(connection.id) ?? []}
                  orgName={org.name}
                  canManage={isAdmin}
                />
              ))}
            </ul>
          </Card>
        ))
      )}
      {isAdmin && (
        <>
          <ConnectPicker open={picking} integrations={connectable} onClose={() => setPicking(false)} onPick={connect} title={`Connect an integration for ${org.name}`} />
          <ConnectIntegrationDialog
            target={target}
            onClose={() => setTarget(null)}
            redirectAfter={INTEGRATIONS_SETTINGS_PATH}
            owner={{ orgId: org.id, orgName: org.name }}
            onOrgConnectionsDisabled={() => setCreateDisabled(true)}
          />
        </>
      )}
    </section>
  );
}

export function ConnectPicker({
  open,
  integrations,
  onClose,
  onPick,
  title = "Connect an integration",
}: {
  open: boolean;
  integrations: Integration[];
  onClose: () => void;
  onPick: (integration: Integration) => void;
  title?: string;
}) {
  return (
    <Modal open={open} onClose={onClose} size="md" title={title}>
      <ul className="space-y-2" aria-label="Integrations">
        {integrations.map((integration) => {
          const unavailable = methodsUnavailableHere(integration.methods);
          return (
            <li key={integration.id}>
              <button
                type="button"
                disabled={unavailable}
                onClick={() => onPick(integration)}
                className="flex w-full items-center gap-3 rounded-lg border border-border px-3 py-2 text-left text-sm text-foreground hover:bg-muted focus:outline-none focus-visible:ring-2 focus-visible:ring-ring disabled:cursor-not-allowed disabled:opacity-60 disabled:hover:bg-transparent"
              >
                <IntegrationLogoTile icon={integration.id} size="sm" />
                <span className="font-medium">{integration.displayName}</span>
                {unavailable && <span className="ml-auto text-xs text-muted-foreground">Not available on this deployment</span>}
              </button>
            </li>
          );
        })}
      </ul>
    </Modal>
  );
}

export function ConnectionRow({
  connection,
  integrationName,
  automations,
  orgName,
  canManage = true,
}: {
  connection: Connection;
  integrationName: string;
  automations: Trigger[];
  /** An org connection's org, for its copy; unset for a personal one. */
  orgName?: string;
  /** Whether the caller may change it: always for their own; an org's admins for an org's. */
  canManage?: boolean;
}) {
  // An org's connection is acted on as that org's, explicitly (owner_org_id).
  const ownerOrgId = connection.ownerKind === "org" ? connection.orgId : undefined;
  const defaultOf = orgName ? `${orgName}'s default` : "your default";
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
      { id: connection.id, name, ownerOrgId },
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
      { id: connection.id, ownerOrgId },
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
    connection.isDefault ? `Steps that use ${defaultOf} ${integrationName} connection will have none until you pick another.` : "",
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
      {!renaming && canManage && (
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
                          { id: connection.id, ownerOrgId },
                          {
                            onSuccess: () => toast.success(`${label} is now ${defaultOf} ${integrationName} connection`),
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
            { id: connection.id, ownerOrgId },
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
