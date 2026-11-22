/**
 * The Setup Guide, docked in the shell's nav footer: a "Get started · n/9" row
 * that opens its checklist in place, so nothing floats over the page.
 *
 * It floated in the window's bottom-right corner before the one shell, where
 * it covered the composer's Send button and sat over every page's content
 * (the forge console, Projects). In the nav it competes with nothing.
 *
 * Open or closed is the store's `panelState` ("expanded" / "collapsed"), so it
 * is remembered across reloads and devices. ✕ dismisses it ("dismissed"); the
 * Help row's "Show setup guide" brings it back (store `revive`). A finished
 * guide (every required item done) retires itself.
 *
 * Reliant only: the checklist's state lives in Reliant's settings rows, so the
 * forge shell (local tour storage, no settings service) never shows it.
 */

import { useState } from "react";
import { ArrowUpRight, Calendar, CheckCircle2, ChevronRight, Circle, X } from "lucide-react";

import { cn } from "../../lib/utils";
import { trackEvent } from "../../lib/analytics";
import { homePath, newWorkflowPath, settingsPath, workflowLibraryPath } from "@/lib/paths";
import { DOCS_ORIGIN } from "@/lib/learnLinks";
import { useAuthStore } from "../../store/authStore";
import { useApiKeySetupStore } from "../../store/apiKeySetupStore";
import { useOnboardingChecklistStore } from "../../store/onboardingChecklistStore";
import { useTourStore } from "../../store/tourStore";
import { REQUIRED_ITEMS, BONUS_ITEMS, CHECKLIST_ITEMS } from "./constants";
import type { ChecklistItem, ChecklistItemId } from "./types";
import { getTourStorage } from "./tourShell";
import { requestChatInputFocus, requestCreateWorktree } from "./checklistRequests";

// ─── Action Handlers ──────────────────────────────────────────────────────────

// Navigation comes in as the nav's own `onNavigate` (a drawer closes on it, as
// on any other row), so this stays a pure switch with no router references.
interface ItemActionDeps {
  navigate: (href: string) => void;
  startTour: () => void;
}

function executeItemAction(item: ChecklistItem, deps: ItemActionDeps) {
  switch (item.id as ChecklistItemId) {
    case "add-api-key":
      useApiKeySetupStore.getState().openModal();
      break;
    case "start-chat":
      // Off the chat (an app's console, Projects), the chat home's composer.
      if (!requestChatInputFocus()) deps.navigate(homePath());
      break;
    case "use-custom-workflow":
      deps.navigate(workflowLibraryPath());
      break;
    case "create-workflow":
      // The builder's New workflow dialog, not the library: each item has its own place.
      deps.navigate(newWorkflowPath());
      break;
    case "take-product-tour":
      deps.startTour();
      break;
    case "create-workspace":
      // The modal is the chat home's; from any other page, open it there.
      if (!requestCreateWorktree()) deps.navigate(homePath());
      break;
    case "create-preset":
      // Settings › Presets lists them and says where one is made (the composer).
      deps.navigate(settingsPath("presets"));
      break;
    case "install-mcp":
      deps.navigate(settingsPath("mcp"));
      break;
    case "read-docs":
      window.open(`${DOCS_ORIGIN}/`, "_blank", "noopener,noreferrer");
      void useOnboardingChecklistStore.getState().markComplete("read-docs");
      break;
  }
}

// ─── Visibility ───────────────────────────────────────────────────────────────

/**
 * Whether the guide shows: signed in, Reliant's settings storage, its state
 * loaded, not dismissed, and required work left. Onboarding is per-user state,
 * so it never renders to a signed-out visitor.
 */
