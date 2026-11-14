// Copyright (c) 2026 Reliant Labs

/**
 * Settings › Organization › AI usage: the AI usage the active org PAYS FOR
 * (control-plane docs/design/org-provider-accounts.md §5, decision D8), for
 * the members who hold billing:read in it.
 *
 *   Totals        calls, tokens and the estimated list cost of the org's own
 *                 API keys over the range
 *   Breakdown     by member, by account or by model
 *
 * Members' own accounts are never here, even in the org's chats: the server
 * reads only the org's rollup of org-funded calls, and the page says so. Every
 * cost is an estimate at list price, labelled as one, and "—" (never "$0.00")
 * where nothing could be priced.
 *
 * Keyed by org: a switch remounts the page, so nothing of one org is shown in
 * the next.
 */

import { useMemo, useState } from "react";

import DataTable, { type DataTableColumn } from "@/components/forge-ui/data_table";
import Button from "@/components/forge-ui/button";
import Tabs from "@/components/forge-ui/tabs";
import { OrgLLMCostEstimateStatus, type GetOrgLLMUsageResponse, type OrgLLMUsageBucket } from "@/gen/reliant/v1/org_usage_pb";
import { useOrgLLMUsage } from "@/hooks/org-usage-queries";
import { useActiveOrg } from "@/hooks/useOrgs";
import { memberNamesBySubject } from "@/services/controlPlane/members";
import type { MyOrg } from "@/services/controlPlane/orgs";

import { SettingsPageHeader } from "../../SettingsPageHeader";
import { useMembers } from "../members/useMembers";
import {
  bucketName,
  formatEstimate,
  formatTokens,
  isPermissionError,
  rangeDates,
  usageErrorMessage,
  USAGE_GROUPS,
  USAGE_RANGES,
  type UsageGroupId,
  type UsageRangeId,
} from "./orgUsageModel";

const TITLE = "AI usage";

export function OrgAIUsagePage() {
  const { active, isLoading } = useActiveOrg();
  if (!active) {
    return (
      <div className="space-y-6">
        <SettingsPageHeader title={TITLE} description="The AI usage your organization pays for." />
        <p className="text-sm text-muted-foreground" role="status">
          {isLoading ? "Loading your organization…" : "You're not in an organization yet."}
        </p>
      </div>
    );
  }
  return <OrgAIUsage key={active.id} org={active} />;
}

function OrgAIUsage({ org }: { org: MyOrg }) {
  const [range, setRange] = useState<UsageRangeId>("month");
  const [group, setGroup] = useState<UsageGroupId>("member");
  const dates = useMemo(() => rangeDates(range), [range]);
  const groupBy = USAGE_GROUPS.find((g) => g.id === group)!.groupBy;
  const q = useOrgLLMUsage({ orgId: org.id, groupBy, ...dates });
  const members = useMembers(org.id);
  const names = memberNamesBySubject(members.data?.pages.flatMap((p) => p.members) ?? []);

  return (
    <div className="forge-ui space-y-6" data-testid="org-ai-usage-page">
      <SettingsPageHeader
        title={TITLE}
        description={`The AI usage ${org.name} pays for: its own API keys, seats it pays for, and Reliant credits. Members' own accounts are private and never shown here.`}
      />

      <div className="flex flex-wrap items-center justify-between gap-3">
        <Tabs tabs={USAGE_GROUPS} activeTab={group} onChange={(id) => setGroup(id as UsageGroupId)} variant="pills" ariaLabel="Group usage" />
        <label className="flex items-center gap-2 text-sm text-muted-foreground">
          Range
          <select
            className="rounded-md border border-border bg-background px-2 py-1 text-sm text-foreground"
            value={range}
            onChange={(e) => setRange(e.target.value as UsageRangeId)}
            data-testid="org-usage-range"
          >
            {USAGE_RANGES.map((r) => (
              <option key={r.id} value={r.id}>
                {r.label}
              </option>
            ))}
          </select>
        </label>
      </div>

      {q.isLoading ? (
        <p className="text-sm text-muted-foreground" role="status">
          Loading {org.name}'s AI usage…
        </p>
      ) : q.error ? (
        isPermissionError(q.error) ? (
          <p role="note" className="rounded-md border border-border/60 bg-background px-3 py-2 text-sm text-muted-foreground" data-testid="org-usage-denied">
            {usageErrorMessage(q.error, org.name)}
          </p>
        ) : (
          <div role="alert" className="flex flex-wrap items-center justify-between gap-3 rounded-lg border border-border bg-card p-4 text-sm" data-testid="org-usage-error">
            <span className="text-foreground">{usageErrorMessage(q.error, org.name)}</span>
            <Button variant="outline" size="sm" onClick={() => void q.refetch()}>
              Try again
            </Button>
          </div>
        )
      ) : q.data ? (
        <UsageReport data={q.data} group={group} names={names} orgName={org.name} />
      ) : null}
    </div>
  );
}

