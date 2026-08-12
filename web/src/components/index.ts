// Layout components
export { Header } from "./Layout/Header";
export { ResizableSidebar } from "./Layout/ResizableSidebar";
export { NavigationBar } from "./Layout/NavigationBar";
export { LoadingSpinner } from "./Layout/LoadingSpinner";

// Chat components
export { ChatInterface } from "./Chat/ChatInterface";
export { ChatMessage } from "./Chat/ChatMessage";
export { ChatInput } from "./Chat/ChatInput";
export { ToolExecution } from "./Chat/ToolExecution";
export { NewChatView } from "./Chat/NewChatView";
export { ErrorMessage } from "./Chat/ErrorMessage";
export { WorktreeSelector } from "./Chat/WorktreeSelector";
export { MarkdownRenderer } from "./Chat/MarkdownRenderer";
export { PermissionsPanel } from "./Chat/PermissionsPanel";

// Project components
export { ProjectPicker } from "./Projects/ProjectPicker";
export { InitializationModal } from "./Projects/InitializationModal";
export { RescanModal } from "./Projects/RescanModal";
export { ProjectPickerModal } from "./Projects/ProjectPickerModal";

// Settings components
export { SettingsNavigation } from "./Settings/SettingsNavigation";
export { SettingsContent } from "./Settings/SettingsContent";
export { SettingsHeader } from "./Settings/SettingsHeader";
export { SettingsPage } from "./Settings/SettingsPage";
export { CombinedGeneralSettings } from "./Settings/CombinedGeneralSettings";
export { ProjectSettings } from "./Settings/ProjectSettings";

// Worktree components
export { WorktreesPanel } from "./Worktrees/WorktreesPanel";
export { WorktreeDetailView } from "./Worktrees/WorktreeDetailView";

// Terminal components
export { Terminal } from "./Terminal/Terminal";
export { ResizableTerminalPanel } from "./Terminal/ResizableTerminalPanel";
// FileBrowser components
// Note: FileBrowser component removed - use RightSidebar instead

// UI components
export { Modal } from "./ui/Modal";
export { ErrorAlert } from "./ui/ErrorAlert";

// Error Boundaries
export { SentryErrorBoundary } from "./ErrorBoundary";
export { SectionErrorBoundary } from "./SectionErrorBoundary";

// App components
export { AppInitializer } from "./AppInitializer";