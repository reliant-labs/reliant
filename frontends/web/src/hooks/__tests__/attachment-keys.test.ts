/**
 * AttachmentService's one read — an attachment's bytes — goes through the
 * generated `attachmentServiceKeys.getAttachment` key:
 *
 *  - two surfaces asking for the same attachment at once share one
 *    GetAttachment (the chip and the inline image mount together);
 *  - the entry does not outlive the fetch (callers own the bytes as blob
 *    URLs; a cached copy would double a video's memory);
 *  - deleting an attachment drops whatever the cache still holds for it,
 *    and `attachmentServiceKeys.all` reaches the entry.
 */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { setTransport } from "@/lib/connect";
import { queryClient } from "@/lib/query-client";
import { mockTransport } from "@/lib/test-utils";
import { attachmentServiceKeys } from "../attachment-service-hooks_gen";

// test/setup.ts stubs deleteAttachment for every attachment-store test; this
// file is about the real one.
const { deleteAttachment, fetchAttachmentBlob, uploadAttachment } =
  await vi.importActual<typeof import("../attachment-queries")>("../attachment-queries");

let getCalls: number;
let deleted: string[];

beforeEach(() => {
  getCalls = 0;
  deleted = [];
  setTransport(
    mockTransport({
      "AttachmentService.GetAttachment": async (req) => {
        getCalls++;
        await Promise.resolve();
        return {
          attachment: { id: (req as { attachmentId: string }).attachmentId, mimeType: "image/png" },
          content: new TextEncoder().encode("png"),
        };
      },
      "AttachmentService.DeleteAttachment": (req) => {
        deleted.push((req as { attachmentId: string }).attachmentId);
        return {};
      },
      "AttachmentService.UploadAttachment": (req) => {
        const r = req as { filename: string; mimeType: string; content: Uint8Array };
        return {
          attachment: {
            id: "att-new",
            filename: r.filename,
            mimeType: r.mimeType,
            size: BigInt(r.content.length),
            attachmentType: "image",
          },
        };
      },
    }),
  );
});

afterEach(() => {
  queryClient.clear();
  setTransport(null);
});

const key = (attachmentId: string) => attachmentServiceKeys.getAttachment({ attachmentId });

describe("AttachmentService keys", () => {
  it("concurrent reads of one attachment share a single GetAttachment", async () => {
    const [a, b] = await Promise.all([fetchAttachmentBlob("att-1"), fetchAttachmentBlob("att-1")]);
    expect(getCalls).toBe(1);
    expect(a.type).toBe("image/png");
    expect(b.type).toBe("image/png");
  });

  it("the bytes are not kept once the fetch has been handed over", async () => {
    await fetchAttachmentBlob("att-1");
    await vi.waitFor(() => expect(queryClient.getQueryCache().find({ queryKey: key("att-1") })).toBeUndefined());
  });

  it("deleting an attachment drops its cache entry", async () => {
    queryClient.setQueryData(key("att-1"), { attachment: { id: "att-1" }, content: new Uint8Array() });
    await deleteAttachment("att-1");
    expect(deleted).toEqual(["att-1"]);
    expect(queryClient.getQueryData(key("att-1"))).toBeUndefined();
  });

  it("attachmentServiceKeys.all reaches the bytes entry", () => {
    queryClient.setQueryData(key("att-1"), { attachment: { id: "att-1" }, content: new Uint8Array() });
    queryClient.removeQueries({ queryKey: attachmentServiceKeys.all });
    expect(queryClient.getQueryData(key("att-1"))).toBeUndefined();
  });

  it("an upload comes back as the composer's draft attachment", async () => {
    // jsdom's File has no arrayBuffer().
    const file = Object.assign(new File(["abc"], "shot.png", { type: "image/png" }), {
      arrayBuffer: async () => new TextEncoder().encode("abc").buffer,
    });
    const attachment = await uploadAttachment(file);
    expect(attachment).toMatchObject({ id: "att-new", filename: "shot.png", mime_type: "image/png", size: 3 });
  });
});
