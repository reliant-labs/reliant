import { create } from 'zustand';
import { subscribeWithSelector } from 'zustand/middleware';
import { toast } from '../lib/toast-manager';
import { attachmentGrpc, type Attachment } from '../api/attachment-grpc';
import { logger } from '../lib/logger';
import { getAttachmentType, isFileTypeSupported, getUnsupportedFileMessage } from '../lib/filetypes';

interface AttachmentStore {
  attachments: Map<string, Attachment[]>;
  uploading: boolean;
  blobUrls: Map<string, string>;

  attachFile: (sessionId: string, file: File) => Promise<Attachment>;
  removeAttachment: (sessionId: string, attachmentId: string) => void;
  getAttachments: (sessionId: string) => Attachment[];
  clearAttachments: (sessionId: string) => void;
  getBlobUrl: (attachmentId: string) => string | undefined;
  reset: () => void;
}

export const useAttachmentStore = create<AttachmentStore>()(
  subscribeWithSelector((set, get) => ({
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
        const attachmentType = getAttachmentType(file.name);
        let attachment: Attachment;
        let blobUrl: string | undefined;

        if (attachmentType === 'image') {
          // For images, upload binary and create blob URL for preview
          blobUrl = URL.createObjectURL(file);
          attachment = await attachmentGrpc.uploadAttachment(file);
        } else {
          // For text files, also upload the content (same as images)
          // The backend will store it and read it at send time
          attachment = await attachmentGrpc.uploadAttachment(file);
          // No blob URL for text files - we'll show an icon instead
        }

        // Store the blob URL for this attachment (if any)
        const newBlobUrls = new Map(get().blobUrls);
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
      attachmentGrpc.deleteAttachment(attachmentId).catch((error) => {
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
  }))
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
