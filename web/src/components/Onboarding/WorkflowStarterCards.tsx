/**
 * WorkflowStarterCards
 *
 * Inline empty state of NewChatView. Cards configure the FIRST chat's
 * workflow + presets via tempNewChatParams.
 */

import { useEffect, useState } from "react";
import {
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
  | "plain_chat";

type AccentTone = "sky" | "fuchsia" | "emerald" | "amber" | "rose" | "violet" | "slate";

interface StarterOption {
  intent: WorkflowStarterIntent;
  icon: LucideIcon;
  label: string;
  description: string;
  workflowId: string;
  workflowParams: Record<string, unknown>;
  selectedPresets?: Record<string, string | null>;
  accent?: AccentTone;
}

const STARTER_OPTIONS: StarterOption[] = [
  {
    intent: "build_app",
    icon: Sparkles,
    label: "Build something new",
    description:
      "Forge scaffolds the project, picks the stack, and orchestrates the build end-to-end.",
    workflowId: "builtin://forge-one-shot",
    workflowParams: { mode: "auto", ask: true },
    accent: "sky",
  },
  {
    intent: "landing_page",
    icon: Palette,
    label: "Create a landing page",
    description: "Review-loop workflow that iterates until the page feels polished.",
    workflowId: "builtin://get-it-right",
    workflowParams: {
      mode: "auto",
      ask: true,
      review_instructions:
        "Evaluate visual quality, responsiveness, accessibility, copy clarity, and brand consistency.",
    },
    selectedPresets: { default: "ux" },
    accent: "fuchsia",
  },
  {
    intent: "pitch_deck",
    icon: BarChart3,
    label: "Create a pitch deck",
    description: "Pipeline that coordinates research, narrative, and slide generation.",
    workflowId: "builtin://pitch-deck",
    workflowParams: { mode: "auto", ask: false },
    accent: "amber",
  },
  {
    intent: "blog_post",
    icon: FileText,
    label: "Write docs or a blog post",
    description: "Turn source material into structured technical writing with reviewable steps.",
    workflowId: "builtin://blog-content-pipeline",
    workflowParams: { mode: "auto", ask: false },
    selectedPresets: { default: "documentation" },
    accent: "emerald",
  },
  {
    intent: "custom_workflow",
    icon: Workflow,
    label: "Create a custom workflow",
    description: "Design and build a multi-agent pipeline tailored to your process.",
    workflowId: "builtin://build-workflow",
    workflowParams: { mode: "auto", ask: true },
    accent: "violet",
  },
  {
    intent: "plain_chat",
    icon: MessageSquare,
    label: "Just chat",
    description: "Skip the workflow picker — start with a basic agentic chat.",
    workflowId: "builtin://agent",
    workflowParams: { mode: "plan" },
    accent: "slate",
  },
];

const ACCENT_STYLES: Record<AccentTone, { iconBg: string; iconText: string; hoverBorder: string; hoverGlow: string }> = {
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
  const [selectedIntent, setSelectedIntent] = useState<WorkflowStarterIntent | null>(null);

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
      <div className="space-y-8 font-sans">
        <div className="space-y-3 text-center font-sans">
          <h2 className="text-3xl font-semibold tracking-tight text-foreground">
            What are you building?
          </h2>
          <p className="mx-auto max-w-xl text-sm leading-relaxed text-muted-foreground">
            Pick a starting point and Reliant will configure the right workflow.
            You can change it any time in the chat composer.
          </p>
        </div>

        <div className="grid grid-cols-1 gap-4 sm:grid-cols-2 lg:grid-cols-3">
          {STARTER_OPTIONS.map((option) => (
            <SecondaryCard
              key={option.intent}
              option={option}
              selected={selectedIntent === option.intent}
              onClick={() => handlePick(option)}
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
}

function SecondaryCard({ option, selected, onClick }: SecondaryCardProps) {
  const Icon = option.icon;
  const accent = ACCENT_STYLES[option.accent ?? "slate"];

  return (
    <button
      type="button"
      onClick={onClick}
      className={cn(
        "group relative flex h-full flex-col overflow-hidden rounded-2xl border p-5 text-left font-sans transition-all duration-300",
        "border-white/10 bg-white/[0.025]",
        "hover:-translate-y-0.5 hover:bg-white/[0.045]",
        accent.hoverBorder,
        accent.hoverGlow,
        selected && "border-primary/70 bg-primary/[0.08] ring-2 ring-primary/30",
      )}
    >
      <div
        className="pointer-events-none absolute inset-x-0 top-0 h-px bg-gradient-to-r from-transparent via-white/20 to-transparent"
        aria-hidden="true"
      />
      <div className="flex items-start gap-3.5">
        <div
          className={cn(
            "flex h-10 w-10 flex-shrink-0 items-center justify-center rounded-xl transition-transform duration-300 group-hover:scale-105",
            accent.iconBg,
            accent.iconText,
          )}
        >
          <Icon className="h-5 w-5" />
        </div>
        <div className="min-w-0 flex-1 space-y-1.5">
          <h3 className="text-[15px] font-semibold leading-tight tracking-tight text-foreground">
            {option.label}
          </h3>
          <p className="text-xs leading-relaxed text-muted-foreground/80">
            {option.description}
          </p>
        </div>
      </div>
    </button>
  );
}