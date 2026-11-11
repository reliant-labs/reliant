import { beforeEach, describe, expect, it, vi } from "vitest";

const api = vi.hoisted(() => ({
  uploadAttachment: vi.fn(),
  deleteAttachment: vi.fn(),
}));
vi.mock("../../hooks/attachment-queries", () => api);
vi.mock("../../lib/toast-manager", () => ({ toast: { error: vi.fn() } }));

import { isDraftAttachmentPersisted, useAttachmentStore } from "../attachmentStore";

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

  it("attaches even when storage refuses the write, and reports the draft as not persisted", async () => {
    api.uploadAttachment.mockResolvedValue(att("a"));
    const setItem = vi.spyOn(Storage.prototype, "setItem").mockImplementation(() => {
      throw new DOMException("quota", "QuotaExceededError");
    });
    try {
      await expect(useAttachmentStore.getState().attachFile("chat-1", new File(["x"], "a.txt"))).resolves.toMatchObject({ id: "a" });
      expect(useAttachmentStore.getState().getAttachments("chat-1").map((a) => a.id)).toEqual(["a"]);
      expect(isDraftAttachmentPersisted("chat-1", "a")).toBe(false);
    } finally {
      setItem.mockRestore();
    }
    useAttachmentStore.setState({ attachments: new Map([["chat-1", [att("a")]]]) });
    expect(isDraftAttachmentPersisted("chat-1", "a")).toBe(true);
    expect(isDraftAttachmentPersisted("chat-2", "a")).toBe(false);
  });

  it("keeps a text upload's bytes, not a blob URL, until it leaves the draft", async () => {
    api.uploadAttachment.mockResolvedValue(att("a"));
    const file = new File(["pasted"], "pasted-text.txt", { type: "text/plain" });
    await useAttachmentStore.getState().attachFile("chat-1", file);
    expect(useAttachmentStore.getState().localFiles.get("a")).toBe(file);
    expect(useAttachmentStore.getState().blobUrls.has("a")).toBe(false);

    useAttachmentStore.getState().removeAttachment("chat-1", "a");
    expect(useAttachmentStore.getState().localFiles.has("a")).toBe(false);
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

  it("keeps send blocked until the replacement settles, even when another upload finishes first", async () => {
    useAttachmentStore.setState({ attachments: new Map([["s", [att("a")]]]) });
    let finishReplace!: (a: ReturnType<typeof att>) => void;
    api.uploadAttachment
      .mockImplementationOnce(() => new Promise((resolve) => { finishReplace = resolve; }))
      .mockResolvedValueOnce(att("b"));

    const replacing = useAttachmentStore.getState().replaceAttachment("s", "a", new File(["edit"], "a.txt"));
    await useAttachmentStore.getState().attachFile("s", new File(["b"], "b.txt"));
    // The composer gates send on `uploading`: a send now would capture "a",
    // which the pending replacement is about to swap out and delete.
    expect(useAttachmentStore.getState().uploading).toBe(true);

    finishReplace(att("a2", "a.txt"));
    await replacing;
    expect(useAttachmentStore.getState().uploading).toBe(false);
    // What a send captures now is the committed edit, and it stays on the server.
    expect(useAttachmentStore.getState().getAttachments("s").map((x) => x.id)).toEqual(["a2", "b"]);
    expect(api.deleteAttachment).toHaveBeenCalledWith("a");
    expect(api.deleteAttachment).not.toHaveBeenCalledWith("a2");
  });

  it("keeps the old attachment when the upload fails", async () => {
    useAttachmentStore.setState({ attachments: new Map([["s", [att("a")]]]) });
    api.uploadAttachment.mockRejectedValue(new Error("offline"));
    await expect(useAttachmentStore.getState().replaceAttachment("s", "a", new File(["x"], "a.txt"))).rejects.toThrow("offline");
    expect(useAttachmentStore.getState().getAttachments("s")).toEqual([att("a")]);
    expect(api.deleteAttachment).not.toHaveBeenCalled();
  });
});
