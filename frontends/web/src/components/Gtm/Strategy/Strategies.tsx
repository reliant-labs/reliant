// Copyright (c) 2026 Reliant Labs

/**
 * The org's strategies: the list (ListStrategies), one strategy with its
 * campaigns (GetStrategy), and a person's edits.
 *
 * Only a person moves a strategy to ACTIVE, DONE or ARCHIVED, or edits one
 * that is not DRAFT (an agent drafts strategies and edits drafts only).
 * Every web user is a person, so the status moves are buttons here, each on
 * the revision on screen (expected_revision). Several strategies may be
 * ACTIVE at once.
 */

import { Code } from "@connectrpc/connect";
import { ArrowLeft, Pencil, Plus } from "lucide-react";
import { useState } from "react";

import Badge from "@/components/forge-ui/badge";
import Button from "@/components/forge-ui/button";
import Card, { CardBody } from "@/components/forge-ui/card";
import EmptyState from "@/components/forge-ui/empty_state";
import Input from "@/components/forge-ui/input";
import Link from "@/components/forge-ui/link";
import Modal from "@/components/forge-ui/modal";
import SkeletonLoader from "@/components/forge-ui/skeleton_loader";
import Tag from "@/components/forge-ui/tag";
import Textarea from "@/components/forge-ui/textarea";
import { GtmStrategyCategory, GtmStrategyStatus, type GtmCampaign, type GtmStrategy } from "@/gen/reliant/v1/gtm_pb";
import { useCreateStrategy, useGetStrategy, useListStrategies, useUpdateStrategy } from "@/hooks/gtm-service-hooks_gen";
import { gtmPath, withSearch } from "@/lib/paths";

import { useGtmOrgId } from "../shared/GtmShell";
import { PersonOnlyTag } from "../shared/PersonAction";
import { CAMPAIGN_STATUS, categoryLabel, Fields, formatDay, STRATEGY_CATEGORIES, StrategyStatusBadge, tsMs } from "../shared/ui";
import { useGtmWriteError } from "../shared/writes";
import { useStrategyNav } from "./nav";

const STATUS_ORDER: Record<number, number> = {
  [GtmStrategyStatus.ACTIVE]: 0,
  [GtmStrategyStatus.DRAFT]: 1,
  [GtmStrategyStatus.DONE]: 2,
  [GtmStrategyStatus.ARCHIVED]: 3,
};

/** A strategy's period: "Oct 27 – Apr 25", "From Oct 27", or "Open". Its dates are YYYY-MM-DD. */
export function periodOf(s: Pick<GtmStrategy, "periodStart" | "periodEnd">): string {
  const day = (ymd: string) => {
    const m = /^(\d{4})-(\d{2})-(\d{2})$/.exec(ymd);
    return m ? formatDay(new Date(Number(m[1]), Number(m[2]) - 1, Number(m[3]))) : ymd;
  };
  if (s.periodStart && s.periodEnd) return `${day(s.periodStart)} – ${day(s.periodEnd)}`;
  if (s.periodStart) return `From ${day(s.periodStart)}`;
  if (s.periodEnd) return `Until ${day(s.periodEnd)}`;
  return "Open";
}

export function StrategiesView() {
  const orgId = useGtmOrgId();
  const q = useListStrategies({ orgId, pageSize: 200 }, { enabled: !!orgId });
  const [creating, setCreating] = useState(false);
  const nav = useStrategyNav();
  const strategies = [...(q.data?.strategies ?? [])].sort((a, b) => (STATUS_ORDER[a.status] ?? 9) - (STATUS_ORDER[b.status] ?? 9) || tsMs(b.createdAt) - tsMs(a.createdAt));

  return (
    <div className="space-y-4" data-testid="gtm-strategies">
      <div className="flex justify-end">
        <Button variant="primary" size="sm" leftIcon={<Plus />} onClick={() => setCreating(true)} aria-haspopup="dialog">
          New strategy
        </Button>
      </div>
      {q.isPending ? (
        <SkeletonLoader variant="card" count={2} />
      ) : q.isError ? (
        <EmptyState title="Couldn't load strategies" description="Something went wrong reading this org's strategies." actionLabel="Try again" onAction={() => void q.refetch()} />
      ) : strategies.length === 0 ? (
        <EmptyState title="No strategy yet" description="Start one from the library, write one, or ask the agent to propose one from your positioning." />
      ) : (
        <ul className="space-y-3">
          {strategies.map((s) => (
            <li key={s.id}>
              <StrategySummary strategy={s} onOpen={() => nav.strategy(s.id)} />
            </li>
          ))}
        </ul>
      )}
      {q.data?.nextPageToken ? <p className="text-xs text-ink-muted">Showing the first 200 strategies.</p> : null}
      {creating && <StrategyForm onClose={() => setCreating(false)} onSaved={(id) => nav.strategy(id)} />}
    </div>
  );
}

