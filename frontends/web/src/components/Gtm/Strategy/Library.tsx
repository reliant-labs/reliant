// Copyright (c) 2026 Reliant Labs

/**
 * The strategy library (ListStrategyLibrary): a bank of generic plays built
 * into Reliant and the same for every org, read-only. Each is a jumping-off
 * point: what it is, why it works, the questions that tailor it to a
 * business, and optionally a tool-agnostic automation that carries it out.
 *
 * It opens on what fits the org's business (fits_my_business: the business
 * type and models on Positioning), with "Show all" beside it, and filters by
 * business model, business type, category, stage, effort, cost and a search;
 * the service filters, in pages of 50. When the org has no business type or
 * models yet, a hint links to Positioning.
 *
 * An entry opens in a dialog (`?entry=<key>`, gtmLibraryPath). "Use this
 * strategy" (UseLibraryStrategy) makes a DRAFT strategy from it, and
 * optionally a first DRAFT campaign planned on the org's channels that fit.
 */

import { Code } from "@connectrpc/connect";
import { Info, Plus } from "lucide-react";
import { useEffect, useMemo, useState } from "react";

import Button from "@/components/forge-ui/button";
import EmptyState from "@/components/forge-ui/empty_state";
import { filterChipClass, FilterSearch } from "@/components/forge-ui/filter_bar";
import Input from "@/components/forge-ui/input";
import Link from "@/components/forge-ui/link";
import Modal from "@/components/forge-ui/modal";
import SkeletonLoader from "@/components/forge-ui/skeleton_loader";
import Tag from "@/components/forge-ui/tag";
import ToggleSwitch from "@/components/forge-ui/toggle_switch";
import { GtmBusinessType, type GtmBusinessModel, type GtmStrategyCategory, type GtmStrategyLibraryEntry } from "@/gen/reliant/v1/gtm_pb";
import { useGetPositioning, useGetStrategyLibraryEntry, useListStrategyLibraryInfinite, useUseLibraryStrategy } from "@/hooks/gtm-service-hooks_gen";
import { gtmPath } from "@/lib/paths";

import { useGtmOrgId } from "../shared/GtmShell";
import { SuggestedWorkflow } from "../shared/SuggestedWorkflow";
import {
  BUSINESS_MODELS,
  BUSINESS_TYPES,
  businessModelLabel,
  businessTypeLabel,
  categoryLabel,
  Fields,
  LIBRARY_COSTS,
  LIBRARY_EFFORTS,
  LIBRARY_OBJECTIVES,
  LIBRARY_STAGES,
  STRATEGY_CATEGORIES,
  vocabLabel,
} from "../shared/ui";
import { useGtmWriteError } from "../shared/writes";

const LIBRARY_PAGE = 50;

const SELECT =
  "h-8 rounded-md border bg-surface px-2.5 text-sm text-ink focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring";

const CHIP_ON =
  "inline-flex h-8 shrink-0 items-center gap-1.5 whitespace-nowrap rounded-md border border-ink bg-ink px-2.5 text-sm font-medium text-surface transition-colors focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring";

/**
 * A toggle chip. Drawn here, not with FilterChip: FilterChip's set state is
 * the accent tint, and Reliant's light accent is a near-white grey, so a set
 * chip did not read. A set chip here is the ink fill instead.
 */
function Chip({ on, onClick, children }: { on: boolean; onClick: () => void; children: string }) {
  return (
    <button type="button" aria-pressed={on} onClick={onClick} className={on ? CHIP_ON : filterChipClass(false)}>
      {children}
    </button>
  );
}

/** A pick-one filter: a native select, its set state the ink edge. */
function FilterSelect({ label, any, value, options, onChange }: { label: string; any: string; value: string; options: readonly { id: string; label: string }[]; onChange: (v: string) => void }) {
  return (
    <select aria-label={label} value={value} onChange={(e) => onChange(e.target.value)} className={`${SELECT} ${value ? "border-ink font-medium" : "border-border-strong"}`}>
      <option value="">{any}</option>
      {options.map((o) => (
        <option key={o.id} value={o.id}>
          {label}: {o.label}
        </option>
      ))}
    </select>
  );
}

