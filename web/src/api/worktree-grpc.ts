// Copyright (c) 2025 Reliant Labs

import { grpcClient } from "./grpc-client";
import { create } from "@bufbuild/protobuf";
import type {
  Worktree as ProtoWorktree,
  CleanupMetadata as ProtoCleanupMetadata,
  DiscoveredWorktree as ProtoDiscoveredWorktree,
  GitCommit as ProtoGitCommit,
  WorktreeFileChange as ProtoFileChange,
} from "../gen/reliant/v1/worktree_pb";
import { WorktreeStatus } from "../gen/reliant/v1/worktree_pb";
import { FileChangeStatus } from "../gen/reliant/v1/common_pb";

export { WorktreeStatus };
import {
  CreateWorktreeRequestSchema,
  BatchCreateWorktreesRequestSchema,
  ListWorktreesRequestSchema,
  GetWorktreeRequestSchema,
  UpdateWorktreeRequestSchema,
  DeleteWorktreeRequestSchema,
  ArchiveWorktreeRequestSchema,
  UnarchiveWorktreeRequestSchema,
  ImportWorktreeRequestSchema,
  DiscoverWorktreesRequestSchema,
  RecreateWorktreeRequestSchema,
  GetWorktreeChangesRequestSchema,
  GetWorktreeGitStatusRequestSchema,
  GetWorktreeCommitsRequestSchema,
  StageFilesRequestSchema,
  UnstageFilesRequestSchema,
  RevertFilesRequestSchema,
  CommitWorktreeRequestSchema,
  PushWorktreeRequestSchema,
  PullWorktreeRequestSchema,
  GetWorktreePRRequestSchema,
  CreateWorktreePRRequestSchema,
} from "../gen/reliant/v1/worktree_pb";

// Type definitions matching frontend expectations (snake_case to match store interface)

export interface CleanupMetadata {
  directory_deleted: boolean;
  branch_deleted: boolean;
}

export interface Worktree {
  id: string;
  name: string;
  path: string;
  branch: string;
  base_branch: string;
  project_id: string;
  chat_id?: string;
  session_id?: string;
  status: WorktreeStatus;
  is_main: boolean;
  created_at: string;
  updated_at: string;
  last_active: string;
  deleted_at?: string;
  cleanup_metadata?: CleanupMetadata;
}

export interface DiscoveredWorktree {
  path: string;
  name: string;
  branch: string;
  is_imported: boolean;
  is_prunable: boolean;
  imported_id?: string;
}

export interface GitCommit {
  hash: string;
  short_hash: string;
  author: string;
  email: string;
  date: string;
  message: string;
}

export interface WorktreeFileChange {
  path: string;
  status: FileChangeStatus;
  is_new: boolean;
  diff: string;
}

export interface WorktreeChanges {
  files: WorktreeFileChange[];
  total_files: number;
  branch: string;
  ahead: number;
  behind: number;
  default_branch: string;  // Repository's default branch (for PR targeting)
}

export interface WorktreeGitStatus {
  is_clean: boolean;
  current_branch: string;
  ahead: number;
  behind: number;
  staged_files: string[];
  modified_files: string[];
  untracked_files: string[];
  has_remote: boolean;
}

export interface WorktreeCommits {
  commits: GitCommit[];
  total: number;
  branch: string;
  base_branch: string;
  comparison_mode: boolean;
  comparison_ref: string;
  current_branch: string;
}

export interface PRInfo {
  exists: boolean;
  url?: string;
  number?: number;
  title?: string;
  state?: string;
}

export interface BatchCreateWorktreeResult {
  repo_id: string;
  worktree?: Worktree;
  error?: string;
}

export interface BatchCreateResult {
  results: BatchCreateWorktreeResult[];
  all_succeeded: boolean;
  rolled_back: boolean;
}

// Convert proto Worktree to frontend Worktree
function protoToFrontend(proto: ProtoWorktree): Worktree {
  return {
    id: proto.id,
    name: proto.name,
    path: proto.path,
    branch: proto.branch,
    base_branch: proto.baseBranch,
    project_id: proto.projectId,
    chat_id: proto.chatId || undefined,
    status: proto.status,
    is_main: proto.isMain,
    created_at: proto.createdAt,
    updated_at: proto.updatedAt,
    last_active: proto.lastActive,
    deleted_at: proto.deletedAt || undefined,
    cleanup_metadata: proto.cleanupMetadata
      ? protoCleanupMetadataToFrontend(proto.cleanupMetadata)
      : undefined,
  };
}

