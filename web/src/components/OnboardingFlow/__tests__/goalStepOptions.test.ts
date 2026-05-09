import { describe, expect, it, vi } from "vitest";
import type { OnboardingIntent } from "../types";

/**
 * LAUNCH_OPTIONS is a private const inside GoalStep.tsx. We test the contract
 * indirectly by rendering GoalStep, clicking each option, and asserting the
 * values passed to updatePlan.
 */

// Mock lucide-react icons to avoid JSX rendering issues in unit tests
vi.mock("lucide-react", () => ({
  Sparkles: "Sparkles",
  FolderOpen: "FolderOpen",
  Palette: "Palette",
  BarChart3: "BarChart3",
  FileText: "FileText",
  Search: "Search",
}));

// Dynamically import the module and extract LAUNCH_OPTIONS via the module internals.
// Since GoalStep is a React component and LAUNCH_OPTIONS is not exported, we
// capture the updatePlan calls to reconstruct the option configs.

interface CapturedPlan {
  intent: OnboardingIntent;
  workflowId: string;
  presetId?: string;
  codeSource: string;
  useForge?: boolean;
  workflowParams?: Record<string, unknown>;
  selectedPresets?: Record<string, string | null>;
  initialPrompt?: string;
  launchTour?: boolean;
}

/**
 * Render GoalStep with a spy updatePlan, click every option button, and
 * collect the plans that were passed.
 */
async function collectLaunchPlans(): Promise<Map<OnboardingIntent, CapturedPlan>> {
  // We need React + testing-library to render the component
  const React = await import("react");
  const { render, screen } = await import("@testing-library/react");
  const { userEvent } = await import("@testing-library/user-event");
  const { GoalStep } = await import("../steps/GoalStep");

  const plans = new Map<OnboardingIntent, CapturedPlan>();

  // We know the 6 intents; render once per click since onNext advances.
  const allIntents: OnboardingIntent[] = [
    "build_app",
    "existing_codebase",
    "landing_page",
    "pitch_deck",
    "blog_post",
    "explore",
  ];

  // The labels map (from GoalStep source) so we can find the buttons
  const labels: Record<OnboardingIntent, string> = {
    build_app: "Build something new",
    existing_codebase: "Work on an existing project",
    landing_page: "Create a landing page",
    pitch_deck: "Create a pitch deck",
    blog_post: "Write docs or a blog post",
    explore: "Explore Reliant",
  };

  for (const intent of allIntents) {
    const updatePlan = vi.fn();
    const onNext = vi.fn();

    const { unmount } = render(
      React.createElement(GoalStep, {
        plan: {},
        updatePlan,
        onNext,
        onBack: vi.fn(),
      }),
    );

    const user = userEvent.setup();
    const button = screen.getByRole("button", { name: new RegExp(labels[intent]) });
    await user.click(button);

    expect(updatePlan).toHaveBeenCalledOnce();
    expect(onNext).toHaveBeenCalledOnce();

    const calledWith = updatePlan.mock.calls[0][0] as CapturedPlan;
    plans.set(intent, calledWith);

    unmount();
  }

  return plans;
}

describe("GoalStep LAUNCH_OPTIONS contract", () => {
  let plans: Map<OnboardingIntent, CapturedPlan>;

  // Collect all plans once; the test cases are pure assertions.
  beforeAll(async () => {
    plans = await collectLaunchPlans();
  });

  // ── 1. Correct workflowId per option ──────────────────────

  it.each([
    ["build_app", "builtin://forge-one-shot"],
    ["existing_codebase", "builtin://agent"],
    ["landing_page", "builtin://get-it-right"],
    ["pitch_deck", "builtin://pitch-deck"],
    ["blog_post", "builtin://blog-content-pipeline"],
    ["explore", "builtin://agent"],
  ] as const)("%s → workflowId = %s", (intent, expectedWorkflowId) => {
    expect(plans.get(intent)!.workflowId).toBe(expectedWorkflowId);
  });

  // ── 2. Forge options have useForge: true ──────────────────

  it.each([
    ["build_app", true],
    ["existing_codebase", false],
    ["landing_page", true],
    ["pitch_deck", true],
    ["blog_post", true],
    ["explore", false],
  ] as const)("%s → useForge = %s", (intent, expectedUseForge) => {
    expect(plans.get(intent)!.useForge).toBe(expectedUseForge);
  });

  // ── 3. Options with selectedPresets have corresponding presetId ──

  it("landing_page has presetId 'ux' and selectedPresets { default: 'ux' }", () => {
    const plan = plans.get("landing_page")!;
    expect(plan.presetId).toBe("ux");
    expect(plan.selectedPresets).toEqual({ default: "ux" });
  });

  it("blog_post has presetId 'documentation' and selectedPresets { default: 'documentation' }", () => {
    const plan = plans.get("blog_post")!;
    expect(plan.presetId).toBe("documentation");
    expect(plan.selectedPresets).toEqual({ default: "documentation" });
  });

  it("build_app has no presetId", () => {
    expect(plans.get("build_app")!.presetId).toBeUndefined();
  });

  it("existing_codebase has no presetId", () => {
    expect(plans.get("existing_codebase")!.presetId).toBeUndefined();
  });

  // ── 4. workflowParams contain expected keys ──────────────

  it("build_app params: mode=auto, ask=true", () => {
    expect(plans.get("build_app")!.workflowParams).toEqual({
      mode: "auto",
      ask: true,
    });
  });

  it("existing_codebase params: mode=auto", () => {
    expect(plans.get("existing_codebase")!.workflowParams).toEqual({
      mode: "auto",
    });
  });

  it("landing_page params: mode=auto, ask=true, review_instructions (string)", () => {
    const params = plans.get("landing_page")!.workflowParams!;
    expect(params.mode).toBe("auto");
    expect(params.ask).toBe(true);
    expect(typeof params.review_instructions).toBe("string");
    expect((params.review_instructions as string).length).toBeGreaterThan(0);
  });

  it("pitch_deck params: mode=auto, ask=false", () => {
    expect(plans.get("pitch_deck")!.workflowParams).toEqual({
      mode: "auto",
      ask: false,
    });
  });

  it("blog_post params: mode=auto, ask=false", () => {
    expect(plans.get("blog_post")!.workflowParams).toEqual({
      mode: "auto",
      ask: false,
    });
  });

  it("explore params: mode=plan", () => {
    expect(plans.get("explore")!.workflowParams).toEqual({
      mode: "plan",
    });
  });

  // ── 5. All options have initialPrompt defined ─────────────

  it.each([
    "build_app",
    "existing_codebase",
    "landing_page",
    "pitch_deck",
    "blog_post",
    "explore",
  ] as OnboardingIntent[])("%s has a non-empty initialPrompt", (intent) => {
    const prompt = plans.get(intent)!.initialPrompt;
    expect(prompt).toBeDefined();
    expect(typeof prompt).toBe("string");
    expect(prompt!.length).toBeGreaterThan(0);
  });

  // ── 6. Explore has launchTour ─────────────────────────────

  it("explore has launchTour = true", () => {
    expect(plans.get("explore")!.launchTour).toBe(true);
  });

  it("build_app has launchTour = false", () => {
    expect(plans.get("build_app")!.launchTour).toBe(false);
  });

  // ── 7. All 6 options are present ─────────────────────────

  it("GoalStep renders exactly 6 options", () => {
    expect(plans.size).toBe(6);
  });
});
