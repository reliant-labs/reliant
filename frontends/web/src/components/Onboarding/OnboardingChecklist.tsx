/**
 * The Setup Guide: a small floating pill ("Get started · 3/8") on every page
 * of the one shell, that opens into a readable panel of the app's first steps
 * (checklistItems.ts). The shell frame mounts it (Layout/shell/AppShell.tsx),
 * so it is wherever the nav is: chats, Projects, an app's console, Workflows,
 * Settings. Never on the phone surface, the workflow builder or onboarding.
 *
 * WHERE IT SITS. The bottom-right corner, 16px in, lifted above whatever must
 * stay clickable under it: the composer (`[data-onboarding='chat-input']`)
 * and any bar marked `data-guide-clear` (a page's sticky action bar). It
 * measures them (useGuideClearance), so a composer that grows as you type
 * pushes the guide up instead of being covered. Toasts are top-centre, page
 * actions are in the page header: the corner holds neither.
 *
 * OPEN AND CLOSED. Collapsed it is the pill; expanded, a panel above the
 * same corner, as tall as the room above the composer allows. The panel is a
 * popover: Escape, a click outside it, or running one of its items puts it
 * back to the pill, so it only covers the page while the person is reading
 * it. ✕ dismisses the guide (Help › Show setup guide brings it back). Open,
 * collapsed and dismissed are the store's `panelState`, kept across reloads
 * and devices. A finished guide retires itself.
 *
 * Reliant only: the checklist's state lives in Reliant's settings rows, so the
 * forge shell (local tour storage, no settings service) never shows it.
 */

import { useCallback, useEffect, useLayoutEffect, useRef, useState } from "react";
import { ArrowUpRight, Calendar, CheckCircle2, ChevronDown, ChevronRight, Circle, X } from "lucide-react";

import { cn } from "../../lib/utils";
import { trackEvent } from "../../lib/analytics";
import { isPhoneViewport } from "../../lib/mobileRedirect";
import { homePath, sandboxesPath, settingsPath, workflowLibraryPath } from "@/lib/paths";
import { START_APP } from "../Apps/appStart";
import ProgressBar from "../forge-ui/progress_bar";
import { useAuthStore } from "../../store/authStore";
import { useApiKeySetupStore } from "../../store/apiKeySetupStore";
import { openAddProject } from "../../store/addProjectDialogStore";
import { useOnboardingChecklistStore } from "../../store/onboardingChecklistStore";
import { activeChecklistItems } from "./checklistItems";
import type { ChecklistItem, ChecklistItemId } from "./types";
import { getTourStorage } from "./tourShell";
import { requestChatInputFocus } from "./checklistRequests";

// ─── Action Handlers ──────────────────────────────────────────────────────────

// Navigation comes in from the shell (`onNavigate`: a narrow window's nav
// drawer closes on it), so this stays a pure switch with no router references.
interface ItemActionDeps {
  navigate: (href: string) => void;
  openTours: () => void;
}

/** Where each item takes you. Doing the thing there is what completes it, never this click. */
export function runChecklistItem(id: ChecklistItemId, deps: ItemActionDeps) {
  switch (id) {
    case "connect-machine":
      deps.navigate(sandboxesPath());
      break;
    case "add-api-key":
      useApiKeySetupStore.getState().openModal();
      break;
    case "start-chat":
      // Off the chat (an app's console, Projects), the chat home's composer.
      if (!requestChatInputFocus()) deps.navigate(homePath());
      break;
    case "add-repo":
      // Add project opens over any route; clone is the GitHub path.
      openAddProject("clone");
      break;
    case "run-workflow":
      deps.navigate(workflowLibraryPath());
      break;
    case "deploy-app":
      // "Build an app" is a chat with the starter text in the composer (Apps/appStart.ts).
      deps.navigate(`${homePath()}?start=${START_APP}`);
      break;
    case "invite-teammate":
      deps.navigate(settingsPath("member-permissions"));
      break;
    case "take-product-tour":
      deps.openTours();
      break;
  }
}

// ─── Visibility ───────────────────────────────────────────────────────────────

/** Signed in, Reliant's settings storage (never the forge shell) and the checklist's state loaded. */
function useGuideEligible(): boolean {
  const user = useAuthStore((state) => state.user);
  const isInitialized = useOnboardingChecklistStore((state) => state.isInitialized);
  return !!user && isInitialized && getTourStorage().kind === "settings";
}

/** Whether every item that applies here is done (activeChecklistItems). */
function useAllDone(): boolean {
  const completedItems = useOnboardingChecklistStore((state) => state.completedItems);
  return activeChecklistItems().every((item) => completedItems.has(item.id));
}

