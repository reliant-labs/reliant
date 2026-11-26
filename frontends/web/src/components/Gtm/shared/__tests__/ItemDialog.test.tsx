// Copyright (c) 2026 Reliant Labs

/**
 * The item drawer: Approve is a person's write on the revision on screen, a
 * conflict refreshes rather than overwrites, findings are CheckClaims' live
 * answer on the body, and an email has no send button, only its fields
 * read-only and how sending happens. The lifecycle's other moves (schedule,
 * pause, resume) show only where they are allowed; an edit of an approved
 * item says it goes back to review; a sent item is read-only; a refusal says
 * why.
 */

import { create } from "@bufbuild/protobuf";
import { Code, ConnectError } from "@connectrpc/connect";
import { cleanup, fireEvent, screen, waitFor, within } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";

import { GtmItemKind, GtmItemSchema, GtmItemStatus } from "@/gen/reliant/v1/gtm_pb";

import { timestampFromDate } from "@bufbuild/protobuf/wkt";

import { ItemDialog } from "../ItemDialog";
import { REVISION_CONFLICT_MESSAGE } from "../writes";
import { renderGtm, TEST_ORG } from "./gtmHarness";

const toasts = vi.hoisted(() => ({ warning: vi.fn(), error: vi.fn() }));
vi.mock("@/lib/toast-manager", () => ({ toast: toasts }));

afterEach(() => {
  cleanup();
  vi.clearAllMocks();
});

const post = create(GtmItemSchema, {
  id: "item-1",
  orgId: TEST_ORG,
  kind: GtmItemKind.BLOG_POST,
  channel: "blog",
  title: "Launch post",
  bodyMd: "Reliant runs on your own machine.",
  status: GtmItemStatus.REVIEW,
  revision: 3,
});

const email = create(GtmItemSchema, {
  ...post,
  id: "item-2",
  kind: GtmItemKind.EMAIL,
  channel: "email",
  title: "Beta invite",
  emailTo: ["ada@example.com"],
  emailSubject: "You're in",
});

const reads = {
  "GtmService.ListCampaigns": { response: { campaigns: [] } },
  "GtmService.CheckClaims": { response: { findings: [] } },
};

