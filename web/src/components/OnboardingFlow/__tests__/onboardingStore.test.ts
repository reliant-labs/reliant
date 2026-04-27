import { beforeEach, describe, expect, it, vi } from "vitest";
import type { StepConfig } from "../types";

// Node 22+ ships a built-in `localStorage` on globalThis that requires
// --localstorage-file to work.  Zustand's persist middleware picks it up
// instead of jsdom's Storage.  Stub it so zustand can persist without errors.
const storage = new Map<string, string>();
vi.stubGlobal("localStorage", {
  getItem: (key: string) => storage.get(key) ?? null,
  setItem: (key: string, value: string) => storage.set(key, value),
  removeItem: (key: string) => storage.delete(key),
  clear: () => storage.clear(),
  get length() { return storage.size; },
  key: (i: number) => [...storage.keys()][i] ?? null,
});

let useOnboardingFlowStore: typeof import("../onboardingStore")["useOnboardingFlowStore"];
let stepRegistry: typeof import("../StepRegistry")["stepRegistry"];

function makeStep(overrides: Partial<StepConfig> & { id: string }): StepConfig {
  return {
    category: "goal",
    component: (() => null) as unknown as StepConfig["component"],
    shouldShow: () => true,
    order: 0,
    ...overrides,
  };
}

const testSteps: StepConfig[] = [
  makeStep({ id: "goal", category: "goal", order: 0 }),
  makeStep({
    id: "workspace",
    category: "workspace",
    order: 0,
    shouldShow: (plan) => plan.intent === "build_app",
  }),
  makeStep({ id: "ready", category: "start", order: 0 }),
];

beforeEach(async () => {
  storage.clear();
  vi.resetModules();

  const registryMod = await import("../StepRegistry");
  stepRegistry = registryMod.stepRegistry;
  stepRegistry.registerMany(testSteps);

  const storeMod = await import("../onboardingStore");
  useOnboardingFlowStore = storeMod.useOnboardingFlowStore;

  // Reset to initial state
  useOnboardingFlowStore.setState({
    state: "not_started",
    plan: {},
    currentStepIndex: 0,
  });
});

describe("onboardingStore", () => {
  // ── Initial state ───────────────────────────────────────────────────

  it("initial state is not_started with empty plan", () => {
    const { state, plan, currentStepIndex } = useOnboardingFlowStore.getState();
    expect(state).toBe("not_started");
    expect(plan).toEqual({});
    expect(currentStepIndex).toBe(0);
  });

  // ── updatePlan ──────────────────────────────────────────────────────

  it("updatePlan() merges partial updates", () => {
    useOnboardingFlowStore.getState().updatePlan({ intent: "build_app" });
    useOnboardingFlowStore.getState().updatePlan({ codeSource: "new_project" });

    const { plan } = useOnboardingFlowStore.getState();
    expect(plan.intent).toBe("build_app");
    expect(plan.codeSource).toBe("new_project");
  });

  it("updatePlan() transitions state from not_started to in_progress", () => {
    expect(useOnboardingFlowStore.getState().state).toBe("not_started");

    useOnboardingFlowStore.getState().updatePlan({ intent: "explore" });

    expect(useOnboardingFlowStore.getState().state).toBe("in_progress");
  });

  it("updatePlan() does not change state if already in_progress", () => {
    useOnboardingFlowStore.getState().updatePlan({ intent: "explore" });
    expect(useOnboardingFlowStore.getState().state).toBe("in_progress");

    useOnboardingFlowStore.getState().updatePlan({ codeSource: "github_repo" });
    expect(useOnboardingFlowStore.getState().state).toBe("in_progress");
  });

  // ── Navigation ──────────────────────────────────────────────────────

  it("nextStep() increments currentStepIndex", () => {
    expect(useOnboardingFlowStore.getState().currentStepIndex).toBe(0);

    useOnboardingFlowStore.getState().nextStep();

    expect(useOnboardingFlowStore.getState().currentStepIndex).toBe(1);
  });

  it("nextStep() does not exceed visible steps length", () => {
    // With empty plan, only "goal" and "ready" are visible (workspace requires build_app)
    useOnboardingFlowStore.getState().nextStep(); // 0 → 1
    useOnboardingFlowStore.getState().nextStep(); // should stay at 1

    expect(useOnboardingFlowStore.getState().currentStepIndex).toBe(1);
  });

  it("prevStep() decrements currentStepIndex", () => {
    useOnboardingFlowStore.getState().nextStep(); // 0 → 1
    expect(useOnboardingFlowStore.getState().currentStepIndex).toBe(1);

    useOnboardingFlowStore.getState().prevStep();
    expect(useOnboardingFlowStore.getState().currentStepIndex).toBe(0);
  });

  it("prevStep() does not go below 0", () => {
    useOnboardingFlowStore.getState().prevStep();
    expect(useOnboardingFlowStore.getState().currentStepIndex).toBe(0);
  });

  // ── State transitions ──────────────────────────────────────────────

  it("skipOnboarding() sets state to skipped", () => {
    useOnboardingFlowStore.getState().skipOnboarding();
    expect(useOnboardingFlowStore.getState().state).toBe("skipped");
  });

  it("completeOnboarding() sets state to completed", () => {
    useOnboardingFlowStore.getState().completeOnboarding();
    expect(useOnboardingFlowStore.getState().state).toBe("completed");
  });

  it("reset() returns to initial state", () => {
    useOnboardingFlowStore.getState().updatePlan({ intent: "build_app" });
    useOnboardingFlowStore.getState().nextStep();
    useOnboardingFlowStore.getState().completeOnboarding();

    useOnboardingFlowStore.getState().reset();

    const { state, plan, currentStepIndex } = useOnboardingFlowStore.getState();
    expect(state).toBe("not_started");
    expect(plan).toEqual({});
    expect(currentStepIndex).toBe(0);
  });

  // ── Computed getters ───────────────────────────────────────────────

  it("visibleSteps() returns steps from registry filtered by plan", () => {
    // Empty plan: workspace step hidden
    let steps = useOnboardingFlowStore.getState().visibleSteps();
    expect(steps.map((s) => s.id)).toEqual(["goal", "ready"]);

    // With build_app: workspace step shown
    useOnboardingFlowStore.getState().updatePlan({ intent: "build_app" });
    steps = useOnboardingFlowStore.getState().visibleSteps();
    expect(steps.map((s) => s.id)).toEqual(["goal", "workspace", "ready"]);
  });

  it("currentStep() returns the step at currentStepIndex", () => {
    const step = useOnboardingFlowStore.getState().currentStep();
    expect(step?.id).toBe("goal");

    useOnboardingFlowStore.getState().nextStep();
    const nextStep = useOnboardingFlowStore.getState().currentStep();
    expect(nextStep?.id).toBe("ready");
  });

  it("currentStep() returns null when index is out of range", () => {
    useOnboardingFlowStore.setState({ currentStepIndex: 99 });
    expect(useOnboardingFlowStore.getState().currentStep()).toBeNull();
  });

  it("progress() returns correct category index and counts", () => {
    // Index 0 → goal step
    const p0 = useOnboardingFlowStore.getState().progress();
    expect(p0).toEqual({ current: 1, total: 2, categoryIndex: 0 });

    // Move to "ready" (start category, index 3 in category list)
    useOnboardingFlowStore.getState().nextStep();
    const p1 = useOnboardingFlowStore.getState().progress();
    expect(p1).toEqual({ current: 2, total: 2, categoryIndex: 3 });
  });
});
