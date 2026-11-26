// Copyright (c) 2026 Reliant Labs

/** The Models pages' shared pieces: gate checks, the alias history timeline, a version link. */

import type { ReactNode } from "react";

import type { MlAliasMove, MlGateCheck } from "@/gen/controlplane/services/ml_early_access/v1/ml_early_access_pb";

import { EmptyNote, fmtAgo, fmtTime, MlLink, mlHref, StatusDot, SubjectLink, tsMs } from "../kit";
import { gateTone, versionOf } from "./modelsModel";

/** A model version, as a link: "v2". */
export function VersionLink({ versionId, mono, children }: { versionId: string; mono?: boolean; children?: ReactNode }) {
  if (!versionId) return <span className="text-ink-muted">—</span>;
  const [model, ...rest] = versionId.split("/");
  return (
    <MlLink href={mlHref("models", [model, rest.join("/")])} mono={mono}>
      {children ?? versionOf(versionId)}
    </MlLink>
  );
}

/** Gate checks: a dot and its word, the detail, and the evidence it read. */
export function GateChecks({ checks, testId }: { checks: readonly MlGateCheck[]; testId?: string }) {
  if (checks.length === 0) return <EmptyNote>No gate declared.</EmptyNote>;
  return (
    <ul className="divide-y divide-border-subtle" data-testid={testId}>
      {checks.map((c) => (
        <li key={c.name} className="flex items-start justify-between gap-3 py-1.5">
          <div className="min-w-0">
            <StatusDot tone={gateTone(c.status)} label={<span className="text-ink">{c.name}</span>} />
            {(c.detail || c.evidence?.id) && (
              <p className="pl-3.5 text-meta text-ink-muted">
                {c.detail}
                {c.evidence?.id && (
                  <>
                    {c.detail ? " · " : ""}
                    <SubjectLink subject={c.evidence} className="text-meta">
                      {c.evidence.label || c.evidence.id}
                    </SubjectLink>
                  </>
                )}
              </p>
            )}
          </div>
          <span className="shrink-0 text-meta text-ink-muted">{c.status}</span>
        </li>
      ))}
    </ul>
  );
}

/** Who moved which alias, when, why, and in which release; newest first. */
export function AliasHistory({ moves, now, testId }: { moves: readonly MlAliasMove[]; now: number; testId?: string }) {
  if (moves.length === 0) return <EmptyNote>No alias has pointed at this version.</EmptyNote>;
  return (
    <ol className="relative space-y-3 border-l border-border-subtle pl-4" data-testid={testId}>
      {moves.map((m, i) => (
        <li key={`${m.alias}-${m.toVersionId}-${i}`} className="relative">
          <span className="absolute -left-5 top-1.5 h-2 w-2 rounded-full border border-border bg-surface" aria-hidden="true" />
          <div className="flex flex-wrap items-baseline gap-x-2 text-ui">
            <span className="font-medium text-ink">{m.alias}</span>
            <span className="text-ink-muted">→</span>
            <VersionLink versionId={m.toVersionId} />
            {m.fromVersionId && (
              <span className="text-meta text-ink-muted">
                from <VersionLink versionId={m.fromVersionId} />
              </span>
            )}
            <span className="text-meta text-ink-muted" title={fmtTime(tsMs(m.at))}>
              {fmtAgo(tsMs(m.at), now)} · {m.by} · release {m.release}
            </span>
          </div>
          <p className="text-meta text-ink-muted">{m.reason}</p>
        </li>
      ))}
    </ol>
  );
}
