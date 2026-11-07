/**
 * Every command on the download / connect surfaces has an always-visible copy
 * button (owner, 2026-11-06). Surfaces with their own heavy setup assert this
 * in their own files (PairMachinePanel, LocalModelsSection, TokenSettings,
 * MobileAIProvidersPanel); the shared leaf panels are rendered here, and a
 * source scan below keeps a bare command from coming back in any surface of
 * the inventory.
 */
import { render } from "@testing-library/react";
import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { afterEach, describe, expect, it, vi } from "vitest";

import { expectEveryCommandCopyable } from "@/test/commandBlocks";
import { OAuthHelperPanel } from "../OAuthHelperPanel";
import { ReliantDownloadOptions } from "../ReliantDownloadOptions";

function setPlatform(platform: string) {
  Object.defineProperty(navigator, "platform", { value: platform, configurable: true });
}

afterEach(() => setPlatform(""));

describe("setup surfaces: every command is copyable", () => {
  it.each(["MacIntel", "Win32", "Linux x86_64", ""])("ReliantDownloadOptions on %s", (platform) => {
    setPlatform(platform);
    const { container } = render(<ReliantDownloadOptions />);
    // macOS and unknown platforms show the Homebrew command; the others none.
    expectEveryCommandCopyable(container, { atLeast: platform === "MacIntel" ? 1 : 0 });
  });

  it("OAuthHelperPanel when the sign-in helper is not running", () => {
    setPlatform("MacIntel");
    const { container } = render(
      <OAuthHelperPanel providerName="Claude" available={false} loading={false} connecting={false} onRetry={vi.fn()} onConnect={vi.fn()} />,
    );
    // The Homebrew install and `reliant auth serve`.
    expectEveryCommandCopyable(container, { atLeast: 2 });
  });
});

/**
 * The inventory (PR body). A surface listed here shows install or connect
 * instructions; a command literal in it must go through CommandBlock (or a
 * component that renders one), never a bare <code>/<pre>/font-mono line.
 */
const SRC = join(dirname(fileURLToPath(import.meta.url)), "..", "..");
const INVENTORY = [
  "components/OnboardingFlow/steps/ComputeStep.tsx",
  "components/OnboardingFlow/steps/ModelStep.tsx",
  "components/Machines/PairMachinePanel.tsx",
  "components/Machines/AddMachineSheet.tsx",
  "components/Settings/cloud/machines.tsx",
  "components/Settings/providers/ConnectPanel.tsx",
  "components/Settings/providers/ProvidersSection.tsx",
  "components/Settings/LocalModelsSection.tsx",
  "components/Settings/TokenSettings.tsx",
  "components/ApiKeySetupModal.tsx",
  "components/OAuthHelperPanel.tsx",
  "components/CopilotDevicePanel.tsx",
  "components/ReliantDownloadOptions.tsx",
  "components/NoMachineState.tsx",
  "components/DaemonWaitState.tsx",
  "components/Chat/ConnectMachineDialog.tsx",
  "components/Mobile/MobileAIProvidersPanel.tsx",
  "components/Mobile/MobileDaemonList.tsx",
];

const BARE_COMMAND =
  /<(code|pre)\b[^>]*>\s*\{?\s*["'`]?(?:[A-Z_]+=\S+\s+)*(?:echo\s[^<]*\|\s*)?(?:reliant|brew|ollama|curl)\s+[a-z-]+[^<]*<\/\1>|className="[^"]*\bfont-mono\b[^"]*">\s*(?:echo\s[^<]*\|\s*)?(?:reliant|brew|ollama|curl)\s+[a-z-]+/;

describe("setup surface inventory", () => {
  it.each(INVENTORY)("%s shows no command outside a CommandBlock", (file) => {
    const source = readFileSync(join(SRC, file), "utf8");
    expect(source.match(BARE_COMMAND)?.[0] ?? null).toBeNull();
  });
});
