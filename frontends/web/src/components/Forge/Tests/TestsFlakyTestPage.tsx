// Copyright (c) 2026 Reliant Labs

/**
 * /forge/p/:project/tests/flaky/:test — one test's record, `:test` its id as
 * base64url (Console/paths.ts encodeTestIdSegment) (control-plane
 * TestsService.GetTest; forge docs/proposals/tests-coverage-flakes.md §3.2,
 * the flaky page's drawer, as a page of its own so it can be linked).
 *
 * Top to bottom: the test and its flake record (state, the flake rate with
 * its n, its confirmed events, its quarantine and issue, and what to do: the
 * quarantine command and the agent's slot); a strip of its outcomes over its
 * last runs, oldest to newest, each a link to the run; its flake events, each
 * with the signal that caught it and its verdict (host and correlated events
 * are shown, and said never to count); and its failures grouped by
 * signature, each with a sample output.
 *
 * Any test a run names opens here, flaky or not: one that never flaked is
 * "Healthy", with its outcomes.
 */

import type { ReactNode } from "react";
import { Code, ConnectError } from "@connectrpc/connect";
import { useParams } from "@tanstack/react-router";

import Card from "@/components/forge-ui/card";
import Link from "@/components/forge-ui/link";
import Status from "@/components/forge-ui/status";
import Tag from "@/components/forge-ui/tag";
import { useTest } from "@/hooks/test-queries";
import type { TestDetail, TestFailureSignature, TestFlake, TestFlakeEvent } from "@/services/controlPlane/tests/types";
import { cn } from "@/lib/utils";

import { useForgeConsole } from "../Console/consoleContext";
import { decodeTestIdSegment } from "../Console/paths";
import { CommandLine } from "../GettingStarted";
import { formatAgo, formatDuration, formatWhen } from "../Observability/format";
import { ObsLoadingRows } from "../Observability/ObsStates";
import { FLAKE_RATE_MEANS, FLAKE_STATE, flakeRateWords, issueLabel, outcomeStrip, quarantineCommand, SIGNAL_LABEL, STRIP_LABEL, suggestsQuarantine, type StripKind } from "./flakyModel";
import { failureVerdictWords } from "./runsModel";
import { shortSha } from "./testsModel";
import { FixWithAgentSlot } from "./TestsFlakyPage";
import { FailureOutput, OutLink, Section, shortPackage } from "./TestsParts";
import { TestsFrame } from "./testsScope";
import { TestsError } from "./TestsStates";

export function TestsFlakyTestPage() {
  const { forgeProject, href } = useForgeConsole();
  const { test } = useParams({ strict: false }) as { test?: string };
  // The segment is the id as base64url (or, in a link from before, the id itself).
  const testId = test ? decodeTestIdSegment(test) : "";
  return (
    <TestsFrame title="Test" subtitle={testId ? <code className="break-all font-mono">{testId}</code> : undefined} testId="console-tests-flaky-test">
      <TestsFlakyTestView project={forgeProject} testId={testId} runHref={(runId) => href({ kind: "tests-run", run: runId })} />
    </TestsFrame>
  );
}

export interface TestsFlakyTestViewProps {
  project: string;
  testId: string;
  runHref: (runId: string) => string;
  now?: number;
}

/** The page below its header, free of the console: what tests and the preview render. */
export function TestsFlakyTestView({ project, testId, runHref, now }: TestsFlakyTestViewProps) {
  const read = useTest(project, testId);
  if (read.isLoading) {
    return (
      <Card padding="none">
        <ObsLoadingRows testId="tests-test-loading" />
      </Card>
    );
  }
  if (read.error || !read.data?.flake) {
    return (
      <TestsError
        error={read.error ?? new ConnectError("the answer named no test", Code.NotFound)}
        subject={testId ? `Test ${testId}` : "This test"}
        onRetry={() => void read.refetch()}
        retrying={read.isFetching}
        testId="tests-test-error"
      />
    );
  }
  const at = now ?? Date.now();
  return (
    <div className="space-y-4" data-testid="tests-test">
      <Record flake={read.data.flake} now={at} />
      <OutcomeStrip detail={read.data} runHref={runHref} now={at} />
      <Events events={read.data.events} runHref={runHref} now={at} />
      <Signatures signatures={read.data.signatures} now={at} />
    </div>
  );
}

function Fact({ label, children, testId }: { label: string; children: ReactNode; testId?: string }) {
  return (
    <div className="min-w-0" data-testid={testId}>
      <dt className="text-meta font-medium text-ink-muted">{label}</dt>
      <dd className="mt-0.5 min-w-0 text-ink">{children}</dd>
    </div>
  );
}

const ago = (ms: number | null, now: number) => (ms === null ? <span className="text-ink-muted">—</span> : <span title={formatWhen(ms, now)}>{formatAgo(ms, now)}</span>);

