/**
 * ReliantAISection — the managed-Reliant AI settings surface, rendered at
 * `/settings/reliant-ai` (Foundation maps the `reliant-ai` section id here).
 *
 * This is the END-USER view of Reliant-issued LLM access + spend, ported from
 * admin-web's `(dashboard)/ai/page.tsx` but built on the PUBLIC RPCs only:
 *   - BillingService.GetCurrentUserWalletOverview  → credit balance
 *   - BillingService.GetCurrentUserReliantOverview → period spend / cap / models
 *   - LLMGatewayService.GetLLMSpend                → spend by model + usage rows
 *   - LLMGatewayService.ListLLMKeys / Create / Revoke / Rotate → key management
 *
 * Distinct from `/settings/general` (CombinedGeneralSettings), which manages the
 * user's OWN bring-your-own provider API keys. There is no platform-admin data
 * here — admin-web's BillingAdminService cards are intentionally omitted; the
 * per-user GetLLMSpend gives the end-user the equivalent spend view.
 */

import { useMemo, useState } from "react";
import {
  CreditCard,
  KeyRound,
  Plus,
  RefreshCw,
  Trash2,
  Copy,
  Check,
  X,
} from "lucide-react";

import { cn } from "../../../lib/utils";
import {
  Button,
  Card,
  CardHeader,
  CardTitle,
  CardContent,
  PageHeader,
  Table,
  Thead,
  Tbody,
  Tr,
  Th,
  Td,
  Badge,
  EmptyState,
} from "./ui";
import {
  reliantAIAvailable,
  type LLMKey,
  type LLMSpendEntry,
} from "../../../services/controlPlane/reliantAI";
import {
  useReliantOverview,
  useWalletOverview,
  useLLMKeys,
  useAvailableModels,
  useLLMSpend,
  useCreateLLMKey,
  useRevokeLLMKey,
  useRotateLLMKey,
} from "../../../hooks/useReliantAIQueries";
import { LLMKeyStatus } from "../../../gen/controlplane/v1/public/shared_pb";

// ---------------------------------------------------------------------------
// Formatting helpers (self-contained — reliant-web has no lib/billing).
// ---------------------------------------------------------------------------

const usd = (value: number): string =>
  new Intl.NumberFormat("en-US", {
    style: "currency",
    currency: "USD",
    minimumFractionDigits: 2,
    maximumFractionDigits: 2,
  }).format(Number.isFinite(value) ? value : 0);

const usdFromNanos = (nanos?: bigint): string => usd(Number(nanos ?? 0n) / 1e9);
const usdFromCents = (cents?: bigint): string => usd(Number(cents ?? 0n) / 100);

const formatModelLabel = (model?: string | null): string => {
  if (!model) return "—";
  return model
    .split("-")
    .map((part) => (part ? part.charAt(0).toUpperCase() + part.slice(1) : part))
    .join(" ");
};

const formatBudgetDuration = (duration?: string): string => {
  if (!duration) return "";
  const match = duration.match(/^(\d+)\s*([a-zA-Z]+)$/);
  if (!match) return duration;
  const [, n, unit] = match;
  const unitLabels: Record<string, string> = {
    d: "day",
    day: "day",
    days: "day",
    mo: "month",
    month: "month",
    months: "month",
    h: "hour",
    hour: "hour",
    w: "week",
    week: "week",
  };
  const label = unitLabels[unit] ?? unit;
  return `${n} ${label}${Number(n) === 1 ? "" : "s"}`;
};

const formatTimestamp = (ts?: { seconds: bigint; nanos: number }): string => {
  if (!ts?.seconds) return "—";
  return new Date(Number(ts.seconds) * 1000).toLocaleDateString("en-US", {
    year: "numeric",
    month: "short",
    day: "numeric",
  });
};

const toDateParam = (d: Date): string => d.toISOString().slice(0, 10);

interface KeyStatusMeta {
  label: string;
  variant: "success" | "error" | "neutral";
  isActive: boolean;
}

const keyStatusMeta = (status: LLMKeyStatus): KeyStatusMeta => {
  switch (status) {
    case LLMKeyStatus.LLM_KEY_STATUS_ACTIVE:
      return { label: "Active", variant: "success", isActive: true };
    case LLMKeyStatus.LLM_KEY_STATUS_REVOKED:
      return { label: "Revoked", variant: "error", isActive: false };
    case LLMKeyStatus.LLM_KEY_STATUS_EXPIRED:
      return { label: "Expired", variant: "neutral", isActive: false };
    default:
      return { label: "Unknown", variant: "neutral", isActive: false };
  }
};

