// Copyright (c) 2025 Reliant Labs

/**
 * Machine credentials (TokenService, kind DAEMON) on forge's generated hooks.
 *
 * The list is the generated `ListTokens` entry, and every generated
 * TokenService write (create, revoke, update) invalidates
 * `tokenServiceKeys.all`, so a write refetches whatever list is mounted with
 * no hand-written refresh. Nothing on the stream touches it.
 *
 * Every call names `kind: DAEMON`: the server refuses a request without it,
 * and a list without it would surface API tokens on the daemon-token page.
 */

import { TokenKind, type ListTokensResponse, type TokenInfo } from "../gen/reliant/v1/token_pb";
import { useListTokens } from "./token-service-hooks_gen";

export const DAEMON_TOKENS_REQUEST = { kind: TokenKind.DAEMON } as const;

/** Hoisted, so React Query re-runs the mapping only when the response changes. */
function selectTokens(response: ListTokensResponse): TokenInfo[] {
  return response.tokens;
}

/** The caller's live daemon tokens. ListTokens returns only live tokens. */
export function useDaemonTokens() {
  return useListTokens(DAEMON_TOKENS_REQUEST, { select: selectTokens });
}
