// Copyright (c) 2026 Reliant Labs

/**
 * The project Overview's two Tests surfaces (forge
 * docs/proposals/tests-coverage-flakes.md §3.2, "Elsewhere"):
 *
 *   OverviewTestsCard    a card beside Recent releases: the default branch's
 *                        coverage with its trend, the pass rate, and the
 *                        flaky count, linking into Tests
 *   TestsAttentionRows   the Tests rows of "Needs attention": suspected and
 *                        newly flaky tests, overdue quarantines, coverage
 *                        that fell (surfacesModel.ts testsAttentionItems)
 *
 * Both read the default branch's GetTestSummary, the Tests Overview's own
 * read (one cache entry for the three), and ignore the console's
 * environment scope like every Tests view (testsScope.tsx).
 *
 * A control plane without TestsService, or a reader without tests:read, gets
 * neither: the Overview is about the running app, and a card saying "not
 * available" on every project would be noise. The Tests group in the nav
 * stays, and its pages say why.
 */

import { useMemo, type ReactNode } from "react";
import { ArrowRight, FlaskConical } from "lucide-react";

import Link from "@/components/forge-ui/link";
import Status from "@/components/forge-ui/status";
import { useFlakyTests, useTestSummary } from "@/hooks/test-queries";
import { testsErrorKind } from "@/services/controlPlane/tests/errors";
import type { FlakyFilters } from "@/services/controlPlane/tests/types";
import { cn } from "@/lib/utils";

import { flakyTestPath, testsPath, withSearch } from "../../Console/paths";
import { Sparkline } from "../../LiveState/Sparkline";
import { formatDelta, formatPassRate, formatPercent, headlineCoverage } from "../testsModel";
import { testsAttentionItems, type TestsAttentionHrefs } from "./surfacesModel";

/** Whether a failed summary read hides the surfaces (no service, no grant) rather than showing a failure. */
const hides = (error: unknown) => {
  const kind = testsErrorKind(error);
  return kind === "not-configured" || kind === "denied";
};

/** The console's links for `project`'s Tests rows. */
export function testsAttentionHrefs(project: string): TestsAttentionHrefs {
  return {
    flaky: (state) => withSearch(testsPath(project, "flaky"), { state }),
    test: (testId) => flakyTestPath(project, testId),
    coverage: (path, commit) => withSearch(testsPath(project, "coverage"), { path: path || undefined, commit: commit || undefined }),
  };
}

// ── The card ────────────────────────────────────────────────────────────────

const RAIL_LINK = "no-color -mr-2 inline-flex h-7 items-center gap-1 rounded-md px-2 text-meta text-ink-muted hover:bg-ink/5 hover:text-ink";

/**
 * `frame` wraps the card in the Overview's own block (its LandingCard), so
 * the card can render nothing at all, frame included, when it hides.
 */
