// The account's address, and changing it: a code goes to the NEW address, and
// the change is done once that code is entered here (or its mailed link opened,
// ConfirmEmailChange). Until then the account's address is the new one,
// unverified — the issuer's semantics, said plainly below.

import { useState, type FormEvent } from "react";
import { Mail } from "lucide-react";
import Button from "@/components/forge-ui/button";
import Input from "@/components/forge-ui/input";
import Label from "@/components/forge-ui/label";
import { changeEmail, confirmEmailChange } from "@/lib/auth/account";
import { useRecentProof } from "./RecentProof";
import { AccountSection, InlineError, InlineSuccess, SectionHeading, accountErrorMessage } from "./parts";

type Step = "idle" | "address" | "code";

export function EmailSection({ email, emailVerified, onChanged }: { email: string; emailVerified: boolean; onChanged: () => void }) {
  const withRecentProof = useRecentProof();
  const [step, setStep] = useState<Step>("idle");
  const [address, setAddress] = useState("");
  const [code, setCode] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [notice, setNotice] = useState<string | null>(null);

  const reset = () => {
    setStep("idle");
    setAddress("");
    setCode("");
    setError(null);
  };

  const sendCode = async (event: FormEvent) => {
    event.preventDefault();
    const next = address.trim();
    if (!next) return;
    setBusy(true);
    setError(null);
    setNotice(null);
    try {
      const sent = await withRecentProof(() => changeEmail(next));
      if (sent) {
        setNotice(sent.message);
        setStep("code");
        onChanged();
      }
    } catch (err) {
      setError(accountErrorMessage(err));
    } finally {
      setBusy(false);
    }
  };

  const confirm = async (event: FormEvent) => {
    event.preventDefault();
    const value = code.trim();
    if (!value) return;
    setBusy(true);
    setError(null);
    try {
      const done = await confirmEmailChange(value);
      reset();
      setNotice(`Your address is now ${done.email || address.trim()}.`);
      onChanged();
    } catch (err) {
      setError(accountErrorMessage(err));
    } finally {
      setBusy(false);
    }
  };

  return (
    <AccountSection testId="forge-account-email">
      <SectionHeading
        title="Email address"
        description="Where sign-in links and account mail go."
        action={
          step === "idle" && (
            <Button variant="outline" size="sm" onClick={() => { setNotice(null); setStep("address"); }}>
              Change
            </Button>
          )
        }
      />
      <div className="flex items-center gap-2 text-ui text-foreground">
        <Mail className="h-4 w-4 text-muted-foreground" aria-hidden="true" />
        <span className="truncate">{email || "No address"}</span>
        {email && (
          <span className="flex items-center gap-1.5 text-meta text-muted-foreground">
            <span
              aria-hidden="true"
              className={emailVerified ? "h-2 w-2 rounded-full bg-success" : "h-2 w-2 rounded-full bg-warning"}
            />
            {emailVerified ? "Verified" : "Not verified"}
          </span>
        )}
      </div>

      {step === "address" && (
        <form onSubmit={(e) => void sendCode(e)} className="space-y-3">
          <div className="space-y-1.5">
            <Label htmlFor="forge-new-email">New address</Label>
            <Input
              id="forge-new-email"
              type="email"
              autoComplete="email"
              autoFocus
              value={address}
              onChange={(e) => setAddress(e.target.value)}
            />
            <p className="text-meta text-muted-foreground">
              We send a code to the new address. Until you enter it, that address is your account's, unverified.
            </p>
          </div>
          <div className="flex gap-2">
            <Button variant="secondary" size="sm" type="submit" loading={busy} disabled={!address.trim()}>
              Send code
            </Button>
            <Button variant="ghost" size="sm" onClick={reset} disabled={busy}>
              Cancel
            </Button>
          </div>
        </form>
      )}

      {step === "code" && (
        <form onSubmit={(e) => void confirm(e)} className="space-y-3">
          <div className="space-y-1.5">
            <Label htmlFor="forge-email-code">Code from the email</Label>
            <Input
              id="forge-email-code"
              autoComplete="one-time-code"
              autoFocus
              value={code}
              onChange={(e) => setCode(e.target.value)}
            />
          </div>
          <div className="flex gap-2">
            <Button variant="secondary" size="sm" type="submit" loading={busy} disabled={!code.trim()}>
              Confirm
            </Button>
            <Button variant="ghost" size="sm" onClick={reset} disabled={busy}>
              Later
            </Button>
          </div>
        </form>
      )}

      <InlineError message={error} />
      <InlineSuccess message={notice} />
    </AccountSection>
  );
}
