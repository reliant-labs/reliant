// Copyright (c) 2026 Reliant Labs

/**
 * /forge/p/:project/tests/flaky/:test — one test's record, `:test` its id as
 * base64url (Console/paths.ts encodeTestIdSegment) (control-plane
 * TestsService.GetTest; forge docs/proposals/tests-coverage-flakes.md §3.2,
 * the flaky page's drawer, as a page of its own so it can be linked).
 *
 * Top to bottom: the test's short name, its package, file:line and state,
 * with "Ask an agent to fix" and a `⋯` (copy the quarantine command, open the
 * issue, copy the test id); one row of figures (flake rate, runs, first seen,
 * last flaked); its quarantine, when it has one; its last runs as a strip,
 * oldest to newest, each tick a link to its run; its flake events as a plain
 * timeline (host and infrastructure events, which never count, folded behind
 * "Show N not counted"); and its failures grouped by signature, collapsed,
 * each with its first line of output.
 *
 * Any test a run names opens here, flaky or not: one that never flaked is
 * "Healthy", with its runs.
 */

import { useState, type ReactNode } from "react";
import { Code, ConnectError } from "@connectrpc/connect";
import { useParams } from "@tanstack/react-router";
import { Check, ChevronRight, Copy, MoreHorizontal } from "lucide-react";

import Card from "@/components/forge-ui/card";
import DropdownMenu, { type MenuItem } from "@/components/forge-ui/dropdown_menu";
import Link from "@/components/forge-ui/link";
import Status from "@/components/forge-ui/status";
import Tag from "@/components/forge-ui/tag";
import Tooltip from "@/components/forge-ui/tooltip";
import { useTest } from "@/hooks/test-queries";
import { toast } from "@/lib/toast-manager";
import type { TestDetail, TestFailureSignature, TestFlake, TestFlakeEvent } from "@/services/controlPlane/tests/types";
import { cn } from "@/lib/utils";

import { useForgeConsole } from "../Console/consoleContext";
import { decodeTestIdSegment } from "../Console/paths";
import { formatWhen } from "../Observability/format";
import { ObsLoadingRows } from "../Observability/ObsStates";
import { agoWords, eventWords, FLAKE_RATE_MEANS, VERDICT_TAG, FLAKE_STATE, formatFlakeRate, issueLabel, isUncounted, outcomeStrip, quarantineCommand, suggestsQuarantine, testIdParts } from "./flakyModel";
import { shortSha } from "./testsModel";
import { FixFlakyTestButton } from "./TestsAgentActions";
import { FailureOutput, safeOutUrl, Section, shortPackage } from "./TestsParts";
import { RunStrip, stripSummary } from "./TestsRunStrip";
import { TestsError } from "./TestsStates";

export function TestsFlakyTestPage() {
  const { forgeProject, href, projectId } = useForgeConsole();
  const { test } = useParams({ strict: false }) as { test?: string };
  // The segment is the id as base64url (or, in a link from before, the id itself).
  const testId = test ? decodeTestIdSegment(test) : "";
  return (
    <div className="min-w-0" data-testid="console-tests-flaky-test">
      <TestsFlakyTestView project={forgeProject} projectId={projectId} testId={testId} runHref={(runId) => href({ kind: "tests-run", run: runId })} />
    </div>
  );
}

export interface TestsFlakyTestViewProps {
  project: string;
  /** The Reliant project holding this app's repository: where "Ask an agent to fix" starts its agent. */
  projectId?: string | null;
  testId: string;
  runHref: (runId: string) => string;
  now?: number;
}

/** The page, header included, free of the console: what tests and the preview render. */
export function TestsFlakyTestView({ project, projectId, testId, runHref, now }: TestsFlakyTestViewProps) {
  const read = useTest(project, testId);
  const parts = testIdParts(testId);
  if (read.isLoading) {
    return (
      <div className="space-y-5">
        <TitleBlock name={parts.name} pkg={parts.package} testId={testId} />
        <Card padding="none">
          <ObsLoadingRows testId="tests-test-loading" />
        </Card>
      </div>
    );
  }
  if (read.error || !read.data?.flake) {
    return (
      <div className="space-y-5">
        <TitleBlock name={parts.name} pkg={parts.package} testId={testId} />
        <TestsError
          error={read.error ?? new ConnectError("the answer named no test", Code.NotFound)}
          subject={testId ? `Test ${parts.name}` : "This test"}
          onRetry={() => void read.refetch()}
          retrying={read.isFetching}
          testId="tests-test-error"
        />
      </div>
    );
  }
  const at = now ?? Date.now();
  const flake = read.data.flake;
  return (
    <div className="space-y-6" data-testid="tests-test">
      <Header flake={flake} project={project} projectId={projectId} />
      <Figures flake={flake} runs={read.data.outcomes.length} now={at} />
      {flake.quarantine && <Quarantine flake={flake} now={at} />}
      <Runs detail={read.data} runHref={runHref} now={at} />
      <Events events={read.data.events} runHref={runHref} now={at} />
      <Signatures signatures={read.data.signatures} now={at} />
    </div>
  );
}