// ---------------------------------------------------------------------------
// Section shell — gates on cloud availability so the hooks below never fire
// (and never throw from getControlPlaneClient) in a non-cloud build.
// ---------------------------------------------------------------------------

export function ReliantAISection() {
  if (!reliantAIAvailable) {
    return (
      <div className="space-y-6">
        <PageHeader
          title="Reliant AI"
          subtitle="Managed AI access, credits, spend, and LLM keys."
        />
        <EmptyState
          icon={KeyRound}
          title="Cloud not configured"
          description="Reliant AI management is only available when this app is connected to a Reliant control plane."
        />
      </div>
    );
  }
  return <ReliantAIPanel />;
}

function ReliantAIPanel() {
  const [createOpen, setCreateOpen] = useState(false);
  const [revealedKey, setRevealedKey] = useState<{
    name: string;
    secret: string;
    kind: "created" | "rotated";
  } | null>(null);

  const overviewQ = useReliantOverview();
  const walletQ = useWalletOverview();

  const overview = overviewQ.data;
  const wallet = walletQ.data;
  const entitlement = overview?.entitlement;

  // Org resolution mirrors admin-web: prefer the wallet's org, fall back to the
  // entitlement's source ref. A signed-in user resolves to exactly one billing
  // scope, so there is no org switcher.
  const orgId = wallet?.organization?.id ?? entitlement?.sourceRefId ?? "";

  const { startDate, endDate } = useMemo(() => {
    const end = new Date();
    const start = new Date(end);
    start.setDate(end.getDate() - 30);
    return { startDate: toDateParam(start), endDate: toDateParam(end) };
  }, []);

  const keysQ = useLLMKeys(orgId);
  const spendQ = useLLMSpend({ orgId, startDate, endDate });
  const modelsQ = useAvailableModels(orgId, createOpen);

  const createMut = useCreateLLMKey();
  const revokeMut = useRevokeLLMKey(orgId);
  const rotateMut = useRotateLLMKey(orgId);

  const keys: LLMKey[] = keysQ.data ?? [];
  const spendEntries: LLMSpendEntry[] = spendQ.data?.entries ?? [];
  const totalSpend = spendQ.data?.totalSpend ?? 0;
  const allowedModels = entitlement?.allowedModels ?? [];

  // Spend-by-model rollup — the end-user analog of admin's provider breakdown.
  const spendByModel = useMemo(() => {
    const byModel = new Map<string, { spend: number; requests: number }>();
    for (const entry of spendEntries) {
      const bucket = byModel.get(entry.model) ?? { spend: 0, requests: 0 };
      bucket.spend += entry.spend;
      bucket.requests += Number(entry.requests ?? 0n);
      byModel.set(entry.model, bucket);
    }
    return [...byModel.entries()]
      .map(([model, v]) => ({ model, ...v }))
      .sort((a, b) => b.spend - a.spend);
  }, [spendEntries]);

  const loading = overviewQ.isLoading || walletQ.isLoading;
  const fatalError =
    overviewQ.error && walletQ.error
      ? (overviewQ.error as Error).message || "Failed to load Reliant AI data."
      : "";

  const capLabel = entitlement
    ? entitlement && overview?.spendCapUnlimited
      ? "Unlimited"
      : usdFromCents(entitlement.monthlySpendCapCents)
    : "—";

  const handleRevoke = (key: LLMKey) => {
    if (!confirm(`Revoke "${key.name}"? This cannot be undone.`)) return;
    revokeMut.mutate(key.id);
  };

  const handleRotate = async (key: LLMKey) => {
    if (
      !confirm(
        `Rotate "${key.name}"? A new secret is issued immediately; update your integrations.`,
      )
    )
      return;
    const res = await rotateMut.mutateAsync({ keyId: key.id });
    setRevealedKey({ name: key.name, secret: res.plaintextKey, kind: "rotated" });
  };

  return (
    <div className="space-y-6">
      <PageHeader
        title="Reliant AI"
        subtitle="Manage your Reliant-issued AI access, available models, credit balance, spend, and LLM keys."
      />

      {fatalError && (
        <div className="rounded-md border border-destructive/20 bg-destructive/10 p-3 text-sm text-destructive">
          {fatalError}
        </div>
      )}

      {revealedKey && (
        <RevealedKeyBanner
          name={revealedKey.name}
          secret={revealedKey.secret}
          kind={revealedKey.kind}
          onDismiss={() => setRevealedKey(null)}
        />
      )}

      {loading ? (
        <div className="text-sm text-muted-foreground">
          Loading Reliant AI access…
        </div>
      ) : !overview && !wallet ? (
        <EmptyState
          icon={KeyRound}
          title="No access data"
          description="We could not load your AI access and billing configuration. Try again in a moment."
        />
      ) : (
        <>
          {/* Balance + spend summary */}
          <div className="grid grid-cols-1 gap-4 xl:grid-cols-2">
            <Card>
              <CardHeader>
                <CardTitle>Credit balance</CardTitle>
              </CardHeader>
              <CardContent>
                <p className="text-3xl font-semibold text-foreground">
                  {usdFromNanos(wallet?.wallet?.balanceUsdNanos)}
                </p>
                <p className="mt-1 text-sm text-muted-foreground">
                  Available account credits
                </p>
                {allowedModels.length > 0 && (
                  <div className="mt-4">
                    <p className="text-xs uppercase tracking-wide text-muted-foreground">
                      Available models
                    </p>
                    <div className="mt-2 flex flex-wrap gap-1.5">
                      {allowedModels.map((m) => (
                        <Badge key={m} label={formatModelLabel(m)} variant="info" />
                      ))}
                    </div>
                  </div>
                )}
              </CardContent>
            </Card>

            <Card>
              <CardHeader>
                <div className="flex items-center gap-3">
                  <div className="rounded-lg bg-muted p-2 text-foreground">
                    <CreditCard className="h-5 w-5" />
                  </div>
                  <CardTitle>AI spend (last 30 days)</CardTitle>
                </div>
              </CardHeader>
              <CardContent>
                <p className="text-3xl font-semibold text-foreground">
                  {usd(totalSpend)}
                </p>
                <div className="mt-4 grid grid-cols-2 gap-3 text-sm sm:grid-cols-3">
                  <div>
                    <p className="text-xs uppercase tracking-wide text-muted-foreground">
                      This period
                    </p>
                    <p className="mt-1 font-medium text-foreground">
                      {usdFromCents(overview?.currentPeriodSpendCents)}
                    </p>
                  </div>
                  <div>
                    <p className="text-xs uppercase tracking-wide text-muted-foreground">
                      Remaining
                    </p>
                    <p className="mt-1 font-medium text-foreground">
                      {overview?.spendCapUnlimited
                        ? "Unlimited"
                        : usdFromCents(overview?.remainingSpendCents)}
                    </p>
                  </div>
                  <div>
                    <p className="text-xs uppercase tracking-wide text-muted-foreground">
                      Monthly cap
                    </p>
                    <p className="mt-1 font-medium text-foreground">{capLabel}</p>
                  </div>
                </div>
                {overview?.spendCapReached && (
                  <p className="mt-3 text-sm text-amber-600">
                    Your monthly spend cap has been reached.
                  </p>
                )}
              </CardContent>
            </Card>
          </div>

          {/* Spend by model */}
          <Card>
            <CardHeader>
              <CardTitle>Spend by model</CardTitle>
            </CardHeader>
            <CardContent>
              {spendQ.isLoading ? (
                <p className="text-sm text-muted-foreground">Loading spend…</p>
              ) : spendByModel.length === 0 ? (
                <p className="text-sm text-muted-foreground">
                  No AI spend recorded in the last 30 days.
                </p>
              ) : (
                <Table>
                  <Thead>
                    <Tr>
                      <Th>Model</Th>
                      <Th>Requests</Th>
                      <Th>Spend</Th>
                    </Tr>
                  </Thead>
                  <Tbody>
                    {spendByModel.map((row) => (
                      <Tr key={row.model}>
                        <Td className="font-medium text-foreground">
                          {formatModelLabel(row.model)}
                        </Td>
                        <Td className="text-muted-foreground">
                          {row.requests.toLocaleString("en-US")}
                        </Td>
                        <Td className="text-muted-foreground">{usd(row.spend)}</Td>
                      </Tr>
                    ))}
                  </Tbody>
                </Table>
              )}
            </CardContent>
          </Card>

          {/* Recent usage */}
          <Card>
            <CardHeader>
              <CardTitle>Recent usage</CardTitle>
            </CardHeader>
            <CardContent>
              {spendQ.isLoading ? (
                <p className="text-sm text-muted-foreground">Loading usage…</p>
              ) : spendEntries.length === 0 ? (
                <p className="text-sm text-muted-foreground">
                  No AI usage yet. Your first request will appear here.
                </p>
              ) : (
                <Table>
                  <Thead>
                    <Tr>
                      <Th>Key</Th>
                      <Th>Model</Th>
                      <Th>Requests</Th>
                      <Th>Spend</Th>
                      <Th>Period</Th>
                    </Tr>
                  </Thead>
                  <Tbody>
                    {spendEntries.map((e, i) => (
                      <Tr key={`${e.keyId}-${e.model}-${i}`}>
                        <Td className="font-medium text-foreground">
                          {e.keyName || e.keyId || "—"}
                        </Td>
                        <Td className="text-muted-foreground">
                          {formatModelLabel(e.model)}
                        </Td>
                        <Td className="text-muted-foreground">
                          {Number(e.requests ?? 0n).toLocaleString("en-US")}
                        </Td>
                        <Td className="text-muted-foreground">{usd(e.spend)}</Td>
                        <Td className="text-muted-foreground">
                          {formatTimestamp(e.periodStart)}
                        </Td>
                      </Tr>
                    ))}
                  </Tbody>
                </Table>
              )}
            </CardContent>
          </Card>

          {/* LLM keys */}
          <LLMKeysCard
            orgId={orgId}
            keys={keys}
            loading={keysQ.isLoading}
            error={keysQ.error ? (keysQ.error as Error).message : ""}
            revoking={revokeMut.isPending ? revokeMut.variables ?? null : null}
            rotating={rotateMut.isPending}
            createOpen={createOpen}
            onToggleCreate={() => setCreateOpen((v) => !v)}
            availableModels={
              modelsQ.data && modelsQ.data.length > 0
                ? modelsQ.data
                : allowedModels
            }
            creating={createMut.isPending}
            onCreate={async (args) => {
              const res = await createMut.mutateAsync({ orgId, ...args });
              setCreateOpen(false);
              setRevealedKey({
                name: args.name,
                secret: res.plaintextKey,
                kind: "created",
              });
            }}
            onRevoke={handleRevoke}
            onRotate={handleRotate}
          />
        </>
      )}
    </div>
  );
}

