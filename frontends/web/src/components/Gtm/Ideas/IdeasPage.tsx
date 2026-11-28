// Copyright (c) 2026 Reliant Labs

/**
 * GTM › Ideas: what to build, say or launch next, proposed by agents (a chat)
 * or by people. A tab per status (`?status=`), a row per idea with its
 * source, status and why; a row opens the idea's page with its draft.
 */

import { useNavigate, useSearch } from "@tanstack/react-router";
import { Sparkles } from "lucide-react";
import { useMemo, useState } from "react";

import Button from "@/components/forge-ui/button";
import DataTable, { type DataTableColumn } from "@/components/forge-ui/data_table";
import EmptyState from "@/components/forge-ui/empty_state";
import PageHeader from "@/components/forge-ui/page_header";
import Tabs from "@/components/forge-ui/tabs";
import { GtmIdeaStatus, type GtmIdea } from "@/gen/reliant/v1/gtm_pb";
import { useListCampaigns, useListIdeas } from "@/hooks/gtm-service-hooks_gen";
import { gtmIdeaPath } from "@/lib/paths";

import { GtmShell, useGtmOrgId } from "../shared/GtmShell";
import { formatDay, IDEA_KIND, IDEA_STATUS, IdeaStatusBadge, SourceTag, tsMs } from "../shared/ui";
import { AskForIdeasDialog } from "./AskForIdeas";
import { splitIdeaBody } from "./ideaBody";

const TAB_STATUS = {
  proposed: GtmIdeaStatus.PROPOSED,
  accepted: GtmIdeaStatus.ACCEPTED,
  done: GtmIdeaStatus.DONE,
  rejected: GtmIdeaStatus.REJECTED,
} as const;
type StatusTab = "all" | keyof typeof TAB_STATUS;
const STATUS_TABS: StatusTab[] = ["all", "proposed", "accepted", "done", "rejected"];

export function statusTabOf(value: unknown): StatusTab {
  return STATUS_TABS.includes(value as StatusTab) ? (value as StatusTab) : "all";
}

const tabOfStatus = (status: GtmIdeaStatus): StatusTab | undefined =>
  (Object.keys(TAB_STATUS) as (keyof typeof TAB_STATUS)[]).find((tab) => TAB_STATUS[tab] === status);

/** The most ideas one page lists; the list's own page limit (ListIdeasRequest.page_size). */
const IDEAS_PAGE = 200;

type IdeaRow = GtmIdea & Record<string, unknown>;

export function IdeasPage() {
  return (
    <GtmShell page="ideas">
      <IdeasView />
    </GtmShell>
  );
}

export function IdeasView() {
  const orgId = useGtmOrgId();
  const ideasQ = useListIdeas({ orgId, pageSize: IDEAS_PAGE }, { enabled: !!orgId });
  const campaignsQ = useListCampaigns({ orgId, pageSize: 200 }, { enabled: !!orgId });
  const navigate = useNavigate();
  const search = useSearch({ strict: false }) as Record<string, unknown>;
  const tab = statusTabOf(search.status);
  const [asking, setAsking] = useState(false);
  const setTab = (next: StatusTab) =>
    void navigate({ to: ".", search: (prev: Record<string, unknown>) => ({ ...prev, status: next === "all" ? undefined : next }) } as never);

  const ideas = useMemo(() => ideasQ.data?.ideas ?? [], [ideasQ.data]);
  const campaigns = campaignsQ.data?.campaigns ?? [];
  const counts = useMemo(() => {
    const c: Record<string, number> = { all: ideas.length };
    for (const i of ideas) {
      const t = tabOfStatus(i.status);
      if (t) c[t] = (c[t] ?? 0) + 1;
    }
    return c;
  }, [ideas]);
  const rows = useMemo(
    () =>
      ideas
        .filter((i) => tab === "all" || i.status === TAB_STATUS[tab])
        .sort((a, b) => tsMs(b.updatedAt) - tsMs(a.updatedAt)) as IdeaRow[],
    [ideas, tab],
  );

  const columns: DataTableColumn<IdeaRow>[] = [
    {
      key: "title",
      header: "Idea",
      stackTitle: true,
      render: (_, row) => (
        <span className="block min-w-0 whitespace-normal py-1">
          <span className="block font-medium text-ink">{row.title}</span>
          <span className="mt-0.5 line-clamp-2 block text-xs text-ink-muted">{splitIdeaBody(row.bodyMd).why}</span>
        </span>
      ),
    },
    { key: "source", header: "Source", width: "110px", render: (_, row) => <SourceTag source={row.source} /> },
    { key: "status", header: "Status", width: "110px", render: (_, row) => <IdeaStatusBadge status={row.status} /> },
    { key: "kind", header: "Kind", width: "90px", render: (_, row) => <span className="text-ink-muted">{IDEA_KIND[row.kind] ?? "—"}</span> },
    {
      key: "campaignId",
      header: "Campaign",
      width: "190px",
      render: (_, row) => <span className="block whitespace-normal text-ink-muted">{campaigns.find((c) => c.id === row.campaignId)?.name ?? "—"}</span>,
    },
    { key: "updatedAt", header: "Updated", width: "90px", render: (_, row) => <span className="text-ink-muted">{formatDay(row.updatedAt)}</span> },
  ];

  return (
    <>
      {/* The action wraps under the title on a phone: PageHeader keeps actions
          beside it and squeezes the meta line to a column. */}
      <div className="flex flex-wrap items-start justify-between gap-x-4 gap-y-3">
        <PageHeader title="Ideas" meta={<span>What to build, say or launch next. Agents propose; a person accepts or rejects.</span>} className="" />
        <Button variant="primary" size="sm" leftIcon={<Sparkles />} onClick={() => setAsking(true)} aria-haspopup="dialog">
          Ask the agent for ideas
        </Button>
      </div>
      <Tabs
        tabs={STATUS_TABS.map((id) => ({ id, label: id === "all" ? "All" : IDEA_STATUS[TAB_STATUS[id]]!.label, badge: counts[id] ?? 0 }))}
        activeTab={tab}
        onChange={(id) => setTab(statusTabOf(id))}
        ariaLabel="Idea status"
      />
      {ideasQ.isError ? (
        <EmptyState title="Couldn't load ideas" description="Something went wrong reading this org's ideas." actionLabel="Try again" onAction={() => void ideasQ.refetch()} />
      ) : (
        <DataTable
          columns={columns}
          data={rows}
          loading={ideasQ.isPending}
          getRowKey={(row) => row.id}
          getRowHref={(row) => gtmIdeaPath(row.id)}
          getRowLabel={(row) => row.title}
          layout="fixed"
          stacked
          ariaLabel="Ideas"
          emptyMessage={tab === "all" ? "No ideas yet. Ask the agent for some, or add your own in a chat." : "No ideas with this status."}
        />
      )}
      {ideasQ.data?.nextPageToken ? (
        <p className="text-xs text-ink-muted" data-testid="gtm-ideas-truncated">
          Showing the first {IDEAS_PAGE} ideas.
        </p>
      ) : null}
      <AskForIdeasDialog open={asking} onClose={() => setAsking(false)} />
    </>
  );
}