/** Whether the guide shows: eligible, not dismissed, and work left. */
export function useSetupGuideVisible(): boolean {
  const eligible = useGuideEligible();
  const panelState = useOnboardingChecklistStore((state) => state.panelState);
  const allDone = useAllDone();
  return eligible && panelState !== "dismissed" && !allDone;
}

/**
 * Whether a dismissed guide can come back (the Help row's "Show setup guide"):
 * the same gates, dismissed, with work still left.
 */
export function useSetupGuideRevivable(): boolean {
  const eligible = useGuideEligible();
  const panelState = useOnboardingChecklistStore((state) => state.panelState);
  const allDone = useAllDone();
  return eligible && panelState === "dismissed" && !allDone;
}

// ─── Placement ────────────────────────────────────────────────────────────────

/** What the guide stays above: the composer, and any bar a page marks. */
export const GUIDE_CLEAR_SELECTOR = "[data-onboarding='chat-input'], [data-guide-clear]";
/** Its distance from the window's edges, and from what it clears. */
const EDGE_PX = 16;
const GAP_PX = 12;
/** The panel's width: the room the corner reserves, open or closed, so opening it never moves it. */
const PANEL_WIDTH_PX = 360;
/** Room kept above the panel: the page's 48px header bar, where its actions are. */
const TOP_CLEAR_PX = 64;

type Box = Pick<DOMRect, "top" | "bottom" | "left" | "right" | "width" | "height">;

/**
 * The guide's bottom offset: 16px, or above the highest element it must clear
 * that reaches its column and sits on the window's bottom edge. Pure, for tests.
 */
export function guideBottom(rects: ReadonlyArray<Box>, viewport: { width: number; height: number }): number {
  const columnLeft = viewport.width - EDGE_PX - Math.min(PANEL_WIDTH_PX, viewport.width - 2 * EDGE_PX);
  let bottom = EDGE_PX;
  for (const r of rects) {
    if (r.width === 0 || r.height === 0) continue;
    if (r.right <= columnLeft) continue;
    // Only what is pinned to the bottom of the window: a bar mid-page scrolls away.
    if (r.bottom < viewport.height - 160) continue;
    bottom = Math.max(bottom, Math.ceil(viewport.height - r.top + GAP_PX));
  }
  return bottom;
}

/**
 * Keeps `guideBottom` current: on resize, when a composer grows (ResizeObserver),
 * and when one mounts or unmounts (a MutationObserver, at most every 200ms).
 */
function useGuideClearance(active: boolean): number {
  const [bottom, setBottom] = useState(EDGE_PX);
  useLayoutEffect(() => {
    if (!active) return;
    let resize: ResizeObserver | null = null;
    let watched: Element[] = [];
    const measure = () => {
      const elements = Array.from(document.querySelectorAll(GUIDE_CLEAR_SELECTOR));
      if (elements.length !== watched.length || elements.some((el, i) => el !== watched[i])) {
        resize?.disconnect();
        watched = elements;
        if (typeof ResizeObserver !== "undefined") {
          resize = new ResizeObserver(() => measure());
          for (const el of elements) resize.observe(el);
        }
      }
      setBottom(
        guideBottom(
          elements.map((el) => el.getBoundingClientRect()),
          { width: window.innerWidth, height: window.innerHeight },
        ),
      );
    };
    measure();
    let pending: ReturnType<typeof setTimeout> | null = null;
    const soon = () => {
      if (pending) return;
      pending = setTimeout(() => {
        pending = null;
        measure();
      }, 200);
    };
    const mutations = new MutationObserver(soon);
    mutations.observe(document.body, { childList: true, subtree: true });
    window.addEventListener("resize", measure);
    return () => {
      if (pending) clearTimeout(pending);
      mutations.disconnect();
      resize?.disconnect();
      window.removeEventListener("resize", measure);
    };
  }, [active]);
  return bottom;
}

// ─── Progress Ring (SVG) ──────────────────────────────────────────────────────

function ProgressRing({ percentage, size = 16, strokeWidth = 2 }: { percentage: number; size?: number; strokeWidth?: number }) {
  const radius = (size - strokeWidth) / 2;
  const circumference = 2 * Math.PI * radius;
  const offset = circumference - (percentage / 100) * circumference;
  return (
    <svg width={size} height={size} className="shrink-0 -rotate-90" viewBox={`0 0 ${size} ${size}`} aria-hidden="true">
      <circle cx={size / 2} cy={size / 2} r={radius} fill="none" stroke="hsl(var(--border))" strokeWidth={strokeWidth} />
      <circle
        cx={size / 2}
        cy={size / 2}
        r={radius}
        fill="none"
        stroke="hsl(var(--primary))"
        strokeWidth={strokeWidth}
        strokeDasharray={circumference}
        strokeDashoffset={offset}
        strokeLinecap="round"
        className="transition-all duration-500 ease-out motion-reduce:transition-none"
      />
    </svg>
  );
}

