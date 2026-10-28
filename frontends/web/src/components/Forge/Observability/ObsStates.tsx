// Copyright (c) 2025 Reliant Labs

/**
 * The states every observability view shares: loading, "not set up yet",
 * "nothing has arrived yet", "nothing in this window", and failures — each
 * worded for what it means to the reader rather than for the error code
 * behind it (errors.ts).
 *
 * "Not set up" and "nothing yet" are what a new organization meets first, so
 * they are written as an explanation, not an error: what the page will show,
 * then exactly how to get data onto it (GettingStarted).
 */

import type { ReactNode } from "react";
import { Activity, AlertTriangle, Boxes, Clock, Lock, PauseCircle, RefreshCw, Rocket, SearchX } from "lucide-react";

import Button from "@/components/forge-ui/button";
import { DOCS, GettingStarted } from "@/components/Forge/GettingStarted";
import type { BillingTab } from "@/routeSchemas";
import {
  obsErrorKind,
  obsErrorMessage,
  type ObsErrorKind,
} from "@/services/controlPlane/observability/errors";
import { useObsQuotaState } from "@/services/controlPlane/observability/ObsQuotaContext";
import { formatClock, quotaOf } from "@/services/controlPlane/observability/quota";
import type { Provenance } from "@/services/controlPlane/observability/types";
import { RANGE_LABELS, type RangePreset } from "@/services/controlPlane/observability/window";

import type { ObsView } from "./obsSearch";

function StateFrame({
  icon,
  title,
  children,
  actions,
  testId,
  tone = "neutral",
}: {
  icon: ReactNode;
  title: string;
  children?: ReactNode;
  actions?: ReactNode;
  testId: string;
  tone?: "neutral" | "error";
}) {
  return (
    <div
      data-testid={testId}
      role={tone === "error" ? "alert" : "status"}
      className="flex items-start gap-3 rounded-lg border border-dashed border-border bg-surface-sunken px-4 py-5"
    >
      <span
        className={tone === "error" ? "mt-0.5 shrink-0 text-danger-ink" : "mt-0.5 shrink-0 text-ink-muted"}
        aria-hidden="true"
      >
        {icon}
      </span>
      <div className="min-w-0 space-y-1.5">
        <p className="text-sm font-medium text-ink">{title}</p>
        {children && <div className="space-y-1 text-xs text-ink-muted">{children}</div>}
        {actions && <div className="flex flex-wrap gap-2 pt-1">{actions}</div>}
      </div>
    </div>
  );
}

// ── Getting started ──────────────────────────────────────────────────────────
//
// Three states look alike and mean different things, so each is drawn by its
// own component and none borrows another's words:
//
//   ObsNotSetUp     the organization has no telemetry store yet. Nothing can
//                   arrive until it does; the store is created with the first
//                   environment deployed through Reliant.
//   ObsNothingYet   the store exists, but nothing in scope has sent anything
//                   in the last week: the app is not deployed, or not a forge
//                   app, or its telemetry is switched off.
//   ObsNoData       nothing in this window or behind this filter. The reader
//                   widens or clears; nothing to set up. Also what a view says
//                   when the week could not be asked about (an older control
//                   plane), since claiming an app is empty needs a definite no.
//
// The first two lead with what the page will show (OBS_TOPICS), then the
// steps. The copy is one table, not a fork per view, so every view says the
// same true thing about how data arrives.

/** A page of the observability area, for the "what this page shows" line. */
export type ObsTopic = ObsView | "metrics" | "dashboards";

interface TopicCopy {
  /** What the page shows once data arrives. */
  about: string;
  /** How this page's data gets here, where that differs from "forge services send it". */
  source?: ReactNode;
  /** The docs section for this page, when it has its own. */
  docs?: { href: string; label: string };
}

const SIGNALS_ON = (signal: string) => (
  <code className="font-mono text-ink">{`signals = forge.ObservabilitySignals {${signal} = True}`}</code>
);

