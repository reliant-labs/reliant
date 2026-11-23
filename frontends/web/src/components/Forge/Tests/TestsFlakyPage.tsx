// Copyright (c) 2026 Reliant Labs

/**
 * /forge/p/:project/tests/flaky — Tests › Flaky tests: the project's tests
 * that are, or were, flaky (control-plane TestsService.ListFlakyTests; forge
 * docs/proposals/tests-coverage-flakes.md §1.5 and §3.2, phase-0 amendments
 * 6 and 7).
 *
 * Top to bottom: one line of counts with a 30-day sparkline of flake events;
 * a tab per state (Suspected, Flaky, Quarantined, Fixed, or All), held in
 * the URL (`?state=`); one single-line row per test, by flake rate. A row is
 * its state's dot, the test and its package with why it counts as flaky in
 * plain words, the flake rate with a bar, its last 20 runs, when it last
 * flaked, its issue, and a `⋯` menu (Ask an agent to fix, Copy quarantine
 * command, Open issue, Copy test ID). The whole row opens the test.
 *
 * What the page will not do:
 *
 *   - call the flake rate a verdict. It is a Wilson lower bound that RANKS
 *     tests, its n in the tooltip; the state comes from confirmed events on
 *     distinct commits, and `suspected` (one event) is the state to act on.
 *   - count a host or infrastructure failure against a test. Those are host
 *     health, one line below the list, never a row in it.
 *   - make a quarantine. For a customer the product suggests one and never
 *     makes one: the menu copies the command, and the project's lanes apply
 *     the entry it writes.
 *
 * Each row's runs come from the test's own read (GetTest, useTests), the one
 * its page makes, so opening a test is answered from the cache. Flakes are
 * judged on the default branch's runs, across every environment, so the
 * environment scope does not narrow this page.
 */

import { useCallback, useMemo } from "react";
import { useNavigate, useSearch } from "@tanstack/react-router";
import { CircleDot, HelpCircle, MoreHorizontal } from "lucide-react";

import Button from "@/components/forge-ui/button";
import Card from "@/components/forge-ui/card";
import DropdownMenu, { type MenuItem } from "@/components/forge-ui/dropdown_menu";
import Link from "@/components/forge-ui/link";
import Status from "@/components/forge-ui/status";
import Tabs from "@/components/forge-ui/tabs";
import Tag from "@/components/forge-ui/tag";
import Tooltip from "@/components/forge-ui/tooltip";
import { useFlakyTests, useTests, useTestSummary } from "@/hooks/test-queries";
import { toast } from "@/lib/toast-manager";
import type { TestDetail, TestFlake, TestSummary } from "@/services/controlPlane/tests/types";

import { useForgeConsole } from "../Console/consoleContext";
import { formatWhen } from "../Observability/format";
import { ObsLoadingRows, StateFrame } from "../Observability/ObsStates";
import {
  agoWords,
  FLAKE_RATE_MEANS,
  FLAKE_STATE,
  flakeEventsByDay,
  flakeRateWords,
  flakeReason,
  FLAKY_STATE_FILTER_LABEL,
  FLAKY_STATE_FILTERS,
  formatFlakeRate,
  issueLabel,
  pickFlakySearch,
  quarantineCommand,
  suggestsQuarantine,
  type FlakySearch,
  type FlakyStateFilter,
} from "./flakyModel";
import { FIX_LABEL, useFixFlakyTest } from "./TestsAgentActions";
import { safeOutUrl, shortPackage } from "./TestsParts";
import { MiniRunStrip } from "./TestsRunStrip";
import { TestsFrame } from "./testsScope";
import { TestsError, TestsNoRunsYet } from "./TestsStates";

const SUBTITLE = "Tests that failed and then passed on the same code, last 30 days";

/** How the list is judged: the \"?\" beside the tabs (and the Learn doc). */
const RULE =
  "Judged on the default branch's runs over 30 days. Suspected is one confirmed flake: the state to act on. Flaky is two or more, on different commits. The flake rate ranks the list and decides nothing. Host and infrastructure failures never count.";