// ---------------------------------------------------------------------------
// One-time plaintext secret reveal.
// ---------------------------------------------------------------------------

function RevealedKeyBanner({
  name,
  secret,
  kind,
  onDismiss,
}: {
  name: string;
  secret: string;
  kind: "created" | "rotated";
  onDismiss: () => void;
}) {
  const [copied, setCopied] = useState(false);
  const copy = async () => {
    try {
      await navigator.clipboard.writeText(secret);
      setCopied(true);
      setTimeout(() => setCopied(false), 2000);
    } catch {
      /* clipboard unavailable — user can select manually */
    }
  };
  return (
    <div className="rounded-lg border border-primary/40 bg-primary/10 p-4">
      <div className="flex items-start justify-between gap-3">
        <div className="min-w-0">
          <p className="text-sm font-semibold text-foreground">
            {kind === "created" ? "Key created" : "Key rotated"}: {name}
          </p>
          <p className="mt-1 text-xs text-muted-foreground">
            Copy this secret now — it is shown once and cannot be retrieved
            again.
          </p>
          <code className="mt-2 block break-all rounded-md bg-background px-3 py-2 font-mono text-xs text-foreground">
            {secret}
          </code>
        </div>
        <button
          type="button"
          onClick={onDismiss}
          aria-label="Dismiss"
          className="text-muted-foreground hover:text-foreground"
        >
          <X className="h-4 w-4" />
        </button>
      </div>
      <div className="mt-3 flex justify-end">
        <Button variant="outline" size="sm" onClick={copy}>
          {copied ? (
            <Check className="mr-1 h-3.5 w-3.5" />
          ) : (
            <Copy className="mr-1 h-3.5 w-3.5" />
          )}
          {copied ? "Copied" : "Copy secret"}
        </Button>
      </div>
    </div>
  );
}

