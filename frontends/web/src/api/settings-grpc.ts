// Copyright (c) 2025 Reliant Labs

import { connectClient } from "../lib/connect";
import { create } from "@bufbuild/protobuf";
import {
  fetchProviderStatuses,
  invalidateProviderStatuses,
} from "../hooks/settings-queries";
import type {
  Setting as ProtoSetting,
  ProviderStatus as ProtoProviderStatus,
} from "../gen/reliant/v1/settings_pb";
import {
  SettingsService,
  // Sub-phase 6a: Core Settings CRUD
  ListSettingsRequestSchema,
  DeleteSettingRequestSchema,
  BatchUpsertSettingsRequestSchema,
  // Sub-phase 6b: UI Settings
  GetShortcutsRequestSchema,
  UpdateShortcutsRequestSchema,
  // Sub-phase 6d: Provider Settings
  ValidateProviderAPIKeyRequestSchema,
  SyncReliantProviderRequestSchema,
  CompleteCodexOAuthRequestSchema,
  CompleteClaudeOAuthRequestSchema,
  CompleteAntigravityOAuthRequestSchema,
  StartCopilotDeviceAuthRequestSchema,
  PollCopilotDeviceAuthRequestSchema,
  PollCopilotDeviceAuthResponse_Status,
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

export interface ProviderStatus {
  provider: string;
  /** At least one account of the provider can serve (none is waiting for sign-in). */
  configured: boolean;
  /** Accounts connected for the provider, whatever their state. */
  account_count: number;
  display_name: string;
}

/**
 * What a sign-in completion says happened to the account. Signing in to an upstream account that
 * is already connected refreshes it (`created` false); signing in as someone other than the
 * reconnect target saves a separate account and sets `identityMismatch`.
 */
export interface SignInOutcome {
  success: boolean;
  message: string;
  created: boolean;
  identityMismatch?: string;
  /** The account's label (its detected email, else a name), for the confirmation line. */
  accountLabel?: string;
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

function protoProviderStatusToFrontend(
  proto: ProtoProviderStatus
): ProviderStatus {
  return {
    provider: proto.provider,
    configured: proto.configured,
    account_count: proto.accountCount,
    display_name: proto.displayName,
  };
}

// ============================================
// Settings gRPC Client
// ============================================

export const settingsGrpc = {
  // ============================================
  // Sub-phase 6a: Core Settings CRUD
  // ============================================

  async listSettings(projectId?: string): Promise<{
    settings: Setting[];
    total: number;
  }> {
    const client = connectClient(SettingsService);
    const request = create(ListSettingsRequestSchema, { projectId });
    const response = await client.listSettings(request);
    return {
      settings: response.settings.map(protoSettingToFrontend),
      total: response.total,
    };
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
    const client = connectClient(SettingsService);
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
    const client = connectClient(SettingsService);
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
    const client = connectClient(SettingsService);
    const request = create(GetShortcutsRequestSchema, {});
    const response = await client.getShortcuts(request);
    return response.shortcuts;
  },

  async updateShortcuts(shortcuts: string): Promise<{
    success: boolean;
    message: string;
  }> {
    const client = connectClient(SettingsService);
    const request = create(UpdateShortcutsRequestSchema, { shortcuts });
    const response = await client.updateShortcuts(request);
    return {
      success: response.success,
      message: response.message,
    };
  },

  // ============================================
  // Sub-phase 6d: Provider Settings
  // ============================================

  /**
   * Through hooks/settings-queries' cache entry (which also dedupes
   * concurrent callers — the onboarding checklist and apiKeySetupStore both
   * ask on page load).
   */
  async getProviderStatuses(): Promise<ProviderStatus[]> {
    const response = await fetchProviderStatuses();
    return response.providers.map(protoProviderStatusToFrontend);
  },

  async validateProviderAPIKey(
    provider: string,
    apiKey: string
  ): Promise<{
    valid: boolean;
    message: string;
  }> {
    const client = connectClient(SettingsService);
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
    redirectURI: string,
    accountId?: string
  ): Promise<SignInOutcome> {
    const client = connectClient(SettingsService);
    const request = create(CompleteCodexOAuthRequestSchema, {
      code,
      codeVerifier,
      redirectUri: redirectURI,
      accountId,
    });
    const response = await client.completeCodexOAuth(request);
    invalidateProviderStatuses();
    return {
      success: response.success,
      message: response.message,
      created: response.created,
      identityMismatch: response.identityMismatch || undefined,
      accountLabel: response.account?.label || undefined,
    };
  },

  /**
   * Sync the Reliant provider API key from control-plane.
   *
   * Mints or fetches the per-user rlat_ LLM key (llm:invoke) and persists it
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
    const client = connectClient(SettingsService);
    const request = create(SyncReliantProviderRequestSchema, {
      forceRotate,
    });
    const response = await client.syncReliantProvider(request);
    invalidateProviderStatuses();
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
    state: string,
    accountId?: string
  ): Promise<SignInOutcome> {
    const client = connectClient(SettingsService);
    const request = create(CompleteClaudeOAuthRequestSchema, {
      code,
      codeVerifier,
      redirectUri: redirectURI,
      state,
      accountId,
    });
    const response = await client.completeClaudeOAuth(request);
    invalidateProviderStatuses();
    return {
      success: response.success,
      message: response.message,
      created: response.created,
      identityMismatch: response.identityMismatch || undefined,
      accountLabel: response.account?.label || undefined,
    };
  },

  /**
   * Exchange an Antigravity (Google) OAuth authorization code for tokens.
   *
   * Takes no `state`: Google's token endpoint does not want it echoed back on
   * the exchange. State is still generated and verified in the browser against
   * the redirect — see lib/antigravity-oauth.
   */
  async completeAntigravityOAuth(
    code: string,
    codeVerifier: string,
    redirectURI: string,
    accountId?: string
  ): Promise<SignInOutcome> {
    const client = connectClient(SettingsService);
    const request = create(CompleteAntigravityOAuthRequestSchema, {
      code,
      codeVerifier,
      redirectUri: redirectURI,
      accountId,
    });
    const response = await client.completeAntigravityOAuth(request);
    invalidateProviderStatuses();
    return {
      success: response.success,
      message: response.message,
      created: response.created,
      identityMismatch: response.identityMismatch || undefined,
      accountLabel: response.account?.label || undefined,
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
    const client = connectClient(SettingsService);
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
    deviceCode: string,
    accountId?: string
  ): Promise<{
    status: PollCopilotDeviceAuthResponse_Status;
    errorMessage: string;
    created: boolean;
    identityMismatch?: string;
    accountLabel?: string;
  }> {
    const client = connectClient(SettingsService);
    const request = create(PollCopilotDeviceAuthRequestSchema, {
      deviceCode,
      accountId,
    });
    const response = await client.pollCopilotDeviceAuth(request);
    invalidateProviderStatuses();
    return {
      status: response.status,
      errorMessage: response.errorMessage,
      created: response.created,
      identityMismatch: response.identityMismatch || undefined,
      accountLabel: response.account?.label || undefined,
    };
  },
};