/** An entry's facets, labelled: "Stage: Launch, Growth", "Effort: Low", "Cost: Free". */
export function entryFacets(entry: Pick<GtmStrategyLibraryEntry, "stages" | "effort" | "cost">): string[] {
  return [
    entry.stages.length > 0 && `Stage: ${entry.stages.map((s) => vocabLabel(LIBRARY_STAGES, s)).join(", ")}`,
    entry.effort && `Effort: ${vocabLabel(LIBRARY_EFFORTS, entry.effort)}`,
    entry.cost && `Cost: ${vocabLabel(LIBRARY_COSTS, entry.cost)}`,
  ].filter(Boolean) as string[];
}

/** Who an entry fits: "Any business", or its types and models. */
export function entryFits(entry: Pick<GtmStrategyLibraryEntry, "businessTypes" | "businessModels">): string {
  const types = entry.businessTypes.map(businessTypeLabel);
  const models = entry.businessModels.map(businessModelLabel);
  if (types.length === 0 && models.length === 0) return "Any business";
  return [types.length ? types.join(", ") : "Any type", models.length ? models.join(", ") : "any model"].join(" · ");
}

interface Filters {
  categories: GtmStrategyCategory[];
  businessModels: GtmBusinessModel[];
  businessType: string;
  stage: string;
  effort: string;
  cost: string;
  query: string;
}

const NO_FILTERS: Filters = { categories: [], businessModels: [], businessType: "", stage: "", effort: "", cost: "", query: "" };

const toggle = <T,>(list: T[], v: T) => (list.includes(v) ? list.filter((x) => x !== v) : [...list, v]);

function useDebounced<T>(value: T, ms: number): T {
  const [settled, setSettled] = useState(value);
  useEffect(() => {
    const t = setTimeout(() => setSettled(value), ms);
    return () => clearTimeout(t);
  }, [value, ms]);
  return settled;
}

