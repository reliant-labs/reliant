import { useState } from "react";
import { AlertCircle, Check, Eye, EyeOff, Plus, TestTube } from "lucide-react";
import Button from "@/components/forge-ui/button";
import { cn } from "../../../lib/utils";
import { api } from "../../../api/client";
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
import { addAnotherLabel, parseErrorMessage, providerConfig } from "./providerCatalog";
import { ProviderMark } from "./ProviderMark";

type Feedback = { ok: boolean; message: string } | null;

export interface ConnectPanelProps {
  provider: string;
  /** "add" connects an account; "reconnect" signs the given account in again. */
  mode: "add" | "reconnect";
  /**
   * The provider already has an account, so this adds ANOTHER one. The copy
   * says so ("Add another Claude account") and, for a browser sign-in, warns
   * up front that the browser may still be signed in to the first account.
   */
  another?: boolean;
  account?: ProviderAccount;
  variant: "desktop" | "mobile";
  /** Adds a key as a new account (ProviderAccountService.AddProviderAPIKey). */
  addApiKey: (provider: string, apiKey: string) => Promise<{ created: boolean }>;
  /** Called after an account was added or re-authenticated. */
  onConnected: () => void | Promise<void>;
  onClose: () => void;
}

/** The site whose browser session a sign-in reuses. */
function signInSite(provider: string): string {
  switch (provider) {
    case "codex":
      return "chatgpt.com";
    case "claude":
      return "claude.ai";
    case "antigravity":
      return "accounts.google.com";
    default:
      return "github.com";
  }
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
    return {
      ok: true,
      message: `${who} To add a different account, sign out of ${signInSite(provider)} in your browser, or use a private window, then try again.`,
    };
  }
  return null;
}

/** The panel's heading: what clicking its primary button will do. */
export function connectHeading(
  provider: string,
  mode: "add" | "reconnect",
  another: boolean,
  account?: ProviderAccount,
): string {
  if (mode === "reconnect" && account) return `Reconnect ${account.label}`;
  if (another) return addAnotherLabel(provider);
  const config = providerConfig(provider);
  if (!config) return `Connect ${provider}`;
  return config.connect === "api_key" ? `Add your ${config.name} key` : `Sign in with ${config.name}`;
}

function useAfterConnect(provider: string, onConnected: () => void | Promise<void>) {
  return async () => {
    await onConnected();
    await useGlobalDataStore.getState().refetchModels();
    getEventBus().emit("api-key:saved", { provider });
  };
}

/**
 * The one sign-in / key-entry surface for a provider: connecting its first
 * account, adding another, or re-authenticating one. The "Add provider" picker
 * and each card's "Add another …" button both open it, so there is a single
 * add path with two entry points. It runs the redirect OAuth flows (through
 * `reliant auth serve` or the desktop daemon callback), the Copilot device
 * flow, or a pasted API key; only the target differs.
 */
export function ConnectPanel({
  provider,
  mode,
  another = false,
  account,
  variant,
  addApiKey,
  onConnected,
  onClose,
}: ConnectPanelProps) {
  const mobile = variant === "mobile";
  const config = providerConfig(provider);
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
  const [testing, setTesting] = useState(false);
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

  const testKey = async () => {
    setTesting(true);
    setFeedback(null);
    try {
      const result = await api.settings.validateProviderAPIKey(provider, apiKey.trim());
      setFeedback(
        result.valid
          ? { ok: true, message: "The key works." }
          : { ok: false, message: parseErrorMessage(result.message || "That key was not accepted.", provider) },
      );
    } catch {
      setFeedback({ ok: false, message: "Could not test the key. Try again." });
    } finally {
      setTesting(false);
    }
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

  const heading = connectHeading(provider, mode, another, account);

  // One line of help BEFORE the click, where it can still prevent the trap,
  // rather than only signInNote's after-the-fact explanation.
  let hint: string | null = null;
  if (mode === "add" && another) {
    if (isRedirectOAuthProvider(kind) || kind === "copilot") {
      hint = `Still signed in to ${signInSite(provider)} as your other account? Sign out there first, or use a private window.`;
    } else if (config?.connect === "api_key") {
      hint = "Extra keys are used in turn, and when one hits its limit.";
    }
  }

  let body;
  if (kind === "copilot") {
    body = <CopilotDevicePanel oauth={copilotOAuth} onStart={connectCopilot} framed={false} />;
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
        framed={false}
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
            placeholder={config?.keyFormat || "Paste the key"}
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
        {config?.docsUrl && (
          <p className="text-xs text-muted-foreground">
            Create a key in the{" "}
            <a href={config.docsUrl} target="_blank" rel="noopener noreferrer" className="text-primary hover:underline">
              {config.shortName} console
            </a>
            .
          </p>
        )}
        <div className={cn("flex gap-2 pt-1", mobile ? "flex-col" : "justify-end")}>
          <Button
            variant="outline"
            size={mobile ? "xl" : "md"}
            className={mobile ? "min-h-[44px]" : undefined}
            onClick={() => void testKey()}
            disabled={!apiKey.trim() || busy}
            loading={testing}
            leftIcon={<TestTube />}
          >
            Test connection
          </Button>
          <Button
            variant="primary"
            size={mobile ? "xl" : "md"}
            className={mobile ? "min-h-[44px]" : undefined}
            onClick={() => void saveKey()}
            disabled={!apiKey.trim() || testing}
            loading={busy}
            leftIcon={<Plus />}
          >
            Add key
          </Button>
        </div>
      </div>
    );
  }

  return (
    <div className="rounded-lg border border-border/60 bg-background p-3" data-testid="connect-panel">
      <div className="mb-2 flex items-center justify-between gap-2">
        <div className="flex min-w-0 items-center gap-2">
          <ProviderMark provider={provider} size="sm" />
          <span className="truncate text-sm font-medium text-foreground">{heading}</span>
        </div>
        <Button
          variant="ghost"
          size={mobile ? "xl" : "sm"}
          className={mobile ? "min-h-[44px]" : undefined}
          onClick={() => {
            copilotOAuth.reset();
            onClose();
          }}
        >
          Cancel
        </Button>
      </div>
      {hint && <p className="mb-3 text-xs text-muted-foreground">{hint}</p>}
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
