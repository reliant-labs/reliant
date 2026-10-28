import { useEffect, useState } from "react";
import { Loader2, Monitor, ShieldAlert } from "lucide-react";

import Button from "@/components/forge-ui/button";
import { supabase } from "@/lib/supabase";
import { AuthorizeOrgPicker, useAuthorizeOrg } from "@/components/Orgs/AuthorizeOrgPicker";
import {
  decidePendingMachine,
  formatPairingCode,
  lookupPendingMachine,
  normalizePairingCode,
  PairingError,
  type PendingMachine,
} from "@/services/controlPlane/pairing";

/**
 * The page `reliant daemon start` links to when it pairs a machine
 * (RFC 8628's verification URI, /oauth/device?user_code=BDFG-HJKL).
 *
 * The same approval also happens inside the app — the Add machine sheet asks
 * for the code and shows the same prompt — so this page exists for the person
 * who follows the link the terminal printed, possibly on another device. It
 * signs them in if needed, keeping the code across the detour, then asks
 * "Approve machine <hostname>?".
 */

type Phase =
  | { kind: "loading" }
  | { kind: "enter"; message?: string }
  | { kind: "confirm"; machine: PendingMachine; busy?: boolean }
  | { kind: "done"; approved: boolean; hostname: string }
  | { kind: "error"; message: string };

export function DevicePairing() {
  const initial = new URLSearchParams(window.location.search).get("user_code") ?? "";
  const [code, setCode] = useState(formatPairingCode(initial));
  const [phase, setPhase] = useState<Phase>({ kind: "loading" });
  const authorizeOrg = useAuthorizeOrg(null);

  const lookUp = async (typed: string) => {
    setPhase({ kind: "loading" });
    try {
      setPhase({ kind: "confirm", machine: await lookupPendingMachine(typed) });
    } catch (err) {
      setPhase({
        kind: "enter",
        message:
          err instanceof PairingError && err.notFound
            ? "No machine is waiting with that code. Check it against your terminal — a code lasts ten minutes."
            : err instanceof Error
              ? err.message
              : "Could not look up the code.",
      });
    }
  };

  useEffect(() => {
    let cancelled = false;
    void (async () => {
      const {
        data: { session },
      } = await supabase.auth.getSession();
      if (cancelled) return;
      if (!session) {
        const back = `/oauth/device${window.location.search}`;
        window.location.href = `/auth?redirect=${encodeURIComponent(back)}`;
        return;
      }
      if (normalizePairingCode(initial).length === 8) void lookUp(initial);
      else setPhase({ kind: "enter" });
    })();
    return () => {
      cancelled = true;
    };
    // Runs once: the code in the URL is the request being answered.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  const decide = async (machine: PendingMachine, approved: boolean) => {
    setPhase({ kind: "confirm", machine, busy: true });
    try {
      await decidePendingMachine(machine.userCode, approved, approved ? authorizeOrg.orgId : null);
      setPhase({ kind: "done", approved, hostname: machine.hostname });
    } catch (err) {
      setPhase({ kind: "error", message: err instanceof Error ? err.message : "Could not record your answer." });
    }
  };

  const shell = (children: React.ReactNode) => (
    <div className="flex min-h-screen items-center justify-center bg-background px-6 py-10">
      <div className="w-full max-w-md space-y-6">{children}</div>
    </div>
  );

  if (phase.kind === "loading") {
    return (
      <div className="flex min-h-screen items-center justify-center bg-background">
        <Loader2 className="h-5 w-5 animate-spin text-muted-foreground" />
      </div>
    );
  }

  if (phase.kind === "error") {
    return shell(
      <div className="space-y-4 text-center">
        <div className="mx-auto flex h-12 w-12 items-center justify-center rounded-full bg-destructive/10">
          <ShieldAlert className="h-6 w-6 text-destructive" />
        </div>
        <h1 className="text-lg font-semibold">Pairing failed</h1>
        <p className="text-sm text-muted-foreground">{phase.message}</p>
      </div>,
    );
  }

  if (phase.kind === "done") {
    return shell(
      <div className="space-y-2 text-center">
        <h1 className="text-lg font-semibold">{phase.approved ? "Machine approved" : "Machine declined"}</h1>
        <p className="text-sm text-muted-foreground">
          {phase.approved
            ? `${phase.hostname || "The machine"} is connecting now. You can close this tab.`
            : "Nothing was connected. You can close this tab."}
        </p>
      </div>,
    );
  }

  if (phase.kind === "confirm") {
    const { machine, busy } = phase;
    return shell(
      <>
        <div className="space-y-2 text-center">
          <div className="mx-auto flex h-12 w-12 items-center justify-center rounded-full bg-primary/10">
            <Monitor className="h-6 w-6 text-primary" />
          </div>
          <h1 className="text-xl font-semibold">Approve machine {machine.hostname || "with this code"}?</h1>
          <p className="text-sm text-muted-foreground">
            {[machine.platform, `code ${machine.userCode}`].filter(Boolean).join(" · ")}. It will run your projects as
            you. Only approve a machine you just started.
          </p>
        </div>
        <AuthorizeOrgPicker
          orgs={authorizeOrg.orgs}
          error={authorizeOrg.failed}
          onRetry={authorizeOrg.retry}
          orgId={authorizeOrg.orgId}
          onChoose={authorizeOrg.choose}
          disabled={busy}
          noun="this machine"
        />
        <div className="flex gap-3">
          <Button variant="secondary" className="flex-1" disabled={busy} onClick={() => void decide(machine, false)}>
            Not my machine
          </Button>
          <Button
            variant="primary"
            className="flex-1"
            loading={busy}
            disabled={!authorizeOrg.ready}
            onClick={() => void decide(machine, true)}
          >
            Approve
          </Button>
        </div>
      </>,
    );
  }

  return shell(
    <form
      className="space-y-4"
      onSubmit={(e) => {
        e.preventDefault();
        if (normalizePairingCode(code).length === 8) void lookUp(code);
      }}
    >
      <div className="space-y-2 text-center">
        <h1 className="text-xl font-semibold">Pair a machine</h1>
        <p className="text-sm text-muted-foreground">
          Enter the code <code className="font-mono">reliant daemon start</code> printed in your terminal.
        </p>
      </div>
      <input
        aria-label="Pairing code"
        value={code}
        onChange={(e) => setCode(formatPairingCode(e.target.value))}
        placeholder="BDFG-HJKL"
        autoComplete="off"
        maxLength={9}
        className="w-full rounded-md border border-border bg-background px-3 py-2 text-center font-mono text-lg uppercase tracking-widest text-foreground"
      />
      {phase.message && <p className="text-center text-xs text-destructive-ink">{phase.message}</p>}
      <Button type="submit" className="w-full" disabled={normalizePairingCode(code).length !== 8}>
        Continue
      </Button>
    </form>,
  );
}