export function LibraryView({
  onStarted,
  entryKey,
  onOpenEntry,
  onCloseEntry,
}: {
  onStarted: (strategyId: string) => void;
  /** The entry open in the detail dialog (`?entry=`); "" for none. */
  entryKey: string;
  onOpenEntry: (key: string) => void;
  onCloseEntry: () => void;
}) {
  const orgId = useGtmOrgId();
  const [fits, setFits] = useState(true);
  const [filters, setFilters] = useState<Filters>(NO_FILTERS);
  const set = (patch: Partial<Filters>) => setFilters((f) => ({ ...f, ...patch }));
  const query = useDebounced(filters.query.trim(), 300);
  const [using, setUsing] = useState<GtmStrategyLibraryEntry | null>(null);

  const positioningQ = useGetPositioning({ orgId }, { enabled: !!orgId });
  const profile = positioningQ.data?.positioning;
  const noProfile =
    !positioningQ.isPending &&
    (positioningQ.isError ? positioningQ.error.code === Code.NotFound : (profile?.businessType ?? GtmBusinessType.UNSPECIFIED) === GtmBusinessType.UNSPECIFIED && (profile?.businessModels.length ?? 0) === 0);

  const libraryQ = useListStrategyLibraryInfinite(
    {
      orgId,
      categories: filters.categories,
      businessTypes: filters.businessType ? [Number(filters.businessType) as GtmBusinessType] : [],
      businessModels: filters.businessModels,
      ...(filters.stage ? { stage: filters.stage } : {}),
      ...(filters.effort ? { effort: filters.effort } : {}),
      ...(filters.cost ? { cost: filters.cost } : {}),
      ...(query ? { query } : {}),
      fitsMyBusiness: fits,
      pageSize: LIBRARY_PAGE,
    },
    { enabled: !!orgId },
  );
  const entries = useMemo(() => libraryQ.data?.pages.flatMap((p) => p.entries) ?? [], [libraryQ.data]);
  const active =
    filters.categories.length + filters.businessModels.length + [filters.businessType, filters.stage, filters.effort, filters.cost, filters.query.trim()].filter(Boolean).length;

  return (
    <div className="space-y-4" data-testid="gtm-library">
      <p className="text-sm text-ink-muted">Plays to start a strategy from. Each is a starting point to tailor to your business, not a script.</p>
      <div className="flex flex-wrap items-center gap-2" role="group" aria-label="Which entries">
        <Chip on={fits} onClick={() => setFits(true)}>
          Fits my business
        </Chip>
        <Chip on={!fits} onClick={() => setFits(false)}>
          Show all
        </Chip>
      </div>
      {noProfile && (
        <p className="flex items-start gap-2 rounded-lg border border-border bg-surface-muted px-3 py-2 text-sm text-ink-muted" data-testid="gtm-library-profile-hint">
          <Info className="mt-0.5 size-4 shrink-0" aria-hidden="true" />
          <span>
            Your business type and models are not set, so every entry counts as a fit.{" "}
            <Link href={gtmPath("positioning")} className="font-medium text-ink underline underline-offset-2">
              Set them on Positioning
            </Link>
            .
          </span>
        </p>
      )}
      {/* One scrolling row on a phone: thirteen wrapped chips would push the entries off the first screen. */}
      <div className="flex gap-2 overflow-x-auto pb-1 sm:flex-wrap sm:overflow-visible sm:pb-0" role="group" aria-label="Category">
        {STRATEGY_CATEGORIES.map((c) => (
          <Chip key={c.id} on={filters.categories.includes(c.id)} onClick={() => set({ categories: toggle(filters.categories, c.id) })}>
            {c.label}
          </Chip>
        ))}
      </div>
      <div className="flex gap-2 overflow-x-auto pb-1 sm:flex-wrap sm:overflow-visible sm:pb-0" role="group" aria-label="Business model">
        {BUSINESS_MODELS.map((m) => (
          <Chip key={m.id} on={filters.businessModels.includes(m.id)} onClick={() => set({ businessModels: toggle(filters.businessModels, m.id) })}>
            {m.label}
          </Chip>
        ))}
      </div>
      <div className="flex flex-wrap items-center gap-2" role="group" aria-label="Filter the library">
        <FilterSearch value={filters.query} onChange={(q) => set({ query: q })} placeholder="Search the library" className="w-full sm:w-64" />
        <FilterSelect
          label="Business type"
          any="Any business type"
          value={filters.businessType}
          options={BUSINESS_TYPES.map((t) => ({ id: String(t.id), label: t.label }))}
          onChange={(v) => set({ businessType: v })}
        />
        <FilterSelect label="Stage" any="Any stage" value={filters.stage} options={LIBRARY_STAGES} onChange={(v) => set({ stage: v })} />
        <FilterSelect label="Effort" any="Any effort" value={filters.effort} options={LIBRARY_EFFORTS} onChange={(v) => set({ effort: v })} />
        <FilterSelect label="Cost" any="Any cost" value={filters.cost} options={LIBRARY_COSTS} onChange={(v) => set({ cost: v })} />
        {active > 0 && (
          <Button variant="ghost" size="sm" onClick={() => setFilters(NO_FILTERS)}>
            Clear filters ({active})
          </Button>
        )}
      </div>
      {libraryQ.isPending ? (
        <SkeletonLoader variant="card" count={2} />
      ) : libraryQ.isError && entries.length === 0 ? (
        <EmptyState title="Couldn't load the library" description="Something went wrong reading it." actionLabel="Try again" onAction={() => void libraryQ.refetch()} />
      ) : entries.length === 0 ? (
        <EmptyState
          title="Nothing matches"
          description={fits ? "Nothing that fits your business matches these filters. Show all, or clear the filters." : "Try another category, or clear the filters."}
        />
      ) : (
        <>
          <p className="text-xs text-ink-muted" aria-live="polite">
            {entries.length}
            {libraryQ.hasNextPage ? "+" : ""} {entries.length === 1 ? "entry" : "entries"}
          </p>
          <ul className="grid gap-3 md:grid-cols-2">
            {entries.map((e) => (
              <li key={e.key}>
                <LibraryCard entry={e} onOpen={() => onOpenEntry(e.key)} onUse={() => setUsing(e)} />
              </li>
            ))}
          </ul>
          {libraryQ.isFetchNextPageError && (
            <p className="text-sm text-ink-muted" role="alert" data-testid="gtm-library-partial">
              The next entries could not be read.
            </p>
          )}
          {libraryQ.hasNextPage && (
            <div className="flex justify-center">
              <Button variant="outline" size="sm" loading={libraryQ.isFetchingNextPage} onClick={() => void libraryQ.fetchNextPage()}>
                {libraryQ.isFetchNextPageError ? "Try again" : "Show more"}
              </Button>
            </div>
          )}
        </>
      )}
      {entryKey && <EntryDialog entryKey={entryKey} onClose={onCloseEntry} onUse={(e) => setUsing(e)} />}
      {using && <UseStrategyDialog entry={using} onClose={() => setUsing(null)} onStarted={onStarted} />}
    </div>
  );
}

