// Main App Entry Point
import ModernApp from "./ModernApp";

function App() {
  // Viewer lifecycle belongs to project/workspace selection, not this route's
  // mount: global Runs and Settings temporarily unmount the chat app.
  return <ModernApp />;
}

export default App;
