// Copyright (c) 2025 Reliant Labs

/**
 * The project's files, read and written on the open chat's machine.
 *
 * The one FileSystemService module for everything that is not a plain cached
 * read. The pull reads with no client state behind them are forge's generated
 * hooks (hooks/file-system-queries.ts: content search, the directory picker).
 * What stays here is what a query cache is the wrong container for:
 *
 *   - the file tree, which FileTree assembles itself, directory by directory
 *     as folders are expanded, so no single request's answer is "the tree";
 *   - file content and previews, which feed an editor buffer that a refetch
 *     must never overwrite, and can be megabytes;
 *   - the writes, each of which refreshes git status.
 *
 * Concurrent identical tree and content reads share one request through the
 * in-flight maps below. Nothing is cached once a request settles.
 */

import { ConnectError, Code } from "@connectrpc/connect";
import type { QueryClient } from "@tanstack/react-query";
import {
  FileNodeType,
  FileSystemService,
  FileViewerKind,
  type FileMetadata as ProtoFileMetadata,
  type FileNode as ProtoFileNode,
  type FilePreviewInfo as ProtoFilePreviewInfo,
} from "../gen/reliant/v1/filesystem_pb";
import type { FileNode } from "../components/FileBrowser";
import { fileSystemServiceKeys } from "../hooks/file-system-service-hooks_gen";
import { connectClient } from "../lib/connect";
import { notifyFilesRewritten } from "../lib/fileRewrites";
import { queryClient as appQueryClient } from "../lib/query-client";
import { useProjectStore } from "../store/projectStore";
import { triggerGitStatusRefresh } from "../store/gitStatusStore";

const client = connectClient(FileSystemService);

export interface FileSystemAPI {
  /** See {@link getFileTree} for `depth` semantics — it defaults to one level, NOT the whole tree. */
  getFileTree: (path?: string, showHidden?: boolean, worktreeId?: string, depth?: number) => Promise<FileNode[]>;
  getFileContent: (path: string, worktreeId?: string) => Promise<string>;
  saveFileContent: (path: string, content: string, worktreeId?: string) => Promise<void>;
  getFileMetadata: (path: string, worktreeId?: string) => Promise<FileMetadata>;
  getFilePreviewInfo: (path: string, worktreeId?: string) => Promise<FilePreviewInfo>;
  getFilePreviewBlob: (path: string, worktreeId?: string) => Promise<Blob>;
  createFile: (path: string, content?: string, worktreeId?: string) => Promise<void>;
  createFolder: (path: string, worktreeId?: string) => Promise<void>;
  deleteFileOrFolder: (path: string, worktreeId?: string) => Promise<void>;
  copyFile: (sourcePath: string, destinationPath: string, worktreeId?: string) => Promise<void>;
  moveFile: (sourcePath: string, destinationPath: string, worktreeId?: string) => Promise<void>;
}

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

/**
 * Depth requested when a caller does not say otherwise: the immediate children
 * of `path` and nothing below them. Deliberately the *safest* value, not the
 * most useful one — a caller that wants more levels must say how many.
 */
export const FILE_TREE_DEPTH_DEFAULT = 1;

/**
 * Sentinel for "walk as deep as the server's budget allows".
 *
 * This is NOT unbounded. The server caps every walk at `filetree.MaxTreeNodes`
 * (50,000 nodes) and skips both the canonical noise directories and anything
 * `.gitignore` excludes, then reports `truncated` when it stops early. Those
 * bounds are what make a whole-project walk affordable at all: a 106k-file
 * Unity project comes back as ~8.3k nodes because the 8.2 GB gitignored
 * `Library/` is never entered.
 *
 * Historical note, because the naming is a trap: depth `0` used to mean
 * "recurse forever" AND was the default, which is how a single Cmd+P walked a
 * whole game repo and exhausted the host file table (ENFILE), taking down both
 * Docker Desktop and the app. Depth `0` now means "server default depth", and
 * no value produces an unbounded walk.
 */
export const FILE_TREE_DEPTH_MAX = -1;

