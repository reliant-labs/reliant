// Copyright (c) 2026 Reliant Labs

/**
 * Tests › Overview and Tests › Coverage across several projects: the
 * console's aggregate pages for `tests` and `tests-coverage`
 * (Console/aggregate/registry.ts). Both read ONE GetTestPortfolio answer
 * (hooks/test-queries.ts useTestPortfolio): a row per project on its default
 * branch, and the server's totals across them, which the strip shows as they
 * are (portfolioModel.ts). Nothing is summed or averaged here.
 *
 * Where they differ from one project's page, a hint says what selecting one
 * project shows: a branch picker and the coverage trend (Overview), the
 * package and file tree (Coverage).
 *
 * The reader's access decides the page. Figures across projects merge runs
 * from every environment, so a person or token bound to some environments is
 * refused (PermissionDenied), and the page says so instead of showing part of
 * a total; one project's own page still reads what they may see. A control
 * plane from before GetTestPortfolio answers Unimplemented: totals across
 * projects are not available there, which is a state, not an error.
 */

import { createContext, useContext, useMemo, type ReactNode } from "react";
import { FlaskConical, Info, Lock } from "lucide-react";

import SkeletonLoader from "@/components/forge-ui/skeleton_loader";
import Status from "@/components/forge-ui/status";
import { useTestPortfolio } from "@/hooks/test-queries";
import { testsErrorKind } from "@/services/controlPlane/tests/errors";
import type { PortfolioCoverage, PortfolioProject, TestPortfolio } from "@/services/controlPlane/tests/types";

import { AggregatePage, ProjectBreakdown, type AggregateTotal, type BreakdownColumn, type ProjectAccess } from "../Console/aggregate/AggregatePage";
import { DrillDownHint } from "../Console/aggregate/DrillDownHint";
import type { AggregatePageProps } from "../Console/aggregate/registry";
import type { SeveralApp } from "../Console/severalProjects";
import { formatAgo, formatWhen } from "../Observability/format";
import { StateFrame } from "../Observability/ObsStates";
import {
  coverageColumns,
  coverageCounts,
  coverageKey,
  coverageName,
  coverageTotal,
  languageName,
  portfolioProjects,
  rankByCoverage,
  rowsByProject,
  testsTotals,
} from "./portfolioModel";
import { formatPassRate, formatPercent, runVerdict, shortSha } from "./testsModel";
import { TestsError } from "./TestsStates";

/** What a row's cells read: the portfolio's rows, and the Coverage page's columns. */
interface PortfolioView {
  rows: ReadonlyMap<string, PortfolioProject>;
  columns: readonly PortfolioCoverage[];
  now: number;
}

const PortfolioContext = createContext<PortfolioView>({ rows: new Map(), columns: [], now: 0 });

/** A row with nothing to read: its project has no recorded run (or is not this org's, which reads the same). */
function accessOfRow(rows: ReadonlyMap<string, PortfolioProject>) {
  return (project: SeveralApp): ProjectAccess => (rows.get(project.app)?.hasRuns ? "ok" : "no-data");
}

const NO_COLUMNS: readonly PortfolioCoverage[] = [];

const dash = <span className="text-ink-muted">—</span>;

/** When a run happened: finished, else started, else recorded. */
const ranAt = (row: PortfolioProject): number | null => row.lastRun?.finishedMs ?? row.lastRun?.startedMs ?? row.lastRun?.createdMs ?? null;

// ── Tests › Overview ────────────────────────────────────────────────────────

const TESTS_COLUMNS: readonly BreakdownColumn[] = [
  { header: "Branch" },
  { header: "Pass rate", align: "end" },
  { header: "Latest result", track: "minmax(0,1.2fr)" },
  { header: "Flaky", align: "end", track: "minmax(0,0.6fr)" },
  { header: "Coverage", track: "minmax(0,1.4fr)" },
  { header: "Last run", align: "end" },
];

/** The aggregate Tests › Overview: health of every project in scope on its default branch. */
export function TestsAggregatePage({ page, apps, mode }: AggregatePageProps) {
  const portfolio = useTestPortfolio(portfolioProjects(apps, mode));
  const data = portfolio.data;
  const view = usePortfolioView(data, NO_COLUMNS);
  return (
    <AggregatePage
      title="Tests"
      mode={mode}
      count={apps.length}
      coverage="each on its default branch"
      totals={data ? testsTotals(data.totals) : undefined}
      hint={<DrillDownHint purpose="pick a branch and see its coverage trend over recent commits" projects={apps} page={page} />}
      testId="tests-aggregate"
    >
      <PortfolioState portfolio={portfolio} subject="these projects' tests">
        <PortfolioContext.Provider value={view}>
          <ProjectBreakdown
            projects={apps}
            page={page}
            columns={TESTS_COLUMNS}
            Cells={TestsCells}
            accessOf={accessOfRow(view.rows)}
            rowTestId={(project) => `tests-aggregate-row-${project.key}`}
          />
        </PortfolioContext.Provider>
      </PortfolioState>
    </AggregatePage>
  );
}