export const OBS_TOPICS: Record<ObsTopic, TopicCopy> = {
  graph: { about: "Service graph shows observed process dependencies, separate declared metadata, and explicit coverage gaps." },
  rpc: { about: "RPC explorer browses observed methods and verified declared schemas and capabilities. It never executes requests." },
  overview: {
    about: "This page leads with requests, error rate and latency for every service and frontend, each marked with your releases.",
    source: "Forge services report requests, errors and latency on their own; a forge frontend adds page loads and web vitals.",
  },
  alerts: {
    about: "Alerts shows what is firing, the rules each environment declares, silences, and where alerts are sent.",
    docs: { href: DOCS.alerts, label: "How alerts work" },
  },
  errors: {
    about: "Errors groups every error from your services and your users' browsers into issues, by stack and across releases, so you can resolve, ignore or reopen each one.",
    source: "A forge service's error logs and a forge frontend's uncaught errors become issues on their own, with no setup.",
    docs: { href: DOCS.errors, label: "How errors become issues" },
  },
  sessions: {
    about: "Sessions lists real users' browser sessions: the pages they saw, the requests that failed, the errors they hit, and a masked replay when one was recorded.",
    source: (
      <>
        A forge frontend sends sessions on its own. Replay is opt-in: build the frontend with{" "}
        <code className="font-mono text-ink">VITE_OTEL_REPLAY=true</code> (
        <code className="font-mono text-ink">NEXT_PUBLIC_OTEL_REPLAY=true</code> for Next.js) and set {SIGNALS_ON("replay")} on the environment.
        Every replay is masked; there is no unmasked mode.
      </>
    ),
    docs: { href: DOCS.sessionReplay, label: "How session replay works" },
  },
  logs: {
    about: "Logs searches your services' structured logs, each linked to the trace that wrote it.",
    source: "A forge service's JSON logs on stdout are collected with no setup; a forge frontend adds its browser console output.",
  },
  traces: {
    about: "Traces follows every request through your services, with each RPC, database query and call between components drawn as a waterfall.",
    source: "Forge services trace every RPC and query with no instrumentation; a forge frontend adds its fetch calls and page loads.",
  },
  profiles: {
    about: "Profiles shows where your Go services spend CPU, memory and goroutines, as flame graphs.",
    source: <>Continuous profiling is off on hosting until the environment turns it on with {SIGNALS_ON("profiles")}.</>,
  },
  metrics: {
    about: "Metrics charts RPC rate, errors and latency per method, database and Go runtime metrics, and any metric you declare in internal/metrics/metrics.go.",
    source: "Forge services export these with no setup.",
  },
  dashboards: {
    about: "Dashboards collects the dashboards forge generates from your services and frontends, the ones you commit, and the ones you build here.",
    docs: { href: DOCS.dashboards, label: "How dashboards work" },
  },
};

const ENV_PLACEHOLDER = "<env>";

export function ObsNotSetUp({ subject, topic, env }: { subject: string; topic?: ObsTopic; env?: string }) {
  return (
    <GettingStarted
      testId="obs-not-set-up"
      icon={<Activity className="h-4 w-4" />}
      title="Observability isn't set up for this organization yet"
      about={topic ? OBS_TOPICS[topic].about : "Observability shows the logs, traces, metrics and errors of the apps you deploy with forge."}
      steps={[
        {
          text: "Deploy an environment through Reliant. The store is created with your organization's first one.",
          command: `forge env deploy ${env ?? ENV_PLACEHOLDER}`,
        },
        { text: "Telemetry then arrives on its own: services and frontends built with forge send it without any setup." },
      ]}
      docs={{ href: DOCS.observability, label: "How observability works" }}
    >
      <p>
        Reliant keeps each organization&apos;s logs, traces, metrics and profiles in a store of its own. Yours
        hasn&apos;t been created yet, so there is nothing to show for <span className="font-mono">{subject}</span>.
      </p>
    </GettingStarted>
  );
}

/**
 * The store exists, and nothing in scope has sent anything in the last week
 * (useNothingArrived). Not an empty window: an empty app.
 */
export function ObsNothingYet({ subject, topic, env }: { subject: string; topic: ObsTopic; env?: string }) {
  const copy = OBS_TOPICS[topic];
  const target = env ?? ENV_PLACEHOLDER;
  return (
    <GettingStarted
      testId="obs-nothing-yet"
      icon={<Rocket className="h-4 w-4" />}
      title={`Nothing has arrived from ${subject} yet`}
      about={copy.about}
      steps={[
        {
          text: "Deploy a forge service or frontend. Its first data arrives once its pods are serving traffic.",
          command: `forge env deploy ${target}`,
        },
        {
          text: "Check what is arriving: it reports when the newest log and span landed, and whether the telemetry token reached the cluster.",
          command: `forge obs status ${target}`,
        },
      ]}
      docs={copy.docs ?? { href: DOCS.nothingShowsUp, label: "Nothing showing up?" }}
    >
      <p>Observability is set up, but nothing in {subject} has sent telemetry in the last 7 days.</p>
      {copy.source && <p>{copy.source}</p>}
    </GettingStarted>
  );
}