/** The title alone: the short name, the package and the id to copy. What shows while the record loads. */
function TitleBlock({ name, pkg, testId, meta, actions }: { name: string; pkg: string; testId: string; meta?: ReactNode; actions?: ReactNode }) {
  return (
    <div className="flex flex-wrap items-start justify-between gap-x-6 gap-y-3" data-testid="tests-test-header">
      <div className="min-w-0 space-y-1">
        <div className="flex min-w-0 items-center gap-2">
          <h1 className="truncate text-title text-ink" title={name}>
            {name || testId}
          </h1>
          {testId && <CopyId id={testId} />}
        </div>
        <p className="flex flex-wrap items-center gap-x-2 gap-y-1 text-meta text-ink-muted">
          {pkg && <span title={pkg}>{shortPackage(pkg).replace(/^…\//, "")}</span>}
          {meta}
        </p>
      </div>
      {actions && <div className="flex shrink-0 items-center gap-2">{actions}</div>}
    </div>
  );
}

/** The full test id, behind a copy button: too long for a title, needed for `forge test` commands. */
function CopyId({ id }: { id: string }) {
  const [copied, setCopied] = useState(false);
  return (
    <Tooltip content={copied ? "Copied" : <span className="break-all font-mono">{id}</span>} tooltipClassName="max-w-md">
      <button
        type="button"
        onClick={() =>
          void navigator.clipboard.writeText(id).then(
            () => {
              setCopied(true);
              setTimeout(() => setCopied(false), 1500);
            },
            () => toast.error("Couldn't copy: the browser denied the clipboard.")
          )
        }
        aria-label={`Copy test ID ${id}`}
        className="inline-flex h-6 w-6 shrink-0 items-center justify-center rounded-md text-ink-muted hover:bg-ink/5 hover:text-ink focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
        data-testid="tests-test-copy-id"
      >
        {copied ? <Check className="h-3.5 w-3.5" aria-hidden="true" /> : <Copy className="h-3.5 w-3.5" aria-hidden="true" />}
      </button>
    </Tooltip>
  );
}

const ACTIONABLE = new Set<TestFlake["state"]>(["suspected", "flaky", "quarantined"]);

function Header({ flake, project, projectId }: { flake: TestFlake; project: string; projectId?: string | null }) {
  const state = FLAKE_STATE[flake.state];
  const { test } = flake;
  const place = test.file ? (test.line > 0 ? `${test.file}:${test.line}` : test.file) : "";
  const issue = safeOutUrl(flake.issueUrl || flake.quarantine?.issueUrl || "");
  const items: MenuItem[] = [];
  if (suggestsQuarantine(flake)) {
    items.push({ label: "Copy quarantine command", onClick: () => void copy(quarantineCommand(flake), "Quarantine command copied"), testId: "tests-test-menu-quarantine" });
  }
  if (issue) items.push({ label: `Open issue ${issueLabel(issue.href)}`, onClick: () => window.open(issue.href, "_blank", "noopener,noreferrer"), testId: "tests-test-menu-issue" });
  items.push({ label: "Copy test ID", onClick: () => void copy(test.id, "Test ID copied"), testId: "tests-test-menu-copy-id" });
  return (
    <TitleBlock
      name={test.name}
      pkg={test.package}
      testId={test.id}
      meta={
        <>
          {place && (
            <>
              <span aria-hidden="true">·</span>
              <code className="font-mono" data-testid="tests-test-place">
                {place}
              </code>
            </>
          )}
          <span aria-hidden="true">·</span>
          <span title={state.means} data-testid="tests-test-state">
            <Status tone={state.tone} label={state.label} size="sm" />
          </span>
          {flake.quarantine?.overdue && <Tag tone="warning">Quarantine overdue</Tag>}
        </>
      }
      actions={
        <>
          {ACTIONABLE.has(flake.state) && <FixFlakyTestButton project={project} projectId={projectId} flake={flake} variant="primary" />}
          <DropdownMenu
            trigger={<MoreHorizontal className="h-4 w-4" aria-hidden="true" />}
            triggerLabel={`More actions for ${test.name}`}
            triggerClassName="inline-flex h-8 w-8 items-center justify-center rounded-md border border-border text-ink-muted hover:bg-ink/5 hover:text-ink focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
            groups={[{ items }]}
            menuClassName="min-w-[13rem]"
          />
        </>
      }
    />
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

function Figure({ label, children, title, testId }: { label: string; children: ReactNode; title?: string; testId?: string }) {
  return (
    <div className="min-w-0 px-4 py-3" title={title} data-testid={testId}>
      <dt className="text-meta text-ink-muted">{label}</dt>
      <dd className="mt-0.5 truncate text-section tabular-nums text-ink">{children}</dd>
    </div>
  );
}

/** Flake rate, runs, first seen, last flaked: one row. */
function Figures({ flake, runs, now }: { flake: TestFlake; runs: number; now: number }) {
  const when = (ms: number | null) => (ms === null ? <span className="text-ink-muted">—</span> : <span title={formatWhen(ms, now)}>{agoWords(ms, now)}</span>);
  return (
    <Card padding="none">
      <dl className="grid grid-cols-2 divide-border-subtle sm:grid-cols-4 sm:divide-x">
        <Figure label="Flake rate" title={FLAKE_RATE_MEANS} testId="tests-test-rate">
          {flake.scoreRuns > 0 ? formatFlakeRate(flake.score) : "—"}
          <span className="ml-1.5 text-meta font-normal text-ink-muted">of {flake.scoreRuns.toLocaleString()} runs</span>
        </Figure>
        <Figure label="Flakes, 30 days" testId="tests-test-events-count">
          {flake.confirmedEvents30d}
          {runs > 0 && <span className="ml-1.5 text-meta font-normal text-ink-muted">in {runs} recent runs</span>}
        </Figure>
        <Figure label="First seen">{when(flake.firstSeenMs)}</Figure>
        <Figure label="Last flaked">{when(flake.lastEventMs)}</Figure>
      </dl>
    </Card>
  );
}

function Quarantine({ flake, now }: { flake: TestFlake; now: number }) {
  const q = flake.quarantine!;
  return (
    <div className="flex flex-wrap items-baseline gap-x-2 gap-y-1 text-ui text-ink-muted" data-testid="tests-test-quarantine">
      <span className="font-medium text-ink">Quarantined</span>
      {flake.quarantinedMs !== null && <span>{agoWords(flake.quarantinedMs, now)}</span>}
      {q.reason && <span>· {q.reason}</span>}
      <span>
        ·{" "}
        {q.expiresMs === null
          ? "never expires"
          : q.overdue
            ? `expired ${formatWhen(q.expiresMs, now)} and is still applied: fix or delete the test, and remove the entry in the same change`
            : `expires ${formatWhen(q.expiresMs, now)}`}
      </span>
      <code className="break-all font-mono text-meta" title="The quarantine entry">
        {q.path}
      </code>
    </div>
  );
}

function Runs({ detail, runHref, now }: { detail: TestDetail; runHref: (runId: string) => string; now: number }) {
  const { cells, counts } = outcomeStrip(detail.outcomes);
  return (
    <section className="space-y-2.5" data-testid="tests-test-strip">
      <div className="flex flex-wrap items-baseline justify-between gap-x-3 gap-y-1">
        <h2 className="text-section text-ink">{cells.length > 0 ? `Last ${cells.length} ${cells.length === 1 ? "run" : "runs"}` : "Recent runs"}</h2>
        {cells.length > 0 && <p className="text-meta text-ink-muted">{stripSummary(counts)} · oldest left</p>}
      </div>
      {cells.length === 0 ? <p className="text-ui text-ink-muted">No run has recorded this test yet.</p> : <RunStrip outcomes={detail.outcomes} runHref={runHref} now={now} />}
    </section>
  );
}

function Events({ events, runHref, now }: { events: TestFlakeEvent[]; runHref: (runId: string) => string; now: number }) {
  const [showAll, setShowAll] = useState(false);
  const uncounted = events.filter(isUncounted).length;
  const shown = showAll ? events : events.filter((e) => !isUncounted(e));
  return (
    <Section
      title="Flake events"
      note={
        uncounted > 0 ? (
          <button type="button" onClick={() => setShowAll(!showAll)} className="text-meta text-ink-muted underline-offset-2 hover:text-ink hover:underline" data-testid="tests-test-events-toggle">
            {showAll ? `Hide ${uncounted} not counted` : `Show ${uncounted} not counted`}
          </button>
        ) : undefined
      }
      testId="tests-test-events"
      flush
    >
      {events.length === 0 ? (
        <p className="px-4 pb-4 text-ui text-ink-muted">No flake event in the last 30 days.</p>
      ) : (
        <ol className="border-t border-border-subtle px-4 py-2">
          {shown.map((e, index) => {
            const { words, counted } = eventWords(e);
            return (
              <li key={`${e.gitCommit}|${index}`} className="relative flex items-baseline gap-3 py-1.5 pl-5 text-ui" data-testid={`tests-test-event-${events.indexOf(e)}`} data-verdict={e.verdict}>
                {/* The timeline: a dot per event on one hairline. */}
                <span aria-hidden="true" className={cn("absolute left-0 top-[0.6rem] h-2 w-2 rounded-full", counted ? "bg-warning" : "border border-ink-muted/50 bg-transparent")} />
                {index < shown.length - 1 && <span aria-hidden="true" className="absolute bottom-[-0.6rem] left-[3.5px] top-[1.25rem] w-px bg-border-subtle" />}
                <span className={cn("min-w-0 flex-1", counted ? "text-ink" : "text-ink-muted")}>
                  {words}
                  <span className="text-ink-muted">
                    {" "}
                    on{" "}
                    <code className="font-mono text-code" title={e.gitCommit}>
                      {shortSha(e.gitCommit)}
                    </code>
                  </span>
                  {e.infraGroup && <span className="text-meta text-ink-muted"> · {e.infraGroup}</span>}
                </span>
                <span className="shrink-0 whitespace-nowrap text-meta text-ink-muted">{e.atMs === null ? "—" : <span title={formatWhen(e.atMs, now)}>{agoWords(e.atMs, now)}</span>}</span>
                <span className="flex shrink-0 gap-2 text-meta">
                  {e.runIds.map((runId, i) => (
                    <Link key={runId} href={runHref(runId)} className="text-ink-muted underline-offset-2 hover:text-ink hover:underline" title={runId}>
                      {e.runIds.length === 1 ? "View run" : `Run ${i + 1}`}
                    </Link>
                  ))}
                </span>
              </li>
            );
          })}
        </ol>
      )}
    </Section>
  );
}

/** The first line of output worth reading: the assertion, not the `=== RUN` banner. */
function headline(output: string): string {
  const lines = output.split("\n").map((line) => line.trim());
  return lines.find((line) => line && !line.startsWith("=== RUN") && !line.startsWith("--- FAIL")) ?? lines.find(Boolean) ?? "";
}

function Signatures({ signatures, now }: { signatures: TestFailureSignature[]; now: number }) {
  return (
    <Section title="Failure signatures" note={signatures.length > 0 ? "Grouped by output, most frequent first" : undefined} testId="tests-test-signatures" flush>
      {signatures.length === 0 ? (
        <p className="px-4 pb-4 text-ui text-ink-muted">No failure recorded in its recent runs.</p>
      ) : (
        <ul className="divide-y divide-border-subtle border-t border-border-subtle">
          {signatures.map((s, index) => {
            const counted = s.verdict === "confirmed";
            return (
              <li key={s.signature} data-testid={`tests-test-signature-${index}`} data-verdict={s.verdict}>
                <details className="group">
                  <summary className="flex cursor-pointer list-none items-center gap-3 px-4 py-2.5 text-ui hover:bg-ink/5 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-inset focus-visible:ring-ring [&::-webkit-details-marker]:hidden">
                    <ChevronRight className="h-3.5 w-3.5 shrink-0 text-ink-muted transition-transform group-open:rotate-90" aria-hidden="true" />
                    <span className="w-20 shrink-0 tabular-nums text-ink">
                      {s.count} {s.count === 1 ? "failure" : "failures"}
                    </span>
                    <code className={cn("min-w-0 flex-1 truncate font-mono text-code", counted ? "text-ink" : "text-ink-muted")} title={headline(s.sampleOutput)}>
                      {headline(s.sampleOutput) || s.signature}
                    </code>
                    {!counted && <Tag>{VERDICT_TAG[s.verdict]}</Tag>}
                    <span className="hidden shrink-0 text-meta text-ink-muted sm:inline">{s.lastSeenMs !== null ? agoWords(s.lastSeenMs, now) : ""}</span>
                  </summary>
                  <div className="px-4 pb-3 pl-11">
                    <FailureOutput output={s.sampleOutput} open label="Sample output" testId={`tests-test-signature-${index}-output`} />
                  </div>
                </details>
              </li>
            );
          })}
        </ul>
      )}
    </Section>
  );
}
