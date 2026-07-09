// Copyright (c) 2025 Reliant Labs

import { grpcClient } from "./grpc-client";
import { create } from "@bufbuild/protobuf";
import { singleflight } from "../lib/singleflight";
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
  ListWorktreeRepoStatusesRequestSchema,
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
  repo_id?: string;
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

// Per-repo summary row used by the right-sidebar grouped view.
// Mirrors reliant.v1.WorktreeRepoStatus.
export interface WorktreeRepoStatus {
  repo_id: string;
  repo_name: string;
  repo_relative_path: string;
  current_branch: string;
  has_changes: boolean;
  ahead: number;
  behind: number;
  changed_files: number;
  error: string;
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
    repo_id: undefined,
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
  // baseBranches lets the caller pin a specific base branch per repo (key =
  // repo_id). Repos missing from the map fall back to baseBranch, then to
  // per-repo default-branch detection on the daemon.
  async batchCreate(
    projectId: string,
    repoIds: string[],
    name: string,
    branch: string,
    options?: {
      baseBranch?: string;
      baseBranches?: Record<string, string>;
      chatId?: string;
      copyFiles?: string[];
      force?: boolean;
    }
  ): Promise<BatchCreateResult> {
    const client = grpcClient.worktree();
    const request = create(CreateWorktreeRequestSchema, {
      projectId,
      name,
      branch,
      baseBranch: options?.baseBranch,
      baseBranches: options?.baseBranches || {},
      chatId: options?.chatId,
      copyFiles: options?.copyFiles || [],
      force: options?.force || false,
    });
    const response = await client.createWorktree(request);
    if (!response.worktree) throw new Error("No worktree in response");

    const worktree = protoToFrontend(response.worktree);
    return {
      results: repoIds.map((repoId) => ({
        repo_id: repoId,
        worktree,
      })),
      all_succeeded: true,
      rolled_back: false,
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

  // Get file changes for a worktree.
  // repoId scopes to a nested repo. Empty/undefined uses legacy single-repo
  // behavior (server returns InvalidArgument if the project has 2+ repos).
  async getChanges(worktreeId: string, repoId?: string): Promise<WorktreeChanges> {
    // Singleflight per worktree+repo: this RPC is issued by several mounted
    // consumers (useWorktreeChanges instances, RecentChanges) plus a 30s
    // fallback poll, with NO await between ticks — when the daemon answers
    // slowly (large diffs), identical calls stacked up, each pinning one of
    // the renderer's 6 HTTP/1.1 connections in dev until the whole app
    // starved. Concurrent callers now share one in-flight request.
    return singleflight(`worktree:getChanges:${worktreeId}:${repoId ?? ""}`, async () => {
      const client = grpcClient.worktree();
      const request = create(GetWorktreeChangesRequestSchema, {
        worktreeId,
        repoId: repoId ?? "",
      });
      const response = await client.getWorktreeChanges(request);
      return {
        files: response.files.map(protoFileChangeToFrontend),
        total_files: response.totalFiles,
        branch: response.branch,
        ahead: response.ahead,
        behind: response.behind,
        default_branch: response.defaultBranch,
      };
    });
  },

  // Get git status for a worktree.
  // repoId scopes to a nested repo (see getChanges for legacy semantics).
  async getGitStatus(worktreeId: string, repoId?: string): Promise<WorktreeGitStatus> {
    const client = grpcClient.worktree();
    const request = create(GetWorktreeGitStatusRequestSchema, {
      worktreeId,
      repoId: repoId ?? "",
    });
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

  // Get commit history for a worktree.
  // repoId scopes to a nested repo (see getChanges for legacy semantics).
  async getCommits(worktreeId: string, limit?: number, repoId?: string): Promise<WorktreeCommits> {
    const client = grpcClient.worktree();
    const request = create(GetWorktreeCommitsRequestSchema, {
      worktreeId,
      limit: limit || 20,
      repoId: repoId ?? "",
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

  // List per-repo git status rows for every nested repo in the worktree's
  // project. Drives the multi-repo right-sidebar. Single-repo / zero-repo
  // projects collapse to a one- or zero-element response.
  async listRepoStatuses(worktreeId: string): Promise<WorktreeRepoStatus[]> {
    const client = grpcClient.worktree();
    const request = create(ListWorktreeRepoStatusesRequestSchema, { worktreeId });
    const response = await client.listWorktreeRepoStatuses(request);
    return response.statuses.map((s) => ({
      repo_id: s.repoId,
      repo_name: s.repoName,
      repo_relative_path: s.repoRelativePath,
      current_branch: s.currentBranch,
      has_changes: s.hasChanges,
      ahead: s.ahead,
      behind: s.behind,
      changed_files: s.changedFiles,
      error: s.error,
    }));
  },

  // =============================================================================
  // Git Write Operations
  // =============================================================================

  // Stage files in a worktree.
  // repoId scopes to a nested repo; file paths must be relative to that repo.
  async stageFiles(worktreeId: string, files: string[], repoId?: string): Promise<{ message: string; files: string[] }> {
    const client = grpcClient.worktree();
    const request = create(StageFilesRequestSchema, {
      worktreeId,
      files,
      repoId: repoId ?? "",
    });
    const response = await client.stageFiles(request);
    return {
      message: response.message,
      files: response.files,
    };
  },

  // Unstage files in a worktree.
  // repoId scopes to a nested repo; file paths must be relative to that repo.
  async unstageFiles(worktreeId: string, files: string[], repoId?: string): Promise<{ message: string; files: string[] }> {
    const client = grpcClient.worktree();
    const request = create(UnstageFilesRequestSchema, {
      worktreeId,
      files,
      repoId: repoId ?? "",
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
  // repoId scopes to a nested repo; file paths must be relative to that repo.
  async revertFiles(worktreeId: string, files: string[], repoId?: string): Promise<{ message: string; files: string[] }> {
    const client = grpcClient.worktree();
    const request = create(RevertFilesRequestSchema, {
      worktreeId,
      files,
      repoId: repoId ?? "",
    });
    const response = await client.revertFiles(request);
    return {
      message: response.message,
      files: response.files,
    };
  },

  // Commit staged changes in a worktree.
  // repoId scopes to a nested repo. Commits are intrinsically per-repo.
  async commit(worktreeId: string, message: string, repoId?: string): Promise<{ message: string; output: string }> {
    const client = grpcClient.worktree();
    const request = create(CommitWorktreeRequestSchema, {
      worktreeId,
      message,
      repoId: repoId ?? "",
    });
    const response = await client.commitWorktree(request);
    return {
      message: response.message,
      output: response.output,
    };
  },

  // Push changes from a worktree to remote.
  // repoId scopes to a nested repo (each repo has its own remote).
  async push(worktreeId: string, repoId?: string): Promise<{ message: string; output: string }> {
    const client = grpcClient.worktree();
    const request = create(PushWorktreeRequestSchema, {
      worktreeId,
      repoId: repoId ?? "",
    });
    const response = await client.pushWorktree(request);
    return {
      message: response.message,
      output: response.output,
    };
  },

  // Pull changes from remote to a worktree.
  // repoId scopes to a nested repo (each repo has its own remote).
  async pull(worktreeId: string, repoId?: string): Promise<{ message: string; output: string }> {
    const client = grpcClient.worktree();
    const request = create(PullWorktreeRequestSchema, {
      worktreeId,
      repoId: repoId ?? "",
    });
    const response = await client.pullWorktree(request);
    return {
      message: response.message,
      output: response.output,
    };
  },

  // Get PR info for a worktree's branch.
  // repoId scopes to a nested repo (PRs are intrinsically per-repo).
  async getPR(worktreeId: string, repoId?: string): Promise<PRInfo> {
    const client = grpcClient.worktree();
    const request = create(GetWorktreePRRequestSchema, {
      worktreeId,
      repoId: repoId ?? "",
    });
    const response = await client.getWorktreePR(request);
    return {
      exists: response.exists,
      url: response.url || undefined,
      number: response.number || undefined,
      title: response.title || undefined,
      state: response.state || undefined,
    };
  },

  // Create a PR for a worktree.
  // repoId scopes to a nested repo (PRs are intrinsically per-repo).
  async createPR(
    worktreeId: string,
    title: string,
    body?: string,
    repoId?: string
  ): Promise<{ message: string; pr_url: string; output: string; auto_committed: boolean; auto_pushed: boolean }> {
    const client = grpcClient.worktree();
    const request = create(CreateWorktreePRRequestSchema, {
      worktreeId,
      title,
      body,
      repoId: repoId ?? "",
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