/**
 * Depth `0` defers to the server's default (currently 2 levels). Kept as a
 * named constant so callers never hand-write the magic number that used to
 * mean "unlimited".
 */
export const FILE_TREE_DEPTH_SERVER_DEFAULT = 0;

// ============================================
// Request Deduplication
// ============================================

// In-flight getFileContent requests, by (project, worktree, chat, path).
const pendingRequests = new Map<string, Promise<string>>();

// In-flight getFileTree requests, by (project, worktree, chat, path, depth).
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
// Request scope
// ============================================

// The chat store and worktree-queries are imported lazily, to keep this
// module out of their import graphs, and once: every read names the chat, and
// one import promise means two reads at once see the same module.
let chatStoreModule: Promise<typeof import("../store/chatStore")> | undefined;
let worktreeQueriesModule: Promise<typeof import("../hooks/worktree-queries")> | undefined;

/**
 * The chat the file APIs act for: the open one.
 *
 * The server sends each request to that chat's machine (its pinned machine,
 * else its workspace's owner), which is where its tools read and write.
 * Without it a request goes to the user's default machine, the wrong disk for
 * a chat on any other machine, main checkout included.
 */
async function activeChatIdForAPI(): Promise<string | undefined> {
  chatStoreModule ??= import("../store/chatStore");
  const { useChatStore } = await chatStoreModule;
  return useChatStore.getState().activeChatId ?? undefined;
}

function requireProjectId(): string {
  const currentProject = useProjectStore.getState().currentProject;
  if (!currentProject) {
    throw new Error("No current project selected");
  }
  return currentProject.id;
}

async function normalizeFilePathForAPI(path: string, worktreeId?: string): Promise<string> {
  if (!worktreeId) {
    return path;
  }

  worktreeQueriesModule ??= import("../hooks/worktree-queries");
  const { resolveWorktree } = await worktreeQueriesModule;
  // By id: the cache holds only the workspaces something asked for, and an
  // unknown one would leave an absolute path unstripped.
  const worktree = await resolveWorktree(worktreeId);
  const worktreeBasePath = worktree?.path;

  if (worktreeBasePath && path.startsWith(worktreeBasePath)) {
    let apiPath = path.slice(worktreeBasePath.length);
    if (apiPath.startsWith('/')) {
      apiPath = apiPath.slice(1);
    }
    return apiPath;
  }

  return path;
}

// ============================================
// Reads
// ============================================

/**
 * Fetches the file tree structure from the API.
 *
 * @param path - Optional path to start from (default: the project/worktree root)
 * @param showHidden - Optional flag to show hidden files (default: false)
 * @param worktreeId - Optional worktree ID to scope the file tree
 * @param depth - How many levels of children to return below `path`:
 *   - `N > 0` walks N levels (1 = immediate children only). Directories at the
 *     boundary come back with `hasChildren` set and `children` undefined, so
 *     callers can lazily fetch deeper levels.
 *   - `FILE_TREE_DEPTH_MAX` (-1) walks as deep as the server's node budget
 *     allows. Bounded, not unbounded — see the constant's docs.
 *   - `FILE_TREE_DEPTH_SERVER_DEFAULT` (0) defers to the server default.
 *   Defaults to {@link FILE_TREE_DEPTH_DEFAULT} (1).
 * @returns Promise resolving to an array of FileNode
 */
export async function getFileTree(path: string = "/", showHidden: boolean = false, worktreeId?: string, depth: number = FILE_TREE_DEPTH_DEFAULT): Promise<FileNode[]> {
  const currentProject = useProjectStore.getState().currentProject;
  if (!currentProject) {
    return [];
  }
  const projectId = currentProject.id;
  const chatId = await activeChatIdForAPI();

  // Always fetch with showHidden=true so all callers share one in-flight request.
  // Callers that want hidden files filtered get the result filtered client-side.
  // NOTE: this means the server walk always includes dot-directories no matter
  // what the caller asked for, which inflates every walk. Fixing it belongs
  // with the flat file-list RPC, not here.
  // depth is part of the key so a depth-2 root fetch and a depth-1 subdir fetch
  // never collide. The chat is in the key because the server reads from the
  // chat's machine: the same path for two chats can be two different disks.
  const cacheKey = `${projectId}:${worktreeId || 'main'}:${chatId || ''}:${path}:${depth}`;

  let promise = pendingFileTreeRequests.get(cacheKey);
  if (!promise) {
    promise = (async () => {
      try {
        const response = await client.getFileTree({
          projectId,
          path,
          showHidden: true,
          worktreeId,
          chatId,
          depth,
        });
        return sortFileTree(response.files.map(protoFileNodeToFrontend));
      } finally {
        pendingFileTreeRequests.delete(cacheKey);
      }
    })();
    pendingFileTreeRequests.set(cacheKey, promise);
  }

  const result = await promise;
  return showHidden ? result : filterHiddenFiles(result);
}

