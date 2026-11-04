/**
 * The status slot above the composer (dev-docs/design/mobile-chat-redesign.md
 * §6).
 *
 * Up to nine bands used to stack between the transcript and the composer, each
 * reasonable alone and with no shared budget. On a compact column that stack
 * could eat the conversation, so there the slot shows ONE line — the highest
 * priority active item, with a `+N` count — and tapping it opens a sheet
 * listing every active item in full, with its existing actions. A medium column
 * gets up to three lines; a wide column renders every band in full, exactly as
 * before.
 *
 * Collapsing is for phones only: the mobile shell (`/m/*`), or any viewport
 * below the mobile breakpoint (`useNarrowViewport`, Tailwind's `md`). The
 * desktop shell renders every band in full at any column width (§6: "The
 * desktop shell is unchanged"), so a chat column narrowed by its sidebars keeps
 * the queued strip's interrupt and cancel buttons and the background-work
 * pill's inline expand, instead of a one-line summary that opens a sheet.
 *
 * Pending approvals are never collapsed: they are passed in as `approvals` and
 * always render in full above the composer. ask_user questions and the
 * take-over bar replace the composer and never pass through here.
 *
 * ## How the slot knows what is active
 *
 * Each banner decides for itself whether it is showing: ResumeDaemonPill holds
 * resume-in-flight and dismissal state, DiskPressureBanner an open dialog,
 * QueuedMessages per-row busy state, and so on. Lifting every one of those
 * "am I active" computations into shared hooks would either double-subscribe
 * (the slot and the banner each calling it, with local state like dismissal
 * diverging between the two) or hoist all that state into ChatPresenter. So
 * the banners stay the single owner of their state and REPORT instead: each is
 * wrapped in a `StatusSlotItem`, and calls `useStatusSlotSummary(...)` with its
 * one-line summary while it renders something (null while it does not).
 *
 * ## How one mounted banner shows up in two places
 *
 * A banner must stay mounted whatever the layout, or collapsing would drop its
 * state (a resume in flight, a half-read queue). So the banners render ONCE,
 * through React portals into two detached `display: contents` nodes that the
 * slot owns, and the slot moves those nodes — plain DOM moves, which React
 * neither sees nor minds, since it never inserts or removes a portal's
 * container — between the inline band (wide), a hidden holder (collapsed) and
 * the sheet's body (collapsed, sheet open). React's event bubbling follows the
 * component tree, not the DOM, so every action keeps working wherever the node
 * sits.
 *
 * ## How a banner looks in the sheet
 *
 * Inline, a banner is a full-width band of the column: a `border-t`, the
 * composer's gutters, its own vertical padding. Moved into the sheet, that
 * read as a stack of edge-to-edge strips with doubled padding. So collapsed,
 * each `StatusSlotItem` is a rounded card (the MobileCardGroup treatment), and
 * a banner reading `useStatusSlotCollapsed()` drops its band chrome for the
 * card's padding — `statusBannerFrame()` names the two frames for the banners
 * that share them.
 */

import {
  createContext,
  useCallback,
  useContext,
  useEffect,
  useLayoutEffect,
  useMemo,
  useRef,
  useState,
  type ReactNode,
} from "react";
import { createPortal } from "react-dom";
import { AlertTriangle, ChevronRight } from "lucide-react";
import { cn } from "../../lib/utils";
import { useComposeMode } from "../../lib/composeMode";
import { useSurface } from "../../lib/surfaceContext";
import { useNarrowViewport } from "../../hooks/useNarrowViewport";
import { ComposerSheet } from "./ComposerSheet";

// ---------------------------------------------------------------------------
// Items and their priority (§6 table)
// ---------------------------------------------------------------------------

export type StatusSlotItemId =
  | "recovery"
  | "no-machine"
  | "wake"
  | "oom"
  | "disk"
  | "resume"
  | "queued"
  | "background";

/** Lower wins. Also the item's order in the sheet. */
export const STATUS_SLOT_PRIORITY: Record<StatusSlotItemId, number> = {
  recovery: 1,
  "no-machine": 2,
  wake: 3,
  oom: 4,
  disk: 5,
  resume: 6,
  queued: 7,
  background: 8,
};

/** Ambient items: what is going on, not what went wrong. */
const AMBIENT: ReadonlySet<StatusSlotItemId> = new Set(["queued", "background"]);

