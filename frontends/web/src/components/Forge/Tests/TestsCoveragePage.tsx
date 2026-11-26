// Copyright (c) 2026 Reliant Labs

/**
 * /forge/p/:project/tests/coverage — Tests › Coverage: one report's coverage
 * as a tree, package → file, and a file's lines annotated with their hits
 * (forge docs/proposals/tests-coverage-flakes.md §3.2, Amendment 1 part A).
 *
 * The URL is the state: `path` (a package, directory or file, repo-relative;
 * it rides the search), `commit` (whose report: unset is the scoped
 * environment's current release, else the newest report of the default
 * branch), `lane` (one lane's lens; unset combines every lane) and `base`
 * (diff coverage against that commit). A file is a file because its parent's
 * listing says so (the node kind), never by guessing from its name.
 *
 * SOURCE. A file's code comes from the project's linked GitHub repository at
 * view time, by the git blob id the report names, through
 * CoverageSourceContext; nothing stores it. Highlights are drawn only over
 * text verified to be that blob. Without it (no linked repository, a run with
 * uncommitted changes, a blob GitHub no longer has, a revoked connection,
 * this build not fetching yet) the file shows its line numbers and hits with
 * one line saying why, and every figure stays: none of them needs source.
 */

import { Fragment, useMemo, type ReactNode } from "react";
import { useNavigate, useSearch } from "@tanstack/react-router";
import { FileCode2, Folder, GitCommitHorizontal, Package } from "lucide-react";

import Card from "@/components/forge-ui/card";
import DataTable, { type DataTableColumn } from "@/components/forge-ui/data_table";
import Link from "@/components/forge-ui/link";
import SkeletonLoader from "@/components/forge-ui/skeleton_loader";
import { LightweightCodeViewer } from "@/components/shared/LightweightCodeViewer";
import { useCoverage, useFileCoverage, useTestSummary } from "@/hooks/test-queries";
import { unavailableReason, type CoverageSourceUnavailableReason } from "@/services/coverageSource/coverageSource";
import type { CoverageNode, CoverageTarget, CoverageTree, FileCoverage, LineCoverage, TestSummary } from "@/services/controlPlane/tests/types";
import { cn } from "@/lib/utils";

import { useForgeConsole } from "../Console/consoleContext";
import { CONTROL_FIELD } from "../controlStyles";
import { ObsLoadingRows } from "../Observability/ObsStates";
import {
  childName,
  compactCount,
  coverageTarget,
  LINE_TONES,
  lineState,
  noSourceWords,
  parentPath,
  pathCrumbs,
  percentOf,
  sortNodes,
  sourceText,
  viewerLanguage,
  type CoverageSearch,
} from "./coverageModel";
import { useCoverageSource } from "./coverageSourceQuery";
import { RaiseCoverageButton } from "./TestsAgentActions";
import { laneColor, orderLanes, shortSha } from "./testsModel";
import { TestsFrame, useTestsScope, type TestsScope } from "./testsScope";
import { TestsError, TestsNoRunsYet } from "./TestsStates";

const SUBTITLE = "How much of the code each lane's tests reach, by package and file, and which lines.";

/** The URL keys this page owns, without empty values: the bare URL is the default view. */
function compact(search: CoverageSearch): Record<string, string> {
  return Object.fromEntries(Object.entries(search).filter(([, value]) => typeof value === "string" && value !== "")) as Record<string, string>;
}

export function TestsCoveragePage() {
  const { href, projectId } = useForgeConsole();
  const resolved = useTestsScope("coverage");
  const raw = useSearch({ strict: false }) as CoverageSearch;
  const navigate = useNavigate();
  const search: CoverageSearch = { path: raw.path, commit: raw.commit, lane: raw.lane, base: raw.base, environment: raw.environment };
  // A selector rewrites one key and keeps the rest (the scope too); drilling is a link, so Back walks up the tree.
  const select = (patch: Partial<CoverageSearch>) =>
    void navigate({ to: ".", search: (prev: Record<string, unknown>) => ({ ...prev, ...patch }), replace: true } as never);
  return (
    <TestsFrame title="Coverage" subtitle={SUBTITLE} testId="console-tests-coverage">
      {"blocked" in resolved ? (
        resolved.blocked
      ) : (
        <TestsCoverageView
          scope={resolved.scope}
          search={search}
          projectId={projectId}
          hrefFor={(next) => href({ kind: "tests-coverage" }, compact(next))}
          onSelect={select}
        />
      )}
    </TestsFrame>
  );
}

