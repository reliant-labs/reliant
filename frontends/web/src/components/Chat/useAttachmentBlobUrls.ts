/**
 * Load image attachments as blob URLs.
 *
 * Extracted from MessageAttachments so the transcript's two image surfaces —
 * the small uploaded-file chips and the large inline generated images — share
 * ONE loading path. There is no HTTP route that serves attachment bytes
 * (`attachment.url` is vestigial), so the only way to get pixels is
 * `fetchAttachmentBlob` (GetAttachment), and a second copy of this effect would
 * be a second place for the revoke/cancel bookkeeping to drift.
 */

import { useEffect, useRef, useState } from "react";
import { fetchAttachmentBlob } from "../../hooks/attachment-queries";

/** Attachment fields this hook needs — narrower than the full Attachment. */
interface LoadableAttachment {
  id: string;
  mimeType?: string;
}

export interface AttachmentBlobUrls {
  /** attachment id -> blob URL, present once the bytes have arrived. */
  blobUrls: Map<string, string>;
  /** The same bytes backing each URL, for actions that require a Blob immediately. */
  blobs: Map<string, Blob>;
  /** attachment ids whose fetch failed. */
  failedIds: Set<string>;
}

export function isImageMimeType(mimeType: string | undefined): boolean {
  return Boolean(mimeType?.startsWith("image/"));
}

export function useAttachmentBlobUrls(
  attachments: readonly LoadableAttachment[] | undefined,
): AttachmentBlobUrls {
  const [{ blobUrls, blobs }, setLoaded] = useState({
    blobUrls: new Map<string, string>(),
    blobs: new Map<string, Blob>(),
  });
  const [failedIds, setFailedIds] = useState<Set<string>>(new Set());
  const blobUrlsRef = useRef<Map<string, string>>(blobUrls);
  blobUrlsRef.current = blobUrls;

  // Both callers build their array during render — `.filter()` in
  // MessageGeneratedImages, `.flatMap()` inside a `segments.map()` callback in
  // ChatMessage — so the reference changes every render and neither can hoist
  // it into a useMemo (the ChatMessage one sits in a loop body, where hooks are
  // illegal). Keying the effect on the array identity therefore re-ran it on
  // every render; the ids are what the effect actually depends on.
  const imageIdsKey = (attachments || [])
    .filter((attachment) => isImageMimeType(attachment.mimeType))
    .map((attachment) => attachment.id)
    .join(",");

  // Read through a ref so the array itself is not a dependency.
  const attachmentsRef = useRef(attachments);
  attachmentsRef.current = attachments;

  useEffect(() => {
    let cancelled = false;

    const loadImages = async () => {
      const loadedUrls = new Map<string, string>();
      const loadedBlobs = new Map<string, Blob>();

      for (const attachment of attachmentsRef.current || []) {
        if (cancelled) break;
        if (!isImageMimeType(attachment.mimeType)) continue;
        if (blobUrlsRef.current.has(attachment.id)) continue;

        try {
          const blob = await fetchAttachmentBlob(attachment.id);
          // Check before creating the URL: one we create after unmount has no
          // owner left to revoke it.
          if (cancelled) break;
          loadedUrls.set(attachment.id, URL.createObjectURL(blob));
          loadedBlobs.set(attachment.id, blob);
        } catch (error) {
          if (cancelled) break;
          console.error("Error loading image:", { id: attachment.id, error });
          setFailedIds((prev) => new Set(prev).add(attachment.id));
        }
      }

      if (cancelled) {
        loadedUrls.forEach((url) => URL.revokeObjectURL(url));
        return;
      }
      if (loadedUrls.size > 0) {
        setLoaded((prev) => ({
          blobUrls: new Map([...prev.blobUrls, ...loadedUrls]),
          blobs: new Map([...prev.blobs, ...loadedBlobs]),
        }));
      }
    };

    void loadImages();

    // Only the in-flight load is cancelled here. Revoking on every effect run
    // destroyed URLs that the <img> tags were still displaying, and clearing
    // blobUrlsRef defeated the "already loaded" guard above, so each run
    // refetched everything it had just fetched.
    return () => {
      cancelled = true;
    };
  }, [imageIdsKey]);

  // The blob URLs are owned for the component's lifetime, so they are released
  // on unmount — not when the effect happens to re-run.
  useEffect(() => {
    const urls = blobUrlsRef;
    return () => {
      urls.current.forEach((url) => URL.revokeObjectURL(url));
      urls.current = new Map();
    };
  }, []);

  return { blobUrls, blobs, failedIds };
}
