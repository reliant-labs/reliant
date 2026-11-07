import { create } from 'zustand';
import { persist, subscribeWithSelector } from 'zustand/middleware';
import { toast } from '../lib/toast-manager';
import { deleteAttachment, uploadAttachment, type Attachment } from '../hooks/attachment-queries';
import { logger } from '../lib/logger';
import { getAttachmentType, isFileTypeSupported, getUnsupportedFileMessage } from '../lib/filetypes';

interface AttachmentStore {
  attachments: Map<string, Attachment[]>;
  uploading: boolean;
  blobUrls: Map<string, string>;

  attachFile: (sessionId: string, file: File) => Promise<Attachment>;
  /**
   * Upload `file` in place of `attachmentId`, keeping its position in the
   * draft, then delete the old one. Attachments are immutable on the server,
   * so this is how a draft attachment is edited. Resolves undefined when the
   * old attachment left the draft (removed or sent) while the upload ran; the
   * new upload is then deleted rather than resurrected.
   */
  replaceAttachment: (sessionId: string, attachmentId: string, file: File) => Promise<Attachment | undefined>;
  removeAttachment: (sessionId: string, attachmentId: string) => void;
  getAttachments: (sessionId: string) => Attachment[];
  clearAttachments: (sessionId: string) => void;
  getBlobUrl: (attachmentId: string) => string | undefined;
  reset: () => void;
}

/**
 * Images get a blob URL for their thumbnail, text files one for the text
 * viewer, both from the bytes already in hand. A draft restored after a reload
 * has neither, and its readers fetch the bytes from the server instead.
 */
function localBlobUrl(file: File): string | undefined {
  const type = getAttachmentType(file.name);
  return type === 'image' || type === 'file_reference' ? URL.createObjectURL(file) : undefined;
}

/** What survives a reload: each draft's attachment metadata, never bytes or blob URLs. */
interface PersistedAttachments {
  drafts: Record<string, Attachment[]>;
}

function isAttachment(value: unknown): value is Attachment {
  const a = value as Partial<Attachment> | null;
  return (
    typeof a === 'object' && a !== null &&
    typeof a.id === 'string' && a.id !== '' &&
    typeof a.filename === 'string' &&
    typeof a.mime_type === 'string' &&
    typeof a.size === 'number'
  );
}

function restoreDrafts(persisted: unknown): Map<string, Attachment[]> {
  const drafts = (persisted as Partial<PersistedAttachments> | null)?.drafts;
  const restored = new Map<string, Attachment[]>();
  if (!drafts || typeof drafts !== 'object') return restored;
  for (const [sessionId, list] of Object.entries(drafts)) {
    if (!Array.isArray(list)) continue;
    const valid = list.filter(isAttachment);
    if (valid.length > 0) restored.set(sessionId, valid);
  }
  return restored;
}

