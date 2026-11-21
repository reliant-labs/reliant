// Copyright (c) 2026 Reliant Labs

/**
 * ALL DASHBOARDS AS A GALLERY (U6e, prototype): the project's dashboards
 * page as a grid of cards, one per dashboard (forge's hubs one card each).
 *
 *   toolbar   search, and the source filter: All · Built-in · From repo ·
 *             Custom, each with its count. Both are the URL's (`q`, `source`).
 *   card      the title, a line of description, the source badge, a mini
 *             preview (the first preview panel the index carries, run through
 *             QueryPanel as a sparkline once the card nears the viewport),
 *             when the caller last viewed it (the index's per-caller time;
 *             nothing when never viewed), the panel count and the star. The
 *             whole card opens the dashboard.
 *   + card    "New dashboard": the same flow as the header's.
 *
 * A dashboard with no preview panel (a text-only one, an organization view)
 * says so in the preview slot rather than drawing an empty chart.
 */

import { useMemo } from "react";
import { Plus, Search } from "lucide-react";

import Link from "@/components/forge-ui/link";
import { useObservabilityOrg, usePanelQuery } from "@/hooks/observability-queries";
import type { DashboardRef, DashboardSummary } from "@/services/controlPlane/observability/dashboardTypes";
import type { ObsScope, Panel } from "@/services/controlPlane/observability/types";
import { cn } from "@/lib/utils";

import { FIELD_BARE, FIELD_SHELL, SEGMENTED, segmentTone } from "../../controlStyles";
import { UPlotChart } from "../charts/UPlotChart";
import { formatAgo } from "../format";
import { alignTo, unionTimestamps } from "./seriesModel";
import { displayTitle, type LandingCard } from "./dashboardHubs";
import { GALLERY_BADGE_LABELS, galleryBadgeOf, type GalleryBadge, type SourceFilter } from "./dashboardModel";
import { StarButton } from "./parts";
import { useNearViewport } from "./useNearViewport";

export interface GalleryItem {
  key: string;
  title: string;
  description: string;
  /** The dashboard the card opens and previews (a hub's first tab). */
  dashboard: DashboardSummary;
  /** A hub's tabs, named under the description. */
  tabs?: string[];
  /** What opening it needs, said in the card's footer (an organization view's billing permission). */
  note?: string;
  /** The card's test id suffix: the hub's id, or the dashboard's key. */
  id: string;
}

/** Every card of the gallery: the landing's hubs and provided dashboards, then the team's own, or the search results. */
export function galleryItems(cards: readonly LandingCard[], own: readonly DashboardSummary[], about: (d: DashboardSummary) => string, project: string, noteOf: (d: DashboardSummary) => string | undefined = () => undefined): GalleryItem[] {
  const fromCard = (card: LandingCard): GalleryItem =>
    card.kind === "hub"
      ? {
          key: `hub:${card.id}`,
          id: card.id,
          title: card.hub.title,
          description: card.hub.about,
          dashboard: card.hub.tabs[0]!.summary,
          tabs: card.hub.tabs.map((tab) => tab.label),
        }
      : { key: card.id, id: card.dashboard.ref.key, title: card.title, description: about(card.dashboard), dashboard: card.dashboard, note: noteOf(card.dashboard) };
  return [...cards.map(fromCard), ...own.map((d) => itemOf(d, about, project, noteOf))];
}

export function itemOf(d: DashboardSummary, about: (d: DashboardSummary) => string, project: string, noteOf: (d: DashboardSummary) => string | undefined = () => undefined): GalleryItem {
  return { key: `${d.ref.kind}/${d.ref.projectId}/${d.ref.key}`, id: d.ref.key, title: displayTitle(d.title, project), description: about(d), dashboard: d, note: noteOf(d) };
}

const FILTERS: { value: GalleryBadge | undefined; label: string }[] = [
  { value: undefined, label: "All" },
  { value: "builtin", label: GALLERY_BADGE_LABELS.builtin },
  { value: "repo", label: GALLERY_BADGE_LABELS.repo },
  { value: "custom", label: GALLERY_BADGE_LABELS.custom },
];

