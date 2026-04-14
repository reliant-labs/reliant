// Copyright (c) 2025 Reliant Labs

import { ConnectError, Code } from "@connectrpc/connect";
import { filesystemGrpc } from "./filesystem-grpc";
import type { FileNode } from "../components/FileBrowser";
import { useProjectStore } from "../store/projectStore";
import { triggerGitStatusRefresh } from "../store/gitStatusStore";

// Re-export search types
export type { SearchResult, SearchMatch, SearchFilesResult, ReplaceResult, ReplaceInFilesResult, FilePreviewInfo, FileViewerKindValue } from "./filesystem-grpc";

export interface FileSystemAPI {
  getFileTree: (path?: string, showHidden?: boolean, worktreeId?: string) => Promise<FileNode[]>;
  getFileContent: (path: string, worktreeId?: string) => Promise<string>;
  saveFileContent: (path: string, content: string, worktreeId?: string) => Promise<void>;
  getFileMetadata: (path: string, worktreeId?: string) => Promise<FileMetadata>;
  getFilePreviewInfo: (path: string, worktreeId?: string) => Promise<import("./filesystem-grpc").FilePreviewInfo>;
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


/**
 * Fetches the file tree structure from the API
 * @param path - Optional path to start from (default: root)
 * @param showHidden - Optional flag to show hidden files (default: false)
 * @param worktreeId - Optional worktree ID to scope the file tree
 * @returns Promise resolving to an array of FileNode
 */
export async function getFileTree(path: string = "/", showHidden: boolean = false, worktreeId?: string): Promise<FileNode[]> {
  const currentProject = useProjectStore.getState().currentProject;
  if (!currentProject) {
    return [];
  }

  return await filesystemGrpc.getFileTree(currentProject.id, path, showHidden, worktreeId);
}

/**
 * Fetches the content of a specific file with request deduplication
 * @param path - Path to the file
 * @param worktreeId - Optional worktree ID to scope the file content
 * @returns Promise resolving to the file content as a string
 */
async function normalizeFilePathForAPI(path: string, worktreeId?: string): Promise<string> {
  if (!worktreeId) {
    return path;
  }

  const { useWorktreeStore } = await import('../store/worktreeStore');
  const worktree = useWorktreeStore.getState().worktrees.find(wt => wt.id === worktreeId);
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

export async function getFileContent(path: string, worktreeId?: string): Promise<string> {
  const currentProject = useProjectStore.getState().currentProject;
  if (!currentProject) {
    throw new Error("No current project selected");
  }

  try {
    const apiPath = await normalizeFilePathForAPI(path, worktreeId);
    return await filesystemGrpc.getFileContent(currentProject.id, apiPath, worktreeId);
  } catch (error) {
    console.error("Failed to fetch file content:", error);
    throw error; // Re-throw to let FileViewer handle it
  }
}

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
    await filesystemGrpc.saveFileContent(currentProject.id, apiPath, content, worktreeId);
    // Trigger git status refresh after file save
    triggerGitStatusRefresh(worktreeId, currentProject.id);
  } catch (error) {
    console.error("Failed to save file content:", error);
    throw error;
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
    const currentProject = useProjectStore.getState().currentProject;
    if (!currentProject) {
      throw new Error("No current project selected");
    }

    const apiPath = await normalizeFilePathForAPI(path, worktreeId);
    return await filesystemGrpc.getFileMetadata(currentProject.id, apiPath, worktreeId);
  } catch (error) {
    console.error("Failed to fetch file metadata:", error);
    throw error;
  }
}

export async function getFilePreviewInfo(path: string, worktreeId?: string) {
  try {
    const currentProject = useProjectStore.getState().currentProject;
    if (!currentProject) {
      throw new Error("No current project selected");
    }

    const apiPath = await normalizeFilePathForAPI(path, worktreeId);
    return await filesystemGrpc.getFilePreviewInfo(currentProject.id, apiPath, worktreeId);
  } catch (error) {
    console.error("Failed to fetch file preview info:", error);
    throw error;
  }
}

