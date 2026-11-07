import { beforeEach, describe, expect, it, vi } from "vitest";

const api = vi.hoisted(() => ({
  uploadAttachment: vi.fn(),
  deleteAttachment: vi.fn(),
}));
vi.mock("../../hooks/attachment-queries", () => api);
vi.mock("../../lib/toast-manager", () => ({ toast: { error: vi.fn() } }));

import { useAttachmentStore } from "../attachmentStore";

const KEY = "reliant-composer-attachments";
const att = (id: string, filename = `${id}.txt`) => ({
  id, filename, size: 5, mime_type: "text/plain", url: "", attachment_type: "file_reference" as const,
});

beforeEach(() => {
  api.uploadAttachment.mockReset();
  api.deleteAttachment.mockReset().mockResolvedValue(undefined);
  useAttachmentStore.getState().reset();
  localStorage.clear();
});

describe("draft attachments across a reload", () => {
  it("persists each draft's metadata and restores it, dropping malformed rows", async () => {
    useAttachmentStore.setState({ attachments: new Map([["chat-1", [att("a")]]]) });
    expect(JSON.parse(localStorage.getItem(KEY)!).state).toEqual({ drafts: { "chat-1": [att("a")] } });

    // A fresh page: empty memory, the stored drafts (with rows no build wrote).
    useAttachmentStore.setState({ attachments: new Map() });
    localStorage.setItem(KEY, JSON.stringify({
      state: { drafts: { "chat-1": [att("a"), { id: "" }, "junk"], "chat-2": [] } },
      version: 1,
    }));
    await useAttachmentStore.persist.rehydrate();
    expect(Object.fromEntries(useAttachmentStore.getState().attachments)).toEqual({ "chat-1": [att("a")] });
  });

  it("forgets every draft on reset (sign-out)", () => {
    useAttachmentStore.setState({ attachments: new Map([["chat-1", [att("a")]]]) });
    useAttachmentStore.getState().reset();
    expect(JSON.parse(localStorage.getItem(KEY)!).state).toEqual({ drafts: {} });
  });
});

describe("replaceAttachment", () => {
  it("swaps the edited upload in at the same position and deletes the old one", async () => {
    useAttachmentStore.setState({ attachments: new Map([["s", [att("a"), att("b"), att("c")]]]) });
    api.uploadAttachment.mockResolvedValue(att("b2", "b.txt"));
    const result = await useAttachmentStore.getState().replaceAttachment("s", "b", new File(["new"], "b.txt"));
    expect(result?.id).toBe("b2");
    expect(useAttachmentStore.getState().getAttachments("s").map((a) => a.id)).toEqual(["a", "b2", "c"]);
    expect(api.deleteAttachment).toHaveBeenCalledWith("b");
    expect(useAttachmentStore.getState().uploading).toBe(false);
  });

  it("discards the new upload when the old attachment left the draft meanwhile", async () => {
    useAttachmentStore.setState({ attachments: new Map([["s", [att("a")]]]) });
    api.uploadAttachment.mockImplementation(async () => {
      useAttachmentStore.setState({ attachments: new Map() }); // sent while uploading
      return att("a2");
    });
    expect(await useAttachmentStore.getState().replaceAttachment("s", "a", new File(["x"], "a.txt"))).toBeUndefined();
    expect(useAttachmentStore.getState().getAttachments("s")).toEqual([]);
    expect(api.deleteAttachment).toHaveBeenCalledWith("a2");
    expect(api.deleteAttachment).not.toHaveBeenCalledWith("a");
  });

  it("keeps the old attachment when the upload fails", async () => {
    useAttachmentStore.setState({ attachments: new Map([["s", [att("a")]]]) });
    api.uploadAttachment.mockRejectedValue(new Error("offline"));
    await expect(useAttachmentStore.getState().replaceAttachment("s", "a", new File(["x"], "a.txt"))).rejects.toThrow("offline");
    expect(useAttachmentStore.getState().getAttachments("s")).toEqual([att("a")]);
    expect(api.deleteAttachment).not.toHaveBeenCalled();
  });
});