/**
 * Fetches the content of a specific file. Concurrent reads of one file share
 * a request.
 * @param path - Path to the file
 * @param worktreeId - Optional worktree ID to scope the file content
 * @returns Promise resolving to the file content as a string
 */
export async function getFileContent(path: string, worktreeId?: string): Promise<string> {
  try {
    const projectId = requireProjectId();
    const apiPath = await normalizeFilePathForAPI(path, worktreeId);
    const chatId = await activeChatIdForAPI();
    // The chat is part of the key for the same reason as in getFileTree: it
    // decides which machine is read.
    const cacheKey = `${projectId}:${worktreeId || 'main'}:${chatId || ''}:${apiPath}`;

    let promise = pendingRequests.get(cacheKey);
    if (!promise) {
      promise = (async () => {
        try {
          const response = await client.getFileContent({ projectId, path: apiPath, worktreeId, chatId });
          return response.content;
        } finally {
          pendingRequests.delete(cacheKey);
        }
      })();
      pendingRequests.set(cacheKey, promise);
    }
    return await promise;
  } catch (error) {
    console.error("Failed to fetch file content:", error);
    throw error; // Re-throw to let FileViewer handle it
  }
}

/**
 * Fetches metadata for a specific file or directory
 * @param path - Path to the file or directory
 * @param worktreeId - Optional worktree ID to scope the metadata query
 * @returns Promise resolving to FileMetadata
 */
export async function getFileMetadata(path: string, worktreeId?: string): Promise<FileMetadata> {
  try {
    const projectId = requireProjectId();
    const apiPath = await normalizeFilePathForAPI(path, worktreeId);
    const response = await client.getFileMetadata({
      projectId,
      path: apiPath,
      worktreeId,
      chatId: await activeChatIdForAPI(),
    });
    if (!response.metadata) {
      throw new Error("File metadata not found");
    }
    return protoMetadataToFrontend(response.metadata);
  } catch (error) {
    console.error("Failed to fetch file metadata:", error);
    throw error;
  }
}

export async function getFilePreviewInfo(path: string, worktreeId?: string): Promise<FilePreviewInfo> {
  try {
    const projectId = requireProjectId();
    const apiPath = await normalizeFilePathForAPI(path, worktreeId);
    const response = await client.getFilePreviewInfo({
      projectId,
      path: apiPath,
      worktreeId,
      chatId: await activeChatIdForAPI(),
    });
    if (!response.info) {
      throw new Error("File preview info not found");
    }
    return protoPreviewInfoToFrontend(response.info);
  } catch (error) {
    console.error("Failed to fetch file preview info:", error);
    throw error;
  }
}

export async function getFilePreviewBlob(path: string, worktreeId?: string): Promise<Blob> {
  const projectId = requireProjectId();
  const apiPath = await normalizeFilePathForAPI(path, worktreeId);
  const response = await client.getFilePreview({
    projectId,
    path: apiPath,
    worktreeId,
    chatId: await activeChatIdForAPI(),
  });
  return new Blob([response.content], { type: response.contentType });
}

export function isBinaryFileError(error: unknown): boolean {
  return error instanceof ConnectError && error.code === Code.FailedPrecondition;
}

// ============================================
// Writes
// ============================================

