// Copyright (c) 2026 Reliant Labs

/**
 * The org's strategy library (ListStrategyLibrary): the plays and tactics it
 * imported, to start a strategy from. Reliant ships no entries; each org
 * imports its own (ImportStrategyLibrary), so only the 13 category names are
 * known here.
 *
 * The library is read whole (it is a few hundred entries at most, in pages
 * of 200) and filtered in the page: a chip per category, a search over name
 * and summary, and facets for stage, cost, difficulty and fit, whose values
 * are the library's own (free vocabularies, as imported).
 *
 * "Use this strategy" (UseLibraryStrategy) makes a DRAFT strategy from an
 * entry, and optionally a first DRAFT campaign planned on the org's channels
 * that fit it.
 *
 * Two pieces of library housekeeping sit above it: a PENDING prune request
 * (an import over an access token never removes entries; it stages the
 * removal for a person to confirm or cancel here), and the import history.
 */

import { useQueryClient } from "@tanstack/react-query";
import { Plus, Trash2 } from "lucide-react";
import { useEffect, useMemo, useState } from "react";

import Button from "@/components/forge-ui/button";
import EmptyState from "@/components/forge-ui/empty_state";
import { filterChipClass, FilterSearch } from "@/components/forge-ui/filter_bar";
import Input from "@/components/forge-ui/input";
import Modal from "@/components/forge-ui/modal";
import Mono from "@/components/forge-ui/mono";
import SkeletonLoader from "@/components/forge-ui/skeleton_loader";
import Tag from "@/components/forge-ui/tag";
import ToggleSwitch from "@/components/forge-ui/toggle_switch";
import {
  GtmCredentialKind,
  GtmLibraryDepth,
  GtmPruneRequestStatus,
  type GtmLibraryPruneRequest,
  type GtmStrategyCategory,
  type GtmStrategyLibraryEntry,
} from "@/gen/reliant/v1/gtm_pb";
import {
  useCancelLibraryPrune,
  useConfirmLibraryPrune,
  useListLibraryImports,
  useListLibraryPruneRequests,
  useListStrategyLibraryInfinite,
  useUseLibraryStrategy,
} from "@/hooks/gtm-service-hooks_gen";
import { toast } from "@/lib/toast-manager";

import { useGtmOrgId } from "../shared/GtmShell";
import { PersonOnlyTag } from "../shared/PersonAction";
import { categoryLabel, Fields, formatDay, STRATEGY_CATEGORIES } from "../shared/ui";
import { reportGtmWriteError, useGtmWriteError } from "../shared/writes";

const LIBRARY_PAGE = 200;

interface Facets {
  stage?: string;
  cost?: string;
  difficulty?: string;
  fit?: string;
}

function matches(e: GtmStrategyLibraryEntry, q: string, category: GtmStrategyCategory | null, f: Facets): boolean {
  if (category !== null && e.category !== category) return false;
  if (f.stage && e.stage !== f.stage) return false;
  if (f.cost && e.cost !== f.cost) return false;
  if (f.difficulty && e.difficulty !== f.difficulty) return false;
  if (f.fit && !e.fits.includes(f.fit)) return false;
  const needle = q.trim().toLowerCase();
  return !needle || e.name.toLowerCase().includes(needle) || e.summaryMd.toLowerCase().includes(needle);
}

/** The library's own values for a facet, sorted. */
const valuesOf = (entries: GtmStrategyLibraryEntry[], pick: (e: GtmStrategyLibraryEntry) => string[]) =>
  [...new Set(entries.flatMap(pick).filter(Boolean))].sort((a, b) => a.localeCompare(b));

const CHIP_ON =
  "inline-flex h-8 shrink-0 items-center gap-1.5 whitespace-nowrap rounded-md border border-ink bg-ink px-2.5 text-sm font-medium text-surface transition-colors focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring";

/**
 * A category chip. Drawn here, not with FilterChip: FilterChip's set state is
 * the accent tint, and Reliant's light accent is a near-white grey, so a set
 * chip did not read. A set chip here is the ink fill instead.
 */
function CategoryChip({ on, count, onClick, children }: { on: boolean; count: number; onClick: () => void; children: string }) {
  return (
    <button type="button" aria-pressed={on} onClick={onClick} className={on ? CHIP_ON : filterChipClass(false)}>
      {children}
      <span aria-hidden="true" className={`min-w-[1.25rem] rounded-full px-1.5 text-center text-meta leading-4 tabular-nums ${on ? "bg-surface/20 text-surface" : "bg-ink/5 text-ink-muted"}`}>
        {count}
      </span>
    </button>
  );
}

