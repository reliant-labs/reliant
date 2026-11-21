// Copyright (c) 2026 Reliant Labs

/**
 * /forge/p/:project/tests/flaky — Tests › Flaky tests: the project's tests
 * that are, or were, flaky (control-plane TestsService.ListFlakyTests; forge
 * docs/proposals/tests-coverage-flakes.md §1.5 and §3.2, phase-0 amendments
 * 6 and 7).
 *
 * One row per test, by flake rate: its state, the flake rate with its n,
 * its confirmed events in 30 days and the signals that caught them, its last
 * event, its quarantine (the entry's path, and "overdue" past its expiry) and
 * its issue. Under each test that still needs acting on: the command that
 * quarantines it, to copy, and the slot for the agent that fixes it (not
 * wired yet). A row opens the test.
 *
 * What the page will not do:
 *
 *   - call the flake rate a verdict. It is a Wilson lower bound that RANKS
 *     tests and is shown with its n; the state comes from confirmed events on
 *     distinct commits, and `suspected` (one event) is the state to act on.
 *   - count a host or infrastructure failure against a test. Those are host
 *     health, below the table, never a row in it.
 *   - make a quarantine. For a customer the product suggests one and never
 *     makes one: the command is shown, and the project's lanes apply the entry
 *     it writes.
 *
 * The state filter is the URL's (`?state=`). Flakes are judged on the
 * default branch's runs, across every environment, so the environment scope
 * does not narrow this page.
 */

import { useCallback, useId, useMemo, type ReactNode } from "react";
import { useNavigate, useSearch } from "@tanstack/react-router";
import { Bot, Server } from "lucide-react";

import Button from "@/components/forge-ui/button";
import Card from "@/components/forge-ui/card";
import DataTable, { type DataTableColumn } from "@/components/forge-ui/data_table";
import { FilterBar, FilterChip } from "@/components/forge-ui/filter_bar";
import Status from "@/components/forge-ui/status";
import Tag from "@/components/forge-ui/tag";
import { useFlakyTests, useTestSummary } from "@/hooks/test-queries";
import type { TestFlake, TestSummary } from "@/services/controlPlane/tests/types";

import { useForgeConsole } from "../Console/consoleContext";
import { CommandLine } from "../GettingStarted";
import { formatAgo, formatWhen } from "../Observability/format";
import { ObsLoadingRows, StateFrame } from "../Observability/ObsStates";
import {
  FLAKE_RATE_MEANS,
  FLAKE_STATE,
  FLAKY_STATE_FILTER_LABEL,
  FLAKY_STATE_FILTERS,
  flakeRateWords,
  issueLabel,
  pickFlakySearch,
  quarantineCommand,
  SIGNAL_LABEL,
  suggestsQuarantine,
  type FlakySearch,
  type FlakyStateFilter,
} from "./flakyModel";
import { OutLink, Section, TestName } from "./TestsParts";
import { TestsFrame } from "./testsScope";
import { TestsError, TestsNoRunsYet } from "./TestsStates";

const SUBTITLE = "Tests that failed and then passed on the same code, ranked by flake rate. Host and infrastructure failures are listed apart and never count against a test.";

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
  // The default branch's counts per state, its host health, and whether anything was ever recorded.
  const summary = useTestSummary(project);
  const rows = useMemo(() => list.data?.pages.flatMap((page) => page.tests) ?? [], [list.data]);
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
    <div className="space-y-4" data-testid="tests-flaky">
      <div className="space-y-2">
        <StateFilters search={search} onSearch={onSearch} summary={record} />
        <p className="text-meta text-ink-muted" data-testid="tests-flaky-rule">
          Judged on {record?.branch ? <code className="font-mono text-ink">{record.branch}</code> : "the default branch"}&apos;s runs over 30 days. <strong className="font-medium text-ink">Suspected</strong> (one
          confirmed flake) is the state to act on; flaky is two or more, on different commits. The flake rate ranks the list and decides nothing.
        </p>
      </div>
      {rows.length === 0 ? (
        search.state ? (
          <StateFrame
            testId="tests-flaky-no-match"
            icon={null}
            title={`No ${FLAKY_STATE_FILTER_LABEL[search.state].toLowerCase()} tests`}
            actions={
              <Button variant="outline" size="sm" onClick={() => onSearch({})} data-testid="tests-flaky-no-match-clear">
                Show every state
              </Button>
            }
          >
            <p>{STATE_EMPTY[search.state]}</p>
          </StateFrame>
        ) : (
          <StateFrame testId="tests-flaky-none" icon={null} title="No flaky tests">
            <p>No test failed and then passed on the same code in the last 30 days. A test that does shows up here as suspected, with the command that quarantines it.</p>
          </StateFrame>
        )
      ) : (
        <FlakyTable
          rows={rows}
          testHref={testHref}
          now={at}
          hasMore={!!list.hasNextPage}
          loadingMore={list.isFetchingNextPage}
          onLoadMore={() => void list.fetchNextPage()}
        />
      )}
      <HostHealth summary={summary.data} loading={summary.isLoading} />
    </div>
  );
}

