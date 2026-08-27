// Copyright (c) 2025 Reliant Labs

import { grpcClient } from "./grpc-client";
import { getDaemonFileSystemClient } from "./grpc-client";
import { create } from "@bufbuild/protobuf";
import type {
  FileNode as ProtoFileNode,
  FileMetadata as ProtoFileMetadata,
} from "../gen/reliant/v1/filesystem_pb";
import { FileNodeType } from "../gen/reliant/v1/filesystem_pb";
import {
  GetFileTreeRequestSchema,
  GetFileContentRequestSchema,
  SaveFileContentRequestSchema,
  GetFileMetadataRequestSchema,
  GetFilePreviewInfoRequestSchema,
  GetFilePreviewRequestSchema,
  CreateFileOrFolderRequestSchema,
  DeleteFileOrFolderRequestSchema,
  CopyFileRequestSchema,
  SearchFilesRequestSchema,
  ReplaceInFilesRequestSchema,
  ListDirectoryRequestSchema,
  CreateDirectoryRequestSchema,
  FileViewerKind,
  type DirectoryEntry,
  type SearchResult as ProtoSearchResult,
  type SearchMatch as ProtoSearchMatch,
  type ReplaceResult as ProtoReplaceResult,
  type FilePreviewInfo as ProtoFilePreviewInfo,
} from "../gen/reliant/v1/filesystem_pb";
import type { FileNode } from "../components/FileBrowser";

// ============================================
// Frontend Type Definitions
// ============================================

export interface FileMetadata {
  name: string;
  path: string;
  size: number;
  modified: string;
  type: "file" | "directory";
  permissions?: string;
}

export type FileViewerKindValue = "text" | "image" | "pdf" | "audio" | "video" | "binary";

export interface FilePreviewInfo {
  name: string;
  path: string;
  size: number;
  modified: string;
  viewerKind: FileViewerKindValue;
  mimeType: string;
  isBinary: boolean;
  isEditable: boolean;
}

export interface SearchMatch {
  lineNumber: number;
  lineContent: string;
  matchStart: number;
  matchEnd: number;
  contextBefore: string[];
  contextAfter: string[];
}

export interface SearchResult {
  path: string;
  matches: SearchMatch[];
}

export interface SearchFilesResult {
  results: SearchResult[];
  totalMatches: number;
  truncated: boolean;
}

export interface ReplaceResult {
  path: string;
  replacements: number;
  success: boolean;
  error?: string;
}

export interface ReplaceInFilesResult {
  results: ReplaceResult[];
  totalReplacements: number;
  filesModified: number;
}

// ============================================
// Request Deduplication
// ============================================

// Map of cache keys to in-flight promises for getFileContent
const pendingRequests = new Map<string, Promise<string>>();

// Map of cache keys to in-flight promises for getFileTree
const pendingFileTreeRequests = new Map<string, Promise<FileNode[]>>();

// ============================================
// Conversion Functions: Proto -> Frontend
// ============================================

function protoFileNodeToFrontend(proto: ProtoFileNode): FileNode {
  const isDir = proto.type === FileNodeType.DIRECTORY;

  // Distinguish "not yet loaded" from "loaded and empty". A depth-limited walk
  // returns directory nodes at the boundary with no children but has_children
  // set — leave those children undefined so the tree lazily fetches on expand.
  let children: FileNode[] | undefined;
  if (proto.children && proto.children.length > 0) {
    children = proto.children.map(protoFileNodeToFrontend);
  } else if (isDir && !proto.hasChildren) {
    children = []; // genuinely empty directory (loaded)
  } else {
    children = undefined; // a file, or a boundary directory to lazily load
  }

  return {
    name: proto.name,
    path: proto.path,
    type: isDir ? "directory" : "file",
    children,
    hasChildren: isDir ? proto.hasChildren : undefined,
    size: proto.size !== undefined ? Number(proto.size) : undefined,
    modified: proto.modified,
  };
}

