import { useState } from "react";
import { AlertCircle, Check, Eye, EyeOff, Loader2, Plus } from "lucide-react";
import { cn } from "../../../lib/utils";
import type { ProviderAccount } from "../../../types/providerAccount";
import {
  useAntigravityOAuth,
  useClaudeOAuth,
  useCodexOAuth,
  useCopilotOAuth,
  useOAuthAvailability,
} from "../../../hooks";
import {
  REDIRECT_OAUTH_DISPLAY_NAMES,
  isRedirectOAuthProvider,
  resolveOAuthFlow,
  type RedirectOAuthProvider,
} from "../../../lib/oauth-providers";
import { getEventBus } from "../../../lib/events";
import { useGlobalDataStore } from "../../../store/globalDataStore";
import { OAuthHelperPanel } from "../../OAuthHelperPanel";
import { CopilotDevicePanel } from "../../CopilotDevicePanel";
import { parseErrorMessage, providerConfigs, type ProviderId } from "../CombinedGeneralSettings";

type Feedback = { ok: boolean; message: string } | null;

export interface ConnectPanelProps {
  provider: string;
  /** "add" connects another account; "reconnect" signs the given account in again. */
  mode: "add" | "reconnect";
  account?: ProviderAccount;
  variant: "desktop" | "mobile";
  /** Adds a key as a new account (ProviderAccountService.AddProviderAPIKey). */
  addApiKey: (provider: string, apiKey: string) => Promise<{ created: boolean }>;
  /** Called after an account was added or re-authenticated. */
  onConnected: () => void | Promise<void>;
  onClose: () => void;
}

/**
 * What to tell the user after a sign-in that did not do what they expected, or null when it did.
 *
 * "Add another account" in a browser that is still signed in to the first account usually
 * re-authorizes that account silently, which the server answers as created = false. That is not an
 * error, but it is not what the user came for, so the panel stays open with the way out.
 */
export function signInNote(
  provider: string,
  mode: "add" | "reconnect",
  outcome: { created: boolean; identityMismatch?: string; accountLabel?: string },
): { ok: boolean; message: string } | null {
  if (outcome.identityMismatch) return { ok: false, message: outcome.identityMismatch };
  if (mode === "add" && !outcome.created) {
    const who = outcome.accountLabel ? `That's ${outcome.accountLabel}, already connected.` : "That account is already connected.";
    const site = provider === "codex" ? "chatgpt.com" : provider === "claude" ? "claude.ai" : provider === "antigravity" ? "accounts.google.com" : "github.com";
    return {
      ok: true,
      message: `${who} To add a different account, sign out of ${site} in your browser, or use a private window, then try again.`,
    };
  }
  return null;
}

function useAfterConnect(provider: string, onConnected: () => void | Promise<void>) {
  return async () => {
    await onConnected();
    await useGlobalDataStore.getState().refetchModels();
    getEventBus().emit("api-key:saved", { provider });
  };
}

/**
 * The sign-in / key-entry surface for adding another account of a provider, or
 * re-authenticating one. It runs the same flows the single-credential view runs
 * (redirect OAuth through `reliant auth serve` or the desktop daemon callback,
 * the Copilot device flow, a pasted API key); only the target differs.
 */
