// Copyright (c) 2025 Reliant Labs

/**
 * "Connect GitHub…": make a connection to an integration without leaving the
 * builder. Offers the integration's methods most-preferred first:
 *
 *   - oauth2      sign in at the provider (lib/connection-oauth.ts). On the
 *                 web the page goes to the provider and comes back to the
 *                 builder with `?connection=<id>`; on the desktop consent
 *                 runs in the system browser while this dialog waits, then
 *                 hands back the connection.
 *   - api_key /   paste the credential (write-only: it is sealed server-side
 *     basic       and never comes back).
 *   - delegated   nothing to connect: the deployment provides it.
 *
 * An unavailable method is listed with its reason, never hidden, so an
 * operator can see what to configure.
 */

import { useEffect, useRef, useState, type FormEvent } from "react";
import { useQueryClient } from "@tanstack/react-query";
import { ExternalLink, KeyRound, Loader2 } from "lucide-react";

import { Modal } from "../../ui/Modal";
import { Button } from "../../ui/Button";
import { CardInset } from "../../forge-ui/card";
import { connectionErrorMessage, type AuthMethod } from "../../../api/connection-grpc";
import { invalidateConnectionQueries, useCatalogEntry, useCreateApiKeyConnection } from "../../../hooks/connection-queries";
import type { Connection } from "../../../api/connection-grpc";
import { ConnectionOAuthCancelled, connectWithOAuth, connectsThroughSystemBrowser } from "../../../lib/connection-oauth";
import { IntegrationIcon } from "../palette/IntegrationIcon";

export interface ConnectIntegrationTarget {
  /** Any catalog ref of the integration; its entry carries the connection methods. */
  ref: string;
  integrationId: string;
  displayName: string;
  icon?: string;
}

export interface ConnectIntegrationDialogProps {
  target: ConnectIntegrationTarget | null;
  onClose: () => void;
  onConnected?: (connection: Connection) => void;
  /** Where OAuth lands afterwards; defaults to the current page. */
  redirectAfter?: string;
}

const METHOD_LABEL: Record<AuthMethod["kind"], string> = {
  oauth2: "Sign in",
  api_key: "API key",
  basic: "Username and password",
  delegated: "Provided by Reliant",
  none: "No credential",
  unknown: "Other",
};

function currentPath(): string {
  if (typeof window === "undefined") return "/";
  return `${window.location.pathname}${window.location.search}`;
}

export function ConnectIntegrationDialog({ target, onClose, onConnected, redirectAfter }: ConnectIntegrationDialogProps) {
  if (!target) return null;
  return <ConnectBody target={target} onClose={onClose} onConnected={onConnected} redirectAfter={redirectAfter} />;
}