function StrategySummary({ strategy, onOpen }: { strategy: GtmStrategy; onOpen: () => void }) {
  return (
    <button
      type="button"
      onClick={onOpen}
      className="w-full rounded-lg border border-border bg-surface p-4 text-left transition-colors hover:border-border-strong focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-accent"
    >
      <span className="flex flex-wrap items-center gap-2">
        <span className="text-base font-semibold text-ink">{strategy.name}</span>
        <StrategyStatusBadge status={strategy.status} />
        {strategy.category !== GtmStrategyCategory.UNSPECIFIED && <Tag>{categoryLabel(strategy.category)}</Tag>}
      </span>
      {strategy.goalMd && <span className="mt-1.5 line-clamp-2 block max-w-prose whitespace-pre-wrap text-sm text-ink-muted">{strategy.goalMd}</span>}
      <span className="mt-3 flex flex-wrap gap-x-4 gap-y-1 text-xs text-ink-muted">
        <span>{periodOf(strategy)}</span>
        {strategy.targetMetric && <span>Target: {strategy.targetMetric}</span>}
        {strategy.libraryKey && <span>From the library</span>}
      </span>
    </button>
  );
}

/** The status moves a person may make from each status. */
const MOVES: { to: GtmStrategyStatus; label: string; from: GtmStrategyStatus[] }[] = [
  { to: GtmStrategyStatus.ACTIVE, label: "Make active", from: [GtmStrategyStatus.DRAFT, GtmStrategyStatus.DONE, GtmStrategyStatus.ARCHIVED] },
  { to: GtmStrategyStatus.DONE, label: "Mark done", from: [GtmStrategyStatus.ACTIVE] },
  { to: GtmStrategyStatus.ARCHIVED, label: "Archive", from: [GtmStrategyStatus.DRAFT, GtmStrategyStatus.ACTIVE, GtmStrategyStatus.DONE] },
];

