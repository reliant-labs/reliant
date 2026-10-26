import { settingsGrpc } from "./settings-grpc";
import { addProviderApiKey, disconnectProvider } from "../hooks/provider-account-queries";

// Re-export types for consumers
export type { Chat, Message, ContentBlock, Attachment } from "../types/chat";
export { ChatState } from "../types/chat";

export const api = {
  settings: {
    getShortcuts: async () => {
      const shortcuts = await settingsGrpc.getShortcuts();
      return { shortcuts };
    },

    updateShortcuts: async (shortcuts: string) => {
      const result = await settingsGrpc.updateShortcuts(shortcuts);
      return { message: result.message };
    },

    getProviders: async () => {
      const providers = await settingsGrpc.getProviderStatuses();
      return providers.map((p) => ({
        provider: p.provider,
        configured: p.configured,
        hasApiKey: p.configured,
        accountCount: p.account_count,
        displayName: p.display_name,
      }));
    },

    /**
     * Adds an API key as a new account of the provider (ProviderAccountService.AddProviderAPIKey,
     * which validates it first). The same key twice is one account. Rejects an empty key: removing
     * a provider is {@link deleteProvider}.
     */
    updateProvider: async (provider: string, apiKey: string) => {
      if (!apiKey.trim()) {
        throw new Error('An API key is required.');
      }
      const { created } = await addProviderApiKey(provider, apiKey.trim());
      return { message: created ? 'API key added' : 'That key is already connected' };
    },

    /** Removes every account of the provider (it becomes disconnected). */
    deleteProvider: async (provider: string) => {
      await disconnectProvider(provider);
      return { message: 'Disconnected' };
    },

    validateProviderAPIKey: async (provider: string, apiKey: string) => {
      return settingsGrpc.validateProviderAPIKey(provider, apiKey);
    },

    syncReliantProvider: async (forceRotate = false) => {
      return settingsGrpc.syncReliantProvider(forceRotate);
    },

    listSettings: async (projectId?: string) => {
      return settingsGrpc.listSettings(projectId);
    },

    batchUpsertSettings: async (
      settings: Array<{ key: string; value: string; valueType?: string }>,
      projectId?: string
    ) => {
      return settingsGrpc.batchUpsertSettings(settings, projectId);
    },

    deleteSetting: async (key: string, projectId?: string) => {
      return settingsGrpc.deleteSetting(key, projectId);
    },
  },
};