export interface TestsCoverageViewProps {
  scope: TestsScope;
  search: CoverageSearch;
  projectId?: string | null;
  /** The Coverage page's href for another search (a drill, a crumb). */
  hrefFor: (search: CoverageSearch) => string;
  /** Change one selector: `undefined` drops the key. */
  onSelect: (patch: Partial<CoverageSearch>) => void;
}

/** The page below its header, free of the console: what tests and the preview render. */
export function TestsCoverageView({ scope, search, projectId, hrefFor, onSelect }: TestsCoverageViewProps) {
  const summary = useTestSummary(scope.project, { environmentId: scope.environmentId });
  const target = coverageTarget(search.commit, scope.environmentId, summary.data?.latest?.gitCommit ?? null);
  const path = search.path ?? "";

  // Which report: until the summary names the newest, there is nothing to read.
  if (!target) {
    if (summary.isLoading) return <LoadingCard testId="tests-coverage-loading" />;
    if (summary.error || !summary.data) {
      return <TestsError error={summary.error} subject="this project's coverage" onRetry={() => void summary.refetch()} retrying={summary.isFetching} />;
    }
    return <TestsNoRunsYet projectId={projectId} />;
  }
  return <CoverageBody scope={scope} search={search} path={path} target={target} summary={summary.data ?? null} projectId={projectId} hrefFor={hrefFor} onSelect={onSelect} />;
}

interface BodyProps {
  scope: TestsScope;
  search: CoverageSearch;
  path: string;
  target: CoverageTarget;
  summary: TestSummary | null;
  projectId?: string | null;
  hrefFor: (search: CoverageSearch) => string;
  onSelect: (patch: Partial<CoverageSearch>) => void;
}

function CoverageBody({ scope, search, path, target, summary, projectId, hrefFor, onSelect }: BodyProps) {
  const lens = { lane: search.lane, baseSha: search.base };
  // A path is a file when its parent's listing says so. The parent is usually cached: it is where the reader came from.
  const parent = useCoverage(scope.project, path ? { target, path: parentPath(path), ...lens } : null);
  const node = parent.data?.children.find((child) => child.path === path);
  const parentSettled = !path || !parent.isLoading;
  const isFile = node?.kind === "file";
  const dir = useCoverage(scope.project, parentSettled && !isFile ? { target, path, ...lens } : null);
  const file = useFileCoverage(scope.project, isFile ? target : null, path, search.base);
  // Below the root, the whole report's figure too: a subtree's rows must never pass for all of the code.
  const root = useCoverage(scope.project, path ? { target, path: "", ...lens } : null);

  const report = isFile ? file.data : dir.data;
  const lanes = orderLanes([...(report?.lanes ?? parent.data?.lanes ?? summary?.latest?.lanes ?? []), ...(search.lane ? [search.lane] : [])]);
  const commit = report?.gitCommit ?? ("gitCommit" in target ? target.gitCommit : "");

  let body: ReactNode;
  if (!parentSettled || (isFile ? file.isLoading : dir.isLoading)) {
    body = <LoadingCard testId="tests-coverage-loading" />;
  } else if (isFile) {
    body = file.data ? (
      <FileView project={scope.project} file={file.data} lane={search.lane} />
    ) : (
      <TestsError error={file.error} subject={path} onRetry={() => void file.refetch()} retrying={file.isFetching} />
    );
  } else {
    body = dir.data ? (
      <TreeView tree={dir.data} lanes={lanes} lane={search.lane} base={search.base} hrefFor={(next) => hrefFor({ ...search, path: next })} />
    ) : (
      <TestsError error={dir.error} subject={path || "this report"} onRetry={() => void dir.refetch()} retrying={dir.isFetching} />
    );
  }

  return (
    <div className="space-y-4" data-testid="tests-coverage">
      <Selectors search={search} target={target} commit={commit} lanes={lanes} summary={summary} onSelect={onSelect} />
      <PathBar
        path={path}
        isFile={isFile}
        hrefFor={(next) => hrefFor({ ...search, path: next || undefined })}
        raise={(pkg) => {
          // The package's own figure, before anything else is read: a file's package is its parent listing.
          const combined = (isFile ? parent.data : dir.data)?.coverage?.combined;
          const current = combined && combined.statements > 0 && !search.lane ? combined.percent : null;
          return <RaiseCoverageButton project={scope.project} projectId={projectId} pkg={pkg} target={target} currentPercent={current} defaultBranch={summary?.branch || undefined} />;
        }}
      />
      {path && (
        <SubtreeNotice
          path={path}
          isFile={isFile}
          statements={(isFile ? node?.combined : dir.data?.coverage?.combined)?.statements ?? null}
          rootStatements={root.data?.coverage?.combined?.statements ?? null}
          rootHref={hrefFor({ ...search, path: undefined })}
        />
      )}
      {body}
    </div>
  );
}

