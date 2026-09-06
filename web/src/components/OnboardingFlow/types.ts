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

/**
 * The cloud compute choices, as data rather than as a comparison repeated at
 * each decision point.
 *
 * Cloud-ness drives four separate things — which steps exist, which step is
 * current, whether `DaemonConnectingGate` renders, and where the default
 * project path points — and it used to be spelled `=== "cloud_free_trial"` at
 * every one of them. `cloud_paid` is an equally valid `ComputeChoice` that
 * `launchPlanSchema` accepts, so each of those sites silently treated a paid
 * plan as local: it routed to the local project picker and never rendered the
 * gate, which is the `458a830c` failure still live on the paid path.
 */
const CLOUD_COMPUTE_CHOICES = ["cloud_free_trial", "cloud_paid"] as const;

/**
 * Whether a compute choice runs on our infrastructure rather than the user's.
 *
 * The single definition of cloud-ness. Adding a third hosted tier means adding
 * it to {@link CLOUD_COMPUTE_CHOICES} and nothing else.
 */
export function isCloudCompute(
  compute: ComputeChoice | undefined,
): compute is (typeof CLOUD_COMPUTE_CHOICES)[number] {
  return (
    compute !== undefined &&
    (CLOUD_COMPUTE_CHOICES as readonly string[]).includes(compute)
  );
}

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
  /**
   * Idempotency key for {@link commitLaunchPlan}, stable for one onboarding
   * run and minted on first arrival at the commit point.
   *
   * It lives in the plan rather than in component state precisely because the
   * plan lives in the URL: a reload mid-provision returns with the same key, so
   * the commit resumes instead of provisioning a second machine.
   *
   * REMOVED alongside it: `daemonProvisioning`. Provisioning is no longer
   * something a step starts and records in the URL — it is what the commit
   * point does, and its progress is server state the gate reads.
   */
  commitKey?: string;
  /**
   * The compute plan the user picked on the checkout step, e.g.
   * `plan_compute_small`. Written to the URL so the choice survives a reload
   * mid-payment — the one moment where losing it is most expensive.
   */
  computePlanId?: string;
  /**
   * AI credit the user chose to buy, in cents. Set only for
   * `reliant_credits` against an unfunded wallet.
   */
  aiCreditCents?: number;
  /**
   * Every purchase this plan owed has been confirmed BY THE SERVER.
   *
   * Never written from Stripe's `onComplete`, which is a presentation signal:
   * the iframe reporting a finished payment and our webhook having granted
   * entitlement can be seconds apart, and the second one can fail. It is
   * written from `EmbeddedCheckoutPanel`'s `onDone`, which fires only after
   * the server agrees, and after the facts have been re-read.
   *
   * Its job in the URL is to make the checkout step non-recurring: without it
   * a webhook that has landed but a query that has not yet refetched would
   * derive the user straight back to a payment they already made.
   */
  paid?: boolean;
  /**
   * The compute step resolved itself — the user already had a usable daemon,
   * so it auto-advanced without ever asking a question.
   *
   * Recorded because a step the user never saw must not behave as one they
   * did: it is hidden from the progress bar (showing "1 Daemon" implies a
   * choice they were never offered), and Back is suppressed on the step after
   * it. Back would otherwise clear `compute`, re-derive the compute step, and
   * immediately auto-skip forward again — a button that visibly does nothing.
   */
  computeAutoSkipped?: boolean;
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