export type StatusTone = "neutral" | "info" | "warning" | "danger";

export interface StatusSummary {
  /** One line, sentence case: "Session interrupted", "2 queued". */
  label: string;
  tone?: StatusTone;
}

interface Entry extends Required<StatusSummary> {
  id: StatusSlotItemId;
}

// ---------------------------------------------------------------------------
// Width classes (§4.1). On a phone the class follows the column; the desktop
// shell is always wide (see ComposerStatusSlot).
// ---------------------------------------------------------------------------

export type StatusSlotLayout = "compact" | "medium" | "wide";

const MEDIUM_MIN = 600;
const WIDE_MIN = 840;

export function statusSlotLayoutForWidth(width: number): StatusSlotLayout {
  // 0 is "not measured" (no layout engine, or not yet laid out): render the
  // band as it has always rendered rather than guessing a phone.
  if (width <= 0 || width >= WIDE_MIN) return "wide";
  return width < MEDIUM_MIN ? "compact" : "medium";
}

/**
 * The width class of the column `ref` sits in. Measured in a layout effect so
 * the first paint already has the right layout (no wide-then-compact flash).
 */
function useColumnLayout(ref: React.RefObject<HTMLElement | null>): StatusSlotLayout {
  const [layout, setLayout] = useState<StatusSlotLayout>("wide");
  useLayoutEffect(() => {
    const el = ref.current;
    if (!el) return;
    const measure = () => setLayout(statusSlotLayoutForWidth(el.getBoundingClientRect().width));
    measure();
    if (typeof ResizeObserver === "undefined") return;
    const observer = new ResizeObserver(measure);
    observer.observe(el);
    return () => observer.disconnect();
  }, [ref]);
  return layout;
}

// ---------------------------------------------------------------------------
// Reporting
// ---------------------------------------------------------------------------

interface SlotContextValue {
  report: (id: StatusSlotItemId, summary: StatusSummary | null) => void;
  /** The band is collapsed into the line (and the sheet). */
  collapsed: boolean;
}

const SlotContext = createContext<SlotContextValue | null>(null);
const ItemReportContext = createContext<((summary: StatusSummary | null) => void) | null>(null);

/**
 * Tell the slot what this banner says in one line, or null while it shows
 * nothing. Call it unconditionally, before any early return. Outside a slot
 * (another screen, a unit test) it does nothing.
 */
export function useStatusSlotSummary(summary: StatusSummary | null): void {
  const report = useContext(ItemReportContext);
  const label = summary?.label;
  const tone = summary?.tone ?? "neutral";
  useLayoutEffect(() => {
    if (!report) return;
    report(label === undefined ? null : { label, tone });
  }, [report, label, tone]);
  // Unmounting is the last word: a banner that goes away is not active.
  useLayoutEffect(() => () => report?.(null), [report]);
}

/**
 * True while the band is collapsed, so this banner renders either hidden or in
 * the sheet — never inline in the column. Banners read it to swap their band
 * chrome for a card body (`statusBannerFrame`), and ResumeDaemonPill to stop
 * floating over the header.
 */
export function useStatusSlotCollapsed(): boolean {
  return useContext(SlotContext)?.collapsed ?? false;
}

/**
 * A banner's outer frame. `band` is how it spans the column inline (its
 * `border-t`, gutters and padding); in the sheet the item around it is already
 * a card, so the banner keeps only its own look (`common`: tone fill, text)
 * on the card's padding.
 */
export function statusBannerFrame(inSheet: boolean, band: string, common?: string): string {
  return cn(inSheet ? STATUS_SHEET_CARD_BODY : band, common);
}

/** The padding of a banner's body inside its sheet card. */
export const STATUS_SHEET_CARD_BODY = "px-3 py-2.5";

/**
 * The frame of a banner that lines up with the composer (the machine lines,
 * the no-machine hint). Inline: the composer's gutters and max width, and the
 * body's small inset and bottom spacing against the input (`bandClassName`).
 * In the sheet: just the body, on the card's padding.
 */
