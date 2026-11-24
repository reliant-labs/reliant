import {
  AI_SETTINGS_TAB_IDS,
  BILLING_TAB_IDS,
  DEFAULT_SETTINGS_SECTION,
  isSettingsSection,
  type SettingsSection,
  type SettingsTab,
} from "../../routeSchemas";
import { settingsSectionHasOtherNavHome } from "./SettingsNavigation";

const STORAGE_KEY = "reliant.settings.lastDestination";

export interface SettingsDestination {
  section: SettingsSection;
  tab?: SettingsTab;
}

// Persist navigation, not a URL: checkout/OAuth results, returnTo and notices
// belong to the visit that supplied them and must never be replayed later.
//
// Only a page whose nav is Settings' own sections is a destination. Agent
// sandboxes (and one machine's page) is Agents › Sandboxes in the nav, so
// restoring it left Settings with no sections to pick from, on every later
// open. A saved one, from before, is read as no destination at all; an
// earlier `machineId` field is ignored for the same reason.
function destinationFrom(value: unknown): SettingsDestination | undefined {
  if (!value || typeof value !== "object") return undefined;
  const { section, tab } = value as Record<string, unknown>;
  if (typeof section !== "string" || !isSettingsSection(section)) return undefined;
  if (settingsSectionHasOtherNavHome(section)) return undefined;

  const destination: SettingsDestination = { section };
  const tabs: readonly string[] = section === "billing"
    ? BILLING_TAB_IDS
    : section === "ai" ? AI_SETTINGS_TAB_IDS : [];
  if (typeof tab === "string" && tabs.includes(tab)) {
    destination.tab = tab as SettingsTab;
  }
  return destination;
}

// Match the other browser-local UI preferences: storage may be unavailable
// (SecurityError), unreadable, or full. None of those should block Settings.
export function readSettingsDestination(): SettingsDestination {
  try {
    const saved = localStorage.getItem(STORAGE_KEY);
    if (saved) {
      const destination = destinationFrom(JSON.parse(saved));
      if (destination) return destination;
    }
  } catch {
    // An optional convenience, not a prerequisite for opening Settings.
  }
  return { section: DEFAULT_SETTINGS_SECTION };
}

/** Remember `destination`; a page that is not a destination keeps the one before it. */
export function rememberSettingsDestination(destination: SettingsDestination): void {
  const saved = destinationFrom(destination);
  if (!saved) return;
  try {
    localStorage.setItem(STORAGE_KEY, JSON.stringify(saved));
  } catch {
    // Keep explicit navigation working even when persistence is unavailable.
  }
}
