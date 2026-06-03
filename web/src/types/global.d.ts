// Global type declarations
//
// Note: `Window.RELIANT_CONFIG` and `Window.electronAPI` are declared in
// `./electron.d.ts` (canonical home). This file only adds renderer-side debug
// hooks that don't belong with the Electron preload contract.

declare global {
  interface Window {
    __chatStore?: typeof import('../store/chatStore').useChatStore;
    __websockets?: WebSocket[];
  }
}

export {};