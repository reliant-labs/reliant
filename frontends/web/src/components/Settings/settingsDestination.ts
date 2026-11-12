import {
  AI_SETTINGS_TAB_IDS,
  BILLING_TAB_IDS,
  DEFAULT_SETTINGS_SECTION,
  isSettingsSection,
  type SettingsSection,
  type SettingsTab,
} from "../../routeSchemas";

const STORAGE_KEY = "reliant.settings.lastDestination";

export interface SettingsDestination {
  section: SettingsSection;
  machineId?: string;
  tab?: SettingsTab;
}

// Persist navigation, not a URL: checkout/OAuth results, returnTo and notices
// belong to the visit that supplied them and must never be replayed later.
function destinationFrom(value: unknown): SettingsDestination | undefined {
  if (!value || typeof value !== "object") return undefined;
  const { section, machineId, tab } = value as Record<string, unknown>;
  if (typeof section !== "string" || !isSettingsSection(section)) return undefined;

  const destination: SettingsDestination = { section };
  if (section === "machines" && typeof machineId === "string" && machineId.trim()) {
    destination.machineId = machineId;
  }
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

export function rememberSettingsDestination(destination: SettingsDestination): void {
  try {
    localStorage.setItem(STORAGE_KEY, JSON.stringify(destinationFrom(destination)));
  } catch {
    // Keep explicit navigation working even when persistence is unavailable.
  }
}