// ---------------------------------------------------------------------------
// LLM keys card: list + inline create form + rotate/revoke actions.
// ---------------------------------------------------------------------------

interface LLMKeysCardProps {
  orgId: string;
  keys: LLMKey[];
  loading: boolean;
  error: string;
  revoking: string | null;
  rotating: boolean;
  createOpen: boolean;
  onToggleCreate: () => void;
  availableModels: string[];
  creating: boolean;
  onCreate: (args: {
    name: string;
    models: string[];
    maxBudget?: number;
    budgetDuration?: string;
  }) => Promise<void>;
  onRevoke: (key: LLMKey) => void;
  onRotate: (key: LLMKey) => Promise<void>;
}

function LLMKeysCard({
  orgId,
  keys,
  loading,
  error,
  revoking,
  rotating,
  createOpen,
  onToggleCreate,
  availableModels,
  creating,
  onCreate,
  onRevoke,
  onRotate,
}: LLMKeysCardProps) {
  if (!orgId) return null;
  return (
    <Card>
      <CardHeader>
        <div className="flex flex-col gap-2 sm:flex-row sm:items-center sm:justify-between">
          <div>
            <CardTitle>LLM keys</CardTitle>
            <p className="mt-1 text-sm text-muted-foreground">
              Reliant-managed keys for your account. Use these with the OpenAI /
              Anthropic / Vertex SDKs via the Reliant gateway.
            </p>
          </div>
          <Button onClick={onToggleCreate} variant={createOpen ? "outline" : "primary"}>
            <Plus className="mr-1.5 h-4 w-4" />
            {createOpen ? "Cancel" : "Create key"}
          </Button>
        </div>
      </CardHeader>
      <CardContent>
        {error && (
          <div className="mb-4 rounded-md border border-destructive/20 bg-destructive/10 px-3 py-2 text-sm text-destructive">
            {error}
          </div>
        )}

        {createOpen && (
          <CreateKeyForm
            availableModels={availableModels}
            creating={creating}
            onCreate={onCreate}
          />
        )}

        {loading ? (
          <p className="text-sm text-muted-foreground">Loading keys…</p>
        ) : keys.length === 0 ? (
          <p className="text-sm text-muted-foreground">
            No LLM keys yet. Create one to start proxying requests through the
            Reliant gateway.
          </p>
        ) : (
          <Table>
            <Thead>
              <Tr>
                <Th>Name</Th>
                <Th>Status</Th>
                <Th>Budget</Th>
                <Th>Spend</Th>
                <Th>Created</Th>
                <Th className="text-right">Actions</Th>
              </Tr>
            </Thead>
            <Tbody>
              {keys.map((k) => {
                const status = keyStatusMeta(k.status);
                const budget =
                  k.maxBudget !== undefined
                    ? (() => {
                        const dur = formatBudgetDuration(k.budgetDuration);
                        const amount = usd(k.maxBudget);
                        return dur ? `${amount} / ${dur}` : amount;
                      })()
                    : "—";
                return (
                  <Tr key={k.id}>
                    <Td className="font-medium text-foreground">{k.name}</Td>
                    <Td>
                      <Badge label={status.label} variant={status.variant} />
                    </Td>
                    <Td className="text-muted-foreground">{budget}</Td>
                    <Td className="text-muted-foreground">
                      {k.spend !== undefined ? usd(k.spend) : "—"}
                    </Td>
                    <Td className="text-muted-foreground">
                      {formatTimestamp(k.createdAt)}
                    </Td>
                    <Td className="text-right">
                      {status.isActive && (
                        <div className="flex justify-end gap-2">
                          <Button
                            variant="outline"
                            size="sm"
                            onClick={() => onRotate(k)}
                            disabled={rotating}
                          >
                            <RefreshCw className="mr-1 h-3.5 w-3.5" />
                            Rotate
                          </Button>
                          <Button
                            variant="outline"
                            size="sm"
                            onClick={() => onRevoke(k)}
                            disabled={revoking === k.id}
                          >
                            <Trash2 className="mr-1 h-3.5 w-3.5" />
                            Revoke
                          </Button>
                        </div>
                      )}
                    </Td>
                  </Tr>
                );
              })}
            </Tbody>
          </Table>
        )}
      </CardContent>
    </Card>
  );
}