export const useAttachmentStore = create<AttachmentStore>()(
  subscribeWithSelector(
    persist(
      (set, get) => ({
        attachments: new Map(),
        uploading: false,
        blobUrls: new Map(),

        attachFile: async (sessionId: string, file: File) => {
          // Check if file type is supported
          if (!isFileTypeSupported(file.name)) {
            const message = getUnsupportedFileMessage(file.name);
            toast.error(message, { duration: 6000 });
            throw new Error(message);
          }

          set({ uploading: true });

          try {
            // The backend stores every upload's bytes and reads them at send time.
            const attachment = await uploadAttachment(file);

            const newBlobUrls = new Map(get().blobUrls);
            const blobUrl = localBlobUrl(file);
            if (blobUrl) {
              newBlobUrls.set(attachment.id, blobUrl);
            }

            // Add to attachments for this session
            const currentAttachments = get().attachments.get(sessionId) || [];
            const newAttachments = new Map(get().attachments);
            newAttachments.set(sessionId, [...currentAttachments, attachment]);

            set({
              attachments: newAttachments,
              uploading: false,
              blobUrls: newBlobUrls
            });

            return attachment;
          } catch (error) {
            const errorMessage = error instanceof Error ? error.message : 'Failed to attach file';
            set({ uploading: false });

            toast.error(`Failed to attach ${file.name}: ${errorMessage}`, {
              duration: 6000
            });

            throw error;
          }
        },

        replaceAttachment: async (sessionId: string, attachmentId: string, file: File) => {
          set({ uploading: true });
          let attachment: Attachment;
          try {
            attachment = await uploadAttachment(file);
          } catch (error) {
            set({ uploading: false });
            const errorMessage = error instanceof Error ? error.message : 'Upload failed';
            toast.error(`Failed to save ${file.name}: ${errorMessage}`, { duration: 6000 });
            throw error;
          }

          const current = get().attachments.get(sessionId) || [];
          const index = current.findIndex((a) => a.id === attachmentId);
          if (index === -1) {
            set({ uploading: false });
            deleteAttachment(attachment.id).catch((error) => {
              logger.warn('Failed to delete superseded attachment:', attachment.id, error);
            });
            return undefined;
          }

          const next = [...current];
          next[index] = attachment;
          const newAttachments = new Map(get().attachments);
          newAttachments.set(sessionId, next);

          const newBlobUrls = new Map(get().blobUrls);
          const oldBlobUrl = newBlobUrls.get(attachmentId);
          if (oldBlobUrl) {
            URL.revokeObjectURL(oldBlobUrl);
            newBlobUrls.delete(attachmentId);
          }
          const blobUrl = localBlobUrl(file);
          if (blobUrl) newBlobUrls.set(attachment.id, blobUrl);

          set({ attachments: newAttachments, blobUrls: newBlobUrls, uploading: false });

          deleteAttachment(attachmentId).catch((error) => {
            logger.warn('Failed to delete replaced attachment from server:', attachmentId, error);
          });
          return attachment;
        },

        removeAttachment: (sessionId: string, attachmentId: string) => {
          const currentAttachments = get().attachments.get(sessionId) || [];
          const filteredAttachments = currentAttachments.filter(a => a.id !== attachmentId);

          // Revoke blob URL to free memory
          const blobUrl = get().blobUrls.get(attachmentId);
          const newBlobUrls = new Map(get().blobUrls);
          if (blobUrl) {
            URL.revokeObjectURL(blobUrl);
            newBlobUrls.delete(attachmentId);
          }

          const newAttachments = new Map(get().attachments);
          if (filteredAttachments.length === 0) {
            newAttachments.delete(sessionId);
          } else {
            newAttachments.set(sessionId, filteredAttachments);
          }

          // Single atomic state update for instant UI response
          set({ attachments: newAttachments, blobUrls: newBlobUrls });

          // Delete from server in background (fire-and-forget)
          deleteAttachment(attachmentId).catch((error) => {
            logger.warn('Failed to delete attachment from server:', attachmentId, error);
          });
        },

        getAttachments: (sessionId: string) => {
          return get().attachments.get(sessionId) || [];
        },

        clearAttachments: (sessionId: string) => {
          const currentAttachments = get().attachments.get(sessionId) || [];
          const newBlobUrls = new Map(get().blobUrls);

          // Revoke blob URLs for all attachments in this session to free memory
          for (const attachment of currentAttachments) {
            const blobUrl = newBlobUrls.get(attachment.id);
            if (blobUrl) {
              URL.revokeObjectURL(blobUrl);
              newBlobUrls.delete(attachment.id);
            }
          }

          const newAttachments = new Map(get().attachments);
          newAttachments.delete(sessionId);
          set({ attachments: newAttachments, blobUrls: newBlobUrls });
        },

        getBlobUrl: (attachmentId: string) => {
          return get().blobUrls.get(attachmentId);
        },

        reset: () => {
          // Revoke all blob URLs to free memory
          const { blobUrls } = get();
          blobUrls.forEach((url) => {
            URL.revokeObjectURL(url);
          });

          set({
            attachments: new Map(),
            uploading: false,
            blobUrls: new Map(),
          });
        },
      }),
      {
        // Draft attachments survive a reload alongside the draft text
        // (workspace-state). Only metadata is kept: the bytes live on the
        // server until the draft is sent or the attachment removed, and
        // sign-out's reset() empties this with the rest of the store.
        name: 'reliant-composer-attachments',
        version: 1,
        partialize: (state): PersistedAttachments => ({
          drafts: Object.fromEntries(state.attachments),
        }),
        merge: (persisted, current) => ({
          ...current,
          attachments: restoreDrafts(persisted),
        }),
      },
    ),
  ),
);

export function formatFileSize(bytes: number): string {
  if (bytes === 0) return '0 Bytes';
  
  const k = 1024;
  const sizes = ['Bytes', 'KB', 'MB', 'GB'];
  const i = Math.floor(Math.log(bytes) / Math.log(k));
  
  return parseFloat((bytes / Math.pow(k, i)).toFixed(2)) + ' ' + sizes[i];
}

export function isImageFile(mimeType: string): boolean {
  return mimeType.startsWith('image/');
}

export function getFileTypeDescription(mimeType: string): string {
  if (mimeType.startsWith('image/')) return 'img';
  if (mimeType.includes('pdf')) return 'pdf';
  if (mimeType.includes('text')) return 'txt';
  if (mimeType.includes('json')) return 'json';
  return 'file';
}

// Re-export Attachment type for convenience
export type { Attachment };