/** A project's latest result: its newest report's counts, else its newest run's verdict. */
function latestResult(row: PortfolioProject): { words: string; tone: "success" | "danger" | "neutral" } | null {
  const totals = row.latest?.totals;
  if (totals && totals.failed > 0) return { words: `${totals.failed.toLocaleString("en-US")} failing`, tone: "danger" };
  if (totals && totals.passed > 0) return { words: "Passing", tone: "success" };
  return row.lastRun ? runVerdict(row.lastRun) : null;
}

/** A project's coverage, one figure per language: "Go 71.2% · TypeScript 64.0%" ("71.2%" with one language). */
function coverageWords(row: PortfolioProject): string | null {
  if (row.coverage.length === 0) return null;
  if (row.coverage.length === 1) return formatPercent(row.coverage[0]!.combined.percent);
  return row.coverage.map((c) => `${languageName(c.lang)} ${formatPercent(c.combined.percent)}`).join(" · ");
}

function TestsCells({ project }: { project: SeveralApp }) {
  const { rows, now } = useContext(PortfolioContext);
  const row = rows.get(project.app);
  if (!row) return null;
  const result = latestResult(row);
  const coverage = coverageWords(row);
  const ran = ranAt(row);
  return (
    <>
      <span className="min-w-0 truncate">{row.branch ? <code className="font-mono text-code">{row.branch}</code> : <span className="text-ink-muted">Every branch</span>}</span>
      <span className="text-right tabular-nums">
        {row.passRate === null ? (
          dash
        ) : (
          <span title={`Over the last ${row.passRateRuns} ${row.passRateRuns === 1 ? "run" : "runs"}`}>{formatPassRate(row.passRate)}</span>
        )}
      </span>
      <span className="min-w-0 truncate">{result ? <Status tone={result.tone} label={result.words} size="sm" /> : dash}</span>
      <span className="text-right tabular-nums">{row.flaky}</span>
      <span className="min-w-0 truncate tabular-nums" title={coverage ?? undefined}>
        {coverage ?? <span className="text-ink-muted">No coverage</span>}
      </span>
      <span className="text-right text-meta text-ink-muted">{ran === null ? "—" : <span title={formatWhen(ran, now)}>{formatAgo(ran, now)}</span>}</span>
    </>
  );
}

// ── Tests › Coverage ────────────────────────────────────────────────────────

/** The aggregate Tests › Coverage: every project's newest report per language, lowest first. */
export function CoverageAggregatePage({ page, apps, mode }: AggregatePageProps) {
  const portfolio = useTestPortfolio(portfolioProjects(apps, mode));
  const data = portfolio.data;
  const columns = coverageColumns(data?.totals);
  const view = usePortfolioView(data, columns);
  const ranked = useMemo(() => rankByCoverage(apps, view.rows), [apps, view.rows]);
  const breakdown = useMemo((): readonly BreakdownColumn[] => {
    const languages = columns.length > 0 ? columns.map((c): BreakdownColumn => ({ header: coverageName(c), align: "end" })) : [{ header: "Coverage", align: "end" as const }];
    return [...languages, { header: "Report", align: "end" }];
  }, [columns]);
  const totals = data ? coverageTotals(data) : undefined;
  return (
    <AggregatePage
      title="Coverage"
      mode={mode}
      count={apps.length}
      coverage="the newest report on each project's default branch, generated code left out; lowest coverage first"
      totals={totals}
      hint={<DrillDownHint purpose="browse its package and file tree" projects={apps} page={page} />}
      testId="tests-coverage-aggregate"
    >
      <PortfolioState portfolio={portfolio} subject="these projects' coverage">
        <PortfolioContext.Provider value={view}>
          <ProjectBreakdown
            projects={ranked}
            page={page}
            columns={breakdown}
            Cells={CoverageCells}
            accessOf={accessOfRow(view.rows)}
            rowTestId={(project) => `coverage-aggregate-row-${project.key}`}
          />
        </PortfolioContext.Provider>
      </PortfolioState>
    </AggregatePage>
  );
}

