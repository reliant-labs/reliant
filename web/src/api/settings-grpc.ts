// Copyright (c) 2025 Reliant Labs

import { grpcClient } from "./grpc-client";
import { ConnectError, Code } from "@connectrpc/connect";
import { create } from "@bufbuild/protobuf";
import { ConfigScope } from "../gen/reliant/v1/common_pb";
import {
  ConfigSeverity,
  HiddenItemType,
  SkillScope as ProtoSkillScope,
  SkillFormat as ProtoSkillFormat,
  SkillConflictPolicy as ProtoSkillConflictPolicy,
} from "../gen/reliant/v1/settings_pb";
import type {
  Setting as ProtoSetting,
  UserPrompt as ProtoUserPrompt,
  ProviderStatus as ProtoProviderStatus,
  InstalledSkill as ProtoInstalledSkill,
  RecommendedSkill as ProtoRecommendedSkill,
  SkillDiscoveryDiagnostic as ProtoSkillDiscoveryDiagnostic,
  SkillInstallResult as ProtoSkillInstallResult,
} from "../gen/reliant/v1/settings_pb";
import {
  // Sub-phase 6a: Core Settings CRUD
  CreateSettingRequestSchema,
  ListSettingsRequestSchema,
  GetSettingRequestSchema,
  UpdateSettingRequestSchema,
  DeleteSettingRequestSchema,
  // Sub-phase 6b: UI Settings
  GetShortcutsRequestSchema,
  UpdateShortcutsRequestSchema,
  GetPreferencesRequestSchema,
  UpdatePreferencesRequestSchema,
  SetHiddenItemRequestSchema,
  // Sub-phase 6c: Prompts
  GetPromptsRequestSchema,
  SavePromptsRequestSchema,
  // Sub-phase 6d: Provider Settings
  GetProviderStatusesRequestSchema,
  UpdateProviderAPIKeyRequestSchema,
  ValidateProviderAPIKeyRequestSchema,
  CompleteCodexOAuthRequestSchema,
  CompleteClaudeOAuthRequestSchema,
  // Sub-phase 6e: Privacy
  GetPrivacySettingsRequestSchema,
  UpdatePrivacySettingsRequestSchema,
  TrackPageVisitedRequestSchema,
  InstallSkillRequestSchema,
  ListInstalledSkillsRequestSchema,
  ListRecommendedSkillsRequestSchema,
  GetInstalledSkillDefinitionRequestSchema,
  SetSkillEnabledRequestSchema,
  DeleteGlobalSkillRequestSchema,
  UserPromptSchema,
  // Sub-phase 6g: Configuration Health
  GetConfigHealthRequestSchema,
  type ConfigError as ProtoConfigError,
} from "../gen/reliant/v1/settings_pb";

// ============================================
// Frontend Type Definitions
// ============================================

export interface Setting {
  id: string;
  key: string;
  value: string;
  value_type: string;
  project_id?: string;
  created_at: string;
  updated_at: string;
}

export interface UserPrompt {
  id: string;
  title: string;
  content: string;
  default?: boolean;
  hotkey?: string;
  category?: string;
}

export interface ProviderStatus {
  provider: string;
  configured: boolean;
  has_api_key: boolean;
  masked_key?: string;
  display_name: string;
}

export interface Preferences {
  streaming_enabled: boolean;
  worktree_archive_mode: string;
  worktree_default_delete_directory: boolean;
  worktree_default_delete_branch: boolean;
  additional: Record<string, string>;
  branch_copy_uncommitted_files_default: boolean;
  default_mcp_scope: ConfigScope;
  default_workflow_scope: ConfigScope;
  default_workflow: string;
  hide_builtin_workflows: boolean;
  hide_builtin_presets: boolean;
  hidden_workflow_slugs: string[];
  hidden_preset_slugs: string[];
}

export interface PrivacySettings {
  analytics_enabled: boolean;
  crash_reporting_enabled: boolean;
}

export interface ConfigError {
  type: string; // preset, workflow, mcp, config
  source: string;
  message: string;
  severity: ConfigSeverity;
  details: Record<string, string>;
}

export interface ConfigHealth {
  errors: ConfigError[];
  error_count: number;
  warning_count: number;
}

export type SkillScope = "project_local" | "project" | "global" | "builtin";

export type SkillConflictPolicy = "skip" | "overwrite" | "rename";

export type SkillFormat = "claude_markdown";

