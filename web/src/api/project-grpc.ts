// Copyright (c) 2025 Reliant Labs

import { grpcClient } from "./grpc-client";
import { singleflight } from "../lib/singleflight";
import { create } from "@bufbuild/protobuf";
import type { 
  Project as ProtoProject,
  GitBranch as ProtoGitBranch,
  FileChange as ProtoFileChange,
  Prompt as ProtoPrompt,
} from "../gen/reliant/v1/project_pb";
import { FileChangeStatus } from "../gen/reliant/v1/common_pb";
import {
  CreateProjectRequestSchema,
  ListProjectsRequestSchema,
  GetProjectRequestSchema,
  UpdateProjectRequestSchema,
  DeleteProjectRequestSchema,
  TouchProjectRequestSchema,
  GetProjectMetadataRequestSchema,
  UpdateProjectMetadataRequestSchema,
  GetProjectGitInfoRequestSchema,
  GetProjectGitBranchesRequestSchema,
  GetProjectInitStatusRequestSchema,
  InitializeProjectRequestSchema,
  GetProjectChangesRequestSchema,
  GetProjectPromptsRequestSchema,
  SaveProjectPromptsRequestSchema,
  InitializeGitRepoRequestSchema,
  PromptSchema,
} from "../gen/reliant/v1/project_pb";

// Type definitions matching frontend expectations (snake_case to match store interface)
export interface Project {
  id: string;
  name: string;
  path: string;
  description?: string;
  is_git_repo: boolean;
  default_branch?: string;
  worktree_count: number;
  last_active: string;
  created_at: string;
  updated_at: string;
}

export interface GitBranch {
  name: string;
  is_current: boolean;
  is_remote: boolean;
  upstream: string;
  last_commit_age: number;
}

export interface GitInfo {
  project_id: string;
  is_git_repo: boolean;
  current_branch: string;
  has_changes: boolean;
  status: string;
  staged_files: string[];
  unstaged_files: string[];
  untracked_files: string[];
  ahead: number;
  behind: number;
  remote_url: string;
  message: string;
}

export interface ProjectMetadata {
  project_id: string;
  name: string;
  path: string;
  description?: string;
  is_git_repo: boolean;
  default_branch?: string;
  created_at: string;
  updated_at: string;
  last_active: string;
}

export interface FileChange {
  path: string;
  status: FileChangeStatus;
  diff: string;
  content: string;
  original_content: string;
  is_new: boolean;
}

export interface ProjectChanges {
  branch: string;
  files: FileChange[];
  total_files: number;
}

export interface Prompt {
  id: string;
  name: string;
  content: string;
  description: string;
  created_at: string;
  updated_at: string;
}

export interface ProjectInitStatus {
  initialized: boolean;
  project_id: string;
  message: string;
}

// Convert proto Project to frontend Project
function protoToFrontend(proto: ProtoProject): Project {
  return {
    id: proto.id,
    name: proto.name,
    path: proto.path,
    description: proto.description || undefined,
    is_git_repo: proto.isGitRepo,
    default_branch: proto.defaultBranch || undefined,
    worktree_count: 0, // Not included in proto, will be fetched separately if needed
    last_active: proto.lastActive,
    created_at: proto.createdAt,
    updated_at: proto.updatedAt,
  };
}

// Convert proto GitBranch to frontend GitBranch
function protoBranchToFrontend(proto: ProtoGitBranch): GitBranch {
  return {
    name: proto.name,
    is_current: proto.isCurrent,
    is_remote: proto.isRemote,
    upstream: proto.upstream,
    last_commit_age: Number(proto.lastCommitAge),
  };
}

// Convert proto FileChange to frontend FileChange
function protoFileChangeToFrontend(proto: ProtoFileChange): FileChange {
  return {
    path: proto.path,
    status: proto.status,
    diff: proto.diff,
    content: proto.content,
    original_content: proto.originalContent,
    is_new: proto.isNew,
  };
}