export function TestsFlakyPage() {
  const { forgeProject, href, projectId } = useForgeConsole();
  const raw = useSearch({ strict: false }) as Record<string, unknown>;
  const search = useMemo(() => pickFlakySearch(raw), [raw]);
  const navigate = useNavigate();
  const onSearch = useCallback(
    (next: FlakySearch) =>
      void navigate({
        to: ".",
        search: (prev: Record<string, unknown>) => ({ env: prev.env as string | undefined, ...next }),
        replace: true,
      }),
    [navigate]
  );
  return (
    <TestsFrame title="Flaky tests" subtitle={SUBTITLE} testId="console-tests-flaky">
      <TestsFlakyView project={forgeProject} projectId={projectId} search={search} onSearch={onSearch} testHref={(testId) => href({ kind: "tests-flaky-test", test: testId })} />
    </TestsFrame>
  );
}

export interface TestsFlakyViewProps {
  project: string;
  projectId?: string | null;
  search: FlakySearch;
  onSearch: (next: FlakySearch) => void;
  testHref: (testId: string) => string;
  now?: number;
}

/** The page below its header, free of the console: what tests and the preview render. */
export function TestsFlakyView({ project, projectId, search, onSearch, testHref, now }: TestsFlakyViewProps) {
  const list = useFlakyTests(project, search.state ? { state: search.state } : {});
  // Every state, for the counts line and the tabs (the same read as the list when no tab is chosen).
  const every = useFlakyTests(project, {});
  // The default branch's counts per state, its host health, and whether anything was ever recorded.
  const summary = useTestSummary(project);
  const rows = useMemo(() => list.data?.pages.flatMap((page) => page.tests) ?? [], [list.data]);
  const all = useMemo(() => every.data?.pages.flatMap((page) => page.tests) ?? [], [every.data]);
  const ids = useMemo(() => [...new Set([...all, ...rows].map((flake) => flake.test.id))], [all, rows]);
  const reads = useTests(project, ids);
  const details = useMemo(() => {
    const byId = new Map<string, TestDetail>();
    ids.forEach((id, i) => {
      const data = reads[i]?.data;
      if (data) byId.set(id, data);
    });
    return byId;
  }, [ids, reads]);
  const at = now ?? Date.now();

  if (list.isLoading) {
    return (
      <Card padding="none">
        <ObsLoadingRows testId="tests-flaky-loading" />
      </Card>
    );
  }
  if (list.error && !list.data) {
    return <TestsError error={list.error} subject="this project's flaky tests" onRetry={() => void list.refetch()} retrying={list.isFetching} />;
  }
  const record = summary.data;
  const neverRan = !!record && !record.latest && !record.lastRun;
  if (rows.length === 0 && !search.state && neverRan) return <TestsNoRunsYet projectId={projectId} testId="tests-flaky-no-runs" />;

  return (
    <div className="space-y-5" data-testid="tests-flaky">
      <SummaryStrip summary={record} all={all} details={details} now={at} />
      <StateTabs search={search} onSearch={onSearch} summary={record} all={all} />
      {rows.length === 0 ? (
        search.state ? (
          <StateFrame
            testId="tests-flaky-no-match"
            icon={null}
            title={`No ${FLAKY_STATE_FILTER_LABEL[search.state].toLowerCase()} tests`}
            actions={
              <Button variant="outline" size="sm" onClick={() => onSearch({})} data-testid="tests-flaky-no-match-clear">
                Show all
              </Button>
            }
          >
            <p>{STATE_EMPTY[search.state]}</p>
          </StateFrame>
        ) : (
          <StateFrame testId="tests-flaky-none" icon={null} title="No flaky tests">
            <p>Nothing failed and then passed on the same code in the last 30 days.</p>
          </StateFrame>
        )
      ) : (
        <FlakyList
          project={project}
          projectId={projectId}
          defaultBranch={record?.branch || undefined}
          rows={rows}
          details={details}
          testHref={testHref}
          now={at}
          hasMore={!!list.hasNextPage}
          loadingMore={list.isFetchingNextPage}
          onLoadMore={() => void list.fetchNextPage()}
        />
      )}
      <HostHealth summary={record} loading={summary.isLoading} />
    </div>
  );
}

const STATE_EMPTY: Record<FlakyStateFilter, string> = {
  suspected: "No test has a single confirmed flake waiting to be acted on.",
  flaky: "No test flaked on two or more commits in the last 30 days.",
  quarantined: "No test is quarantined on the default branch.",
  fixed: "No flaky test has gone 14 days without a flake since its file changed.",
};

