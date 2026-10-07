// Copyright (c) 2025 Reliant Labs

/**
 * Which connection an action (or an activation) authenticates with:
 *
 *   - "Use my default" (the stored value is empty: the run owner's default
 *     connection for the integration, resolved at call time), or one of the
 *     caller's connections by id.
 *   - "Connect…" when there is none yet, or, when this deployment offers no
 *     way to connect at all, a plain "can't be connected here yet" instead of
 *     a button that leads nowhere.
 *   - A delegated integration (hosted GitHub) has no connection to pick:
 *     "Uses your connected GitHub account", with a link to Settings.
 *   - No connection required: nothing to render.
 *
 * The id travels in the node; the secret never leaves the server.
 */

import { AlertTriangle, Plus } from "lucide-react";
import { useId } from "react";

import { CardInset } from "../../forge-ui/card";
import type { CatalogEntry } from "../../../api/catalog-search-grpc";
import type { Connection } from "../../../api/connection-grpc";
import { methodsUnavailableHere } from "../../../lib/integrationAvailability";
import { useConnections } from "../../../hooks/connection-queries";
import { cn } from "../../../lib/utils";

export interface ConnectionPickerProps {
  entry: CatalogEntry;
  /** The connection id stored on the node; empty means "Use my default". */
  value: string;
  onChange: (connectionId: string) => void;
  onConnect: () => void;
  disabled?: boolean;
}

export type ConnectionPickerState = "none_required" | "delegated" | "unconnected" | "unavailable" | "connected";

/** What the picker should show for an entry and the caller's connections. */
export function connectionPickerState(entry: CatalogEntry, connections: readonly Connection[]): ConnectionPickerState {
  const methods = entry.connection.methods;
  const usable = connections.filter((c) => c.status !== "revoked");
  if (usable.length > 0) return "connected";
  // Delegated wins only when it is what serves this deployment.
  if (methods.some((m) => m.kind === "delegated" && m.available)) return "delegated";
  if (!entry.connection.required && methods.length === 0) return "none_required";
  if (!(entry.connection.required || entry.summary.connectionRequired)) return "none_required";
  // A Connect button that can only open "nothing is set up here" is a dead end.
  return methodsUnavailableHere(methods) ? "unavailable" : "unconnected";
}

function connectionLabel(connection: Connection): string {
  const who = connection.accountLabel ? ` · ${connection.accountLabel}` : "";
  return `${connection.name || connection.integrationId}${who}${connection.isDefault ? " (default)" : ""}`;
}

export function ConnectionPicker({ entry, value, onChange, onConnect, disabled = false }: ConnectionPickerProps) {
  const id = useId();
  const integration = entry.summary.integration;
  const query = useConnections(integration.id);
  const connections = query.data ?? [];
  const state = connectionPickerState(entry, connections);

  if (state === "none_required" && !value) return null;

  const selected = connections.find((c) => c.id === value);
  const stale = !!value && !query.isLoading && !selected;
  const needsReauth = selected?.status === "needs_reauth";

  return (
    <div className="space-y-1.5">
      <div className="cpv2-field-label">
        {/* Only the select is a control the label can name; the other states
            are text and a button that carries its own name. */}
        {state === "connected" && !query.isLoading && !query.isError ? (
          <label htmlFor={id}>Connection</label>
        ) : (
          <span>Connection</span>
        )}
      </div>

      {query.isLoading ? (
        <div className="h-8 animate-pulse rounded-md bg-muted motion-reduce:animate-none" role="status" aria-label="Loading connections" />
      ) : query.isError ? (
        <p role="alert" className="cpv2-field-hint !mt-0">
          Couldn't load your connections.{" "}
          <button type="button" onClick={() => void query.refetch()} className="font-medium text-primary hover:underline">Retry</button>
        </p>
      ) : state === "delegated" ? (
        <CardInset className="text-sm">
          <p className="text-foreground">Uses your connected {integration.displayName} account.</p>
          <a href="/settings/git-connections" className="text-xs font-medium text-primary hover:underline">
            Manage in Settings
          </a>
        </CardInset>
      ) : state === "unavailable" ? (
        <CardInset className="text-sm">
          <p className="text-foreground">{integration.displayName} can't be connected on this deployment yet.</p>
          <p className="cpv2-field-hint !mt-0">None of its sign-in methods are set up here, so this step won't run until one is.</p>
        </CardInset>
      ) : state === "unconnected" ? (
        <CardInset className="flex items-center justify-between gap-3">
          <span className="text-sm text-foreground">{integration.displayName} isn't connected yet.</span>
          <button
            type="button"
            onClick={onConnect}
            disabled={disabled}
            className="inline-flex flex-shrink-0 items-center gap-1 rounded-md bg-primary px-2.5 py-1 text-xs font-medium text-primary-foreground hover:bg-primary/90 disabled:opacity-50"
          >
            <Plus className="h-3.5 w-3.5" aria-hidden /> Connect {integration.displayName}
          </button>
        </CardInset>
      ) : (
        <div className="flex items-center gap-2">
          <select
            id={id}
            value={value}
            disabled={disabled}
            onChange={(event) => {
              if (event.target.value === "__connect__") {
                onConnect();
                return;
              }
              onChange(event.target.value);
            }}
            className={cn("cpv2-field-select flex-1", (stale || needsReauth) && "!border-warning")}
            aria-describedby={stale || needsReauth ? `${id}-warn` : undefined}
          >
            <option value="">Use my default</option>
            {connections.map((connection) => (
              <option key={connection.id} value={connection.id}>
                {connectionLabel(connection)}
                {connection.status === "needs_reauth" ? " — needs reconnecting" : ""}
              </option>
            ))}
            {stale && <option value={value}>Missing connection ({value.slice(0, 8)})</option>}
            <option value="__connect__">Connect another…</option>
          </select>
        </div>
      )}

      {(stale || needsReauth) && (
        <p id={`${id}-warn`} className="flex items-center gap-1.5 text-xs text-warning-ink">
          <AlertTriangle className="h-3.5 w-3.5 flex-shrink-0" aria-hidden />
          {stale
            ? "This connection no longer exists. Pick another, or use your default."
            : `This connection needs reconnecting before ${integration.displayName} accepts it.`}
        </p>
      )}
    </div>
  );
}
