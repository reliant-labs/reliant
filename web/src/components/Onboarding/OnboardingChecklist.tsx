import { useState } from "react";
import {
  X,
  ChevronDown,
  ChevronRight,
  CheckCircle2,
  Circle,
  Calendar,
} from "lucide-react";
import { cn } from "../../lib/utils";
import { Progress } from "../ui/Progress";
import { useOnboardingChecklistStore } from "../../store/onboardingChecklistStore";
import { useApiKeySetupStore } from "../../store/apiKeySetupStore";
import { useViewerStore } from "../../store/viewerStore";
import { REQUIRED_ITEMS, BONUS_ITEMS, CHECKLIST_ITEMS } from "./constants";
import type { ChecklistItem, ChecklistItemId } from "./types";

// ─── Action Handlers ──────────────────────────────────────────────────────────

function executeItemAction(item: ChecklistItem) {
  switch (item.id as ChecklistItemId) {
    case "add-api-key":
      useApiKeySetupStore.getState().openModal();
      break;
    case "start-chat":
      document.querySelector<HTMLElement>("[data-chat-input]")?.focus();
      break;
    case "use-custom-workflow":
      useViewerStore.getState().setWorkflowMode(true);
      break;
    case "create-workflow":
      useViewerStore.getState().setWorkflowMode(true);
      break;
    case "create-workspace":
      window.dispatchEvent(new CustomEvent("open-create-worktree-modal"));
      break;
    case "create-preset":
      useViewerStore.getState().setWorkflowMode(true);
      break;
    case "install-mcp":
      useViewerStore.getState().setSettingsMode(true, "mcp");
      break;
    case "read-docs":
      window.open("https://docs.reliantlabs.io/", "_blank");
      useOnboardingChecklistStore.getState().markComplete("read-docs");
      break;
  }
}

// ─── Progress Ring (SVG) ──────────────────────────────────────────────────────

function ProgressRing({
  percentage,
  size = 28,
  strokeWidth = 3,
  variant = "default",
}: {
  percentage: number;
  size?: number;
  strokeWidth?: number;
  /** "default" uses theme colors, "inverted" uses white on primary bg */
  variant?: "default" | "inverted";
}) {
  const radius = (size - strokeWidth) / 2;
  const circumference = 2 * Math.PI * radius;
  const offset = circumference - (percentage / 100) * circumference;

  const trackColor = variant === "inverted" ? "rgba(255,255,255,0.3)" : "hsl(var(--muted))";
  const progressColor = variant === "inverted" ? "white" : "hsl(var(--primary))";

  return (
    <svg
      width={size}
      height={size}
      className="shrink-0 -rotate-90"
      viewBox={`0 0 ${size} ${size}`}
    >
      <circle
        cx={size / 2}
        cy={size / 2}
        r={radius}
        fill="none"
        stroke={trackColor}
        strokeWidth={strokeWidth}
      />
      <circle
        cx={size / 2}
        cy={size / 2}
        r={radius}
        fill="none"
        stroke={progressColor}
        strokeWidth={strokeWidth}
        strokeDasharray={circumference}
        strokeDashoffset={offset}
        strokeLinecap="round"
        className="transition-all duration-500 ease-out"
      />
    </svg>
  );
}

// ─── Checklist Item Row ───────────────────────────────────────────────────────

function ChecklistItemRow({
  item,
  isComplete,
}: {
  item: ChecklistItem;
  isComplete: boolean;
}) {
  return (
    <div className="flex items-start gap-3 py-2 px-1 group">
      {/* Checkbox icon */}
      <div className="mt-0.5 shrink-0">
        {isComplete ? (
          <CheckCircle2 className="size-5 text-success" />
        ) : (
          <Circle className="size-5 text-muted-foreground/50" />
        )}
      </div>

      {/* Text */}
      <div className="flex-1 min-w-0">
        <p
          className={cn(
            "text-sm font-medium leading-tight",
            isComplete && "line-through text-muted-foreground",
          )}
        >
          {item.title}
        </p>
        <p
          className={cn(
            "text-xs text-muted-foreground mt-0.5 leading-snug",
            isComplete && "line-through",
          )}
        >
          {item.description}
        </p>
      </div>

      {/* Action button */}
      {!isComplete && (
        <button
          onClick={(e) => {
            e.stopPropagation();
            executeItemAction(item);
          }}
          className="shrink-0 mt-0.5 text-xs font-medium px-2.5 py-1 rounded-md bg-primary/10 text-primary hover:bg-primary/20 transition-colors opacity-0 group-hover:opacity-100 focus:opacity-100"
        >
          {item.actionLabel}
        </button>
      )}
    </div>
  );
}

