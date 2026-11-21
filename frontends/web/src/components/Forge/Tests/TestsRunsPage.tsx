// Copyright (c) 2026 Reliant Labs

/**
 * /forge/p/:project/tests/runs — Tests › Runs: every recorded run of the
 * project's test lanes, newest first (control-plane
 * TestsService.ListTestRuns; forge docs/proposals/tests-coverage-flakes.md
 * §3.2).
 *
 * One row per run (one lane on one tree): its verdict, lane, where it ran
 * (CI, a local checkout, against an environment, or a flake confirm), the
 * commit and branch, its counts, the tests that passed only on a retry, how
 * long it took, and the CI run behind it. A row opens the run.
 *
 * The filters are the URL's (runsModel.ts RunsSearch: branch, commit,
 * release, lane, source, status), each one ListTestRuns' optional field. The
 * environment is the console's one scope (`?env=`), shown in the bar beside
 * them: CI and local runs carry no environment, so an empty list under one
 * says so and offers every environment.
 *
 * Honest about its data: a run still being ingested is "Processing", never
 * passed; a count it did not record is a dash.
 */

import { useCallback, useMemo } from "react";
import { useNavigate, useSearch } from "@tanstack/react-router";

import Button from "@/components/forge-ui/button";
import Card from "@/components/forge-ui/card";
import DataTable, { type DataTableColumn } from "@/components/forge-ui/data_table";
import { FilterBar, FilterMenu } from "@/components/forge-ui/filter_bar";
import Status from "@/components/forge-ui/status";
import Tag from "@/components/forge-ui/tag";
import { useTestRuns } from "@/hooks/test-queries";
import type { TestRun } from "@/services/controlPlane/tests/types";

import { useForgeConsole } from "../Console/consoleContext";
import { ENV_ALL } from "../Console/envScope";
import { formatAgo, formatDuration, formatWhen } from "../Observability/format";
import { ObsLoadingRows, StateFrame } from "../Observability/ObsStates";
import {
  activeRunsFilterCount,
  pickRunsSearch,
  RUN_SOURCE_LABEL,
  RUN_SOURCES,
  RUN_STATUS_LABEL,
  RUN_STATUSES,
  runAtMs,
  runDurationMs,
  runsFiltersOf,
  runSourceLabel,
  totalsWords,
  type RunsSearch,
} from "./runsModel";
import { orderLanes, runVerdict, shortSha } from "./testsModel";
import { OutLink, TextFilter } from "./TestsParts";
import { TestsFrame, useTestsScope, type TestsScope } from "./testsScope";
import { TestsError, TestsNoRunsYet } from "./TestsStates";

const SUBTITLE = "Every recorded run of the project's test lanes, newest first.";

export function TestsRunsPage() {
  const { href, projectId, env, setEnv, roster } = useForgeConsole();
  const resolved = useTestsScope("test runs");
  const raw = useSearch({ strict: false }) as Record<string, unknown>;
  const search = useMemo(() => pickRunsSearch(raw), [raw]);
  const navigate = useNavigate();
  const onSearch = useCallback(
    (next: RunsSearch) =>
      void navigate({
        to: ".",
        // Only the scope survives from the old search: every filter is the new view's.
        search: (prev: Record<string, unknown>) => ({ env: prev.env as string | undefined, ...next }),
        replace: true,
      }),
    [navigate]
  );
  const envFilter: EnvFilter = {
    value: env === ENV_ALL ? undefined : env,
    choices: roster.envs.map((candidate) => ({ name: candidate.name, id: candidate.live?.id })),
    onChange: (name) => setEnv(name ?? ENV_ALL),
  };
  return (
    <TestsFrame title="Runs" subtitle={SUBTITLE} testId="console-tests-runs">
      {"blocked" in resolved ? (
        resolved.blocked
      ) : (
        <TestsRunsView
          scope={resolved.scope}
          search={search}
          onSearch={onSearch}
          envFilter={envFilter}
          projectId={projectId}
          runHref={(runId) => href({ kind: "tests-run", run: runId })}
        />
      )}
    </TestsFrame>
  );
}

/** The console's environment scope, as the bar shows it: the scoped name (none for All) and the project's environments. */
export interface EnvFilter {
  value?: string;
  choices: { name: string; id?: string }[];
  onChange: (name: string | undefined) => void;
}

export interface TestsRunsViewProps {
  scope: TestsScope;
  search: RunsSearch;
  onSearch: (next: RunsSearch) => void;
  envFilter?: EnvFilter;
  projectId?: string | null;
  runHref: (runId: string) => string;
  /** "Now", for relative times (tests pin it). */
  now?: number;
}

