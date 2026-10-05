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
 * SURFACES. Each table is a `Card` (`bg-card` + `border-border`) on the page's
 * `bg-background`; anything inset inside a card (the evidence block in
 * CheckRow) is `bg-background` + `border-border/60`. Cards are never nested,
 * and `bg-muted` is never used for structure — `--muted` inverts direction
 * between light and dark across the seven schemes in
 * themes/professional-themes.css.
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
  failing: "border-solid border-destructive/40 bg-destructive/10 text-destructive-ink",
  degraded: "border-solid border-warning/40 bg-warning/10 text-warning-ink",
  "all-good": "border-solid border-success/40 bg-success/10 text-success-ink",
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
        className="border border-destructive/40 bg-destructive/10 text-center text-sm text-destructive-ink"
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

  const services =
    report.services && report.services.length > 0 ? <ServiceSummary services={report.services} /> : null;

  return (
    <div className="space-y-4" data-testid="forge-env-status">
      {servicesFirst && services}
      <header className="space-y-3">
        <div className="flex flex-wrap items-baseline gap-x-3 gap-y-1">
          {/* The env is the page's subject, named in its header — not repeated here. */}
          <h3 className="text-sm font-semibold text-foreground">Runtime checks</h3>
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
function ServiceSummary({
  services,
}: {
  services: NonNullable<ForgeEnvStatusReport["services"]>;
}) {
  return (
    <section className="space-y-2" data-testid="forge-status-services">
      <h3 className="text-sm font-semibold text-foreground">Host services</h3>

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
                          ? "border-success/40 bg-success/15 text-success-ink"
                          : "border-destructive/40 bg-destructive/15 text-destructive-ink"
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
                          <span className="rounded-md border border-solid border-warning/40 bg-warning/15 px-2 py-0.5 text-xs text-warning-ink">
                            Stale build
                          </span>
                        )}
                        {service.duplicate && (
                          <span className="rounded-md border border-solid border-warning/40 bg-warning/15 px-2 py-0.5 text-xs text-warning-ink">
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