function Record({ flake, now }: { flake: TestFlake; now: number }) {
  const state = FLAKE_STATE[flake.state];
  const test = flake.test;
  const place = test.file ? (test.line > 0 ? `${test.file}:${test.line}` : test.file) : "";
  const actionable = flake.state === "suspected" || flake.state === "flaky" || flake.state === "quarantined";
  const issue = flake.issueUrl || flake.quarantine?.issueUrl || "";
  return (
    <Card padding="none" data-testid="tests-test-record">
      <div className="space-y-1 px-4 pt-4">
        <h2 className="break-all font-mono text-section text-ink">{test.name || test.id}</h2>
        <p className="break-all text-meta text-ink-muted">
          <span title={test.package}>{shortPackage(test.package)}</span>
          {place && <span className="font-mono"> · {place}</span>}
        </p>
      </div>
      <dl className="grid grid-cols-2 gap-x-6 gap-y-3 p-4 sm:grid-cols-3 lg:grid-cols-6">
        <Fact label="State" testId="tests-test-state">
          <span title={state.means}>
            <Status tone={state.tone} label={state.label} />
          </span>
        </Fact>
        <Fact label="Flake rate" testId="tests-test-rate">
          <span title={FLAKE_RATE_MEANS}>{flakeRateWords(flake)}</span>
        </Fact>
        <Fact label="Events, 30 days">
          <span className="tabular-nums">{flake.confirmedEvents30d}</span>
          {flake.signals.length > 0 && <span className="block text-meta text-ink-muted">{flake.signals.map((signal) => SIGNAL_LABEL[signal]).join(" · ")}</span>}
        </Fact>
        <Fact label="First seen">{ago(flake.firstSeenMs, now)}</Fact>
        <Fact label="Last event">{ago(flake.lastEventMs, now)}</Fact>
        <Fact label="Issue">{issue ? <OutLink href={issue}>{issueLabel(issue)}</OutLink> : <span className="text-ink-muted">none</span>}</Fact>
      </dl>
      <div className="space-y-3 border-t border-border-subtle p-4" data-testid="tests-test-next">
        <p className="text-ui text-ink">{state.means}</p>
        {flake.quarantine && (
          <div className="space-y-1 text-meta text-ink-muted" data-testid="tests-test-quarantine">
            <p className="flex flex-wrap items-center gap-2">
              <span className="font-medium text-ink">Quarantine entry</span>
              <code className="break-all font-mono text-ink">{flake.quarantine.path}</code>
              {flake.quarantine.overdue && <Tag tone="danger">overdue</Tag>}
            </p>
            {flake.quarantine.reason && <p>Reason: {flake.quarantine.reason}</p>}
            <p>
              {flake.quarantine.expiresMs === null
                ? "Never expires."
                : flake.quarantine.overdue
                  ? `Expired ${formatWhen(flake.quarantine.expiresMs, now)}. It is still applied: delete or rewrite the test, and remove the entry in the same change.`
                  : `Expires ${formatWhen(flake.quarantine.expiresMs, now)}.`}
              {flake.quarantinedMs !== null && ` Quarantined ${formatAgo(flake.quarantinedMs, now)}.`}
            </p>
          </div>
        )}
        {suggestsQuarantine(flake) && (
          <div className="max-w-2xl space-y-1" data-testid="tests-test-quarantine-command">
            <p className="text-meta text-ink-muted">Quarantine it from your checkout; the project&apos;s lanes skip it once the entry is on the default branch:</p>
            <CommandLine command={quarantineCommand(flake)} />
          </div>
        )}
        {actionable && <FixWithAgentSlot />}
      </div>
    </Card>
  );
}

const STRIP_CELL: Record<StripKind, string> = {
  pass: "bg-success",
  retry: "bg-warning",
  fail: "bg-danger",
  skip: "border border-border-strong bg-transparent",
  unknown: "bg-ink-muted/30",
};

const STRIP_ORDER: StripKind[] = ["pass", "retry", "fail", "skip", "unknown"];

function OutcomeStrip({ detail, runHref, now }: { detail: TestDetail; runHref: (runId: string) => string; now: number }) {
  const { cells, counts } = outcomeStrip(detail.outcomes);
  const summary = STRIP_ORDER.filter((kind) => counts[kind] > 0)
    .map((kind) => `${counts[kind]} ${STRIP_LABEL[kind]}`)
    .join(" · ");
  return (
    <Section title={cells.length > 0 ? `Last ${cells.length} ${cells.length === 1 ? "run" : "runs"}` : "Recent runs"} note={summary || undefined} testId="tests-test-strip">
      {cells.length === 0 ? (
        <p className="text-ui text-ink-muted">No run has recorded this test yet.</p>
      ) : (
        <>
          <ol className="flex flex-wrap gap-1" aria-label="Outcomes, oldest first">
            {cells.map((cell) => {
              const words = `${STRIP_LABEL[cell.kind]}: ${cell.lane} on ${cell.branch || "no branch"} ${shortSha(cell.gitCommit)}${cell.atMs !== null ? `, ${formatAgo(cell.atMs, now)}` : ""}`;
              return (
                <li key={cell.runId} data-kind={cell.kind}>
                  <Link
                    href={runHref(cell.runId)}
                    aria-label={words}
                    title={`${words}${cell.durationMs > 0 ? ` (${formatDuration(cell.durationMs)})` : ""}`}
                    className={cn("block h-6 w-3 rounded-full focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring", STRIP_CELL[cell.kind])}
                  />
                </li>
              );
            })}
          </ol>
          <ul className="mt-3 flex flex-wrap gap-x-4 gap-y-1 text-meta text-ink-muted" aria-label="Legend">
            {STRIP_ORDER.filter((kind) => kind !== "unknown").map((kind) => (
              <li key={kind} className="inline-flex items-center gap-1.5">
                <span aria-hidden="true" className={cn("inline-block h-3 w-1.5 rounded-full", STRIP_CELL[kind])} />
                {STRIP_LABEL[kind]}
              </li>
            ))}
            <li>Oldest left, newest right</li>
          </ul>
        </>
      )}
    </Section>
  );
}

