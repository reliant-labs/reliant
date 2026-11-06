// Copyright (c) 2025 Reliant Labs

/**
 * The data browser's states, each worded for what it means to the person
 * reading rather than for the Connect code behind it
 * (errors.ts dataBrowserErrorKind). Drawn in the observability states' frame
 * (StateFrame), so a refusal here looks like one anywhere in the console.
 */

import type { ReactNode } from "react";
import { AlertTriangle, Database, Lock, PlugZap, RefreshCw, SearchX, ShieldOff, Timer } from "lucide-react";

import Button from "@/components/forge-ui/button";
import { dataBrowserErrorKind, obsErrorMessage, type DataBrowserErrorKind } from "@/services/controlPlane/observability/errors";

import { StateFrame } from "../Observability/ObsStates";

const KIND_ICON: Record<DataBrowserErrorKind, ReactNode> = {
  "not-configured": <Database className="h-4 w-4" />,
  denied: <Lock className="h-4 w-4" />,
  confirmation: <Lock className="h-4 w-4" />,
  unreachable: <PlugZap className="h-4 w-4" />,
  "not-found": <SearchX className="h-4 w-4" />,
  busy: <Timer className="h-4 w-4" />,
  "too-slow": <Timer className="h-4 w-4" />,
  invalid: <AlertTriangle className="h-4 w-4" />,
  unavailable: <AlertTriangle className="h-4 w-4" />,
};

function errorCopy(kind: DataBrowserErrorKind, env: string, subject: string): { title: string; body: ReactNode } {
  switch (kind) {
    case "not-configured":
      return {
        title: "This control plane can't browse data yet",
        body: "It answers database queries, but not the data browser's reads. It will once it is upgraded; forge db query works meanwhile.",
      };
    case "denied":
      return {
        title: `You can't read ${env}'s data`,
        body: (
          <>
            Reading an environment&apos;s databases needs the <code className="font-mono text-ink">db:query</code> grant in this
            organization; an organization admin can give it to you. An access token is never admitted to an audited environment,
            even with the grant: sign in as yourself.
          </>
        ),
      };
    case "confirmation":
      return { title: `${env} is audited`, body: "Every read here needs a reason, and this one was sent without one." };
    case "unreachable":
      return {
        title: `Reliant can't reach ${subject}`,
        body: "The control plane has no route to this database: it is not hosted by Reliant, or it is an external database the control plane cannot dial.",
      };
    case "not-found":
      return {
        title: `${subject} isn't readable here`,
        body: `It does not exist in ${env}, or the read-only role cannot SELECT it. The browser shows exactly what that role reads, and nothing it doesn't.`,
      };
    case "busy":
      return {
        title: "Too many reads at once",
        body: "The control plane limits how many database reads one person runs at a time, and how many a minute. Wait a moment and try again.",
      };
    case "too-slow":
      return {
        title: "The database took too long",
        body: "The read ran past its time limit. A filter on an indexed column (a key, a foreign key) usually makes it fast.",
      };
    case "invalid":
      return { title: "The database refused this read", body: "Usually a filter value the column's type cannot take. Its words:" };
    case "unavailable":
      return { title: `Couldn't read ${subject}`, body: "The control plane or the database did not answer." };
  }
}

/** A failed read, in words. `subject` names what was being read: "the shop database", "public.chats". */
export function DataError({
  error,
  env,
  subject,
  onRetry,
  retrying,
  actions,
  testId = "data-error",
}: {
  error: unknown;
  env: string;
  subject: string;
  onRetry?: () => void;
  retrying?: boolean;
  actions?: ReactNode;
  testId?: string;
}) {
  const kind = dataBrowserErrorKind(error);
  const copy = errorCopy(kind, env, subject);
  const detail = kind === "invalid" || kind === "unavailable" || kind === "not-configured" ? obsErrorMessage(error) : "";
  const canRetry = onRetry && (kind === "busy" || kind === "too-slow" || kind === "unavailable");
  return (
    <div data-testid={testId} data-kind={kind}>
      <StateFrame
        testId={`${testId}-${kind}`}
        tone={kind === "busy" ? "neutral" : "error"}
        icon={KIND_ICON[kind]}
        title={copy.title}
        actions={
          canRetry || actions ? (
            <>
              {canRetry && (
                <Button variant="outline" size="sm" onClick={onRetry} loading={retrying} disabled={retrying} leftIcon={<RefreshCw className="h-3 w-3" />}>
                  Try again
                </Button>
              )}
              {actions}
            </>
          ) : undefined
        }
      >
        <p>{copy.body}</p>
        {detail && <p className="break-words font-mono text-2xs text-ink">{detail}</p>}
      </StateFrame>
    </div>
  );
}

/** A note that is not a failure: the frame, neutral. */
export function DataNote({ icon, title, children, actions, testId }: { icon?: ReactNode; title: string; children?: ReactNode; actions?: ReactNode; testId: string }) {
  return (
    <StateFrame testId={testId} icon={icon ?? <Database className="h-4 w-4" />} title={title} actions={actions}>
      {children}
    </StateFrame>
  );
}

/** The database's policy refuses every read: say whose decision that is and where it lives. */
export function DataDenied({ env, database, unknown }: { env: string; database: string; unknown: boolean }) {
  return (
    <StateFrame
      testId="data-policy-deny"
      icon={<ShieldOff className="h-4 w-4" />}
      title={unknown ? `${env} has no recorded data access policy` : `${env} doesn't allow reading its data`}
    >
      <p>
        {unknown ? (
          <>
            Reliant reads an environment&apos;s data access policy from its last recorded release, and {env} has none it
            understands, so every read of <span className="font-mono text-ink">{database}</span> is refused.
          </>
        ) : (
          <>
            Its data access policy is <span className="font-mono text-ink">deny</span>, so no one can read{" "}
            <span className="font-mono text-ink">{database}</span>&apos;s rows here, including organization admins.
          </>
        )}
      </p>
      <p>
        The policy is part of the environment&apos;s declaration (<code className="font-mono text-ink">forge.DataAccess</code>,{" "}
        <code className="font-mono text-ink">db_query</code>): change it there and deploy to open it as{" "}
        <span className="font-mono text-ink">audited</span> or <span className="font-mono text-ink">open</span>.
      </p>
    </StateFrame>
  );
}

/** Rows of placeholder bars: the grid, the rail or the row page while it loads. */
export function DataLoading({ rows = 8, testId = "data-loading", label = "Loading" }: { rows?: number; testId?: string; label?: string }) {
  return (
    <div data-testid={testId} aria-busy="true" aria-label={label} className="space-y-2 p-3">
      {Array.from({ length: rows }, (_, index) => (
        <div key={index} className="h-5 animate-pulse rounded bg-border/60 motion-reduce:animate-none" style={{ width: `${94 - ((index * 17) % 34)}%` }} />
      ))}
    </div>
  );
}
