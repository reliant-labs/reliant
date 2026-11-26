// Copyright (c) 2026 Reliant Labs

/**
 * GTM › Campaigns: each campaign's goal and period, and a board of what it
 * holds (its ideas, posts and emails) by stage. The campaign is `?campaign=`.
 *
 * Cards move freely between Drafting and In review (UpdateItem status, on
 * the revision on screen). A move into Approved is a person's approval, so it
 * opens the approval dialog and the card moves only once that is confirmed.
 * Nothing moves into Out from here: posting is outside GTM, and an email goes
 * out only through the agent, behind an approval card in the chat.
 */

import { useNavigate, useSearch } from "@tanstack/react-router";
import { useMemo, useState } from "react";

import Badge from "@/components/forge-ui/badge";
import EmptyState from "@/components/forge-ui/empty_state";
import KanbanBoard, { type KanbanColumn } from "@/components/forge-ui/kanban_board";
import PageHeader from "@/components/forge-ui/page_header";
import SkeletonLoader from "@/components/forge-ui/skeleton_loader";
import { GtmIdeaStatus, GtmItemKind, GtmItemStatus, type GtmCampaign, type GtmIdea, type GtmItem } from "@/gen/reliant/v1/gtm_pb";
import { useListCampaigns, useListIdeas, useListItems, useUpdateItem } from "@/hooks/gtm-service-hooks_gen";
import { gtmIdeaPath } from "@/lib/paths";
import { toast } from "@/lib/toast-manager";

import { GtmShell, useGtmOrgId } from "../shared/GtmShell";
import { ItemDialog } from "../shared/ItemDialog";
import { PersonActionDialog, PersonOnlyTag } from "../shared/PersonAction";
import { CAMPAIGN_STATUS, formatDay, formatWhen, IDEA_STATUS, ITEM_KIND, kindAndChannel, tsMs } from "../shared/ui";
import { useGtmWriteError } from "../shared/writes";

type Stage = "ideas" | "drafting" | "review" | "approved" | "out";

const STAGES: { id: Stage; title: string; tone: KanbanColumn["tone"] }[] = [
  { id: "ideas", title: "Ideas", tone: "neutral" },
  { id: "drafting", title: "Drafting", tone: "neutral" },
  { id: "review", title: "In review", tone: "warning" },
  { id: "approved", title: "Approved", tone: "success" },
  { id: "out", title: "Out", tone: "neutral" },
];

const STAGE_OF: Partial<Record<GtmItemStatus, Stage>> = {
  [GtmItemStatus.DRAFT]: "drafting",
  [GtmItemStatus.PAUSED]: "drafting",
  [GtmItemStatus.REVIEW]: "review",
  [GtmItemStatus.APPROVED]: "approved",
  [GtmItemStatus.SCHEDULED]: "approved",
  [GtmItemStatus.SENT]: "out",
  [GtmItemStatus.PUBLISHED]: "out",
};

/** Item statuses a card moves between without an approval. */
const UNGATED: readonly GtmItemStatus[] = [GtmItemStatus.DRAFT, GtmItemStatus.REVIEW, GtmItemStatus.PAUSED];

const IDEA_CARD = "idea:";
const ITEM_CARD = "item:";

/** The most of each record the board reads; the lists' own page limits. */
const PAGE = { campaigns: 200, ideas: 200, items: 500 } as const;

export function CampaignsPage() {
  return (
    <GtmShell page="campaigns">
      <CampaignsView />
    </GtmShell>
  );
}

interface Counts {
  ideas: number;
  posts: number;
  emails: number;
}

export function countsOf(ideas: readonly GtmIdea[], items: readonly GtmItem[], campaignId: string): Counts {
  const mine = items.filter((i) => i.campaignId === campaignId);
  return {
    ideas: ideas.filter((i) => i.campaignId === campaignId && i.status !== GtmIdeaStatus.REJECTED).length,
    posts: mine.filter((i) => i.kind !== GtmItemKind.EMAIL).length,
    emails: mine.filter((i) => i.kind === GtmItemKind.EMAIL).length,
  };
}