/**
 * Saves content to a specific file
 * @param path - Path to the file
 * @param content - Content to save
 * @param worktreeId - Optional worktree ID to scope the save operation
 * @returns Promise that resolves when save is complete
 */
export async function saveFileContent(path: string, content: string, worktreeId?: string): Promise<void> {
  try {
    const currentProject = useProjectStore.getState().currentProject;
    if (!currentProject) {
      return;
    }

    const apiPath = await normalizeFilePathForAPI(path, worktreeId);
    await client.saveFileContent({
      projectId: currentProject.id,
      path: apiPath,
      content,
      worktreeId,
      chatId: await activeChatIdForAPI(),
    });
    // Trigger git status refresh after file save
    triggerGitStatusRefresh(worktreeId, currentProject.id);
  } catch (error) {
    console.error("Failed to save file content:", error);
    throw error;
  }
}

/**
 * Creates a new file
 * @param path - Path for the new file
 * @param content - Optional initial content for the file
 * @param worktreeId - Optional worktree ID to scope the file creation
 * @returns Promise that resolves when file is created
 */
export async function createFile(path: string, content: string = "", worktreeId?: string): Promise<void> {
  try {
    const projectId = requireProjectId();
    await client.createFileOrFolder({
      projectId,
      path,
      type: FileNodeType.FILE,
      content,
      worktreeId,
      chatId: await activeChatIdForAPI(),
    });
    // Trigger git status refresh after file creation
    triggerGitStatusRefresh(worktreeId, projectId);
  } catch (error) {
    console.error("Failed to create file:", error);
    throw error;
  }
}

/**
 * Creates a new folder
 * @param path - Path for the new folder
 * @param worktreeId - Optional worktree ID to scope the folder creation
 * @returns Promise that resolves when folder is created
 */
export async function createFolder(path: string, worktreeId?: string): Promise<void> {
  try {
    const projectId = requireProjectId();
    await client.createFileOrFolder({
      projectId,
      path,
      type: FileNodeType.DIRECTORY,
      content: "",
      worktreeId,
      chatId: await activeChatIdForAPI(),
    });
    // Trigger git status refresh after folder creation
    triggerGitStatusRefresh(worktreeId, projectId);
  } catch (error) {
    console.error("Failed to create folder:", error);
    throw error;
  }
}

/**
 * Deletes a file or folder
 * @param path - Path to the file or folder to delete
 * @param worktreeId - Optional worktree ID to scope the delete operation
 * @returns Promise that resolves when deletion is complete
 */
export async function deleteFileOrFolder(path: string, worktreeId?: string): Promise<void> {
  try {
    const projectId = requireProjectId();
    await client.deleteFileOrFolder({
      projectId,
      path,
      worktreeId,
      chatId: await activeChatIdForAPI(),
    });
    // Trigger git status refresh after deletion
    triggerGitStatusRefresh(worktreeId, projectId);
  } catch (error) {
    console.error("Failed to delete file or folder:", error);
    throw error;
  }
}

async function copyOnServer(projectId: string, sourcePath: string, destinationPath: string, worktreeId?: string): Promise<void> {
  await client.copyFile({
    projectId,
    sourcePath,
    destinationPath,
    worktreeId,
    chatId: await activeChatIdForAPI(),
  });
}

/**
 * Copies a file to a new location
 * @param sourcePath - Path to the source file
 * @param destinationPath - Path for the copied file
 * @param worktreeId - Optional worktree ID to scope the copy operation
 * @returns Promise that resolves when copy is complete
 */
export async function copyFile(sourcePath: string, destinationPath: string, worktreeId?: string): Promise<void> {
  try {
    const projectId = requireProjectId();
    await copyOnServer(projectId, sourcePath, destinationPath, worktreeId);
    // Trigger git status refresh after file copy
    triggerGitStatusRefresh(worktreeId, projectId);
  } catch (error) {
    console.error("Failed to copy file:", error);
    throw error;
  }
}

/**
 * Recursively copies a directory and all its contents
 * @param sourcePath - Path to the source directory
 * @param destinationPath - Path for the copied directory
 * @param worktreeId - Optional worktree ID to scope the copy operation
 * @returns Promise that resolves when copy is complete
 */
