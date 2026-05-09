import {
  BarChart3,
  FileText,
  FolderOpen,
  Palette,
  Search,
  Sparkles,
  Workflow,
  type LucideIcon,
} from "lucide-react";
import { cn } from "@/lib/utils";
import type {
  CodeSource,
  ModelProvider,
  OnboardingIntent,
  StepProps,
} from "../types";
type LaunchOption = {
  intent: OnboardingIntent;
  icon: LucideIcon;
  label: string;
  description: string;
  workflowId: string;
  presetId?: string;
  codeSource: CodeSource;
  modelProvider: ModelProvider;
  useForge?: boolean;
  launchTour?: boolean;
  workflowParams?: Record<string, unknown>;
  selectedPresets?: Record<string, string | null>;
  initialPrompt?: string;
  badge?: string;
};

const LAUNCH_OPTIONS: LaunchOption[] = [
  {
    intent: "build_app",
    icon: Sparkles,
    label: "Build something new",
    description: "Start a new app or tool with orchestration from idea to implementation.",
    workflowId: "builtin://forge-one-shot",
    codeSource: "new_project",
    modelProvider: "not_configured",
    useForge: true,
    workflowParams: {
      mode: "auto",
      ask: true,
    },
    initialPrompt:
      "Help me scope a new Forge app. Ask the key product and design questions, then create the initial plan.",
    launchTour: false,
    badge: "Recommended",
  },
  {
    intent: "existing_codebase",
    icon: FolderOpen,
    label: "Work on an existing project",
    description: "Open a repo or folder, then use agentic workflows against real code.",
    workflowId: "builtin://agent",
    codeSource: "local_folder",
    modelProvider: "not_configured",
    useForge: false,
    workflowParams: {
      mode: "auto",
    },
    initialPrompt:
      "Explore this codebase and summarize the architecture, key files, and the safest first improvements.",
    launchTour: false,
  },
  {
    intent: "landing_page",
    icon: Palette,
    label: "Create a landing page",
    description: "Use a review loop with UX guidance until the page feels polished.",
    workflowId: "builtin://get-it-right",
    presetId: "ux",
    codeSource: "new_project",
    modelProvider: "not_configured",
    useForge: true,
    workflowParams: {
      mode: "auto",
      ask: true,
      review_instructions:
        "Evaluate visual quality, responsiveness, accessibility, copy clarity, and brand consistency.",
    },
    selectedPresets: {
      default: "ux",
    },
    initialPrompt:
      "Create a polished landing page. Ask for the product, audience, and visual direction if missing, then implement it end-to-end.",
    launchTour: false,
  },
  {
    intent: "pitch_deck",
    icon: BarChart3,
    label: "Create a pitch deck",
    description: "Coordinate research, narrative, and slide generation in one pipeline.",
    workflowId: "builtin://pitch-deck",
    codeSource: "new_project",
    modelProvider: "not_configured",
    useForge: true,
    workflowParams: {
      mode: "auto",
      ask: false,
    },
    initialPrompt:
      "Help me create an investor pitch deck. Ask for the company URL and any missing context before starting.",
    launchTour: false,
  },
  {
    intent: "blog_post",
    icon: FileText,
    label: "Write docs or a blog post",
    description: "Turn source material into structured technical writing with reviewable steps.",
    workflowId: "builtin://blog-content-pipeline",
    presetId: "documentation",
    codeSource: "new_project",
    modelProvider: "not_configured",
    useForge: true,
    workflowParams: {
      mode: "auto",
      ask: false,
    },
    selectedPresets: {
      default: "documentation",
    },
    initialPrompt:
      "Help me draft a high-quality technical article or documentation page. Ask for topic, audience, and source material if missing.",
    launchTour: false,
  },
  {
    intent: "custom_workflow",
    icon: Workflow,
    label: "Create a custom workflow",
    description: "Design and build a multi-agent pipeline tailored to your process.",
    workflowId: "builtin://build-workflow",
    codeSource: "new_project",
    modelProvider: "not_configured",
    useForge: false,
    workflowParams: {
      mode: "auto",
      ask: true,
    },
    initialPrompt:
      "Help me design and build a custom workflow. Ask about my use case, integrations, and quality requirements, then create it.",
    launchTour: false,
  },
  {
    intent: "explore",
    icon: Search,
    label: "Explore Reliant",
    description: "Start with a basic chat and the guided tour before choosing a deeper pipeline.",
    workflowId: "builtin://agent",
    codeSource: "sample_project",
    modelProvider: "not_configured",
    launchTour: true,
    useForge: false,
    workflowParams: {
      mode: "plan",
    },
    initialPrompt: "Show me what Reliant can do and suggest a first workflow to try.",
  },
];