export function columnsOf(ideas: readonly GtmIdea[], items: readonly GtmItem[], campaignId: string): KanbanColumn[] {
  const cols: KanbanColumn[] = STAGES.map((s) => ({ id: s.id, title: s.title, tone: s.tone, cards: [] }));
  const col = (id: Stage) => cols.find((c) => c.id === id)!;
  for (const idea of ideas) {
    if (idea.campaignId !== campaignId || idea.status === GtmIdeaStatus.REJECTED) continue;
    col("ideas").cards.push({ id: IDEA_CARD + idea.id, title: idea.title, description: `Idea · ${IDEA_STATUS[idea.status]?.label ?? ""}`, tags: ["Idea"] });
  }
  const mine = items
    .filter((i) => i.campaignId === campaignId)
    .sort((a, b) => (a.scheduledAt ? tsMs(a.scheduledAt) : Infinity) - (b.scheduledAt ? tsMs(b.scheduledAt) : Infinity));
  for (const item of mine) {
    col(STAGE_OF[item.status] ?? "drafting").cards.push({
      id: ITEM_CARD + item.id,
      title: item.title,
      description: `${kindAndChannel(item.kind, item.channel)} · ${formatWhen(item.scheduledAt)}`,
      tags: [ITEM_KIND[item.kind] ?? "Item"],
    });
  }
  return cols;
}

const plural = (n: number, noun: string) => `${n} ${noun}${n === 1 ? "" : "s"}`;