function ConnectBody({
  target,
  onClose,
  onConnected,
  redirectAfter,
}: Omit<ConnectIntegrationDialogProps, "target"> & { target: ConnectIntegrationTarget }) {
  const entryQuery = useCatalogEntry(target.ref);
  const methods = (entryQuery.data?.connection.methods ?? []).filter((m) => m.kind !== "none");
  const params = entryQuery.data?.connection.params ?? [];
  const [chosen, setChosen] = useState<AuthMethod["kind"] | null>(null);
  const active = methods.find((m) => m.kind === chosen) ?? methods.find((m) => m.available);
  const [name, setName] = useState("");
  const [fields, setFields] = useState<Record<string, string>>({});
  const [paramValues, setParamValues] = useState<Record<string, string>>({});
  const [error, setError] = useState<string | null>(null);
  const [redirecting, setRedirecting] = useState(false);
  const createKey = useCreateApiKeyConnection();
  const queryClient = useQueryClient();
  const inSystemBrowser = connectsThroughSystemBrowser();
  // A desktop flow waits on a loopback receiver; closing the dialog releases it.
  const pendingOAuth = useRef<AbortController | null>(null);
  useEffect(() => () => pendingOAuth.current?.abort(), []);

  const startOAuth = async () => {
    setError(null);
    setRedirecting(true);
    const controller = new AbortController();
    pendingOAuth.current = controller;
    try {
      const outcome = await connectWithOAuth(
        {
          integrationId: target.integrationId,
          name: name.trim() || undefined,
          redirectAfter: redirectAfter ?? currentPath(),
          params: paramValues,
        },
        controller.signal,
      );
      if (outcome.kind === "connected") {
        invalidateConnectionQueries(queryClient);
        onConnected?.(outcome.connection);
        onClose();
      }
    } catch (err) {
      if (err instanceof ConnectionOAuthCancelled) return;
      setRedirecting(false);
      setError(connectionErrorMessage(err));
    } finally {
      if (pendingOAuth.current === controller) pendingOAuth.current = null;
    }
  };

  const cancelOAuth = () => {
    pendingOAuth.current?.abort();
    pendingOAuth.current = null;
    setRedirecting(false);
  };

  const submitKey = async (event: FormEvent) => {
    event.preventDefault();
    if (!active) return;
    setError(null);
    try {
      const connection = await createKey.mutateAsync({
        integrationId: target.integrationId,
        name: name.trim() || target.displayName,
        kind: active.kind === "basic" ? "basic" : "api_key",
        fields,
        params: paramValues,
      });
      onConnected?.(connection);
      onClose();
    } catch (err) {
      setError(connectionErrorMessage(err));
    }
  };

  const fieldLabels = active ? Object.entries(active.fieldLabels) : [];
  const keyFields: Array<[string, string]> =
    fieldLabels.length > 0 ? fieldLabels : active?.kind === "basic" ? [["username", "Username"], ["password", "Password"]] : [["api_key", "API key"]];

  return (
    <Modal
      isOpen
      onClose={onClose}
      size="md"
      title={`Connect ${target.displayName}`}
      titlePrefix={<IntegrationIcon hint={target.icon ?? target.integrationId} />}
    >
      {entryQuery.isLoading ? (
        <div role="status" className="flex items-center gap-2 text-sm text-muted-foreground">
          <Loader2 className="h-4 w-4 animate-spin motion-reduce:animate-none" aria-hidden /> Loading connection options…
        </div>
      ) : entryQuery.isError ? (
        <div role="alert" className="space-y-3 text-sm">
          <p className="text-foreground">Couldn't load how to connect {target.displayName}.</p>
          <Button variant="outline" size="sm" onClick={() => void entryQuery.refetch()}>Retry</Button>
        </div>
      ) : methods.length === 0 ? (
        <p className="text-sm text-muted-foreground">{target.displayName} needs no connection.</p>
      ) : (
        <div className="space-y-5">
          {methods.length > 1 && (
            <div role="radiogroup" aria-label="How to connect" className="flex flex-wrap gap-2">
              {methods.map((method) => (
                <button
                  key={method.kind}
                  type="button"
                  role="radio"
                  aria-checked={active?.kind === method.kind}
                  disabled={!method.available}
                  onClick={() => setChosen(method.kind)}
                  className={
                    active?.kind === method.kind
                      ? "rounded-lg border border-primary bg-primary px-3 py-1.5 text-sm font-medium text-primary-foreground"
                      : "rounded-lg border border-border px-3 py-1.5 text-sm text-foreground hover:bg-muted disabled:cursor-not-allowed disabled:opacity-60"
                  }
                >
                  {METHOD_LABEL[method.kind]}
                </button>
              ))}
            </div>
          )}

          {methods.some((m) => !m.available) && (
            <CardInset>
              <ul className="space-y-1 text-xs text-muted-foreground">
                {methods
                  .filter((m) => !m.available)
                  .map((m) => (
                    <li key={m.kind}>
                      <span className="font-medium text-foreground">{METHOD_LABEL[m.kind]}</span> isn't set up on this deployment
                      {m.unavailableReason ? `: ${m.unavailableReason}` : "."}
                    </li>
                  ))}
              </ul>
            </CardInset>
          )}

          {!active ? (
            <p role="alert" className="text-sm text-foreground">
              No way to connect {target.displayName} is configured here yet.
            </p>
          ) : active.kind === "delegated" ? (
            <p className="text-sm text-foreground">
              Reliant connects {target.displayName} for you through your signed-in account. There is nothing to set up.
            </p>
          ) : (
            <form onSubmit={active.kind === "oauth2" ? (e) => { e.preventDefault(); void startOAuth(); } : submitKey} className="space-y-4">
              <ConnectField id="connect-name" label="Name" hint="So you can tell connections apart.">
                <input
                  id="connect-name"
                  value={name}
                  onChange={(e) => setName(e.target.value)}
                  placeholder={`My ${target.displayName}`}
                  className="cpv2-field-input w-full"
                />
              </ConnectField>

              {active.kind === "oauth2" &&
                params.map((param) => (
                  <ConnectField key={param.name} id={`connect-param-${param.name}`} label={param.displayName} hint={param.description}>
                    <input
                      id={`connect-param-${param.name}`}
                      value={paramValues[param.name] ?? param.defaultValue}
                      onChange={(e) => setParamValues((prev) => ({ ...prev, [param.name]: e.target.value }))}
                      required={param.required}
                      className="cpv2-field-input w-full"
                    />
                  </ConnectField>
                ))}

              {active.kind !== "oauth2" && (
                <>
                  {params.map((param) => (
                    <ConnectField key={param.name} id={`connect-param-${param.name}`} label={param.displayName} hint={param.description}>
                      <input
                        id={`connect-param-${param.name}`}
                        value={paramValues[param.name] ?? param.defaultValue}
                        onChange={(e) => setParamValues((prev) => ({ ...prev, [param.name]: e.target.value }))}
                        required={param.required}
                        className="cpv2-field-input w-full"
                      />
                    </ConnectField>
                  ))}
                  {keyFields.map(([key, label]) => (
                    <ConnectField key={key} id={`connect-field-${key}`} label={label}>
                      <input
                        id={`connect-field-${key}`}
                        type={key === "username" ? "text" : "password"}
                        autoComplete="off"
                        value={fields[key] ?? ""}
                        onChange={(e) => setFields((prev) => ({ ...prev, [key]: e.target.value }))}
                        required
                        className="cpv2-field-input w-full font-mono"
                      />
                    </ConnectField>
                  ))}
                  <p className="flex items-center gap-1.5 text-xs text-muted-foreground">
                    <KeyRound className="h-3.5 w-3.5" aria-hidden /> Stored encrypted. Reliant never shows it again.
                  </p>
                </>
              )}

              {active.kind === "oauth2" && redirecting && inSystemBrowser && (
                <p role="status" className="flex items-center gap-2 text-sm text-muted-foreground">
                  <Loader2 className="h-4 w-4 animate-spin motion-reduce:animate-none" aria-hidden />
                  Finish signing in to {target.displayName} in your browser. This will update when you're done.
                </p>
              )}

              {error && (
                <p role="alert" className="text-sm text-destructive-ink">
                  {error}
                </p>
              )}

              <div className="flex justify-end gap-2 border-t border-border pt-4">
                <Button
                  type="button"
                  variant="outline"
                  onClick={redirecting && inSystemBrowser ? cancelOAuth : onClose}
                >
                  Cancel
                </Button>
                {active.kind === "oauth2" ? (
                  <Button type="submit" variant="primary" disabled={redirecting}>
                    {redirecting ? (inSystemBrowser ? "Waiting for browser…" : "Opening…") : `Continue to ${target.displayName}`}
                    <ExternalLink className="ml-1.5 h-3.5 w-3.5" aria-hidden />
                  </Button>
                ) : (
                  <Button type="submit" variant="primary" disabled={createKey.isPending}>
                    {createKey.isPending ? "Saving…" : "Save connection"}
                  </Button>
                )}
              </div>
            </form>
          )}
        </div>
      )}
    </Modal>
  );
}

function ConnectField({ id, label, hint, children }: { id: string; label: string; hint?: string; children: React.ReactNode }) {
  return (
    <div className="space-y-1.5">
      <label htmlFor={id} className="block text-sm font-medium text-foreground">{label}</label>
      {children}
      {hint && <p className="text-xs text-muted-foreground">{hint}</p>}
    </div>
  );
}