// Convert proto CleanupMetadata to frontend CleanupMetadata
function protoCleanupMetadataToFrontend(proto: ProtoCleanupMetadata): CleanupMetadata {
  return {
    directory_deleted: proto.directoryDeleted,
    branch_deleted: proto.branchDeleted,
  };
}

// Convert proto DiscoveredWorktree to frontend DiscoveredWorktree
function protoDiscoveredToFrontend(proto: ProtoDiscoveredWorktree): DiscoveredWorktree {
  return {
    path: proto.path,
    name: proto.name,
    branch: proto.branch,
    is_imported: proto.isImported,
    is_prunable: proto.isPrunable,
    imported_id: proto.importedId || undefined,
  };
}

// Convert proto GitCommit to frontend GitCommit
function protoCommitToFrontend(proto: ProtoGitCommit): GitCommit {
  return {
    hash: proto.hash,
    short_hash: proto.shortHash,
    author: proto.author,
    email: proto.email,
    date: proto.date,
    message: proto.message,
  };
}

// Convert proto WorktreeFileChange to frontend WorktreeFileChange
function protoFileChangeToFrontend(proto: ProtoFileChange): WorktreeFileChange {
  return {
    path: proto.path,
    status: proto.status,
    is_new: proto.isNew,
    diff: proto.diff,
  };
}