export interface InstallSkillRequest {
  project_id: string;
  source: string;
  source_subpath?: string;
  ref?: string;
  name?: string;
  scope?: SkillScope;
  conflict_policy?: SkillConflictPolicy;
  dry_run?: boolean;
}

export interface SkillInstallDetails {
  source: string;
  source_type: "local" | "git" | "unknown";
  source_subpath?: string;
  git_ref?: string;
  resolved_source: string;
  target_dir: string;
  skill_name: string;
  install_dir_name: string;
  installed_files: string[];
  skipped_files: string[];
  dry_run: boolean;
  scope: SkillScope;
  conflict_policy: SkillConflictPolicy;
}

export interface InstallSkillResult {
  success: boolean;
  message: string;
  result?: SkillInstallDetails;
}

export interface InstalledSkill {
  skill_id: string;
  name: string;
  description: string;
  scope: SkillScope;
  format: SkillFormat | "unknown";
  skill_dir: string;
  definition_path: string;
  active: boolean;
  shadowed_by_definition_path?: string;
}

export interface SkillAsset {
  path: string;
  mime_type: string;
  content: Uint8Array;
}

export interface SkillDiscoveryDiagnostic {
  path: string;
  scope: SkillScope | "unknown";
  message: string;
}

export interface RecommendedSkill {
  id: string;
  name: string;
  description: string;
  source: string;
  source_subpath?: string;
  ref?: string;
  bundled_by?: string;
}

export interface SetSkillEnabledResult {
  success: boolean;
  message: string;
  skill_id: string;
  enabled: boolean;
}

export interface DeleteGlobalSkillResult {
  success: boolean;
  message: string;
}

// ============================================
// Conversion Functions
// ============================================

function protoSettingToFrontend(proto: ProtoSetting): Setting {
  return {
    id: proto.id,
    key: proto.key,
    value: proto.value,
    value_type: proto.valueType,
    project_id: proto.projectId || undefined,
    created_at: proto.createdAt,
    updated_at: proto.updatedAt,
  };
}

function protoUserPromptToFrontend(proto: ProtoUserPrompt): UserPrompt {
  return {
    id: proto.id,
    title: proto.title,
    content: proto.content,
    default: proto.default || undefined,
    hotkey: proto.hotkey || undefined,
    category: proto.category || undefined,
  };
}

function protoProviderStatusToFrontend(
  proto: ProtoProviderStatus
): ProviderStatus {
  return {
    provider: proto.provider,
    configured: proto.configured,
    has_api_key: proto.hasApiKey,
    masked_key: proto.maskedKey || undefined,
    display_name: proto.displayName,
  };
}

function protoConfigErrorToFrontend(proto: ProtoConfigError): ConfigError {
  return {
    type: proto.type,
    source: proto.source,
    message: proto.message,
    severity: proto.severity,
    details: proto.details,
  };
}

function protoScopeToFrontend(scope: ProtoSkillScope): SkillScope | "unknown" {
  switch (scope) {
    case ProtoSkillScope.PROJECT_LOCAL:
      return "project_local";
    case ProtoSkillScope.PROJECT:
      return "project";
    case ProtoSkillScope.GLOBAL:
      return "global";
    case ProtoSkillScope.BUILTIN:
      return "builtin";
    default:
      return "unknown";
  }
}

function scopeToProto(scope?: SkillScope): ProtoSkillScope {
  switch (scope) {
    case "project_local":
      return ProtoSkillScope.PROJECT_LOCAL;
    case "global":
      return ProtoSkillScope.GLOBAL;
    case "project":
      return ProtoSkillScope.PROJECT;
    default:
      return ProtoSkillScope.UNSPECIFIED;
  }
}

function conflictPolicyToProto(policy?: SkillConflictPolicy): ProtoSkillConflictPolicy {
  switch (policy) {
    case "skip":
      return ProtoSkillConflictPolicy.SKIP;
    case "overwrite":
      return ProtoSkillConflictPolicy.OVERWRITE;
    case "rename":
      return ProtoSkillConflictPolicy.RENAME;
    default:
      return ProtoSkillConflictPolicy.UNSPECIFIED;
  }
}

function protoConflictPolicyToFrontend(
  policy: ProtoSkillConflictPolicy
): SkillConflictPolicy {
  switch (policy) {
    case ProtoSkillConflictPolicy.OVERWRITE:
      return "overwrite";
    case ProtoSkillConflictPolicy.RENAME:
      return "rename";
    case ProtoSkillConflictPolicy.SKIP:
    default:
      return "skip";
  }
}

