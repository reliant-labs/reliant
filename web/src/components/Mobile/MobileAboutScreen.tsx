/**
 * `/m/settings` → "About" section.
 *
 * Version display only. The desktop `AboutSection` bundles quick links, the
 * "Restart Onboarding Guide" action (which replays the desktop-chrome
 * spotlight tour `OnboardingWizard` suppresses on this surface — see
 * `MobileLayout`'s module comment), Electron CLI install, and
 * `UpdateSection` (an Electron auto-updater UI with no meaning in a mobile
 * browser tab). None of that is reimplemented here; this renders the same
 * `VersionDetails` block desktop does (this app's build, the server's, the
 * release) and a couple of links worth keeping on a phone.
 */

import { ExternalLink, Github, Globe } from "lucide-react";
import { BrandMark } from "../icons/BrandMark";
import { VersionDetails } from "../Settings/VersionDetails";
import { MobileCardGroup } from "./MobileChrome";
import { MobileSettingsSectionHeader } from "./MobileSettingsSectionHeader";

const LINKS = [
  { icon: Globe, label: "Website", href: "https://reliantlabs.io" },
  {
    icon: Github,
    label: "GitHub",
    href: "https://github.com/reliant-labs/reliant",
  },
];

export function MobileAboutScreen({ onBack }: { onBack: () => void }) {
  return (
    <div className="flex h-full min-h-0 flex-col">
      <MobileSettingsSectionHeader title="About" onBack={onBack} />
      <div className="min-h-0 flex-1 overflow-y-auto p-6 text-center">
        <BrandMark className="mx-auto mb-4 h-16 w-16" />
        <h1 className="text-2xl font-semibold text-foreground">Reliant</h1>
        <VersionDetails />

        <div className="mx-auto mt-6 max-w-sm text-left">
          <MobileCardGroup>
            {LINKS.map((link) => (
              <a
                key={link.href}
                href={link.href}
                target="_blank"
                rel="noopener noreferrer"
                className="flex min-h-[44px] items-center justify-between border-b border-border px-4 py-3 text-sm text-foreground/80 last:border-b-0 active:bg-foreground/5"
              >
                <span className="flex items-center gap-3">
                  <link.icon className="h-4 w-4 text-muted-foreground" />
                  {link.label}
                </span>
                <ExternalLink className="h-3.5 w-3.5 text-muted-foreground/50" />
              </a>
            ))}
          </MobileCardGroup>
        </div>

        <p className="mt-8 text-xs text-muted-foreground/60">
          © {new Date().getFullYear()} Reliant Labs
        </p>
      </div>
    </div>
  );
}