export function StrategyDetail({ id }: { id: string }) {
  const orgId = useGtmOrgId();
  const nav = useStrategyNav();
  const q = useGetStrategy({ orgId, id }, { enabled: !!orgId && id !== "" });
  const [editing, setEditing] = useState(false);
  const onError = useGtmWriteError();
  const update = useUpdateStrategy({ onError });
  const pending = update.isPending ? update.variables?.status : undefined;

  const back = (
    <Button variant="ghost" size="sm" leftIcon={<ArrowLeft />} onClick={nav.list}>
      All strategies
    </Button>
  );
  if (q.isPending) return <SkeletonLoader variant="card" count={2} />;
  const strategy = q.data?.strategy;
  if (q.isError && q.error.code !== Code.NotFound) {
    return (
      <div className="space-y-3">
        {back}
        <EmptyState title="Couldn't load this strategy" description="Something went wrong reading it." actionLabel="Try again" onAction={() => void q.refetch()} />
      </div>
    );
  }
  if (!strategy) {
    return (
      <div className="space-y-3">
        {back}
        <EmptyState title="Strategy not found" description="It may have been removed, or it belongs to another org." />
      </div>
    );
  }
  const campaigns = q.data?.campaigns ?? [];

  return (
    <div className="space-y-4" data-testid="gtm-strategy-detail">
      {back}
      <Card>
        <CardBody>
          <div className="flex flex-wrap items-start justify-between gap-3">
            <div className="min-w-0">
              <div className="flex flex-wrap items-center gap-2">
                <h2 className="text-lg font-semibold text-ink">{strategy.name}</h2>
                <StrategyStatusBadge status={strategy.status} />
                {strategy.category !== GtmStrategyCategory.UNSPECIFIED && <Tag>{categoryLabel(strategy.category)}</Tag>}
              </div>
              {strategy.goalMd && <p className="mt-1.5 max-w-prose whitespace-pre-wrap text-sm text-ink-muted">{strategy.goalMd}</p>}
            </div>
            <Button variant="ghost" size="sm" leftIcon={<Pencil />} onClick={() => setEditing(true)} aria-haspopup="dialog">
              Edit
            </Button>
          </div>
          <div className="mt-4">
            <Fields
              columns={2}
              fields={[
                { label: "Target", value: strategy.targetMetric || "Not set" },
                { label: "Period", value: periodOf(strategy) },
                { label: "Started from", value: strategy.libraryKey ? `The library (${strategy.libraryKey})` : "Written here" },
                { label: "Created by", value: strategy.createdBy || "—" },
              ]}
            />
          </div>
          <section aria-label="Status" className="mt-5 flex flex-wrap items-center gap-2 border-t border-border pt-4" data-testid="gtm-strategy-status">
            {MOVES.filter((m) => m.from.includes(strategy.status)).map((m) => (
              <Button
                key={m.to}
                variant={m.to === GtmStrategyStatus.ACTIVE ? "primary" : "outline"}
                size="sm"
                loading={pending === m.to}
                disabled={update.isPending}
                onClick={() => update.mutate({ orgId, id: strategy.id, expectedRevision: strategy.revision, status: m.to })}
              >
                {m.label}
              </Button>
            ))}
            <PersonOnlyTag />
          </section>
        </CardBody>
      </Card>
      <section aria-labelledby="gtm-strategy-campaigns" className="space-y-2">
        <h3 id="gtm-strategy-campaigns" className="text-sm font-medium text-ink">
          Campaigns ({campaigns.length}
          {q.data?.truncated ? "+" : ""})
        </h3>
        {campaigns.length === 0 ? (
          <p className="text-sm text-ink-muted">No campaigns yet. Create one on the Campaigns page and pick this strategy.</p>
        ) : (
          <CampaignList campaigns={campaigns} />
        )}
        {q.data?.truncated && <p className="text-xs text-ink-muted">Showing the first 200 campaigns.</p>}
      </section>
      {/* Keyed on the revision: when a conflict re-reads the strategy, the form starts again from the current version. */}
      {editing && <StrategyForm key={`${strategy.id}@${strategy.revision}`} strategy={strategy} onClose={() => setEditing(false)} />}
    </div>
  );
}

function CampaignList({ campaigns }: { campaigns: GtmCampaign[] }) {
  return (
    <ul className="divide-y divide-border rounded-lg border border-border bg-surface">
      {campaigns.map((c) => {
        const status = CAMPAIGN_STATUS[c.status];
        return (
          <li key={c.id} className="flex flex-wrap items-center justify-between gap-x-4 gap-y-1 px-3 py-2.5">
            <span className="flex min-w-0 flex-wrap items-center gap-2">
              <Link href={withSearch(gtmPath("campaigns"), { campaign: c.id })} className="font-medium text-ink underline-offset-2 hover:underline">
                {c.name}
              </Link>
              {status && <Badge label={status.label} variant={status.variant} size="sm" />}
            </span>
            {(c.periodStart || c.periodEnd) && (
              <span className="text-xs text-ink-muted">
                {formatDay(c.periodStart)} – {formatDay(c.periodEnd)}
              </span>
            )}
          </li>
        );
      })}
    </ul>
  );
}

const SELECT =
  "h-9 w-full rounded-md border border-border-strong bg-surface px-2.5 text-sm text-ink focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring";

