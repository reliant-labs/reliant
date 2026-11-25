// The account's passkeys: remove one (recent proof; refused for the last way
// in, `last_credential`), and add one where this deployment offers passkeys
// and this browser can make them.

import { useState } from "react";
import { KeyRound } from "lucide-react";
import Button from "@/components/forge-ui/button";
import { removePasskey, type AccountPasskey } from "@/lib/auth/account";
import { AuthError, passkeysSupported, registerPasskey } from "@/lib/auth/native-login";
import { useRecentProof } from "./RecentProof";
import { AccountSection, InlineError, InlineSuccess, SectionHeading, accountErrorMessage } from "./parts";

export function PasskeysSection({
  passkeys,
  offered,
  onChanged,
}: {
  passkeys: AccountPasskey[];
  /** This deployment offers passkey sign-in (GET /auth/options). */
  offered: boolean;
  onChanged: () => void;
}) {
  const withRecentProof = useRecentProof();
  const [confirming, setConfirming] = useState<string | null>(null);
  const [busy, setBusy] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [notice, setNotice] = useState<string | null>(null);
  const canAdd = offered && passkeysSupported();

  if (passkeys.length === 0 && !canAdd) return null;

  const remove = async (passkey: AccountPasskey) => {
    setBusy(passkey.id);
    setError(null);
    setNotice(null);
    try {
      const done = await withRecentProof(async () => {
        await removePasskey(passkey.id);
        return true;
      });
      if (done) {
        setNotice(`${passkey.name || "Passkey"} removed.`);
        onChanged();
      }
    } catch (err) {
      setError(accountErrorMessage(err));
    } finally {
      setBusy(null);
      setConfirming(null);
    }
  };

  const add = async () => {
    setBusy("add");
    setError(null);
    setNotice(null);
    try {
      await registerPasskey();
      setNotice("Passkey added.");
      onChanged();
    } catch (err) {
      if (!(err instanceof AuthError && err.code === "passkey_cancelled")) setError(accountErrorMessage(err));
    } finally {
      setBusy(null);
    }
  };

  return (
    <AccountSection testId="forge-account-passkeys">
      <SectionHeading
        title="Passkeys"
        description="Sign in with your device's lock instead of a password."
        action={
          canAdd && (
            <Button variant="outline" size="sm" onClick={() => void add()} loading={busy === "add"} disabled={busy !== null && busy !== "add"}>
              Add a passkey
            </Button>
          )
        }
      />
      {passkeys.length === 0 ? (
        <p className="text-meta text-muted-foreground">No passkeys yet.</p>
      ) : (
        <ul className="divide-y divide-border-subtle">
          {passkeys.map((passkey) => (
            <li key={passkey.id} className="flex items-center justify-between gap-3 py-2.5">
              <div className="flex min-w-0 items-center gap-3">
                <KeyRound className="h-4 w-4 shrink-0 text-muted-foreground" aria-hidden="true" />
                <span className="truncate text-ui text-foreground">{passkey.name || "Passkey"}</span>
              </div>
              {confirming === passkey.id ? (
                <div className="flex shrink-0 gap-1">
                  <Button variant="ghost" size="sm" onClick={() => setConfirming(null)} disabled={busy === passkey.id}>
                    Keep
                  </Button>
                  <Button variant="danger" size="sm" onClick={() => void remove(passkey)} loading={busy === passkey.id}>
                    Remove
                  </Button>
                </div>
              ) : (
                <Button
                  variant="ghost"
                  size="sm"
                  onClick={() => setConfirming(passkey.id)}
                  disabled={busy !== null}
                  aria-label={`Remove ${passkey.name || "passkey"}`}
                >
                  Remove
                </Button>
              )}
            </li>
          ))}
        </ul>
      )}
      <InlineError message={error} />
      <InlineSuccess message={notice} />
    </AccountSection>
  );
}
