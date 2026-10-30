// Copyright (c) 2025 Reliant Labs

import { act, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { Code, ConnectError } from "@connectrpc/connect";
import { QueryClientProvider } from "@tanstack/react-query";
import { describe, expect, it, vi } from "vitest";

import { issueKeys } from "@/hooks/observability-queries";
import { createTestQueryClient } from "@/test/renderWithQuery";
import { ObsQuotaProvider } from "@/services/controlPlane/observability/ObsQuotaContext";
import { ObservabilityApiProvider, type ObservabilityApi } from "@/services/controlPlane/observability/ObservabilityApiContext";
import { quotaPausedError } from "@/services/controlPlane/observability/quota";
import { createFixtureApi } from "../../__fixtures__/fixtureApi";
import { ObsQuotaBanner } from "../../ObsStates";
import { IssueDetail } from "../IssueDetail";

vi.mock("uplot", async () => (await import("../../__tests__/obsMocks")).obsMocks.uplot());

function rig(api: ObservabilityApi) {
  const client = createTestQueryClient();
  // Keep inactive views cached, as production does, so revocation must scrub them too.
  client.setDefaultOptions({ queries: { ...client.getDefaultOptions().queries, gcTime: Infinity } });
  render(
    <QueryClientProvider client={client}>
      <ObservabilityApiProvider api={api}>
        <ObsQuotaProvider>
          <ObsQuotaBanner />
          <IssueDetail issueId="iss-orders-deadline" scope={{ orgId: "org-fixture", envIds: ["env-prod"], filters: [] }} subject="production" range="1h" navigate={vi.fn()} onBack={vi.fn()} />
        </ObsQuotaProvider>
      </ObservabilityApiProvider>
    </QueryClientProvider>
  );
  return client;
}

async function ready() {
  await waitFor(() => expect(screen.getByTestId("obs-issue")).toHaveAttribute("data-state", "ready"));
  await screen.findByTestId("obs-issue-timeline-list");
}

describe("issue timeline recovery preserves main's quota and retention behavior", () => {
  it("keeps the loaded detail and discussion, marked as of, after a quota refusal", async () => {
    const api = createFixtureApi();
    const getIssue = api.getIssue;
    let exhausted = false;
    api.getIssue = (req) => exhausted
      ? Promise.reject(quotaPausedError({ limit: "queries", lane: "interactive", resetsAtMs: Date.now() + 60_000, soft: false }))
      : getIssue(req);
    const client = rig(api);
    await ready();
    const timeline = screen.getByTestId("obs-issue-timeline-list");
    exhausted = true;
    await act(async () => { await client.invalidateQueries({ queryKey: issueKeys.detail("iss-orders-deadline", "1h") }); });
    await waitFor(() => expect(client.getQueryState(issueKeys.detail("iss-orders-deadline", "1h"))?.error).toBeTruthy());
    expect(screen.getByTestId("obs-issue")).toHaveAttribute("data-state", "ready");
    expect(screen.getByTestId("obs-issue-timeline-list")).toBe(timeline);
    expect(screen.getByTestId("obs-as-of")).toHaveTextContent("as of");
  });

  it("still replaces cached detail on a non-quota refusal", async () => {
    const api = createFixtureApi();
    const client = rig(api);
    await ready();
    api.getIssue = async () => { throw new Error("permission refused"); };
    await act(async () => { await client.invalidateQueries({ queryKey: issueKeys.detail("iss-orders-deadline", "1h") }); });
    await waitFor(() => expect(screen.getByTestId("obs-issue")).toHaveAttribute("data-state", "error"));
    expect(screen.queryByTestId("obs-issue-timeline-list")).not.toBeInTheDocument();
  });

  it.each([Code.PermissionDenied, Code.Unauthenticated])("scrubs cached comments, inactive detail ranges and events after authorization refusal %s", async (code) => {
    const api = createFixtureApi();
    const client = rig(api);
    await ready();
    const detail = client.getQueryData(issueKeys.detail("iss-orders-deadline", "1h"));
    client.setQueryData(issueKeys.detail("iss-orders-deadline", "6h"), detail);
    client.setQueryData(issueKeys.timeline("other-org-issue"), { pages: ["other-org-data"], pageParams: [null] });
    api.listIssueTimeline = async () => { throw new ConnectError("membership revoked", code); };
    await act(async () => { await client.invalidateQueries({ queryKey: issueKeys.timeline("iss-orders-deadline") }); });
    await waitFor(() => expect(screen.getByTestId("obs-issue")).toHaveAttribute("data-state", "error"));
    for (const key of [issueKeys.timeline("iss-orders-deadline"), issueKeys.events("iss-orders-deadline"), issueKeys.detail("iss-orders-deadline", "1h"), issueKeys.detail("iss-orders-deadline", "6h")]) {
      expect(client.getQueryData(key)).toBeUndefined();
      expect(client.getQueryState(key)?.error).toBeInstanceOf(ConnectError);
    }
    expect(screen.queryByTestId("obs-comment-body")).not.toBeInTheDocument();
    expect(client.getQueryData(issueKeys.timeline("other-org-issue"))).toEqual({ pages: ["other-org-data"], pageParams: [null] });
    // Re-entry cannot reuse the previously cached discussion.
    await act(async () => { await client.invalidateQueries({ queryKey: issueKeys.detail("iss-orders-deadline", "1h") }); });
    await waitFor(() => expect(screen.getByTestId("obs-issue")).toHaveAttribute("data-state", "error"));
    expect(client.getQueryData(issueKeys.timeline("iss-orders-deadline"))).toBeUndefined();
    expect(screen.queryByTestId("obs-comment-body")).not.toBeInTheDocument();
  });

  it("does not resurrect cached discussion from a late response after a sibling authorization refusal", async () => {
    const api = createFixtureApi();
    const timeline = await api.listIssueTimeline({ issueId: "iss-orders-deadline" });
    const client = rig(api);
    await ready();
    let resolve!: (value: typeof timeline) => void;
    api.listIssueTimeline = () => new Promise((done) => { resolve = done; });
    const pending = client.invalidateQueries({ queryKey: issueKeys.timeline("iss-orders-deadline") });
    await waitFor(() => expect(resolve).toBeTypeOf("function"));
    api.getIssue = async () => { throw new ConnectError("revoked", Code.PermissionDenied); };
    await act(async () => { await client.invalidateQueries({ queryKey: issueKeys.detail("iss-orders-deadline", "1h") }); });
    await act(async () => { resolve(timeline); await pending; });
    expect(client.getQueryData(issueKeys.timeline("iss-orders-deadline"))).toBeUndefined();
    await waitFor(() => expect(screen.queryByTestId("obs-comment-body")).not.toBeInTheDocument());
  });

  it("blocks same-tick keyboard/form overlaps, retries after refusal and keeps text typed during a save", async () => {
    const api = createFixtureApi();
    rig(api);
    await ready();
    const original = api.addIssueComment;
    let resolve!: (value: Awaited<ReturnType<typeof original>>) => void;
    let reject!: (error: Error) => void;
    const add = vi.fn(() => new Promise<Awaited<ReturnType<typeof original>>>((yes, no) => { resolve = yes; reject = no; }));
    api.addIssueComment = add;
    const box = screen.getByTestId("obs-comment-box");
    fireEvent.change(box, { target: { value: "first comment" } });
    act(() => {
      fireEvent.keyDown(box, { key: "Enter", ctrlKey: true });
      fireEvent.keyDown(box, { key: "Enter", ctrlKey: true });
      fireEvent.submit(screen.getByTestId("obs-comment-composer"));
      fireEvent.click(screen.getByTestId("obs-comment-submit"));
    });
    await waitFor(() => expect(add).toHaveBeenCalledTimes(1));
    await act(async () => { reject(new Error("temporarily refused")); });
    expect(await screen.findByTestId("obs-comment-add-error")).toBeInTheDocument();
    expect(box).toHaveValue("first comment");
    fireEvent.click(screen.getByTestId("obs-comment-submit"));
    await waitFor(() => expect(add).toHaveBeenCalledTimes(2));
    fireEvent.change(box, { target: { value: "next comment while saving" } });
    const saved = await original({ issueId: "iss-orders-deadline", body: "first comment" });
    await act(async () => { resolve(saved); });
    await waitFor(() => expect(screen.getByTestId("obs-issue-timeline-list")).toHaveTextContent("first comment"));
    expect(box).toHaveValue("next comment while saving");
    fireEvent.keyDown(box, { key: "Enter", ctrlKey: true });
    await waitFor(() => expect(add).toHaveBeenCalledTimes(3));
    const next = await original({ issueId: "iss-orders-deadline", body: "next comment while saving" });
    await act(async () => { resolve(next); });
    await waitFor(() => expect(box).toHaveValue(""));
    expect(screen.getByTestId("obs-issue-timeline-list")).toHaveTextContent("next comment while saving");
  });

  it("keeps the timeline when the latest event has aged out, without inventing an event", async () => {
    const api = createFixtureApi();
    const getIssue = api.getIssue;
    api.getIssue = async (req) => ({ ...await getIssue(req), latestEvent: null, frames: [] });
    rig(api);
    await ready();
    expect(within(screen.getByTestId("obs-issue-latest")).getByText(/aged out of retention/)).toBeInTheDocument();
    expect(screen.getByTestId("obs-issue-counts")).toBeInTheDocument();
    expect(screen.queryByTestId("obs-issue-latest-message")).not.toBeInTheDocument();
  });
});