/** A facet: a native select shaped like the filter controls, its set state the ink edge. */
function FacetSelect({ label, any, value, values, count, onChange }: { label: string; any: string; value: string | undefined; values: string[]; count: (v: string) => number; onChange: (v: string | undefined) => void }) {
  if (values.length === 0) return null;
  return (
    <select
      aria-label={label}
      value={value ?? ""}
      onChange={(e) => onChange(e.target.value || undefined)}
      className={`h-8 rounded-md border bg-surface px-2.5 text-sm text-ink focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring ${value ? "border-ink font-medium" : "border-border-strong"}`}
    >
      <option value="">{any}</option>
      {values.map((v) => (
        <option key={v} value={v}>
          {v} ({count(v)})
        </option>
      ))}
    </select>
  );
}

export function LibraryView({ onStarted }: { onStarted: (strategyId: string) => void }) {
  const orgId = useGtmOrgId();
  const libraryQ = useListStrategyLibraryInfinite({ orgId, pageSize: LIBRARY_PAGE }, { enabled: !!orgId });
  const { hasNextPage, isFetchingNextPage, fetchNextPage } = libraryQ;
  // The whole library, page after page: the filters below work over all of it.
  useEffect(() => {
    if (hasNextPage && !isFetchingNextPage) void fetchNextPage();
  }, [hasNextPage, isFetchingNextPage, fetchNextPage]);
  const library = useMemo(() => libraryQ.data?.pages.flatMap((p) => p.entries) ?? [], [libraryQ.data]);

  const [q, setQ] = useState("");
  const [category, setCategory] = useState<GtmStrategyCategory | null>(null);
  const [facets, setFacets] = useState<Facets>({});
  const [using, setUsing] = useState<GtmStrategyLibraryEntry | null>(null);
  const rows = useMemo(() => library.filter((e) => matches(e, q, category, facets)), [library, q, category, facets]);
  const inCategory = (c: GtmStrategyCategory | null) => library.filter((e) => matches(e, q, c, facets)).length;
  const facetCount = (key: keyof Facets, value: string) => library.filter((e) => matches(e, q, category, { ...facets, [key]: value })).length;
  const active = Object.values(facets).filter(Boolean).length + (q ? 1 : 0) + (category !== null ? 1 : 0);
  const facetValues = useMemo(
    () => ({
      stage: valuesOf(library, (e) => [e.stage]),
      cost: valuesOf(library, (e) => [e.cost]),
      difficulty: valuesOf(library, (e) => [e.difficulty]),
      fit: valuesOf(library, (e) => e.fits),
    }),
    [library],
  );

  return (
    <div className="space-y-4" data-testid="gtm-library">
      <PruneRequests />
      <p className="text-sm text-ink-muted">Plays and tactics to start a strategy from: your org&apos;s own library, as it imported it.</p>
      {/* One scrolling row on a phone: thirteen wrapped chips would push the entries off the first screen. */}
      <div className="flex gap-2 overflow-x-auto pb-1 sm:flex-wrap sm:overflow-visible sm:pb-0" role="group" aria-label="Category">
        <CategoryChip on={category === null} onClick={() => setCategory(null)} count={inCategory(null)}>
          All categories
        </CategoryChip>
        {STRATEGY_CATEGORIES.map((c) => (
          <CategoryChip key={c.id} on={category === c.id} onClick={() => setCategory(category === c.id ? null : c.id)} count={inCategory(c.id)}>
            {c.label}
          </CategoryChip>
        ))}
      </div>
      <div className="flex flex-wrap items-center gap-2" role="group" aria-label="Filter the library">
        <FilterSearch value={q} onChange={setQ} placeholder="Search plays and tactics" className="w-full sm:w-64" />
        <FacetSelect label="Stage" any="Any stage" value={facets.stage} values={facetValues.stage} count={(v) => facetCount("stage", v)} onChange={(v) => setFacets({ ...facets, stage: v })} />
        <FacetSelect label="Cost" any="Any cost" value={facets.cost} values={facetValues.cost} count={(v) => facetCount("cost", v)} onChange={(v) => setFacets({ ...facets, cost: v })} />
        <FacetSelect
          label="Difficulty"
          any="Any difficulty"
          value={facets.difficulty}
          values={facetValues.difficulty}
          count={(v) => facetCount("difficulty", v)}
          onChange={(v) => setFacets({ ...facets, difficulty: v })}
        />
        <FacetSelect label="Fits" any="Fits anyone" value={facets.fit} values={facetValues.fit} count={(v) => facetCount("fit", v)} onChange={(v) => setFacets({ ...facets, fit: v })} />
        {active > 0 && (
          <Button
            variant="ghost"
            size="sm"
            onClick={() => {
              setQ("");
              setCategory(null);
              setFacets({});
            }}
          >
            Clear filters ({active})
          </Button>
        )}
      </div>
      {libraryQ.isPending ? (
        <SkeletonLoader variant="card" count={2} />
      ) : libraryQ.isError ? (
        <EmptyState title="Couldn't load the library" description="Something went wrong reading it." actionLabel="Try again" onAction={() => void libraryQ.refetch()} />
      ) : library.length === 0 ? (
        <EmptyState title="No library yet" description="Your org has not imported a strategy library. A person imports one with an access token; it shows here." />
      ) : (
        <>
          <p className="text-xs text-ink-muted" aria-live="polite">
            {rows.length} of {library.length} entries
          </p>
          {rows.length === 0 ? (
            <EmptyState title="Nothing matches" description="Try another category, or clear the filters." />
          ) : (
            <ul className="grid gap-3 md:grid-cols-2">
              {rows.map((e) => (
                <li key={e.key}>
                  <LibraryCard entry={e} onUse={() => setUsing(e)} />
                </li>
              ))}
            </ul>
          )}
        </>
      )}
      <ImportHistory />
      {using && <UseStrategyDialog entry={using} onClose={() => setUsing(null)} onStarted={onStarted} />}
    </div>
  );
}