function UsageReport({
  data,
  group,
  names,
  orgName,
}: {
  data: GetOrgLLMUsageResponse;
  group: UsageGroupId;
  names: Map<string, string>;
  orgName: string;
}) {
  const totals = data.totals;
  const estimate = data.estimate;
  const estimateAvailable = estimate?.status === OrgLLMCostEstimateStatus.ORG_LLM_COST_ESTIMATE_STATUS_AVAILABLE;
  const totalCost = formatEstimate(totals?.estimatedListCost?.usdNanos);
  const column = USAGE_GROUPS.find((g) => g.id === group)!.column;

  const columns: DataTableColumn<Row>[] = [
    { key: "name", header: column, render: (_, { name }) => name },
    { key: "calls", header: "Calls", numeric: true, render: (_, { bucket }) => bucket.calls.toLocaleString("en-US") },
    { key: "input", header: "Input tokens", numeric: true, className: "text-muted-foreground", render: (_, { bucket }) => formatTokens(bucket.inputTokens) },
    { key: "output", header: "Output tokens", numeric: true, className: "text-muted-foreground", render: (_, { bucket }) => formatTokens(bucket.outputTokens) },
    {
      key: "cost",
      header: "Est. cost (org keys)",
      numeric: true,
      render: (_, { bucket }) => <span data-testid="org-usage-row-cost">{formatEstimate(bucket.estimatedListCost?.usdNanos) ?? "—"}</span>,
    },
  ];

  return (
    <div className="space-y-4">
      <dl className="grid grid-cols-1 gap-3 sm:grid-cols-3" data-testid="org-usage-totals">
        <Stat label="Calls" value={(totals?.calls ?? 0n).toLocaleString("en-US")} />
        <Stat label="Tokens (in / out)" value={`${formatTokens(totals?.inputTokens ?? 0n)} / ${formatTokens(totals?.outputTokens ?? 0n)}`} />
        <Stat
          label="Estimated cost of org API keys"
          value={totalCost ?? "—"}
          hint={estimateAvailable ? "Estimate" : estimate?.status === OrgLLMCostEstimateStatus.ORG_LLM_COST_ESTIMATE_STATUS_UNAVAILABLE ? "Prices unavailable" : "No org key usage"}
        />
      </dl>

      <DataTable<Row>
        ariaLabel={`${orgName} AI usage by ${column.toLowerCase()}`}
        columns={columns}
        data={data.buckets.map((bucket) => ({ key: bucket.key, bucket, name: bucketName(group, bucket, names) }))}
        getRowKey={(r) => r.key}
        emptyMessage="No AI usage paid for by the organization in this range."
      />

      <div className="space-y-1 text-xs text-muted-foreground" data-testid="org-usage-footnote">
        {estimate?.label && <p data-testid="org-usage-estimate-label">{estimate.label}</p>}
        {estimateAvailable && estimate.unpricedModels.length > 0 && (
          <p>No list price for {estimate.unpricedModels.join(", ")}: those calls are counted but not in the estimate.</p>
        )}
        {data.note && <p>{data.note}</p>}
      </div>
    </div>
  );
}

type Row = { key: string; bucket: OrgLLMUsageBucket; name: string };

function Stat({ label, value, hint }: { label: string; value: string; hint?: string }) {
  return (
    <div className="rounded-lg border border-border bg-card p-4">
      <dt className="text-xs font-medium text-muted-foreground">{label}</dt>
      <dd className="mt-1 text-xl font-semibold tabular-nums text-foreground">{value}</dd>
      {hint && <dd className="mt-0.5 text-xs text-muted-foreground">{hint}</dd>}
    </div>
  );
}
