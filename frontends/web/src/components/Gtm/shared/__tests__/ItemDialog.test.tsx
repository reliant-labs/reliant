// Copyright (c) 2026 Reliant Labs

/**
 * The item drawer: Approve is a person's write on the revision on screen, a
 * conflict refreshes rather than overwrites, findings are CheckClaims' live
 * answer on the body, and an email has no send button, only its fields
 * read-only and how sending happens. The lifecycle's other moves (schedule,
 * pause, resume) show only where they are allowed; an edit of an approved
 * item says it goes back to review; a sent item is read-only; a refusal says
 * why. On a manual channel a person records the URL and marks it published;
 * a held email is resolved by a person; files upload in three steps and say
 * when storage is unavailable.
 */

import { create } from "@bufbuild/protobuf";
import { Code, ConnectError } from "@connectrpc/connect";
import { cleanup, fireEvent, screen, waitFor, within } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";

import {
  GtmAssetSchema,
  GtmAssetSource,
  GtmAssetStatus,
  GtmChannelMode,
  GtmChannelSchema,
  GtmChannelStatus,
  GtmItemFormat,
  GtmItemSchema,
  GtmItemStatus,
} from "@/gen/reliant/v1/gtm_pb";
import { useGetItem } from "@/hooks/gtm-service-hooks_gen";

import { timestampFromDate } from "@bufbuild/protobuf/wkt";

import { ItemDialog } from "../ItemDialog";
import { REVISION_CONFLICT_MESSAGE } from "../writes";
import { renderGtm, TEST_ORG } from "./gtmHarness";

const toasts = vi.hoisted(() => ({ success: vi.fn(), warning: vi.fn(), error: vi.fn() }));
vi.mock("@/lib/toast-manager", () => ({ toast: toasts }));

afterEach(() => {
  cleanup();
  vi.clearAllMocks();
});

const post = create(GtmItemSchema, {
  id: "item-1",
  orgId: TEST_ORG,
  format: GtmItemFormat.LONG_POST,
  channelId: "ch-blog",
  title: "Launch post",
  bodyMd: "Reliant runs on your own machine.",
  status: GtmItemStatus.REVIEW,
  revision: 3,
});

const email = create(GtmItemSchema, {
  ...post,
  id: "item-2",
  format: GtmItemFormat.EMAIL,
  channelId: "ch-email",
  title: "Beta invite",
  emailTo: ["ada@example.com"],
  emailSubject: "You're in",
});

const blog = create(GtmChannelSchema, { id: "ch-blog", orgId: TEST_ORG, key: "blog", name: "Blog", mode: GtmChannelMode.MANUAL, manualInstructionsMd: "Publish via the site repo, then record the URL." });
const gmail = create(GtmChannelSchema, { id: "ch-email", orgId: TEST_ORG, key: "email", name: "Email", mode: GtmChannelMode.INTEGRATED, integrationKind: "gmail" });

