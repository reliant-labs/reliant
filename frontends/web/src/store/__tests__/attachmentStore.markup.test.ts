import { beforeEach, describe, expect, it, vi } from "vitest";

const api = vi.hoisted(() => ({
  uploadAttachment: vi.fn(),
  deleteAttachment: vi.fn(),
}));
vi.mock("../../hooks/attachment-queries", () => api);
vi.mock("../../lib/toast-manager", () => ({ toast: { error: vi.fn() } }));

import { useAttachmentStore } from "../attachmentStore";
import type { MarkupDoc } from "../../components/Chat/imageMarkup/model";

const KEY = "reliant-composer-attachments";
const img = (id: string, filename = `${id}.png`) => ({
  id, filename, size: 5, mime_type: "image/png", url: "", attachment_type: "image" as const,
});
const doc = (): MarkupDoc => ({ width: 100, height: 50, shapes: [] });
const store = () => useAttachmentStore.getState();

beforeEach(() => {
  api.uploadAttachment.mockReset();
  api.deleteAttachment.mockReset().mockResolvedValue(undefined);
  URL.createObjectURL = vi.fn(() => "blob:mock");
  URL.revokeObjectURL = vi.fn();
  store().reset();
  localStorage.clear();
});

async function attachImage(id: string, sessionId = "s") {
  api.uploadAttachment.mockResolvedValueOnce(img(id));
  const file = new File(["orig"], `${id}.png`, { type: "image/png" });
  await store().attachFile(sessionId, file);
  return file;
}

describe("attachment markup", () => {
  it("keeps an image upload's original bytes, not for text", async () => {
    const file = await attachImage("a");
    expect(store().localImages.get("a")).toBe(file);
    expect(store().getImageSource("a")).toBe(file);
    expect(store().localFiles.has("a")).toBe(false);

    api.uploadAttachment.mockResolvedValueOnce({ ...img("t"), attachment_type: "file_reference" });
    await store().attachFile("s", new File(["x"], "t.txt"));
    expect(store().localImages.has("t")).toBe(false);
    expect(store().getImageSource("t")).toBeUndefined();
  });

  it("carries the markup to the new id and drops the old one", async () => {
    await attachImage("a");
    const source = new Blob(["orig"]);
    store().markups.set("a", { source, doc: doc() });

    const flat = new File(["flat"], "a.png", { type: "image/png" });
    api.uploadAttachment.mockResolvedValueOnce(img("a2"));
    const next = { source, doc: { ...doc(), width: 7 } };
    await store().replaceAttachment("s", "a", flat, { markup: next });

    expect(store().getMarkup("a")).toBeUndefined();
    expect(store().getMarkup("a2")).toBe(next);
    expect(store().localImages.has("a")).toBe(false);
    expect(store().localImages.get("a2")).toBe(flat);
    // The editor re-edits from the un-annotated original, not the flattened file.
    expect(store().getImageSource("a2")).toBe(source);
  });

  it("markup: null clears it (revert to original)", async () => {
    await attachImage("a");
    const source = new Blob(["orig"]);
    store().markups.set("a", { source, doc: doc() });

    const original = new File(["orig"], "a.png", { type: "image/png" });
    api.uploadAttachment.mockResolvedValueOnce(img("a2"));
    await store().replaceAttachment("s", "a", original, { markup: null });

    expect(store().getMarkup("a")).toBeUndefined();
    expect(store().getMarkup("a2")).toBeUndefined();
    expect(store().getImageSource("a2")).toBe(original);
  });

  it("carries nothing over when opts is omitted (3-arg callers)", async () => {
    await attachImage("a");
    store().markups.set("a", { source: new Blob(["o"]), doc: doc() });
    api.uploadAttachment.mockResolvedValueOnce(img("a2"));
    await store().replaceAttachment("s", "a", new File(["n"], "a.png"));
    expect(store().markups.size).toBe(0);
  });

  it("sets no markup when the old attachment left the draft during the upload", async () => {
    await attachImage("a");
    const old = { source: new Blob(["o"]), doc: doc() };
    store().markups.set("a", old);
    api.uploadAttachment.mockImplementationOnce(async () => {
      store().removeAttachment("s", "a");
      return img("a2");
    });
    const result = await store().replaceAttachment("s", "a", new File(["n"], "a.png"), {
      markup: { source: new Blob(["o"]), doc: doc() },
    });
    expect(result).toBeUndefined();
    expect(store().markups.size).toBe(0);
    expect(store().localImages.size).toBe(0);
    expect(api.deleteAttachment).toHaveBeenCalledWith("a2");
  });

  it("drops markup and local images on remove, clear and reset", async () => {
    await attachImage("a");
    await attachImage("b");
    await attachImage("c", "other");
    for (const id of ["a", "b", "c"]) store().markups.set(id, { source: new Blob([id]), doc: doc() });

    store().removeAttachment("s", "a");
    expect(store().markups.has("a")).toBe(false);
    expect(store().localImages.has("a")).toBe(false);
    expect(store().markups.has("b")).toBe(true);
    expect(URL.revokeObjectURL).toHaveBeenCalled();

    store().clearAttachments("s");
    expect(store().markups.has("b")).toBe(false);
    expect(store().localImages.has("b")).toBe(false);
    expect(store().markups.has("c")).toBe(true);
    expect(store().localImages.has("c")).toBe(true);

    store().reset();
    expect(store().markups.size).toBe(0);
    expect(store().localImages.size).toBe(0);
  });

  it("never writes markup or image bytes to localStorage, nor restores them", async () => {
    await attachImage("a");
    store().markups.set("a", { source: new Blob(["orig"]), doc: { width: 1, height: 1, shapes: [] } });
    // Trigger a persist write after markups is populated.
    useAttachmentStore.setState({ attachments: new Map(store().attachments) });

    const raw = localStorage.getItem(KEY)!;
    expect(JSON.parse(raw).state).toEqual({ drafts: { s: [img("a")] } });
    expect(raw).not.toMatch(/markup|localImages|shapes/);

    // A fresh page: only the metadata comes back.
    useAttachmentStore.setState({ markups: new Map(), localImages: new Map() });
    await useAttachmentStore.persist.rehydrate();
    expect(store().getAttachments("s").map((x) => x.id)).toEqual(["a"]);
    expect(store().markups.size).toBe(0);
    expect(store().getImageSource("a")).toBeUndefined();
  });
});
