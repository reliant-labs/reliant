/**
 * WorkflowStarterCards
 *
 * Inline empty state of NewChatView. Cards configure the FIRST chat's
 * workflow + presets via tempNewChatParams.
 */

import { useEffect, useState } from "react";
import {
  ArrowRightLeft,
  BarChart3,
  FileText,
  MessageSquare,
  Palette,
  Sparkles,
  Workflow,
  type LucideIcon,
} from "lucide-react";
import { cn } from "@/lib/utils";
import { trackEvent } from "@/lib/analytics";
import { useChatParamsStore } from "@/store/chatParamsStore";

export type WorkflowStarterIntent =
  | "build_app"
  | "landing_page"
  | "pitch_deck"
  | "blog_post"
  | "custom_workflow"
  | "migrate"
  | "plain_chat";

type AccentTone =
  | "sky"
  | "fuchsia"
  | "emerald"
  | "amber"
  | "rose"
  | "violet"
  | "slate";

interface StarterOption {
  intent: WorkflowStarterIntent;
  icon: LucideIcon;
  label: string;
  description: string;
  workflowId: string;
  workflowParams: Record<string, unknown>;
  selectedPresets?: Record<string, string | null>;
  accent?: AccentTone;
  featured?: boolean;
  learnMoreUrl?: string;
}

const STARTER_OPTIONS: StarterOption[] = [
  {
    intent: "build_app",
    icon: Sparkles,
    label: "Build something new with Forge",
    description:
      "Guardrails and best practices from day 1 — go from idea to a production-ready app in your first week.",
    workflowId: "builtin://forge-one-shot",
    workflowParams: { mode: "auto", ask: true },
    accent: "sky",
    featured: true,
    learnMoreUrl: "https://github.com/reliant-labs/forge",
  },
  {
    intent: "landing_page",
    icon: Palette,
    label: "Create a landing page",
    description:
      "Scopes the page, then chains a copy pass and a visual-design pass before handing you a live preview.",
    workflowId: "builtin://landing-page",
    workflowParams: { mode: "auto", ask: true },
    accent: "fuchsia",
  },
  {
    intent: "pitch_deck",
    icon: BarChart3,
    label: "Create a pitch deck",
    description:
      "Pipeline that coordinates research, narrative, and slide generation.",
    workflowId: "builtin://pitch-deck",
    workflowParams: { mode: "auto", ask: false },
    accent: "amber",
  },
  {
    intent: "blog_post",
    icon: FileText,
    label: "Write docs or a blog post",
    description:
      "Turn source material into structured technical writing with reviewable steps.",
    workflowId: "builtin://blog-content-pipeline",
    workflowParams: { mode: "auto", ask: false },
    selectedPresets: { default: "documentation" },
    accent: "emerald",
  },
  {
    intent: "custom_workflow",
    icon: Workflow,
    label: "Create a custom workflow",
    description:
      "Design and build a multi-agent pipeline tailored to your process.",
    workflowId: "builtin://build-workflow",
    workflowParams: { mode: "auto", ask: true },
    accent: "violet",
  },
  {
    intent: "migrate",
    icon: ArrowRightLeft,
    label: "Migrate from Claude Code",
    description:
      "Import configuration from Claude Code, Cursor, Codex, or Windsurf into Reliant.",
    workflowId: "builtin://migrate",
    workflowParams: { mode: "auto" },
    accent: "rose",
  },
  {
    intent: "plain_chat",
    icon: MessageSquare,
    label: "Just chat",
    description: "Skip the workflow picker — start with a basic agentic chat.",
    workflowId: "builtin://agent",
    // No param overrides ON PURPOSE: this card's whole promise is "skip the
    // picker", so it chooses the workflow and nothing else — `mode` resolves
    // from builtin://agent's own default like it would if you picked the
    // workflow by hand. It previously forced `mode: "plan"`, which is
    // read-only tools — the opposite of the "basic agentic chat" it advertises,
    // and the only card of the seven that overrode the mode this way.
    workflowParams: {},
    accent: "slate",
  },
];

const ACCENT_STYLES: Record<
  AccentTone,
  { iconBg: string; iconText: string; hoverBorder: string; hoverGlow: string }
> = {
  sky: {
    iconBg: "bg-sky-500/15 ring-1 ring-sky-400/30",
    iconText: "text-sky-300",
    hoverBorder: "group-hover:border-sky-400/40",
    hoverGlow: "group-hover:shadow-[0_18px_40px_-20px_rgba(56,189,248,0.45)]",
  },
  fuchsia: {
    iconBg: "bg-fuchsia-500/15 ring-1 ring-fuchsia-400/30",
    iconText: "text-fuchsia-300",
    hoverBorder: "group-hover:border-fuchsia-400/40",
    hoverGlow: "group-hover:shadow-[0_18px_40px_-20px_rgba(232,121,249,0.45)]",
  },
  emerald: {
    iconBg: "bg-emerald-500/15 ring-1 ring-emerald-400/30",
    iconText: "text-emerald-300",
    hoverBorder: "group-hover:border-emerald-400/40",
    hoverGlow: "group-hover:shadow-[0_18px_40px_-20px_rgba(52,211,153,0.45)]",
  },
  amber: {
    iconBg: "bg-amber-500/15 ring-1 ring-amber-400/30",
    iconText: "text-amber-300",
    hoverBorder: "group-hover:border-amber-400/40",
    hoverGlow: "group-hover:shadow-[0_18px_40px_-20px_rgba(251,191,36,0.45)]",
  },
  rose: {
    iconBg: "bg-rose-500/15 ring-1 ring-rose-400/30",
    iconText: "text-rose-300",
    hoverBorder: "group-hover:border-rose-400/40",
    hoverGlow: "group-hover:shadow-[0_18px_40px_-20px_rgba(251,113,133,0.45)]",
  },
  violet: {
    iconBg: "bg-violet-500/15 ring-1 ring-violet-400/30",
    iconText: "text-violet-300",
    hoverBorder: "group-hover:border-violet-400/40",
    hoverGlow: "group-hover:shadow-[0_18px_40px_-20px_rgba(167,139,250,0.45)]",
  },
  slate: {
    iconBg: "bg-slate-500/15 ring-1 ring-slate-400/30",
    iconText: "text-slate-300",
    hoverBorder: "group-hover:border-slate-400/40",
    hoverGlow: "group-hover:shadow-[0_18px_40px_-20px_rgba(148,163,184,0.45)]",
  },
};