function CreateKeyForm({
  availableModels,
  creating,
  onCreate,
}: {
  availableModels: string[];
  creating: boolean;
  onCreate: (args: {
    name: string;
    models: string[];
    maxBudget?: number;
    budgetDuration?: string;
  }) => Promise<void>;
}) {
  const [name, setName] = useState("");
  const [selectedModels, setSelectedModels] = useState<string[]>([]);
  const [budget, setBudget] = useState("");
  const [duration, setDuration] = useState("30d");
  const [formError, setFormError] = useState("");

  const toggleModel = (m: string) =>
    setSelectedModels((prev) =>
      prev.includes(m) ? prev.filter((x) => x !== m) : [...prev, m],
    );

  const submit = async () => {
    setFormError("");
    if (!name.trim()) {
      setFormError("A key name is required.");
      return;
    }
    const parsedBudget = budget.trim() ? Number(budget) : undefined;
    if (parsedBudget !== undefined && (Number.isNaN(parsedBudget) || parsedBudget < 0)) {
      setFormError("Budget must be a non-negative number.");
      return;
    }
    try {
      await onCreate({
        name: name.trim(),
        models: selectedModels,
        maxBudget: parsedBudget,
        budgetDuration: parsedBudget !== undefined ? duration : "",
      });
      setName("");
      setSelectedModels([]);
      setBudget("");
    } catch (err) {
      setFormError(err instanceof Error ? err.message : "Failed to create key.");
    }
  };

  return (
    <div className="mb-4 space-y-4 rounded-lg border border-border bg-muted/30 p-4">
      <div className="space-y-2">
        <label className="text-sm font-medium text-foreground">Key name</label>
        <input
          value={name}
          onChange={(e) => setName(e.target.value)}
          placeholder="e.g. production-agent"
          className="w-full rounded-md border border-border bg-background px-3 py-2 text-sm text-foreground focus:ring-2 focus:ring-ring/40"
        />
      </div>

      <div className="space-y-2">
        <label className="text-sm font-medium text-foreground">
          Models{" "}
          <span className="font-normal text-muted-foreground">
            (leave empty for all allowed models)
          </span>
        </label>
        {availableModels.length === 0 ? (
          <p className="text-xs text-muted-foreground">
            No model list available — the key will allow all plan models.
          </p>
        ) : (
          <div className="flex flex-wrap gap-2">
            {availableModels.map((m) => {
              const active = selectedModels.includes(m);
              return (
                <button
                  key={m}
                  type="button"
                  onClick={() => toggleModel(m)}
                  className={cn(
                    "rounded-full border px-3 py-1 text-xs font-medium transition-colors",
                    active
                      ? "border-primary bg-primary/10 text-primary"
                      : "border-border bg-background text-muted-foreground hover:bg-muted",
                  )}
                >
                  {formatModelLabel(m)}
                </button>
              );
            })}
          </div>
        )}
      </div>

      <div className="grid grid-cols-1 gap-3 sm:grid-cols-2">
        <div className="space-y-2">
          <label className="text-sm font-medium text-foreground">
            Budget (USD, optional)
          </label>
          <input
            value={budget}
            onChange={(e) => setBudget(e.target.value)}
            inputMode="decimal"
            placeholder="e.g. 25"
            className="w-full rounded-md border border-border bg-background px-3 py-2 text-sm text-foreground focus:ring-2 focus:ring-ring/40"
          />
        </div>
        <div className="space-y-2">
          <label className="text-sm font-medium text-foreground">
            Budget period
          </label>
          <select
            value={duration}
            onChange={(e) => setDuration(e.target.value)}
            disabled={!budget.trim()}
            className="w-full rounded-md border border-border bg-background px-3 py-2 text-sm text-foreground focus:ring-2 focus:ring-ring/40 disabled:opacity-50"
          >
            <option value="1d">Daily</option>
            <option value="7d">Weekly</option>
            <option value="30d">Monthly</option>
          </select>
        </div>
      </div>

      {formError && <p className="text-sm text-destructive">{formError}</p>}

      <div className="flex justify-end">
        <Button onClick={submit} isLoading={creating} disabled={creating}>
          <Plus className="mr-1.5 h-4 w-4" />
          Create key
        </Button>
      </div>
    </div>
  );
}
