// Copyright (c) 2026 Reliant Labs

/**
 * The compact block on top of one issue, one trace or one session in the led
 * console (Console/ledConsole.tsx): "What happened" in a sentence built from
 * the item's own data (leadStory.ts), then its key facts in one short row.
 * The console's page follows unchanged, its less-used panels folded
 * (detailDensity.tsx). Each reads the same query, with the same key, as the
 * page under it, so the block costs no extra request.
 */

import { useIssue, useTrace } from "@/hooks/observability-queries";
import { useSession } from "@/hooks/observability-session-queries";
import type { Issue } from "@/services/controlPlane/observability/issuesTypes";
import type { ObsScope } from "@/services/controlPlane/observability/types";
import { rangeOf } from "@/services/controlPlane/observability/window";

import { affectedNoun } from "../Errors/issueModel";
import { formatAgo, formatCount, formatDuration } from "../format";
import { DetailLead, type Fact } from "./leadParts";
import { issueStory, sessionLength, sessionStory, stepsOf, traceStory } from "./leadStory";

const count = (n: number, one: string, many = `${one}s`) => (n === 1 ? one : many);

export function IssueLead({ issueId, range, explainIssue }: { issueId: string; range?: string; explainIssue?: (issue: Issue) => string | undefined }) {
  const detail = useIssue(issueId, rangeOf(range));
  const issue = detail.data?.issue;
  if (!issue) return null;
  const explained = explainIssue?.(issue);
  const facts: Fact[] = [
    { value: formatCount(issue.events), label: count(issue.events, "event"), tone: issue.events ? "bad" : undefined },
    { value: formatCount(issue.affected), label: `${affectedNoun(issue.platform, issue.affected)} affected` },
    ...(issue.service ? [{ value: issue.service, label: issue.envName ? `in ${issue.envName}` : undefined }] : []),
    ...(issue.lastSeenMs ? [{ value: formatAgo(issue.lastSeenMs), label: "last seen" }] : []),
    ...(issue.assignee ? [{ value: issue.assignee.name, label: "is on it" }] : []),
  ];
  const story = issueStory(issue);
  return <DetailLead story={explained ? [explained, story].filter(Boolean).join(" ") : story} facts={facts} />;
}

export function TraceLead({ scope, traceId, range }: { scope: ObsScope; traceId: string; range?: string }) {
  const trace = useTrace(scope, traceId, rangeOf(range));
  const spans = trace.data?.spans ?? [];
  const root = stepsOf(spans)[0]?.span;
  if (!root) return null;
  const services = new Set(spans.map((span) => span.service)).size;
  const failed = spans.filter((span) => span.status === "error").length;
  return (
    <DetailLead
      story={traceStory(spans)}
      facts={[
        { value: formatDuration(root.durationMs), label: "end to end" },
        { value: formatCount(spans.length), label: count(spans.length, "step") },
        { value: formatCount(services), label: count(services, "service") },
        ...(failed ? [{ value: formatCount(failed), label: count(failed, "failed step"), tone: "bad" as const }] : []),
        { value: formatAgo(root.startMs), label: `in ${root.service}` },
      ]}
    />
  );
}

export function SessionLead({ scope, sessionId }: { scope: ObsScope; sessionId: string }) {
  const session = useSession(scope, sessionId);
  const s = session.data;
  if (!s) return null;
  return (
    <DetailLead
      story={sessionStory(s)}
      facts={[
        { value: s.userId || "Signed-out visitor" },
        { value: formatCount(s.pageViews), label: count(s.pageViews, "page") },
        { value: formatDuration(sessionLength(s)), label: "long" },
        { value: formatCount(s.errors), label: count(s.errors, "error"), tone: s.errors ? "bad" : undefined },
        ...(s.release || s.serviceVersion ? [{ value: s.release || s.serviceVersion, label: "build" }] : []),
        { value: formatAgo(s.startedMs), label: s.envName ? `in ${s.envName}` : undefined },
      ]}
    />
  );
}