const STATE_EMPTY: Record<FlakyStateFilter, string> = {
  suspected: "No test has a single confirmed flake waiting to be acted on.",
  flaky: "No test flaked on two or more commits in the last 30 days.",
  quarantined: "No test has a quarantine entry on the default branch.",
  fixed: "No test that was flaky has gone 14 days without a flake since its file changed.",
};

/** The state filter: one chip per state, with the default branch's count where the summary has one. */
function StateFilters({ search, onSearch, summary }: { search: FlakySearch; onSearch: (next: FlakySearch) => void; summary?: TestSummary }) {
  const counts: Partial<Record<FlakyStateFilter, number>> = summary ? { suspected: summary.suspected, flaky: summary.flaky, quarantined: summary.quarantined } : {};
  return (
    <FilterBar label="Filter flaky tests by state" active={search.state ? 1 : 0} onClear={() => onSearch({})} testId="tests-flaky-filters">
      {FLAKY_STATE_FILTERS.map((state) => (
        <FilterChip
          key={state}
          on={search.state === state}
          count={counts[state]}
          onClick={() => onSearch(search.state === state ? {} : { state })}
          title={FLAKE_STATE[state].means}
          testId={`tests-flaky-filter-${state}`}
        >
          {FLAKY_STATE_FILTER_LABEL[state]}
        </FilterChip>
      ))}
    </FilterBar>
  );
}

type Row = { flake: TestFlake };

/** States a person still has to act on: quarantine it, or fix it. */
const ACTIONABLE = new Set<TestFlake["state"]>(["suspected", "flaky", "quarantined"]);