export function GalleryToolbar({
  q,
  source,
  counts,
  onSearch,
}: {
  q: string | undefined;
  source: SourceFilter | undefined;
  /** By badge, over the search's matches: what each filter would show. */
  counts: Record<GalleryBadge | "all", number>;
  onSearch: (next: { q?: string; source?: SourceFilter }) => void;
}) {
  return (
    <div className="flex flex-wrap items-center gap-3" data-testid="dashboards-gallery-toolbar">
      <label className={cn(FIELD_SHELL, "w-full sm:w-auto sm:min-w-80")}>
        <Search className="h-3.5 w-3.5 shrink-0" aria-hidden="true" />
        <span className="sr-only">Search dashboards</span>
        <input
          value={q ?? ""}
          onChange={(event) => onSearch({ q: event.target.value || undefined })}
          placeholder="Search dashboards, services, tags"
          className={FIELD_BARE}
          data-testid="dashboards-search"
        />
      </label>
      <div role="radiogroup" aria-label="Source" className={SEGMENTED} data-testid="dashboards-source-filter">
        {FILTERS.map(({ value, label }) => {
          const on = (source === "builtin" || source === "repo" || source === "custom" ? source : undefined) === value;
          return (
            <button
              key={label}
              type="button"
              role="radio"
              aria-checked={on}
              onClick={() => onSearch({ source: value })}
              className={cn("inline-flex h-7 items-center gap-1.5 rounded-md px-2.5 text-ui", segmentTone(on))}
              data-testid={`dashboards-source-${value ?? "all"}`}
            >
              {label}
              <span className="text-meta tabular-nums text-ink-muted">{counts[value ?? "all"]}</span>
            </button>
          );
        })}
      </div>
    </div>
  );
}

export function GalleryGrid({
  items,
  project,
  envIds,
  href,
  onStar,
  onNew,
  canWrite,
  writeHint,
}: {
  items: GalleryItem[];
  project: string;
  /** The console scope's environments, for the previews (empty: every environment). */
  envIds: string[];
  href: (ref: DashboardRef) => string;
  onStar: (d: DashboardSummary) => void;
  onNew: (() => void) | null;
  canWrite: boolean;
  writeHint?: string;
}) {
  return (
    <ul className="grid grid-cols-1 gap-4 sm:grid-cols-2 xl:grid-cols-3" aria-label="Dashboards" data-testid="dashboards-gallery">
      {items.map((item) => (
        <GalleryCard key={item.key} item={item} project={project} envIds={envIds} href={href(item.dashboard.ref)} onStar={onStar} />
      ))}
      {onNew && (
        <li>
          <button
            type="button"
            onClick={onNew}
            disabled={!canWrite}
            title={writeHint}
            className="flex h-full min-h-56 w-full flex-col items-center justify-center gap-2 rounded-xl border border-dashed border-border-strong px-4 text-ui text-ink-muted transition-colors hover:bg-ink/5 hover:text-ink focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring disabled:cursor-not-allowed disabled:opacity-60"
            data-testid="dashboards-gallery-new"
          >
            <Plus className="size-5" aria-hidden="true" />
            New dashboard
            <span className="text-meta">Blank, from a template, or imported</span>
          </button>
        </li>
      )}
    </ul>
  );
}

const BADGE_CLASS: Record<GalleryBadge, string> = {
  builtin: "border-border text-ink-muted",
  repo: "border-border text-ink",
  custom: "border-primary/40 text-primary-ink",
};

