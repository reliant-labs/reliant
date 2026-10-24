/**
 * ApprovalService reads and writes share the generated key scope, so a
 * mounted approval list is never left stale by a write:
 *
 *  - every approve/deny (single, batch, the keyboard shortcut) flips the
 *    status at once, then refetches what the server recorded;
 *  - a failed write rolls the optimistic flip back;
 *  - usePendingApprovals reads the same entry as useApprovals, so one patch
 *    or refetch updates both.
 */

import { Code, ConnectError } from "@connectrpc/connect";
import { QueryClientProvider } from "@tanstack/react-query";
import { act, cleanup, renderHook, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it } from "vitest";
import type { ReactNode } from "react";

import { ApprovalStatus } from "@/gen/reliant/v1/approval_pb";
import { setTransport } from "@/lib/connect";
import { queryClient } from "@/lib/query-client";
import { mockTransport } from "@/lib/test-utils";
import {
  approveAllPendingApprovals,
  useApprovals,
  useApproveToolRequest,
  useBatchDenyInChat,
  usePendingApprovals,
} from "../approval-queries";
import { approvalServiceKeys } from "../approval-service-hooks_gen";

function wrapper({ children }: { children: ReactNode }) {
  return <QueryClientProvider client={queryClient}>{children}</QueryClientProvider>;
}

type Row = { id: string; chatId: string; status: ApprovalStatus; title: string };
let server: Row[];
let listCalls: number;
let failWrites: boolean;

function resolveOnServer(ids: string[], status: ApprovalStatus) {
  if (failWrites) throw new ConnectError("nope", Code.FailedPrecondition);
  server = server.map((a) => (ids.includes(a.id) ? { ...a, status } : a));
}

beforeEach(() => {
  server = [
    { id: "a1", chatId: "c1", status: ApprovalStatus.PENDING, title: "Deploy?" },
    { id: "a2", chatId: "c1", status: ApprovalStatus.PENDING, title: "Migrate?" },
  ];
  listCalls = 0;
  failWrites = false;
  setTransport(
    mockTransport({
      "ApprovalService.ListApprovalsByChat": () => {
        listCalls++;
        return { approvals: server, total: server.length };
      },
      "ApprovalService.Approve": (req) => {
        resolveOnServer([(req as { requestId: string }).requestId], ApprovalStatus.APPROVED);
        return { success: true };
      },
      "ApprovalService.BatchApprove": (req) => {
        resolveOnServer((req as { requestIds: string[] }).requestIds, ApprovalStatus.APPROVED);
        return { success: true };
      },
      "ApprovalService.BatchDeny": (req) => {
        resolveOnServer((req as { requestIds: string[] }).requestIds, ApprovalStatus.DENIED);
        return { success: true };
      },
    }),
  );
});

afterEach(() => {
  cleanup();
  queryClient.clear();
  setTransport(null);
});

const statuses = (data: { approvals: { id: string; status: ApprovalStatus }[] } | undefined) =>
  data?.approvals.map((a) => [a.id, a.status]);

describe("ApprovalService keys", () => {
  it("approving refetches every mounted read of the chat's list", async () => {
    const all = renderHook(() => useApprovals("c1"), { wrapper });
    const pending = renderHook(() => usePendingApprovals("c1"), { wrapper });
    await waitFor(() => expect(pending.result.current.data?.approvals).toHaveLength(2));
    expect(listCalls).toBe(1); // one cache entry, shared

    const approve = renderHook(() => useApproveToolRequest("c1"), { wrapper });
    await act(() => approve.result.current.mutateAsync({ requestId: "a1" }));

    await waitFor(() => expect(listCalls).toBe(2));
    await waitFor(() => expect(pending.result.current.data?.approvals.map((a) => a.id)).toEqual(["a2"]));
    expect(statuses(all.result.current.data)).toEqual([
      ["a1", ApprovalStatus.APPROVED],
      ["a2", ApprovalStatus.PENDING],
    ]);
  });

  it("a batch deny that fails rolls the optimistic flip back", async () => {
    const all = renderHook(() => useApprovals("c1"), { wrapper });
    await waitFor(() => expect(all.result.current.isSuccess).toBe(true));

    failWrites = true;
    const deny = renderHook(() => useBatchDenyInChat("c1"), { wrapper });
    await act(async () => {
      await deny.result.current.mutateAsync({ requestIds: ["a1", "a2"] }).catch(() => undefined);
    });

    expect(statuses(all.result.current.data)).toEqual([
      ["a1", ApprovalStatus.PENDING],
      ["a2", ApprovalStatus.PENDING],
    ]);
  });

  it("the keyboard shortcut's approve-all refetches too", async () => {
    const pending = renderHook(() => usePendingApprovals("c1"), { wrapper });
    await waitFor(() => expect(pending.result.current.data?.approvals).toHaveLength(2));

    await approveAllPendingApprovals("c1");

    await waitFor(() => expect(listCalls).toBe(2));
    expect(pending.result.current.data?.approvals).toEqual([]);
  });

  it("approvalServiceKeys.all reaches the chat's list", async () => {
    renderHook(() => useApprovals("c1"), { wrapper });
    await waitFor(() => expect(listCalls).toBe(1));

    await queryClient.invalidateQueries({ queryKey: approvalServiceKeys.all });
    await waitFor(() => expect(listCalls).toBe(2));
  });
});