function FlakyTable({
  rows,
  testHref,
  now,
  hasMore,
  loadingMore,
  onLoadMore,
}: {
  rows: TestFlake[];
  testHref: (testId: string) => string;
  now: number;
  hasMore: boolean;
  loadingMore: boolean;
  onLoadMore: () => void;
}) {
  const columns: DataTableColumn<Row>[] = [
    { key: "test", header: "Test", className: "w-full max-w-0", render: (_, { flake }) => <TestName test={flake.test} /> },
    {
      key: "state",
      header: "State",
      render: (_, { flake }) => (
        <span title={FLAKE_STATE[flake.state].means}>
          <Status tone={FLAKE_STATE[flake.state].tone} label={FLAKE_STATE[flake.state].label} />
        </span>
      ),
    },
    {
      key: "rate",
      header: "Flake rate",
      numeric: true,
      render: (_, { flake }) => (
        <span className="text-ink" title={FLAKE_RATE_MEANS} data-testid={`tests-flake-rate-${flake.test.name}`}>
          {flakeRateWords(flake)}
        </span>
      ),
    },
    {
      key: "events",
      header: "Events, 30 days",
      render: (_, { flake }) => (
        <span className="block">
          <span className="block tabular-nums text-ink">{flake.confirmedEvents30d}</span>
          {flake.signals.length > 0 && <span className="block text-meta text-ink-muted">{flake.signals.map((signal) => SIGNAL_LABEL[signal]).join(" · ")}</span>}
        </span>
      ),
    },
    {
      key: "last",
      header: "Last event",
      render: (_, { flake }) =>
        flake.lastEventMs === null ? (
          <span className="text-ink-muted">—</span>
        ) : (
          <span className="whitespace-nowrap text-ink" title={formatWhen(flake.lastEventMs, now)}>
            {formatAgo(flake.lastEventMs, now)}
          </span>
        ),
    },
    {
      key: "quarantine",
      header: "Quarantine",
      render: (_, { flake }) =>
        flake.quarantine ? (
          <span className="block max-w-[16rem]" data-testid={`tests-flake-quarantine-${flake.test.name}`}>
            <span className="flex items-center gap-1.5">
              <span className="text-ink">Quarantined</span>
              {flake.quarantine.overdue && (
                <Tag tone="danger" title={flake.quarantine.expiresMs ? `Expired ${formatWhen(flake.quarantine.expiresMs, now)}: still skipped, and overdue for a fix` : "Past its expiry"}>
                  overdue
                </Tag>
              )}
            </span>
            <span className="block truncate font-mono text-meta text-ink-muted" title={flake.quarantine.path}>
              {flake.quarantine.path}
            </span>
          </span>
        ) : (
          <span className="text-ink-muted">—</span>
        ),
    },
    {
      key: "issue",
      header: "Issue",
      interactive: true,
      render: (_, { flake }) => {
        const url = flake.issueUrl || flake.quarantine?.issueUrl || "";
        return url ? <OutLink href={url}>{issueLabel(url)}</OutLink> : <span className="text-ink-muted">—</span>;
      },
    },
  ];
  return (
    <Card padding="none" className="overflow-hidden" data-testid="tests-flaky-list">
      <DataTable<Row>
        flush
        stacked
        ariaLabel="Flaky tests, by flake rate"
        columns={columns}
        data={rows.map((flake) => ({ flake }))}
        getRowKey={({ flake }) => flake.test.id}
        getRowHref={({ flake }) => testHref(flake.test.id)}
        getRowLabel={({ flake }) => `${flake.test.name}: ${FLAKE_STATE[flake.state].label.toLowerCase()}, flake rate ${flakeRateWords(flake)}`}
        getRowProps={({ flake }) => ({ className: "align-top", "data-testid": `tests-flake-row-${flake.test.name}`, "data-state": flake.state })}
        getRowDetail={({ flake }) => (ACTIONABLE.has(flake.state) ? <FlakeActions flake={flake} /> : undefined)}
      />
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

/**
 * What to do about a test: the command that quarantines it (when it has no
 * entry yet), and the agent that fixes it. The agent is R1's (not wired
 * yet): its button is here, disabled, so the slot and its reason are seen.
 */
export function FlakeActions({ flake, testId }: { flake: TestFlake; testId?: string }) {
  const id = testId ?? `tests-flake-actions-${flake.test.name}`;
  return (
    <div className="flex flex-wrap items-center gap-2" data-testid={id}>
      {suggestsQuarantine(flake) && (
        <div className="min-w-0 max-w-full flex-1 sm:max-w-xl" data-testid={`${id}-quarantine`}>
          <CommandLine command={quarantineCommand(flake)} />
        </div>
      )}
      <FixWithAgentSlot />
    </div>
  );
}

/** The agent action's slot: shown, disabled, with its reason, until it is wired. */
export function FixWithAgentSlot() {
  const reason = useId();
  return (
    <span className="inline-flex items-center gap-2">
      <Button
        variant="outline"
        size="sm"
        disabled
        leftIcon={<Bot className="h-3.5 w-3.5" />}
        title="An agent that reproduces, fixes or deletes this test, and opens the PR: coming soon"
        aria-describedby={reason}
        data-testid="tests-fix-with-agent"
      >
        Fix this flaky test
      </Button>
      <span id={reason} className="text-meta text-ink-muted">
        Coming soon
      </span>
    </span>
  );
}

/**
 * Failures the tests did not cause: host failures (a network or download
 * error, a rate limit, a full disk, a port in use, a docker timeout, a test
 * timeout under load) and correlated groups (three or more failures of one
 * run sharing a package, a harness or a signature). Neither ever counts
 * against a test.
 */
function HostHealth({ summary, loading }: { summary?: TestSummary; loading: boolean }) {
  const host = summary?.hostFailures30d;
  return (
    <Section title="Host health" note="Never counted against a test" testId="tests-flaky-host-health" flush>
      <div className="grid divide-y divide-border-subtle border-t border-border-subtle md:grid-cols-2 md:divide-x md:divide-y-0">
        <HealthCell
          icon={<Server className="h-4 w-4" />}
          title="Host failures"
          figure={loading ? "…" : host === undefined ? "—" : host.toLocaleString()}
          testId="tests-flaky-host-failures"
        >
          {host === undefined ? (
            <p>The count of host failures could not be read.</p>
          ) : (
            <p>
              {host === 0 ? "No failure in the last 30 days" : `${host === 1 ? "Failure" : "Failures"} in the last 30 days`} matched a host signature: a network or download error, a rate
              limit, a full disk, a port already in use, a docker timeout, or a test timing out on a loaded host.
            </p>
          )}
          <p>
            This control plane counts them but does not list them by signature or runner yet; <code className="font-mono text-ink">reliant forge test flakes</code> lists a
            checkout&apos;s own.
          </p>
        </HealthCell>
        <HealthCell icon={null} title="Infrastructure groups" figure={null} testId="tests-flaky-infra-groups">
          <p>
            Three or more failures in one run that share a package, a harness (testsrv, ClickHouse, pgtest) or a signature are one infrastructure event, not a flake of each test. The same
            co-failing set on a later commit is the same event.
          </p>
          <p>This control plane does not list them yet. A run&apos;s page marks each such failure, and a test&apos;s page lists the groups it was part of.</p>
        </HealthCell>
      </div>
    </Section>
  );
}

function HealthCell({ icon, title, figure, testId, children }: { icon: ReactNode; title: string; figure: ReactNode; testId: string; children: ReactNode }) {
  return (
    <div className="space-y-1 p-4" data-testid={testId}>
      <p className="flex items-center gap-1.5 text-meta font-medium text-ink-muted">
        {icon}
        {title}
      </p>
      {figure !== null && <p className="text-2xl font-semibold tabular-nums text-ink">{figure}</p>}
      <div className="space-y-1 text-meta text-ink-muted">{children}</div>
    </div>
  );
}
