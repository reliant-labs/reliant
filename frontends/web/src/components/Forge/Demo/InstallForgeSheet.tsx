// Copyright (c) 2025 Reliant Labs

/**
 * "Install forge", from the public demo's banner and end card (DemoCta): the
 * commands from forge's README, each with a Copy button that records
 * `demo_install_copied{method}`.
 */

import Modal from "@/components/forge-ui/modal";
import { TourCommand } from "@/components/Onboarding/journeys/TourCommand";
import { trackEvent } from "@/lib/analytics";

const QUICK_START_URL = "https://docs.reliantlabs.io/forge/quick-start";

export const INSTALL_COMMANDS = [
  { method: "curl", command: "curl -fsSL https://raw.githubusercontent.com/reliant-labs/forge/main/install.sh | sh" },
  { method: "brew", command: "brew install reliant-labs/reliant/forge" },
  { method: "start", command: "forge start" },
] as const;

export function InstallForgeSheet({ open, onClose }: { open: boolean; onClose: () => void }) {
  return (
    <Modal open={open} onClose={onClose} size="md" title="Install forge" description="Installs one binary. forge start takes an empty directory to a running app with this console.">
      <div className="space-y-3" data-testid="demo-install-sheet">
        <p className="text-sm text-ink-muted">With the install script, or Homebrew:</p>
        <div className="space-y-1.5">
          {INSTALL_COMMANDS.slice(0, 2).map(({ method, command }) => (
            <TourCommand key={method} command={command} onCopied={() => trackEvent("demo_install_copied", { method })} />
          ))}
        </div>
        <p className="text-sm text-ink-muted">Then, in an empty directory:</p>
        <TourCommand command={INSTALL_COMMANDS[2].command} onCopied={() => trackEvent("demo_install_copied", { method: "start" })} />
        <p className="text-sm">
          <a href={QUICK_START_URL} target="_blank" rel="noopener noreferrer" className="font-medium text-primary-ink underline-offset-2 hover:underline">
            Quick start
          </a>
        </p>
      </div>
    </Modal>
  );
}
