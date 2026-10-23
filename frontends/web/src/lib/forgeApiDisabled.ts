import { Code, ConnectError } from "@connectrpc/connect";

import { hasErrorReason } from "./errorReasons";

/**
 * The reason the API server sets when its operator has turned the
 * forge RPC surface off (RELIANT_DISABLE_FORGE_API; the server side is
 * internal/grpc/forge_api_disabled.go). Every ForgeService rpc then answers
 * FailedPrecondition carrying it. The reason, not the code, is the tell:
 * FailedPrecondition alone is also what a forge rpc answers when forge ran and
 * failed, which is a different thing to show.
 */
export const FORGE_API_DISABLED_REASON = "forge_api_disabled" as const;

export const FORGE_API_DISABLED_COPY = "Forge is turned off on this server.";

export function isForgeAPIDisabled(err: unknown): err is ConnectError {
  return (
    err instanceof ConnectError &&
    err.code === Code.FailedPrecondition &&
    hasErrorReason(err, FORGE_API_DISABLED_REASON)
  );
}
