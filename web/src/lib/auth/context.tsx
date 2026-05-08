import { createContext, useContext, useEffect, useState, type ReactNode } from "react";
import type { AuthProvider, AuthUser } from "./provider";

interface AuthContextValue {
  user: AuthUser | null;
  isAuthenticated: boolean;
  isLoading: boolean;
  login: () => Promise<void>;
  logout: () => Promise<void>;
  getToken: () => Promise<string | null>;
}

const AuthContext = createContext<AuthContextValue | null>(null);

export function AuthContextProvider({
  provider,
  children,
}: {
  provider: AuthProvider;
  children: ReactNode;
}) {
  const [user, setUser] = useState<AuthUser | null>(provider.getUser());

  useEffect(() => {
    return provider.onAuthStateChange(setUser);
  }, [provider]);

  const value: AuthContextValue = {
    user,
    isAuthenticated: provider.isAuthenticated(),
    isLoading: provider.isLoading(),
    login: () => provider.login(),
    logout: () => provider.logout(),
    getToken: () => provider.getToken(),
  };

  return <AuthContext.Provider value={value}>{children}</AuthContext.Provider>;
}

export function useAuth(): AuthContextValue {
  const ctx = useContext(AuthContext);
  if (!ctx) {
    throw new Error("useAuth must be used within an <AuthContextProvider>");
  }
  return ctx;
}
