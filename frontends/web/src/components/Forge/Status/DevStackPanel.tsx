// Copyright (c) 2025 Reliant Labs

/**
 * An environment's DEV STACK — what `forge env up` runs on this machine, and
 * forge's runtime checks against it. Rendered on a LOCAL environment's page
 * only: the host services are processes on the reader's own laptop, and the
 * runtime checks probe the stack those processes form.
 *
 * WHY LOCAL ONLY. `forge env status <env>` returns the same `services` array
 * for every env — the host processes forge would launch — and rendering it
 * on a hosted or cluster env's page is how prod once appeared to have two
 * services while its cluster ran sixteen. A deployed environment's contents
 * are its Workloads section; this is the laptop.
 *
 * PURE PROPS: it takes an outcome and renders it, owning no data fetching, so
 * the visual contract is tested by handing it a report object.
 *
 * NO CHECK IS EVER HIDDEN. There is no filter control and no "only show
 * problems" affordance, deliberately. Forge puts the cluster-workload probe
 * inside the `app` signal precisely because a report that covers "app" while
 * ignoring every pod forge applied is the report that was all-green for the hour
 * daemon-gateway spent OOMKilled and the ten hours three more services
 * crashlooped. A tidier panel is that outage's UI.
 *
 * Read-only by construction: it never offers to re-run a check or restart a
 * service. It reports.
 *
 * SURFACES. Each table is a forge `Card` (`bg-surface` = `--card`, `border-border`)
 * on the page's `bg-surface-sunken`; anything inset inside a card (the evidence
 * block in CheckTable) is `bg-surface-sunken` + `border-border/60`. Cards are never
 * nested, and `bg-surface-sunken` is never used for structure — `--muted` inverts
 * direction between light and dark across the ten schemes in
 * themes/professional-themes.css.
 */

import { cn } from "@/lib/utils";
import AlertBanner from "@/components/forge-ui/alert_banner";
import Card from "@/components/forge-ui/card";
import type { ForgeOutcome } from "@/services/forge/topology";
import {
  checksOf,
  verdictOf,
  verdictSentence,
  type EnvStatusVerdict,
  type ForgeEnvStatusReport,
} from "@/services/forge/status";

import {
  ForgeMalformed,
  ForgeUnreachable,
  ForgeUnsupported,
  NotForgeProject,
} from "../ForgeStates";
import { CheckTable } from "./CheckTable";
import { DispositionLegend } from "./DispositionLegend";
import DataTable, { type DataTableColumn } from "@/components/forge-ui/data_table";

export interface DevStackPanelProps {
  outcome: ForgeOutcome<ForgeEnvStatusReport> | undefined;
  isLoading: boolean;
  /** A transport/daemon failure — genuinely an error, unlike every outcome above. */
  error?: Error | null;
  /** Which environment was asked about, for copy when the report omits it. */
  env: string;
  projectName?: string;
  /**
   * Lead with the host services. The Running tab passes it: on a local
   * environment's page "what is running" is the question, and the checks
   * that measure it come second.
   */
  servicesFirst?: boolean;
}

/**
 * VERDICT_STYLES paints the header summary.
 *
 * `incomplete` is the load-bearing entry. It is NOT the success treatment — a run
 * with holes has established nothing — and it is NOT the destructive one either,
 * because nothing was proven wrong. It gets the same dashed, ringed, unfilled
 * treatment an undetermined row gets, so the header and the rows agree at a
 * glance about what kind of answer this is.
 */
const VERDICT_STYLES: Record<EnvStatusVerdict, string> = {
  failing: "border-solid border-danger-border bg-danger-surface text-danger-ink",
  degraded: "border-solid border-warning-border bg-warning-surface text-warning-ink",
  "all-good": "border-solid border-success-border bg-success-surface text-success-ink",
  incomplete:
    "border-dashed border-ink-muted/60 bg-transparent text-ink-muted ring-1 ring-inset ring-ink-muted/30",
  "no-checks": "border-dashed border-border bg-transparent text-ink-muted",
};

