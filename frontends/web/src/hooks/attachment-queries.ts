/**
 * Chat attachments: AttachmentService on the generated client and keys.
 *
 * Uploads are composer drafts (attachmentStore holds them until the message is
 * sent), so nothing here is a mounted read that a write could leave stale. The
 * one read — an attachment's bytes, for the transcript's image and video
 * surfaces — goes through the query cache under the generated
 * `attachmentServiceKeys.getAttachment` key, so two surfaces mounting the same
 * attachment at once share ONE GetAttachment, and a delete drops the entry.
 */

import { AttachmentService, type AttachmentInfo } from "../gen/reliant/v1/attachment_pb";
import { connectClient } from "../lib/connect";
import type { AttachmentType } from "../lib/filetypes";
import { queryClient } from "../lib/query-client";
import { attachmentServiceKeys, attachmentServiceQueries } from "./attachment-service-hooks_gen";

/** A composer draft attachment, as attachmentStore and the chips render it. */
export interface Attachment {
  id: string;
  filename: string;
  size: number;
  mime_type: string;
  url: string;
  hash?: string;
  attachment_type: AttachmentType; // "image" or "file_reference"
  file_path?: string; // For file references, the original path
}

const client = connectClient(AttachmentService);

function attachmentFromProto(proto: AttachmentInfo): Attachment {
  return {
    id: proto.id,
    filename: proto.filename,
    size: Number(proto.size),
    mime_type: proto.mimeType,
    url: proto.url,
    hash: proto.fileHash,
    attachment_type: proto.attachmentType as AttachmentType,
    file_path: proto.filePath || undefined,
  };
}

/** Upload a file's bytes (images, text files and documents alike). */
export async function uploadAttachment(file: File): Promise<Attachment> {
  const response = await client.uploadAttachment({
    filename: file.name,
    content: new Uint8Array(await file.arrayBuffer()),
    mimeType: file.type || "application/octet-stream",
  });
  if (!response.attachment) {
    throw new Error("Upload failed: no attachment returned");
  }
  return attachmentFromProto(response.attachment);
}

/** Delete an attachment, and drop its cached bytes with it. */
export async function deleteAttachment(attachmentId: string): Promise<void> {
  await client.deleteAttachment({ attachmentId });
  queryClient.removeQueries({ queryKey: attachmentServiceKeys.getAttachment({ attachmentId }) });
}

/**
 * An attachment's bytes as a Blob, for display.
 *
 * The cache entry exists to share an in-flight fetch, not to hold the bytes:
 * every caller turns them into a blob URL it owns, so keeping a second copy
 * (a generated video can be tens of MB) would only double the memory. Hence
 * `gcTime: 0` — the entry goes as soon as nobody is waiting on it.
 */
export async function fetchAttachmentBlob(attachmentId: string): Promise<Blob> {
  const response = await queryClient.fetchQuery(
    attachmentServiceQueries.getAttachment({ attachmentId }, { staleTime: 0, gcTime: 0 }),
  );
  if (!response.attachment) {
    throw new Error("Attachment not found");
  }
  return new Blob([response.content], { type: response.attachment.mimeType });
}