/** The page below its header, free of the console: what tests and the preview render. */
export function TestsRunsView({ scope, search, onSearch, envFilter, projectId, runHref, now }: TestsRunsViewProps) {
  const list = useTestRuns(scope.project, runsFiltersOf(scope.environmentId, search));
  const rows = useMemo(() => list.data?.pages.flatMap((page) => page.runs) ?? [], [list.data]);
  const filtered = activeRunsFilterCount(search) > 0;
  const envScoped = !!scope.environmentId;

  if (list.isLoading) {
    return (
      <Card padding="none">
        <ObsLoadingRows testId="tests-runs-loading" />
      </Card>
    );
  }
  if (list.error && !list.data) {
    return <TestsError error={list.error} subject="this project's test runs" onRetry={() => void list.refetch()} retrying={list.isFetching} />;
  }
  if (rows.length === 0 && !filtered && !envScoped) return <TestsNoRunsYet projectId={projectId} />;

  const envName = new Map((envFilter?.choices ?? []).filter((choice) => choice.id).map((choice) => [choice.id!, choice.name]));
  return (
    <div className="space-y-3" data-testid="tests-runs">
      <RunsFilters search={search} onSearch={onSearch} envFilter={envFilter} lanes={rows.map((r) => r.lane)} />
      {rows.length === 0 ? (
        filtered ? (
          <StateFrame
            testId="tests-runs-no-match"
            icon={null}
            title="No runs match these filters"
            actions={
              <Button variant="outline" size="sm" onClick={() => onSearch({})} data-testid="tests-runs-no-match-clear">
                Clear filters
              </Button>
            }
          >
            <p>{envScoped ? `Only runs recorded against ${envFilter?.value ?? "this environment"} are searched.` : "Other runs exist in this project."}</p>
          </StateFrame>
        ) : (
          <StateFrame
            testId="tests-runs-none-in-env"
            icon={null}
            title={`No runs recorded against ${envFilter?.value ?? "this environment"}`}
            actions={
              envFilter && (
                <Button variant="outline" size="sm" onClick={() => envFilter.onChange(undefined)} data-testid="tests-runs-all-envs">
                  Show every environment
                </Button>
              )
            }
          >
            <p>CI and local runs carry no environment. Only a lane run against a deployed environment (a preview&apos;s e2e, say) is recorded against one.</p>
          </StateFrame>
        )
      ) : (
        <RunsTable
          rows={rows}
          envName={envName}
          runHref={runHref}
          now={now ?? Date.now()}
          hasMore={!!list.hasNextPage}
          loadingMore={list.isFetchingNextPage}
          onLoadMore={() => void list.fetchNextPage()}
          moreError={list.isFetchNextPageError ? list.error : null}
        />
      )}
    </div>
  );
}

const SOURCE_OPTIONS = RUN_SOURCES.map((value) => ({ value, label: RUN_SOURCE_LABEL[value] }));
const STATUS_OPTIONS = RUN_STATUSES.map((value) => ({ value, label: RUN_STATUS_LABEL[value] }));

function RunsFilters({ search, onSearch, envFilter, lanes }: { search: RunsSearch; onSearch: (next: RunsSearch) => void; envFilter?: EnvFilter; lanes: string[] }) {
  const set = (patch: Partial<RunsSearch>) => onSearch({ ...search, ...patch });
  // The scaffolded lanes, any other lane a run on screen has, and the one the URL names.
  const laneOptions = orderLanes(["unit", "integration", "e2e", ...lanes, ...(search.lane ? [search.lane] : [])]).map((lane) => ({ value: lane, label: lane }));
  return (
    <FilterBar label="Filter runs" active={activeRunsFilterCount(search)} onClear={() => onSearch({})} testId="tests-runs-filters">
      <TextFilter label="Branch" value={search.branch} placeholder="main" onChange={(branch) => set({ branch })} testId="tests-runs-filter-branch" mono />
      <TextFilter label="Commit" value={search.commit} placeholder="sha or prefix" onChange={(commit) => set({ commit })} testId="tests-runs-filter-commit" mono />
      <TextFilter label="Release" value={search.release} placeholder="version" onChange={(release) => set({ release })} testId="tests-runs-filter-release" mono />
      {envFilter && (
        <FilterMenu
          label="Environment"
          value={envFilter.value}
          options={envFilter.choices.map((choice) => ({ value: choice.name, label: choice.name }))}
          onChange={envFilter.onChange}
          testId="tests-runs-filter-env"
        />
      )}
      <FilterMenu label="Lane" value={search.lane} options={laneOptions} onChange={(lane) => set({ lane })} testId="tests-runs-filter-lane" />
      <FilterMenu label="Source" value={search.source} options={SOURCE_OPTIONS} onChange={(source) => set({ source: source as RunsSearch["source"] })} testId="tests-runs-filter-source" />
      <FilterMenu label="Status" value={search.status} options={STATUS_OPTIONS} onChange={(status) => set({ status: status as RunsSearch["status"] })} testId="tests-runs-filter-status" />
    </FilterBar>
  );
}

type Row = { run: TestRun };

