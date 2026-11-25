import { useEffect, useState } from "react";

import { fetchPasswordPolicy, type PasswordPolicy } from "./native-login";

/**
 * Loads the password policy once per mount. `null` while loading and when the
 * server cannot say — callers treat both as "show no checklist"
 * (<PasswordRequirements> in @/components/password-requirements renders it).
 */
export function usePasswordPolicy(): PasswordPolicy | null {
  const [policy, setPolicy] = useState<PasswordPolicy | null>(null);
  useEffect(() => {
    let live = true;
    void fetchPasswordPolicy().then((p) => {
      if (live) setPolicy(p);
    });
    return () => {
      live = false;
    };
  }, []);
  return policy;
}
