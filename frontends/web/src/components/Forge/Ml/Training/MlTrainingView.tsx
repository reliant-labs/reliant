// Copyright (c) 2026 Reliant Labs

/**
 * The ML "Training" view (path segment `runs`): parses its own drill-down.
 *
 *   []                          the list (experiments, runs)
 *   ["compare"]                 run comparison (?compare=a,b,c)
 *   [run]                       run detail (run id or name)
 *   [run, "phases", phase]      the run scoped to a phase (?attempt=n)
 *   [run, "traces", traceId]    a training trace in the run's frame
 */

import { MlFrame, MlRead } from "../MlParts";
import { CompareTray, MlTraceView, mlHref, useMl, useMlSearch } from "../kit";
import { RunCompare } from "./compare/RunCompare";
import { RunDetail } from "./RunDetail";
import { RunsList } from "./RunsList";

export function MlTrainingView({ sub }: { sub: readonly string[] }) {
  return (
    <>
      <TrainingPage sub={sub} />
      {/* The tray follows the reader across Training's pages, but not onto the comparison it opens. */}
      {sub[0] !== "compare" && <CompareTray />}
    </>
  );
}

function TrainingPage({ sub }: { sub: readonly string[] }) {
  const [search] = useMlSearch();
  const [first, second, third] = sub;
  if (!first) return <RunsList />;
  if (first === "compare") {
    const ids = (search.compare ?? "").split(",").filter(Boolean);
    return (
      <MlFrame view="runs" title={`Compare ${ids.length} run${ids.length === 1 ? "" : "s"}`} crumbs={[{ label: "Compare" }]}>
        <RunCompare ids={ids} />
      </MlFrame>
    );
  }
  if (second === "traces" && third) return <RunTrace runKey={first} traceId={third} />;
  return <RunDetail runKey={first} phaseKey={second === "phases" ? third : undefined} />;
}

function RunTrace({ runKey, traceId }: { runKey: string; traceId: string }) {
  const read = useMl("getMlRun", { run: runKey });
  return (
    <MlRead query={read} what="this run">
      {(detail) => {
        const run = detail.run!;
        const base = run.name || run.id;
        return (
          <MlFrame
            view="runs"
            title={`Trace ${traceId.slice(0, 12)}…`}
            crumbs={[
              { label: run.name, href: mlHref("runs", [base]) },
              { label: "Traces", href: mlHref("runs", [base], { tab: "traces" }) },
              { label: traceId.slice(0, 12) },
            ]}
            testId="ml-page-run-trace"
          >
            <MlTraceView traceId={traceId} testId="ml-run-trace" />
          </MlFrame>
        );
      }}
    </MlRead>
  );
}