function protoFormatToFrontend(format: ProtoSkillFormat): SkillFormat | "unknown" {
  switch (format) {
    case ProtoSkillFormat.CLAUDE_MARKDOWN:
      return "claude_markdown";
    default:
      return "unknown";
  }
}

function protoSkillInstallResultToFrontend(
  proto: ProtoSkillInstallResult
): SkillInstallDetails {
  const source_type =
    proto.sourceType === 2
      ? "git"
      : proto.sourceType === 1
        ? "local"
        : "unknown";
  const scopeValue = protoScopeToFrontend(proto.scope);

  return {
    source: proto.source,
    source_type,
    source_subpath: proto.sourceSubpath || undefined,
    git_ref: proto.gitRef || undefined,
    resolved_source: proto.resolvedSource,
    target_dir: proto.targetDir,
    skill_name: proto.skillName,
    install_dir_name: proto.installDirName,
    installed_files: [...proto.installedFiles],
    skipped_files: [...proto.skippedFiles],
    dry_run: proto.dryRun,
    scope: scopeValue === "unknown" ? "project" : scopeValue,
    conflict_policy: protoConflictPolicyToFrontend(proto.conflictPolicy),
  };
}

function installedSkillScope(proto: ProtoInstalledSkill): SkillScope {
  const scopeValue = protoScopeToFrontend(proto.scope);
  return scopeValue === "unknown" ? "project" : scopeValue;
}

function protoInstalledSkillToFrontend(
  proto: ProtoInstalledSkill
): InstalledSkill {
  return {
    skill_id: proto.skillId,
    name: proto.name,
    description: proto.description,
    scope: installedSkillScope(proto),
    format: protoFormatToFrontend(proto.format),
    skill_dir: proto.skillDir,
    definition_path: proto.definitionPath,
    active: proto.active,
    shadowed_by_definition_path: proto.shadowedByDefinitionPath || undefined,
  };
}

function protoRecommendedSkillToFrontend(
  proto: ProtoRecommendedSkill
): RecommendedSkill {
  return {
    id: proto.id,
    name: proto.name,
    description: proto.description,
    source: proto.source,
    source_subpath: proto.sourceSubpath || undefined,
    ref: proto.ref || undefined,
    bundled_by: proto.bundledBy || undefined,
  };
}

function protoSkillDiscoveryDiagnosticToFrontend(
  proto: ProtoSkillDiscoveryDiagnostic
): SkillDiscoveryDiagnostic {
  return {
    path: proto.path,
    scope: protoScopeToFrontend(proto.scope),
    message: proto.message,
  };
}

function isSkillsDisabledError(error: unknown): boolean {
  if (!(error instanceof ConnectError)) {
    return false;
  }
  if (error.code !== Code.FailedPrecondition) {
    return false;
  }
  return error.message.toLowerCase().includes("skills feature is disabled");
}

// ============================================
// Settings gRPC Client
// ============================================

