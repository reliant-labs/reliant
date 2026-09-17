/**
 * The hook must tolerate a caller that passes a NEW array instance on every
 * render, because both of its call sites do exactly that and neither can stop.
 *
 * MessageGeneratedImages builds `images` with `.filter()` during render, and
 * ChatMessage builds `runImages` with `.flatMap()` INSIDE a `segments.map()`
 * callback — a loop body, where `useMemo` is illegal. So a stable reference is
 * not available to give, and the hook is the only place the problem can be
 * fixed.
 *
 * The bug this pins: keying the effect on the array identity re-ran it every
 * render, and its cleanup cleared `blobUrlsRef.current` — the very map the
 * "already loaded" guard reads. Each run therefore refetched every image,
 * setState'd, re-rendered, and ran again, sustaining ~28 GetAttachment calls a
 * second. The synchronous revoke on each pass blanked an <img> still pointing
 * at that URL, so rows collapsed to the placeholder box and grew back ~35ms
 * later; that height oscillation is what shredded Virtuoso's measurement cache
 * and read to the user as scroll jitter and freezing.
 *
 * Both assertions are about the loop, not about pixels: fetch ONCE per id
 * across many renders, and do NOT revoke a URL that is still being displayed.
 */

import { renderHook, waitFor } from "@testing-library/react";
import { beforeAll, beforeEach, describe, expect, it, vi } from "vitest";
import { useAttachmentBlobUrls } from "../useAttachmentBlobUrls";

// jsdom implements neither of these, and the hook's whole job is creating and
// releasing them — without a stub every test fails on the missing API rather
// than on the behaviour under test. Each URL is unique so the assertions can
// tell one attachment's URL from another's.
let objectUrlSeq = 0;
beforeAll(() => {
  URL.createObjectURL = vi.fn(() => `blob:mock/${++objectUrlSeq}`);
  URL.revokeObjectURL = vi.fn();
});

const getAttachmentAsBlob = vi.fn(
  async () => new Blob(["png-bytes"], { type: "image/png" }),
);

vi.mock("../../../api/attachment-grpc", () => ({
  attachmentGrpc: {
    getAttachmentAsBlob: (id: string) => getAttachmentAsBlob(id),
  },
}));

/** Mirrors the call sites: a fresh array of equal attachments every render. */
const freshAttachments = () => [
  { id: "att-1", mimeType: "image/png" },
  { id: "att-2", mimeType: "image/png" },
];

describe("useAttachmentBlobUrls with an unstable attachments array", () => {
  beforeEach(() => {
    getAttachmentAsBlob.mockClear();
  });

  it("fetches each attachment once across many re-renders", async () => {
    const { result, rerender } = renderHook(() =>
      useAttachmentBlobUrls(freshAttachments()),
    );

    await waitFor(() => expect(result.current.blobUrls.size).toBe(2));

    // Stands in for the re-render storm Virtuoso produces while scrolling
    // (rangeChanged / isScrolling fire continuously).
    for (let i = 0; i < 10; i++) rerender();
    await waitFor(() => expect(result.current.blobUrls.size).toBe(2));

    expect(getAttachmentAsBlob).toHaveBeenCalledTimes(2);
    expect(getAttachmentAsBlob).toHaveBeenCalledWith("att-1");
    expect(getAttachmentAsBlob).toHaveBeenCalledWith("att-2");
  });

  it("keeps blob URLs alive while the component is still mounted", async () => {
    const revoke = URL.revokeObjectURL as ReturnType<typeof vi.fn>;

    const { result, rerender } = renderHook(() =>
      useAttachmentBlobUrls(freshAttachments()),
    );
    await waitFor(() => expect(result.current.blobUrls.size).toBe(2));

    const displayed = [...result.current.blobUrls.values()];
    revoke.mockClear();

    for (let i = 0; i < 5; i++) rerender();
    await waitFor(() => expect(result.current.blobUrls.size).toBe(2));

    // A URL revoked while it is still the <img>'s src blanks the image and
    // collapses the row — the height oscillation behind the scroll glitch.
    for (const url of displayed) {
      expect(revoke).not.toHaveBeenCalledWith(url);
    }
    expect([...result.current.blobUrls.values()]).toEqual(displayed);
  });

  it("fetches only the newly added attachment when the list grows", async () => {
    const { result, rerender } = renderHook(
      ({ atts }) => useAttachmentBlobUrls(atts),
      { initialProps: { atts: freshAttachments() } },
    );
    await waitFor(() => expect(result.current.blobUrls.size).toBe(2));
    getAttachmentAsBlob.mockClear();

    rerender({
      atts: [...freshAttachments(), { id: "att-3", mimeType: "image/png" }],
    });
    await waitFor(() => expect(result.current.blobUrls.size).toBe(3));

    expect(getAttachmentAsBlob).toHaveBeenCalledTimes(1);
    expect(getAttachmentAsBlob).toHaveBeenCalledWith("att-3");
  });
});