const reads = {
  "GtmService.ListCampaigns": { response: { campaigns: [] } },
  "GtmService.ListChannels": { response: { channels: [blog, gmail] } },
  "GtmService.ListAssets": { response: { assets: [] } },
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

  it("starts an edit again from the current version after a conflict, so a second Save cannot overwrite it", async () => {
    // The page holds the item it read: GetItem answers whatever the server has now.
    let server = post;
    const theirs = create(GtmItemSchema, { ...post, title: "Their title", revision: 4 });
    const update = vi.fn((req: unknown) => {
      if ((req as { expectedRevision: number }).expectedRevision === 3) {
        server = theirs;
        throw new ConnectError("revision moved", Code.Aborted);
      }
      return { item: theirs };
    });
    function Host() {
      const q = useGetItem({ orgId: TEST_ORG, id: "item-1" }, { enabled: true });
      return <ItemDialog item={q.data?.item ?? null} onClose={() => {}} />;
    }
    renderGtm(<Host />, { ...reads, "GtmService.GetItem": () => ({ item: server }), "GtmService.UpdateItem": update });
    fireEvent.click(await screen.findByRole("button", { name: "Edit" }));
    fireEvent.change(screen.getByLabelText("Title"), { target: { value: "My title" } });
    fireEvent.click(screen.getByRole("button", { name: "Save" }));
    await waitFor(() => expect(toasts.warning).toHaveBeenCalledWith(REVISION_CONFLICT_MESSAGE));
    // The re-read moved the item to revision 4: the editor shows it, not the stale edit.
    await waitFor(() => expect(screen.getByLabelText("Title")).toHaveValue("Their title"));
    fireEvent.click(screen.getByRole("button", { name: "Save" }));
    await waitFor(() => expect(update).toHaveBeenCalledTimes(2));
    expect(update).toHaveBeenLastCalledWith(expect.objectContaining({ expectedRevision: 4 }));
    expect(update).toHaveBeenLastCalledWith(expect.not.objectContaining({ title: expect.anything() }));
  });

  it("offers no archived channel to move to, but keeps the one the item is on", async () => {
    const old = create(GtmChannelSchema, { id: "ch-old", orgId: TEST_ORG, key: "old", name: "Old forum", mode: GtmChannelMode.MANUAL, status: GtmChannelStatus.ARCHIVED });
    const gone = create(GtmChannelSchema, { id: "ch-gone", orgId: TEST_ORG, key: "gone", name: "Gone forum", mode: GtmChannelMode.MANUAL, status: GtmChannelStatus.ARCHIVED });
    const onOld = create(GtmItemSchema, { ...post, channelId: "ch-old" });
    renderGtm(<ItemDialog item={onOld} onClose={() => {}} />, { ...reads, "GtmService.ListChannels": { response: { channels: [blog, gmail, old, gone] } } });
    await screen.findByText("Old forum");
    fireEvent.click(screen.getByRole("button", { name: "Edit" }));
    const picker = screen.getByLabelText("Channel");
    const names = within(picker).getAllByRole("option").map((o) => o.textContent);
    expect(names).toEqual(["No channel yet", "Blog", "Email", "Old forum"]);
  });

  it("opens where an item went out in a new tab, without an opener", async () => {
    const out = create(GtmItemSchema, { ...post, status: GtmItemStatus.PUBLISHED, publishedUrl: "https://blog.example/launch" });
    renderGtm(<ItemDialog item={out} onClose={() => {}} />, reads);
    const link = await screen.findByRole("link", { name: /blog\.example\/launch/ });
    expect(link).toHaveAttribute("target", "_blank");
    expect(link).toHaveAttribute("rel", "noopener noreferrer");
    cleanup();

    // Anything but http(s) is shown as text, never as a link.
    renderGtm(<ItemDialog item={create(GtmItemSchema, { ...out, publishedUrl: "javascript:alert(1)" })} onClose={() => {}} />, reads);
    expect(await screen.findByText("javascript:alert(1)")).toBeInTheDocument();
    expect(screen.queryByRole("link", { name: /alert/ })).not.toBeInTheDocument();
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

  it("records where a hand-posted item went out, marking it published in one write", async () => {
    const approved = create(GtmItemSchema, { ...post, status: GtmItemStatus.APPROVED });
    const update = vi.fn(() => ({ item: { ...approved, status: GtmItemStatus.PUBLISHED } }));
    renderGtm(<ItemDialog item={approved} onClose={() => {}} />, { ...reads, "GtmService.UpdateItem": update });
    const step = await screen.findByTestId("gtm-manual-step");
    expect(within(step).getByText("Post by hand on Blog")).toBeInTheDocument();
    const mark = within(step).getByRole("button", { name: "Mark published" });
    expect(mark).toBeDisabled();
    fireEvent.change(within(step).getByLabelText("Where it went out"), { target: { value: "https://blog.example/launch" } });
    fireEvent.click(mark);
    await waitFor(() => expect(update).toHaveBeenCalled());
    expect(update).toHaveBeenCalledWith(
      expect.objectContaining({ id: "item-1", expectedRevision: 3, status: GtmItemStatus.PUBLISHED, publishedUrl: "https://blog.example/launch" }),
    );
  });

  it("asks for approval before the manual step can be recorded", async () => {
    renderGtm(<ItemDialog item={post} onClose={() => {}} />, reads);
    const step = await screen.findByTestId("gtm-manual-step");
    expect(within(step).queryByRole("button", { name: "Mark published" })).not.toBeInTheDocument();
    expect(within(step).getByText(/Once it is approved/)).toBeInTheDocument();
  });

  describe("a held email (send in doubt)", () => {
    const held = create(GtmItemSchema, { ...email, status: GtmItemStatus.APPROVED, approvalHash: "sha256:abc", revision: 6 });

    it("is frozen, offering only the resolve", async () => {
      renderGtm(<ItemDialog item={held} onClose={() => {}} />, reads);
      expect(await screen.findByTestId("gtm-held-send")).toBeInTheDocument();
      expect(screen.queryByRole("button", { name: "Edit" })).not.toBeInTheDocument();
      expect(screen.queryByRole("button", { name: "Pause" })).not.toBeInTheDocument();
      expect(screen.queryByRole("button", { name: /^send/i })).not.toBeInTheDocument();
    });

    it("records it as sent with the Gmail message id a person found", async () => {
      const resolve = vi.fn(() => ({ item: { ...held, status: GtmItemStatus.SENT } }));
      renderGtm(<ItemDialog item={held} onClose={() => {}} />, { ...reads, "GtmService.ResolveItemSend": resolve });
      fireEvent.click(await screen.findByRole("button", { name: "It went out" }));
      fireEvent.change(await screen.findByLabelText("Gmail message id"), { target: { value: "18c2f00d" } });
      fireEvent.click(screen.getByRole("button", { name: "Record as sent" }));
      await waitFor(() => expect(resolve).toHaveBeenCalled());
      expect(resolve).toHaveBeenCalledWith(expect.objectContaining({ orgId: TEST_ORG, id: "item-2", approvalHash: "sha256:abc", gmailMessageId: "18c2f00d" }));
    });

    it("releases the hold when it did not go out, and shows an in-flight refusal in the dialog", async () => {
      const resolve = vi.fn(() => {
        throw new ConnectError("a send of this item may still be in progress; wait a few minutes, then check the sent mail in Gmail", Code.FailedPrecondition);
      });
      renderGtm(<ItemDialog item={held} onClose={() => {}} />, { ...reads, "GtmService.ResolveItemSend": resolve });
      fireEvent.click(await screen.findByRole("button", { name: "It did not go out" }));
      fireEvent.click(await screen.findByRole("button", { name: "Release the hold" }));
      expect(await screen.findByTestId("gtm-resolve-refusal")).toHaveTextContent("A send of this item may still be in progress");
      expect(resolve).toHaveBeenCalledWith(expect.objectContaining({ approvalHash: "sha256:abc" }));
      expect(((resolve.mock.calls[0] as unknown[])[0] as { gmailMessageId?: string }).gmailMessageId).toBeUndefined();
    });
  });

  describe("files", () => {
    const realFetch = globalThis.fetch;
    afterEach(() => {
      globalThis.fetch = realFetch;
    });

    it("upload in three steps: start, PUT with the required headers, confirm", async () => {
      const put = vi.fn(async () => new Response(null, { status: 200 }));
      globalThis.fetch = put as unknown as typeof fetch;
      const asset = create(GtmAssetSchema, { id: "as-1", orgId: TEST_ORG, itemId: "item-1", filename: "hero.png", status: GtmAssetStatus.PENDING });
      const start = vi.fn(() => ({ asset, uploadUrl: "https://bucket.example/gtm/org-1/as-1.upload?sig=x", requiredHeaders: { "content-type": "image/png" } }));
      const confirm = vi.fn(() => ({ asset: { ...asset, status: GtmAssetStatus.READY } }));
      renderGtm(<ItemDialog item={post} onClose={() => {}} />, { ...reads, "GtmService.CreateAssetUpload": start, "GtmService.ConfirmAssetUpload": confirm });
      const file = new File(["png-bytes"], "hero.png", { type: "image/png" });
      fireEvent.change(await screen.findByLabelText("Upload a file to assets"), { target: { files: [file] } });
      await waitFor(() => expect(confirm).toHaveBeenCalled());
      expect(start).toHaveBeenCalledWith(expect.objectContaining({ orgId: TEST_ORG, target: { case: "itemId", value: "item-1" }, filename: "hero.png", contentType: "image/png", sizeBytes: 9n }));
      expect(put).toHaveBeenCalledWith("https://bucket.example/gtm/org-1/as-1.upload?sig=x", expect.objectContaining({ method: "PUT", headers: { "content-type": "image/png" }, body: file }));
      expect(confirm).toHaveBeenCalledWith(expect.objectContaining({ orgId: TEST_ORG, assetId: "as-1" }));
    });

    it("refuse a file over 25 MB before asking to upload it", async () => {
      const start = vi.fn(() => ({}));
      renderGtm(<ItemDialog item={post} onClose={() => {}} />, { ...reads, "GtmService.CreateAssetUpload": start });
      const big = new File(["x"], "demo.mov", { type: "video/quicktime" });
      Object.defineProperty(big, "size", { value: 30 * 1024 * 1024 });
      fireEvent.change(await screen.findByLabelText("Upload a file to assets"), { target: { files: [big] } });
      await waitFor(() => expect(toasts.warning).toHaveBeenCalledWith("demo.mov is 30.0 MB. Files up to 25 MB can be uploaded."));
      expect(start).not.toHaveBeenCalled();
      expect(screen.getByLabelText("Upload a file to assets")).toHaveAttribute("tabindex", "-1");
    });

    it("say uploads are unavailable while storage is not set up", async () => {
      renderGtm(<ItemDialog item={post} onClose={() => {}} />, {
        ...reads,
        "GtmService.CreateAssetUpload": { error: new ConnectError("asset storage is not configured", Code.Unavailable) },
      });
      fireEvent.change(await screen.findByLabelText("Upload a file to assets"), { target: { files: [new File(["x"], "a.txt", { type: "text/plain" })] } });
      expect(await screen.findByTestId("gtm-assets-unavailable")).toBeInTheDocument();
      expect(screen.getByRole("button", { name: "Upload" })).toBeDisabled();
      expect(toasts.error).not.toHaveBeenCalled();
    });

    it("download on a click: a fresh URL from GetAssetDownload, opened in a new tab; an image's thumbnail asks once", async () => {
      const png = create(GtmAssetSchema, { id: "as-3", orgId: TEST_ORG, itemId: "item-1", filename: "hero.png", contentType: "image/png", sizeBytes: 2048n, status: GtmAssetStatus.READY });
      let n = 0;
      const get = vi.fn(() => ({ downloadUrl: `https://bucket.example/hero.png?sig=${++n}` }));
      const tab = { opener: {} as unknown, location: { href: "" }, close: vi.fn() };
      const open = vi.spyOn(window, "open").mockReturnValue(tab as unknown as Window);
      try {
        renderGtm(<ItemDialog item={post} onClose={() => {}} />, { ...reads, "GtmService.ListAssets": { response: { assets: [png] } }, "GtmService.GetAssetDownload": get });
        const img = await screen.findByRole("img", { name: "hero.png" });
        expect(img).toHaveAttribute("src", "https://bucket.example/hero.png?sig=1");
        expect(get).toHaveBeenCalledTimes(1);
        fireEvent.click(screen.getByRole("button", { name: "Download hero.png" }));
        await waitFor(() => expect(tab.location.href).toBe("https://bucket.example/hero.png?sig=2"));
        expect(open).toHaveBeenCalledWith("", "_blank");
        expect(tab.opener).toBeNull();
        expect(get).toHaveBeenLastCalledWith(expect.objectContaining({ orgId: TEST_ORG, assetId: "as-3" }));
      } finally {
        open.mockRestore();
      }
    });

    it("delete only after a confirm", async () => {
      const asset = create(GtmAssetSchema, { id: "as-2", orgId: TEST_ORG, itemId: "item-1", filename: "notes.txt", contentType: "text/plain", sizeBytes: 2048n, status: GtmAssetStatus.READY, source: GtmAssetSource.PERSON });
      const del = vi.fn(() => ({}));
      renderGtm(<ItemDialog item={post} onClose={() => {}} />, { ...reads, "GtmService.ListAssets": { response: { assets: [asset] } }, "GtmService.DeleteAsset": del });
      fireEvent.click(await screen.findByRole("button", { name: "Delete notes.txt" }));
      expect(del).not.toHaveBeenCalled();
      fireEvent.click(screen.getByRole("button", { name: "Delete" }));
      await waitFor(() => expect(del).toHaveBeenCalledWith(expect.objectContaining({ orgId: TEST_ORG, assetId: "as-2" })));
    });
  });

  it("offers no approval once an item is out", async () => {
    renderGtm(<ItemDialog item={create(GtmItemSchema, { ...post, status: GtmItemStatus.PUBLISHED })} onClose={() => {}} />, reads);
    expect(await screen.findByTestId("gtm-item-dialog")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /approve/i })).not.toBeInTheDocument();
  });
});
