import { useState, useEffect, useCallback } from "react";
import { create } from "@bufbuild/protobuf";
import { Copy, Check, Plus, Loader2 } from "lucide-react";
import { grpcClient } from "../../api/grpc-client";
import {
  ListTokensRequestSchema,
  CreateTokenRequestSchema,
  RevokeTokenRequestSchema,
  UpdateTokenRequestSchema,
  ScopeListSchema,
  TokenKind,
} from "../../gen/reliant/v1/token_pb";
import type { TokenInfo } from "../../gen/reliant/v1/token_pb";
import { Button } from "../ui/Button";
import { Input } from "../ui/Input";

// The permissions a daemon credential can carry. Mirrors the org-administration
// half of forge/pkg/accesstoken plus daemon:connect, which every daemon token
// needs and which is therefore not editable away here — a daemon credential
// without it cannot connect, and the store would refuse the empty set anyway.
const EDITABLE_SCOPES = [
  "deploy:read",
  "deploy:write",
  "secret:read",
  "secret:write",
  "domain:read",
  "domain:write",
] as const;

// REQUIRED_SCOPE is kept on every edit: dropping it would produce a "daemon"
// token that cannot connect a daemon.
const REQUIRED_SCOPE = "daemon:connect";

export function TokenSettings() {
  const [tokens, setTokens] = useState<TokenInfo[]>([]);
  const [loading, setLoading] = useState(true);
  const [creating, setCreating] = useState(false);
  const [newTokenName, setNewTokenName] = useState("");
  const [newTokenRaw, setNewTokenRaw] = useState<string | null>(null);
  const [revokingId, setRevokingId] = useState<string | null>(null);
  const [submitting, setSubmitting] = useState(false);
  const [copied, setCopied] = useState(false);
  const [error, setError] = useState<string | null>(null);
  // The token being edited, plus the draft. Editing changes the row in place
  // rather than opening a dialog: the permissions are what the operator came
  // to read, so they should not disappear behind a modal to be changed.
  const [editingId, setEditingId] = useState<string | null>(null);
  const [draftName, setDraftName] = useState("");
  const [draftScopes, setDraftScopes] = useState<string[]>([]);

  const fetchTokens = useCallback(async () => {
    try {
      setError(null);
      const res = await grpcClient
        .token()
        .listTokens(create(ListTokensRequestSchema, { kind: TokenKind.DAEMON }));
      setTokens(res.tokens);
    } catch (err) {
      console.error("Failed to fetch tokens:", err);
      setError("Failed to load tokens.");
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => {
    fetchTokens();
  }, [fetchTokens]);

  const handleCreate = async () => {
    if (!newTokenName.trim()) return;
    setSubmitting(true);
    setError(null);
    try {
      const res = await grpcClient
        .token()
        .createToken(
          create(CreateTokenRequestSchema, {
            name: newTokenName.trim(),
            kind: TokenKind.DAEMON,
          })
        );
      setNewTokenRaw(res.token);
      setNewTokenName("");
      setCreating(false);
      await fetchTokens();
    } catch (err) {
      console.error("Failed to create token:", err);
      setError("Failed to create token.");
    } finally {
      setSubmitting(false);
    }
  };

  const handleRevoke = async (id: string) => {
    setError(null);
    try {
      await grpcClient
        .token()
        .revokeToken(create(RevokeTokenRequestSchema, { id }));
      setRevokingId(null);
      await fetchTokens();
    } catch (err) {
      console.error("Failed to revoke token:", err);
      setError("Failed to revoke token.");
    }
  };

  const startEdit = (token: TokenInfo) => {
    setEditingId(token.id);
    setDraftName(token.name);
    setDraftScopes([...(token.scopes ?? [])]);
    setError(null);
  };

  const cancelEdit = () => {
    setEditingId(null);
    setDraftName("");
    setDraftScopes([]);
  };

  const toggleDraftScope = (scope: string) => {
    setDraftScopes((current) =>
      current.includes(scope)
        ? current.filter((s) => s !== scope)
        : [...current, scope]
    );
  };

  // A rename sends NO scopes at all. The field is optional on the wire
  // precisely so that "leave permissions alone" is expressible; sending the
  // list on every save would make a rename a permissions edit too.
  const handleSaveEdit = async (token: TokenInfo) => {
    const name = draftName.trim();
    if (!name) return;
    const current = token.scopes ?? [];
    const scopesChanged =
      draftScopes.length !== current.length ||
      draftScopes.some((s) => !current.includes(s));

    setSubmitting(true);
    setError(null);
    try {
      await grpcClient.token().updateToken(
        create(UpdateTokenRequestSchema, {
          id: token.id,
          ...(name !== token.name ? { name } : {}),
          ...(scopesChanged
            ? {
                scopes: create(ScopeListSchema, {
                  // daemon:connect is re-added unconditionally: it is what
                  // makes the credential a daemon credential.
                  scopes: Array.from(
                    new Set([REQUIRED_SCOPE, ...draftScopes])
                  ),
                }),
              }
            : {}),
        })
      );
      cancelEdit();
      await fetchTokens();
    } catch (err) {
      console.error("Failed to update token:", err);
      setError("Failed to update token.");
    } finally {
      setSubmitting(false);
    }
  };

  const handleCopy = async (text: string) => {
    await navigator.clipboard.writeText(text);
    setCopied(true);
    setTimeout(() => setCopied(false), 2000);
  };

  const formatDate = (iso: string) => {
    if (!iso) return "Never";
    return new Date(iso).toLocaleDateString(undefined, {
      year: "numeric",
      month: "short",
      day: "numeric",
    });
  };

  // TokenService.ListTokens returns only LIVE tokens; revoked ones are
  // filtered server-side.
  const activeTokens = tokens;

  return (
    <div className="space-y-6">
      <div>
        <h2 className="text-lg font-semibold mb-2">Access Tokens</h2>
        <p className="text-sm text-muted-foreground">
          Create and manage access tokens for connecting headless daemons and
          self-hosted environments.
        </p>
      </div>

      {error && (
        <div className="rounded-lg bg-red-50 dark:bg-red-950/20 border border-red-200 dark:border-red-800 p-3">
          <p className="text-sm text-red-800 dark:text-red-200">{error}</p>
        </div>
      )}

      {/* Usage instructions */}
      <div className="border border-border/40 rounded-lg p-4 bg-muted/30 shadow-[inset_0_1px_0_0_rgba(255,255,255,0.03)]">
        <p className="text-xs text-muted-foreground mb-2">
          Use a token to connect a headless daemon:
        </p>
        <p className="text-sm font-mono">
          echo "your-token" | reliant daemon start --token
        </p>
      </div>

      {/* New token reveal */}
      {newTokenRaw && (
        <div className="border border-green-200 dark:border-green-800 rounded-lg p-6 space-y-3 bg-green-50 dark:bg-green-950/20">
          <h3 className="font-medium text-green-800 dark:text-green-200">
            Token Created
          </h3>
          <div className="flex items-center gap-2">
            <Input
              value={newTokenRaw}
              readOnly
              className="font-mono text-sm"
            />
            <Button
              variant="outline"
              size="sm"
              onClick={() => handleCopy(newTokenRaw)}
            >
              {copied ? (
                <Check className="w-4 h-4" />
              ) : (
                <Copy className="w-4 h-4" />
              )}
            </Button>
          </div>
          <p className="text-sm text-yellow-700 dark:text-yellow-400 font-medium">
            This token will only be shown once. Copy it now.
          </p>
          <Button variant="ghost" size="sm" onClick={() => setNewTokenRaw(null)}>
            Dismiss
          </Button>
        </div>
      )}

      {/* Create token */}
      <div className="border border-border/40 rounded-lg p-6 space-y-4 shadow-[inset_0_1px_0_0_rgba(255,255,255,0.03)]">
        <h3 className="font-medium">Create Token</h3>
        {creating ? (
          <div className="flex items-center gap-2">
            <Input
              placeholder="Token name (e.g. my-server)"
              value={newTokenName}
              onChange={(e) => setNewTokenName(e.target.value)}
              onKeyDown={(e) => e.key === "Enter" && handleCreate()}
              autoFocus
            />
            <Button
              variant="primary"
              size="sm"
              onClick={handleCreate}
              disabled={!newTokenName.trim() || submitting}
            >
              {submitting ? (
                <Loader2 className="w-4 h-4 animate-spin" />
              ) : (
                "Create"
              )}
            </Button>
            <Button
              variant="ghost"
              size="sm"
              onClick={() => {
                setCreating(false);
                setNewTokenName("");
              }}
            >
              Cancel
            </Button>
          </div>
        ) : (
          <Button
            variant="outline"
            size="sm"
            onClick={() => setCreating(true)}
            leftIcon={<Plus className="w-4 h-4" />}
          >
            New Token
          </Button>
        )}
      </div>

      {/* Token list */}
      <div className="border border-border/40 rounded-lg p-6 space-y-4 shadow-[inset_0_1px_0_0_rgba(255,255,255,0.03)]">
        <h3 className="font-medium">Active Tokens</h3>
        {loading ? (
          <div className="flex items-center gap-2 text-sm text-muted-foreground">
            <Loader2 className="w-4 h-4 animate-spin" />
            Loading tokens...
          </div>
        ) : activeTokens.length === 0 ? (
          <p className="text-sm text-muted-foreground">No active tokens.</p>
        ) : (
          <div className="space-y-3">
            {activeTokens.map((token) => (
              <div
                key={token.id}
                className="border border-border/40 rounded-lg p-3 space-y-3"
              >
                <div className="flex items-start justify-between gap-4">
                  <div className="space-y-1 min-w-0">
                    {editingId === token.id ? (
                      <Input
                        value={draftName}
                        onChange={(e) => setDraftName(e.target.value)}
                        className="text-sm"
                        autoFocus
                      />
                    ) : (
                      <p className="text-sm font-medium">{token.name}</p>
                    )}
                    <div className="flex flex-wrap items-center gap-3 text-xs text-muted-foreground">
                      <span className="font-mono">{token.tokenPrefix}...</span>
                      {token.daemonId && (
                        <span>Daemon {token.daemonId}</span>
                      )}
                      <span>Created {formatDate(token.createdAt)}</span>
                      <span>Last used {formatDate(token.lastUsedAt)}</span>
                      {/* A permanent credential is the normal case for a
                          daemon. Rendering the expiry column blank would read
                          as missing data rather than as the fact that
                          revoking is the only way to end it. */}
                      <span>
                        {token.expiresAt
                          ? `Expires ${formatDate(token.expiresAt)}`
                          : "Never expires"}
                      </span>
                    </div>
                  </div>

                  {editingId === token.id ? (
                    <div className="flex items-center gap-2 shrink-0">
                      <Button
                        variant="primary"
                        size="xs"
                        onClick={() => handleSaveEdit(token)}
                        disabled={!draftName.trim() || submitting}
                      >
                        {submitting ? (
                          <Loader2 className="w-4 h-4 animate-spin" />
                        ) : (
                          "Save"
                        )}
                      </Button>
                      <Button variant="ghost" size="xs" onClick={cancelEdit}>
                        Cancel
                      </Button>
                    </div>
                  ) : revokingId === token.id ? (
                    <div className="flex items-center gap-2 shrink-0">
                      <span className="text-xs text-muted-foreground">
                        {token.daemonId
                          ? "Revoke? This will disconnect the daemon."
                          : "Revoke? This cannot be undone."}
                      </span>
                      <Button
                        variant="destructive"
                        size="xs"
                        onClick={() => handleRevoke(token.id)}
                      >
                        Confirm
                      </Button>
                      <Button
                        variant="ghost"
                        size="xs"
                        onClick={() => setRevokingId(null)}
                      >
                        Cancel
                      </Button>
                    </div>
                  ) : (
                    <div className="flex items-center gap-2 shrink-0">
                      <Button
                        variant="ghost"
                        size="xs"
                        onClick={() => startEdit(token)}
                      >
                        Edit
                      </Button>
                      <Button
                        variant="ghost"
                        size="xs"
                        onClick={() => setRevokingId(token.id)}
                      >
                        Revoke
                      </Button>
                    </div>
                  )}
                </div>

                {/* Permissions. A permanent credential has no expiry to bound
                    it, so this is the only remaining answer to "what can this
                    token do" — it is shown always, not only while editing. */}
                {editingId === token.id ? (
                  <div className="flex flex-wrap gap-x-4 gap-y-2 pt-1">
                    {EDITABLE_SCOPES.map((scope) => (
                      <label
                        key={scope}
                        className="flex items-center gap-2 text-xs text-muted-foreground"
                      >
                        <input
                          type="checkbox"
                          aria-label={scope}
                          checked={draftScopes.includes(scope)}
                          onChange={() => toggleDraftScope(scope)}
                          className="accent-primary"
                        />
                        <span className="font-mono">{scope}</span>
                      </label>
                    ))}
                  </div>
                ) : (
                  <div className="flex flex-wrap gap-1.5">
                    {(token.scopes ?? []).map((scope) => (
                      <span
                        key={scope}
                        className="font-mono text-xs px-1.5 py-0.5 rounded border border-border/60 bg-background text-muted-foreground"
                      >
                        {scope}
                      </span>
                    ))}
                  </div>
                )}
              </div>
            ))}
          </div>
        )}
      </div>
    </div>
  );
}