interface WorkflowStarterCardsProps {
  onComplete?: (intent: WorkflowStarterIntent) => void;
}

function applyStarter(option: StarterOption) {
  const store = useChatParamsStore.getState();
  store.setTempNewChatWorkflow(option.workflowId);
  store.setTempNewChatParams(option.workflowParams);
  if (option.selectedPresets) {
    store.setTempNewChatPresets(option.selectedPresets);
  } else {
    store.setTempNewChatPresets({});
  }
}

export function WorkflowStarterCards({
  onComplete,
}: WorkflowStarterCardsProps = {}) {
  const [selectedIntent, setSelectedIntent] =
    useState<WorkflowStarterIntent | null>(null);

  useEffect(() => {
    trackEvent("starter_cards_shown");
  }, []);

  const handlePick = (option: StarterOption) => {
    setSelectedIntent(option.intent);

    applyStarter(option);

    trackEvent("starter_card_picked", {
      intent: option.intent,
      workflow_id: option.workflowId,
    });

    onComplete?.(option.intent);
  };

  return (
    <div className="w-full max-w-5xl mx-auto font-sans">
      <div className="space-y-5 font-sans">
        <h2 className="text-center text-xs font-semibold uppercase tracking-[0.18em] text-muted-foreground/80">
          What are you building?
        </h2>

        <div className="grid grid-cols-1 gap-3 sm:grid-cols-2 lg:grid-cols-3">
          {STARTER_OPTIONS.map((option) => (
            <SecondaryCard
              key={option.intent}
              option={option}
              selected={selectedIntent === option.intent}
              onClick={() => handlePick(option)}
              className={cn(option.featured && "sm:col-span-2 lg:col-span-3")}
            />
          ))}
        </div>
      </div>
    </div>
  );
}

interface SecondaryCardProps {
  option: StarterOption;
  selected: boolean;
  onClick: () => void;
  className?: string;
}

function SecondaryCard({
  option,
  selected,
  onClick,
  className,
}: SecondaryCardProps) {
  const Icon = option.icon;
  const accent = ACCENT_STYLES[option.accent ?? "slate"];

  return (
    <div
      role="button"
      tabIndex={0}
      onClick={onClick}
      onKeyDown={(e) => {
        if (e.key === "Enter" || e.key === " ") {
          e.preventDefault();
          onClick();
        }
      }}
      className={cn(
        "group relative flex h-full cursor-pointer flex-col overflow-hidden rounded-2xl border p-4 text-left font-sans transition-all duration-300 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-primary/40",
        "border-white/10 bg-white/[0.025]",
        "hover:-translate-y-0.5 hover:bg-white/[0.045]",
        accent.hoverBorder,
        accent.hoverGlow,
        selected &&
          "border-primary/70 bg-primary/[0.08] ring-2 ring-primary/30",
        className,
      )}
    >
      <div
        className="pointer-events-none absolute inset-x-0 top-0 h-px bg-gradient-to-r from-transparent via-white/20 to-transparent"
        aria-hidden="true"
      />
      {option.featured && (
        <span className="absolute right-4 top-4 inline-flex items-center gap-1.5 rounded-full bg-primary/15 px-2.5 py-1 text-[11px] font-semibold uppercase tracking-wider text-primary ring-1 ring-inset ring-primary/30">
          <span className="h-1.5 w-1.5 rounded-full bg-primary shadow-[0_0_8px_1px] shadow-primary/60" />
          Recommended
        </span>
      )}
      <div className="flex items-start gap-3.5">
        <div
          className={cn(
            "flex flex-shrink-0 items-center justify-center rounded-xl transition-transform duration-300 group-hover:scale-105",
            option.featured ? "h-12 w-12" : "h-10 w-10",
            accent.iconBg,
            accent.iconText,
          )}
        >
          <Icon className={option.featured ? "h-6 w-6" : "h-5 w-5"} />
        </div>
        <div className="min-w-0 flex-1 space-y-1.5">
          <h3
            className={cn(
              "font-semibold leading-tight tracking-tight text-foreground",
              option.featured ? "text-base pr-32" : "text-[15px]",
            )}
          >
            {option.label}
          </h3>
          <p
            className={cn(
              "leading-relaxed text-muted-foreground/80",
              option.featured ? "text-sm" : "text-xs",
            )}
          >
            {option.description}
          </p>
          {option.learnMoreUrl && (
            <p className={option.featured ? "text-sm" : "text-xs"}>
              <a
                href={option.learnMoreUrl}
                target="_blank"
                rel="noreferrer noopener"
                onClick={(e) => e.stopPropagation()}
                className="text-sky-400 underline-offset-2 hover:text-sky-300 hover:underline"
              >
                Learn more →
              </a>
            </p>
          )}
        </div>
      </div>
    </div>
  );
}
