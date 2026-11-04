/**
 * The providers Settings offers, and how each one connects.
 *
 * One table drives every surface that names a provider: the "Add provider"
 * picker (grouped by `connect`), each provider card's title and its scoped
 * "Add another Claude account" button, the connect panel, and the mobile
 * providers panel. Names are what a user recognises — "Claude", "ChatGPT" —
 * and the way you connect is said by the group a provider sits in, not
 * folded into its name ("Claude subscription", "Codex (ChatGPT)").
 */

/** How a provider is connected: a subscription sign-in, a pasted key, or Reliant's own credits. */
export type ConnectMethod = "subscription" | "api_key" | "credits";

/** The redirect / device flow a sign-in provider runs, or false for a key or managed provider. */
export type ProviderOAuth = "claude" | "codex" | "antigravity" | "copilot" | false;

export interface ProviderConfig {
  /** The provider's name on its card and in the picker. */
  name: string;
  /** The brand alone, for scoped copy: "Add another Claude account". */
  shortName: string;
  /** One line in the picker: what you get, in the user's terms. */
  blurb: string;
  connect: ConnectMethod;
  usesOAuth: ProviderOAuth;
  /** Where to get a key (API-key providers) or manage the subscription. */
  docsUrl: string;
  /** A key's visible prefix, for the input placeholder. Empty for sign-in providers. */
  keyFormat: string;
}

export const providerConfigs = {
  claude: {
    name: "Claude",
    shortName: "Claude",
    blurb: "Sign in with your Claude Pro or Max plan",
    connect: "subscription",
    usesOAuth: "claude",
    docsUrl: "https://claude.ai",
    keyFormat: "",
  },
  codex: {
    name: "ChatGPT",
    shortName: "ChatGPT",
    blurb: "Codex models with your ChatGPT plan",
    connect: "subscription",
    usesOAuth: "codex",
    docsUrl: "https://chatgpt.com",
    keyFormat: "",
  },
  antigravity: {
    name: "Google",
    shortName: "Google",
    blurb: "Gemini models with your Google account",
    connect: "subscription",
    usesOAuth: "antigravity",
    docsUrl: "https://antigravity.google",
    keyFormat: "",
  },
  copilot: {
    name: "GitHub Copilot",
    shortName: "Copilot",
    blurb: "Models from your Copilot Individual or Enterprise plan",
    connect: "subscription",
    usesOAuth: "copilot",
    docsUrl: "https://github.com/settings/copilot",
    keyFormat: "",
  },
  anthropic: {
    name: "Anthropic API",
    shortName: "Anthropic",
    blurb: "Claude models, billed to your Anthropic account",
    connect: "api_key",
    usesOAuth: false,
    docsUrl: "https://console.anthropic.com/settings/keys",
    keyFormat: "sk-ant-...",
  },
  openai: {
    name: "OpenAI API",
    shortName: "OpenAI",
    blurb: "GPT models, billed to your OpenAI account",
    connect: "api_key",
    usesOAuth: false,
    docsUrl: "https://platform.openai.com/api-keys",
    keyFormat: "sk-...",
  },
  gemini: {
    name: "Gemini API",
    shortName: "Gemini",
    blurb: "Gemini models, billed to your Google AI Studio account",
    connect: "api_key",
    usesOAuth: false,
    docsUrl: "https://aistudio.google.com/app/apikey",
    keyFormat: "AIza...",
  },
  openrouter: {
    name: "OpenRouter",
    shortName: "OpenRouter",
    blurb: "Hundreds of models through one key",
    connect: "api_key",
    usesOAuth: false,
    docsUrl: "https://openrouter.ai/keys",
    keyFormat: "sk-or-...",
  },
  reliant: {
    name: "Reliant",
    shortName: "Reliant",
    blurb: "Claude, GPT and Gemini on Reliant credits, no key needed",
    connect: "credits",
    usesOAuth: false,
    docsUrl: "https://reliant.dev/docs",
    keyFormat: "",
  },
} as const satisfies Record<string, ProviderConfig>;

export type ProviderId = keyof typeof providerConfigs;

/** Every provider Settings offers, in the order it lists them. */
export const VISIBLE_PROVIDERS = [
  "claude",
  "codex",
  "antigravity",
  "copilot",
  "reliant",
  "anthropic",
  "openai",
  "gemini",
  "openrouter",
] as const satisfies readonly ProviderId[];

export function isProviderId(id: string): id is ProviderId {
  return Object.prototype.hasOwnProperty.call(providerConfigs, id);
}

/** The config for a provider id, or undefined for one this build does not know. */
export function providerConfig(id: string): ProviderConfig | undefined {
  return isProviderId(id) ? providerConfigs[id] : undefined;
}

/** The provider's display name, falling back to the server's name, then its id. */
export function providerName(id: string, serverName?: string): string {
  return providerConfig(id)?.name ?? (serverName || id);
}

/** "account" for a sign-in or managed provider, "key" for an API-key one. */
export function accountNoun(id: string): "account" | "key" {
  return providerConfig(id)?.connect === "api_key" ? "key" : "account";
}

/** "Add another Claude account" / "Add another OpenAI key". */
export function addAnotherLabel(id: string): string {
  const short = providerConfig(id)?.shortName ?? id;
  return `Add another ${short} ${accountNoun(id)}`;
}

