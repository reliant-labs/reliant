export type OnboardingIntent =
  | "build_app"
  | "existing_codebase"
  | "landing_page"
  | "pitch_deck"
  | "blog_post"
  | "custom_workflow"
  | "explore";

export type ComputeChoice =
  | "cloud_free_trial"
  | "cloud_paid"
  | "local_daemon"
  | "undecided";

export type DaemonLocation = "reliant_cloud" | "self_hosted";

export type CodeSource =
  | "new_project"
  | "github_repo"
  | "local_folder"
  | "sample_project";

export type ModelProvider =
  | "reliant_credits"
  | "openai"
  | "anthropic"
  | "openrouter"
  | "other"
  | "not_configured";

export interface LaunchPlan {
  intent: OnboardingIntent;
  compute: ComputeChoice;
  daemonLocation?: DaemonLocation;
  codeSource: CodeSource;
  repo?: {
    provider: "github" | "gitlab" | "bitbucket";
    url: string;
    branch?: string;
  };
  localPath?: string;
  projectName?: string;
  workflowId: string;
  presetId?: string;
  useForge?: boolean;
  modelProvider: ModelProvider;
  workflowParams?: Record<string, unknown>;
  selectedPresets?: Record<string, string | null>;
  launchTour?: boolean;
  daemonProvisioning?: boolean;
}

export interface StepConfig {
  id: string;
  label: string;
  category: string;
  component: React.ComponentType<StepProps>;
  order: number;
}

export interface StepProps {
  plan: Partial<LaunchPlan>;
  updatePlan: (updates: Partial<LaunchPlan>) => Promise<void> | void;
  onNext: () => void;
  onBack: () => void;
}

export type OnboardingState = "not_started" | "in_progress" | "completed";