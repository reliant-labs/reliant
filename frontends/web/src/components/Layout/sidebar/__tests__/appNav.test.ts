import { describe, expect, it } from "vitest";

import type { GroupedNavInput } from "../../../Forge/Console/groupedNav";
import { appActivityOf, appNavFor } from "../AppsNavSection";

// The model's page off the console defaults to the Overview (useConsoleNavModel).
const model: GroupedNavInput = { project: "shop", page: { kind: "overview" }, env: "prod", envs: [{ name: "prod", preview: false }] };
const current = (pathname: string) => appNavFor(model, pathname).items.filter((item) => item.active).map((item) => item.label);

describe("the project shell's app rows", () => {
  it("light no row on a chat page, the Inbox or a project's Home", () => {
    expect(current("/c/chat-1")).toEqual([]);
    expect(current("/o/acme/c/chat-1/t/th-1")).toEqual([]);
    expect(current("/inbox")).toEqual([]);
    expect(current("/projects/p-1")).toEqual([]);
    expect(appActivityOf(model, "/c/chat-1")?.active).toBe(false);
  });

  it("light Overview on the app's overview page", () => {
    expect(current("/forge/p/shop")).toEqual(["Overview"]);
    expect(current("/o/acme/apps/shop")).toEqual(["Overview"]);
  });
});
