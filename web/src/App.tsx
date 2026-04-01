// Main App Entry Point
import { useEffect } from "react";
import { useViewerStore } from "./store/viewerStore";
import ModernApp from "./ModernApp";

function App() {
  // Clear all viewer state on mount/remount
  useEffect(() => {
    // Close all viewers and reset state IMMEDIATELY
    const viewerState = useViewerStore.getState();
    viewerState.closeAllViewers();
    viewerState.setActiveViewer(null);
  }, []);

  return <ModernApp />;
}

export default App;
