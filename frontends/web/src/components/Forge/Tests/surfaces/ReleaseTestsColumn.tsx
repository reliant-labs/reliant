// Copyright (c) 2026 Reliant Labs

/**
 * The Releases list's Tests column, beside Checks: each release's test
 * evidence as one compact mark (a pass/fail dot) and its combined coverage.
 *
 * LAZY. Each cell reads its own release (GetReleaseTests, cached per
 * version), and the table renders one page of rows, so only the releases on
 * screen are asked about.
 *
 * FAILURE-TOLERANT. The column exists only once the control plane answers
 * for one release: a control plane without TestsService (Unimplemented),
 * and a reader without tests:read, get no column at all rather than a column
 * of dashes. The probe is the newest release, the same read (and cache
 * entry) as its cell. Any other failure is that cell's dash, with the reason
 * on hover; it never costs the table.
 */

import { useMemo } from "react";

import type { DataTableColumn } from "@/components/forge-ui/data_table";
import Status from "@/components/forge-ui/status";
import { useReleaseTests } from "@/hooks/test-queries";
import { testsErrorKind } from "@/services/controlPlane/tests/errors";

import { releaseTestsMark } from "./surfacesModel";

/** Whether the column shows: the probe answered, or failed for a reason other than "no service" or "no grant". */
export function releaseTestsColumnShown(probe: { data?: unknown; error: unknown }): boolean {
  if (probe.data) return true;
  if (!probe.error) return false;
  const kind = testsErrorKind(probe.error);
  return kind !== "not-configured" && kind !== "denied";
}

/** The Tests column for `project`'s release table, or null while it should not show. `probeVersion`: the newest release. */
export function useReleaseTestsColumn<T extends { version: string }>(project: string, probeVersion: string | undefined): DataTableColumn<T> | null {
  const probe = useReleaseTests(project, probeVersion ?? "");
  const shown = releaseTestsColumnShown(probe);
  return useMemo(
    () =>
      shown
        ? {
            key: "tests",
            header: "Tests",
            render: (_: unknown, row: T) => <ReleaseTestsMark project={project} version={row.version} />,
          }
        : null,
    [shown, project]
  );
}

export function ReleaseTestsMark({ project, version }: { project: string; version: string }) {
  const tests = useReleaseTests(project, version);
  if (tests.isLoading) {
    return <span className="inline-block h-3 w-14 animate-pulse rounded bg-ink/5 motion-reduce:animate-none" aria-label="Loading tests" data-testid={`release-tests-mark-${version}`} />;
  }
  if (tests.error || !tests.data) {
    const kind = testsErrorKind(tests.error);
    return (
      <span className="text-ink-muted" title={kind === "not-found" ? "No test record for this release" : "Its tests could not be read right now"} data-testid={`release-tests-mark-${version}`} data-state="error">
        —
      </span>
    );
  }
  const mark = releaseTestsMark(tests.data);
  if (tests.data.status === "unavailable" || !tests.data.report) {
    return (
      <span className="text-ink-muted" title={mark.title} data-testid={`release-tests-mark-${version}`} data-state="unavailable">
        —<span className="sr-only"> {mark.title}</span>
      </span>
    );
  }
  return (
    <span className="inline-flex items-center gap-2 whitespace-nowrap" title={mark.title} data-testid={`release-tests-mark-${version}`} data-state={tests.data.status} data-tone={mark.tone}>
      <Status tone={mark.tone} label={mark.words} hideLabel />
      <span className="tabular-nums text-ink">{mark.coverage ?? <span className="text-ink-muted">no coverage</span>}</span>
    </span>
  );
}
