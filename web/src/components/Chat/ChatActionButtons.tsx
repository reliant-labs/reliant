import { useChatButtons } from "./useChatButtons";
import { CollapsibleButtonGroup } from "./CollapsibleButtonGroup";

interface ChatActionButtonsProps {
  // Send/Stop
  onSend: () => void;
  onStop?: () => void;
  canSend: boolean;
  isStreaming: boolean;
  disabled: boolean;

  // File actions
  onAttach: () => void;
  uploading: boolean;

  // Recent changes
  onToggleRecentChanges?: () => void;
  isRecentChangesOpen?: boolean;
  hasWorktree?: boolean;

  // Compact
  onCompact?: () => void;
  isCompacting?: boolean;

  // Dev mode
  isDev?: boolean;
  forceStreaming?: boolean;
  onToggleForceStreaming?: () => void;

  // Discuss
  isDiscussMode?: boolean;
  canDiscuss?: boolean;
  onToggleDiscuss?: () => void;
  isPaused?: boolean;

  // Prompts element (passed as ReactNode)
  promptsElement?: React.ReactNode;

  // Responsiveness
  compact?: boolean;
}

interface ButtonLayoutProps extends ChatActionButtonsProps {
  layout?: string[]; // Array of button names to control order/grouping
}

export function ChatActionButtons(props: ChatActionButtonsProps) {
  // Minimal layout - only attach, more menu (with everything else), and send
  const defaultLayout = [
    "attach", "prompts", "recentChanges", "compact", "devTool", "discuss", "sendStop"
  ];

  return <ButtonLayout {...props} layout={defaultLayout} />;
}

// Flexible layout component that you can use with custom arrangements
export function ButtonLayout({
  layout = ["tasks", "plans", "devTool", "attach", "sendStop"],
  ...props
}: ButtonLayoutProps) {
  const { buttons } = useChatButtons(props);

  // Always keep send/stop button on the right
  const sendButtonNames = ["sendStop", "send", "stop"];
  const sendButton = layout.find(name => sendButtonNames.includes(name));

  // Keep only attach always visible (not in collapsible group)
  const alwaysVisibleNames = ["attach"];
  const rightOfMenuNames = ["discuss"];
  const alwaysVisibleButtons = layout.filter(name => alwaysVisibleNames.includes(name) && !sendButtonNames.includes(name));
  const collapsibleButtons = layout.filter(name => !alwaysVisibleNames.includes(name) && !sendButtonNames.includes(name) && !rightOfMenuNames.includes(name));

  // Define priority for collapsible buttons (higher priority = shown first when collapsing)
  const buttonPriority = {
    prompts: 7,      // High priority - prompts selector
    recentChanges: 6, // Medium-high priority - recent changes
    compact: 5,      // Medium priority - compact context
    devTool: 4,      // Medium priority - dev only
    divider: 1,      // Lowest priority
  };

  // Sort collapsible buttons by priority while maintaining relative order for same priority
  const sortedCollapsibleButtons = [...collapsibleButtons].sort((a, b) => {
    const aPriority = buttonPriority[a as keyof typeof buttonPriority] || 0;
    const bPriority = buttonPriority[b as keyof typeof buttonPriority] || 0;
    if (aPriority !== bPriority) {
      return bPriority - aPriority; // High priority first
    }
    return collapsibleButtons.indexOf(a) - collapsibleButtons.indexOf(b); // Maintain original order for same priority
  });

  const alwaysVisibleElements = alwaysVisibleButtons.map((buttonName) => buttons[buttonName]).filter(Boolean);
  const collapsibleElements = sortedCollapsibleButtons.map((buttonName) => {
    // Handle prompts as a special case - render the provided element
    if (buttonName === "prompts" && props.promptsElement) {
      return props.promptsElement;
    }
    return buttons[buttonName];
  }).filter(Boolean) as React.ReactElement[];
  const rightOfMenuElements = rightOfMenuNames.map((buttonName) => buttons[buttonName]).filter(Boolean);
  const sendButtonElement = sendButton ? buttons[sendButton] : null;

  return (
    <div className="flex items-center gap-1.5 flex-shrink-0">
      {alwaysVisibleElements}
      <CollapsibleButtonGroup maxVisibleButtons={0} compact={props.compact}>
        {collapsibleElements}
      </CollapsibleButtonGroup>
      {rightOfMenuElements}
      {sendButtonElement}
    </div>
  );
}