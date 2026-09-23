// Copyright (c) 2025 Reliant Labs

/**
 * The env-monitoring panel's presentation layer.
 *
 * PURE PROPS, like TopologyView: it takes an outcome and renders it, owning no
 * data fetching. That is what lets the visual contract be tested by handing it a
 * report object instead of standing up a query client and a transport.
 *
 * The four non-report outcomes are delegated to the SHARED ForgeStates
 * components rather than re-implemented here. They are properties of the RPC
 * envelope, not of this screen — "no forge.yaml", "your forge is too old" and
 * "the cluster was unreachable" mean the same thing whichever forge command was
 * asked — and a second copy of them would be a second place for the unreachable
 * case to drift back into a red banner.
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
 * LAYOUT. Two standard surfaces — checks, then services — each a `Card` holding
 * a real `<table>` with a real `<thead>`. Every fact has its own column, so a
 * reader can scan one column down the page instead of re-parsing a differently
 * shaped flex row each time. The env picker is NOT here: the forge layout
 * renders shared EnvTabs above this panel, and the `env` prop is what this
 * component is told to report on.
 *
 * SURFACES. The page is `bg-background`; each Card is `bg-card` + `border-border`;
 * anything inset inside a card (the evidence block in CheckRow) is
 * `bg-background` + `border-border/60`. Cards are never nested, and `bg-muted`
 * is never used for structure — `--muted` inverts direction between light and
 * dark across the seven schemes in themes/professional-themes.css, so a
 * muted-backed panel recesses in one mode and lifts in the other.
 */

import { cn } from "@/lib/utils";
import { Card } from "@/components/ui";
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
import { CheckRow } from "./CheckRow";
import { DispositionLegend } from "./DispositionLegend";

export interface EnvStatusPanelProps {
  outcome: ForgeOutcome<ForgeEnvStatusReport> | undefined;
  isLoading: boolean;
  /** A transport/daemon failure — genuinely an error, unlike every outcome above. */
  error?: Error | null;
  /** Which environment was asked about, for copy when the report omits it. */
  env: string;
  projectName?: string;
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
  failing: "border-solid border-destructive/40 bg-destructive/10 text-destructive",
  degraded: "border-solid border-warning/40 bg-warning/10 text-warning",
  "all-good": "border-solid border-success/40 bg-success/10 text-success",
  incomplete:
    "border-dashed border-muted-foreground/60 bg-transparent text-muted-foreground ring-1 ring-inset ring-muted-foreground/30",
  "no-checks": "border-dashed border-border bg-transparent text-muted-foreground",
};

const VERDICT_LABELS: Record<EnvStatusVerdict, string> = {
  failing: "Failing",
  degraded: "Degraded",
  "all-good": "Healthy",
  incomplete: "Incomplete — not a clean bill of health",
  "no-checks": "Nothing measured",
};

/** Shared column-header treatment, so both tables on the screen read alike. */
const TH = "px-4 py-2 text-left text-xs font-medium text-muted-foreground";

