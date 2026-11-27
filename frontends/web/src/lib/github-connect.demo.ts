// Copyright (c) 2025 Reliant Labs

/**
 * lib/github-connect.ts in the forge demo's bundle (a demo twin,
 * vite-plugin-demo-bundle.ts). Connecting GitHub stores a credential on the
 * signed-in Reliant account: the real module sends the browser to the control
 * plane with the session's token (Supabase or the forge login broker). The
 * demo has no account and no sign-in, so the flow ends at once with an error
 * outcome the caller shows; connectGitHub never throws, here as in the app.
 */

import type * as Real from "./github-connect";

export type GitHubConnectOutcome = Real.GitHubConnectOutcome;
export type GitHubConnectOptions = Real.GitHubConnectOptions;

export const connectGitHub: typeof Real.connectGitHub = async () => ({
  kind: "error",
  message: "Connecting GitHub needs a Reliant account. The demo runs on sample data, with no sign-in.",
});
