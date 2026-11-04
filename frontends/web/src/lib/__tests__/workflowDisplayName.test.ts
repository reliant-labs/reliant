import { afterEach, describe, expect, it } from "vitest";
import {
  formatWorkflowSlug,
  getCachedWorkflowDisplayName,
  workflowDisplayName,
} from "../workflowDisplayName";
import { workflowServiceKeys } from "../../hooks/workflow-service-hooks_gen";
import { queryClient } from "../query-client";
import { useProjectStore } from "../../store/projectStore";

afterEach(() => {
  queryClient.clear();
  useProjectStore.setState({ currentProject: null });
});

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
    // The current project's chat-safe list, as the generated ListWorkflows read caches it.
    useProjectStore.setState({ currentProject: { id: "p1", name: "p1" } as never });
    queryClient.setQueryData(workflowServiceKeys.listWorkflows({ projectId: "p1", includeHidden: false }), {
      workflows: [{ name: "forge-one-shot", title: "Build an App" }],
      invalidWorkflows: [],
    });
    expect(getCachedWorkflowDisplayName("builtin://forge-one-shot")).toBe("Build an App");
    expect(getCachedWorkflowDisplayName("builtin://unknown-flow")).toBe("Unknown Flow");
  });
});