export const settingsGrpc = {
  // ============================================
  // Sub-phase 6a: Core Settings CRUD
  // ============================================

  async createSetting(
    key: string,
    value: string,
    valueType?: string,
    projectId?: string
  ): Promise<Setting> {
    const client = grpcClient.settings();
    const request = create(CreateSettingRequestSchema, {
      key,
      value,
      valueType,
      projectId,
    });
    const response = await client.createSetting(request);
    if (!response.setting) throw new Error("No setting in response");
    return protoSettingToFrontend(response.setting);
  },

  async listSettings(projectId?: string): Promise<{
    settings: Setting[];
    total: number;
  }> {
    const client = grpcClient.settings();
    const request = create(ListSettingsRequestSchema, { projectId });
    const response = await client.listSettings(request);
    return {
      settings: response.settings.map(protoSettingToFrontend),
      total: response.total,
    };
  },

  async getSetting(key: string, projectId?: string): Promise<Setting | null> {
    const client = grpcClient.settings();
    const request = create(GetSettingRequestSchema, { key, projectId });
    const response = await client.getSetting(request);
    return response.setting ? protoSettingToFrontend(response.setting) : null;
  },

  async updateSetting(
    key: string,
    value?: string,
    valueType?: string,
    projectId?: string
  ): Promise<Setting> {
    const client = grpcClient.settings();
    const request = create(UpdateSettingRequestSchema, {
      key,
      value,
      valueType,
      projectId,
    });
    const response = await client.updateSetting(request);
    if (!response.setting) throw new Error("No setting in response");
    return protoSettingToFrontend(response.setting);
  },

  async deleteSetting(
    key: string,
    projectId?: string
  ): Promise<{
    success: boolean;
    message: string;
  }> {
    const client = grpcClient.settings();
    const request = create(DeleteSettingRequestSchema, { key, projectId });
    const response = await client.deleteSetting(request);
    return {
      success: response.success,
      message: response.message,
    };
  },

  // ============================================
  // Sub-phase 6b: UI Settings
  // ============================================

  async getShortcuts(): Promise<string> {
    const client = grpcClient.settings();
    const request = create(GetShortcutsRequestSchema, {});
    const response = await client.getShortcuts(request);
    return response.shortcuts;
  },

  async updateShortcuts(shortcuts: string): Promise<{
    success: boolean;
    message: string;
  }> {
    const client = grpcClient.settings();
    const request = create(UpdateShortcutsRequestSchema, { shortcuts });
    const response = await client.updateShortcuts(request);
    return {
      success: response.success,
      message: response.message,
    };
  },

  // In-flight dedup: concurrent calls share a single RPC round-trip.
  _getPreferencesInflight: null as Promise<Preferences> | null,

  async getPreferences(): Promise<Preferences> {
    if (this._getPreferencesInflight) {
      return this._getPreferencesInflight;
    }

    this._getPreferencesInflight = (async () => {
      try {
        const client = grpcClient.settings();
        const request = create(GetPreferencesRequestSchema, {});
        const response = await client.getPreferences(request);
        return {
          streaming_enabled: response.streamingEnabled,
          worktree_archive_mode: response.worktreeArchiveMode,
          worktree_default_delete_directory:
            response.worktreeDefaultDeleteDirectory,
          worktree_default_delete_branch: response.worktreeDefaultDeleteBranch,
          additional: response.additional,
          branch_copy_uncommitted_files_default:
            response.branchCopyUncommittedFilesDefault,
          default_mcp_scope: response.defaultMcpScope ?? ConfigScope.PROJECT,
          default_workflow_scope:
            response.defaultWorkflowScope ?? ConfigScope.PROJECT,
          default_workflow: response.defaultWorkflow || "builtin://agent",
          hide_builtin_workflows: response.hideBuiltinWorkflows ?? false,
          hide_builtin_presets: response.hideBuiltinPresets ?? false,
          hidden_workflow_slugs: response.hiddenWorkflowSlugs ?? [],
          hidden_preset_slugs: response.hiddenPresetSlugs ?? [],
        };
      } finally {
        this._getPreferencesInflight = null;
      }
    })();

    return this._getPreferencesInflight;
  },

  async updatePreferences(prefs: Partial<Preferences>): Promise<{
    success: boolean;
    message: string;
  }> {
    const client = grpcClient.settings();
    const request = create(UpdatePreferencesRequestSchema, {
      streamingEnabled: prefs.streaming_enabled,
      worktreeArchiveMode: prefs.worktree_archive_mode,
      worktreeDefaultDeleteDirectory: prefs.worktree_default_delete_directory,
      worktreeDefaultDeleteBranch: prefs.worktree_default_delete_branch,
      additional: prefs.additional || {},
      branchCopyUncommittedFilesDefault:
        prefs.branch_copy_uncommitted_files_default,
      defaultMcpScope: prefs.default_mcp_scope,
      defaultWorkflowScope: prefs.default_workflow_scope,
      defaultWorkflow: prefs.default_workflow,
      hideBuiltinWorkflows: prefs.hide_builtin_workflows,
      hideBuiltinPresets: prefs.hide_builtin_presets,
    });
    const response = await client.updatePreferences(request);
    return {
      success: response.success,
      message: response.message,
    };
  },

  async setHiddenItem(itemType: HiddenItemType, slug: string, hidden: boolean): Promise<{
    success: boolean;
    message: string;
  }> {
    const client = grpcClient.settings();
    const request = create(SetHiddenItemRequestSchema, {
      itemType: itemType,
      slug,
      hidden,
    });
    const response = await client.setHiddenItem(request);
    return {
      success: response.success,
      message: response.message,
    };
  },

  // ============================================
  // Sub-phase 6c: Prompts & Active Settings
  // ============================================

  async getPrompts(): Promise<UserPrompt[]> {
    const client = grpcClient.settings();
    const request = create(GetPromptsRequestSchema, {});
    const response = await client.getPrompts(request);
    return response.prompts.map(protoUserPromptToFrontend);
  },

  async savePrompts(prompts: UserPrompt[]): Promise<{
    success: boolean;
    message: string;
    prompts: UserPrompt[];
  }> {
    const client = grpcClient.settings();
    const protoPrompts = prompts.map((p) =>
      create(UserPromptSchema, {
        id: p.id,
        title: p.title,
        content: p.content,
        default: p.default,
        hotkey: p.hotkey,
        category: p.category,
      })
    );
    const request = create(SavePromptsRequestSchema, { prompts: protoPrompts });
    const response = await client.savePrompts(request);
    return {
      success: response.success,
      message: response.message,
      prompts: response.prompts.map(protoUserPromptToFrontend),
    };
  },

  // ============================================
  // Sub-phase 6d: Provider Settings
  // ============================================

  async getProviderStatuses(): Promise<ProviderStatus[]> {
    const client = grpcClient.settings();
    const request = create(GetProviderStatusesRequestSchema, {});
    const response = await client.getProviderStatuses(request);
    return response.providers.map(protoProviderStatusToFrontend);
  },

  async updateProviderAPIKey(
    provider: string,
    apiKey: string
  ): Promise<{
    success: boolean;
    message: string;
  }> {
    const client = grpcClient.settings();
    const request = create(UpdateProviderAPIKeyRequestSchema, {
      provider,
      apiKey,
    });
    const response = await client.updateProviderAPIKey(request);
    return {
      success: response.success,
      message: response.message,
    };
  },

  async validateProviderAPIKey(
    provider: string,
    apiKey: string
  ): Promise<{
    valid: boolean;
    message: string;
  }> {
    const client = grpcClient.settings();
    const request = create(ValidateProviderAPIKeyRequestSchema, {
      provider,
      apiKey,
    });
    const response = await client.validateProviderAPIKey(request);
    return {
      valid: response.valid,
      message: response.message,
    };
  },

  async completeCodexOAuth(
    code: string,
    codeVerifier: string,
    redirectURI: string
  ): Promise<{
    success: boolean;
    message: string;
  }> {
    const client = grpcClient.settings();
    const request = create(CompleteCodexOAuthRequestSchema, {
      code,
      codeVerifier,
      redirectUri: redirectURI,
    });
    const response = await client.completeCodexOAuth(request);
    return {
      success: response.success,
      message: response.message,
    };
  },

  async completeClaudeOAuth(
    code: string,
    codeVerifier: string,
    redirectURI: string,
    state: string
  ): Promise<{
    success: boolean;
    message: string;
  }> {
    const client = grpcClient.settings();
    const request = create(CompleteClaudeOAuthRequestSchema, {
      code,
      codeVerifier,
      redirectUri: redirectURI,
      state,
    });
    const response = await client.completeClaudeOAuth(request);
    return {
      success: response.success,
      message: response.message,
    };
  },

  // ============================================
  // Sub-phase 6e: Privacy Settings
  // ============================================

  async getPrivacySettings(): Promise<PrivacySettings> {
    const client = grpcClient.settings();
    const request = create(GetPrivacySettingsRequestSchema, {});
    const response = await client.getPrivacySettings(request);
    return {
      analytics_enabled: response.analyticsEnabled,
      crash_reporting_enabled: response.crashReportingEnabled,
    };
  },

  async updatePrivacySettings(settings: Partial<PrivacySettings>): Promise<{
    success: boolean;
    message: string;
    analytics_enabled: boolean;
    crash_reporting_enabled: boolean;
    requires_restart: boolean;
  }> {
    const client = grpcClient.settings();
    const request = create(UpdatePrivacySettingsRequestSchema, {
      analyticsEnabled: settings.analytics_enabled,
      crashReportingEnabled: settings.crash_reporting_enabled,
    });
    const response = await client.updatePrivacySettings(request);
    return {
      success: response.success,
      message: response.message,
      analytics_enabled: response.analyticsEnabled,
      crash_reporting_enabled: response.crashReportingEnabled,
      requires_restart: response.requiresRestart,
    };
  },

  async trackPageVisited(pageName: string, previousPage?: string): Promise<{
    success: boolean;
  }> {
    const client = grpcClient.settings();
    const request = create(TrackPageVisitedRequestSchema, {
      pageName,
      previousPage,
    });
    const response = await client.trackPageVisited(request);
    return {
      success: response.success,
    };
  },

  // ============================================
  // Skills Management
  // ============================================

  async installSkill(input: InstallSkillRequest): Promise<InstallSkillResult> {
    const client = grpcClient.settings();
    const request = create(InstallSkillRequestSchema, {
      projectId: input.project_id,
      source: input.source,
      sourceSubpath: input.source_subpath,
      ref: input.ref,
      name: input.name,
      scope: scopeToProto(input.scope),
      conflictPolicy: conflictPolicyToProto(input.conflict_policy),
      dryRun: input.dry_run ?? false,
    });
    try {
      const response = await client.installSkill(request);
      return {
        success: response.success,
        message: response.message,
        result: response.result ? protoSkillInstallResultToFrontend(response.result) : undefined,
      };
    } catch (error) {
      if (isSkillsDisabledError(error)) {
        return {
          success: false,
          message: "Skills feature is disabled",
        };
      }
      throw error;
    }
  },

  async listInstalledSkills(
    projectId: string,
    options?: { page_size?: number; page_token?: string }
  ): Promise<{
    skills: InstalledSkill[];
    total: number;
    diagnostics: SkillDiscoveryDiagnostic[];
    next_page_token: string;
  }> {
    const client = grpcClient.settings();
    const request = create(ListInstalledSkillsRequestSchema, {
      projectId,
      pageSize: options?.page_size ?? 0,
      pageToken: options?.page_token ?? "",
    });
    try {
      const response = await client.listInstalledSkills(request);
      return {
        skills: response.skills.map(protoInstalledSkillToFrontend),
        total: response.total,
        diagnostics: response.diagnostics.map(protoSkillDiscoveryDiagnosticToFrontend),
        next_page_token: response.nextPageToken,
      };
    } catch (error) {
      if (isSkillsDisabledError(error)) {
        return {
          skills: [],
          total: 0,
          diagnostics: [],
          next_page_token: "",
        };
      }
      throw error;
    }
  },

  async getInstalledSkillDefinition(projectId: string, skillId: string): Promise<{
    skill_id: string;
    definition_path: string;
    definition_content: string;
    assets: SkillAsset[];
  }> {
    const client = grpcClient.settings();
    const request = create(GetInstalledSkillDefinitionRequestSchema, {
      projectId,
      skillId,
    });
    const response = await client.getInstalledSkillDefinition(request);
    return {
      skill_id: response.skillId,
      definition_path: response.definitionPath,
      definition_content: response.definitionContent,
      assets: response.assets.map((asset) => ({
        path: asset.path,
        mime_type: asset.mimeType,
        content: asset.content,
      })),
    };
  },

  async setSkillEnabled(
    projectId: string,
    skillId: string,
    enabled: boolean
  ): Promise<SetSkillEnabledResult> {
    const client = grpcClient.settings();
    const request = create(SetSkillEnabledRequestSchema, {
      projectId,
      skillId,
      enabled,
    });
    const response = await client.setSkillEnabled(request);
    return {
      success: response.success,
      message: response.message,
      skill_id: response.skillId,
      enabled: response.enabled,
    };
  },

  async listRecommendedSkills(
    projectId: string,
    options?: { page_size?: number; page_token?: string }
  ): Promise<{
    recommended: RecommendedSkill[];
    total: number;
    next_page_token: string;
  }> {
    const client = grpcClient.settings();
    const request = create(ListRecommendedSkillsRequestSchema, {
      projectId,
      pageSize: options?.page_size ?? 0,
      pageToken: options?.page_token ?? "",
    });
    try {
      const response = await client.listRecommendedSkills(request);
      return {
        recommended: response.recommended.map(protoRecommendedSkillToFrontend),
        total: response.total,
        next_page_token: response.nextPageToken,
      };
    } catch (error) {
      if (isSkillsDisabledError(error)) {
        return {
          recommended: [],
          total: 0,
          next_page_token: "",
        };
      }
      throw error;
    }
  },

  async deleteGlobalSkill(projectId: string, relativePath: string): Promise<DeleteGlobalSkillResult> {
    const client = grpcClient.settings();
    const request = create(DeleteGlobalSkillRequestSchema, { projectId, relativePath });
    const response = await client.deleteGlobalSkill(request);
    return {
      success: response.success,
      message: response.message,
    };
  },

  // ============================================
  // Sub-phase 6g: Configuration Health
  // ============================================

  async getConfigHealth(projectId?: string, typeFilter?: string): Promise<ConfigHealth> {
    const client = grpcClient.settings();
    const request = create(GetConfigHealthRequestSchema, {
      projectId,
      typeFilter,
    });
    const response = await client.getConfigHealth(request);
    return {
      errors: response.errors.map(protoConfigErrorToFrontend),
      error_count: response.errorCount,
      warning_count: response.warningCount,
    };
  },
};