/** The Coverage strip: each (language, granularity) as the server summed it, then how many projects have a report. */
function coverageTotals(data: TestPortfolio): AggregateTotal[] {
  const { totals } = data;
  return [
    ...totals.coverage.map((c) => coverageTotal(c)),
    { label: "Projects with a report", value: `${totals.projectsWithReport.toLocaleString("en-US")} of ${totals.projects.toLocaleString("en-US")}` },
  ];
}

function CoverageCells({ project }: { project: SeveralApp }) {
  const { rows, columns, now } = useContext(PortfolioContext);
  const row = rows.get(project.app);
  if (!row) return null;
  const byKey = new Map(row.coverage.map((c) => [coverageKey(c), c]));
  const updated = row.latest?.updatedMs ?? null;
  return (
    <>
      {columns.length === 0 ? (
        <span className="text-right text-ink-muted">No coverage</span>
      ) : (
        columns.map((column) => {
          const figure = byKey.get(coverageKey(column));
          return (
            <span key={coverageKey(column)} className="text-right tabular-nums" data-testid={`coverage-cell-${column.lang}-${column.granularity}`}>
              {figure ? (
                <>
                  {formatPercent(figure.combined.percent)}
                  <span className="block text-meta text-ink-muted">{coverageCounts(figure)}</span>
                </>
              ) : (
                dash
              )}
            </span>
          );
        })
      )}
      <span className="min-w-0 truncate text-right text-meta text-ink-muted">
        {row.latest ? (
          <>
            <code className="font-mono">{shortSha(row.latest.gitCommit)}</code>
            {updated !== null && <span title={formatWhen(updated, now)}> · {formatAgo(updated, now)}</span>}
          </>
        ) : (
          "None yet"
        )}
      </span>
    </>
  );
}

// ── Shared ──────────────────────────────────────────────────────────────────

function usePortfolioView(data: TestPortfolio | undefined, columns: readonly PortfolioCoverage[]): PortfolioView {
  const rows = useMemo(() => rowsByProject(data), [data]);
  // The answer's moment: relative times are measured from when it arrived, so they do not move as rows re-render.
  return useMemo(() => ({ rows, columns, now: Date.now() }), [rows, columns]);
}

interface PortfolioRead {
  data?: TestPortfolio;
  error: unknown;
  isPending: boolean;
  isFetching: boolean;
  refetch: () => unknown;
  capped: boolean;
}

/**
 * The portfolio read's states around the breakdown: loading, the reader's
 * access, an older control plane, a failure, and notes on what the totals
 * leave out.
 */
function PortfolioState({ portfolio, subject, children }: { portfolio: PortfolioRead; subject: string; children: ReactNode }) {
  if (portfolio.error && !portfolio.data) {
    const kind = testsErrorKind(portfolio.error);
    if (kind === "denied") {
      return (
        <StateFrame testId="portfolio-denied" icon={<Lock className="h-4 w-4" />} title="Your access is limited to some environments">
          <p>
            Figures across projects merge test runs from every environment, so they need test access to the whole organization. Select one project to see the runs
            you can read there.
          </p>
        </StateFrame>
      );
    }
    if (kind === "not-configured") {
      return (
        <StateFrame testId="portfolio-unavailable" icon={<FlaskConical className="h-4 w-4" />} title="Test figures across projects aren't available here yet">
          <p>This control plane answers each project's tests on its own. Select one project to see its tests.</p>
        </StateFrame>
      );
    }
    return <TestsError error={portfolio.error} subject={subject} onRetry={() => void portfolio.refetch()} retrying={portfolio.isFetching} testId="portfolio-error" />;
  }
  if (!portfolio.data) {
    return (
      <div aria-busy="true" aria-label="Loading" data-testid="portfolio-loading">
        <SkeletonLoader variant="table-row" count={4} />
      </div>
    );
  }
  return (
    <>
      {portfolio.capped && <PortfolioNote testId="portfolio-capped">Only the first 50 of the chosen projects are counted: that is as many as one read covers.</PortfolioNote>}
      {portfolio.data.truncated && (
        <PortfolioNote testId="portfolio-truncated">Only the 50 projects with the most recent test runs are counted and listed.</PortfolioNote>
      )}
      {children}
    </>
  );
}

function PortfolioNote({ children, testId }: { children: ReactNode; testId: string }) {
  return (
    <p className="flex items-center gap-2 text-meta text-ink-muted" data-testid={testId}>
      <Info className="size-3.5 shrink-0" aria-hidden="true" />
      {children}
    </p>
  );
}
