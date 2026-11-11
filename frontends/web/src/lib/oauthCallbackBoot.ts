import { captureOAuthCallbackCredentials } from "./oauthCallbackCredentials";

// Before router construction and telemetry URL capture, not in a React effect.
captureOAuthCallbackCredentials();