describe("ItemDialog", () => {
  it("approves on the revision on screen, as a person", async () => {
    const update = vi.fn(() => ({ item: { ...post, status: GtmItemStatus.APPROVED, revision: 4 } }));
    renderGtm(<ItemDialog item={post} onClose={() => {}} />, { ...reads, "GtmService.UpdateItem": update });
    fireEvent.click(await screen.findByRole("button", { name: "Approve: Launch post" }));
    const dialog = await screen.findByTestId("person-action-dialog-approve");
    expect(within(dialog).getByText("Only a person")).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Approve" }));
    await waitFor(() => expect(update).toHaveBeenCalled());
    expect(update).toHaveBeenCalledWith(expect.objectContaining({ orgId: TEST_ORG, id: "item-1", expectedRevision: 3, status: GtmItemStatus.APPROVED }));
  });

  it("says a conflict happened and reads again, rather than overwriting", async () => {
    const { queryClient } = renderGtm(<ItemDialog item={post} onClose={() => {}} />, {
      ...reads,
      "GtmService.UpdateItem": { error: new ConnectError("revision moved", Code.Aborted) },
    });
    const invalidate = vi.spyOn(queryClient, "invalidateQueries");
    fireEvent.click(await screen.findByRole("button", { name: "Approve: Launch post" }));
    fireEvent.click(await screen.findByRole("button", { name: "Approve" }));
    await waitFor(() => expect(toasts.warning).toHaveBeenCalledWith(REVISION_CONFLICT_MESSAGE));
    expect(invalidate).toHaveBeenCalled();
    expect(toasts.error).not.toHaveBeenCalled();
  });

  it("checks the body's claims live, and counts what blocks", async () => {
    const check = vi.fn(() => ({
      findings: [
        { mention: "your own machine", blocking: false, claimText: "Runs on the user's machine" },
        { mention: "SOC 2", blocking: true, reason: "unverified" },
      ],
      blocking: true,
    }));
    renderGtm(<ItemDialog item={post} onClose={() => {}} />, { ...reads, "GtmService.CheckClaims": check });
    expect(await screen.findByText("· 1 blocking")).toBeInTheDocument();
    expect(screen.getByText("SOC 2")).toBeInTheDocument();
    expect(check).toHaveBeenCalledWith(expect.objectContaining({ orgId: TEST_ORG, text: post.bodyMd }));
  });

  it("shows an email's recipients and subject read-only, with no send button", async () => {
    renderGtm(<ItemDialog item={email} onClose={() => {}} />, reads);
    expect(await screen.findByText("ada@example.com")).toBeInTheDocument();
    expect(screen.getByText("You're in")).toBeInTheDocument();
    expect(screen.getByTestId("gtm-email-send-note")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /send/i })).not.toBeInTheDocument();
    expect(screen.queryByRole("textbox")).not.toBeInTheDocument();
  });

  it("edits an approved item, saying it goes back to review", async () => {
    const approved = create(GtmItemSchema, { ...post, status: GtmItemStatus.APPROVED });
    const update = vi.fn(() => ({ item: { ...approved, status: GtmItemStatus.REVIEW, revision: 4 } }));
    renderGtm(<ItemDialog item={approved} onClose={() => {}} />, { ...reads, "GtmService.UpdateItem": update });
    fireEvent.click(await screen.findByRole("button", { name: "Edit" }));
    expect(screen.getByTestId("gtm-item-edit-review-note")).toHaveTextContent("back to review");
    fireEvent.change(screen.getByLabelText("Body"), { target: { value: "New copy." } });
    fireEvent.click(screen.getByRole("button", { name: "Save" }));
    await waitFor(() => expect(update).toHaveBeenCalled());
    expect(update).toHaveBeenCalledWith(expect.objectContaining({ id: "item-1", expectedRevision: 3, bodyMd: "New copy." }));
    expect(update).toHaveBeenCalledWith(expect.not.objectContaining({ status: expect.anything() }));
  });

  it("schedules an approved, dated post, and pauses; an approved email has no schedule", async () => {
    const dated = { scheduledAt: timestampFromDate(new Date(2026, 10, 30, 9)) };
    const approvedPost = create(GtmItemSchema, { ...post, ...dated, status: GtmItemStatus.APPROVED });
    const update = vi.fn(() => ({ item: approvedPost }));
    renderGtm(<ItemDialog item={approvedPost} onClose={() => {}} />, { ...reads, "GtmService.UpdateItem": update });
    fireEvent.click(await screen.findByRole("button", { name: "Mark scheduled" }));
    await waitFor(() => expect(update).toHaveBeenCalledWith(expect.objectContaining({ status: GtmItemStatus.SCHEDULED, expectedRevision: 3 })));
    fireEvent.click(screen.getByRole("button", { name: "Pause" }));
    await waitFor(() => expect(update).toHaveBeenCalledWith(expect.objectContaining({ status: GtmItemStatus.PAUSED })));
    cleanup();

    renderGtm(<ItemDialog item={create(GtmItemSchema, { ...email, ...dated, status: GtmItemStatus.APPROVED })} onClose={() => {}} />, reads);
    expect(await screen.findByRole("button", { name: "Pause" })).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Mark scheduled" })).not.toBeInTheDocument();
  });

  it("resumes a paused item as a draft", async () => {
    const update = vi.fn(() => ({ item: post }));
    renderGtm(<ItemDialog item={create(GtmItemSchema, { ...post, status: GtmItemStatus.PAUSED })} onClose={() => {}} />, { ...reads, "GtmService.UpdateItem": update });
    fireEvent.click(await screen.findByRole("button", { name: "Resume as draft" }));
    await waitFor(() => expect(update).toHaveBeenCalledWith(expect.objectContaining({ status: GtmItemStatus.DRAFT })));
    expect(screen.queryByRole("button", { name: "Pause" })).not.toBeInTheDocument();
  });

  it("keeps a sent email read-only", async () => {
    renderGtm(<ItemDialog item={create(GtmItemSchema, { ...email, status: GtmItemStatus.SENT, sentBy: "Ada" })} onClose={() => {}} />, reads);
    expect(await screen.findByText(/read-only/)).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Edit" })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Pause" })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /approve/i })).not.toBeInTheDocument();
  });

  it("says why a refused change was refused", async () => {
    renderGtm(<ItemDialog item={email} onClose={() => {}} />, {
      ...reads,
      "GtmService.UpdateItem": { error: new ConnectError("the email is held while a send is in doubt", Code.FailedPrecondition) },
    });
    fireEvent.click(await screen.findByRole("button", { name: "Pause" }));
    await waitFor(() => expect(toasts.warning).toHaveBeenCalledWith("This change isn't possible right now. The email is held while a send is in doubt."));
    expect(toasts.error).not.toHaveBeenCalled();
  });

  it("offers no approval once an item is out", async () => {
    renderGtm(<ItemDialog item={create(GtmItemSchema, { ...post, status: GtmItemStatus.PUBLISHED })} onClose={() => {}} />, reads);
    expect(await screen.findByTestId("gtm-item-dialog")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /approve/i })).not.toBeInTheDocument();
  });
});
