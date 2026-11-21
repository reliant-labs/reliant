/// <reference types="vite/client" />

// Global constants injected by Vite
declare const __WORKTREE_NAME__: string;
// This bundle's build info (vite-plugin-version-json.ts). Read it through
// lib/appBuild.ts, which handles the test runner, where nothing defines it.
declare const __RELIANT_BUILD__: import("./lib/releaseIdentity").BuildInfo;
// Which build this bundle is: "app" (index.html) or "demo" (demo.html, the
// forge demo). vite.config.ts defines it; read it through lib/shell.ts.
declare const __WEB_TARGET__: "app" | "demo";

interface ImportMetaEnv {
  // "true" turns on dev sign-in in a Vite dev server (lib/devSignIn.ts).
  // control-plane's dev KCL sets it; its KCL refuses it in any other env.
  readonly VITE_DEV_SIGN_IN?: string;
}
