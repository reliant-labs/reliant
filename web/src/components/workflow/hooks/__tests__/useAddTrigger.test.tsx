/**
 * "Add trigger" is one picker. On a workflow you can edit, a pick DECLARES
 * the trigger in the definition; on a built-in (read-only definition) the
 * same pick becomes your PERSONAL trigger, a row with that source inline.
 */

import { describe, expect, it, vi } from "vitest";
import { act, renderHook } from "@testing-library/react";
import type { ReactNode } from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";

const getCatalogEntry = vi.fn();
vi.mock("@/api/grpc-client", () => ({ grpcClient: { catalog: () => ({ getCatalogEntry }) } }));

import { useAddTrigger } from "../useAddTrigger";
import { connectionKeys } from "@/hooks/connection-queries";
import type { CatalogEntrySummary } from "@/api/catalog-search-grpc";
import type { DeclaredTrigger } from "@/lib/declaredTriggers";

const existing = [{ name: "schedule", source: { case: "schedule", value: { cron: ["0 9 * * *"] } } }] as unknown as DeclaredTrigger[];

const issueOpened = {
  ref: "github/issue.opened@1",
  id: "issue.opened",
  summary: "A new issue",
  integration: { id: "github", displayName: "GitHub" },
} as unknown as CatalogEntrySummary;

function setup(canEditDefinition: boolean) {
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  // The catalog entry is already cached, as the palette leaves it: no RPC.
  queryClient.setQueryData(connectionKeys.catalogEntry(issueOpened.ref), {
    payloadSchema: { properties: { event: { enum: ["issues.opened", "issues.reopened"] } } },
  });
  const declare = vi.fn();
  const closePalette = vi.fn();
  const wrapper = ({ children }: { children: ReactNode }) => <QueryClientProvider client={queryClient}>{children}</QueryClientProvider>;
  const hook = renderHook(() => useAddTrigger({ canEditDefinition, declared: existing, declare, closePalette }), { wrapper });
  return { hook, declare, closePalette };
}

describe("useAddTrigger", () => {
  it("declares a picked trigger on your own workflow, uniquely named", () => {
    const { hook, declare, closePalette } = setup(true);
    act(() => hook.result.current.chooseBuiltin("schedule"));
    expect(closePalette).toHaveBeenCalled();
    expect(declare).toHaveBeenCalledWith(expect.objectContaining({ name: "schedule-2", source: expect.objectContaining({ case: "schedule" }) }), undefined);
    expect(hook.result.current.personal).toBeNull();
  });

  it("declares an integration trigger with the type's events and its catalog ref", async () => {
    const { hook, declare } = setup(true);
    await act(() => hook.result.current.chooseCatalog(issueOpened));
    expect(declare).toHaveBeenCalledWith(
      expect.objectContaining({
        name: "issue-opened",
        source: { case: "integration", value: expect.objectContaining({ integration: "github", events: ["issues.opened", "issues.reopened"] }) },
      }),
      "github/issue.opened@1",
    );
    expect(getCatalogEntry).not.toHaveBeenCalled();
  });

  it("on a built-in, the same pick becomes a personal trigger instead of a declaration", async () => {
    const { hook, declare, closePalette } = setup(false);
    await act(() => hook.result.current.chooseCatalog(issueOpened));
    expect(declare).not.toHaveBeenCalled();
    expect(closePalette).toHaveBeenCalled();
    expect(hook.result.current.personal).toMatchObject({
      catalogRef: "github/issue.opened@1",
      trigger: { source: { case: "integration", value: { integration: "github" } } },
    });
    act(() => hook.result.current.closePersonal());
    expect(hook.result.current.personal).toBeNull();
  });
});