function protoMetadataToFrontend(proto: ProtoFileMetadata): FileMetadata {
  return {
    name: proto.name,
    path: proto.path,
    size: Number(proto.size),
    modified: proto.modified,
    type: proto.type === FileNodeType.DIRECTORY ? "directory" : "file",
    permissions: proto.permissions || undefined,
  };
}

function protoViewerKindToFrontend(kind: FileViewerKind): FileViewerKindValue {
  switch (kind) {
    case FileViewerKind.TEXT:
      return "text";
    case FileViewerKind.IMAGE:
      return "image";
    case FileViewerKind.PDF:
      return "pdf";
    case FileViewerKind.AUDIO:
      return "audio";
    case FileViewerKind.VIDEO:
      return "video";
    case FileViewerKind.BINARY:
    default:
      return "binary";
  }
}

function protoPreviewInfoToFrontend(proto: ProtoFilePreviewInfo): FilePreviewInfo {
  return {
    name: proto.name,
    path: proto.path,
    size: Number(proto.size),
    modified: proto.modified,
    viewerKind: protoViewerKindToFrontend(proto.viewerKind),
    mimeType: proto.mimeType,
    isBinary: proto.isBinary,
    isEditable: proto.isEditable,
  };
}

// ============================================
// Tree Helpers
// ============================================

/**
 * Recursively filters out hidden files/directories (names starting with '.')
 */
function filterHiddenFiles(nodes: FileNode[]): FileNode[] {
  return nodes
    .filter(node => !node.name.startsWith('.'))
    .map(node => ({
      ...node,
      children: node.children ? filterHiddenFiles(node.children) : undefined,
    }));
}

/**
 * Sorts file tree nodes: directories first (alphabetically), then files (alphabetically)
 * Recursively sorts children as well
 */
function sortFileTree(nodes: FileNode[]): FileNode[] {
  return nodes
    .sort((a, b) => {
      // Directories come before files
      if (a.type === "directory" && b.type === "file") return -1;
      if (a.type === "file" && b.type === "directory") return 1;
      
      // Within same type, sort alphabetically (case-insensitive)
      return a.name.toLowerCase().localeCompare(b.name.toLowerCase());
    })
    .map(node => ({
      ...node,
      children: node.children ? sortFileTree(node.children) : undefined
    }));
}

// ============================================
// FileSystem gRPC Client
// ============================================