export function useSetupGuideVisible(): boolean {
  const user = useAuthStore((state) => state.user);
  const isInitialized = useOnboardingChecklistStore((state) => state.isInitialized);
  const panelState = useOnboardingChecklistStore((state) => state.panelState);
  const completedItems = useOnboardingChecklistStore((state) => state.completedItems);
  if (!user || !isInitialized || getTourStorage().kind !== "settings") return false;
  if (panelState === "dismissed") return false;
  // A finished guide retires itself: bonus items are optional, so clearing the
  // required set is what "done" means.
  return !REQUIRED_ITEMS.every((item) => completedItems.has(item.id));
}

/**
 * Whether a dismissed guide can come back (the Help row's "Show setup guide"):
 * the same gates, dismissed, with required work still left.
 */
export function useSetupGuideRevivable(): boolean {
  const user = useAuthStore((state) => state.user);
  const isInitialized = useOnboardingChecklistStore((state) => state.isInitialized);
  const panelState = useOnboardingChecklistStore((state) => state.panelState);
  const completedItems = useOnboardingChecklistStore((state) => state.completedItems);
  if (!user || !isInitialized || getTourStorage().kind !== "settings") return false;
  return panelState === "dismissed" && !REQUIRED_ITEMS.every((item) => completedItems.has(item.id));
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
      <li className="flex min-h-[var(--row-h)] items-center gap-2 rounded-md pl-2 pr-2 text-ui text-muted-foreground" data-testid={`setup-guide-item-${item.id}`} data-complete="true">
        <CheckCircle2 className="size-4 shrink-0 text-success-ink" aria-hidden="true" />
        <span className="min-w-0 flex-1 truncate">
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
          "group/item flex min-h-[var(--row-h)] w-full items-start gap-2 rounded-md py-1.5 pl-2 pr-2 text-left text-ui text-foreground transition-colors hover:bg-foreground/5",
          FOCUS_INSET,
        )}
      >
        <Circle className="mt-0.5 size-4 shrink-0 text-muted-foreground" aria-hidden="true" />
        <span className="min-w-0 flex-1">
          <span className="flex items-center gap-1">
            <span className="min-w-0 truncate">{item.title}</span>
            {item.action === "external-link" && <ArrowUpRight className="size-3.5 shrink-0 text-muted-foreground" aria-hidden="true" />}
          </span>
          {isNext && <span className="mt-0.5 block text-meta text-muted-foreground">{item.description}</span>}
        </span>
      </button>
    </li>
  );
}

// ─── Main Component ───────────────────────────────────────────────────────────

