import { ExternalLink } from "lucide-react";
import { usePrivacyConsent, useUpdatePrivacySettings } from "../../hooks/privacy-queries";
import { hasControlPlane } from "../../services/controlPlane/config";
import ToggleSwitch from "../forge-ui/toggle_switch";
import { SettingsPageHeader } from "./SettingsPageHeader";
import { PRIVACY_DOCS_URL, PRIVACY_POLICY_URL } from "./privacyLinks";


/**
 * Where a user's data lives, in four plain sentences, and the two switches
 * that are actually theirs to flip.
 *
 * The page used to say code and chats are "always stored locally". That is
 * false for the cloud product: the servers run the agent loop and keep every
 * chat, and a cloud machine holds the whole repository. These sentences
 * follow docs/help/privacy (the flow diagram and the "What goes where"
 * table there), so they stay true with a local daemon, a cloud machine and a
 * self-hosted server alike.
 *
 * Turning a switch off is a choice, not a fault, so nothing here warns about
 * it; the switch's own state says what is on.
 */
export function PrivacySettings() {
  const { crashReportingEnabled, analyticsEnabled } = usePrivacyConsent();
  const { setCrashReporting, setAnalytics } = useUpdatePrivacySettings();
  const servers = hasControlPlane ? "Reliant's servers" : "your Reliant server";

  return (
    <div className="space-y-6">
      <SettingsPageHeader
        title="Privacy"
        description="Choose whether Reliant sends us crash reports and usage analytics. Both are on until you turn them off, and a change applies immediately."
      />

      <div className="space-y-4">
        <div className="flex items-start justify-between p-4 rounded-lg border border-border bg-card">
          <div className="flex-1 pr-4">
            <h3 className="mb-1 text-sm font-semibold">Crash and error reporting</h3>
            <p className="text-xs text-muted-foreground">
              Send crash reports and error logs, including stack traces, so we can find and fix problems. You are
              identified by an internal ID, not your name or email.
            </p>
          </div>
          <ToggleSwitch
            checked={crashReportingEnabled}
            onChange={setCrashReporting}
            ariaLabel="Crash and error reporting"
            size="lg"
            className="forge-ui"
          />
        </div>

        <div className="flex items-start justify-between p-4 rounded-lg border border-border bg-card">
          <div className="flex-1 pr-4">
            <h3 className="mb-1 text-sm font-semibold">Analytics and usage data</h3>
            <p className="text-xs text-muted-foreground">
              Send product usage events, tied to your account, so we can see which features are used and improve them.
              Events record what you did, not what you wrote.
            </p>
          </div>
          <ToggleSwitch
            checked={analyticsEnabled}
            onChange={setAnalytics}
            ariaLabel="Analytics and usage data"
            size="lg"
            className="forge-ui"
          />
        </div>

        <section
          aria-labelledby="privacy-where"
          className="space-y-3 rounded-lg border border-border/60 bg-background p-4 text-xs text-muted-foreground"
        >
          <h3 id="privacy-where" className="text-sm font-semibold text-foreground">
            Where your data lives
          </h3>
          <p>
            <strong className="text-foreground">Chats are stored on {servers}, which run your agents.</strong> Your
            prompts, the model's replies, and anything an agent reads or a command prints are saved with the chat and
            sent to your model provider.
          </p>
          <p>
            <strong className="text-foreground">Your code stays on the machine it runs on.</strong> On your own
            computer, files an agent never opens stay there. A Reliant cloud machine keeps the whole repository on its
            disk in our cloud.
          </p>
          <p>
            <strong className="text-foreground">These switches don't change any of that.</strong> They only control
            crash reports and usage analytics. Server logs and model usage records (token counts, never content) are
            kept either way.
          </p>
          <p className="flex flex-wrap gap-x-4 gap-y-1 pt-1">
            <a
              href={PRIVACY_DOCS_URL}
              target="_blank"
              rel="noopener noreferrer"
              className="inline-flex items-center gap-1 font-medium text-primary-ink hover:underline"
            >
              What is stored where
              <ExternalLink className="h-3 w-3" aria-hidden="true" />
            </a>
            <a
              href={PRIVACY_POLICY_URL}
              target="_blank"
              rel="noopener noreferrer"
              className="inline-flex items-center gap-1 font-medium text-primary-ink hover:underline"
            >
              Privacy policy
              <ExternalLink className="h-3 w-3" aria-hidden="true" />
            </a>
          </p>
        </section>
      </div>
    </div>
  );
}