const VERDICT_LABELS: Record<EnvStatusVerdict, string> = {
  failing: "Failing",
  degraded: "Degraded",
  "all-good": "Healthy",
  incomplete: "Incomplete — not a clean bill of health",
  "no-checks": "Nothing measured",
};

export function DevStackPanel({
  outcome,
  isLoading,
  error,
  env,
  projectName,
  servicesFirst = false,
}: DevStackPanelProps) {
  if (isLoading && !outcome) {
    return (
      <Card
        data-testid="forge-status-loading"
        padding="lg"
        className="border-dashed text-center text-sm text-ink-muted"
      >
        Running runtime checks for <span className="font-mono">{env}</span>…
      </Card>
    );
  }

  if (error && !outcome) {
    return (
      <AlertBanner
        data-testid="forge-status-error"
        variant="error"
        message={<>Could not reach your daemon to run forge&apos;s runtime checks: {error.message}</>}
      />
    );
  }

  if (!outcome) return null;

  // Each of these is a SUCCESSFUL RPC with a different meaning. See ForgeStates.
  if (outcome.kind === "not-forge-project") return <NotForgeProject projectName={projectName} />;
  if (outcome.kind === "unsupported") return <ForgeUnsupported meta={outcome.meta} />;
  if (outcome.kind === "unreachable") return <ForgeUnreachable meta={outcome.meta} />;
  if (outcome.kind === "malformed") return <ForgeMalformed meta={outcome.meta} />;

  const report = outcome.report;
  const checks = checksOf(report);
  const verdict = verdictOf(report);
  const reportEnv = report.env || env;

  const services =
    report.services && report.services.length > 0 ? <ServiceSummary services={report.services} /> : null;

  return (
    <div className="space-y-4" data-testid="forge-env-status">
      {servicesFirst && services}
      <header className="space-y-3">
        <div className="flex flex-wrap items-baseline gap-x-3 gap-y-1">
          {/* The env is the page's subject, named in its header — not repeated here. */}
          <h3 className="text-sm font-semibold text-ink">Runtime checks</h3>
          {report.head_commit_at && (
            <span className="text-xs text-ink-muted">
              measured against HEAD at {report.head_commit_at}
            </span>
          )}
        </div>

        <div
          data-testid="forge-status-verdict"
          data-verdict={verdict}
          className={cn("rounded-lg border px-3 py-2", VERDICT_STYLES[verdict])}
        >
          <p className="text-sm font-medium">{VERDICT_LABELS[verdict]}</p>
          <p className="mt-0.5 text-xs opacity-90">{verdictSentence(verdict, reportEnv)}</p>
        </div>

        {/* The sentence that keeps the panel honest, stated once at the top as
            well as per row. A check that could not look is not a check that
            passed, and this is the misreading the whole surface exists to stop. */}
        <p className="text-xs text-ink-muted">
          A check that could not obtain its facts reports{" "}
          <span className="text-ink">could not measure</span> — never a pass and never a
          skip. Three outcomes, not two.
        </p>
      </header>

      {checks.length === 0 ? (
        <Card
          data-testid="forge-status-no-checks"
          padding="lg"
          className="border-dashed text-center text-sm text-ink-muted"
        >
          Forge returned no runtime checks for <span className="font-mono">{reportEnv}</span>.
          Nothing here has been measured — this is not a statement that the environment is healthy.
        </Card>
      ) : (
        <div className="space-y-3">
          <DispositionLegend report={report} />

          <Card padding="none" className="overflow-hidden">
            {/* The testid stays on the checks' container, so the contract
                test's "every check is rendered" count still reads check rows. */}
            <div data-testid="forge-status-checks">
              <CheckTable checks={checks} />
            </div>
          </Card>
        </div>
      )}

      {!servicesFirst && services}
    </div>
  );
}

