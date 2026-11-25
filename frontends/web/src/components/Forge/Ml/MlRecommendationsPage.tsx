// Copyright (c) 2026 Reliant Labs

/**
 * ML › Recommendations (MlEarlyAccessService.ListMlFindings): the open
 * findings, worst first — each with its severity, what it is about, the
 * evidence, the fix (a KCL change, or the exact command for a setting outside
 * forge's declarations), and, for a slow start, where the pod's start time
 * goes. "Propose fix" and "Dismiss" come with the real service (slice 1).
 */

import { useMemo } from "react";

import Status from "@/components/forge-ui/status";
import { useMlFindings } from "@/hooks/ml-queries";
import type { MlFinding } from "@/services/controlPlane/ml/types";

import { formatAgo } from "../Observability/format";
import { findingsWorstFirst, SEVERITY } from "./mlModel";
import { CodeBlock, MlFrame, MlRead, Section, StageBreakdown, useMlGate } from "./MlParts";

export function MlRecommendationsPage({ now }: { now?: number }) {
  const { orgId } = useMlGate();
  const query = useMlFindings(orgId);
  return (
    <MlFrame view="recommendations">
      <MlRead query={query} what="recommendations">
        {(findings) => <FindingsView findings={findings} now={now ?? Date.now()} />}
      </MlRead>
    </MlFrame>
  );
}

function FindingsView({ findings, now }: { findings: MlFinding[]; now: number }) {
  const ordered = useMemo(() => findingsWorstFirst(findings), [findings]);
  if (ordered.length === 0) {
    return <p className="text-ui text-ink-muted" data-testid="ml-findings-none">Nothing to fix: no open findings in the connected clusters.</p>;
  }
  return (
    <div className="space-y-4" data-testid="ml-findings">
      {ordered.map((finding) => (
        <FindingCard key={finding.id} finding={finding} now={now} />
      ))}
    </div>
  );
}

function FindingCard({ finding, now }: { finding: MlFinding; now: number }) {
  const severity = SEVERITY[finding.severity];
  return (
    <Section
      title={
        <span className="flex items-center gap-2">
          <Status tone={severity.tone} label={severity.label} hideLabel />
          {finding.title}
        </span>
      }
      note={[finding.subject, finding.clusterId, finding.lastSeenAtMs !== null ? `seen ${formatAgo(finding.lastSeenAtMs, now)}` : ""].filter(Boolean).join(" · ")}
      testId={`ml-finding-${finding.id}`}
    >
      <div className="space-y-3" data-severity={finding.severity}>
        {finding.summary && <p className="text-ui text-ink">{finding.summary}</p>}
        {finding.evidence.length > 0 && (
          <dl className="grid grid-cols-[minmax(8rem,max-content)_1fr] gap-x-4 gap-y-1 text-ui" aria-label="Evidence">
            {finding.evidence.map((item, index) => (
              <div key={index} className="contents">
                <dt className="text-ink-muted">{item.label}</dt>
                <dd className="min-w-0 break-words font-mono text-code text-ink">{item.value}</dd>
              </div>
            ))}
          </dl>
        )}
        {finding.podStart && finding.podStart.stages.length > 0 && <StageBreakdown breakdown={finding.podStart} testId={`ml-finding-podstart-${finding.id}`} />}
        {finding.fixKcl && <CodeBlock code={finding.fixKcl} label="The fix, in KCL" testId={`ml-finding-kcl-${finding.id}`} />}
        {finding.fixCommand && <CodeBlock code={finding.fixCommand} label="The fix, as a command" testId={`ml-finding-command-${finding.id}`} />}
      </div>
    </Section>
  );
}
