// Intent values the rendered onboarding wizard can actually set. ProjectChoiceStep
// only ever writes "build_app" or "existing_codebase"; deriveStep / getStepsForPlan
// / codeSourceForCompute branch on those two alone. The richer set of
// landing_page/pitch_deck/blog_post/etc. lives in WorkflowStarterCards
// (components/Onboarding) under its own `WorkflowStarterIntent` type — unrelated.
export type OnboardingIntent = "build_app" | "existing_codebase";

export type ComputeChoice =
  | "cloud_free_trial"
  | "cloud_paid"
  | "local_daemon"
  | "undecided";

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
  | "copilot"
  | "other"
  | "not_configured";

export interface LaunchPlan {
  intent: OnboardingIntent;
  compute: ComputeChoice;
  repo?: {
    provider: "github" | "gitlab" | "bitbucket";
    url: string;
    branch?: string;
  };
  localPath?: string;
  projectName?: string;
  workflowId: string;
  modelProvider: ModelProvider;
  workflowParams?: Record<string, unknown>;
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