/**
 * Below the root, one line saying so, with the share of the report it is and
 * the way back to all of it: a deep link (an agent's, a bookmark) lands here
 * with no drill to remember, and a subtree's rows read like a whole report.
 */
function SubtreeNotice({ path, isFile, statements, rootStatements, rootHref }: { path: string; isFile: boolean; statements: number | null; rootStatements: number | null; rootHref: string }) {
  const share = statements !== null && rootStatements ? ` · ${statements.toLocaleString("en-US")} of ${rootStatements.toLocaleString("en-US")} statements` : "";
  return (
    <div className="flex flex-wrap items-center justify-between gap-2 rounded-md border border-primary/40 bg-primary/5 px-3 py-2 text-ui" role="status" data-testid="tests-coverage-subtree">
      <span className="text-ink">
        {isFile ? "Showing only the file " : "Showing only "}
        <code className="font-mono font-medium">{isFile ? path : `${path}/`}</code>
        <span className="text-ink-muted">{share}</span>
      </span>
      <Link href={rootHref} className="font-medium text-primary-ink underline-offset-2 hover:underline">
        View all code
      </Link>
    </div>
  );
}

function LoadingCard({ testId }: { testId: string }) {
  return (
    <Card padding="none">
      <ObsLoadingRows testId={testId} />
    </Card>
  );
}

// ── Selectors ───────────────────────────────────────────────────────────────

const selectClass = cn(CONTROL_FIELD, "h-8 max-w-[16rem] px-2 text-ui");

function Selector({ label, value, onChange, testId, children }: { label: string; value: string; onChange: (value: string) => void; testId: string; children: ReactNode }) {
  return (
    <label className="flex items-center gap-2 text-meta font-medium text-ink-muted">
      {label}
      <select className={selectClass} value={value} onChange={(event) => onChange(event.target.value)} data-testid={testId}>
        {children}
      </select>
    </label>
  );
}

const day = (ms: number | null) => (ms === null ? "" : ` · ${new Date(ms).toISOString().slice(0, 10)}`);

function Selectors({ search, target, commit, lanes, summary, onSelect }: { search: CoverageSearch; target: CoverageTarget; commit: string; lanes: string[]; summary: TestSummary | null; onSelect: (patch: Partial<CoverageSearch>) => void }) {
  // The branch's recent commits with coverage, newest first: what a reader compares.
  const recent = useMemo(() => [...(summary?.trend ?? [])].reverse(), [summary]);
  const defaultLabel =
    "environmentId" in target ? `Current release of ${search.environment ?? "the chosen environment"}` : `Newest report on the default branch${commit ? ` · ${shortSha(commit)}` : ""}`;
  const commitListed = !search.commit || recent.some((point) => point.gitCommit === search.commit);
  const baseListed = !search.base || recent.some((point) => point.gitCommit === search.base);
  return (
    <div className="flex flex-wrap items-center gap-x-4 gap-y-2" data-testid="tests-coverage-selectors">
      <Selector label="Commit" value={search.commit ?? ""} onChange={(value) => onSelect({ commit: value || undefined })} testId="tests-coverage-commit">
        <option value="">{defaultLabel}</option>
        {!commitListed && <option value={search.commit}>{shortSha(search.commit!)}</option>}
        {recent.map((point) => (
          <option key={point.gitCommit} value={point.gitCommit}>
            {shortSha(point.gitCommit)}
            {day(point.atMs)} · {point.combinedPercent.toFixed(1)}%
          </option>
        ))}
      </Selector>
      <Selector label="Lane" value={search.lane ?? ""} onChange={(value) => onSelect({ lane: value || undefined })} testId="tests-coverage-lane">
        <option value="">All lanes, combined</option>
        {lanes.map((lane) => (
          <option key={lane} value={lane}>
            {lane} only
          </option>
        ))}
      </Selector>
      <Selector label="Diff against" value={search.base ?? ""} onChange={(value) => onSelect({ base: value || undefined })} testId="tests-coverage-base">
        <option value="">No base</option>
        {!baseListed && <option value={search.base}>{shortSha(search.base!)}</option>}
        {recent
          .filter((point) => point.gitCommit !== commit)
          .map((point) => (
            <option key={point.gitCommit} value={point.gitCommit}>
              {shortSha(point.gitCommit)}
              {day(point.atMs)}
            </option>
          ))}
      </Selector>
    </div>
  );
}