export const worktreeGrpc = {
  // =============================================================================
  // CRUD Operations
  // =============================================================================

  // Create a new worktree
  async create(
    projectId: string,
    name: string,
    branch: string,
    options?: {
      baseBranch?: string;
      chatId?: string;
      copyFiles?: string[];
      force?: boolean;
      sourceWorktreeId?: string; // Source worktree to copy files from
    }
  ): Promise<Worktree> {
    const client = grpcClient.worktree();
    const request = create(CreateWorktreeRequestSchema, {
      projectId,
      name,
      branch,
      baseBranch: options?.baseBranch,
      chatId: options?.chatId,
      copyFiles: options?.copyFiles || [],
      force: options?.force || false,
      sourceWorktreeId: options?.sourceWorktreeId,
    });
    const response = await client.createWorktree(request);
    if (!response.worktree) throw new Error("No worktree in response");
    return protoToFrontend(response.worktree);
  },

  // Create worktrees in multiple repos atomically (all-or-nothing).
  async batchCreate(
    projectId: string,
    repoIds: string[],
    name: string,
    branch: string,
    options?: {
      baseBranch?: string;
      chatId?: string;
      copyFiles?: string[];
      force?: boolean;
    }
  ): Promise<BatchCreateResult> {
    const client = grpcClient.worktree();
    const request = create(BatchCreateWorktreesRequestSchema, {
      projectId,
      repoIds,
      name,
      branch,
      baseBranch: options?.baseBranch,
      chatId: options?.chatId,
      copyFiles: options?.copyFiles || [],
      force: options?.force || false,
    });
    const response = await client.batchCreateWorktrees(request);
    return {
      results: response.results.map((r) => ({
        repo_id: r.repoId,
        worktree: r.worktree ? protoToFrontend(r.worktree) : undefined,
        error: r.error,
      })),
      all_succeeded: response.allSucceeded,
      rolled_back: response.rolledBack,
    };
  },

  // List worktrees for a project
  async list(
    projectId: string,
    options?: { chatId?: string; limit?: number; includeArchived?: boolean }
  ): Promise<{ worktrees: Worktree[]; total: number }> {
    const client = grpcClient.worktree();
    const request = create(ListWorktreesRequestSchema, {
      projectId,
      chatId: options?.chatId,
      limit: options?.limit || 100,
      includeArchived: options?.includeArchived ?? false,
    });
    const response = await client.listWorktrees(request);
    return {
      worktrees: response.worktrees.map(protoToFrontend),
      total: response.total,
    };
  },

  // Get a worktree by ID
  async get(worktreeId: string): Promise<Worktree> {
    const client = grpcClient.worktree();
    const request = create(GetWorktreeRequestSchema, { worktreeId });
    const response = await client.getWorktree(request);
    if (!response.worktree) throw new Error("No worktree in response");
    return protoToFrontend(response.worktree);
  },

  // Update a worktree
  async update(
    worktreeId: string,
    updates: { name?: string; status?: WorktreeStatus; baseBranch?: string }
  ): Promise<Worktree> {
    const client = grpcClient.worktree();
    const request = create(UpdateWorktreeRequestSchema, {
      worktreeId,
      name: updates.name,
      status: updates.status,
      baseBranch: updates.baseBranch,
    });
    const response = await client.updateWorktree(request);
    if (!response.worktree) throw new Error("No worktree in response");
    return protoToFrontend(response.worktree);
  },

  // Delete a worktree (permanent delete if already archived, otherwise archives)
  async delete(
    worktreeId: string,
    options?: { deleteLocalDirectory?: boolean; deleteGitBranch?: boolean }
  ): Promise<{ message: string; deleted_directory: boolean; deleted_branch: boolean; is_permanent_delete: boolean }> {
    const client = grpcClient.worktree();
    const request = create(DeleteWorktreeRequestSchema, {
      worktreeId,
      deleteLocalDirectory: options?.deleteLocalDirectory || false,
      deleteGitBranch: options?.deleteGitBranch || false,
    });
    const response = await client.deleteWorktree(request);
    return {
      message: response.message,
      deleted_directory: response.deletedDirectory,
      deleted_branch: response.deletedBranch,
      is_permanent_delete: response.isPermanentDelete,
    };
  },

  // Archive a worktree
  async archive(
    worktreeId: string,
    options?: { deleteLocalDirectory?: boolean; deleteGitBranch?: boolean }
  ): Promise<{ message: string; deleted_directory: boolean; deleted_branch: boolean }> {
    const client = grpcClient.worktree();
    const request = create(ArchiveWorktreeRequestSchema, {
      worktreeId,
      deleteLocalDirectory: options?.deleteLocalDirectory || false,
      deleteGitBranch: options?.deleteGitBranch || false,
    });
    const response = await client.archiveWorktree(request);
    return {
      message: response.message,
      deleted_directory: response.deletedDirectory,
      deleted_branch: response.deletedBranch,
    };
  },

  // Unarchive a worktree
  async unarchive(worktreeId: string): Promise<{ message: string }> {
    const client = grpcClient.worktree();
    const request = create(UnarchiveWorktreeRequestSchema, { worktreeId });
    const response = await client.unarchiveWorktree(request);
    return { message: response.message };
  },

  // =============================================================================
  // Import/Discovery Operations
  // =============================================================================

  // Import an existing git worktree
  async import(
    projectId: string,
    path: string,
    options?: { name?: string; chatId?: string }
  ): Promise<Worktree> {
    const client = grpcClient.worktree();
    const request = create(ImportWorktreeRequestSchema, {
      projectId,
      path,
      name: options?.name,
      chatId: options?.chatId,
    });
    const response = await client.importWorktree(request);
    if (!response.worktree) throw new Error("No worktree in response");
    return protoToFrontend(response.worktree);
  },

  // Discover existing git worktrees
  async discover(projectId: string): Promise<{ discovered: DiscoveredWorktree[]; total: number }> {
    const client = grpcClient.worktree();
    const request = create(DiscoverWorktreesRequestSchema, { projectId });
    const response = await client.discoverWorktrees(request);
    return {
      discovered: response.discovered.map(protoDiscoveredToFrontend),
      total: response.total,
    };
  },

  // Recreate an archived worktree from its branch
  async recreate(worktreeId: string): Promise<{ message: string; path: string; branch: string }> {
    const client = grpcClient.worktree();
    const request = create(RecreateWorktreeRequestSchema, { worktreeId });
    const response = await client.recreateWorktree(request);
    return {
      message: response.message,
      path: response.path,
      branch: response.branch,
    };
  },

  // =============================================================================
  // Git Read Operations
  // =============================================================================

  // Get file changes for a worktree
  async getChanges(worktreeId: string): Promise<WorktreeChanges> {
    const client = grpcClient.worktree();
    const request = create(GetWorktreeChangesRequestSchema, { worktreeId });
    const response = await client.getWorktreeChanges(request);
    return {
      files: response.files.map(protoFileChangeToFrontend),
      total_files: response.totalFiles,
      branch: response.branch,
      ahead: response.ahead,
      behind: response.behind,
      default_branch: response.defaultBranch,
    };
  },

  // Get git status for a worktree
  async getGitStatus(worktreeId: string): Promise<WorktreeGitStatus> {
    const client = grpcClient.worktree();
    const request = create(GetWorktreeGitStatusRequestSchema, { worktreeId });
    const response = await client.getWorktreeGitStatus(request);
    return {
      is_clean: response.clean,
      current_branch: response.branch,
      ahead: response.ahead,
      behind: response.behind,
      staged_files: response.stagedFiles,
      modified_files: response.modifiedFiles,
      untracked_files: response.untrackedFiles,
      has_remote: false, // Not available in proto response
    };
  },

  // Get commit history for a worktree
  async getCommits(worktreeId: string, limit?: number): Promise<WorktreeCommits> {
    const client = grpcClient.worktree();
    const request = create(GetWorktreeCommitsRequestSchema, {
      worktreeId,
      limit: limit || 20,
    });
    const response = await client.getWorktreeCommits(request);
    return {
      commits: response.commits.map(protoCommitToFrontend),
      total: response.total,
      branch: response.branch,
      base_branch: response.baseBranch,
      comparison_mode: response.comparisonMode,
      comparison_ref: response.comparisonRef,
      current_branch: response.currentBranch,
    };
  },

  // =============================================================================
  // Git Write Operations
  // =============================================================================

  // Stage files in a worktree
  async stageFiles(worktreeId: string, files: string[]): Promise<{ message: string; files: string[] }> {
    const client = grpcClient.worktree();
    const request = create(StageFilesRequestSchema, {
      worktreeId,
      files,
    });
    const response = await client.stageFiles(request);
    return {
      message: response.message,
      files: response.files,
    };
  },

  // Unstage files in a worktree
  async unstageFiles(worktreeId: string, files: string[]): Promise<{ message: string; files: string[] }> {
    const client = grpcClient.worktree();
    const request = create(UnstageFilesRequestSchema, {
      worktreeId,
      files,
    });
    const response = await client.unstageFiles(request);
    return {
      message: response.message,
      files: response.files,
    };
  },

  // Revert/discard file changes in a worktree
  // For staged files: unstages and discards changes
  // For modified files: discards working tree changes
  // For untracked files: deletes the file
  async revertFiles(worktreeId: string, files: string[]): Promise<{ message: string; files: string[] }> {
    const client = grpcClient.worktree();
    const request = create(RevertFilesRequestSchema, {
      worktreeId,
      files,
    });
    const response = await client.revertFiles(request);
    return {
      message: response.message,
      files: response.files,
    };
  },

  // Commit staged changes in a worktree
  async commit(worktreeId: string, message: string): Promise<{ message: string; output: string }> {
    const client = grpcClient.worktree();
    const request = create(CommitWorktreeRequestSchema, {
      worktreeId,
      message,
    });
    const response = await client.commitWorktree(request);
    return {
      message: response.message,
      output: response.output,
    };
  },

  // Push changes from a worktree to remote
  async push(worktreeId: string): Promise<{ message: string; output: string }> {
    const client = grpcClient.worktree();
    const request = create(PushWorktreeRequestSchema, { worktreeId });
    const response = await client.pushWorktree(request);
    return {
      message: response.message,
      output: response.output,
    };
  },

  // Pull changes from remote to a worktree
  async pull(worktreeId: string): Promise<{ message: string; output: string }> {
    const client = grpcClient.worktree();
    const request = create(PullWorktreeRequestSchema, { worktreeId });
    const response = await client.pullWorktree(request);
    return {
      message: response.message,
      output: response.output,
    };
  },

  // Get PR info for a worktree's branch
  async getPR(worktreeId: string): Promise<PRInfo> {
    const client = grpcClient.worktree();
    const request = create(GetWorktreePRRequestSchema, { worktreeId });
    const response = await client.getWorktreePR(request);
    return {
      exists: response.exists,
      url: response.url || undefined,
      number: response.number || undefined,
      title: response.title || undefined,
      state: response.state || undefined,
    };
  },

  // Create a PR for a worktree
  async createPR(
    worktreeId: string,
    title: string,
    body?: string
  ): Promise<{ message: string; pr_url: string; output: string; auto_committed: boolean; auto_pushed: boolean }> {
    const client = grpcClient.worktree();
    const request = create(CreateWorktreePRRequestSchema, {
      worktreeId,
      title,
      body,
    });
    const response = await client.createWorktreePR(request);
    return {
      message: response.message,
      pr_url: response.prUrl,
      output: response.output,
      auto_committed: response.autoCommitted,
      auto_pushed: response.autoPushed,
    };
  },
};