export function GoalStep({ plan, updatePlan, onNext }: StepProps) {
  const handleSelect = (option: LaunchOption) => {
    const codeSource =
      option.intent === "existing_codebase" && plan.compute === "cloud_free_trial"
        ? "github_repo"
        : option.codeSource;
    const resetProjectContext = {
      localPath: undefined,
      projectName: undefined,
      repo: undefined,
    };

    updatePlan({
      ...resetProjectContext,
      intent: option.intent,
      workflowId: option.workflowId,
      presetId: option.presetId,
      codeSource,
      modelProvider: option.modelProvider,
      useForge: option.useForge,
      workflowParams: option.workflowParams,
      selectedPresets: option.selectedPresets,
      initialPrompt: option.initialPrompt,
      launchTour: option.launchTour ?? false,
    });
    onNext();
  };

  return (
    <div className="space-y-6">
      <div className="space-y-3 text-center">
        <p className="text-xs font-semibold uppercase tracking-[0.18em] text-violet-500 dark:text-violet-300">
          Agentic pipelines in one workspace
        </p>
        <h2 className="text-2xl font-semibold tracking-tight text-foreground">
          What are you building?
        </h2>
        <p className="mx-auto max-w-xl text-sm leading-relaxed text-muted-foreground">
          Reliant lets you orchestrate multi-agent pipelines, run focused workflows, or use a basic chat against your code, all from one place.
        </p>
      </div>

      <div className="grid grid-cols-1 gap-3 sm:grid-cols-2">
        {LAUNCH_OPTIONS.map((option) => {
          const Icon = option.icon;
          const selected = plan.intent === option.intent;
          return (
            <button
              key={option.intent}
              type="button"
              onClick={() => handleSelect(option)}
              className={cn(
                "relative overflow-hidden rounded-2xl border p-4 text-left shadow-sm transition-all hover:-translate-y-0.5",
                "border-border/60 bg-background/70 hover:border-primary/35 hover:bg-muted/45",
                selected && "border-primary/70 bg-primary/[0.08] ring-2 ring-primary/15",
              )}
            >
              {selected && (
                <div className="absolute -right-10 -top-10 h-24 w-24 rounded-full bg-primary/12 blur-2xl" aria-hidden="true" />
              )}
              <div className="relative flex items-start gap-3">
                <div
                  className={cn(
                    "mt-0.5 flex-shrink-0 rounded-xl p-2 transition-colors",
                    selected ? "bg-primary/15 text-primary" : "bg-muted text-muted-foreground",
                  )}
                >
                  <Icon className="h-4 w-4" />
                </div>
                <div className="min-w-0 space-y-1">
                  <div className="flex flex-wrap items-center gap-2">
                    <h3 className="text-sm font-semibold text-foreground">
                      {option.label}
                    </h3>
                    {option.badge && (
                      <span className="rounded-full bg-primary/12 px-2 py-0.5 text-[10px] font-semibold uppercase tracking-wider text-primary">
                        {option.badge}
                      </span>
                    )}
                  </div>
                  <p className="text-xs leading-relaxed text-muted-foreground">
                    {option.description}
                  </p>
                </div>
              </div>
            </button>
          );
        })}
      </div>
    </div>
  );
}