/** "4 flaky · 3 suspected · 2 quarantined (1 overdue)", and a 30-day sparkline of flake events. */
function SummaryStrip({ summary, all, details, now }: { summary?: TestSummary; all: TestFlake[]; details: Map<string, TestDetail>; now: number }) {
  const overdue = all.filter((flake) => flake.quarantine?.overdue).length;
  const days = useMemo(() => flakeEventsByDay([...details.values()].flatMap((detail) => detail.events), now), [details, now]);
  const total = days.reduce((sum, n) => sum + n, 0);
  const peak = Math.max(1, ...days);
  const figure = (n: number | undefined, word: string, testId: string) => (
    <span data-testid={testId}>
      <span className="font-medium tabular-nums text-ink">{n ?? "—"}</span> {word}
    </span>
  );
  return (
    <div className="flex flex-wrap items-center justify-between gap-x-6 gap-y-2" data-testid="tests-flaky-summary">
      <p className="flex flex-wrap items-center gap-x-1.5 text-ui text-ink-muted">
        {figure(summary?.flaky, "flaky", "tests-flaky-count-flaky")}
        <span aria-hidden="true">·</span>
        {figure(summary?.suspected, "suspected", "tests-flaky-count-suspected")}
        <span aria-hidden="true">·</span>
        {figure(summary?.quarantined, "quarantined", "tests-flaky-count-quarantined")}
        {overdue > 0 && (
          <span className="text-warning-ink" data-testid="tests-flaky-count-overdue">
            ({overdue} overdue)
          </span>
        )}
      </p>
      {details.size > 0 && (
        <div className="flex items-center gap-2.5 text-meta text-ink-muted" data-testid="tests-flaky-sparkline">
          <span>
            <span className="tabular-nums text-ink">{total}</span> flake {total === 1 ? "event" : "events"}, 30 days
          </span>
          <span className="flex h-5 items-end gap-0.5" role="img" aria-label={`Flake events per day over 30 days: ${total} in all`}>
            {days.map((n, i) => (
              <span
                key={i}
                className="block w-1 rounded-full"
                // Data colour: the series' one hue (web-styling, "Chart series"); a quiet stub for an empty day.
                style={n > 0 ? { height: `${Math.max(30, (n / peak) * 100)}%`, backgroundColor: "hsl(var(--chart-1))" } : { height: "2px", backgroundColor: "hsl(var(--chart-1) / 0.25)" }}
              />
            ))}
          </span>
        </div>
      )}
    </div>
  );
}

/** The state tabs, with the default branch's count on each, and how flakes are judged behind the "?". */
function StateTabs({ search, onSearch, summary, all }: { search: FlakySearch; onSearch: (next: FlakySearch) => void; summary?: TestSummary; all: TestFlake[] }) {
  const counts: Record<FlakyStateFilter | "all", number | undefined> = {
    all: all.length || undefined,
    suspected: summary?.suspected,
    flaky: summary?.flaky,
    quarantined: summary?.quarantined,
    fixed: all.filter((flake) => flake.state === "fixed").length || undefined,
  };
  const tabs = [
    { id: "all", label: "All", badge: counts.all },
    ...FLAKY_STATE_FILTERS.map((state) => ({ id: state, label: FLAKY_STATE_FILTER_LABEL[state], badge: counts[state] || undefined })),
  ];
  return (
    <div className="relative" data-testid="tests-flaky-filters">
      {/* Scrolls sideways on a phone rather than clipping the last tab. */}
      <div className="-mx-1 overflow-x-auto px-1 sm:pr-8">
        <Tabs
          tabs={tabs}
          activeTab={search.state ?? "all"}
          ariaLabel="Flaky tests by state"
          onChange={(id) => onSearch(id === "all" ? {} : { state: id as FlakyStateFilter })}
        />
      </div>
      <div className="absolute bottom-2.5 right-0 hidden sm:block">
        <Tooltip content={RULE} tooltipClassName="max-w-xs" side="left">
          <button type="button" className="inline-flex h-6 w-6 items-center justify-center rounded-md text-ink-muted hover:text-ink focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring" aria-label="How flaky tests are judged" data-testid="tests-flaky-rule">
            <HelpCircle className="h-4 w-4" aria-hidden="true" />
          </button>
        </Tooltip>
      </div>
    </div>
  );
}

/*
 * The list's columns. md and up: test · rate · last 20 runs · last flaked ·
 * issue · menu. Under md the runs strip and the time drop out; under sm the
 * rate too. Fixed widths keep each column on one line across rows.
 */
