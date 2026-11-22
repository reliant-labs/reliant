import { useEffect, useState } from "react";

import { fetchAuthOptions, LEGACY_AUTH_OPTIONS, type AuthOptions } from "./native-login";

/**
 * Loads which optional sign-in routes the server serves, once per mount.
 * Everything is off while loading and when the server cannot say, so the
 * sign-in screen never offers "Create an account" on a deployment that would
 * refuse it.
 */
export function useAuthOptions(): AuthOptions {
  const [options, setOptions] = useState<AuthOptions>(LEGACY_AUTH_OPTIONS);
  useEffect(() => {
    let live = true;
    void fetchAuthOptions().then((o) => {
      if (live) setOptions(o);
    });
    return () => {
      live = false;
    };
  }, []);
  return options;
}
