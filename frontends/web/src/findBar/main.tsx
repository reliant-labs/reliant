// The find bar's page (find-bar.html): the desktop app lays it over a window
// in a view of its own — see ./protocol.ts. Only the bar, the app's styles and
// the dev log mirror; none of the app's boot (config, auth, the router).
//
// FIRST, as in main.tsx: its console wrapping must precede every other module.
import "../lib/browser-log-boot";

import { createRoot } from "react-dom/client";
import "../index.css";
import { FindBar } from "./FindBar";

const root = document.getElementById("root");
const api = window.findBarAPI;

if (root && api) {
  createRoot(root).render(<FindBar api={api} />);
} else if (!api) {
  // Opened outside the desktop app's find-bar view, which supplies the API.
  console.error("[FindBar] window.findBarAPI is missing; this page only runs in the desktop app");
}