export function CampaignsView() {
  const orgId = useGtmOrgId();
  const campaignsQ = useListCampaigns({ orgId, pageSize: PAGE.campaigns }, { enabled: !!orgId });
  const ideasQ = useListIdeas({ orgId, pageSize: PAGE.ideas }, { enabled: !!orgId });
  const itemsQ = useListItems({ orgId, pageSize: PAGE.items }, { enabled: !!orgId });
  const navigate = useNavigate();
  const search = useSearch({ strict: false }) as Record<string, unknown>;
  const [openItemId, setOpenItemId] = useState<string | null>(null);
  const [approving, setApproving] = useState<GtmItem | null>(null);
  const onError = useGtmWriteError();
  const move = useUpdateItem({ onError });
  const approve = useUpdateItem({ onSuccess: () => setApproving(null), onError: (e) => (setApproving(null), onError(e)) });

  const campaigns = useMemo(() => campaignsQ.data?.campaigns ?? [], [campaignsQ.data]);
  const ideas = useMemo(() => ideasQ.data?.ideas ?? [], [ideasQ.data]);
  const items = useMemo(() => itemsQ.data?.items ?? [], [itemsQ.data]);
  const selected = campaigns.find((c) => c.id === search.campaign) ?? campaigns[0];
  const columns = useMemo(() => (selected ? columnsOf(ideas, items, selected.id) : []), [ideas, items, selected]);
  const select = (id: string) => void navigate({ to: ".", search: (prev: Record<string, unknown>) => ({ ...prev, campaign: id }) } as never);

  function onCardMove(cardId: string, _from: string, to: string) {
    if (!cardId.startsWith(ITEM_CARD)) return; // ideas stay in Ideas: their posts move, not they
    const item = items.find((i) => ITEM_CARD + i.id === cardId);
    if (!item || !UNGATED.includes(item.status)) return;
    if (to === "drafting" || to === "review") {
      const status = to === "drafting" ? GtmItemStatus.DRAFT : GtmItemStatus.REVIEW;
      if (status !== item.status) move.mutate({ orgId, id: item.id, expectedRevision: item.revision, status });
      return;
    }
    if (to === "approved") {
      setApproving(item);
      return;
    }
    if (to === "out") {
      toast.info(
        item.kind === GtmItemKind.EMAIL
          ? "Emails go out through the agent, after you approve its card in the chat. Nothing sends from this board."
          : "Posting happens outside GTM. Nothing publishes from this board.",
      );
    }
  }

  const header = <PageHeader title="Campaigns" meta={<span>Each campaign's ideas, posts and emails, by stage.</span>} className="" />;
  if (campaignsQ.isPending) {
    return (
      <>
        {header}
        <SkeletonLoader variant="card" count={2} />
      </>
    );
  }
  if (campaignsQ.isError) {
    return (
      <>
        {header}
        <EmptyState title="Couldn't load campaigns" description="Something went wrong reading this org's campaigns." actionLabel="Try again" onAction={() => void campaignsQ.refetch()} />
      </>
    );
  }
  if (!selected) {
    return (
      <>
        {header}
        <EmptyState title="No campaigns yet" description="Ask the agent to plan one in a chat; it saves campaigns here." />
      </>
    );
  }

  return (
    <>
      {header}
      <div className="grid gap-3 md:grid-cols-2" role="radiogroup" aria-label="Campaign">
        {campaigns.map((c) => (
          <CampaignCard key={c.id} campaign={c} counts={countsOf(ideas, items, c.id)} selected={c.id === selected.id} onSelect={() => select(c.id)} />
        ))}
      </div>
      <div className="flex flex-wrap items-center gap-x-3 gap-y-1 text-xs text-ink-muted">
        <span>Drag a card between Drafting and In review, or focus it and press Alt+Arrow.</span>
        <span className="inline-flex items-center gap-1.5">
          Moving into Approved <PersonOnlyTag />
        </span>
      </div>
      <div className="h-[620px]" data-testid="gtm-campaign-board">
        <KanbanBoard
          columns={columns}
          columnClassName="w-64 shrink-0 lg:w-auto lg:min-w-48 lg:flex-1 lg:shrink"
          onCardMove={onCardMove}
          onCardClick={(card) => {
            if (card.id.startsWith(IDEA_CARD)) {
              void navigate({ to: gtmIdeaPath(card.id.slice(IDEA_CARD.length)) } as never);
              return;
            }
            setOpenItemId(card.id.slice(ITEM_CARD.length));
          }}
        />
      </div>
      <ItemDialog item={items.find((i) => i.id === openItemId) ?? null} onClose={() => setOpenItemId(null)} />
      {approving && (
        <PersonActionDialog
          open
          onClose={() => setApproving(null)}
          action="approve"
          subject={approving.title}
          busy={approve.isPending}
          onConfirm={() => approve.mutate({ orgId, id: approving.id, expectedRevision: approving.revision, status: GtmItemStatus.APPROVED })}
        />
      )}
    </>
  );
}

function CampaignCard({ campaign, counts, selected, onSelect }: { campaign: GtmCampaign; counts: Counts; selected: boolean; onSelect: () => void }) {
  const status = CAMPAIGN_STATUS[campaign.status];
  return (
    <button
      type="button"
      role="radio"
      aria-checked={selected}
      onClick={onSelect}
      className={`rounded-lg border bg-surface p-4 text-left transition-colors focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-accent ${
        selected ? "border-ink/40 ring-1 ring-ink/20" : "border-border hover:border-border-strong"
      }`}
    >
      <span className="flex flex-wrap items-center gap-2">
        <span className="text-sm font-semibold text-ink">{campaign.name}</span>
        {status && <Badge label={status.label} variant={status.variant} size="sm" />}
      </span>
      {campaign.goal && <span className="mt-1 block text-sm text-ink-muted">{campaign.goal}</span>}
      <span className="mt-3 flex flex-wrap gap-x-4 gap-y-1 text-xs text-ink-muted">
        {(campaign.periodStart || campaign.periodEnd) && (
          <span>
            {formatDay(campaign.periodStart)} – {formatDay(campaign.periodEnd)}
          </span>
        )}
        <span className="tabular-nums">{plural(counts.ideas, "idea")}</span>
        <span className="tabular-nums">{plural(counts.posts, "post")}</span>
        <span className="tabular-nums">{plural(counts.emails, "email")}</span>
      </span>
    </button>
  );
}