// ── Where we are ────────────────────────────────────────────────────────────

function PathBar({ path, isFile, hrefFor, raise }: { path: string; isFile: boolean; hrefFor: (path: string) => string; raise: (pkg: string) => ReactNode }) {
  const crumbs = pathCrumbs(path);
  // The package a coverage-raising agent would work in: the path itself, or a file's own package.
  const pkg = isFile ? parentPath(path) : path;
  return (
    <div className="flex flex-wrap items-center justify-between gap-2">
      <nav aria-label="Coverage path" data-testid="tests-coverage-crumbs">
        <ol className="flex flex-wrap items-center gap-1 text-ui">
          {crumbs.map((crumb, i) => {
            const last = i === crumbs.length - 1;
            return (
              <li key={crumb.path || "root"} className="flex items-center gap-1">
                {i > 0 && <span className="text-ink-muted" aria-hidden="true">/</span>}
                {last ? (
                  <span className="font-mono font-medium text-ink" aria-current="page">
                    {crumb.label}
                  </span>
                ) : (
                  <Link href={hrefFor(crumb.path)} className="font-mono text-ink-muted underline-offset-2 hover:text-ink hover:underline">
                    {crumb.label}
                  </Link>
                )}
              </li>
            );
          })}
        </ol>
      </nav>
      {pkg && (
        <div className="flex items-center gap-2" data-testid="tests-coverage-raise">
          {raise(pkg)}
        </div>
      )}
    </div>
  );
}

// ── The tree ────────────────────────────────────────────────────────────────

/** A percentage with its bar, in the lane's own hue (a data series, not a status). */
function Meter({ percent, color, label }: { percent: number | null; color: string; label: string }) {
  if (percent === null) return <span className="text-ink-muted">—</span>;
  return (
    <span className="inline-flex items-center justify-end gap-2" title={label}>
      <span className="hidden h-1.5 w-12 overflow-hidden rounded-full bg-surface-sunken sm:inline-block" aria-hidden="true">
        <span className="block h-full rounded-full" style={{ width: `${Math.max(0, Math.min(100, percent))}%`, backgroundColor: `hsl(var(${color}))` }} />
      </span>
      <span className="tabular-nums">{percent.toFixed(1)}%</span>
    </span>
  );
}

const KIND_ICON: Record<CoverageNode["kind"], ReactNode> = {
  package: <Package className="h-3.5 w-3.5 shrink-0 text-ink-muted" aria-hidden="true" />,
  directory: <Folder className="h-3.5 w-3.5 shrink-0 text-ink-muted" aria-hidden="true" />,
  file: <FileCode2 className="h-3.5 w-3.5 shrink-0 text-ink-muted" aria-hidden="true" />,
  unknown: <Folder className="h-3.5 w-3.5 shrink-0 text-ink-muted" aria-hidden="true" />,
};

type Row = { node: CoverageNode } & Record<string, unknown>;