const COLUMNS = "grid grid-cols-[minmax(0,1fr)_2rem] items-center gap-x-4 sm:grid-cols-[minmax(0,1fr)_5rem_2rem] md:grid-cols-[minmax(0,1fr)_5rem_7rem_5rem_1.5rem_2rem]";

function FlakyList({
  project,
  projectId,
  defaultBranch,
  rows,
  details,
  testHref,
  now,
  hasMore,
  loadingMore,
  onLoadMore,
}: {
  project: string;
  projectId?: string | null;
  defaultBranch?: string;
  rows: TestFlake[];
  details: Map<string, TestDetail>;
  testHref: (testId: string) => string;
  now: number;
  hasMore: boolean;
  loadingMore: boolean;
  onLoadMore: () => void;
}) {
  const topRate = Math.max(...rows.map((flake) => flake.score), 0);
  return (
    <Card padding="none" className="overflow-hidden" data-testid="tests-flaky-list">
      <div className={`${COLUMNS} border-b border-border-subtle px-4 py-2 text-meta font-medium text-ink-muted`} aria-hidden="true">
        <span>Test</span>
        <span className="hidden text-right sm:block">Flake rate</span>
        <span className="hidden md:block">Last 20 runs</span>
        <span className="hidden md:block">Last flaked</span>
        <span className="hidden md:block" />
        <span />
      </div>
      <ul aria-label="Flaky tests, by flake rate" className="divide-y divide-border-subtle">
        {rows.map((flake) => (
          <FlakyRow
            key={flake.test.id}
            flake={flake}
            detail={details.get(flake.test.id)}
            topRate={topRate}
            href={testHref(flake.test.id)}
            now={now}
            project={project}
            projectId={projectId}
            defaultBranch={defaultBranch}
          />
        ))}
      </ul>
      {hasMore && (
        <div className="border-t border-border-subtle px-4 py-2">
          <Button variant="ghost" size="sm" onClick={onLoadMore} loading={loadingMore} data-testid="tests-flaky-more">
            Load more tests
          </Button>
        </div>
      )}
    </Card>
  );
}

/** States a person still has to act on. */
const ACTIONABLE = new Set<TestFlake["state"]>(["suspected", "flaky", "quarantined"]);