/** No environment recorded for the project: telemetry has nowhere to be filed yet. */
export function ObsNoEnvironments({ topic }: { topic: ObsTopic }) {
  return (
    <GettingStarted
      testId="obs-no-environments"
      icon={<Activity className="h-4 w-4" />}
      title="No environments to observe yet"
      about={OBS_TOPICS[topic].about}
      steps={[
        {
          text: "Deploy an environment through Reliant. Its telemetry starts arriving with its first release.",
          command: `forge env deploy ${ENV_PLACEHOLDER}`,
        },
        {
          text: "Working locally? forge env up keeps development telemetry on your machine and prints the HyperDX URL to read it.",
          command: "forge env up",
        },
      ]}
      docs={{ href: DOCS.observability, label: "How observability works" }}
    >
      <p>Telemetry is filed under the environments Reliant records, and this project has none yet.</p>
    </GettingStarted>
  );
}

/** Apps are selected but none of them has an environment recorded: there is nothing to read (and nothing is queried). */
export function ObsNoAppEnvironments({ apps }: { apps: readonly string[] }) {
  const names = apps.join(", ");
  return (
    <StateFrame testId="obs-no-app-environments" icon={<Boxes className="h-4 w-4" />} title={`No environments recorded for ${names}`}>
      <p>Telemetry is read per environment, and the selected {apps.length === 1 ? "app has" : "apps have"} none yet. Pick another app, or clear the app filter.</p>
    </StateFrame>
  );
}

export function ObsNoData({
  what,
  range,
  filtered = false,
  onWiden,
  onClearFilters,
  topic,
}: {
  /** "logs", "traces", "requests"… */
  what: string;
  range: RangePreset;
  /** A filter or query is narrowing the result — say so, and offer to clear it. */
  filtered?: boolean;
  onWiden?: () => void;
  onClearFilters?: () => void;
  /**
   * The page, when how its data arrives is not obvious (sessions, replay and
   * profiles are opt-in): the unfiltered state adds that line, since other
   * telemetry flowing says nothing about this page's.
   */
  topic?: ObsTopic;
}) {
  const window = RANGE_LABELS[range].toLowerCase();
  const source = topic ? OBS_TOPICS[topic].source : undefined;
  return (
    <StateFrame
      testId="obs-no-data"
      icon={filtered ? <SearchX className="h-4 w-4" /> : <Clock className="h-4 w-4" />}
      title={filtered ? `No ${what} match in the ${window}` : `No ${what} in the ${window}`}
      actions={
        <>
          {filtered && onClearFilters && (
            <Button variant="outline" size="sm" onClick={onClearFilters} data-testid="obs-clear-filters">
              Clear filters
            </Button>
          )}
          {onWiden && range !== "7d" && (
            <Button variant="ghost" size="sm" onClick={onWiden} data-testid="obs-widen">
              Look at the last 7 days
            </Button>
          )}
        </>
      }
    >
      {!filtered && (
        <p>
          Nothing was sent in that window. A service that is idle, or was deployed after it began, looks exactly like
          this.
        </p>
      )}
      {!filtered && source && <p data-testid="obs-no-data-source">{source}</p>}
    </StateFrame>
  );
}

/**
 * No customer metadata: the node agent reported nothing about this scope's
 * workloads. It is collected only where Reliant runs the workloads, and an
 * environment can opt out — both look like this, and the copy says so rather
 * than guessing which.
 */