function LibraryCard({ entry, onOpen, onUse }: { entry: GtmStrategyLibraryEntry; onOpen: () => void; onUse: () => void }) {
  return (
    <article className="flex h-full flex-col rounded-lg border border-border bg-surface p-4" aria-label={entry.name}>
      <div className="flex flex-wrap items-center gap-2 text-xs text-ink-muted">
        <span>{categoryLabel(entry.category)}</span>
        {entry.objective && (
          <>
            <span aria-hidden="true">·</span>
            <span>{vocabLabel(LIBRARY_OBJECTIVES, entry.objective)}</span>
          </>
        )}
      </div>
      <h3 className="mt-1 text-sm font-semibold text-ink">{entry.name}</h3>
      {entry.summaryMd && <p className="mt-1 line-clamp-3 whitespace-pre-wrap text-sm text-ink-muted">{entry.summaryMd}</p>}
      <div className="mt-3 flex flex-wrap gap-1.5">
        {entryFacets(entry).map((t) => (
          <Tag key={t}>{t}</Tag>
        ))}
      </div>
      <p className="mt-2 text-xs text-ink-muted">Fits: {entryFits(entry)}</p>
      <div className="mt-auto flex flex-wrap items-center justify-end gap-2 pt-4">
        <Button variant="ghost" size="sm" onClick={onOpen} aria-haspopup="dialog" aria-label={`Details: ${entry.name}`}>
          Details
        </Button>
        <Button variant="outline" size="sm" leftIcon={<Plus />} onClick={onUse} aria-haspopup="dialog" aria-label={`Use this strategy: ${entry.name}`}>
          Use this strategy
        </Button>
      </div>
    </article>
  );
}

/** One entry, whole (GetStrategyLibraryEntry): what it is, why it works, the questions that tailor it, its suggested workflow. */
function EntryDialog({ entryKey, onClose, onUse }: { entryKey: string; onClose: () => void; onUse: (entry: GtmStrategyLibraryEntry) => void }) {
  const orgId = useGtmOrgId();
  const q = useGetStrategyLibraryEntry({ orgId, key: entryKey }, { enabled: !!orgId });
  const entry = q.data?.entry;
  return (
    <Modal
      open
      onClose={onClose}
      size="lg"
      sheetOnMobile
      title={entry?.name ?? "Library entry"}
      description={entry ? categoryLabel(entry.category) : undefined}
      footer={
        <div className="flex w-full justify-end gap-2">
          <Button variant="secondary" size="sm" onClick={onClose}>
            Close
          </Button>
          {entry && (
            <Button variant="primary" size="sm" leftIcon={<Plus />} onClick={() => onUse(entry)} aria-haspopup="dialog">
              Use this strategy
            </Button>
          )}
        </div>
      }
    >
      <div className="space-y-5 text-sm" data-testid="gtm-library-entry">
        {q.isPending ? (
          <SkeletonLoader variant="text" count={4} />
        ) : !entry ? (
          <p className="text-ink-muted">{q.isError && q.error.code !== Code.NotFound ? "This entry could not be read." : `The library no longer has "${entryKey}".`}</p>
        ) : (
          <>
            {entry.summaryMd && <p className="whitespace-pre-wrap text-ink">{entry.summaryMd}</p>}
            {entry.whyItWorksMd && (
              <section aria-label="Why it works" className="space-y-1.5">
                <h3 className="font-medium text-ink">Why it works</h3>
                <p className="whitespace-pre-wrap text-ink-muted">{entry.whyItWorksMd}</p>
              </section>
            )}
            {entry.tailorQuestions.length > 0 && (
              <section aria-label="Questions to tailor it" className="space-y-1.5">
                <h3 className="font-medium text-ink">Questions to tailor it</h3>
                <ol className="list-decimal space-y-1 pl-5 text-ink">
                  {entry.tailorQuestions.map((t) => (
                    <li key={t}>{t}</li>
                  ))}
                </ol>
              </section>
            )}
            <Fields
              columns={2}
              fields={[
                ...(entry.objective ? [{ label: "Objective", value: vocabLabel(LIBRARY_OBJECTIVES, entry.objective) }] : []),
                { label: "Stage", value: entry.stages.length ? entry.stages.map((s) => vocabLabel(LIBRARY_STAGES, s)).join(", ") : "Any" },
                ...(entry.effort ? [{ label: "Effort", value: vocabLabel(LIBRARY_EFFORTS, entry.effort) }] : []),
                ...(entry.cost ? [{ label: "Cost", value: vocabLabel(LIBRARY_COSTS, entry.cost) }] : []),
                ...(entry.signalDays ? [{ label: "First signal", value: `In about ${entry.signalDays} ${entry.signalDays === 1 ? "day" : "days"}` }] : []),
                { label: "Fits", value: entryFits(entry) },
                ...(entry.kpis.length ? [{ label: "Measure", value: entry.kpis.join(", ") }] : []),
                ...(entry.channelTypes.length ? [{ label: "Channel types", value: entry.channelTypes.join(", ") }] : []),
              ]}
            />
            <SuggestedWorkflow text={entry.suggestedWorkflowMd} />
          </>
        )}
      </div>
    </Modal>
  );
}