function GalleryCard({ item, project, envIds, href, onStar }: { item: GalleryItem; project: string; envIds: string[]; href: string; onStar: (d: DashboardSummary) => void }) {
  const d = item.dashboard;
  const badge = galleryBadgeOf(d);
  const [ref, near] = useNearViewport();
  return (
    <li
      ref={ref}
      className={cn("relative flex min-h-56 min-w-0 flex-col rounded-xl border border-border bg-surface transition-colors", href && "hover:border-border-strong hover:bg-ink/5")}
      data-testid={`dashboards-card-${item.id}`}
      data-badge={badge}
    >
      <div className="flex items-start gap-2 px-4 pt-3">
        <div className="min-w-0 flex-1">
          {href ? (
            <Link
              href={href}
              aria-label={item.title}
              className="no-color block truncate text-ui font-medium text-ink after:absolute after:inset-0 after:rounded-xl after:content-[''] focus-visible:outline-none focus-visible:after:ring-2 focus-visible:after:ring-ring"
            >
              {item.title}
            </Link>
          ) : (
            <span className="block truncate text-ui font-medium text-ink">{item.title}</span>
          )}
          <p className="mt-0.5 line-clamp-2 min-h-[2lh] text-meta text-ink-muted">{item.description || "\u00a0"}</p>
        </div>
        <span className="relative z-10 -mr-1.5 -mt-1 shrink-0">
          <StarButton starred={d.starred} title={item.title} onToggle={() => onStar(d)} />
        </span>
      </div>
      <div className="mx-4 mt-2 h-20" data-testid={`dashboards-card-${item.id}-preview`}>
        <CardPreview dashboard={d} project={project} envIds={envIds} enabled={near} />
      </div>
      <div className="mt-auto flex min-w-0 items-center gap-2 px-4 pb-3 pt-3 text-meta text-ink-muted">
        <span className={cn("inline-flex h-5 shrink-0 items-center rounded-md border px-1.5 text-meta font-medium", BADGE_CLASS[badge])} data-testid={`dashboards-card-${item.id}-badge`}>
          {GALLERY_BADGE_LABELS[badge]}
        </span>
        {item.note ? (
          <span className="min-w-0 truncate">{item.note}</span>
        ) : item.tabs && item.tabs.length > 1 ? (
          <span className="min-w-0 truncate" title={item.tabs.join(" · ")} data-testid={`dashboards-card-${item.id}-tabs`}>
            {item.tabs.length} dashboards
          </span>
        ) : d.panelCount > 0 ? (
          <span className="shrink-0 tabular-nums">{d.panelCount} panels</span>
        ) : null}
        {d.lastViewedAtMs !== null && (
          <span className="ml-auto shrink-0" data-testid={`dashboards-card-${item.id}-viewed`}>
            Viewed {formatAgo(d.lastViewedAtMs)}
          </span>
        )}
      </div>
    </li>
  );
}

/** The panel a card previews: its first preview panel, as a line over time (a number panel's series drawn over the window). */
export function previewPanelOf(d: Pick<DashboardSummary, "previewPanels">): Panel | null {
  const panel = d.previewPanels.find((candidate) => ["line", "number", "bar", "sql"].includes(candidate.kind) && (candidate.kind === "sql" ? !!candidate.sql : candidate.series.length > 0));
  if (!panel) return null;
  return panel.kind === "sql" ? panel : { ...panel, kind: "line" };
}

const PREVIEW_RANGE = "24h" as const;

function CardPreview({ dashboard, project, envIds, enabled }: { dashboard: DashboardSummary; project: string; envIds: string[]; enabled: boolean }) {
  const org = useObservabilityOrg();
  const panel = useMemo(() => previewPanelOf(dashboard), [dashboard]);
  const scope = useMemo<ObsScope | null>(() => (org.orgId ? { orgId: org.orgId, projectId: dashboard.ref.projectId || project, envIds, filters: [] } : null), [org.orgId, dashboard.ref.projectId, project, envIds]);
  const result = usePanelQuery(scope, PREVIEW_RANGE, panel ?? EMPTY_PANEL, [], [], { enabled: enabled && !!panel });
  if (!panel) return <PreviewNote>No chart to preview</PreviewNote>;
  if (!enabled || result.isLoading) return <div className="h-full animate-pulse rounded-md bg-ink/5 motion-reduce:animate-none" aria-label="Loading preview" />;
  // One series: the panel's first, which is what its headline is about.
  const series = (result.data?.series ?? []).filter((s) => s.timestamps.length > 1).slice(0, 1);
  if (result.error || series.length === 0) return <PreviewNote>{result.error ? "Preview unavailable" : "No data in the last 24 hours"}</PreviewNote>;
  const timestamps = unionTimestamps(series);
  return <UPlotChart compact height={80} timestamps={timestamps} series={series.map((s) => ({ label: s.label, values: alignTo(timestamps, s) }))} label={`${panel.title || dashboard.title}, last 24 hours`} />;
}

const EMPTY_PANEL: Panel = { id: "", title: "", kind: "line", signal: "traces", series: [] };

function PreviewNote({ children }: { children: string }) {
  return <div className="flex h-full items-center justify-center rounded-md border border-dashed border-border text-meta text-ink-muted">{children}</div>;
}
