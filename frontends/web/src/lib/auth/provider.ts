export interface AuthUser {
  id: string;
  email?: string;
  name?: string;
  roles?: string[];
}

export interface AuthProvider {
  /** Get the current access token, or null if not authenticated */
  getToken(): Promise<string | null>;
  /** Get the current user, or null if not authenticated */
  getUser(): AuthUser | null;
  /** Whether the user is currently authenticated */
  isAuthenticated(): boolean;
  /** Whether auth state is still loading */
  isLoading(): boolean;
  /** Sign in — implementation-specific (redirect, popup, etc.) */
  login(): Promise<void>;
  /** Sign out */
  logout(): Promise<void>;
  /** Subscribe to auth state changes. Returns unsubscribe function. */
  onAuthStateChange(callback: (user: AuthUser | null) => void): () => void;
}
