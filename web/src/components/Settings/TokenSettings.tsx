import { useState, useEffect, useCallback } from "react";
import { create } from "@bufbuild/protobuf";
import { Copy, Check, Plus, Trash2, Loader2 } from "lucide-react";
import { grpcClient } from "../../api/grpc-client";
import {
  ListDaemonTokensRequestSchema,
  CreateDaemonTokenRequestSchema,
  RevokeDaemonTokenRequestSchema,
} from "../../gen/reliant/v1/tools_daemon_pb";
import type { DaemonTokenInfo } from "../../gen/reliant/v1/tools_daemon_pb";
import { Button } from "../ui/Button";
import { Input } from "../ui/Input";

export function TokenSettings() {
  const [tokens, setTokens] = useState<DaemonTokenInfo[]>([]);
  const [loading, setLoading] = useState(true);
  const [creating, setCreating] = useState(false);
  const [newTokenName, setNewTokenName] = useState("");
  const [newTokenRaw, setNewTokenRaw] = useState<string | null>(null);
  const [revokingId, setRevokingId] = useState<string | null>(null);
  const [submitting, setSubmitting] = useState(false);
  const [copied, setCopied] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const fetchTokens = useCallback(async () => {
    try {
      setError(null);
      const res = await grpcClient
        .daemonRegistry()
        .listDaemonTokens(create(ListDaemonTokensRequestSchema, {}));
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
        .daemonRegistry()
        .createDaemonToken(
          create(CreateDaemonTokenRequestSchema, { name: newTokenName.trim() })
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
        .daemonRegistry()
        .revokeDaemonToken(
          create(RevokeDaemonTokenRequestSchema, { tokenId: id })
        );
      setRevokingId(null);
      await fetchTokens();
    } catch (err) {
      console.error("Failed to revoke token:", err);
      setError("Failed to revoke token.");
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

  const activeTokens = tokens.filter((t) => !t.revoked);

  return (
    <div className="space-y-6">
      <div>
        <h2 className="text-lg font-semibold mb-2">Access Tokens</h2>
        <p className="text-sm text-muted-foreground">
          Create and manage personal access tokens for headless daemon
          connections.
        </p>
      </div>

      {error && (
        <div className="rounded-lg bg-red-50 dark:bg-red-950/20 border border-red-200 dark:border-red-800 p-3">
          <p className="text-sm text-red-800 dark:text-red-200">{error}</p>
        </div>
      )}

      {/* Usage instructions */}
      <div className="border border-border rounded-lg p-4 bg-muted/30">
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
      <div className="border border-border rounded-lg p-6 space-y-4">
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
      <div className="border border-border rounded-lg p-6 space-y-4">
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
                className="flex items-center justify-between border border-border rounded-lg p-3"
              >
                <div className="space-y-1">
                  <p className="text-sm font-medium">{token.name}</p>
                  <div className="flex items-center gap-3 text-xs text-muted-foreground">
                    <span className="font-mono">{token.tokenPrefix}...</span>
                    <span>Created {formatDate(token.createdAt)}</span>
                    <span>
                      Last used {formatDate(token.lastUsedAt)}
                    </span>
                  </div>
                </div>
                {revokingId === token.id ? (
                  <div className="flex items-center gap-2">
                    <span className="text-xs text-muted-foreground">
                      Revoke?
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
                  <Button
                    variant="ghost"
                    size="xs"
                    onClick={() => setRevokingId(token.id)}
                  >
                    <Trash2 className="w-4 h-4 text-muted-foreground" />
                  </Button>
                )}
              </div>
            ))}
          </div>
        )}
      </div>
    </div>
  );
}