export const filesystemGrpc = {
  /**
   * Get file tree for a project
   */
  async getFileTree(
    projectId: string,
    path: string = "/",
    showHidden: boolean = false,
    worktreeId?: string,
    chatId?: string,
    depth: number = 1
  ): Promise<FileNode[]> {
    // Always fetch with showHidden=true so all callers share one in-flight request.
    // Callers that want hidden files filtered get the result filtered client-side.
    // NOTE: this means the server walk always includes dot-directories no matter
    // what the caller asked for, which inflates every walk. Fixing it belongs
    // with the flat file-list RPC, not here.
    // depth is part of the key so a depth-2 root fetch and a depth-1 subdir fetch
    // never collide. Depth 0 defers to the server default (2); -1 walks as deep
    // as the server's node budget allows. Neither is unbounded — the default
    // here is 1 so a caller that forgets gets the cheapest useful answer.
    const cacheKey = `${projectId}:${worktreeId || 'main'}:${path}:${depth}`;

    let promise = pendingFileTreeRequests.get(cacheKey);
    if (!promise) {
      promise = (async () => {
        try {
          const client = await grpcClient.filesystem();
          const request = create(GetFileTreeRequestSchema, {
            projectId,
            path,
            showHidden: true,
            worktreeId,
            chatId,
            depth,
          });
          const response = await client.getFileTree(request);
          const files = response.files.map(protoFileNodeToFrontend);
          return sortFileTree(files);
        } finally {
          pendingFileTreeRequests.delete(cacheKey);
        }
      })();
      pendingFileTreeRequests.set(cacheKey, promise);
    }

    const result = await promise;
    return showHidden ? result : filterHiddenFiles(result);
  },

  /**
   * Get file content with request deduplication
   */
  async getFileContent(
    projectId: string,
    path: string,
    worktreeId?: string,
    chatId?: string
  ): Promise<string> {
    // Create unique cache key for this request
    const cacheKey = `${projectId}:${worktreeId || 'main'}:${path}`;
    
    // Check if this request is already in-flight
    const existingRequest = pendingRequests.get(cacheKey);
    if (existingRequest) {
      return existingRequest;
    }
    
    // Create new request promise
    const requestPromise = (async () => {
      try {
        const client = await grpcClient.filesystem();
        const request = create(GetFileContentRequestSchema, {
          projectId,
          path,
          worktreeId,
          chatId,
        });
        const response = await client.getFileContent(request);
        return response.content;
      } finally {
        // Clean up: remove from pending requests when done
        pendingRequests.delete(cacheKey);
      }
    })();
    
    // Store the promise so concurrent requests can reuse it
    pendingRequests.set(cacheKey, requestPromise);
    
    return requestPromise;
  },

  /**
   * Save file content
   */
  async saveFileContent(
    projectId: string,
    path: string,
    content: string,
    worktreeId?: string,
    chatId?: string
  ): Promise<void> {
    const client = await grpcClient.filesystem();
    const request = create(SaveFileContentRequestSchema, {
      projectId,
      path,
      content,
      worktreeId,
      chatId,
    });
    await client.saveFileContent(request);
  },

  /**
   * Get file metadata
   */
  async getFileMetadata(
    projectId: string,
    path: string,
    worktreeId?: string,
    chatId?: string
  ): Promise<FileMetadata> {
    const client = await grpcClient.filesystem();
    const request = create(GetFileMetadataRequestSchema, {
      projectId,
      path,
      worktreeId,
      chatId,
    });
    const response = await client.getFileMetadata(request);
    if (!response.metadata) {
      throw new Error("File metadata not found");
    }
    return protoMetadataToFrontend(response.metadata);
  },

  async getFilePreviewInfo(
    projectId: string,
    path: string,
    worktreeId?: string,
    chatId?: string
  ): Promise<FilePreviewInfo> {
    const client = await grpcClient.filesystem();
    const request = create(GetFilePreviewInfoRequestSchema, {
      projectId,
      path,
      worktreeId,
      chatId,
    });
    const response = await client.getFilePreviewInfo(request);
    if (!response.info) {
      throw new Error("File preview info not found");
    }
    return protoPreviewInfoToFrontend(response.info);
  },

  /**
   * Get file preview content (binary)
   */
  async getFilePreview(
    projectId: string,
    path: string,
    worktreeId?: string,
    chatId?: string
  ): Promise<{ content: Uint8Array; contentType: string; filename: string; size: bigint }> {
    const client = await grpcClient.filesystem();
    const request = create(GetFilePreviewRequestSchema, {
      projectId,
      path,
      worktreeId,
      chatId,
    });
    const response = await client.getFilePreview(request);
    return {
      content: response.content,
      contentType: response.contentType,
      filename: response.filename,
      size: response.size,
    };
  },

  /**
   * Create a file
   */
  async createFile(
    projectId: string,
    path: string,
    content: string = "",
    worktreeId?: string,
    chatId?: string
  ): Promise<void> {
    const client = await grpcClient.filesystem();
    const request = create(CreateFileOrFolderRequestSchema, {
      projectId,
      path,
      type: FileNodeType.FILE,
      content,
      worktreeId,
      chatId,
    });
    await client.createFileOrFolder(request);
  },

  /**
   * Create a folder
   */
  async createFolder(
    projectId: string,
    path: string,
    worktreeId?: string,
    chatId?: string
  ): Promise<void> {
    const client = await grpcClient.filesystem();
    const request = create(CreateFileOrFolderRequestSchema, {
      projectId,
      path,
      type: FileNodeType.DIRECTORY,
      content: "",
      worktreeId,
      chatId,
    });
    await client.createFileOrFolder(request);
  },

  /**
   * Delete a file or folder
   */
  async deleteFileOrFolder(
    projectId: string,
    path: string,
    worktreeId?: string,
    chatId?: string
  ): Promise<void> {
    const client = await grpcClient.filesystem();
    const request = create(DeleteFileOrFolderRequestSchema, {
      projectId,
      path,
      worktreeId,
      chatId,
    });
    await client.deleteFileOrFolder(request);
  },

  /**
   * Copy a file
   */
  async copyFile(
    projectId: string,
    sourcePath: string,
    destinationPath: string,
    worktreeId?: string,
    chatId?: string
  ): Promise<void> {
    const client = await grpcClient.filesystem();
    const request = create(CopyFileRequestSchema, {
      projectId,
      sourcePath,
      destinationPath,
      worktreeId,
      chatId,
    });
    await client.copyFile(request);
  },

  /**
   * Search for text within files in the workspace
   */
  async searchFiles(
    projectId: string,
    query: string,
    options?: {
      path?: string;
      worktreeId?: string;
      chatId?: string;
      filePattern?: string;
      caseSensitive?: boolean;
      maxResults?: number;
      contextLines?: number;
    }
  ): Promise<SearchFilesResult> {
    const client = await grpcClient.filesystem();
    const request = create(SearchFilesRequestSchema, {
      projectId,
      query,
      path: options?.path,
      worktreeId: options?.worktreeId,
      chatId: options?.chatId,
      filePattern: options?.filePattern,
      caseSensitive: options?.caseSensitive,
      maxResults: options?.maxResults,
      contextLines: options?.contextLines,
    });
    const response = await client.searchFiles(request);
    
    // Convert proto types to frontend types
    return {
      results: response.results.map((r: ProtoSearchResult) => ({
        path: r.path,
        matches: r.matches.map((m: ProtoSearchMatch) => ({
          lineNumber: m.lineNumber,
          lineContent: m.lineContent,
          matchStart: m.matchStart,
          matchEnd: m.matchEnd,
          contextBefore: [...m.contextBefore],
          contextAfter: [...m.contextAfter],
        })),
      })),
      totalMatches: response.totalMatches,
      truncated: response.truncated,
    };
  },

  /**
   * Replace text in files across the workspace
   */
  async replaceInFiles(
    projectId: string,
    searchText: string,
    replaceText: string,
    options?: {
      path?: string;
      worktreeId?: string;
      chatId?: string;
      filePattern?: string;
      caseSensitive?: boolean;
      filePaths?: string[];
    }
  ): Promise<ReplaceInFilesResult> {
    const client = await grpcClient.filesystem();
    const request = create(ReplaceInFilesRequestSchema, {
      projectId,
      searchText,
      replaceText,
      path: options?.path,
      worktreeId: options?.worktreeId,
      chatId: options?.chatId,
      filePattern: options?.filePattern,
      caseSensitive: options?.caseSensitive,
      filePaths: options?.filePaths ?? [],
    });
    const response = await client.replaceInFiles(request);
    
    return {
      results: response.results.map((r: ProtoReplaceResult) => ({
        path: r.path,
        replacements: r.replacements,
        success: r.success,
        error: r.error || undefined,
      })),
      totalReplacements: response.totalReplacements,
      filesModified: response.filesModified,
    };
  },
};

// ============================================
// Daemon FileSystem Client (ListDirectory)
// ============================================

export async function listDirectory(path: string): Promise<{ path: string; entries: DirectoryEntry[] }> {
  const client = getDaemonFileSystemClient();
  const request = create(ListDirectoryRequestSchema, { path });
  const response = await client.listDirectory(request);
  return { path: response.path, entries: response.entries };
}

// createDirectory makes a new directory at an arbitrary absolute path on the
// daemon (mkdir -p). Used by the project picker's "New folder" action.
export async function createDirectory(path: string): Promise<{ path: string }> {
  const client = getDaemonFileSystemClient();
  const request = create(CreateDirectoryRequestSchema, { path });
  const response = await client.createDirectory(request);
  return { path: response.path };
}

export type { DirectoryEntry };