export function StatusBandColumn({
  children,
  className,
  bandClassName = "px-1 pb-1.5",
  "data-testid": testId,
}: {
  children: ReactNode;
  /** The body's own classes, in both frames. */
  className?: string;
  /** Inline-only body spacing. */
  bandClassName?: string;
  "data-testid"?: string;
}) {
  const inSheet = useStatusSlotCollapsed();
  if (inSheet) {
    return (
      <div className={cn(STATUS_SHEET_CARD_BODY, className)} data-testid={testId}>
        {children}
      </div>
    );
  }
  return (
    <div className="flex-shrink-0 px-4 sm:px-6 lg:px-8" data-testid={testId}>
      <div className="mx-auto max-w-[1200px]">
        <div className={cn(bandClassName, className)}>{children}</div>
      </div>
    </div>
  );
}

/** One banner in the band. Supplies the item's identity to its reporter. */
export function StatusSlotItem({ id, children }: { id: StatusSlotItemId; children: ReactNode }) {
  const slot = useContext(SlotContext);
  const report = slot?.report;
  const itemReport = useCallback((summary: StatusSummary | null) => report?.(id, summary), [report, id]);
  const collapsed = slot?.collapsed ?? false;
  return (
    <ItemReportContext.Provider value={itemReport}>
      {/* Inline, `contents` makes the banner a direct child of the chat
          column, exactly as before. Collapsed, the wrapper is a card in the
          sheet body, ordered by priority; it clips the banner's tone fill to
          its corners. An inactive banner renders nothing and `empty:hidden`
          keeps it from taking a gap. */}
      <div
        data-status-slot-item={id}
        className={collapsed ? "min-w-0 overflow-hidden rounded-lg elevation-1 empty:hidden" : "contents"}
        style={collapsed ? { order: STATUS_SLOT_PRIORITY[id] } : undefined}
      >
        {children}
      </div>
    </ItemReportContext.Provider>
  );
}

// ---------------------------------------------------------------------------
// The slot
// ---------------------------------------------------------------------------

interface ComposerStatusSlotProps {
  /**
   * Banners that sit between the transcript and the approvals on a wide
   * column (queued messages, background work). Each a `StatusSlotItem`.
   */
  upper: ReactNode;
  /** Never collapsed: rendered in full right above the composer. */
  approvals: ReactNode;
  /** Banners below the approvals on a wide column. Each a `StatusSlotItem`. */
  lower: ReactNode;
  /** Test seam: skip measuring (and the phone check) and use this layout. */
  layout?: StatusSlotLayout;
}

function usePortalNode(name: string): HTMLDivElement {
  const [node] = useState(() => {
    const el = document.createElement("div");
    el.style.display = "contents";
    el.dataset.statusSlotSection = name;
    return el;
  });
  return node;
}