function RunsTable({
  rows,
  envName,
  runHref,
  now,
  hasMore,
  loadingMore,
  onLoadMore,
  moreError,
}: {
  rows: TestRun[];
  envName: Map<string, string>;
  runHref: (runId: string) => string;
  now: number;
  hasMore: boolean;
  loadingMore: boolean;
  onLoadMore: () => void;
  moreError: unknown;
}) {
  const columns: DataTableColumn<Row>[] = [
    {
      key: "status",
      header: "Status",
      render: (_, { run }) => {
        const verdict = runVerdict(run);
        return (
          <span title={run.ingestState === "failed" ? run.ingestError || undefined : undefined}>
            <Status tone={verdict.tone} label={verdict.words} />
          </span>
        );
      },
    },
    { key: "lane", header: "Lane", render: (_, { run }) => <span className="font-mono text-code text-ink">{run.lane || "—"}</span> },
    {
      key: "source",
      header: "Source",
      render: (_, { run }) => (
        <span className="block">
          <span className="block text-ink">{runSourceLabel(run)}</span>
          {run.source === "env" && run.environmentId && <span className="block text-meta text-ink-muted">{envName.get(run.environmentId) ?? run.environmentId}</span>}
          {run.source === "local" && run.uploadedBy && <span className="block text-meta text-ink-muted">{run.uploadedBy}</span>}
        </span>
      ),
    },
    {
      key: "commit",
      header: "Commit",
      className: "w-full max-w-0",
      render: (_, { run }) => (
        <span className="block min-w-0">
          <span className="flex items-center gap-1.5">
            <code className="font-mono text-code text-ink" title={run.gitCommit}>
              {shortSha(run.gitCommit) || "—"}
            </code>
            {run.dirty && <Tag title="Recorded from a checkout with uncommitted changes: it never counts toward its commit">uncommitted</Tag>}
          </span>
          <span className="block truncate text-meta text-ink-muted" title={run.branch}>
            {run.branch || "no branch"}
          </span>
        </span>
      ),
    },
    {
      key: "tests",
      header: "Tests",
      render: (_, { run }) =>
        run.ingestState === "ingested" ? <span className="whitespace-nowrap text-ink">{totalsWords(run.totals)}</span> : <span className="text-ink-muted">—</span>,
    },
    {
      key: "retry",
      header: "Retry passes",
      numeric: true,
      render: (_, { run }) =>
        run.ingestState !== "ingested" ? (
          <span className="text-ink-muted">—</span>
        ) : (
          <span className={run.totals.retryPassed > 0 ? "font-medium text-warning-ink" : "text-ink-muted"} title="Tests that failed, then passed on a retry within the run: a flake signal">
            {run.totals.retryPassed}
          </span>
        ),
    },
    {
      key: "duration",
      header: "Duration",
      numeric: true,
      render: (_, { run }) => {
        const took = runDurationMs(run);
        const at = runAtMs(run);
        return (
          <span className="block whitespace-nowrap">
            <span className="block text-ink">{took === null ? "—" : formatDuration(took)}</span>
            {at !== null && (
              <span className="block text-meta text-ink-muted" title={formatWhen(at, now)}>
                {formatAgo(at, now)}
              </span>
            )}
          </span>
        );
      },
    },
    {
      key: "ci",
      header: "CI run",
      interactive: true,
      render: (_, { run }) =>
        run.ci?.runUrl ? (
          <OutLink href={run.ci.runUrl} testId={`tests-run-ci-${run.runId}`}>
            {run.ci.runId ? `#${run.ci.runId}` : "CI run"}
          </OutLink>
        ) : (
          <span className="text-ink-muted">—</span>
        ),
    },
  ];
  return (
    <Card padding="none" className="overflow-hidden" data-testid="tests-runs-list">
      <DataTable<Row>
        flush
        stacked
        ariaLabel="Recorded test runs"
        columns={columns}
        data={rows.map((run) => ({ run }))}
        getRowKey={({ run }) => run.runId}
        getRowHref={({ run }) => runHref(run.runId)}
        getRowLabel={({ run }) => `${run.lane} run of ${run.branch || "no branch"} ${shortSha(run.gitCommit)}: ${runVerdict(run).words}`}
        getRowProps={({ run }) => ({ className: "align-top", "data-testid": `tests-run-row-${run.runId}`, "data-outcome": run.outcome })}
      />
      {(hasMore || !!moreError) && (
        <div className="flex flex-wrap items-center gap-3 border-t border-border-subtle px-4 py-2">
          <Button variant="ghost" size="sm" onClick={onLoadMore} loading={loadingMore} data-testid="tests-runs-more">
            {moreError ? "Retry loading more runs" : "Load more runs"}
          </Button>
          {!!moreError && <span className="text-meta text-danger-ink">The next page didn&apos;t load.</span>}
        </div>
      )}
    </Card>
  );
}
