declare global {
  interface Window {
    RELIANT_CONFIG?: {
      isElectron: boolean;
      backendUrl?: string;
      backendPort?: number;
      grpcPort?: number;
      grpcUrl?: string;
      useTLS?: boolean;
    };
  }
}

export {};