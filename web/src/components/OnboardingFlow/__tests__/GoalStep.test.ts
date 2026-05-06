import { describe, expect, it } from "vitest";
import type { LaunchPlan } from "../types";
import {
  STEP_PATHS,
  INITIAL_PATH,
  AFTER_GOAL_PATH,
  derivePath,
} from "../steps";

describe("derivePath", () => {
  it("returns INITIAL_PATH when no intent is set", () => {
    expect(derivePath({})).toEqual(INITIAL_PATH);
  });

  it("returns AFTER_GOAL_PATH when intent is set but compute is not", () => {
    expect(derivePath({ intent: "build_app" })).toEqual(AFTER_GOAL_PATH);
  });

  // Cloud paths
  it("cloud + build_app → cloud_new_app", () => {
    const plan: Partial<LaunchPlan> = { intent: "build_app", compute: "cloud_free_trial" };
    expect(derivePath(plan)).toEqual(STEP_PATHS.cloud_new_app);
  });

  it("cloud + existing_codebase → cloud_existing", () => {
    const plan: Partial<LaunchPlan> = { intent: "existing_codebase", compute: "cloud_free_trial" };
    expect(derivePath(plan)).toEqual(STEP_PATHS.cloud_existing);
  });

  it("cloud + explore → cloud_explore", () => {
    const plan: Partial<LaunchPlan> = { intent: "explore", compute: "cloud_free_trial" };
    expect(derivePath(plan)).toEqual(STEP_PATHS.cloud_explore);
  });

  it("cloud + landing_page → cloud_landing_page", () => {
    const plan: Partial<LaunchPlan> = { intent: "landing_page", compute: "cloud_free_trial" };
    expect(derivePath(plan)).toEqual(STEP_PATHS.cloud_landing_page);
  });

  it("cloud + pitch_deck → cloud_pitch_deck", () => {
    const plan: Partial<LaunchPlan> = { intent: "pitch_deck", compute: "cloud_free_trial" };
    expect(derivePath(plan)).toEqual(STEP_PATHS.cloud_pitch_deck);
  });

  it("cloud + blog_post → cloud_blog_post", () => {
    const plan: Partial<LaunchPlan> = { intent: "blog_post", compute: "cloud_free_trial" };
    expect(derivePath(plan)).toEqual(STEP_PATHS.cloud_blog_post);
  });

  // Local paths
  it("local + build_app → local_new_app", () => {
    const plan: Partial<LaunchPlan> = { intent: "build_app", compute: "local_daemon" };
    expect(derivePath(plan)).toEqual(STEP_PATHS.local_new_app);
  });

  it("local + existing_codebase → local_existing", () => {
    const plan: Partial<LaunchPlan> = { intent: "existing_codebase", compute: "local_daemon" };
    expect(derivePath(plan)).toEqual(STEP_PATHS.local_existing);
  });

  it("local + explore → local_explore", () => {
    const plan: Partial<LaunchPlan> = { intent: "explore", compute: "local_daemon" };
    expect(derivePath(plan)).toEqual(STEP_PATHS.local_explore);
  });

  it("local + landing_page → local_landing_page", () => {
    const plan: Partial<LaunchPlan> = { intent: "landing_page", compute: "local_daemon" };
    expect(derivePath(plan)).toEqual(STEP_PATHS.local_landing_page);
  });

  it("local + pitch_deck → local_pitch_deck", () => {
    const plan: Partial<LaunchPlan> = { intent: "pitch_deck", compute: "local_daemon" };
    expect(derivePath(plan)).toEqual(STEP_PATHS.local_pitch_deck);
  });

  it("local + blog_post → local_blog_post", () => {
    const plan: Partial<LaunchPlan> = { intent: "blog_post", compute: "local_daemon" };
    expect(derivePath(plan)).toEqual(STEP_PATHS.local_blog_post);
  });

  // Pre-connected daemon paths
  it("preconnected + build_app → preconnected_new_app", () => {
    const plan: Partial<LaunchPlan> = { intent: "build_app", compute: "local_daemon", daemonPreConnected: true };
    expect(derivePath(plan)).toEqual(STEP_PATHS.preconnected_new_app);
  });

  it("preconnected + existing_codebase → preconnected_existing", () => {
    const plan: Partial<LaunchPlan> = { intent: "existing_codebase", compute: "local_daemon", daemonPreConnected: true };
    expect(derivePath(plan)).toEqual(STEP_PATHS.preconnected_existing);
  });

  it("preconnected + explore → preconnected_explore", () => {
    const plan: Partial<LaunchPlan> = { intent: "explore", compute: "local_daemon", daemonPreConnected: true };
    expect(derivePath(plan)).toEqual(STEP_PATHS.preconnected_explore);
  });

  it("preconnected + landing_page → preconnected_landing_page", () => {
    const plan: Partial<LaunchPlan> = { intent: "landing_page", compute: "local_daemon", daemonPreConnected: true };
    expect(derivePath(plan)).toEqual(STEP_PATHS.preconnected_landing_page);
  });

  it("preconnected + pitch_deck → preconnected_pitch_deck", () => {
    const plan: Partial<LaunchPlan> = { intent: "pitch_deck", compute: "local_daemon", daemonPreConnected: true };
    expect(derivePath(plan)).toEqual(STEP_PATHS.preconnected_pitch_deck);
  });

  it("preconnected + blog_post → preconnected_blog_post", () => {
    const plan: Partial<LaunchPlan> = { intent: "blog_post", compute: "local_daemon", daemonPreConnected: true };
    expect(derivePath(plan)).toEqual(STEP_PATHS.preconnected_blog_post);
  });

  // Path structure verification
  it("cloud new app includes forge-style step", () => {
    const path = STEP_PATHS.cloud_new_app;
    expect(path).toContain("forge-style");
    expect(path).toContain("cloud-project-location");
  });

  it("local new app includes daemon-connect and forge-style", () => {
    const path = STEP_PATHS.local_new_app;
    expect(path).toContain("daemon-connect");
    expect(path).toContain("local-project-location");
    expect(path).toContain("forge-style");
  });

  it("cloud existing uses github-connect not project-location", () => {
    const path = STEP_PATHS.cloud_existing;
    expect(path).toContain("github-connect");
    expect(path).not.toContain("cloud-project-location");
    expect(path).not.toContain("local-project-location");
  });

  it("preconnected paths skip daemon-connect", () => {
    for (const [key, path] of Object.entries(STEP_PATHS)) {
      if (key.startsWith("preconnected_")) {
        expect(path).not.toContain("daemon-connect");
      }
    }
  });

  it("all paths start with goal and compute", () => {
    for (const path of Object.values(STEP_PATHS)) {
      expect(path[0]).toBe("goal");
      expect(path[1]).toBe("compute");
    }
  });

  it("all paths end with model", () => {
    for (const path of Object.values(STEP_PATHS)) {
      expect(path[path.length - 1]).toBe("model");
    }
  });

  it("cloud_paid compute maps to local prefix", () => {
    // cloud_paid is not cloud_free_trial, so it maps to "local" prefix
    expect(derivePath({ intent: "build_app", compute: "cloud_paid" })).toEqual(
      STEP_PATHS.local_new_app,
    );
  });
});