export function EnvStatusPanel({
  outcome,
  isLoading,
  error,
  env,
  projectName,
}: EnvStatusPanelProps) {
  if (isLoading && !outcome) {
    return (
      <Card
        data-testid="forge-status-loading"
        variant="outlined"
        size="lg"
        hover={false}
        className="border border-dashed border-border text-center text-sm text-muted-foreground"
      >
        Running runtime checks for <span className="font-mono">{env}</span>…
      </Card>
    );
  }

  if (error && !outcome) {
    return (
      <Card
        data-testid="forge-status-error"
        variant="outlined"
        size="lg"
        hover={false}
        className="border border-destructive/40 bg-destructive/10 text-center text-sm text-destructive"
      >
        Could not reach your daemon to run forge&apos;s runtime checks: {error.message}
      </Card>
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

  return (
    <div className="space-y-4" data-testid="forge-env-status">
      <header className="space-y-3">
        <div className="flex flex-wrap items-baseline gap-x-3 gap-y-1">
          {/* Prose heading, so it is not monospaced. The env name beside it IS
              an identifier and keeps the mono face. */}
          <h1 className="text-base font-semibold text-foreground">Runtime checks</h1>
          <span className="font-mono text-sm text-muted-foreground">{reportEnv}</span>
          {report.head_commit_at && (
            <span className="text-xs text-muted-foreground">
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
        <p className="text-xs text-muted-foreground">
          A check that could not obtain its facts reports{" "}
          <span className="text-foreground">could not measure</span> — never a pass and never a
          skip. Three outcomes, not two.
        </p>
      </header>

      {checks.length === 0 ? (
        <Card
          data-testid="forge-status-no-checks"
          variant="outlined"
          size="lg"
          hover={false}
          className="border border-dashed border-border text-center text-sm text-muted-foreground"
        >
          Forge returned no runtime checks for <span className="font-mono">{reportEnv}</span>.
          Nothing here has been measured — this is not a statement that the environment is healthy.
        </Card>
      ) : (
        <div className="space-y-3">
          <DispositionLegend report={report} />

          <Card
            variant="default"
            size="sm"
            hover={false}
            // `elevation-1` IS the card surface (--surface-raised maps to
            // --card); a second bg-* class here would just race it.
            className="overflow-hidden border-border p-0"
          >
            <table className="w-full border-collapse text-left">
              <thead>
                <tr className="border-b border-border">
                  <th scope="col" className={TH}>
                    Result
                  </th>
                  <th scope="col" className={TH}>
                    Check
                  </th>
                  <th scope="col" className={TH}>
                    Detail
                  </th>
                  <th scope="col" className={cn(TH, "text-right")}>
                    Took
                  </th>
                </tr>
              </thead>
              {/* The testid stays on the row container, so the contract test's
                  "every check is rendered" count still reads check rows. */}
              <tbody data-testid="forge-status-checks">
                {checks.map((check) => (
                  <CheckRow key={check.name} check={check} />
                ))}
              </tbody>
            </table>
          </Card>
        </div>
      )}

      {report.services && report.services.length > 0 && (
        <ServiceSummary services={report.services} />
      )}
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
function ServiceSummary({
  services,
}: {
  services: NonNullable<ForgeEnvStatusReport["services"]>;
}) {
  return (
    <section className="space-y-2" data-testid="forge-status-services">
      <h2 className="text-sm font-medium text-foreground">Services forge resolved</h2>

      <Card
        variant="default"
        size="sm"
        hover={false}
        className="overflow-hidden border-border p-0"
      >
        <table className="w-full border-collapse text-left">
          <thead>
            <tr className="border-b border-border">
              <th scope="col" className={TH}>
                State
              </th>
              <th scope="col" className={TH}>
                Service
              </th>
              <th scope="col" className={TH}>
                Kind
              </th>
              <th scope="col" className={cn(TH, "text-right")}>
                Port
              </th>
              <th scope="col" className={cn(TH, "text-right")}>
                Notes
              </th>
            </tr>
          </thead>
          <tbody>
            {services.map((service, index) => {
              const name = service.name || `Service ${index + 1}`;
              const stale = (service.serving ?? []).some((proc) => proc.stale === true);
              const hasNote = stale || service.duplicate || service.attribution_undetermined;
              return (
                <tr
                  key={name}
                  data-testid={`forge-status-service-${name}`}
                  className="border-b border-border/60 last:border-b-0"
                >
                  <td className="whitespace-nowrap px-4 py-2 align-middle">
                    <span
                      // Listening is a point-in-time port probe, so it is a
                      // measured fact and gets a solid treatment either way.
                      className={cn(
                        "inline-flex items-center rounded-md border border-solid px-2 py-0.5 text-xs",
                        service.listening
                          ? "border-success/40 bg-success/15 text-success"
                          : "border-destructive/40 bg-destructive/15 text-destructive"
                      )}
                    >
                      {service.listening ? "Listening" : "Down"}
                    </span>
                  </td>

                  {/* A service name is an identifier, so it stays mono. */}
                  <td className="px-4 py-2 align-middle font-mono text-sm text-foreground">
                    {name}
                  </td>

                  <td className="whitespace-nowrap px-4 py-2 align-middle text-sm text-muted-foreground">
                    {service.kind || <span aria-hidden="true">—</span>}
                  </td>

                  <td className="whitespace-nowrap px-4 py-2 text-right align-middle font-mono text-sm tabular-nums text-muted-foreground">
                    {typeof service.port === "number" && service.port > 0 ? (
                      service.port
                    ) : (
                      <span aria-hidden="true">—</span>
                    )}
                  </td>

                  <td className="px-4 py-2 text-right align-middle">
                    {hasNote ? (
                      <div className="flex flex-wrap justify-end gap-1">
                        {stale && (
                          <span className="rounded-md border border-solid border-warning/40 bg-warning/15 px-2 py-0.5 text-xs text-warning">
                            Stale build
                          </span>
                        )}
                        {service.duplicate && (
                          <span className="rounded-md border border-solid border-warning/40 bg-warning/15 px-2 py-0.5 text-xs text-warning">
                            Duplicate process
                          </span>
                        )}
                        {service.attribution_undetermined && (
                          <span className="rounded-md border border-dashed border-muted-foreground/60 px-2 py-0.5 text-xs text-muted-foreground ring-1 ring-inset ring-muted-foreground/30">
                            Attribution undetermined
                          </span>
                        )}
                      </div>
                    ) : (
                      <span aria-hidden="true" className="text-xs text-muted-foreground">
                        —
                      </span>
                    )}
                  </td>
                </tr>
              );
            })}
          </tbody>
        </table>
      </Card>
    </section>
  );
}
