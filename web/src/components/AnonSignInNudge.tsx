import { ArrowUpRight, ShieldCheck } from "lucide-react";
import { useNavigate } from "@tanstack/react-router";
import { Modal } from "./ui/Modal";
import { useAnonSignInNudge } from "@/hooks/useAnonSignInNudge";

/**
 * Anonymous-session sign-in nudge.
 *
 * Free-tier users run on anonymous Supabase sessions; their chats and
 * workspaces are tied to that browser session, so losing it loses their work.
 * This periodically (escalating backoff: 1h → 24h → 7d → 30d → every 30d)
 * prompts them to attach a real identity via the EXISTING upgrade flow.
 *
 * Self-contained: it runs the schedule hook and renders the modal, so it can be
 * mounted once at the app root (sibling of ModalLayer) and be evaluated
 * app-wide. It renders nothing for signed-in / non-anon sessions.
 *
 * The primary CTA routes into the existing /upgrade flow (UpgradeAccount), which
 * links a real identity onto the current anonymous account and honors returnTo.
 * "Later" dismisses and advances the backoff stage.
 */
export function AnonSignInNudge() {
  const navigate = useNavigate();
  const { open, dismiss, close } = useAnonSignInNudge();

  if (!open) return null;

  const handleSignIn = () => {
    // Close without advancing the stage — the user is acting on the prompt, not
    // deferring it. Route into the existing upgrade flow and bring them back to
    // wherever they were once an identity is attached.
    close();
    const returnTo = window.location.pathname + window.location.search;
    void navigate({ to: "/upgrade", search: { returnTo } });
  };

  return (
    <Modal
      isOpen={open}
      onClose={dismiss}
      title="Sign in so you don't lose your work"
      size="sm"
    >
      <div className="flex flex-col gap-4 p-6">
        <div className="flex items-start gap-3">
          <div className="rounded-full bg-primary/10 p-2 text-primary">
            <ShieldCheck className="h-5 w-5" />
          </div>
          <div className="flex-1 text-sm text-foreground">
            <p>
              Your chats and workspaces are tied to this browser session — sign
              in to keep them safe.
            </p>
          </div>
        </div>

        <div className="flex justify-end gap-2">
          <button
            type="button"
            onClick={dismiss}
            className="rounded-md border border-border px-4 py-2 text-sm text-foreground hover:bg-muted"
          >
            Later
          </button>
          <button
            type="button"
            onClick={handleSignIn}
            className="inline-flex items-center gap-1.5 rounded-md bg-primary px-4 py-2 text-sm font-medium text-primary-foreground hover:bg-primary/90"
          >
            Sign in
            <ArrowUpRight className="h-3.5 w-3.5" />
          </button>
        </div>
      </div>
    </Modal>
  );
}
