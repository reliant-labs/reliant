// Global type declarations

declare global {
  interface Window {
    __chatStore?: typeof import('../store/chatStore').useChatStore;
    __websockets?: WebSocket[];
    RELIANT_CONFIG?: {
      isElectron: boolean;
      isDev: boolean;
      grpcPort?: number;
      grpcUrl?: string;
      useTLS?: boolean;
      adminURL?: string;
    };
    electronAPI?: {
      getConfig: () => typeof window.RELIANT_CONFIG;
      log: (level: string, ...args: unknown[]) => void;
    };
  }
}

export {};