export async function getFilePreviewBlob(path: string, worktreeId?: string): Promise<Blob> {
  const currentProject = useProjectStore.getState().currentProject;
  if (!currentProject) {
    throw new Error("No current project selected");
  }

  const apiPath = await normalizeFilePathForAPI(path, worktreeId);
  const response = await filesystemGrpc.getFilePreview(currentProject.id, apiPath, worktreeId);
  return new Blob([response.content], { type: response.contentType });
}

export function isBinaryFileError(error: unknown): boolean {
  return error instanceof ConnectError && error.code === Code.FailedPrecondition;
}

/**
 * Searches for text within files in the workspace
 * @param query - Search query (supports regex)
 * @param options - Search options
 * @returns Promise resolving to search results
 */
export async function searchFiles(
  query: string,
  options?: {
    path?: string;
    worktreeId?: string;
    filePattern?: string;
    caseSensitive?: boolean;
    maxResults?: number;
    contextLines?: number;
  }
) {
  const currentProject = useProjectStore.getState().currentProject;
  if (!currentProject) {
    throw new Error("No current project selected");
  }

  return await filesystemGrpc.searchFiles(currentProject.id, query, options);
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
    const currentProject = useProjectStore.getState().currentProject;
    if (!currentProject) {
      throw new Error("No current project selected");
    }

    await filesystemGrpc.createFile(currentProject.id, path, content, worktreeId);
    // Trigger git status refresh after file creation
    triggerGitStatusRefresh(worktreeId, currentProject.id);
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
    const currentProject = useProjectStore.getState().currentProject;
    if (!currentProject) {
      throw new Error("No current project selected");
    }

    await filesystemGrpc.createFolder(currentProject.id, path, worktreeId);
    // Trigger git status refresh after folder creation
    triggerGitStatusRefresh(worktreeId, currentProject.id);
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
    const currentProject = useProjectStore.getState().currentProject;
    if (!currentProject) {
      throw new Error("No current project selected");
    }

    await filesystemGrpc.deleteFileOrFolder(currentProject.id, path, worktreeId);
    // Trigger git status refresh after deletion
    triggerGitStatusRefresh(worktreeId, currentProject.id);
  } catch (error) {
    console.error("Failed to delete file or folder:", error);
    throw error;
  }
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
    const currentProject = useProjectStore.getState().currentProject;
    if (!currentProject) {
      throw new Error("No current project selected");
    }

    await filesystemGrpc.copyFile(currentProject.id, sourcePath, destinationPath, worktreeId);
    // Trigger git status refresh after file copy
    triggerGitStatusRefresh(worktreeId, currentProject.id);
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
    const currentProject = useProjectStore.getState().currentProject;
    if (!currentProject) {
      throw new Error("No current project selected");
    }

    // Get the file tree for the source directory
    const sourceTree = await getFileTree(sourcePath, false, worktreeId);
    
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
    const currentProject = useProjectStore.getState().currentProject;
    if (!currentProject) {
      throw new Error("No current project selected");
    }

    // Move = copy + delete
    await filesystemGrpc.copyFile(currentProject.id, sourcePath, destinationPath, worktreeId);
    await deleteFileOrFolder(sourcePath, worktreeId);
  } catch (error) {
    console.error("Failed to move file:", error);
    throw error;
  }
}

/**
 * Replaces text in files across the workspace
 * @param searchText - Text to search for
 * @param replaceText - Text to replace with
 * @param options - Replace options
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
  }
) {
  const currentProject = useProjectStore.getState().currentProject;
  if (!currentProject) {
    throw new Error("No current project selected");
  }

  const result = await filesystemGrpc.replaceInFiles(currentProject.id, searchText, replaceText, options);
  // Trigger git status refresh after replace in files
  triggerGitStatusRefresh(options?.worktreeId, currentProject.id);
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