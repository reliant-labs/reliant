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

import { withRpcBudget } from "../api/transport";
import { AttachmentService } from "../gen/reliant/v1/attachment_pb";
import type { Attachment as AttachmentMessage } from "../gen/reliant/v1/chat_pb";
import { connectClient } from "../lib/connect";
import { UPLOAD_TIMEOUT_MS } from "../lib/constants";
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

function attachmentFromProto(proto: AttachmentMessage): Attachment {
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

/**
 * The slowest uplink an upload's budget waits for: 128 KiB/s, about 1 Mbps,
 * a weak mobile link. Below it the upload times out rather than hanging.
 */
const UPLOAD_MIN_BYTES_PER_SEC = 128 * 1024;

/**
 * The client-side budget for uploading `sizeBytes`: UPLOAD_TIMEOUT_MS, or the
 * time the bytes take at UPLOAD_MIN_BYTES_PER_SEC plus half that again for the
 * server, whichever is longer. The file rides in ONE unary body, base64 inside
 * JSON (4/3 of its size on the wire), so the time is the link's, not the
 * server's: a fixed 60s fit a phone photo on a good link and nothing larger
 * on a weak one, and the composer accepts files up to 50 MB (about 9 minutes
 * at the floor). The gateway does not count the upload against its route
 * timeout (Envoy's runs from the end of the request), nor does the api-server
 * against its deadline (connect reads the body before the handler's
 * interceptors start), so only this budget bounds the transfer.
 */
export function uploadBudgetMs(sizeBytes: number): number {
  const wireBytes = Math.ceil((sizeBytes * 4) / 3);
  const transferMs = Math.ceil((wireBytes / UPLOAD_MIN_BYTES_PER_SEC) * 1000);
  return Math.max(UPLOAD_TIMEOUT_MS, transferMs + UPLOAD_TIMEOUT_MS / 2);
}

/**
 * Upload a file's bytes (images, text files and documents alike). `orgId` is
 * the org to stamp on it (the chat's, or the org scope's before a chat
 * exists); the server stamps it once it verifies the caller is a member, which
 * is all the stamp means. None stamps no org, and such an attachment never
 * reaches an org-scoped read.
 */
export async function uploadAttachment(file: File, orgId?: string | null): Promise<Attachment> {
  const response = await client.uploadAttachment(
    {
      filename: file.name,
      content: new Uint8Array(await file.arrayBuffer()),
      mimeType: file.type || "application/octet-stream",
      ...(orgId ? { orgId } : {}),
    },
    { contextValues: withRpcBudget(undefined, uploadBudgetMs(file.size)) },
  );
  if (!response.attachment) {
    throw new Error("Upload failed: no attachment returned");
  }
  return attachmentFromProto(response.attachment);
}

/** Delete an attachment, and drop its cached bytes with it. */
export async function deleteAttachment(attachmentId: string): Promise<void> {
  await client.deleteAttachment({ id: attachmentId });
  queryClient.removeQueries({ queryKey: attachmentServiceKeys.getAttachment({ id: attachmentId }) });
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
    attachmentServiceQueries.getAttachment({ id: attachmentId }, { staleTime: 0, gcTime: 0 }),
  );
  if (!response.attachment) {
    throw new Error("Attachment not found");
  }
  // Decoded protobuf bytes are never SharedArrayBuffer-backed, which is all Blob refuses.
  return new Blob([response.content as Uint8Array<ArrayBuffer>], { type: response.attachment.mimeType });
}
