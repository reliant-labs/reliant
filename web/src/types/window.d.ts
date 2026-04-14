declare global {
  interface Window {
    RELIANT_CONFIG?: {
      isElectron: boolean;
      grpcPort?: number;
      grpcUrl?: string;
      useTLS?: boolean;
      adminURL?: string;
    };
  }
}

export {};