export function OnboardingChecklist({
  onNavigate,
  onOpenTours,
}: {
  /** The nav's navigate (a drawer closes on it). */
  onNavigate: (href: string) => void;
  /** Opens the journey picker on the page on screen (`?tour=pick:menu`). */
  onOpenTours?: () => void;
}) {
  const visible = useSetupGuideVisible();
  const { completedItems, panelState, setPanelState } = useOnboardingChecklistStore();
  const [bonusExpanded, setBonusExpanded] = useState(false);

  if (!visible) return null;

  // Starting the tour from the guide: reset any prior tour state and open the
  // journey picker, on whatever route we're on.
  const startTour = () => {
    void useTourStore.getState().resetTourProgress();
    onOpenTours?.();
  };
  const deps: ItemActionDeps = { navigate: onNavigate, startTour };

  const completed = completedItems.size;
  const total = CHECKLIST_ITEMS.length;
  const percentage = total === 0 ? 100 : Math.round((completed / total) * 100);
  const expanded = panelState === "expanded";
  const next = CHECKLIST_ITEMS.find((item) => !completedItems.has(item.id));
  const bonusDone = BONUS_ITEMS.filter((item) => completedItems.has(item.id)).length;

  const onAction = (item: ChecklistItem) => {
    trackEvent("checklist_step_completed", {
      stepId: item.id,
      stepName: item.title,
      stepsCompleted: completed,
      totalSteps: total,
    });
    executeItemAction(item, deps);
  };

  const row = (item: ChecklistItem) => (
    <ChecklistItemRow key={item.id} item={item} isComplete={completedItems.has(item.id)} isNext={item.id === next?.id} onAction={onAction} />
  );

  return (
    <div data-testid="setup-guide" data-tour="setup-guide" data-state={expanded ? "expanded" : "collapsed"}>
      <div className="group/guide flex h-[var(--row-h)] min-w-0 items-center rounded-md text-ui text-foreground transition-colors hover:bg-foreground/5">
        <button
          type="button"
          onClick={() => void setPanelState(expanded ? "collapsed" : "expanded")}
          aria-expanded={expanded}
          aria-controls="setup-guide-items"
          aria-label={`Get started, ${completed} of ${total} done`}
          data-testid="setup-guide-toggle"
          className={cn("flex h-full min-w-0 flex-1 items-center gap-2 rounded-md px-2 text-left", FOCUS_INSET)}
        >
          <span className="flex size-4 shrink-0 items-center justify-center">
            <ProgressRing percentage={percentage} />
          </span>
          <span className="min-w-0 flex-1 truncate">Get started</span>
          <span className="ml-auto flex shrink-0 items-center gap-1.5 text-meta text-muted-foreground">
            <span className="tabular-nums">
              {completed}/{total}
            </span>
            <ChevronRight className={cn("size-3.5 transition-transform motion-reduce:transition-none", expanded && "rotate-90")} aria-hidden="true" />
          </span>
        </button>
        {/* ✕ is "go away"; the Help row's "Show setup guide" brings it back. */}
        <button
          type="button"
          onClick={() => void setPanelState("dismissed")}
          aria-label="Dismiss setup guide"
          title="Dismiss setup guide (Help › Show setup guide brings it back)"
          className={cn(
            "mr-1 inline-flex size-6 shrink-0 items-center justify-center rounded-md text-muted-foreground transition-colors hover:bg-foreground/5 hover:text-foreground focus:outline-none focus-visible:ring-2 focus-visible:ring-ring",
            !expanded && "opacity-0 focus-visible:opacity-100 group-hover/guide:opacity-100",
          )}
        >
          <X className="size-3.5" aria-hidden="true" />
        </button>
      </div>

      {expanded && (
        // In place, under its row; it scrolls inside itself so the nav above keeps its room.
        <div id="setup-guide-items" role="group" aria-label="Setup guide" className="max-h-[40dvh] overflow-y-auto overscroll-contain pb-1 pl-2">
          <ul className="space-y-0.5">{REQUIRED_ITEMS.map(row)}</ul>
          {BONUS_ITEMS.length > 0 && (
            <>
              <button
                type="button"
                onClick={() => setBonusExpanded((open) => !open)}
                aria-expanded={bonusExpanded}
                className={cn(
                  "mt-1 flex h-7 w-full items-center gap-1.5 rounded-md pl-2 pr-2 text-meta font-medium text-muted-foreground transition-colors hover:text-foreground",
                  FOCUS_INSET,
                )}
              >
                <ChevronRight className={cn("size-3.5 transition-transform motion-reduce:transition-none", bonusExpanded && "rotate-90")} aria-hidden="true" />
                Bonus
                <span className="ml-auto tabular-nums">
                  {bonusDone}/{BONUS_ITEMS.length}
                </span>
              </button>
              {bonusExpanded && <ul className="space-y-0.5">{BONUS_ITEMS.map(row)}</ul>}
            </>
          )}
          <a
            href="https://cal.com/team/reliant/onboarding"
            target="_blank"
            rel="noopener noreferrer"
            className={cn(
              "no-color mt-1 flex h-7 items-center gap-2 rounded-md pl-2 pr-2 text-meta text-muted-foreground transition-colors hover:bg-foreground/5 hover:text-foreground",
              FOCUS_INSET,
            )}
          >
            <Calendar className="size-3.5 shrink-0" aria-hidden="true" />
            <span className="min-w-0 flex-1 truncate">Book a demo</span>
            <ArrowUpRight className="size-3.5 shrink-0" aria-hidden="true" />
          </a>
        </div>
      )}
    </div>
  );
}
