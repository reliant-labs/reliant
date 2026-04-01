import { SettingsViewerTab } from "./SettingsViewerTab";

interface SettingsPageProps {
  initialSection?: string;
}

export function SettingsPage({ initialSection }: SettingsPageProps) {
  return (
    <div className="h-full w-full bg-background">
      <SettingsViewerTab initialSection={initialSection} />
    </div>
  );
}