// ─── Checklist Item Row ───────────────────────────────────────────────────────

const FOCUS_INSET = "focus:outline-none focus-visible:ring-2 focus-visible:ring-inset focus-visible:ring-ring";
const ICON_BUTTON =
  "inline-flex size-7 shrink-0 items-center justify-center rounded-md text-muted-foreground transition-colors hover:bg-foreground/5 hover:text-foreground focus:outline-none focus-visible:ring-2 focus-visible:ring-ring";

function ChecklistItemRow({
  item,
  isComplete,
  isNext,
  onAction,
}: {
  item: ChecklistItem;
  isComplete: boolean;
  /** The first item left: it carries its description; the others keep it as a tooltip. */
  isNext: boolean;
  onAction: (item: ChecklistItem) => void;
}) {
  if (isComplete) {
    return (
      <li
        className="flex min-h-[var(--row-h)] items-center gap-2.5 rounded-md px-2 text-ui text-muted-foreground"
        data-testid={`setup-guide-item-${item.id}`}
        data-complete="true"
      >
        <CheckCircle2 className="size-4 shrink-0 text-success-ink" aria-hidden="true" />
        <span className="min-w-0 flex-1">
          {item.title}
          <span className="sr-only"> (done)</span>
        </span>
      </li>
    );
  }
  return (
    <li>
      <button
        type="button"
        onClick={() => onAction(item)}
        title={isNext ? undefined : item.description}
        data-testid={`setup-guide-item-${item.id}`}
        className={cn(
          "group/item flex min-h-[var(--row-h)] w-full items-start gap-2.5 rounded-md px-2 py-1.5 text-left text-ui text-foreground transition-colors hover:bg-foreground/5",
          FOCUS_INSET,
        )}
      >
        <Circle className="mt-0.5 size-4 shrink-0 text-muted-foreground" aria-hidden="true" />
        <span className="min-w-0 flex-1">
          <span className={cn("block", isNext && "font-medium")}>{item.title}</span>
          {isNext && <span className="mt-0.5 block text-meta text-muted-foreground">{item.description}</span>}
        </span>
        <ChevronRight
          className="mt-0.5 size-4 shrink-0 text-muted-foreground opacity-0 transition-opacity group-hover/item:opacity-100 group-focus-visible/item:opacity-100"
          aria-hidden="true"
        />
      </button>
    </li>
  );
}

// ─── Main Component ───────────────────────────────────────────────────────────