function TreeView({ tree, lanes, lane, base, hrefFor }: { tree: CoverageTree; lanes: string[]; lane?: string; base?: string; hrefFor: (path: string) => string }) {
  const rows = useMemo<Row[]>(() => sortNodes(tree.children).map((node) => ({ node })), [tree]);
  const columns: DataTableColumn<Row>[] = [
    {
      key: "name",
      header: "Name",
      render: (_, { node }) => (
        <span className="inline-flex min-w-0 items-center gap-2">
          {KIND_ICON[node.kind]}
          <span className="truncate font-mono text-code text-ink">{childName(node.path, tree.path)}</span>
          {node.changed && (
            <span className="text-meta text-ink-muted" title="Holds a line changed against the base">
              changed
            </span>
          )}
        </span>
      ),
    },
    { key: "statements", header: "Statements", numeric: true, render: (_, { node }) => (node.combined ? compactCount(node.combined.statements) : "—") },
    {
      key: "combined",
      header: lane ? `${lane} only` : "Combined",
      numeric: true,
      render: (_, { node }) => (
        <Meter percent={node.combined && node.combined.statements > 0 ? node.combined.percent : null} color={lane ? laneColor(lane) : "--chart-1"} label={lane ? `Covered by ${lane}` : "Covered by any lane"} />
      ),
    },
    // One bar per lane beside combined; a lane's lens shows that lane alone.
    ...(lane
      ? []
      : lanes.map(
          (name): DataTableColumn<Row> => ({
            key: `lane:${name}`,
            header: name,
            numeric: true,
            render: (_, { node }) => {
              const figure = node.lanes[name];
              return <Meter percent={figure && figure.statements > 0 ? figure.percent : null} color={laneColor(name)} label={`Covered by ${name}`} />;
            },
          })
        )),
    ...(base
      ? [
          {
            key: "diff",
            header: "Diff",
            numeric: true,
            render: (_: unknown, { node }: Row) =>
              node.diff && node.diff.lines > 0 ? (
                <span className="tabular-nums" title={`${node.diff.covered} of ${node.diff.lines} changed lines covered`}>
                  {node.diff.percent.toFixed(1)}% <span className="text-ink-muted">({node.diff.covered}/{node.diff.lines})</span>
                </span>
              ) : (
                <span className="text-ink-muted">—</span>
              ),
          } satisfies DataTableColumn<Row>,
        ]
      : []),
  ];

  return (
    <div className="space-y-3">
      <Totals coverage={tree.coverage} lanes={lanes} lane={lane} commit={tree.gitCommit} testId="tests-coverage-totals" />
      <DataTable<Row>
        columns={columns}
        data={rows}
        getRowKey={({ node }) => node.path}
        getRowHref={({ node }) => hrefFor(node.path)}
        getRowProps={({ node }) => ({ "data-testid": "tests-coverage-row", "data-path": node.path, "data-kind": node.kind })}
        emptyMessage="Nothing under this path has coverage in this report."
        ariaLabel={`Coverage under ${tree.path || "the repository root"}`}
        paginated={false}
        stacked
      />
    </div>
  );
}

/** A path's (or file's) own figures, in one line: "75.0% combined · unit 75.0 · e2e 41.7 · diff 2/3 lines · 12 statements". */
function Totals({ coverage, lanes, lane, commit, testId }: { coverage: CoverageTree["coverage"]; lanes: string[]; lane?: string; commit: string; testId: string }) {
  const combined = coverage?.combined ?? null;
  const parts: ReactNode[] = [];
  parts.push(
    <span key="combined" className="font-medium text-ink">
      {percentOf(combined)} {lane ? `${lane} only` : "combined"}
    </span>
  );
  if (!lane) {
    for (const name of lanes) {
      const figure = coverage?.lanes[name];
      if (figure) parts.push(<span key={name}>{`${name} ${percentOf(figure).replace("%", "")}`}</span>);
    }
  }
  if (coverage?.diff) parts.push(<span key="diff">{`diff ${coverage.diff.covered}/${coverage.diff.lines} lines`}</span>);
  if (combined) parts.push(<span key="statements">{`${combined.statements.toLocaleString("en-US")} statements`}</span>);
  if (coverage && coverage.excludedGeneratedStatements > 0) parts.push(<span key="generated">{`${coverage.excludedGeneratedStatements.toLocaleString("en-US")} generated, not counted`}</span>);
  if (commit) {
    parts.unshift(
      <span key="commit" className="inline-flex items-center gap-1">
        <GitCommitHorizontal className="h-3.5 w-3.5" aria-hidden="true" />
        <code className="font-mono text-ink">{shortSha(commit)}</code>
      </span>
    );
  }
  return (
    <p className="text-meta text-ink-muted" data-testid={testId}>
      {parts.map((part, i) => (
        <Fragment key={i}>
          {i > 0 && " · "}
          {part}
        </Fragment>
      ))}
    </p>
  );
}

// ── A file ──────────────────────────────────────────────────────────────────

function FileView({ project, file, lane }: { project: string; file: FileCoverage; lane?: string }) {
  const { source, isLoading } = useCoverageSource(project, file);
  const lanes = orderLanes(lane ? [lane] : file.lanes);
  const byLine = useMemo(() => new Map(file.lines.map((line) => [line.line, line])), [file]);
  const reason = source ? unavailableReason(source) : null;

  return (
    <div className="space-y-3" data-testid="tests-coverage-file" data-source={isLoading ? "loading" : (reason ?? "github")}>
      <Totals coverage={file.coverage} lanes={orderLanes(file.lanes)} commit={file.gitCommit} testId="tests-coverage-file-totals" />
      <Legend lanes={lanes} />
      {isLoading || !source ? (
        <Card padding="none" aria-label="Loading source" aria-busy="true">
          <SkeletonLoader variant="table-row" count={6} />
        </Card>
      ) : "unavailable" in source ? (
        <NoSource reason={source.unavailable} file={file} lanes={lanes} />
      ) : (
        <AnnotatedSource source={source} file={file} byLine={byLine} lanes={lanes} lane={lane} />
      )}
    </div>
  );
}