export function OverviewTestsCard({ project, frame }: { project: string; frame: (children: ReactNode) => ReactNode }) {
  const summary = useTestSummary(project);
  const testsHref = testsPath(project);

  if (summary.error && hides(summary.error)) return null;

  const header = (
    <div className="flex min-h-7 items-center justify-between gap-3">
      <h2 className="text-section text-ink">Tests</h2>
      <Link href={testsHref} className={RAIL_LINK} data-testid="overview-tests-link">
        Tests
        <ArrowRight className="h-3 w-3" aria-hidden="true" />
      </Link>
    </div>
  );

  let body: ReactNode;
  if (summary.isLoading) {
    body = <div className="h-24 animate-pulse rounded-lg bg-ink/5 motion-reduce:animate-none" aria-label="Loading tests" />;
  } else if (summary.error || !summary.data) {
    body = (
      <p className="text-ui text-ink-muted" data-testid="overview-tests-error">
        Test results could not be read right now.
      </p>
    );
  } else if (!summary.data.latest && !summary.data.lastRun) {
    body = (
      <div className="space-y-2 text-ui text-ink-muted" data-testid="overview-tests-empty">
        <p className="flex items-center gap-2 text-ink">
          <FlaskConical className="size-4 text-ink-muted" aria-hidden="true" />
          No test runs yet
        </p>
        <p>
          Coverage, pass rate and flaky tests appear here once forge records a run: <code className="font-mono text-code">reliant forge test report --upload</code> after
          the project&apos;s lanes.
        </p>
        <Link href={testsHref} className="no-color text-primary-ink hover:underline">
          How to record runs
        </Link>
      </div>
    );
  } else {
    const data = summary.data;
    const coverage = headlineCoverage(data.latest);
    const trend = data.trend.map((point) => point.combinedPercent);
    body = (
      <div className="space-y-3" data-testid="overview-tests-ready">
        <div className="flex items-end justify-between gap-3">
          <div className="min-w-0">
            <div className="text-meta text-ink-muted">Coverage on {data.branch}</div>
            <div className="text-2xl font-semibold tabular-nums text-ink" data-testid="overview-tests-coverage">
              {coverage ? formatPercent(coverage.percent) : "—"}
            </div>
            <div className="text-meta tabular-nums text-ink-muted">
              {coverage ? (
                coverage.delta !== null ? (
                  <>
                    <span className={cn(coverage.delta < 0 ? "text-danger-ink" : "text-ink")} data-testid="overview-tests-delta">
                      {formatDelta(coverage.delta)}
                    </span>{" "}
                    on the last report
                  </>
                ) : coverage.lanes.length === 1 ? (
                  `${coverage.lanes[0]!.lane} only so far`
                ) : (
                  `${coverage.lanes.length} lanes merged`
                )
              ) : data.latest ? (
                "the newest report has no coverage"
              ) : (
                "its runs are still being processed"
              )}
            </div>
          </div>
          {trend.length >= 2 && <Sparkline values={trend} label={`Coverage of ${data.branch} over its last ${trend.length} commits`} width={112} height={36} />}
        </div>
        <dl className="grid grid-cols-2 gap-3 border-t border-border-subtle pt-3 text-meta">
          <div className="min-w-0">
            <dt className="text-ink-muted">Pass rate</dt>
            <dd className="text-ui font-medium tabular-nums text-ink" title={data.passRateRuns > 0 ? `over the last ${data.passRateRuns} runs` : undefined} data-testid="overview-tests-pass-rate">
              {data.passRate === null ? <span className="font-normal text-ink-muted">no run judged</span> : formatPassRate(data.passRate)}
            </dd>
          </div>
          <div className="min-w-0">
            <dt className="text-ink-muted">Flaky tests</dt>
            <dd className="text-ui font-medium tabular-nums text-ink" data-testid="overview-tests-flaky">
              <Link href={testsPath(project, "flaky")} className="no-color hover:underline">
                {data.flaky}
              </Link>
              {(data.suspected > 0 || data.quarantined > 0) && (
                <span className="ml-1 font-normal text-ink-muted">
                  {[data.suspected > 0 ? `${data.suspected} suspected` : "", data.quarantined > 0 ? `${data.quarantined} quarantined` : ""].filter(Boolean).join(" · ")}
                </span>
              )}
            </dd>
          </div>
        </dl>
      </div>
    );
  }

  return frame(
    <section className="min-w-0 space-y-3" data-testid="overview-tests">
      {header}
      {body}
    </section>
  );
}

// ── Needs attention ─────────────────────────────────────────────────────────

const EVERY_STATE: FlakyFilters = {};

/**
 * The Tests rows of "Needs attention", under their own label: the default
 * branch's, whatever environment the Overview is scoped to. Nothing at all
 * while the summary is loading, fails or has nothing to say: the section's
 * own state already speaks for the page.
 */
export function TestsAttentionRows({ project, now }: { project: string; now?: number }) {
  const summary = useTestSummary(project);
  const anyFlake = !!summary.data && summary.data.suspected + summary.data.flaky + summary.data.quarantined > 0;
  const flaky = useFlakyTests(project, anyFlake ? EVERY_STATE : null);
  const first = flaky.data?.pages[0];
  const items = useMemo(
    () =>
      summary.data
        ? testsAttentionItems({ summary: summary.data, flakes: first?.tests ?? [], flakesMore: !!first?.nextPageToken, now: now ?? Date.now(), hrefs: testsAttentionHrefs(project) })
        : [],
    [summary.data, first, now, project]
  );
  if (items.length === 0) return null;
  return (
    <div className="pt-4" data-testid="overview-attention-tests">
      <p className="pb-2 text-meta font-medium text-ink-muted">
        Tests <span className="font-normal">· {summary.data!.branch}, every environment</span>
      </p>
      <ul className="border-t border-border" aria-label="Tests that need attention">
        {items.map((item) => (
          <li key={item.key} className="border-b border-border-subtle" data-testid={`attention-${item.key}`} data-tone={item.tone}>
            <Link href={item.href} className="no-color flex min-h-[44px] items-center gap-3 px-3 py-2 hover:bg-ink/5 max-sm:flex-wrap">
              <Status tone={item.tone} />
              <span className="min-w-0 flex-1 truncate text-ui text-ink">
                <span className="font-medium">{item.subject}</span> {item.what}
              </span>
              <span className="max-w-64 truncate text-right text-meta text-ink-muted max-sm:w-full max-sm:max-w-none max-sm:pl-5 max-sm:text-left" title={item.detail}>
                {item.detail}
              </span>
            </Link>
          </li>
        ))}
      </ul>
    </div>
  );
}