// ─── Main Component ───────────────────────────────────────────────────────────

export function OnboardingChecklist() {
  const {
    completedItems,
    panelState,
    setPanelState,
    totalCompleted,
    completionPercentage,
  } = useOnboardingChecklistStore();

  const [bonusExpanded, setBonusExpanded] = useState(false);

  if (panelState === "dismissed") return null;

  const completed = totalCompleted();
  const total = CHECKLIST_ITEMS.length;
  const percentage = completionPercentage();

  // ─── Collapsed pill ───────────────────────────────────────────────────

  if (panelState === "collapsed") {
    return (
      <button
        onClick={() => setPanelState("expanded")}
        className={cn(
          "fixed bottom-4 right-4 z-40",
          "flex items-center gap-2.5 px-5 py-2.5 font-sans",
          "bg-primary text-primary-foreground rounded-full",
          "shadow-lg shadow-primary/25 cursor-pointer",
          "hover:bg-primary/90 transition-colors",
        )}
      >
        <ProgressRing percentage={percentage} size={24} strokeWidth={2.5} variant="inverted" />
        <span className="text-sm font-medium">
          Setup Guide
        </span>
        <span className="text-xs text-primary-foreground/70 tabular-nums">
          {completed}/{total}
        </span>
      </button>
    );
  }

  // ─── Expanded panel ───────────────────────────────────────────────────

  return (
    <div
      className={cn(
        "fixed bottom-4 right-4 z-40 w-80 max-h-[480px]",
        "flex flex-col font-sans",
        "bg-[hsl(var(--surface-modal))] border border-border/50 rounded-xl",
        "elevation-4",
        "animate-in fade-in slide-in-from-bottom-2 duration-200",
      )}
    >
      {/* Header */}
      <div className="px-4 pt-4 pb-3 border-b border-border/30">
        <div className="flex items-center justify-between mb-2">
          <h3 className="text-sm font-semibold text-foreground">
            Setup Guide
          </h3>
          <button
            onClick={() => setPanelState("collapsed")}
            className="p-1 rounded-md text-muted-foreground hover:text-foreground hover:bg-muted/50 transition-colors"
            aria-label="Collapse setup guide"
          >
            <X className="size-4" />
          </button>
        </div>
        <Progress
          value={percentage}
          variant="primary"
          size="sm"
          className="mb-1.5"
        />
        <p className="text-xs text-muted-foreground">
          {completed} of {total} completed
        </p>
      </div>

      {/* Scrollable content */}
      <div className="flex-1 overflow-y-auto overscroll-contain px-3 py-2">
        {/* Required items */}
        <div className="space-y-0.5">
          {REQUIRED_ITEMS.map((item) => (
            <ChecklistItemRow
              key={item.id}
              item={item}
              isComplete={completedItems.has(item.id)}
            />
          ))}
        </div>

        {/* Bonus section */}
        {BONUS_ITEMS.length > 0 && (
          <div className="mt-2 pt-2 border-t border-border/30">
            <button
              onClick={() => setBonusExpanded((prev) => !prev)}
              className="flex items-center gap-1.5 w-full py-1.5 text-xs font-medium text-muted-foreground hover:text-foreground transition-colors"
            >
              {bonusExpanded ? (
                <ChevronDown className="size-3.5" />
              ) : (
                <ChevronRight className="size-3.5" />
              )}
              Bonus
              <span className="text-muted-foreground/60 ml-auto tabular-nums">
                {BONUS_ITEMS.filter((i) => completedItems.has(i.id)).length}/
                {BONUS_ITEMS.length}
              </span>
            </button>

            {bonusExpanded && (
              <div className="space-y-0.5 animate-in fade-in slide-in-from-top-1 duration-150">
                {BONUS_ITEMS.map((item) => (
                  <ChecklistItemRow
                    key={item.id}
                    item={item}
                    isComplete={completedItems.has(item.id)}
                  />
                ))}
              </div>
            )}
          </div>
        )}
      </div>

      {/* Footer */}
      <div className="px-4 py-3 border-t border-border/30 flex items-center justify-between">
        <button
          onClick={() => setPanelState("dismissed")}
          className="text-xs text-muted-foreground hover:text-foreground transition-colors"
        >
          Dismiss guide
        </button>
        <a
          href="https://cal.com/team/reliant/onboarding"
          target="_blank"
          rel="noopener noreferrer"
          className="no-color text-sm font-medium text-primary-foreground visited:text-primary-foreground hover:text-primary-foreground inline-flex items-center gap-1.5 px-3 py-1.5 rounded-lg bg-primary hover:bg-primary/90 transition-colors"
        >
          <Calendar className="size-3.5" />
          Book a demo
        </a>
      </div>
    </div>
  );
}