export function ConnectPanel({
  provider,
  mode,
  account,
  variant,
  addApiKey,
  onConnected,
  onClose,
}: ConnectPanelProps) {
  const mobile = variant === "mobile";
  const config = providerConfigs[provider as ProviderId] as
    | (typeof providerConfigs)[ProviderId]
    | undefined;
  const kind = config?.usesOAuth;

  const codexOAuth = useCodexOAuth();
  const claudeOAuth = useClaudeOAuth();
  const antigravityOAuth = useAntigravityOAuth();
  const copilotOAuth = useCopilotOAuth();
  const flows: Record<RedirectOAuthProvider, ReturnType<typeof useClaudeOAuth>> = {
    claude: claudeOAuth,
    codex: codexOAuth,
    antigravity: antigravityOAuth,
  };
  const oauthAvailability = useOAuthAvailability({ enabled: isRedirectOAuthProvider(kind) });

  const [busy, setBusy] = useState(false);
  const [feedback, setFeedback] = useState<Feedback>(null);
  const [apiKey, setApiKey] = useState("");
  const [showKey, setShowKey] = useState(false);
  const afterConnect = useAfterConnect(provider, onConnected);

  // A reconnect names its account so the server can tell "signed in again" from "signed in as
  // someone else". Adding never does.
  const targetId = mode === "reconnect" ? account?.id : undefined;

  const connectRedirect = async (flowKind: RedirectOAuthProvider) => {
    setBusy(true);
    setFeedback(null);
    try {
      const result = await resolveOAuthFlow(flows, flowKind).start({
        accountId: targetId,
        // Google keeps offering the browser's signed-in account unless asked; Claude and ChatGPT
        // have no documented equivalent, so for them the copy below carries the instruction.
        ...(flowKind === "antigravity" && mode === "add" ? { prompt: "select_account" } : {}),
      });
      if (!result.ok) {
        // Cancelled is not a failure to report — see lib/oauth-abort.
        if (result.errorCode !== "cancelled") setFeedback({ ok: false, message: result.message });
        return;
      }
      await afterConnect();
      const note = signInNote(provider, mode, result.outcome);
      if (note) {
        setFeedback({ ok: note.ok, message: note.message });
        return;
      }
      onClose();
    } catch (error) {
      const message = error instanceof Error ? error.message : "Connection failed";
      setFeedback({ ok: false, message: parseErrorMessage(message, flowKind) });
    } finally {
      setBusy(false);
    }
  };

  const connectCopilot = async () => {
    setFeedback(null);
    const result = await copilotOAuth.start({ accountId: targetId });
    if (!result.ok) return;
    await afterConnect();
    const note = signInNote(provider, mode, result.outcome);
    copilotOAuth.reset();
    if (note) {
      setFeedback({ ok: note.ok, message: note.message });
      return;
    }
    onClose();
  };

  const saveKey = async () => {
    setBusy(true);
    setFeedback(null);
    try {
      const { created } = await addApiKey(provider, apiKey);
      await afterConnect();
      if (!created) {
        setFeedback({ ok: true, message: "That key is already connected." });
        setApiKey("");
        return;
      }
      onClose();
    } catch (error) {
      const message = error instanceof Error ? error.message : "Could not add the key";
      setFeedback({ ok: false, message: parseErrorMessage(message, provider) });
    } finally {
      setBusy(false);
    }
  };

  const heading =
    mode === "reconnect" && account ? `Reconnect ${account.label}` : `Add another ${config?.name ?? provider} account`;

  let body;
  if (kind === "copilot") {
    body = <CopilotDevicePanel oauth={copilotOAuth} onStart={connectCopilot} />;
  } else if (isRedirectOAuthProvider(kind)) {
    body = (
      <OAuthHelperPanel
        providerName={REDIRECT_OAUTH_DISPLAY_NAMES[kind]}
        available={oauthAvailability.available}
        loading={oauthAvailability.loading}
        onRetry={oauthAvailability.recheck}
        onConnect={() => void connectRedirect(kind)}
        connecting={busy}
        onCancel={() => {
          resolveOAuthFlow(flows, kind).cancel();
          setBusy(false);
          setFeedback(null);
        }}
        buttonAlign={mobile ? "stretch" : "end"}
        buttonVariant="subtle"
      />
    );
  } else {
    body = (
      <div className="space-y-2">
        <label className="block text-sm font-medium text-foreground" htmlFor={`new-key-${provider}`}>
          API key
        </label>
        <div className="relative">
          <input
            id={`new-key-${provider}`}
            type={showKey ? "text" : "password"}
            value={apiKey}
            onChange={(e) => {
              setApiKey(e.target.value);
              setFeedback(null);
            }}
            placeholder={`Enter another ${config?.name ?? provider} API key`}
            autoComplete="off"
            autoCapitalize="off"
            autoCorrect="off"
            spellCheck={false}
            className={cn(
              "w-full rounded-md border border-border bg-background px-3 py-2 pr-10 font-mono text-sm text-foreground focus:outline-none focus:ring-2 focus:ring-ring/40",
              mobile && "min-h-[44px]",
            )}
          />
          <button
            type="button"
            onClick={() => setShowKey((v) => !v)}
            aria-label={showKey ? "Hide key" : "Show key"}
            className={cn(
              "absolute right-0 top-1/2 flex -translate-y-1/2 items-center justify-center text-muted-foreground hover:text-foreground",
              mobile ? "min-h-[44px] min-w-[44px]" : "h-8 w-8",
            )}
          >
            {showKey ? <EyeOff className="h-4 w-4" /> : <Eye className="h-4 w-4" />}
          </button>
        </div>
        <button
          type="button"
          onClick={() => void saveKey()}
          disabled={!apiKey || busy}
          className={cn(
            "flex items-center justify-center gap-2 rounded-md bg-primary px-4 text-sm font-medium text-primary-foreground hover:bg-primary/90 disabled:opacity-50",
            mobile ? "min-h-[44px] w-full" : "py-2",
          )}
        >
          {busy ? <Loader2 className="h-4 w-4 animate-spin" /> : <Plus className="h-4 w-4" />}
          Add key
        </button>
      </div>
    );
  }

  return (
    <div className="mt-3 rounded-lg border border-border/60 bg-background p-3" data-testid="connect-panel">
      <div className="mb-2 flex items-center justify-between gap-2">
        <span className="text-sm font-medium text-foreground">{heading}</span>
        <button
          type="button"
          onClick={() => {
            copilotOAuth.reset();
            onClose();
          }}
          className={cn(
            "rounded-md px-2 text-xs text-muted-foreground hover:text-foreground",
            mobile ? "min-h-[44px]" : "py-1",
          )}
        >
          Cancel
        </button>
      </div>
      {body}
      {feedback && (
        <div
          role={feedback.ok ? "status" : "alert"}
          className={cn(
            "mt-3 flex items-start gap-2 rounded-md border p-2 text-xs",
            feedback.ok
              ? "border-success/20 bg-success/10 text-success-ink"
              : "border-destructive/20 bg-destructive/10 text-destructive-ink",
          )}
        >
          {feedback.ok ? (
            <Check className="mt-0.5 h-3.5 w-3.5 shrink-0" />
          ) : (
            <AlertCircle className="mt-0.5 h-3.5 w-3.5 shrink-0" />
          )}
          <span>{feedback.message}</span>
        </div>
      )}
    </div>
  );
}

/** "Add another account" (sign-in providers) or "Add another key" (API-key providers), with its panel. */
export function AddAccountButton({
  provider,
  variant,
  addApiKey,
  onConnected,
}: Omit<ConnectPanelProps, "mode" | "account" | "onClose">) {
  const [open, setOpen] = useState(false);
  const config = providerConfigs[provider as ProviderId] as
    | (typeof providerConfigs)[ProviderId]
    | undefined;
  const label = config?.usesOAuth ? "Add another account" : "Add another key";
  const mobile = variant === "mobile";

  return (
    <div>
      <button
        type="button"
        onClick={() => setOpen((v) => !v)}
        aria-expanded={open}
        className={cn(
          "inline-flex items-center gap-1.5 rounded-md border border-border px-3 text-sm font-medium text-foreground hover:bg-foreground/5",
          mobile ? "min-h-[44px]" : "py-1.5",
        )}
      >
        <Plus className="h-4 w-4" />
        {label}
      </button>
      {open && (
        <ConnectPanel
          provider={provider}
          mode="add"
          variant={variant}
          addApiKey={addApiKey}
          onConnected={onConnected}
          onClose={() => setOpen(false)}
        />
      )}
    </div>
  );
}
