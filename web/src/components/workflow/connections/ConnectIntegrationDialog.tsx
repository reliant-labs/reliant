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
 * An unavailable method is listed in plain words, never hidden, and when no
 * method is available the dialog says the integration can't be connected
 * here instead of offering a dead Connect. The precise reason (which env vars
 * are unset) is for whoever runs the deployment: it is in the server log, and
 * shown here only on a dev deployment.
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
import { getIsDev } from "../../../lib/constants";

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

/** What an end user is told about a method this deployment hasn't set up. */
export function unavailableMethodMessage(kind: AuthMethod["kind"], displayName: string): string {
  switch (kind) {
    case "oauth2":
      return `${displayName} sign-in isn't available on this deployment yet.`;
    case "api_key":
      return "Connecting with an API key isn't available on this deployment yet.";
    case "basic":
      return "Connecting with a username and password isn't available on this deployment yet.";
    case "delegated":
      return `Connecting ${displayName} through your Reliant account isn't available on this deployment yet.`;
    default:
      return "This way of connecting isn't available on this deployment yet.";
  }
}

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
  const unavailable = methods.filter((m) => !m.available);
  // Env-var names mean something to whoever runs this deployment, and nothing
  // to anyone else; a dev deployment is run by the person looking at it.
  const operatorReasons = getIsDev() ? unavailable.filter((m) => m.unavailableReason) : [];

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
      ) : !active ? (
        <div className="space-y-4">
          <div role="alert" className="space-y-1 text-sm">
            <p className="font-medium text-foreground">{target.displayName} can't be connected on this deployment yet.</p>
            <p className="text-muted-foreground">
              None of the ways to connect it are set up here. Whoever runs this Reliant deployment can turn one on; until then, steps that use{" "}
              {target.displayName} can be built but won't run.
            </p>
          </div>
          <OperatorReasons methods={operatorReasons} />
          <div className="flex justify-end border-t border-border pt-4">
            <Button type="button" variant="outline" onClick={onClose}>
              Close
            </Button>
          </div>
        </div>
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

          {unavailable.length > 0 && (
            <CardInset>
              <ul className="space-y-1 text-xs text-muted-foreground">
                {unavailable.map((m) => (
                  <li key={m.kind}>{unavailableMethodMessage(m.kind, target.displayName)}</li>
                ))}
              </ul>
              <OperatorReasons methods={operatorReasons} />
            </CardInset>
          )}

          {active.kind === "delegated" ? (
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

/** The operator's version (which settings are missing), on a dev deployment only. */
function OperatorReasons({ methods }: { methods: AuthMethod[] }) {
  if (methods.length === 0) return null;
  return (
    <details className="mt-2 text-xs text-muted-foreground">
      <summary className="cursor-pointer select-none">Setup details (dev deployment)</summary>
      <ul className="mt-1 space-y-1 font-mono">
        {methods.map((m) => (
          <li key={m.kind}>
            {METHOD_LABEL[m.kind]}: {m.unavailableReason}
          </li>
        ))}
      </ul>
    </details>
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