function Legend({ lanes }: { lanes: string[] }) {
  return (
    <p className="flex flex-wrap items-center gap-x-3 gap-y-1 text-meta text-ink-muted" data-testid="tests-coverage-legend">
      <span>
        Hits per lane: <span className="font-mono text-ink">{lanes.join(" · ")}</span>
      </span>
      <span>
        <span className="font-mono text-ink">▸</span> changed against the base
      </span>
    </p>
  );
}

/** One gutter cell per lane, right-aligned: the hits, blank on a line with no statement, 0 where the lane did not reach it. */
function HitCells({ line, lanes }: { line: LineCoverage | undefined; lanes: string[] }) {
  return (
    <>
      {lanes.map((lane) => (
        <span key={lane} className="w-10 shrink-0 pr-1 text-right tabular-nums text-ink-muted" data-lane={lane}>
          {line && line.statements > 0 ? compactCount(line.hits[lane] ?? 0) : ""}
        </span>
      ))}
      <span className="w-4 shrink-0 text-center text-ink" aria-label={line?.changed ? "changed" : undefined}>
        {line?.changed ? "▸" : ""}
      </span>
    </>
  );
}

function AnnotatedSource({ source, file, byLine, lanes, lane }: { source: { text: string }; file: FileCoverage; byLine: Map<number, LineCoverage>; lanes: string[]; lane?: string }) {
  return (
    <Card padding="none" className="overflow-hidden" data-testid="tests-coverage-source">
      <LightweightCodeViewer
        content={sourceText(source.text)}
        language={viewerLanguage(file.lang, file.path)}
        filename={file.path}
        maxHeight={720}
        noBorder
        lineTone={(n) => {
          const line = byLine.get(n);
          const state = line ? lineState(line, lane) : null;
          return state ? LINE_TONES[state] : undefined;
        }}
        gutter={(n) => <HitCells line={byLine.get(n)} lanes={lanes} />}
      />
    </Card>
  );
}

/**
 * No source: the line numbers and hits alone, and why. No tone anywhere: a
 * highlight is drawn over code, and there is no code here to be sure of.
 */
function NoSource({ reason, file, lanes }: { reason: CoverageSourceUnavailableReason; file: FileCoverage; lanes: string[] }) {
  return (
    <Card padding="none" data-testid="tests-coverage-no-source" data-reason={reason}>
      <p className="border-b border-border-subtle px-4 py-2.5 text-meta text-ink-muted" data-testid="tests-coverage-no-source-why">
        {noSourceWords(reason)}
      </p>
      <div className="overflow-x-auto">
        <table className="w-full text-code" aria-label={`Hits per line of ${file.path}`} data-testid="tests-coverage-hits">
          <thead>
            <tr className="text-left text-meta text-ink-muted">
              <th scope="col" className="px-4 py-1.5 text-right font-medium">
                Line
              </th>
              {lanes.map((lane) => (
                <th key={lane} scope="col" className="px-2 py-1.5 text-right font-medium">
                  {lane}
                </th>
              ))}
              <th scope="col" className="px-2 py-1.5 text-right font-medium">
                Statements
              </th>
              <th scope="col" className="px-4 py-1.5 font-medium">
                <span className="sr-only">Changed</span>
              </th>
            </tr>
          </thead>
          <tbody className="font-mono">
            {file.lines.map((line) => (
              <tr key={line.line} className="border-t border-border-subtle" data-line={line.line} data-testid="tests-coverage-hits-row">
                <td className="px-4 py-1 text-right tabular-nums text-ink-muted">{line.line}</td>
                {lanes.map((lane) => (
                  <td key={lane} className="px-2 py-1 text-right tabular-nums text-ink" data-lane={lane}>
                    {compactCount(line.hits[lane] ?? 0)}
                  </td>
                ))}
                <td className="px-2 py-1 text-right tabular-nums text-ink-muted">{line.statements}</td>
                <td className="px-4 py-1 text-ink">{line.changed ? <span aria-label="changed">▸</span> : null}</td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
    </Card>
  );
}

