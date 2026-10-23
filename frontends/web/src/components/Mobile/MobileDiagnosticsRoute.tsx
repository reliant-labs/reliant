/**
 * `/m/diagnostics` — the Diagnostics screen at a URL of its own, so support
 * can send a link to it. Its back control goes to Settings, where the same
 * screen is listed.
 */

import { useNavigate } from "@tanstack/react-router";
import { MobileDiagnosticsScreen } from "./MobileDiagnosticsScreen";

export function MobileDiagnosticsRoute() {
  const navigate = useNavigate();
  return <MobileDiagnosticsScreen onBack={() => void navigate({ to: "/m/settings" })} />;
}