/** "Use this strategy": a DRAFT strategy from the entry, and optionally a first DRAFT campaign. */
function UseStrategyDialog({ entry, onClose, onStarted }: { entry: GtmStrategyLibraryEntry; onClose: () => void; onStarted: (strategyId: string) => void }) {
  const orgId = useGtmOrgId();
  const [withCampaign, setWithCampaign] = useState(true);
  const [campaignName, setCampaignName] = useState(entry.name);
  const onError = useGtmWriteError();
  const use = useUseLibraryStrategy({
    onSuccess: (r) => {
      onClose();
      if (r.strategy) onStarted(r.strategy.id);
    },
    onError,
  });
  return (
    <Modal
      open
      onClose={onClose}
      size="md"
      sheetOnMobile
      title="Use this strategy"
      description={entry.name}
      footer={
        <div className="flex w-full justify-end gap-2">
          <Button variant="secondary" size="sm" onClick={onClose} disabled={use.isPending}>
            Cancel
          </Button>
          <Button
            variant="primary"
            size="sm"
            loading={use.isPending}
            disabled={withCampaign && !campaignName.trim()}
            onClick={() => use.mutate({ orgId, libraryKey: entry.key, createFirstCampaign: withCampaign, ...(withCampaign ? { campaignName: campaignName.trim() } : {}) })}
          >
            Create strategy
          </Button>
        </div>
      }
    >
      <div className="space-y-4 text-sm" data-testid="gtm-use-strategy-dialog">
        <Fields
          fields={[
            { label: "Category", value: categoryLabel(entry.category) },
            ...(entry.kpis.length ? [{ label: "Target", value: entry.kpis.join(", ") }] : []),
          ]}
        />
        <div className="rounded-lg border border-border p-3">
          <ToggleSwitch checked={withCampaign} onChange={setWithCampaign} label="Also create a first campaign" />
          {withCampaign && (
            <div className="mt-3 space-y-2">
              <label htmlFor="gtm-first-campaign" className="block font-medium text-ink">
                Campaign name
              </label>
              <Input id="gtm-first-campaign" value={campaignName} onChange={(e) => setCampaignName(e.target.value)} />
              <p className="text-xs text-ink-muted">It is planned on your active channels that fit this entry.</p>
            </div>
          )}
        </div>
        <p className="text-xs text-ink-muted">Both start as drafts. Nothing is published or sent.</p>
      </div>
    </Modal>
  );
}