function LibraryCard({ entry, onUse }: { entry: GtmStrategyLibraryEntry; onUse: () => void }) {
  const facts = [entry.hours ? `~${entry.hours} h` : "", entry.northStar].filter(Boolean).join(" · ");
  return (
    <article className="flex h-full flex-col rounded-lg border border-border bg-surface p-4" aria-label={entry.name}>
      <div className="flex flex-wrap items-center gap-2 text-xs text-ink-muted">
        <span>{categoryLabel(entry.category)}</span>
        <span aria-hidden="true">·</span>
        <span>{entry.depth === GtmLibraryDepth.PLAY ? "Play" : "Tactic"}</span>
      </div>
      <h3 className="mt-1 text-sm font-semibold text-ink">{entry.name}</h3>
      {entry.summaryMd && <p className="mt-1 line-clamp-4 whitespace-pre-wrap text-sm text-ink-muted">{entry.summaryMd}</p>}
      <div className="mt-3 flex flex-wrap gap-1.5">
        {[entry.stage, entry.cost, entry.difficulty, ...entry.fits].filter(Boolean).map((t, i) => (
          <Tag key={`${t}-${i}`}>{t}</Tag>
        ))}
      </div>
      <div className="mt-auto flex flex-wrap items-center justify-between gap-2 pt-4">
        <span className="text-xs text-ink-muted">{facts}</span>
        <Button variant="outline" size="sm" leftIcon={<Plus />} onClick={onUse} aria-haspopup="dialog" aria-label={`Use this strategy: ${entry.name}`}>
          Use this strategy
        </Button>
      </div>
    </article>
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
            onClick={() =>
              use.mutate({ orgId, libraryKey: entry.key, createFirstCampaign: withCampaign, ...(withCampaign ? { campaignName: campaignName.trim() } : {}) })
            }
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
            ...(entry.northStar ? [{ label: "Goal", value: entry.northStar }] : []),
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

// ── Library housekeeping ──────────────────────────────────────────────────

/**
 * PENDING prune requests: an import over an access token asked to remove
 * entries it no longer has, and only a person (signed in here) may confirm
 * it. Confirming removes the listed keys that are still in the library and
 * were not imported again since; cancelling removes nothing.
 */
function PruneRequests() {
  const orgId = useGtmOrgId();
  const q = useListLibraryPruneRequests({ orgId, status: GtmPruneRequestStatus.PENDING, pageSize: 20 }, { enabled: !!orgId });
  const pending = q.data?.requests ?? [];
  if (pending.length === 0) return null;
  return (
    <div className="space-y-3" data-testid="gtm-prune-requests">
      {pending.map((r) => (
        <PruneBanner key={r.id} request={r} />
      ))}
    </div>
  );
}

function PruneBanner({ request }: { request: GtmLibraryPruneRequest }) {
  const orgId = useGtmOrgId();
  const queryClient = useQueryClient();
  const [showAll, setShowAll] = useState(false);
  const onError = (e: unknown) => reportGtmWriteError(queryClient, e);
  const confirm = useConfirmLibraryPrune({
    onSuccess: (r) => toast.success(`Removed ${r.removed} ${r.removed === 1 ? "entry" : "entries"} from the library.`),
    onError,
  });
  const cancel = useCancelLibraryPrune({ onSuccess: () => toast.success("Prune cancelled. Nothing was removed."), onError });
  const keys = showAll ? request.removeKeys : request.removeKeys.slice(0, 8);
  const busy = confirm.isPending || cancel.isPending;
  return (
    <section aria-labelledby={`gtm-prune-${request.id}`} className="rounded-lg border border-warning/40 bg-warning-surface p-4 text-sm" data-testid="gtm-prune-banner">
      <div className="flex flex-wrap items-center gap-2">
        <Trash2 className="size-4 text-warning-ink" aria-hidden="true" />
        <h3 id={`gtm-prune-${request.id}`} className="font-medium text-ink">
          An import wants to remove {request.removeKeys.length} {request.removeKeys.length === 1 ? "entry" : "entries"} from the library
        </h3>
        <PersonOnlyTag />
      </div>
      <p className="mt-1 text-ink-muted">
        {request.credentialKind === GtmCredentialKind.TOKEN ? "An import through an access token" : "An import"}
        {request.sourceRevision ? ` of ${request.sourceRevision}` : ""} no longer has these entries. Access tokens never remove entries themselves, so it
        waits for you{request.expiresAt ? `, until ${formatDay(request.expiresAt)}` : ""}. Entries imported again since are kept.
      </p>
      <ul className="mt-2 flex flex-wrap gap-1.5" aria-label="Keys to remove">
        {keys.map((k) => (
          <li key={k}>
            <Mono value={k} copy={false} className="rounded bg-surface px-1.5 py-0.5 text-xs text-ink" />
          </li>
        ))}
        {!showAll && request.removeKeys.length > keys.length && (
          <li>
            <Button variant="ghost" size="xs" onClick={() => setShowAll(true)}>
              and {request.removeKeys.length - keys.length} more
            </Button>
          </li>
        )}
      </ul>
      <div className="mt-3 flex flex-wrap gap-2">
        <Button variant="danger" size="sm" loading={confirm.isPending} disabled={busy} onClick={() => confirm.mutate({ orgId, requestId: request.id })}>
          Remove them
        </Button>
        <Button variant="secondary" size="sm" loading={cancel.isPending} disabled={busy} onClick={() => cancel.mutate({ orgId, requestId: request.id })}>
          Keep them
        </Button>
      </div>
    </section>
  );
}

const CREDENTIAL: Record<number, string> = { [GtmCredentialKind.PERSON]: "Signed in", [GtmCredentialKind.TOKEN]: "Access token" };

/** The last imports (ListLibraryImports): when, by whom and through what, from which revision, and what changed. */
function ImportHistory() {
  const orgId = useGtmOrgId();
  const q = useListLibraryImports({ orgId, pageSize: 10 }, { enabled: !!orgId });
  const imports = q.data?.imports ?? [];
  if (imports.length === 0) return null;
  return (
    <section aria-labelledby="gtm-imports" className="space-y-2" data-testid="gtm-import-history">
      <h3 id="gtm-imports" className="text-sm font-medium text-ink">
        Imports
      </h3>
      <ul className="divide-y divide-border rounded-lg border border-border bg-surface text-sm">
        {imports.map((i) => {
          const changes = [
            i.created ? `${i.created} new` : "",
            i.updated ? `${i.updated} updated` : "",
            i.unchanged ? `${i.unchanged} unchanged` : "",
            i.pruned ? `${i.pruned} removed` : "",
            i.pruneRequestId ? "a removal waiting for a person" : "",
          ].filter(Boolean);
          return (
            <li key={i.id} className="flex flex-wrap items-center justify-between gap-x-4 gap-y-1 px-3 py-2">
              <span className="flex min-w-0 flex-wrap items-center gap-2">
                <span className="text-ink">{formatDay(i.createdAt)}</span>
                <Tag>{CREDENTIAL[i.credentialKind] ?? "Unknown"}</Tag>
                {i.sourceRevision && <Mono value={i.sourceRevision.slice(0, 12)} copy={false} className="text-xs text-ink-muted" />}
              </span>
              <span className="text-xs text-ink-muted">{changes.join(" · ") || "no changes"}</span>
            </li>
          );
        })}
      </ul>
    </section>
  );
}