export function ComposerStatusSlot({ upper, approvals, lower, layout: layoutOverride }: ComposerStatusSlotProps) {
  const measureRef = useRef<HTMLDivElement>(null);
  const measured = useColumnLayout(measureRef);
  // Only a phone collapses; the desktop shell is always wide (module comment).
  const mobileSurface = useSurface() === "mobile";
  const narrowViewport = useNarrowViewport();
  const layout = layoutOverride ?? (mobileSurface || narrowViewport ? measured : "wide");
  const composeMode = useComposeMode();
  // With the keyboard up the transcript is down to a few lines, so the slot
  // collapses to one line whatever the width.
  const collapsed = composeMode || layout !== "wide";
  const maxLines = composeMode || layout === "compact" ? 1 : 3;

  const [summaries, setSummaries] = useState<ReadonlyMap<StatusSlotItemId, Entry>>(() => new Map());
  const report = useCallback((id: StatusSlotItemId, summary: StatusSummary | null) => {
    setSummaries((prev) => {
      const current = prev.get(id);
      if (!summary) {
        if (!current) return prev;
        const next = new Map(prev);
        next.delete(id);
        return next;
      }
      const tone = summary.tone ?? "neutral";
      if (current && current.label === summary.label && current.tone === tone) return prev;
      return new Map(prev).set(id, { id, label: summary.label, tone });
    });
  }, []);
  const context = useMemo(() => ({ report, collapsed }), [report, collapsed]);

  const entries = useMemo(
    () => [...summaries.values()].sort((a, b) => STATUS_SLOT_PRIORITY[a.id] - STATUS_SLOT_PRIORITY[b.id]),
    [summaries],
  );

  const [sheetOpen, setSheetOpen] = useState(false);
  const showSheet = sheetOpen && collapsed && entries.length > 0;
  // Nothing left to list, or the column grew wide enough to show it all: the
  // sheet closes for good rather than reopening when the next item appears.
  useEffect(() => {
    if (sheetOpen && !showSheet) setSheetOpen(false);
  }, [sheetOpen, showSheet]);

  // --- Where the two banner sections live ---------------------------------
  const upperNode = usePortalNode("upper");
  const lowerNode = usePortalNode("lower");
  const hosts = useRef<{
    upper: HTMLElement | null;
    lower: HTMLElement | null;
    holder: HTMLElement | null;
    sheet: HTMLElement | null;
  }>({ upper: null, lower: null, holder: null, sheet: null });

  const relocate = useCallback(() => {
    const h = hosts.current;
    const collapsedHost = (showSheet && h.sheet) || h.holder;
    const upperTarget = collapsed ? collapsedHost : h.upper ?? h.holder;
    const lowerTarget = collapsed ? collapsedHost : h.lower ?? h.holder;
    if (upperTarget && upperNode.parentNode !== upperTarget) upperTarget.appendChild(upperNode);
    if (lowerTarget && lowerNode.parentNode !== lowerTarget) lowerTarget.appendChild(lowerNode);
  }, [collapsed, showSheet, upperNode, lowerNode]);

  // Callback refs place the sections the moment a host attaches — before the
  // banners' own layout effects and before the sheet's Modal moves focus in,
  // so focus lands on the first action in the sheet, not on its panel.
  const upperHostRef = useCallback((el: HTMLDivElement | null) => {
    hosts.current.upper = el;
    relocate();
  }, [relocate]);
  const lowerHostRef = useCallback((el: HTMLDivElement | null) => {
    hosts.current.lower = el;
    relocate();
  }, [relocate]);
  const holderRef = useCallback((el: HTMLDivElement | null) => {
    hosts.current.holder = el;
    relocate();
  }, [relocate]);
  const sheetHostRef = useCallback((el: HTMLDivElement | null) => {
    hosts.current.sheet = el;
    relocate();
  }, [relocate]);
  useLayoutEffect(relocate);

  const openSheet = useCallback(() => setSheetOpen(true), []);

  return (
    <SlotContext.Provider value={context}>
      {/* Zero-height probe: the width of the chat column. */}
      <div ref={measureRef} aria-hidden="true" className="h-0 w-full flex-shrink-0" />
      {!collapsed && <div ref={upperHostRef} className="contents" />}
      {collapsed && entries.length > 0 && (
        <StatusLines entries={entries} maxLines={maxLines} onOpen={openSheet} />
      )}
      {approvals}
      {!collapsed && <div ref={lowerHostRef} className="contents" />}
      {/* Collapsed and closed, the banners stay mounted (their state lives in
          them) but out of sight. */}
      <div ref={holderRef} hidden data-testid="status-slot-holder" />
      {showSheet && (
        <ComposerSheet title="Status" onClose={() => setSheetOpen(false)}>
          <div ref={sheetHostRef} className="flex flex-col gap-2" data-testid="status-slot-sheet" />
        </ComposerSheet>
      )}
      {createPortal(upper, upperNode)}
      {createPortal(lower, lowerNode)}
    </SlotContext.Provider>
  );
}

// ---------------------------------------------------------------------------
// The collapsed line(s)
// ---------------------------------------------------------------------------

const TONE_TEXT: Record<StatusTone, string> = {
  neutral: "text-foreground",
  info: "text-foreground",
  warning: "text-warning-ink",
  danger: "text-destructive-ink",
};

const TONE_DOT: Record<StatusTone, string> = {
  neutral: "bg-muted-foreground",
  info: "bg-info",
  warning: "bg-warning",
  danger: "bg-destructive",
};

/**
 * Which lines to draw. One line: the top item, or — when nothing outranks the
 * ambient pair and both are active — "2 queued" and "3 running" as two chips
 * sharing it. More lines: the top items in order, the last carrying the count
 * of what did not fit.
 */