function FlakyRow({
  flake,
  detail,
  topRate,
  href,
  now,
  project,
  projectId,
  defaultBranch,
}: {
  flake: TestFlake;
  detail: TestDetail | undefined;
  topRate: number;
  href: string;
  now: number;
  project: string;
  projectId?: string | null;
  defaultBranch?: string;
}) {
  const state = FLAKE_STATE[flake.state];
  const { test } = flake;
  const reason = flakeReason(flake);
  const issue = safeOutUrl(flake.issueUrl || flake.quarantine?.issueUrl || "");
  const fix = useFixFlakyTest({ project, projectId, flake, defaultBranch });
  const quarantine = flake.quarantine;

  const items: MenuItem[] = [];
  if (ACTIONABLE.has(flake.state) && fix.availability.available) {
    items.push({ label: FIX_LABEL, onClick: fix.start, testId: "tests-flake-menu-fix" });
  }
  if (suggestsQuarantine(flake)) {
    items.push({ label: "Copy quarantine command", onClick: () => void copy(quarantineCommand(flake), "Quarantine command copied"), testId: "tests-flake-menu-quarantine" });
  }
  if (issue) items.push({ label: `Open issue ${issueLabel(issue.href)}`, onClick: () => window.open(issue.href, "_blank", "noopener,noreferrer"), testId: "tests-flake-menu-issue" });
  items.push({ label: "Copy test ID", onClick: () => void copy(test.id, "Test ID copied"), testId: "tests-flake-menu-copy-id" });

  return (
    <li
      className={`group relative ${COLUMNS} px-4 py-1.5 transition-colors hover:bg-ink/5`}
      data-testid={`tests-flake-row-${test.name}`}
      data-state={flake.state}
    >
      <div className="flex min-w-0 items-center gap-3">
        <span title={state.means} className="shrink-0">
          <Status tone={state.tone} label={state.label} hideLabel size="sm" />
        </span>
        <div className="min-w-0">
          <div className="flex min-w-0 items-center gap-2">
            {/* The row's one link, stretched over the row: the menu and the issue sit above it. */}
            <Link
              href={href}
              className="truncate text-ui font-medium leading-5 text-ink after:absolute after:inset-0 after:content-[''] hover:underline focus-visible:outline-none focus-visible:after:ring-2 focus-visible:after:ring-inset focus-visible:after:ring-ring"
              aria-label={`${test.name}: ${state.label.toLowerCase()}, flake rate ${flakeRateWords(flake)}`}
            >
              {test.name || test.id}
            </Link>
            {quarantine?.overdue ? (
              <span className="shrink-0" data-testid={`tests-flake-quarantine-${test.name}`}>
                <Tag tone="warning" title={quarantine.expiresMs ? `Quarantine expired ${formatWhen(quarantine.expiresMs, now)}: still skipped, and overdue for a fix` : "Quarantine past its expiry"}>
                  Overdue
                </Tag>
              </span>
            ) : quarantine ? (
              <span className="shrink-0" data-testid={`tests-flake-quarantine-${test.name}`}>
                <Tag title={quarantine.path}>Quarantined</Tag>
              </span>
            ) : null}
          </div>
          <p className="truncate text-meta leading-4 text-ink-muted" title={test.package}>
            {shortPackage(test.package).replace(/^…\//, "")}
            {reason && <span> · {reason}</span>}
          </p>
        </div>
      </div>
      <div className="hidden items-center justify-end gap-2 sm:flex" title={`${flakeRateWords(flake)}. ${FLAKE_RATE_MEANS}`} data-testid={`tests-flake-rate-${test.name}`}>
        <span className="block h-1 w-8 overflow-hidden rounded-full bg-ink-muted/15" aria-hidden="true">
          <span className="block h-full rounded-full bg-ink-muted/60" style={{ width: `${topRate > 0 ? Math.max(6, (flake.score / topRate) * 100) : 0}%` }} />
        </span>
        <span className="w-10 text-right text-ui tabular-nums text-ink">{flake.scoreRuns > 0 ? formatFlakeRate(flake.score) : "—"}</span>
      </div>
      <div className="hidden md:block">
        <MiniRunStrip outcomes={detail?.outcomes} />
      </div>
      <div className="hidden whitespace-nowrap text-meta text-ink-muted md:block">
        {flake.lastEventMs === null ? "—" : <span title={formatWhen(flake.lastEventMs, now)}>{agoWords(flake.lastEventMs, now)}</span>}
      </div>
      <div className="relative z-10 hidden md:block">
        {issue && (
          <a
            href={issue.href}
            target="_blank"
            rel="noopener noreferrer"
            className="no-color inline-flex h-6 w-6 items-center justify-center rounded-md text-ink-muted hover:text-ink focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
            title={`Issue ${issueLabel(issue.href)} on ${issue.host}`}
            aria-label={`Issue ${issueLabel(issue.href)} (opens in a new tab)`}
            data-testid={`tests-flake-issue-${test.name}`}
          >
            <CircleDot className="h-3.5 w-3.5" aria-hidden="true" />
          </a>
        )}
      </div>
      <div className="relative z-10 flex justify-end">
        <DropdownMenu
          trigger={<MoreHorizontal className="h-4 w-4" aria-hidden="true" />}
          triggerLabel={`Actions for ${test.name}`}
          triggerClassName="inline-flex h-7 w-7 items-center justify-center rounded-md text-ink-muted hover:bg-ink/5 hover:text-ink focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
          groups={[{ items }]}
          menuClassName="min-w-[13rem]"
        />
        {fix.dialog}
      </div>
    </li>
  );
}

async function copy(text: string, done: string) {
  try {
    await navigator.clipboard.writeText(text);
    toast.success(done);
  } catch {
    toast.error("Couldn't copy: the browser denied the clipboard.");
  }
}

/** Failures the tests did not cause, one line: never counted against a test. */
function HostHealth({ summary, loading }: { summary?: TestSummary; loading: boolean }) {
  const host = summary?.hostFailures30d;
  return (
    <p className="flex flex-wrap items-center gap-x-1.5 text-meta text-ink-muted" data-testid="tests-flaky-host-health">
      <span>Host and infrastructure failures never count against a test.</span>
      {!loading && host !== undefined && (
        <Tooltip content="A network or download error, a rate limit, a full disk, a port in use, a docker timeout, or a test timing out on a loaded host." tooltipClassName="max-w-xs">
          <span className="underline decoration-dotted underline-offset-2" data-testid="tests-flaky-host-failures">
            {host === 0 ? "None in the last 30 days." : `${host.toLocaleString()} in the last 30 days.`}
          </span>
        </Tooltip>
      )}
    </p>
  );
}
