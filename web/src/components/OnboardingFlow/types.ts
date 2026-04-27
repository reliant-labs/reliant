export type OnboardingIntent =
  | 'build_app'
  | 'existing_codebase'
  | 'landing_page'
  | 'pitch_deck'
  | 'blog_post'
  | 'explore';

export type ComputeChoice =
  | 'cloud_free_trial'
  | 'cloud_paid'
  | 'local_daemon'
  | 'undecided';

export type CodeSource =
  | 'new_project'
  | 'github_repo'
  | 'local_folder'
  | 'sample_project';

export type ModelProvider =
  | 'reliant_credits'
  | 'openai'
  | 'anthropic'
  | 'openrouter'
  | 'other'
  | 'not_configured';

export interface LaunchPlan {
  intent: OnboardingIntent;
  compute: ComputeChoice;
  codeSource: CodeSource;
  repo?: {
    provider: 'github' | 'gitlab' | 'bitbucket';
    url: string;
    branch?: string;
  };
  localPath?: string;
  workflowId: string;
  presetId?: string;
  useForge?: boolean;
  modelProvider: ModelProvider;
}

export interface StepConfig {
  id: string;
  category: 'goal' | 'workspace' | 'compute' | 'start';
  component: React.ComponentType<StepProps>;
  shouldShow: (plan: Partial<LaunchPlan>) => boolean;
  order: number;
}

export interface StepProps {
  plan: Partial<LaunchPlan>;
  updatePlan: (updates: Partial<LaunchPlan>) => void;
  onNext: () => void;
  onBack: () => void;
  onSkip?: () => void;
}

export type OnboardingState = 'not_started' | 'in_progress' | 'completed' | 'skipped';
