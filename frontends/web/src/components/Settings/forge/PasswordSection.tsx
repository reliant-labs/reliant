// Setting or changing the password. A change with the current password needs
// no separate proof (the current password IS one); setting a first password
// needs a recent proof. Either way the broker then signs every OTHER device
// out, and when it cannot it says so (`sessions_not_revoked`): the password
// did change, and "Sign out other devices" finishes the job.

import { useEffect, useState, type FormEvent } from "react";
import { Check, Circle } from "lucide-react";
import Button from "@/components/forge-ui/button";
import Input from "@/components/forge-ui/input";
import Label from "@/components/forge-ui/label";
import { AccountError, changePassword, revokeOtherSessions } from "@/lib/auth/account";
import { activeRules, describeRule, fetchPasswordPolicy, unmetRules, type PasswordPolicy } from "@/lib/auth/native-login";
import { cn } from "@/lib/utils";
import { useRecentProof } from "./RecentProof";
import { AccountSection, InlineError, InlineSuccess, SectionHeading, accountErrorMessage } from "./parts";

function otherDevicesEnded(n: number): string {
  if (n <= 0) return "";
  return n === 1 ? " One other device was signed out." : ` ${n} other devices were signed out.`;
}

export function PasswordSection({ hasPassword, onChanged }: { hasPassword: boolean; onChanged: () => void }) {
  const withRecentProof = useRecentProof();
  const [open, setOpen] = useState(false);
  const [current, setCurrent] = useState("");
  const [next, setNext] = useState("");
  const [policy, setPolicy] = useState<PasswordPolicy | null>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [notice, setNotice] = useState<string | null>(null);
  // The password changed, but the other sessions were not all ended.
  const [unrevoked, setUnrevoked] = useState(false);

  useEffect(() => {
    if (!open || policy) return;
    let live = true;
    void fetchPasswordPolicy().then((p) => {
      if (live) setPolicy(p);
    });
    return () => {
      live = false;
    };
  }, [open, policy]);

  const close = () => {
    setOpen(false);
    setCurrent("");
    setNext("");
    setError(null);
  };

  const unmet = policy ? unmetRules(policy, next) : [];
  const canSubmit = next !== "" && unmet.length === 0 && (!hasPassword || current !== "");

  const submit = async (event: FormEvent) => {
    event.preventDefault();
    if (!canSubmit) return;
    setBusy(true);
    setError(null);
    setNotice(null);
    setUnrevoked(false);
    try {
      const input = hasPassword ? { currentPassword: current, newPassword: next } : { newPassword: next };
      const done = hasPassword ? await changePassword(input) : await withRecentProof(() => changePassword(input));
      if (done) {
        close();
        setNotice(`${hasPassword ? "Password changed." : "Password set."}${otherDevicesEnded(done.otherSessionsEnded)}`);
        onChanged();
      }
    } catch (err) {
      if (err instanceof AccountError && err.code === "sessions_not_revoked") {
        close();
        setUnrevoked(true);
        onChanged();
      } else {
        setError(
          err instanceof AccountError && err.code === "wrong_password"
            ? "Your current password was not accepted."
            : accountErrorMessage(err),
        );
      }
    } finally {
      setBusy(false);
    }
  };

  const finishRevoke = async () => {
    setBusy(true);
    setError(null);
    try {
      const ended = await withRecentProof(() => revokeOtherSessions());
      if (ended !== undefined) {
        setUnrevoked(false);
        setNotice(`Password changed.${otherDevicesEnded(ended)}`);
        onChanged();
      }
    } catch (err) {
      setError(accountErrorMessage(err));
    } finally {
      setBusy(false);
    }
  };

  return (
    <AccountSection testId="forge-account-password">
      <SectionHeading
        title="Password"
        description={
          hasPassword
            ? "Changing it signs you out everywhere else."
            : "You sign in without a password. Set one to have another way in."
        }
        action={
          !open && (
            <Button variant="outline" size="sm" onClick={() => { setNotice(null); setOpen(true); }}>
              {hasPassword ? "Change password" : "Set a password"}
            </Button>
          )
        }
      />

      {open && (
        <form onSubmit={(e) => void submit(e)} className="space-y-3">
          {hasPassword && (
            <div className="space-y-1.5">
              <Label htmlFor="forge-current-password">Current password</Label>
              <Input
                id="forge-current-password"
                type="password"
                autoComplete="current-password"
                autoFocus
                value={current}
                onChange={(e) => setCurrent(e.target.value)}
              />
            </div>
          )}
          <div className="space-y-1.5">
            <Label htmlFor="forge-new-password">New password</Label>
            <Input
              id="forge-new-password"
              type="password"
              autoComplete="new-password"
              autoFocus={!hasPassword}
              value={next}
              onChange={(e) => setNext(e.target.value)}
            />
            {policy && activeRules(policy).length > 0 && (
              <ul className="space-y-0.5" aria-label="Password requirements">
                {activeRules(policy).map((rule) => {
                  const met = next !== "" && !unmet.includes(rule);
                  return (
                    <li key={rule} className={cn("flex items-center gap-1.5 text-meta", met ? "text-foreground" : "text-muted-foreground")}>
                      {met ? (
                        <Check className="h-3 w-3 text-success-ink" aria-hidden="true" />
                      ) : (
                        <Circle className="h-3 w-3" aria-hidden="true" />
                      )}
                      {describeRule(policy, rule)}
                    </li>
                  );
                })}
              </ul>
            )}
          </div>
          <div className="flex gap-2">
            <Button variant="secondary" size="sm" type="submit" loading={busy} disabled={!canSubmit}>
              {hasPassword ? "Change password" : "Set password"}
            </Button>
            <Button variant="ghost" size="sm" onClick={close} disabled={busy}>
              Cancel
            </Button>
          </div>
        </form>
      )}

      {unrevoked && (
        <div className="space-y-2">
          <InlineError message="Your password changed, but other devices could not be signed out. Sign them out to finish." />
          <Button variant="outline" size="sm" onClick={() => void finishRevoke()} loading={busy}>
            Sign out other devices
          </Button>
        </div>
      )}
      <InlineError message={error} />
      <InlineSuccess message={notice} />
    </AccountSection>
  );
}