export function OnboardingChecklist({
  onNavigate,
  onOpenTours,
}: {
  /** The shell's navigate (a narrow window's nav drawer closes on it). */
  onNavigate: (href: string) => void;
  /** Opens the journey picker on the page on screen (`?tour=pick:menu`). */
  onOpenTours: () => void;
}) {
  // A phone has its own surface, with no shell; a desktop window this narrow is handed to it.
  const visible = useSetupGuideVisible() && !isPhoneViewport();
  const completedItems = useOnboardingChecklistStore((state) => state.completedItems);
  const panelState = useOnboardingChecklistStore((state) => state.panelState);
  const setPanelState = useOnboardingChecklistStore((state) => state.setPanelState);
  const bottom = useGuideClearance(visible);
  const rootRef = useRef<HTMLDivElement>(null);
  const expanded = panelState === "expanded";
  const collapse = useCallback(() => void setPanelState("collapsed"), [setPanelState]);

  // A popover: Escape, or a press outside it, puts it back to the pill.
  useEffect(() => {
    if (!visible || !expanded) return;
    const onKeyDown = (event: KeyboardEvent) => {
      if (event.key === "Escape" && !event.defaultPrevented) collapse();
    };
    const onPointerDown = (event: PointerEvent) => {
      if (rootRef.current && !rootRef.current.contains(event.target as Node)) collapse();
    };
    window.addEventListener("keydown", onKeyDown);
    document.addEventListener("pointerdown", onPointerDown, true);
    return () => {
      window.removeEventListener("keydown", onKeyDown);
      document.removeEventListener("pointerdown", onPointerDown, true);
    };
  }, [visible, expanded, collapse]);

  if (!visible) return null;

  const items = activeChecklistItems();
  const completed = items.filter((item) => completedItems.has(item.id)).length;
  const total = items.length;
  const percentage = total === 0 ? 100 : Math.round((completed / total) * 100);
  const next = items.find((item) => !completedItems.has(item.id));

  const onAction = (item: ChecklistItem) => {
    // The click is not the completion: the item completes on its own signal (checklistItems.ts).
    trackEvent("checklist_step_clicked", { stepId: item.id, stepName: item.title, stepsCompleted: completed, totalSteps: total });
    collapse();
    runChecklistItem(item.id, { navigate: onNavigate, openTours: onOpenTours });
  };

  const dismiss = (
    <button
      type="button"
      onClick={() => void setPanelState("dismissed")}
      aria-label="Dismiss setup guide"
      title="Dismiss (Help › Show setup guide brings it back)"
      className={ICON_BUTTON}
    >
      <X className="size-3.5" aria-hidden="true" />
    </button>
  );

  return (
    <div
      ref={rootRef}
      className="fixed right-4 z-40 flex max-w-[calc(100vw-2rem)] flex-col items-end font-sans"
      style={{ bottom }}
      data-testid="setup-guide"
      data-tour="setup-guide"
      data-state={expanded ? "expanded" : "collapsed"}
    >
      {expanded ? (
        <section
          aria-label="Setup guide"
          className="flex w-[22.5rem] max-w-full flex-col rounded-xl border border-border bg-popover text-popover-foreground shadow-xl animate-in fade-in slide-in-from-bottom-2 duration-150 motion-reduce:animate-none"
          style={{ maxHeight: `min(34rem, calc(100dvh - ${bottom + TOP_CLEAR_PX}px))` }}
        >
          <header className="shrink-0 border-b border-border-subtle px-4 pb-3 pt-3">
            <div className="flex items-center gap-1">
              <h2 className="min-w-0 flex-1 text-section">Get started</h2>
              <button type="button" onClick={collapse} aria-label="Collapse setup guide" className={ICON_BUTTON}>
                <ChevronDown className="size-4" aria-hidden="true" />
              </button>
              {dismiss}
            </div>
            <p className="mb-2 text-meta text-muted-foreground">
              {completed} of {total} done
            </p>
            {/* .forge-ui on the bar itself: its fill is forge's `accent`, which is
                the action color only inside that scope (index.css). */}
            <ProgressBar value={percentage} variant="default" size="sm" ariaLabel="Setup progress" className="forge-ui" />
          </header>
          {/* Only the list scrolls, inside the room above the composer; header and footer stay. */}
          <ul className="min-h-0 flex-1 space-y-0.5 overflow-y-auto overscroll-contain p-2" data-testid="setup-guide-items">
            {items.map((item) => (
              <ChecklistItemRow
                key={item.id}
                item={item}
                isComplete={completedItems.has(item.id)}
                isNext={item.id === next?.id}
                onAction={onAction}
              />
            ))}
          </ul>
          <footer className="shrink-0 border-t border-border-subtle px-2 py-1.5">
            <a
              href="https://cal.com/team/reliant/onboarding"
              target="_blank"
              rel="noopener noreferrer"
              className={cn(
                "no-color flex h-8 items-center gap-2 rounded-md px-2 text-meta text-muted-foreground transition-colors hover:bg-foreground/5 hover:text-foreground",
                FOCUS_INSET,
              )}
            >
              <Calendar className="size-3.5 shrink-0" aria-hidden="true" />
              <span className="min-w-0 flex-1">Book a demo</span>
              <ArrowUpRight className="size-3.5 shrink-0" aria-hidden="true" />
            </a>
          </footer>
        </section>
      ) : (
        <div className="flex h-9 items-center gap-0.5 rounded-full border border-border bg-popover pl-1 pr-1 text-popover-foreground shadow-lg">
          <button
            type="button"
            onClick={() => void setPanelState("expanded")}
            aria-expanded={false}
            aria-label={`Get started, ${completed} of ${total} done`}
            data-testid="setup-guide-toggle"
            className="flex h-7 items-center gap-2 rounded-full pl-2 pr-2 text-ui font-medium transition-colors hover:bg-foreground/5 focus:outline-none focus-visible:ring-2 focus-visible:ring-ring"
          >
            <ProgressRing percentage={percentage} />
            <span>Get started</span>
            <span className="text-meta font-normal tabular-nums text-muted-foreground">· {completed}/{total}</span>
          </button>
          {dismiss}
        </div>
      )}
    </div>
  );
}
