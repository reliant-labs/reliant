// The broker's account page (GET /auth/account), re-read on demand.

import { useCallback, useEffect, useState } from "react";
import { AccountError, fetchAccount, type Account } from "@/lib/auth/account";
import { accountErrorMessage } from "./parts";

export type ForgeAccountLoad =
  | { state: "loading" }
  | { state: "ready"; account: Account }
  | { state: "error"; message: string };

/** The account page; `reload` re-reads it after a change. */
export function useForgeAccount(): { load: ForgeAccountLoad; reload: () => void } {
  const [load, setLoad] = useState<ForgeAccountLoad>({ state: "loading" });
  const [generation, setGeneration] = useState(0);

  useEffect(() => {
    let live = true;
    fetchAccount().then(
      (account) => {
        if (live) setLoad({ state: "ready", account });
      },
      (err: unknown) => {
        if (!live) return;
        setLoad((prev) =>
          // A failed re-read keeps what is on screen; only the first read shows the error.
          prev.state === "ready"
            ? prev
            : {
                state: "error",
                message:
                  err instanceof AccountError && err.code === "not_signed_in"
                    ? "Your session has ended. Sign in again to manage your account."
                    : accountErrorMessage(err, "Couldn't load your account."),
              },
        );
      },
    );
    return () => {
      live = false;
    };
  }, [generation]);

  const reload = useCallback(() => setGeneration((g) => g + 1), []);
  return { load, reload };
}
