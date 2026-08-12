import { useChatButtons } from "./useChatButtons";

interface ChatActionButtonsProps {
  // Send/Stop
  onSend: () => void;
  onStop?: () => void;
  onQueue?: () => void;
  canSend: boolean;
  isStreaming: boolean;
  disabled: boolean;

  // File actions
  onAttach: () => void;
  uploading: boolean;

  // Discuss
  isDiscussMode?: boolean;
  onToggleDiscuss?: () => void;
  isPaused?: boolean;

  // Responsiveness
  compact?: boolean;
}

interface ButtonLayoutProps extends ChatActionButtonsProps {
  layout?: string[]; // Array of button names to control order/grouping
}

export function ChatActionButtons(props: ChatActionButtonsProps) {
  const defaultLayout = [
    "attach", "discuss", "sendStop"
  ];

  return <ButtonLayout {...props} layout={defaultLayout} />;
}

// Flexible layout component that you can use with custom arrangements
export function ButtonLayout({
  layout = ["tasks", "plans", "attach", "sendStop"],
  ...props
}: ButtonLayoutProps) {
  const { buttons } = useChatButtons(props);

  // Always keep send/stop button on the right. `queueSend` is rendered just
  // before it, so the Stop affordance never shifts position.
  const sendButtonNames = ["sendStop", "send", "stop", "queueSend"];
  const sendButton = layout.find(name => sendButtonNames.includes(name) && name !== "queueSend");

  const alwaysVisibleNames = ["attach"];
  const trailingActionNames = ["discuss"];
  const alwaysVisibleButtons = layout.filter(name => alwaysVisibleNames.includes(name) && !sendButtonNames.includes(name));
  const inlineActionButtons = layout.filter(name => !alwaysVisibleNames.includes(name) && !sendButtonNames.includes(name) && !trailingActionNames.includes(name));

  const buttonPriority = {
    divider: 1,
  };

  const sortedInlineActionButtons = [...inlineActionButtons].sort((a, b) => {
    const aPriority = buttonPriority[a as keyof typeof buttonPriority] || 0;
    const bPriority = buttonPriority[b as keyof typeof buttonPriority] || 0;
    if (aPriority !== bPriority) {
      return bPriority - aPriority;
    }
    return inlineActionButtons.indexOf(a) - inlineActionButtons.indexOf(b);
  });

  const alwaysVisibleElements = alwaysVisibleButtons.map((buttonName) => buttons[buttonName]).filter(Boolean);
  const inlineActionElements = sortedInlineActionButtons.map((buttonName) => buttons[buttonName]).filter(Boolean) as React.ReactElement[];
  const trailingActionElements = trailingActionNames.map((buttonName) => buttons[buttonName]).filter(Boolean);
  const queueButtonElement = buttons["queueSend"] ?? null;
  const sendButtonElement = sendButton ? buttons[sendButton] : null;

  return (
    <div className="flex items-center gap-1.5 flex-shrink-0">
      {alwaysVisibleElements}
      {inlineActionElements}
      {trailingActionElements}
      {queueButtonElement}
      {sendButtonElement}
    </div>
  );
}