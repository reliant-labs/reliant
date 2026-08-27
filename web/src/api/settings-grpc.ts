// Copyright (c) 2025 Reliant Labs

import { grpcClient } from "./grpc-client";
import { singleflight } from "../lib/singleflight";
import { create } from "@bufbuild/protobuf";
import { ConfigScope } from "../gen/reliant/v1/common_pb";
import {
  ConfigSeverity,
  HiddenItemType,
} from "../gen/reliant/v1/settings_pb";
import type {
  Setting as ProtoSetting,
  UserPrompt as ProtoUserPrompt,
  ProviderStatus as ProtoProviderStatus,
} from "../gen/reliant/v1/settings_pb";
import {
  // Sub-phase 6a: Core Settings CRUD
  CreateSettingRequestSchema,
  ListSettingsRequestSchema,
  GetSettingRequestSchema,
  UpdateSettingRequestSchema,
  DeleteSettingRequestSchema,
  BatchUpsertSettingsRequestSchema,
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
  SyncReliantProviderRequestSchema,
  CompleteCodexOAuthRequestSchema,
  CompleteClaudeOAuthRequestSchema,
  StartCopilotDeviceAuthRequestSchema,
  PollCopilotDeviceAuthRequestSchema,
  PollCopilotDeviceAuthResponse_Status,
  // Sub-phase 6e: Privacy
  GetPrivacySettingsRequestSchema,
  UpdatePrivacySettingsRequestSchema,
  TrackPageVisitedRequestSchema,
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

  /**
   * Upsert many settings in ONE request.
   *
   * Server-side each entry has CreateSetting's upsert semantics, so callers do
   * not need to know which keys already exist.
   */
  async batchUpsertSettings(
    settings: Array<{ key: string; value: string; valueType?: string }>,
    projectId?: string
  ): Promise<Setting[]> {
    if (settings.length === 0) return [];
    const client = grpcClient.settings();
    const request = create(BatchUpsertSettingsRequestSchema, {
      settings: settings.map((s) => ({
        key: s.key,
        value: s.value,
        valueType: s.valueType ?? "string",
      })),
      projectId,
    });
    const response = await client.batchUpsertSettings(request);
    return response.settings.map(protoSettingToFrontend);
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
    // Use singleflight to deduplicate concurrent calls
    // (onboardingChecklistStore and apiKeySetupStore both call this on page load)
    return singleflight('getProviderStatuses', async () => {
      const client = grpcClient.settings();
      const request = create(GetProviderStatusesRequestSchema, {});
      const response = await client.getProviderStatuses(request);
      return response.providers.map(protoProviderStatusToFrontend);
    });
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

  /**
   * Sync the Reliant provider API key from control-plane.
   *
   * Mints or fetches the per-user rlnt_ internal API key and persists it
   * locally so chat requests can use the reliant provider without manual
   * configuration. Safe to call once per session post-login; idempotent.
   *
   * @param forceRotate If true, force-rotate the existing key on control-plane.
   */
  async syncReliantProvider(
    forceRotate = false
  ): Promise<{
    success: boolean;
    message: string;
    synced: boolean;
    createdOrg: boolean;
    createdKey: boolean;
    rotatedKey: boolean;
  }> {
    const client = grpcClient.settings();
    const request = create(SyncReliantProviderRequestSchema, {
      forceRotate,
    });
    const response = await client.syncReliantProvider(request);
    return {
      success: response.success,
      message: response.message,
      synced: response.synced,
      createdOrg: response.createdOrg,
      createdKey: response.createdKey,
      rotatedKey: response.rotatedKey,
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

  /**
   * Begin the GitHub Copilot device-authorization flow.
   *
   * Returns the user-facing code the caller must display, the verification URI
   * to open (github.com/login/device), and the polling parameters
   * (interval + expiry) that govern the subsequent pollCopilotDeviceAuth loop.
   */
  async startCopilotDeviceAuth(): Promise<{
    deviceCode: string;
    userCode: string;
    verificationUri: string;
    intervalSeconds: number;
    expiresInSeconds: number;
  }> {
    const client = grpcClient.settings();
    const request = create(StartCopilotDeviceAuthRequestSchema, {});
    const response = await client.startCopilotDeviceAuth(request);
    return {
      deviceCode: response.deviceCode,
      userCode: response.userCode,
      verificationUri: response.verificationUri,
      intervalSeconds: response.intervalSeconds,
      expiresInSeconds: response.expiresInSeconds,
    };
  },

  /**
   * Poll GitHub for completion of a Copilot device-authorization flow.
   *
   * On AUTHORIZED the backend persists the Copilot credential. Copilot has a
   * single flavor — there is no longer a tier concept.
   */
  async pollCopilotDeviceAuth(
    deviceCode: string
  ): Promise<{
    status: PollCopilotDeviceAuthResponse_Status;
    errorMessage: string;
  }> {
    const client = grpcClient.settings();
    const request = create(PollCopilotDeviceAuthRequestSchema, {
      deviceCode,
    });
    const response = await client.pollCopilotDeviceAuth(request);
    return {
      status: response.status,
      errorMessage: response.errorMessage,
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