export const CONNECT_GROUPS: ReadonlyArray<{ method: ConnectMethod; label: string }> = [
  { method: "subscription", label: "Sign in with a subscription" },
  { method: "api_key", label: "API key" },
  { method: "credits", label: "Reliant credits" },
];

/** A provider's own words for a failure, in plain language. */
export const parseErrorMessage = (errorText: string, provider: string): string => {
  const lowerError = errorText.toLowerCase();
  const short = providerConfig(provider)?.shortName ?? provider;

  // Sign-in providers share one vocabulary: not connected, expired, refused.
  if (provider === "claude" || provider === "codex" || provider === "antigravity" || provider === "copilot") {
    if (provider === "codex" && (lowerError.includes("missing_scope") || lowerError.includes("api.responses.write"))) {
      return "ChatGPT needs updated permissions. Remove the account and sign in again.";
    }
    if (lowerError.includes("not authenticated")) return `${short} is not connected. Sign in with ${short} to connect it.`;
    if (lowerError.includes("expired")) return `Your ${short} sign-in expired. Reconnect the account.`;
    if (lowerError.includes("unauthorized") || lowerError.includes("401")) {
      return provider === "copilot"
        ? "GitHub Copilot refused the sign-in. Check that your Copilot plan is active, then reconnect."
        : `${short} refused the sign-in. Reconnect the account.`;
    }
    if (lowerError.includes("rate limit") || lowerError.includes("429")) {
      return `${short} is rate limiting requests. Wait a moment, then try again.`;
    }
    if (lowerError.includes("session") || lowerError.includes("invalid")) {
      return `${short} sign-in failed. Reconnect the account.`;
    }
  }

  if (provider === "openrouter") {
    if (lowerError.includes("no endpoints found matching your data policy")) {
      return "OpenRouter's privacy settings block every model. Set a data policy at https://openrouter.ai/settings/privacy, then try again.";
    }
    if (lowerError.includes("unauthorized") || lowerError.includes("401")) {
      return "OpenRouter did not accept that key. Check it at https://openrouter.ai/keys";
    }
    if (lowerError.includes("quota") || lowerError.includes("limit")) {
      return "OpenRouter quota exceeded. Check your usage at https://openrouter.ai/activity";
    }
  }

  if (provider === "anthropic") {
    if (lowerError.includes("model:") && lowerError.includes("not found")) {
      const modelMatch = errorText.match(/model:\s*([^\s,]+)/i);
      const modelName = modelMatch ? modelMatch[1] : "the specified model";
      return `The model "${modelName}" was not found. It may need beta access or not be offered in your region. The key itself is valid; see https://console.anthropic.com/ for the models it can use.`;
    }
    if (lowerError.includes("unauthorized") || lowerError.includes("401")) {
      return "Anthropic did not accept that key. Check it at https://console.anthropic.com/settings/keys";
    }
    if (lowerError.includes("rate limit") || lowerError.includes("429")) {
      return "Anthropic is rate limiting requests. Wait a moment, then try again.";
    }
    if (lowerError.includes("quota") || lowerError.includes("usage")) {
      return "Anthropic usage limit reached. Check https://console.anthropic.com/account/usage";
    }
  }

  if (provider === "openai") {
    if (lowerError.includes("unauthorized") || lowerError.includes("401")) {
      return "OpenAI did not accept that key. Check it at https://platform.openai.com/api-keys";
    }
    if (lowerError.includes("quota") || lowerError.includes("billing")) {
      return "OpenAI reports a billing problem. Check https://platform.openai.com/account/billing";
    }
    if (lowerError.includes("rate limit") || lowerError.includes("429")) {
      return "OpenAI is rate limiting requests. Wait a moment, then try again.";
    }
  }

  if (provider === "gemini") {
    if (lowerError.includes("unauthorized") || lowerError.includes("401")) {
      return "Google did not accept that key. Check it at https://aistudio.google.com/app/apikey";
    }
    if (lowerError.includes("quota") || lowerError.includes("limit")) {
      return "Google API quota exceeded. Check your quota in the Google Cloud console.";
    }
  }

  if (
    lowerError.includes("unauthorized") ||
    lowerError.includes("authentication") ||
    lowerError.includes("invalid") ||
    lowerError.includes("api key") ||
    lowerError.includes("401")
  ) {
    return "That key was not accepted. Check it and try again.";
  }
  if (lowerError.includes("rate limit") || lowerError.includes("429")) {
    return "Rate limit exceeded. Wait a moment, then try again.";
  }
  if (
    lowerError.includes("quota") ||
    lowerError.includes("limit") ||
    lowerError.includes("billing") ||
    lowerError.includes("usage")
  ) {
    return "Usage limit or billing problem. Check the account with the provider.";
  }
  if (
    lowerError.includes("network") ||
    lowerError.includes("connection") ||
    lowerError.includes("timeout") ||
    lowerError.includes("refused")
  ) {
    return "Could not reach the provider. Check your connection and try again.";
  }
  if (
    lowerError.includes("500") ||
    lowerError.includes("502") ||
    lowerError.includes("503") ||
    lowerError.includes("server error")
  ) {
    return "The provider had a server error. Try again in a few moments.";
  }
  return errorText.length > 200 ? "Could not connect. Check the key and try again." : errorText;
};