/**
 * The host-service rows forge resolved, alongside the checks.
 *
 * Two facts here are three-valued and are kept that way. `stale` says a process
 * is running code older than HEAD, which is not a failure but is the answer to
 * "why is my fix not live". `attribution_undetermined` is forge declining to say
 * WHICH command is duplicated because it could not read argv — the same
 * "undetermined is not a no" rule as the checks above, so it is surfaced rather
 * than silently rendering as "no duplicates".
 *
 * Those three flags share one right-hand "Notes" column rather than getting a
 * column each: they are sparse, so a column per flag would be almost entirely
 * empty cells. They keep their individual treatments — the two measured warnings
 * solid, `attribution undetermined` dashed and ringed — which is what carries
 * the distinction, not their position.
 */
type ServiceTableRow = { name: string; service: NonNullable<ForgeEnvStatusReport["services"]>[number] };

const NOTE = "rounded-md border border-solid border-warning-border bg-warning-surface px-2 py-0.5 text-xs text-warning-ink";

const SERVICE_COLUMNS: DataTableColumn<ServiceTableRow>[] = [
  {
    key: "state",
    header: "State",
    render: (_, { service }) => (
      <span
        // Listening is a point-in-time port probe, so it is a measured fact
        // and gets a solid treatment either way.
        className={cn(
          "inline-flex items-center rounded-md border border-solid px-2 py-0.5 text-xs",
          service.listening
            ? "border-success-border bg-success-surface text-success-ink"
            : "border-danger-border bg-danger-surface text-danger-ink"
        )}
      >
        {service.listening ? "Listening" : "Down"}
      </span>
    ),
  },
  // A service name is an identifier, so it stays mono.
  { key: "service", header: "Service", className: "font-mono", render: (_, { name }) => name },
  {
    key: "kind",
    header: "Kind",
    className: "text-ink-muted",
    render: (_, { service }) => service.kind || <span aria-hidden="true">—</span>,
  },
  {
    key: "port",
    header: "Port",
    numeric: true,
    className: "font-mono text-ink-muted",
    render: (_, { service }) =>
      typeof service.port === "number" && service.port > 0 ? service.port : <span aria-hidden="true">—</span>,
  },
  {
    key: "notes",
    header: "Notes",
    numeric: true,
    render: (_, { service }) => {
      const stale = (service.serving ?? []).some((proc) => proc.stale === true);
      if (!stale && !service.duplicate && !service.attribution_undetermined) {
        return (
          <span aria-hidden="true" className="text-xs text-ink-muted">
            —
          </span>
        );
      }
      return (
        <div className="flex flex-wrap justify-end gap-1">
          {stale && <span className={NOTE}>Stale build</span>}
          {service.duplicate && <span className={NOTE}>Duplicate process</span>}
          {service.attribution_undetermined && (
            <span className="rounded-md border border-dashed border-ink-muted/60 px-2 py-0.5 text-xs text-ink-muted ring-1 ring-inset ring-ink-muted/30">
              Attribution undetermined
            </span>
          )}
        </div>
      );
    },
  },
];

function ServiceSummary({
  services,
}: {
  services: NonNullable<ForgeEnvStatusReport["services"]>;
}) {
  return (
    <section className="space-y-2" data-testid="forge-status-services">
      <h3 className="text-sm font-semibold text-ink">Host services</h3>

      <Card padding="none" className="overflow-hidden">
        <DataTable<ServiceTableRow>
          flush
          stacked
          ariaLabel="Host services"
          columns={SERVICE_COLUMNS}
          data={services.map((service, index) => ({ name: service.name || `Service ${index + 1}`, service }))}
          getRowKey={(r) => r.name}
          getRowProps={({ name }) => ({ "data-testid": `forge-status-service-${name}` })}
        />
      </Card>
    </section>
  );
}
