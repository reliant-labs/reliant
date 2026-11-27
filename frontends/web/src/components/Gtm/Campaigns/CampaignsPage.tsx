// Copyright (c) 2026 Reliant Labs

/**
 * GTM › Campaigns: each campaign's strategy, goal and period; for the one
 * selected (`?campaign=`), its coverage (GetCampaign.coverage: the channels
 * and formats it planned against its items, "Reddit · Short post: planned,
 * nothing drafted"), its files, and a board of what it holds (its ideas,
 * posts and emails) by stage. A person creates and edits campaigns here:
 * strategy, period, status, planned channels and formats.
 *
 * Cards move between Drafting and In review (UpdateItem status, on the
 * revision on screen), a paused one back to Drafting. A move into Approved
 * is a person's approval, so it opens the approval dialog and the card moves
 * only once that is confirmed.
 * Nothing moves into Out from here: posting is outside GTM, and an email goes
 * out only through the agent, behind an approval card in the chat.
 */

import { useNavigate, useSearch } from "@tanstack/react-router";
import { Pencil, Plus } from "lucide-react";
import { useMemo, useState } from "react";

import Badge from "@/components/forge-ui/badge";
import Button from "@/components/forge-ui/button";
import EmptyState from "@/components/forge-ui/empty_state";
import KanbanBoard, { type KanbanColumn } from "@/components/forge-ui/kanban_board";
import PageHeader from "@/components/forge-ui/page_header";
import SkeletonLoader from "@/components/forge-ui/skeleton_loader";
import { GtmIdeaStatus, GtmItemStatus, type GtmCampaign, type GtmChannel, type GtmIdea, type GtmItem, type GtmStrategy } from "@/gen/reliant/v1/gtm_pb";
import { useGetCampaign, useListCampaigns, useListIdeas, useListItems, useListStrategies, useUpdateItem } from "@/hooks/gtm-service-hooks_gen";
import { gtmIdeaPath } from "@/lib/paths";
import { toast } from "@/lib/toast-manager";

import { GtmShell, useGtmOrgId } from "../shared/GtmShell";
import { AssetPanel } from "../shared/Assets";
import { ClaimCheck, ItemDialog, useGtmChannels } from "../shared/ItemDialog";
import { canApprove, isEmailItem } from "../shared/lifecycle";
import { PersonActionDialog, PersonOnlyTag } from "../shared/PersonAction";
import { CAMPAIGN_STATUS, formatAndChannel, formatDay, formatLabel, formatWhen, IDEA_STATUS, isItemOut, tsMs } from "../shared/ui";
import { useGtmWriteError } from "../shared/writes";
import { CampaignForm } from "./CampaignForm";
import { CoveragePanel } from "./Coverage";

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

const NEW = "new";
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
    posts: mine.filter((i) => !isEmailItem(i)).length,
    emails: mine.filter((i) => isEmailItem(i)).length,
  };
}