export async function copyDirectoryRecursive(sourcePath: string, destinationPath: string, worktreeId?: string): Promise<void> {
  try {
    requireProjectId();

    // Immediate children only — this function walks the subtree itself, so
    // fetching deeper levels here would just re-fetch what the recursion covers.
    const sourceTree = await getFileTree(sourcePath, false, worktreeId, FILE_TREE_DEPTH_DEFAULT);

    // Copy each item in the directory
    for (const item of sourceTree) {
      const itemSourcePath = item.path;
      const itemDestinationPath = `${destinationPath}${item.path.substring(sourcePath.length)}`;

      if (item.type === "directory") {
        // Create the destination directory
        await createFolder(itemDestinationPath, worktreeId);
        // Recursively copy the directory contents
        await copyDirectoryRecursive(itemSourcePath, itemDestinationPath, worktreeId);
      } else {
        // Copy the file
        await copyFile(itemSourcePath, itemDestinationPath, worktreeId);
      }
    }
  } catch (error) {
    console.error("Failed to copy directory recursively:", error);
    throw error;
  }
}

/**
 * Moves a file to a new location (copy + delete)
 * @param sourcePath - Path to the source file
 * @param destinationPath - Path for the moved file
 * @param worktreeId - Optional worktree ID to scope the move operation
 * @returns Promise that resolves when move is complete
 */
export async function moveFile(sourcePath: string, destinationPath: string, worktreeId?: string): Promise<void> {
  try {
    const projectId = requireProjectId();
    // Move = copy + delete
    await copyOnServer(projectId, sourcePath, destinationPath, worktreeId);
    await deleteFileOrFolder(sourcePath, worktreeId);
  } catch (error) {
    console.error("Failed to move file:", error);
    throw error;
  }
}

/**
 * Replaces text in files across the workspace.
 *
 * Imperative, not a generated mutation, because what it changes is not only
 * the server's state: an editor may have one of these files open. So after
 * the write it tells every open editor which files it rewrote (a clean buffer
 * reloads, a dirty one is left alone; lib/fileRewrites), re-runs any mounted
 * content search (its matches are gone), and refreshes git status.
 *
 * @param searchText - Text to search for
 * @param replaceText - Text to replace with
 * @param options - Replace options
 * @param qc - The query cache holding the searches to re-run
 * @returns Promise resolving to replace results
 */
export async function replaceInFiles(
  searchText: string,
  replaceText: string,
  options?: {
    path?: string;
    worktreeId?: string;
    filePattern?: string;
    caseSensitive?: boolean;
    filePaths?: string[];
  },
  qc: QueryClient = appQueryClient,
): Promise<ReplaceInFilesResult> {
  const projectId = requireProjectId();

  const response = await client.replaceInFiles({
    projectId,
    searchText,
    replaceText,
    path: options?.path,
    worktreeId: options?.worktreeId,
    chatId: await activeChatIdForAPI(),
    filePattern: options?.filePattern,
    caseSensitive: options?.caseSensitive,
    filePaths: options?.filePaths ?? [],
  });

  const result: ReplaceInFilesResult = {
    results: response.results.map((r) => ({
      path: r.path,
      replacements: r.replacements,
      success: r.success,
      error: r.error || undefined,
    })),
    totalReplacements: response.totalReplacements,
    filesModified: response.filesModified,
  };

  notifyFilesRewritten({
    worktreeId: options?.worktreeId,
    paths: result.results.filter((r) => r.success).map((r) => r.path),
  });
  void qc.invalidateQueries({ queryKey: fileSystemServiceKeys.searchFiles.all });
  // Trigger git status refresh after replace in files
  triggerGitStatusRefresh(options?.worktreeId, projectId);
  return result;
}

export const fileSystemAPI: FileSystemAPI = {
  getFileTree,
  getFileContent,
  saveFileContent,
  getFileMetadata,
  getFilePreviewInfo,
  getFilePreviewBlob,
  createFile,
  createFolder,
  deleteFileOrFolder,
  copyFile,
  moveFile,
};