export function ObsNoCustomerMetadata({
  range,
  filtered = false,
  onWiden,
  onClearFilters,
}: {
  range: RangePreset;
  filtered?: boolean;
  onWiden?: () => void;
  onClearFilters?: () => void;
}) {
  const window = RANGE_LABELS[range].toLowerCase();
  return (
    <StateFrame
      testId="obs-no-customer-metadata"
      icon={filtered ? <SearchX className="h-4 w-4" /> : <Boxes className="h-4 w-4" />}
      title={filtered ? `No workloads match in the ${window}` : `No customer metadata in the ${window}`}
      actions={
        <>
          {filtered && onClearFilters && (
            <Button variant="outline" size="sm" onClick={onClearFilters} data-testid="obs-clear-filters">
              Clear filters
            </Button>
          )}
          {onWiden && range !== "7d" && (
            <Button variant="ghost" size="sm" onClick={onWiden} data-testid="obs-widen">
              Look at the last 7 days
            </Button>
          )}
        </>
      }
    >
      <p>
        Customer metadata is what Reliant&apos;s node agent records about your own workloads: the images and versions
        running, restarts and why containers exited, CPU and memory against their requests and limits, and Kubernetes
        events such as crash loops, failed image pulls and evictions.
      </p>
      <p>
        It is collected for environments Reliant hosts, unless an environment has opted out, and counts toward your
        observability allowance. Workloads appear here a few minutes after they start.
      </p>
    </StateFrame>
  );
}

const ERROR_COPY: Record<Exclude<ObsErrorKind, "not-set-up">, { title: string; body: string }> = {
  denied: {
    title: "You can't read this environment's telemetry",
    body: "Your role in this organization doesn't include observability. An organization admin can grant it.",
  },
  invalid: {
    title: "That search couldn't be run",
    body: "The server couldn't parse the query. Check its syntax, or clear it.",
  },
  "over-quota": {
    title: "Will load when the hourly query allowance refills",
    body: "Nothing is wrong and nothing is lost: telemetry is still arriving.",
  },
  busy: {
    title: "That query was refused",
    body: "The server limits how many queries run at once and how much one may read. A shorter time range or a narrower filter usually fits.",
  },
  "too-broad": {
    title: "That query took too long",
    body: "It ran past the time limit. A shorter time range, or a narrower filter, usually fits.",
  },
  unavailable: {
    title: "Telemetry couldn't be loaded",
    body: "The observability service didn't answer. This is usually brief.",
  },
};

/**
 * The state for a failed query. `not-set-up` is handed to the area (it is true
 * of every view at once, so the area says it once, above them); every other
 * kind is drawn here.
 */
export function ObsError({
  error,
  subject,
  onRetry,
  retrying,
}: {
  error: unknown;
  subject: string;
  onRetry?: () => void;
  retrying?: boolean;
}) {
  const kind = obsErrorKind(error);
  if (kind === "not-set-up") return <ObsNotSetUp subject={subject} />;
  if (kind === "over-quota") return <ObsWillLoad error={error} />;
  const copy = ERROR_COPY[kind];
  const detail = obsErrorMessage(error);
  return (
    <StateFrame
      testId={`obs-error-${kind}`}
      tone="error"
      icon={kind === "denied" ? <Lock className="h-4 w-4" /> : <AlertTriangle className="h-4 w-4" />}
      title={copy.title}
      actions={
        onRetry && kind !== "denied" && kind !== "invalid" ? (
          <Button
            variant="outline"
            size="sm"
            onClick={onRetry}
            loading={retrying}
            disabled={retrying}
            leftIcon={<RefreshCw className="h-3 w-3" />}
            data-testid="obs-retry"
          >
            Try again
          </Button>
        ) : undefined
      }
    >
      <p>{copy.body}</p>
      {detail && <p className="break-words font-mono text-2xs">{detail}</p>}
    </StateFrame>
  );
}

/**
 * Marks Reliant's own operator telemetry (daemon and platform logs, which live
 * only in Reliant's org) so it is never mistaken for the app's.
 */
export function OperatorBadge({ provenance }: { provenance: Provenance }) {
  if (!provenance.operatorData && !provenance.source) return null;
  const label = provenance.source || "operator";
  return (
    <span
      className="shrink-0 rounded border border-border px-1 py-px font-sans text-2xs uppercase tracking-wide text-ink-muted"
      title="Reliant operator data: platform telemetry, not this app's"
      data-testid="obs-operator-badge"
    >
      {label}
    </span>
  );
}

/** Rows of placeholder bars, for a table or list that is loading. */
export function ObsLoadingRows({ rows = 6, testId = "obs-loading" }: { rows?: number; testId?: string }) {
  return (
    <div data-testid={testId} aria-busy="true" aria-label="Loading" className="space-y-2 p-4">
      {Array.from({ length: rows }, (_, index) => (
        <div
          key={index}
          className="h-5 animate-pulse rounded bg-border/60 motion-reduce:animate-none"
          style={{ width: `${92 - ((index * 13) % 30)}%` }}
        />
      ))}
    </div>
  );
}