export function columnsOf(ideas: readonly GtmIdea[], items: readonly GtmItem[], campaignId: string, channels: readonly GtmChannel[] = []): KanbanColumn[] {
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
      description: `${formatAndChannel(item.format, channels.find((c) => c.id === item.channelId))} · ${formatWhen(item.scheduledAt)}`,
      tags: [formatLabel(item.format)],
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
  const strategiesQ = useListStrategies({ orgId, pageSize: 200 }, { enabled: !!orgId });
  const channels = useGtmChannels();
  // "new", or the id of the campaign being edited: the form reads the campaign
  // live from the list, so after a conflict it is on the current revision.
  const [editing, setEditing] = useState<string | null>(null);
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
  const columns = useMemo(() => (selected ? columnsOf(ideas, items, selected.id, channels) : []), [ideas, items, selected, channels]);
  const detailQ = useGetCampaign({ orgId, id: selected?.id ?? "" }, { enabled: !!orgId && !!selected });
  const strategyOf = (c: GtmCampaign) => strategiesQ.data?.strategies.find((s) => s.id === c.strategyId);
  const select = (id: string) => void navigate({ to: ".", search: (prev: Record<string, unknown>) => ({ ...prev, campaign: id }) } as never);

  // The moves a person may make on the board (shared/lifecycle.ts): DRAFT <-> REVIEW,
  // PAUSED -> DRAFT, and DRAFT or REVIEW -> APPROVED behind a confirm. Nothing
  // moves into Out, and an approved item leaves Approved only by an edit (back
  // to review) or a pause, from its drawer.
  function onCardMove(cardId: string, _from: string, to: string) {
    if (!cardId.startsWith(ITEM_CARD)) return; // ideas stay in Ideas: their posts move, not they
    const item = items.find((i) => ITEM_CARD + i.id === cardId);
    if (!item) return;
    const moveTo = (status: GtmItemStatus) => move.mutate({ orgId, id: item.id, expectedRevision: item.revision, status });
    if (to === "out") {
      toast.info(
        isEmailItem(item)
          ? "Emails go out through the agent, after you approve its card in the chat. Nothing sends from this board."
          : "Posting happens outside GTM. Nothing publishes from this board.",
      );
      return;
    }
    if (isItemOut(item.status)) return;
    if (item.status === GtmItemStatus.PAUSED) {
      if (to === "drafting") moveTo(GtmItemStatus.DRAFT);
      else toast.info("A paused item resumes as a draft first.");
      return;
    }
    if (item.status === GtmItemStatus.APPROVED || item.status === GtmItemStatus.SCHEDULED) {
      if (to !== "approved") toast.info("An approved item goes back to review when you edit it. Open it to edit or pause it.");
      return;
    }
    if (to === "drafting" && item.status !== GtmItemStatus.DRAFT) moveTo(GtmItemStatus.DRAFT);
    else if (to === "review" && item.status !== GtmItemStatus.REVIEW) moveTo(GtmItemStatus.REVIEW);
    else if (to === "approved" && canApprove(item)) setApproving(item);
  }

  const truncated = [ideasQ.data, itemsQ.data, campaignsQ.data].some((r) => !!r?.nextPageToken);
  const header = (
    <div className="flex flex-wrap items-start justify-between gap-x-4 gap-y-3">
      <PageHeader title="Campaigns" meta={<span>Each campaign's plan, what covers it, and its ideas, posts and emails by stage.</span>} className="" />
      <Button variant="primary" size="sm" leftIcon={<Plus />} onClick={() => setEditing(NEW)} aria-haspopup="dialog">
        New campaign
      </Button>
    </div>
  );
  const editingCampaign = editing && editing !== NEW ? campaigns.find((c) => c.id === editing) : undefined;
  const form = editing && (editing === NEW || editingCampaign) && (
    <CampaignForm
      key={editingCampaign ? `${editingCampaign.id}@${editingCampaign.revision}` : NEW}
      campaign={editingCampaign}
      channels={channels}
      onClose={() => setEditing(null)}
      onSaved={(id) => id && select(id)}
    />
  );
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
        <EmptyState title="No campaigns yet" description="Create one, or ask the agent to plan one in a chat; it saves campaigns here." />
        {form}
      </>
    );
  }

  return (
    <>
      {header}
      <div className="grid gap-3 md:grid-cols-2" role="group" aria-label="Campaigns">
        {campaigns.map((c) => (
          <CampaignCard key={c.id} campaign={c} strategy={strategyOf(c)} counts={countsOf(ideas, items, c.id)} selected={c.id === selected.id} onSelect={() => select(c.id)} />
        ))}
      </div>
      <div className="flex justify-end">
        <Button variant="outline" size="sm" leftIcon={<Pencil />} onClick={() => setEditing(selected.id)} aria-haspopup="dialog">
          Edit {selected.name}
        </Button>
      </div>
      {detailQ.isError ? (
        <p className="text-sm text-ink-muted">The campaign's coverage could not be read.</p>
      ) : detailQ.data ? (
        <CoveragePanel name={selected.name} rows={detailQ.data.coverage} channels={channels} />
      ) : null}
      {detailQ.data?.truncated && <p className="text-xs text-ink-muted">This campaign's ideas and items are shown up to the first 200 of each.</p>}
      <AssetPanel key={selected.id} owner={{ campaignId: selected.id }} label="Campaign files" />
      <div className="flex flex-wrap items-center gap-x-3 gap-y-1 text-xs text-ink-muted">
        <span>Drag a card between Drafting and In review, or focus it and press Alt+Arrow.</span>
        <span className="inline-flex items-center gap-1.5">
          Moving into Approved <PersonOnlyTag />
        </span>
      </div>
      {truncated && (
        <p className="text-xs text-ink-muted" data-testid="gtm-campaigns-truncated">
          Showing the first {PAGE.campaigns} campaigns, {PAGE.ideas} ideas and {PAGE.items} posts and emails.
        </p>
      )}
      {/* No fixed height: each column grows with its cards, and the page scrolls. */}
      <div className="min-h-80" data-testid="gtm-campaign-board">
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
      {form}
      {approving && (
        <PersonActionDialog
          open
          onClose={() => setApproving(null)}
          action="approve"
          subject={approving.title}
          busy={approve.isPending}
          onConfirm={() => approve.mutate({ orgId, id: approving.id, expectedRevision: approving.revision, status: GtmItemStatus.APPROVED })}
        >
          {/* What the approval overrides, as in the item's drawer: the copy's claims against the org's verified facts. */}
          <ClaimCheck orgId={orgId} text={approving.bodyMd} />
        </PersonActionDialog>
      )}
    </>
  );
}

function CampaignCard({
  campaign,
  strategy,
  counts,
  selected,
  onSelect,
}: {
  campaign: GtmCampaign;
  strategy: GtmStrategy | undefined;
  counts: Counts;
  selected: boolean;
  onSelect: () => void;
}) {
  const status = CAMPAIGN_STATUS[campaign.status];
  return (
    <button
      type="button"
      aria-pressed={selected}
      onClick={onSelect}
      className={`rounded-lg border bg-surface p-4 text-left transition-colors focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-accent ${
        selected ? "border-ink/40 ring-1 ring-ink/20" : "border-border hover:border-border-strong"
      }`}
    >
      <span className="flex flex-wrap items-center gap-2">
        <span className="text-sm font-semibold text-ink">{campaign.name}</span>
        {status && <Badge label={status.label} variant={status.variant} size="sm" />}
      </span>
      {strategy && <span className="mt-0.5 block text-xs text-ink-muted">Strategy: {strategy.name}</span>}
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
