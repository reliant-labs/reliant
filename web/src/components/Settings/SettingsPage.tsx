import { useCallback, useEffect } from "react";
import { useNavigate, useParams } from "@tanstack/react-router";
import { SettingsHeader } from "./SettingsHeader";
import { SettingsViewerTab } from "./SettingsViewerTab";
import { useSettingsClose } from "@/hooks/useSettingsClose";
import {
  DEFAULT_SETTINGS_SECTION,
  SETTINGS_SECTION_IDS,
  type SettingsSection,
} from "../../routeSchemas";

function isSettingsSection(value: string | undefined): value is SettingsSection {
  return !!value && (SETTINGS_SECTION_IDS as readonly string[]).includes(value);
}

export function SettingsPage() {
  // Escape closes the page (matches the WorkflowBuilderPage convention). The
  // arrow-key section navigator inside SettingsViewerTab also listens with
  // capture, so we don't fight it: this handler only fires for Escape.
  // `section` is the URL — bare /settings yields no param and falls back to
  // DEFAULT_SETTINGS_SECTION. /settings/$section provides it. Anything that
  // isn't a known section gets coerced to the default (could redirect, but
  // letting it render the default is friendlier than bouncing the URL).
  const params = useParams({ strict: false }) as { section?: string };
  const navigate = useNavigate();
  const onClose = useSettingsClose();

  const section: SettingsSection = isSettingsSection(params.section)
    ? params.section
    : DEFAULT_SETTINGS_SECTION;

  const onSectionChange = useCallback(
    (next: SettingsSection) => {
      navigate({ to: "/settings/$section", params: { section: next } });
    },
    [navigate],
  );

  useEffect(() => {
    const handleKeyDown = (e: KeyboardEvent) => {
      if (e.key !== "Escape") return;
      const target = e.target as HTMLElement;
      if (
        target.tagName === "INPUT" ||
        target.tagName === "TEXTAREA" ||
        target.contentEditable === "true"
      ) {
        return;
      }
      e.preventDefault();
      e.stopPropagation();
      onClose();
    };
    window.addEventListener("keydown", handleKeyDown, true);
    return () => window.removeEventListener("keydown", handleKeyDown, true);
  }, [onClose]);

  return (
    <div className="flex h-screen w-full flex-col bg-background">
      <SettingsHeader onClose={onClose} />
      <div className="flex-1 overflow-hidden">
        <SettingsViewerTab section={section} onSectionChange={onSectionChange} />
      </div>
    </div>
  );
}
