// Copyright (c) 2025 Reliant Labs

import { grpcClient } from "./grpc-client";
import { create } from "@bufbuild/protobuf";
import type { AttachmentInfo as ProtoAttachmentInfo } from "../gen/reliant/v1/attachment_pb";
import {
  UploadAttachmentRequestSchema,
  CreateFileReferenceRequestSchema,
  GetAttachmentRequestSchema,
  DeleteAttachmentRequestSchema,
} from "../gen/reliant/v1/attachment_pb";
import { getAttachmentType, type AttachmentType } from "../lib/filetypes";

// ============================================
// Frontend Type Definitions
// ============================================

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

export interface AttachmentInfo extends Attachment {
  user_id: string;
  created_at: string;
  updated_at: string;
}

// ============================================
// Conversion Functions: Proto -> Frontend
// ============================================

function protoAttachmentInfoToFrontend(proto: ProtoAttachmentInfo): Attachment {
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

// ============================================
// File Utilities
// ============================================

/**
 * Convert a File object to Uint8Array for gRPC bytes field
 */
async function fileToUint8Array(file: File): Promise<Uint8Array> {
  const arrayBuffer = await file.arrayBuffer();
  return new Uint8Array(arrayBuffer);
}

// ============================================
// Attachment gRPC Client
// ============================================

export const attachmentGrpc = {
  /**
   * Upload an image file attachment (binary upload)
   */
  async uploadAttachment(file: File): Promise<Attachment> {
    const client = grpcClient.attachment();
    
    // Convert file to bytes
    const content = await fileToUint8Array(file);
    
    const request = create(UploadAttachmentRequestSchema, {
      filename: file.name,
      content: content,
      mimeType: file.type || "application/octet-stream",
    });
    
    const response = await client.uploadAttachment(request);
    
    if (!response.attachment) {
      throw new Error("Upload failed: no attachment returned");
    }
    
    return protoAttachmentInfoToFrontend(response.attachment);
  },

  /**
   * Create a file reference (for text files)
   * Content bytes must be provided — the server does not read the filesystem.
   * @param filePath - Absolute path to the file on disk (kept as metadata for display)
   * @param content - File content as Uint8Array
   * @param filename - Optional filename (defaults to basename of path)
   */
  async createFileReference(filePath: string, content: Uint8Array, filename?: string): Promise<Attachment> {
    const client = grpcClient.attachment();
    
    const request = create(CreateFileReferenceRequestSchema, {
      filePath,
      filename: filename || filePath.split('/').pop() || filePath,
      content,
    });
    
    const response = await client.createFileReference(request);
    
    if (!response.attachment) {
      throw new Error("Failed to create file reference: no attachment returned");
    }
    
    return protoAttachmentInfoToFrontend(response.attachment);
  },

  /**
   * Smart attach - automatically chooses upload vs file reference based on type
   * @param file - File object (for images) or path string (for text files from Electron)
   */
  async attach(fileOrPath: File | { path: string; name: string; size: number }): Promise<Attachment> {
    // If it's a File object with a path property (Electron file), use that
    const _filePath = 'path' in fileOrPath ? fileOrPath.path : (fileOrPath as any).path;
    const filename = 'path' in fileOrPath ? fileOrPath.name : fileOrPath.name;
    
    const attachmentType = getAttachmentType(filename);
    
    if (attachmentType === 'image') {
      // For images, upload the binary content
      if (fileOrPath instanceof File) {
        return this.uploadAttachment(fileOrPath);
      }
      // If it's an Electron file reference, we need to read it first
      throw new Error("Image upload requires a File object");
    } else if (attachmentType === 'file_reference') {
      // For text files from Electron with a File object, upload the content directly
      if (fileOrPath instanceof File) {
        return this.uploadAttachment(fileOrPath);
      }
      // Path-only references: caller must provide content bytes via createFileReference directly
      throw new Error("File reference requires content bytes. Use createFileReference() with content, or pass a File object.");
    } else {
      throw new Error(`Unsupported file type: ${filename}`);
    }
  },

  /**
   * Get an attachment by ID (includes file content)
   */
  async getAttachment(attachmentId: string): Promise<{ attachment: Attachment; content: Uint8Array }> {
    const client = grpcClient.attachment();
    
    const request = create(GetAttachmentRequestSchema, {
      attachmentId,
    });
    
    const response = await client.getAttachment(request);
    
    if (!response.attachment) {
      throw new Error("Attachment not found");
    }
    
    return {
      attachment: protoAttachmentInfoToFrontend(response.attachment),
      content: response.content,
    };
  },

  /**
   * Get attachment content as a Blob (for display/download)
   */
  async getAttachmentAsBlob(attachmentId: string): Promise<Blob> {
    const { attachment, content } = await this.getAttachment(attachmentId);
    return new Blob([content], { type: attachment.mime_type });
  },

  /**
   * Get attachment content as a data URL (for img src, etc.)
   */
  async getAttachmentAsDataURL(attachmentId: string): Promise<string> {
    const blob = await this.getAttachmentAsBlob(attachmentId);
    return new Promise((resolve, reject) => {
      const reader = new FileReader();
      reader.onloadend = () => resolve(reader.result as string);
      reader.onerror = reject;
      reader.readAsDataURL(blob);
    });
  },

  /**
   * Delete an attachment
   */
  async deleteAttachment(attachmentId: string): Promise<void> {
    const client = grpcClient.attachment();
    
    const request = create(DeleteAttachmentRequestSchema, {
      attachmentId,
    });
    
    await client.deleteAttachment(request);
  },
};