function VerdictTag({ verdict }: { verdict: TestFlakeEvent["verdict"] }) {
  const words = failureVerdictWords(verdict);
  if (!words) return <span className="text-ink-muted">unclassified</span>;
  return (
    <Tag tone={verdict === "real" || verdict === "raced" ? "danger" : "neutral"} title={words.title}>
      {words.words}
    </Tag>
  );
}

function Events({ events, runHref, now }: { events: TestFlakeEvent[]; runHref: (runId: string) => string; now: number }) {
  const uncounted = events.filter((e) => e.verdict === "host" || e.verdict === "correlated").length;
  return (
    <Section
      title="Flake events"
      note={events.length === 0 ? undefined : uncounted > 0 ? `${uncounted} host or infrastructure, never counted` : "Newest first"}
      testId="tests-test-events"
      flush
    >
      {events.length === 0 ? (
        <p className="px-4 pb-4 text-ui text-ink-muted">No flake event in the last 30 days.</p>
      ) : (
        <ul className="divide-y divide-border-subtle border-t border-border-subtle">
          {events.map((e, index) => (
            <li key={`${e.gitCommit}|${index}`} className="flex flex-wrap items-center gap-x-4 gap-y-1 px-4 py-2.5 text-ui" data-testid={`tests-test-event-${index}`} data-verdict={e.verdict}>
              <span className="w-20 shrink-0 text-meta text-ink-muted">{ago(e.atMs, now)}</span>
              <VerdictTag verdict={e.verdict} />
              <span className="text-ink-muted">{SIGNAL_LABEL[e.signal]}</span>
              <code className="font-mono text-code text-ink" title={e.gitCommit}>
                {shortSha(e.gitCommit)}
              </code>
              {e.infraGroup && (
                <span className="text-meta text-ink-muted">
                  group <code className="font-mono text-ink">{e.infraGroup}</code>
                </span>
              )}
              <span className="ml-auto flex flex-wrap gap-2 text-meta">
                {e.runIds.map((runId, i) => (
                  <Link key={runId} href={runHref(runId)} className="text-ink underline-offset-2 hover:underline" title={runId}>
                    {e.runIds.length === 1 ? "run" : `run ${i + 1}`}
                  </Link>
                ))}
              </span>
            </li>
          ))}
        </ul>
      )}
    </Section>
  );
}

function Signatures({ signatures, now }: { signatures: TestFailureSignature[]; now: number }) {
  return (
    <Section title="Failure signatures" note={signatures.length > 0 ? "Failures grouped by their normalised output, most frequent first" : undefined} testId="tests-test-signatures" flush>
      {signatures.length === 0 ? (
        <p className="px-4 pb-4 text-ui text-ink-muted">No failure recorded in its recent runs.</p>
      ) : (
        <ul className="divide-y divide-border-subtle border-t border-border-subtle">
          {signatures.map((s, index) => (
            <li key={s.signature} className="space-y-2 px-4 py-3" data-testid={`tests-test-signature-${index}`} data-verdict={s.verdict}>
              <div className="flex flex-wrap items-center gap-x-3 gap-y-1 text-ui">
                <span className="font-medium tabular-nums text-ink">
                  {s.count} {s.count === 1 ? "failure" : "failures"}
                </span>
                <VerdictTag verdict={s.verdict} />
                <code className="font-mono text-meta text-ink-muted" title={s.signature}>
                  {s.signature.slice(0, 19)}
                </code>
                <span className="text-meta text-ink-muted">
                  {s.firstSeenMs !== null && s.lastSeenMs !== null && s.firstSeenMs !== s.lastSeenMs ? (
                    <>
                      first {formatAgo(s.firstSeenMs, now)}, last {formatAgo(s.lastSeenMs, now)}
                    </>
                  ) : s.lastSeenMs !== null ? (
                    <>seen {formatAgo(s.lastSeenMs, now)}</>
                  ) : null}
                </span>
              </div>
              <FailureOutput output={s.sampleOutput} open={index === 0} label="Sample output" testId={`tests-test-signature-${index}-output`} />
            </li>
          ))}
        </ul>
      )}
    </Section>
  );
}
