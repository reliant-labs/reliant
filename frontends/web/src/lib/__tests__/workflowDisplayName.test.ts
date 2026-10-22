import { describe, expect, it } from "vitest";
import {
  formatWorkflowSlug,
  getCachedWorkflowDisplayName,
  workflowDisplayName,
} from "../workflowDisplayName";
import { useGlobalDataStore } from "../../store/globalDataStore";

describe("workflowDisplayName", () => {
  it("prefers the title", () => {
    expect(workflowDisplayName({ name: "forge-one-shot", title: "Build an App" })).toBe("Build an App");
  });

  it("falls back to the title-cased slug when title is empty or missing", () => {
    expect(workflowDisplayName({ name: "get-it-right" })).toBe("Get It Right");
    expect(workflowDisplayName({ name: "get-it-right", title: "  " })).toBe("Get It Right");
  });

  it("strips the builtin:// prefix when formatting", () => {
    expect(formatWorkflowSlug("builtin://forge-one-shot")).toBe("Forge One Shot");
    expect(formatWorkflowSlug("my_flow")).toBe("My Flow");
  });

  it("resolves a ref's title from the cached workflow list", () => {
    const workflows = [
      { name: "forge-one-shot", title: "Build an App" },
    ] as ReturnType<typeof useGlobalDataStore.getState>["workflows"];
    useGlobalDataStore.setState({ workflows });
    expect(getCachedWorkflowDisplayName("builtin://forge-one-shot")).toBe("Build an App");
    expect(getCachedWorkflowDisplayName("builtin://unknown-flow")).toBe("Unknown Flow");
  });
});