/** Write or edit a strategy. A new one starts as a DRAFT; an edit names the revision it read. */
function StrategyForm({ strategy, onClose, onSaved }: { strategy?: GtmStrategy; onClose: () => void; onSaved?: (id: string) => void }) {
  const orgId = useGtmOrgId();
  const [name, setName] = useState(strategy?.name ?? "");
  const [goal, setGoal] = useState(strategy?.goalMd ?? "");
  const [target, setTarget] = useState(strategy?.targetMetric ?? "");
  const [start, setStart] = useState(strategy?.periodStart ?? "");
  const [end, setEnd] = useState(strategy?.periodEnd ?? "");
  const [category, setCategory] = useState<GtmStrategyCategory>(strategy?.category ?? GtmStrategyCategory.UNSPECIFIED);
  const onError = useGtmWriteError();
  const done = (id: string | undefined) => {
    if (id) onSaved?.(id);
    onClose();
  };
  const createS = useCreateStrategy({ onSuccess: (r) => done(r.strategy?.id), onError });
  const updateS = useUpdateStrategy({ onSuccess: (r) => done(r.strategy?.id), onError });
  const busy = createS.isPending || updateS.isPending;
  const badPeriod = start !== "" && end !== "" && end < start;

  function save() {
    if (!strategy) {
      createS.mutate({
        orgId,
        name: name.trim(),
        goalMd: goal,
        targetMetric: target,
        periodStart: start,
        periodEnd: end,
        ...(category === GtmStrategyCategory.UNSPECIFIED ? {} : { category }),
      });
      return;
    }
    // Only what changed, on the revision this form was opened on. A category
    // set back to "No category" is sent as UNSPECIFIED, which clears it.
    updateS.mutate({
      orgId,
      id: strategy.id,
      expectedRevision: strategy.revision,
      ...(name.trim() !== strategy.name ? { name: name.trim() } : {}),
      ...(goal !== strategy.goalMd ? { goalMd: goal } : {}),
      ...(target !== strategy.targetMetric ? { targetMetric: target } : {}),
      ...(start !== strategy.periodStart ? { periodStart: start } : {}),
      ...(end !== strategy.periodEnd ? { periodEnd: end } : {}),
      ...(category !== strategy.category ? { category } : {}),
    });
  }

  return (
    <Modal
      open
      onClose={onClose}
      size="lg"
      sheetOnMobile
      title={strategy ? "Edit strategy" : "New strategy"}
      description={strategy?.name}
      footer={
        <div className="flex w-full justify-end gap-2">
          <Button variant="secondary" size="sm" onClick={onClose} disabled={busy}>
            Cancel
          </Button>
          <Button variant="primary" size="sm" onClick={save} loading={busy} disabled={name.trim() === "" || badPeriod}>
            {strategy ? "Save" : "Create strategy"}
          </Button>
        </div>
      }
    >
      <div className="space-y-4 text-sm" data-testid="gtm-strategy-form">
        <label className="block space-y-1.5">
          <span className="font-medium text-ink">Name</span>
          <Input value={name} onChange={(e) => setName(e.target.value)} />
        </label>
        <label className="block space-y-1.5">
          <span className="font-medium text-ink">Goal</span>
          <Textarea value={goal} onChange={(e) => setGoal(e.target.value)} rows={3} />
        </label>
        <div className="grid gap-4 sm:grid-cols-2">
          <label className="block space-y-1.5">
            <span className="font-medium text-ink">Target</span>
            <Input value={target} onChange={(e) => setTarget(e.target.value)} placeholder="200 weekly active teams" />
          </label>
          <label className="block space-y-1.5">
            <span className="font-medium text-ink">Category</span>
            <select className={SELECT} value={category} onChange={(e) => setCategory(Number(e.target.value) as GtmStrategyCategory)} aria-label="Category">
              <option value={GtmStrategyCategory.UNSPECIFIED}>No category</option>
              {STRATEGY_CATEGORIES.map((c) => (
                <option key={c.id} value={c.id}>
                  {c.label}
                </option>
              ))}
            </select>
          </label>
          <label className="block space-y-1.5">
            <span className="font-medium text-ink">Starts</span>
            <Input type="date" value={start} onChange={(e) => setStart(e.target.value)} />
          </label>
          <label className="block space-y-1.5">
            <span className="font-medium text-ink">Ends</span>
            <Input type="date" value={end} onChange={(e) => setEnd(e.target.value)} />
          </label>
        </div>
        {badPeriod && <p className="text-danger-ink">The end is before the start.</p>}
        {!strategy && <p className="text-xs text-ink-muted">It starts as a draft. Make it active from its page.</p>}
      </div>
    </Modal>
  );
}