// Convert proto Prompt to frontend Prompt
function protoPromptToFrontend(proto: ProtoPrompt): Prompt {
  return {
    id: proto.id,
    name: proto.name,
    content: proto.content,
    description: proto.description,
    created_at: proto.createdAt,
    updated_at: proto.updatedAt,
  };
}

// Convert frontend Prompt to proto Prompt (for saving)
function frontendPromptToProto(prompt: Prompt): ProtoPrompt {
  return create(PromptSchema, {
    id: prompt.id,
    name: prompt.name,
    content: prompt.content,
    description: prompt.description,
    createdAt: prompt.created_at,
    updatedAt: prompt.updated_at,
  });
}

export const projectGrpc = {
  // Create a new project
  async create(name: string, path: string, description?: string, defaultBranch?: string): Promise<Project> {
    const client = grpcClient.project();
    const request = create(CreateProjectRequestSchema, {
      name,
      path,
      description,
      defaultBranch,
    });
    const response = await client.createProject(request);
    if (!response.project) throw new Error("No project in response");
    return protoToFrontend(response.project);
  },

  // List all projects for the current user
  async list(limit?: number, offset?: number): Promise<{ projects: Project[]; total: number }> {
    const client = grpcClient.project();
    const request = create(ListProjectsRequestSchema, {
      limit: limit || 100,
      offset: offset || 0,
    });
    const response = await client.listProjects(request);
    return {
      projects: response.projects.map(protoToFrontend),
      total: response.total,
    };
  },

  // Get a project by ID
  async get(projectId: string): Promise<Project> {
    const client = grpcClient.project();
    const request = create(GetProjectRequestSchema, { projectId });
    const response = await client.getProject(request);
    if (!response.project) throw new Error("No project in response");
    return protoToFrontend(response.project);
  },

  // Update a project
  async update(projectId: string, updates: { name?: string; description?: string; defaultBranch?: string }): Promise<Project> {
    const client = grpcClient.project();
    const request = create(UpdateProjectRequestSchema, {
      projectId,
      name: updates.name,
      description: updates.description,
      defaultBranch: updates.defaultBranch,
    });
    const response = await client.updateProject(request);
    if (!response.project) throw new Error("No project in response");
    return protoToFrontend(response.project);
  },

  // Delete a project
  async delete(projectId: string): Promise<{ success: boolean; message: string }> {
    const client = grpcClient.project();
    const request = create(DeleteProjectRequestSchema, { projectId });
    const response = await client.deleteProject(request);
    return {
      success: response.success,
      message: response.message,
    };
  },

  // Touch project (update last_active timestamp)
  async touch(projectId: string): Promise<{ success: boolean; message: string }> {
    const client = grpcClient.project();
    const request = create(TouchProjectRequestSchema, { projectId });
    const response = await client.touchProject(request);
    return {
      success: response.success,
      message: response.message,
    };
  },

  // Get project metadata
  async getMetadata(projectId: string): Promise<ProjectMetadata> {
    const client = grpcClient.project();
    const request = create(GetProjectMetadataRequestSchema, { projectId });
    const response = await client.getProjectMetadata(request);
    return {
      project_id: response.projectId,
      name: response.name,
      path: response.path,
      description: response.description || undefined,
      is_git_repo: response.isGitRepo,
      default_branch: response.defaultBranch || undefined,
      created_at: response.createdAt,
      updated_at: response.updatedAt,
      last_active: response.lastActive,
    };
  },

  // Update project metadata
  async updateMetadata(projectId: string, description?: string): Promise<Project> {
    const client = grpcClient.project();
    const request = create(UpdateProjectMetadataRequestSchema, {
      projectId,
      description,
    });
    const response = await client.updateProjectMetadata(request);
    if (!response.project) throw new Error("No project in response");
    return protoToFrontend(response.project);
  },

  // Get git info for a project
  async getGitInfo(projectId: string): Promise<GitInfo> {
    const client = grpcClient.project();
    const request = create(GetProjectGitInfoRequestSchema, { projectId });
    const response = await client.getProjectGitInfo(request);
    return {
      project_id: response.projectId,
      is_git_repo: response.isGitRepo,
      current_branch: response.currentBranch,
      has_changes: response.hasChanges,
      status: response.status,
      staged_files: response.stagedFiles,
      unstaged_files: response.unstagedFiles,
      untracked_files: response.untrackedFiles,
      ahead: response.ahead,
      behind: response.behind,
      remote_url: response.remoteUrl,
      message: response.message,
    };
  },

  // Get git branches for a project
  async getGitBranches(projectId: string): Promise<GitBranch[]> {
    const client = grpcClient.project();
    const request = create(GetProjectGitBranchesRequestSchema, { projectId });
    const response = await client.getProjectGitBranches(request);
    return response.branches.map(protoBranchToFrontend);
  },

  // Get project initialization status
  async getInitStatus(projectId: string): Promise<ProjectInitStatus> {
    // Use singleflight to deduplicate concurrent calls for the same project
    // (StrictMode or component remounts can trigger duplicate fetches)
    return singleflight(`getInitStatus:${projectId}`, async () => {
      const client = grpcClient.project();
      const request = create(GetProjectInitStatusRequestSchema, { projectId });
      const response = await client.getProjectInitStatus(request);
      return {
        initialized: response.initialized,
        project_id: response.projectId,
        message: response.message,
      };
    });
  },

  // Initialize a project
  async initialize(projectId: string): Promise<{ project_id: string; status: string; message: string; initialized: boolean }> {
    const client = grpcClient.project();
    const request = create(InitializeProjectRequestSchema, { projectId });
    const response = await client.initializeProject(request);
    return {
      project_id: response.projectId,
      status: response.status,
      message: response.message,
      initialized: response.initialized,
    };
  },

  // Get project changes (recent git changes)
  async getChanges(projectId: string): Promise<ProjectChanges> {
    const client = grpcClient.project();
    const request = create(GetProjectChangesRequestSchema, { projectId });
    const response = await client.getProjectChanges(request);
    return {
      branch: response.branch,
      files: response.files.map(protoFileChangeToFrontend),
      total_files: response.totalFiles,
    };
  },

  // Get project prompts
  async getPrompts(projectId: string): Promise<Prompt[]> {
    const client = grpcClient.project();
    const request = create(GetProjectPromptsRequestSchema, { projectId });
    const response = await client.getProjectPrompts(request);
    return response.prompts.map(protoPromptToFrontend);
  },

  // Save project prompts
  async savePrompts(projectId: string, prompts: Prompt[]): Promise<{ message: string; prompts: Prompt[] }> {
    const client = grpcClient.project();
    const request = create(SaveProjectPromptsRequestSchema, {
      projectId,
      prompts: prompts.map(frontendPromptToProto),
    });
    const response = await client.saveProjectPrompts(request);
    return {
      message: response.message,
      prompts: response.prompts.map(protoPromptToFrontend),
    };
  },

  // Initialize a git repository
  async initializeGitRepo(
    projectId: string, 
    initialBranch?: string,
    gitignorePatterns?: string[],
    initialCommit?: boolean
  ): Promise<{ message: string; project_id: string; is_git_repo: boolean; default_branch: string }> {
    const client = grpcClient.project();
    const request = create(InitializeGitRepoRequestSchema, {
      projectId,
      initialBranch: initialBranch || "main",
      gitignorePatterns: gitignorePatterns || [],
      initialCommit: initialCommit ?? true,
    });
    const response = await client.initializeGitRepo(request);
    return {
      message: response.message,
      project_id: response.projectId,
      is_git_repo: response.isGitRepo,
      default_branch: response.defaultBranch,
    };
  },
};