// ── The query allowance ──────────────────────────────────────────────────────
//
// When the org's hourly query allowance is spent, the console says so ONCE (the
// banner), keeps every panel's last result with a badge, and shows a neutral
// "will load" where there is no result. None of these is the red error: nothing
// is broken, ingest continues and the allowance refills on its own.

/** A view with no result yet, while the allowance is spent: neutral, and says when it will load. */
export function ObsWillLoad({ error }: { error: unknown }) {
  const resetsAtMs = quotaOf(error)?.resetsAtMs;
  const when = resetsAtMs ? `Will load at ${formatClock(resetsAtMs)} when the hourly query allowance refills` : ERROR_COPY["over-quota"].title;
  return (
    <StateFrame testId="obs-error-over-quota" icon={<PauseCircle className="h-4 w-4" />} title={when}>
      <p>{ERROR_COPY["over-quota"].body}</p>
    </StateFrame>
  );
}

/**
 * Marks a result that is the LAST GOOD one: shown while the allowance is spent,
 * and gone when it refills. `updatedAt` is the query's dataUpdatedAt (0 when
 * the result is a previous one kept across a changed filter).
 */
export function ObsAsOf({ updatedAt, className }: { updatedAt?: number; className?: string }) {
  const state = useObsQuotaState();
  if (state.kind !== "exhausted") return null;
  return (
    <span
      role="status"
      data-testid="obs-as-of"
      title="The hourly query allowance is used up, so this is the last result that loaded. It updates by itself once the allowance refills."
      className={`inline-flex items-center gap-1 rounded border border-border px-1.5 py-px text-2xs text-ink-muted ${className ?? ""}`}
    >
      <PauseCircle className="h-3 w-3" aria-hidden="true" />
      Paused{updatedAt ? ` · as of ${formatClock(updatedAt)}` : ""} · query budget resets at {formatClock(state.resetsAtMs)}
    </span>
  );
}

const BILLING_USAGE_TAB = "usage" satisfies BillingTab;
const BILLING_USAGE_HREF = `/settings/billing?${new URLSearchParams({ tab: BILLING_USAGE_TAB })}#billing-observability-usage`;

/**
 * The one banner per page, said once instead of once per panel. Renders nothing
 * while reads run normally. `onOpenUsage` / `usageHref` link to the usage page.
 */
export function ObsQuotaBanner({ usageHref = BILLING_USAGE_HREF, onOpenUsage }: { usageHref?: string; onOpenUsage?: () => void }) {
  const state = useObsQuotaState();
  if (state.kind === "ok") return null;
  const resets = formatClock(state.resetsAtMs);
  const exhausted = state.kind === "exhausted";
  const own = state.lane === "interactive";
  const usage =
    usageHref || onOpenUsage ? (
      usageHref ? (
        <a href={usageHref} className="underline underline-offset-2" data-testid="obs-quota-usage">
          See usage
        </a>
      ) : (
        <button type="button" onClick={onOpenUsage} className="underline underline-offset-2" data-testid="obs-quota-usage">
          See usage
        </button>
      )
    ) : null;
  return (
    <div
      role="status"
      data-testid="obs-quota-banner"
      data-state={state.kind}
      className="flex items-start gap-3 rounded-lg border border-border bg-surface-sunken px-4 py-3 text-sm text-ink"
    >
      <PauseCircle className="mt-0.5 h-4 w-4 shrink-0 text-ink-muted" aria-hidden="true" />
      <div className="min-w-0 space-y-1">
        <p className="font-medium">
          {exhausted ? `Live updates are paused until ${resets}` : "Refresh slowed to protect your query allowance"}
        </p>
        <p className="text-xs text-ink-muted">
          {exhausted
            ? `${own ? "Your organization used this hour's query allowance for the console." : "Your organization used this hour's query allowance."} What is on screen is the last result that loaded; it updates by itself at ${resets}. `
            : `Searches, and opening a trace or an issue, still work. Normal refresh returns at ${resets}. `}
          Telemetry keeps arriving and nothing is lost.
          {own ? " Alerts keep running on their own allowance." : ""} {usage}
        </p>
      </div>
    </div>
  );
}
