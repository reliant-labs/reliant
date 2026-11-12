import { describe, expect, it } from "vitest";

import {
  BUILD_APP_PROMPT,
  START_APP,
  START_CHAT,
  START_REPO,
  START_VALUES,
  isStartValue,
  startForIntent,
  starterPrompt,
} from "../appStart";
import { indexSearchSchema } from "@/routeSchemas";

describe("first-message starters", () => {
  it("every intent onboarding can finish with has a starter", () => {
    expect(startForIntent("build_app")).toBe(START_APP);
    expect(startForIntent("existing_codebase")).toBe(START_REPO);
    expect(startForIntent("chat")).toBe(START_CHAT);
    expect(startForIntent(undefined)).toBe(START_CHAT);
  });

  it("the app starter is the unchanged build prompt", () => {
    expect(starterPrompt(START_APP, "ignored")).toBe(BUILD_APP_PROMPT);
  });

  it("the repo starter is the 'deploy with forge' prompt, tied to the project", () => {
    const text = starterPrompt(START_REPO, "storefront");
    expect(text).toMatch(/Deploy storefront with forge/);
    expect(text).toMatch(/live URL/);
  });

  it("the chat starter asks about the project", () => {
    expect(starterPrompt(START_CHAT, "storefront")).toMatch(/storefront/);
  });

  it("falls back to wording that still reads when no project is known", () => {
    expect(starterPrompt(START_REPO)).toMatch(/Deploy this project with forge/);
    expect(starterPrompt(START_CHAT, "   ")).toMatch(/this project/);
  });

  it("the route accepts exactly the values the starters define", () => {
    for (const value of START_VALUES) {
      expect(indexSearchSchema.safeParse({ start: value }).success).toBe(true);
      expect(isStartValue(value)).toBe(true);
    }
    expect(indexSearchSchema.safeParse({ start: "nope" }).success).toBe(false);
    expect(isStartValue("nope")).toBe(false);
  });
});