export function planStatusLines(
  entries: readonly Entry[],
  maxLines: number,
): Array<{ chips: Entry[]; more: number }> {
  if (entries.length === 0) return [];
  if (entries.length <= maxLines) return entries.map((e) => ({ chips: [e], more: 0 }));
  if (maxLines === 1 && entries.every((e) => AMBIENT.has(e.id))) {
    return [{ chips: [...entries], more: 0 }];
  }
  const shown = entries.slice(0, maxLines);
  return shown.map((e, i) => ({ chips: [e], more: i === shown.length - 1 ? entries.length - maxLines : 0 }));
}

function StatusLines({
  entries,
  maxLines,
  onOpen,
}: {
  entries: readonly Entry[];
  maxLines: number;
  onOpen: () => void;
}) {
  const lines = planStatusLines(entries, maxLines);
  return (
    // Same gutters and column as the composer it belongs to.
    <div
      className="flex-shrink-0 px-4 sm:px-6 lg:px-8"
      data-testid="composer-status-slot"
      data-line-count={lines.length}
    >
      <div className="mx-auto flex max-w-[1200px] flex-col" role="status" aria-live="polite">
        {lines.map((line) =>
          line.chips.length > 1 ? (
            <div key={line.chips.map((c) => c.id).join("+")} className="flex min-w-0 gap-2">
              {line.chips.map((chip) => (
                <StatusLineButton key={chip.id} entry={chip} more={0} onOpen={onOpen} chip />
              ))}
            </div>
          ) : (
            <StatusLineButton key={line.chips[0].id} entry={line.chips[0]} more={line.more} onOpen={onOpen} />
          ),
        )}
      </div>
    </div>
  );
}

function StatusLineButton({
  entry,
  more,
  onOpen,
  chip = false,
}: {
  entry: Entry;
  more: number;
  onOpen: () => void;
  chip?: boolean;
}) {
  const label = more > 0 ? `${entry.label}, ${more} more` : entry.label;
  return (
    <button
      type="button"
      onClick={onOpen}
      aria-haspopup="dialog"
      aria-label={`${label}. Show status`}
      data-testid={`status-slot-line-${entry.id}`}
      className={cn(
        "flex min-h-11 min-w-0 items-center gap-2 rounded-md px-2 text-left text-sm transition-colors pointer-fine:min-h-9",
        "hover:bg-foreground/5 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring",
        chip ? "flex-1" : "w-full",
        TONE_TEXT[entry.tone],
      )}
    >
      {entry.id === "recovery" ? (
        <AlertTriangle className="h-3.5 w-3.5 flex-shrink-0" aria-hidden="true" />
      ) : (
        <span className={cn("h-2 w-2 flex-shrink-0 rounded-full", TONE_DOT[entry.tone])} aria-hidden="true" />
      )}
      <span className="min-w-0 flex-1 truncate font-medium">{entry.label}</span>
      {more > 0 && (
        <span
          className="flex-shrink-0 rounded-full border border-border px-1.5 text-xs tabular-nums text-muted-foreground"
          data-testid="status-slot-more"
        >
          +{more}
        </span>
      )}
      <ChevronRight className="h-4 w-4 flex-shrink-0 text-muted-foreground" aria-hidden="true" />
    </button>
  );
}

// ---------------------------------------------------------------------------
// Recovery banner (priority 1)
// ---------------------------------------------------------------------------

/** The run's workflow was lost; history is intact and a restart picks it up. */
export function RecoveryBanner({ onRestart }: { onRestart?: () => void }) {
  useStatusSlotSummary({ label: "Session interrupted", tone: "warning" });
  const inSheet = useStatusSlotCollapsed();
  return (
    <div
      role="alert"
      data-testid="recovery-banner"
      className={statusBannerFrame(
        inSheet,
        "flex-shrink-0 border-t border-warning/30 px-4 py-2",
        "bg-warning/10 text-sm text-warning-ink",
      )}
    >
      <div className="flex items-center justify-between gap-4">
        <div className="flex items-center gap-2">
          <AlertTriangle className="h-4 w-4 flex-shrink-0" aria-hidden="true" />
          <span>Session was interrupted. Your message history is preserved.</span>
        </div>
        {onRestart && (
          <button
            type="button"
            onClick={onRestart}
            className="min-h-11 flex-shrink-0 rounded-md border border-warning/40 bg-warning/20 px-3 text-xs font-medium transition-colors hover:bg-warning/30 pointer-fine:min-h-8"
          >
            Restart Conversation
          </button>
        )}
      </div>
    </div>
  );
}
