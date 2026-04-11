// Copyright (c) 2025 Reliant Labs

/**
 * Simple fetch-based HTTP client to replace ky.
 * Provides minimal HTTP functionality for remaining REST endpoints.
 */

import { logger } from "../lib/logger";
import { supabase } from "../lib/supabase";
import { HTTPError } from "../lib/http-error";
import { handleFetchError } from "./error-handler";

// Detect if running in Electron and get backend URL
const getAPIBaseURL = (): string | null => {
  // Check if running in Electron with config available
  if (
    typeof window !== "undefined" &&
    window.RELIANT_CONFIG?.isElectron &&
    window.RELIANT_CONFIG?.backendUrl
  ) {
    return window.RELIANT_CONFIG.backendUrl + "/api";
  }

  // If we're in a file:// protocol (Electron but config not loaded yet),
  // we need to wait for the config - return null to indicate not ready
  if (typeof window !== "undefined" && window.location.protocol === "file:") {
    console.warn(
      "[API Client] Electron detected but backend config not yet available"
    );
    return null;
  }

  // Fallback for development/browser - use relative URL through Vite proxy
  const fallbackUrl =
    import.meta.env.VITE_API_URL ||
    "/api";
  logger.info("[API Client] Using fallback URL:", fallbackUrl);
  return fallbackUrl;
};

// Cache the base URL
let _cachedBaseURL: string | null = null;

function getBaseURL(): string {
  if (_cachedBaseURL === null) {
    const url = getAPIBaseURL();
    if (url === null) {
      throw new Error("API client not ready - waiting for backend configuration");
    }
    _cachedBaseURL = url;
  }
  return _cachedBaseURL;
}

interface FetchOptions {
  method?: "GET" | "POST" | "PUT" | "DELETE" | "PATCH";
  body?: unknown;
  searchParams?: Record<string, string | number | boolean>;
  timeout?: number;
}

/**
 * Make an authenticated fetch request.
 * Automatically adds auth headers and handles errors.
 */
async function fetchWithAuth<T>(path: string, options: FetchOptions = {}): Promise<T> {
  const baseURL = getBaseURL();
  const { method = "GET", body, searchParams, timeout = 30000 } = options;

  // Build URL with search params
  let url = `${baseURL}/${path}`;
  if (searchParams) {
    const params = new URLSearchParams();
    for (const [key, value] of Object.entries(searchParams)) {
      if (value !== undefined && value !== null) {
        params.set(key, String(value));
      }
    }
    const paramString = params.toString();
    if (paramString) {
      url += `?${paramString}`;
    }
  }

  // Build headers
  const headers: HeadersInit = {
    "Content-Type": "application/json",
  };

  // Add auth token
  const {
    data: { session },
  } = await supabase.auth.getSession();
  if (session?.access_token) {
    headers["Authorization"] = `Bearer ${session.access_token}`;
  }

  // Create abort controller for timeout
  const controller = new AbortController();
  const timeoutId = setTimeout(() => controller.abort(), timeout);

  try {
    const request = new Request(url, {
      method,
      headers,
      body: body ? JSON.stringify(body) : undefined,
      signal: controller.signal,
    });

    const response = await fetch(request);

    // Handle 401 - redirect to auth
    if (response.status === 401) {
      await supabase.auth.signOut();
      window.location.href = "/auth";
      throw new HTTPError(response, request);
    }

    // Handle other errors
    if (!response.ok) {
      throw await handleFetchError(response, request);
    }

    // Parse JSON response
    const data = await response.json();
    return data as T;
  } catch (error) {
    if (error instanceof Error && error.name === "AbortError") {
      throw new Error("Request timed out");
    }
    throw error;
  } finally {
    clearTimeout(timeoutId);
  }
}

/**
 * Simple HTTP client with method shortcuts
 */
export const httpClient = {
  get: <T>(path: string, searchParams?: Record<string, string | number | boolean>) =>
    fetchWithAuth<T>(path, { method: "GET", searchParams }),

  post: <T>(path: string, body?: unknown) =>
    fetchWithAuth<T>(path, { method: "POST", body }),

  put: <T>(path: string, body?: unknown) =>
    fetchWithAuth<T>(path, { method: "PUT", body }),

  delete: <T>(path: string) =>
    fetchWithAuth<T>(path, { method: "DELETE" }),

  patch: <T>(path: string, body?: unknown) =>
    fetchWithAuth<T>(path, { method: "PATCH", body }),
};